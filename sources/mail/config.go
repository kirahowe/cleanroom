package main

import (
	"flag"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"strings"
)

// config is the server's startup configuration. Nothing in it comes from
// tool arguments.
type config struct {
	mailpitURL *url.URL
	smtpAddr   string
	from       *mail.Address
	allowed    []string
}

// repeated is a flag.Value that collects every occurrence of a flag.
type repeated []string

func (r *repeated) String() string     { return strings.Join(*r, ",") }
func (r *repeated) Set(v string) error { *r = append(*r, v); return nil }

// parseConfig reads flags from args, falling back to environment variables
// looked up through getenv.
func parseConfig(args []string, getenv func(string) string) (config, error) {
	env := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}
	fs := flag.NewFlagSet("mail-source", flag.ContinueOnError)
	mailpitURL := fs.String("mailpit-url", env("MAILPIT_URL", "http://127.0.0.1:8025"), "Mailpit HTTP API base URL (env MAILPIT_URL)")
	smtpAddr := fs.String("smtp-addr", env("MAILPIT_SMTP", "127.0.0.1:1025"), "Mailpit SMTP host:port (env MAILPIT_SMTP)")
	from := fs.String("from", env("MAIL_FROM", "assistant@cleanroom.test"), "sender address (env MAIL_FROM)")
	var allow repeated
	fs.Var(&allow, "allow-recipient", "permitted recipient address, repeatable (env MAIL_ALLOWED_RECIPIENTS, comma separated)")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}

	var cfg config
	var err error
	if cfg.mailpitURL, err = url.Parse(strings.TrimRight(*mailpitURL, "/")); err != nil ||
		(cfg.mailpitURL.Scheme != "http" && cfg.mailpitURL.Scheme != "https") || cfg.mailpitURL.Host == "" {
		return config{}, fmt.Errorf("invalid --mailpit-url %q", *mailpitURL)
	}
	if _, _, err := net.SplitHostPort(*smtpAddr); err != nil {
		return config{}, fmt.Errorf("invalid --smtp-addr %q: %w", *smtpAddr, err)
	}
	cfg.smtpAddr = *smtpAddr
	if cfg.from, err = mail.ParseAddress(*from); err != nil {
		return config{}, fmt.Errorf("invalid --from %q: %w", *from, err)
	}
	if len(allow) == 0 {
		for _, a := range strings.Split(getenv("MAIL_ALLOWED_RECIPIENTS"), ",") {
			if a = strings.TrimSpace(a); a != "" {
				allow = append(allow, a)
			}
		}
	}
	for _, a := range allow {
		addr, err := parseAddress(a)
		if err != nil {
			return config{}, fmt.Errorf("invalid allowed recipient: %w", err)
		}
		cfg.allowed = append(cfg.allowed, addr)
	}
	return cfg, nil
}
