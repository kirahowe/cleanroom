package main

import (
	"reflect"
	"strings"
	"testing"
)

// crlf writes a message the way it travels: lines end in CR LF.
func crlf(s string) string {
	return strings.ReplaceAll(strings.TrimLeft(s, "\n"), "\n", "\r\n")
}

const alternativeMessage = `
From: =?utf-8?q?Ann_L=C3=A9a?= <ann@example.com>
To: bob@example.com, "Carol" <carol@example.com>
Subject: =?utf-8?b?UsOpdW5pb24=?= plans
Date: Sun, 01 Mar 2026 12:00:00 +0100
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary=outer

--outer
Content-Type: multipart/alternative; boundary=inner

--inner
Content-Type: text/plain; charset=iso-8859-1
Content-Transfer-Encoding: quoted-printable

The r=E9union is on July 17.
--inner
Content-Type: text/html; charset=utf-8

<p>The html version</p>
--inner--
--outer
Content-Type: text/plain
Content-Disposition: attachment; filename=notes.txt

attached notes
--outer
Content-Type: application/pdf
Content-Transfer-Encoding: base64

JVBERi0=
--outer--
`

func TestParseMessage(t *testing.T) {
	got, err := parseMessage(strings.NewReader(crlf(alternativeMessage)))
	if err != nil {
		t.Fatal(err)
	}
	want := Message{
		From:    "Ann Léa <ann@example.com>",
		To:      []string{"bob@example.com", "Carol <carol@example.com>"},
		Subject: "Réunion plans",
		Date:    "2026-03-01T11:00:00Z",
		Text:    "The réunion is on July 17.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestParseMessageText(t *testing.T) {
	for name, tc := range map[string]struct{ raw, want string }{
		"not multipart": {
			raw:  "Subject: s\n\njust text\n",
			want: "just text",
		},
		"base64 body": {
			raw:  "Content-Type: text/plain; charset=utf-8\nContent-Transfer-Encoding: base64\n\naGVsbG8gd29ybGQ=\n",
			want: "hello world",
		},
		"html only": {
			raw:  "Content-Type: text/html\n\n<html><head><style>p{}</style></head><body><p>Hi &amp; bye</p><p>Line<br>two</p></body></html>\n",
			want: "Hi & bye\nLine\ntwo",
		},
		"blank plain part falls back to html": {
			raw: "Content-Type: multipart/alternative; boundary=b\n\n" +
				"--b\nContent-Type: text/plain\n\n  \n--b\nContent-Type: text/html\n\n<p>from html</p>\n--b--\n",
			want: "from html",
		},
		"several plain parts": {
			raw: "Content-Type: multipart/mixed; boundary=b\n\n" +
				"--b\nContent-Type: text/plain\n\nfirst\n--b\nContent-Type: text/plain\n\nsecond\n--b--\n",
			want: "first\n\nsecond",
		},
		"unknown charset": {
			raw:  "Content-Type: text/plain; charset=x-no-such-charset\n\nstill readable\n",
			want: "still readable",
		},
		"cut off inside a part": {
			raw: "Content-Type: multipart/mixed; boundary=b\n\n" +
				"--b\nContent-Type: text/plain\n\nfirst\n--b\nContent-Type: text/plain\n\nsecond, then the fetch limit",
			want: "first\n\nsecond, then the fetch limit",
		},
		"no text at all": {
			raw:  "Content-Type: application/pdf\nContent-Transfer-Encoding: base64\n\nJVBERi0=\n",
			want: "",
		},
	} {
		got, err := parseMessage(strings.NewReader(crlf(tc.raw)))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got.Text != tc.want {
			t.Errorf("%s: text = %q, want %q", name, got.Text, tc.want)
		}
	}
}

func TestParseMessageBoundsTheText(t *testing.T) {
	raw := "Content-Type: multipart/mixed; boundary=b\n\n" +
		"--b\nContent-Type: text/plain\n\n" + strings.Repeat("a", maxTextSize-10) + "\n" +
		"--b\nContent-Type: text/plain\n\n" + strings.Repeat("b", 100) + "\n" +
		"--b\nContent-Type: text/plain\n\nnever reached\n--b--\n"
	got, err := parseMessage(strings.NewReader(crlf(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Text) > maxTextSize+2 || strings.Contains(got.Text, "never reached") {
		t.Errorf("text is %d bytes, limit %d", len(got.Text), maxTextSize)
	}
}

// Input that is not a message at all still must not panic or hang.
func FuzzParseMessage(f *testing.F) {
	f.Add(crlf(alternativeMessage))
	f.Add("")
	f.Add("Content-Type: multipart/mixed; boundary=\n\n----\n")
	f.Fuzz(func(t *testing.T, raw string) {
		msg, err := parseMessage(strings.NewReader(raw))
		if err == nil && len(msg.Text) > maxTextSize+2 {
			t.Errorf("text is %d bytes", len(msg.Text))
		}
	})
}
