package main

import (
	"flag"
	"fmt"
	"net/url"
	"strings"
)

// config is the server's startup configuration. All of it comes from the
// command line, which the policy file writes. Nothing comes from tool
// arguments or from the environment.
type config struct {
	mailpitURL *url.URL
}

func parseConfig(args []string) (config, error) {
	fs := flag.NewFlagSet("mail-source", flag.ContinueOnError)
	mailpitURL := fs.String("mailpit-url", "http://127.0.0.1:8025", "Mailpit HTTP API base URL")
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
