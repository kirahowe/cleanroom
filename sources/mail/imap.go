package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"
)

const (
	imapTimeout = 30 * time.Second
	// maxFetchSize bounds how much of one message is fetched. Text parts
	// usually come before attachments, so the cut tends to fall in an
	// attachment.
	maxFetchSize = 4 << 20
	maxSecretLen = 4 << 10
)

var (
	imapIDPattern = regexp.MustCompile(`^([0-9]{1,10})-([0-9]{1,10})$`)

	errStaleID  = errors.New("message not found: the mailbox was rebuilt since this id was issued, search again")
	errNotFound = errors.New("message not found")
)

// imapBox reads one mailbox of an IMAP account. It has no way to send, and
// it cannot change the mailbox either: it opens the mailbox with EXAMINE,
// which is read-only, and fetches with BODY.PEEK, which leaves a message
// unread.
//
// It keeps one connection and uses it for one call at a time. Servers drop
// idle connections, so a call that fails on a connection left over from an
// earlier call is tried once more on a new one. That is safe because every
// call only reads.
type imapBox struct {
	opts     imapOptions
	password string
	tls      *tls.Config

	mu       sync.Mutex
	client   *imapclient.Client
	selected *imap.SelectData
}

// newIMAP reads the password and certificate files named in opts. It does
// not connect.
func newIMAP(opts imapOptions) (*imapBox, error) {
	password, err := readSecret(opts.passwordFile)
	if err != nil {
		return nil, err
	}
	tlsConfig := &tls.Config{ServerName: opts.host, MinVersion: tls.VersionTLS12}
	if opts.caFile != "" {
		pem, err := os.ReadFile(opts.caFile)
		if err != nil {
			return nil, err
		}
		tlsConfig.RootCAs = x509.NewCertPool()
		if !tlsConfig.RootCAs.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s holds no PEM certificate", opts.caFile)
		}
	}
	return &imapBox{opts: opts, password: password, tls: tlsConfig}, nil
}

// readSecret reads a password from a file that only its owner can read.
func readSecret(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s can be read by other users; run chmod 600 on it", path)
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxSecretLen))
	if err != nil {
		return "", err
	}
	secret := strings.TrimRight(string(raw), "\r\n")
	if secret == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return secret, nil
}

func (b *imapBox) QuerySyntax() string { return imapQuerySyntax }

// Search returns the newest matches. A UID grows with each message added
// to a mailbox, so the highest UIDs are the most recently arrived.
func (b *imapBox) Search(ctx context.Context, query string, limit int) ([]Summary, error) {
	criteria, err := parseQuery(query)
	if err != nil {
		return nil, err
	}
	var out []Summary
	err = b.do(ctx, func(c *imapclient.Client, validity uint32) error {
		out = nil
		found, err := c.UIDSearch(criteria, nil).Wait()
		if err != nil {
			return err
		}
		uids := found.AllUIDs()
		slices.Sort(uids)
		slices.Reverse(uids)
		uids = uids[:min(limit, len(uids))]
		if len(uids) == 0 {
			return nil
		}
		msgs, err := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true, Envelope: true, InternalDate: true}).Collect()
		if err != nil {
			return err
		}
		slices.SortFunc(msgs, func(a, b *imapclient.FetchMessageBuffer) int { return int(int64(b.UID) - int64(a.UID)) })
		for _, m := range msgs {
			out = append(out, summarize(validity, m))
		}
		return nil
	})
	return out, err
}

func summarize(validity uint32, m *imapclient.FetchMessageBuffer) Summary {
	s := Summary{ID: imapID(validity, m.UID), To: []string{}}
	if !m.InternalDate.IsZero() {
		s.Date = m.InternalDate.UTC().Format(time.RFC3339)
	}
	if env := m.Envelope; env != nil {
		s.Subject = env.Subject
		if len(env.From) > 0 {
			s.From = displayAddress(env.From[0].Name, env.From[0].Addr())
		}
		for _, a := range env.To {
			s.To = append(s.To, displayAddress(a.Name, a.Addr()))
		}
	}
	return s
}

