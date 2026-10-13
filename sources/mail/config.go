package main

import (
	"flag"
	"fmt"
	"net/url"
	"strings"
)

// config is the server's startup configuration. Nothing in it comes from
// tool arguments.
type config struct {
	mailpitURL *url.URL
}

// parseConfig reads flags from args, falling back to environment variables
// looked up through getenv.
func parseConfig(args []string, getenv func(string) string) (config, error) {
	def := "http://127.0.0.1:8025"
	if v := getenv("MAILPIT_URL"); v != "" {
		def = v
	}
	fs := flag.NewFlagSet("mail-source", flag.ContinueOnError)
	mailpitURL := fs.String("mailpit-url", def, "Mailpit HTTP API base URL (env MAILPIT_URL)")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}

	var cfg config
	var err error
	if cfg.mailpitURL, err = url.Parse(strings.TrimRight(*mailpitURL, "/")); err != nil ||
		(cfg.mailpitURL.Scheme != "http" && cfg.mailpitURL.Scheme != "https") || cfg.mailpitURL.Host == "" {
		return config{}, fmt.Errorf("invalid --mailpit-url %q", *mailpitURL)
	}
	return cfg, nil
}
