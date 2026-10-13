package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// config is the server's startup configuration. All of it comes from the
// command line, which the policy file writes. Nothing comes from tool
// arguments or from the environment.
type config struct {
	mailpitURL *url.URL
	// imap selects the IMAP back end. When it is nil the server reads
	// from Mailpit.
	imap *imapOptions
	// check makes the server test its IMAP connection and exit.
	check bool
}

type imapOptions struct {
	addr string
	// host is the host part of addr, the name the server's certificate
	// has to carry.
	host     string
	startTLS bool
	user     string
	// passwordFile holds the password, so that it appears neither in the
	// policy file nor in the process list.
	passwordFile string
	caFile       string
	mailbox      string
}

func parseConfig(args []string) (config, error) {
	fs := flag.NewFlagSet("mail-source", flag.ContinueOnError)
	mailpitURL := fs.String("mailpit-url", "http://127.0.0.1:8025", "Mailpit HTTP API base URL")
	var opts imapOptions
	fs.StringVar(&opts.addr, "imap-addr", "", "IMAP server as host:port; selects the IMAP back end")
	tlsMode := fs.String("imap-tls", "implicit", "implicit (TLS from the first byte, usually port 993) or starttls")
	fs.StringVar(&opts.user, "imap-user", "", "IMAP user name")
	fs.StringVar(&opts.passwordFile, "imap-password-file", "", "file holding the IMAP password, readable only by its owner")
	fs.StringVar(&opts.caFile, "imap-ca-file", "", "PEM certificate to trust instead of the system's, for a server with a self-signed one")
	fs.StringVar(&opts.mailbox, "imap-mailbox", "INBOX", "mailbox to read")
	check := fs.Bool("check", false, "log in to the IMAP server, print the mailbox's message count and exit")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() > 0 {
		return config{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	cfg := config{check: *check}
	var err error
	if cfg.mailpitURL, err = url.Parse(strings.TrimRight(*mailpitURL, "/")); err != nil ||
		(cfg.mailpitURL.Scheme != "http" && cfg.mailpitURL.Scheme != "https") || cfg.mailpitURL.Host == "" {
		return config{}, fmt.Errorf("invalid --mailpit-url %q", *mailpitURL)
	}

	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if !given["imap-addr"] {
		for name := range given {
			if strings.HasPrefix(name, "imap-") {
				return config{}, fmt.Errorf("--%s needs --imap-addr", name)
			}
		}
		if cfg.check {
			return config{}, errors.New("--check needs --imap-addr")
		}
		return cfg, nil
	}

	switch {
	case given["mailpit-url"]:
		return config{}, errors.New("--mailpit-url and --imap-addr select different back ends; give one")
	case opts.user == "" || opts.passwordFile == "":
		return config{}, errors.New("--imap-addr needs --imap-user and --imap-password-file")
	case opts.mailbox == "":
		return config{}, errors.New("--imap-mailbox is empty")
	case *tlsMode != "implicit" && *tlsMode != "starttls":
		return config{}, fmt.Errorf("invalid --imap-tls %q: use implicit or starttls", *tlsMode)
	}
	var port string
	if opts.host, port, err = net.SplitHostPort(opts.addr); err != nil || opts.host == "" || port == "" {
		return config{}, fmt.Errorf("invalid --imap-addr %q: use host:port", opts.addr)
	}
	opts.startTLS = *tlsMode == "starttls"
	cfg.imap = &opts
	return cfg, nil
}
