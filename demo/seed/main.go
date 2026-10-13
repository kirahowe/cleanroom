// Command seed fills a local Mailpit inbox with the demo messages in
// demo/inbox: receipts from a trip, a reunion invitation, an order with a
// shipping update, and one message that carries a prompt injection.
//
// It talks only to the Mailpit it is pointed at, and checks that the server
// really is Mailpit before sending anything.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/mail"
	"net/smtp"
	"os"
	"path/filepath"
	"slices"
	"time"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("seed: ")
	var (
		api   = flag.String("api", "http://127.0.0.1:18025", "Mailpit HTTP address")
		smtp  = flag.String("smtp", "127.0.0.1:11025", "Mailpit SMTP address")
		dir   = flag.String("dir", "demo/inbox", "directory of .eml files to send")
		reset = flag.Bool("reset", false, "delete every message in Mailpit first")
	)
	flag.Parse()

	if err := checkMailpit(*api); err != nil {
		log.Fatalf("%s does not look like Mailpit, so nothing was sent: %v", *api, err)
	}
	if *reset {
		if err := deleteAll(*api); err != nil {
			log.Fatal(err)
		}
	}
	files, err := filepath.Glob(filepath.Join(*dir, "*.eml"))
	if err != nil || len(files) == 0 {
		log.Fatalf("no .eml files in %s", *dir)
	}
	slices.Sort(files)
	for _, file := range files {
		if err := send(*smtp, file); err != nil {
			log.Fatalf("%s: %v", file, err)
		}
	}
	fmt.Printf("seeded %d messages; open %s to see the inbox\n", len(files), *api)
}

var client = &http.Client{Timeout: 5 * time.Second}

func checkMailpit(api string) error {
	resp, err := client.Get(api + "/api/v1/info")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var info struct{ Version string }
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil || info.Version == "" {
		return fmt.Errorf("no Mailpit version in the reply from /api/v1/info")
	}
	return nil
}

func deleteAll(api string) error {
	req, err := http.NewRequest(http.MethodDelete, api+"/api/v1/messages", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("deleting messages: %s", resp.Status)
	}
	return nil
}

// send delivers one .eml file, taking the envelope from its headers.
func send(addr, file string) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	from, err := mail.ParseAddress(msg.Header.Get("From"))
	if err != nil {
		return fmt.Errorf("reading the From header: %w", err)
	}
	to, err := mail.ParseAddress(msg.Header.Get("To"))
	if err != nil {
		return fmt.Errorf("reading the To header: %w", err)
	}
	// SMTP wants CRLF line endings. The files use LF so they are easy
	// to edit.
	body := bytes.ReplaceAll(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))
	return smtp.SendMail(addr, nil, from.Address, []string{to.Address}, body)
}