// Read fetches one message without marking it read.
func (b *imapBox) Read(ctx context.Context, id string) (Message, error) {
	validity, uid, err := parseIMAPID(id)
	if err != nil {
		return Message{}, err
	}
	section := &imap.FetchItemBodySection{Peek: true, Partial: &imap.SectionPartial{Size: maxFetchSize}}
	var raw []byte
	err = b.do(ctx, func(c *imapclient.Client, current uint32) error {
		if validity != current {
			return errStaleID
		}
		msgs, err := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{
			UID: true, BodySection: []*imap.FetchItemBodySection{section},
		}).Collect()
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			return errNotFound
		}
		raw = msgs[0].FindBodySection(section)
		return nil
	})
	if err != nil {
		return Message{}, err
	}
	msg, err := parseMessage(bytes.NewReader(raw))
	msg.ID = id
	return msg, err
}

// Count connects if needed and reports how many messages the mailbox held
// when it was opened.
func (b *imapBox) Count(ctx context.Context) (uint32, error) {
	var n uint32
	err := b.do(ctx, func(*imapclient.Client, uint32) error {
		n = b.selected.NumMessages
		return nil
	})
	return n, err
}

// imapID names a message for the read tool. A UID alone is not enough: the
// server may renumber a mailbox, and it announces that by changing the
// mailbox's UIDVALIDITY. Carrying that value in the id means an id from
// before a renumbering is refused and never fetches a different message.
func imapID(validity uint32, uid imap.UID) string {
	return fmt.Sprintf("%d-%d", validity, uid)
}

func parseIMAPID(id string) (validity uint32, uid imap.UID, err error) {
	m := imapIDPattern.FindStringSubmatch(id)
	if m == nil {
		return 0, 0, errors.New("invalid message id")
	}
	v, errV := strconv.ParseUint(m[1], 10, 32)
	u, errU := strconv.ParseUint(m[2], 10, 32)
	if errV != nil || errU != nil || u == 0 {
		return 0, 0, errors.New("invalid message id")
	}
	return uint32(v), imap.UID(u), nil
}

// do runs fn with a connection that is logged in and has the mailbox open,
// and with that mailbox's UIDVALIDITY.
func (b *imapBox) do(ctx context.Context, fn func(c *imapclient.Client, validity uint32) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, imapTimeout)
	defer cancel()

	reused := b.client != nil
	err := b.attempt(ctx, fn)
	if err != nil && reused && b.client == nil && ctx.Err() == nil {
		err = b.attempt(ctx, fn)
	}
	return err
}

// attempt runs fn once. It closes the connection when fn fails for a
// reason other than the server or fn saying no, so that the next attempt
// starts on a new one.
func (b *imapBox) attempt(ctx context.Context, fn func(*imapclient.Client, uint32) error) error {
	if b.client == nil {
		if err := b.connect(ctx); err != nil {
			return err
		}
	}
	// The library's calls take no context, so closing the connection is
	// how one is interrupted.
	client := b.client
	stop := context.AfterFunc(ctx, func() { client.Close() })
	err := fn(client, b.selected.UIDValidity)
	if !stop() {
		b.drop()
		return fmt.Errorf("the mail server did not answer in time: %w", ctx.Err())
	}
	var refusal *imap.Error
	switch {
	case err == nil, errors.Is(err, errStaleID), errors.Is(err, errNotFound):
		return err
	case errors.As(err, &refusal):
		return fmt.Errorf("the mail server refused the request: %w", err)
	}
	b.drop()
	return fmt.Errorf("talking to the mail server: %w", err)
}

func (b *imapBox) drop() {
	if b.client != nil {
		b.client.Close()
	}
	b.client, b.selected = nil, nil
}

// connect dials, logs in and opens the mailbox read-only. TLS is always
// used, from the first byte or by STARTTLS before the login.
func (b *imapBox) connect(ctx context.Context) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", b.opts.addr)
	if err != nil {
		return fmt.Errorf("connecting to the mail server: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	options := &imapclient.Options{
		TLSConfig:   b.tls,
		WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader},
	}
	var client *imapclient.Client
	if b.opts.startTLS {
		if client, err = imapclient.NewStartTLS(conn, options); err != nil {
			return fmt.Errorf("starting TLS with the mail server: %w", err)
		}
	} else {
		client = imapclient.New(tls.Client(conn, b.tls), options)
	}
	if err := client.Login(b.opts.user, b.password).Wait(); err != nil {
		client.Close()
		return fmt.Errorf("logging in to the mail server: %w", err)
	}
	selected, err := client.Select(b.opts.mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		client.Close()
		return fmt.Errorf("opening mailbox %q: %w", b.opts.mailbox, err)
	}
	b.client, b.selected = client, selected
	return nil
}
