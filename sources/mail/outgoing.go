package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits on outgoing mail.
const (
	maxBodyBytes    = 100 * 1024
	maxSubjectBytes = 200
	maxRecipients   = 20
)

// Outgoing is a validated message ready to send. Build it with newOutgoing;
// the zero value is not safe to send.
type Outgoing struct {
	To      []string // bare addresses, printable ASCII only
	Subject string   // no control characters
	Body    string   // valid UTF-8, no NUL
}

// newOutgoing validates the tool arguments. It rejects anything that could
// inject a header or an SMTP command, so later stages can write the values
// without further escaping beyond header encoding.
func newOutgoing(to []string, subject, body string) (Outgoing, error) {
	if len(to) == 0 {
		return Outgoing{}, errors.New("at least one recipient is required")
	}
	if len(to) > maxRecipients {
		return Outgoing{}, fmt.Errorf("at most %d recipients are allowed", maxRecipients)
	}
	out := Outgoing{Subject: subject, Body: body}
	for _, raw := range to {
		addr, err := parseAddress(raw)
		if err != nil {
			return Outgoing{}, err
		}
		out.To = append(out.To, addr)
	}
	if subject == "" {
		return Outgoing{}, errors.New("subject is required")
	}
	if len(subject) > maxSubjectBytes {
		return Outgoing{}, fmt.Errorf("subject is longer than %d bytes", maxSubjectBytes)
	}
	if !utf8.ValidString(subject) || strings.ContainsFunc(subject, isControl) {
		return Outgoing{}, errors.New("subject contains control characters or invalid UTF-8")
	}
	if len(body) > maxBodyBytes {
		return Outgoing{}, fmt.Errorf("body is longer than %d bytes", maxBodyBytes)
	}
	if !utf8.ValidString(body) || strings.ContainsRune(body, 0) {
		return Outgoing{}, errors.New("body contains NUL or invalid UTF-8")
	}
	return out, nil
}

// parseAddress returns the bare address in raw. A display name is accepted
// and dropped. Only printable ASCII is allowed, which rules out control
// characters, spaces and non-ASCII addresses that would need SMTPUTF8.
func parseAddress(raw string) (string, error) {
	parsed, err := mail.ParseAddress(raw)
	if err != nil {
		return "", fmt.Errorf("invalid address %q: %w", raw, err)
	}
	for _, r := range parsed.Address {
		if r <= ' ' || r >= 0x7f || r == '<' || r == '>' {
			return "", fmt.Errorf("invalid address %q: unsupported character", raw)
		}
	}
	return parsed.Address, nil
}

func isControl(r rune) bool {
	return (r < ' ' && r != '\t') || r == 0x7f
}

// build renders the RFC 5322 message. The result uses CRLF line endings and
// a quoted-printable body, so it is plain ASCII on the wire.
func (o Outgoing) build(from *mail.Address, now time.Time, msgID string) ([]byte, error) {
	var to []string
	for _, addr := range o.To {
		to = append(to, (&mail.Address{Address: addr}).String())
	}
	var buf bytes.Buffer
	header := func(key, value string) { buf.WriteString(key + ": " + value + "\r\n") }
	header("Date", now.Format(time.RFC1123Z))
	header("From", from.String())
	header("To", strings.Join(to, ", "))
	header("Subject", mime.QEncoding.Encode("utf-8", o.Subject))
	header("Message-ID", msgID)
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	buf.WriteString("\r\n")

	qp := quotedprintable.NewWriter(&buf)
	if _, err := qp.Write([]byte(o.Body)); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	buf.WriteString("\r\n")
	return buf.Bytes(), nil
}

// newMessageID returns a unique Message-ID in the sender's domain.
func newMessageID(from *mail.Address) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	domain := from.Address[strings.LastIndexByte(from.Address, '@')+1:]
	return "<" + hex.EncodeToString(b[:]) + "@" + domain + ">", nil
}
