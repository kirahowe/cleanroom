package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // decoders for charsets other than UTF-8
	"github.com/emersion/go-message/mail"
)

// maxTextSize bounds the text taken from one message. A reader has to hold
// the whole text, and mail can be arbitrarily long.
const maxTextSize = 256 << 10

// parseMessage reads a raw message as it sits in a mailbox and returns its
// headers and text. The ID is left empty.
//
// The text is every inline text/plain part. If those are blank it is the
// inline text/html parts with their tags stripped. Attachments are skipped.
// A message that is cut short or malformed part of the way through gives
// the text that came before the damage, without an error: raw is
// attacker-controlled, and a message that cannot be read at all would hide
// whatever else is in it from the reader.
func parseMessage(raw io.Reader) (Message, error) {
	mr, err := mail.CreateReader(raw)
	if err != nil && !message.IsUnknownCharset(err) {
		return Message{}, fmt.Errorf("parsing the message: %w", err)
	}
	defer mr.Close()

	msg := Message{To: []string{}}
	msg.Subject, _ = mr.Header.Subject()
	if from, _ := mr.Header.AddressList("From"); len(from) > 0 {
		msg.From = displayAddress(from[0].Name, from[0].Address)
	}
	to, _ := mr.Header.AddressList("To")
	for _, a := range to {
		msg.To = append(msg.To, displayAddress(a.Name, a.Address))
	}
	if date, err := mr.Header.Date(); err == nil && !date.IsZero() {
		msg.Date = date.UTC().Format(time.RFC3339)
	}

	var plain, html []string
	room := maxTextSize
	for room > 0 {
		part, err := mr.NextPart()
		if err != nil && !message.IsUnknownCharset(err) {
			break // io.EOF, or damage
		}
		inline, ok := part.Header.(*mail.InlineHeader)
		if !ok {
			continue
		}
		mediaType, _, _ := inline.ContentType()
		if mediaType != "text/plain" && mediaType != "text/html" {
			continue
		}
		// A read error here means the part was cut short. Keep what
		// came before it.
		body, _ := io.ReadAll(io.LimitReader(part.Body, int64(room)))
		room -= len(body)
		text := strings.ToValidUTF8(string(body), "�")
		if mediaType == "text/plain" {
			plain = append(plain, text)
		} else {
			html = append(html, text)
		}
	}

	msg.Text = strings.TrimSpace(strings.Join(plain, "\n\n"))
	if msg.Text == "" {
		msg.Text = stripHTML(strings.Join(html, "\n"))
	}
	return msg, nil
}

// displayAddress writes an address for a reader, with the name decoded.
// The result is for reading and is not a valid header value.
func displayAddress(name, address string) string {
	if name == "" {
		return address
	}
	return name + " <" + address + ">"
}
