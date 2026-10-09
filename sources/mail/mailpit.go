package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	httpTimeout     = 10 * time.Second
	smtpTimeout     = 15 * time.Second
	maxResponseSize = 4 << 20
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// mailpit reads through Mailpit's HTTP API and sends over SMTP.
type mailpit struct {
	baseURL  *url.URL
	smtpAddr string
	from     *mail.Address
	client   *http.Client
	now      func() time.Time
}

func newMailpit(baseURL *url.URL, smtpAddr string, from *mail.Address) *mailpit {
	return &mailpit{
		baseURL:  baseURL,
		smtpAddr: smtpAddr,
		from:     from,
		client:   &http.Client{Timeout: httpTimeout},
		now:      time.Now,
	}
}

// Mailpit API shapes, limited to the fields we use.
type mailpitAddress struct {
	Name    string
	Address string
}

func (a mailpitAddress) String() string {
	if a.Name == "" {
		return a.Address
	}
	return (&mail.Address{Name: a.Name, Address: a.Address}).String()
}

func addressList(list []mailpitAddress) []string {
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, a.String())
	}
	return out
}

type searchResponse struct {
	Messages []struct {
		ID      string
		From    mailpitAddress
		To      []mailpitAddress
		Subject string
		Created string
		Snippet string
	}
}

type messageResponse struct {
	ID      string
	From    mailpitAddress
	To      []mailpitAddress
	Subject string
	Date    string
	Text    string
	HTML    string
}

func (m *mailpit) Search(ctx context.Context, query string, limit int) ([]Summary, error) {
	params := url.Values{"query": {query}, "limit": {strconv.Itoa(limit)}}
	var resp searchResponse
	if err := m.get(ctx, "/api/v1/search?"+params.Encode(), &resp); err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(resp.Messages))
	for _, r := range resp.Messages {
		out = append(out, Summary{
			ID:      r.ID,
			From:    r.From.String(),
			To:      addressList(r.To),
			Subject: r.Subject,
			Date:    rfc3339(r.Created),
			Snippet: r.Snippet,
		})
	}
	return out, nil
}

// Read fetches one message. Mailpit marks a message read when it is fetched
// by ID; see the README.
func (m *mailpit) Read(ctx context.Context, id string) (Message, error) {
	if !idPattern.MatchString(id) {
		return Message{}, errors.New("invalid message id")
	}
	var resp messageResponse
	if err := m.get(ctx, "/api/v1/message/"+id, &resp); err != nil {
		return Message{}, err
	}
	text := resp.Text
	if strings.TrimSpace(text) == "" {
		text = stripHTML(resp.HTML)
	}
	return Message{
		ID:      resp.ID,
		From:    resp.From.String(),
		To:      addressList(resp.To),
		Subject: resp.Subject,
		Date:    rfc3339(resp.Date),
		Text:    text,
	}, nil
}

func (m *mailpit) get(ctx context.Context, pathAndQuery string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.baseURL.String()+pathAndQuery, nil)
	if err != nil {
		return err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("mailpit request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return fmt.Errorf("reading mailpit response: %w", err)
	}
	if len(body) > maxResponseSize {
		return errors.New("mailpit response is too large")
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errors.New("message not found")
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("mailpit returned %s", resp.Status)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("decoding mailpit response: %w", err)
	}
	return nil
}

// Send delivers msg over plain SMTP, with no authentication and no TLS.
func (m *mailpit) Send(ctx context.Context, msg Outgoing) error {
	id, err := newMessageID(m.from)
	if err != nil {
		return err
	}
	data, err := msg.build(m.from, m.now(), id)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, smtpTimeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", m.smtpAddr)
	if err != nil {
		return fmt.Errorf("connecting to smtp: %w", err)
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	// Close the connection if the context ends first, so blocked reads fail.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	host, _, _ := net.SplitHostPort(m.smtpAddr)
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("smtp greeting: %w", err)
	}
	if err := c.Mail(m.from.Address); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	for _, rcpt := range msg.To {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp RCPT TO %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	return c.Quit()
}

var (
	blockTags = regexp.MustCompile(`(?is)<(script|style|head)\b.*?</(script|style|head)\s*>`)
	lineTags  = regexp.MustCompile(`(?i)<(br|/p|/div|/li|/tr|/h[1-6])\b[^>]*>`)
	anyTag    = regexp.MustCompile(`(?s)<[^>]*>`)
	spaces    = regexp.MustCompile(`[ \t\r\f\v]+`)
	blankRuns = regexp.MustCompile(`\n\s*\n\s*`)
)

// stripHTML is a minimal HTML to text conversion for messages that have no
// plain text part. It is not a sanitizer and its output is only ever shown
// as text.
func stripHTML(s string) string {
	s = blockTags.ReplaceAllString(s, "")
	s = lineTags.ReplaceAllString(s, "\n")
	s = anyTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = spaces.ReplaceAllString(s, " ")
	s = blankRuns.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// rfc3339 normalizes a timestamp from Mailpit, or returns it unchanged if
// it does not parse.
func rfc3339(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.UTC().Format(time.RFC3339)
}
