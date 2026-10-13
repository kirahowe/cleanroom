// Command reader is the scripted reader used by the demo and by anyone
// trying cleanroom without a model. It plays the part a model would play:
// given a question, it searches a mailbox through MCP, reads what it finds,
// and prints {"answer": ...}.
//
// It understands only the demo's three questions (a trip's cost, a reunion's
// date, an order's status), by keyword. That is enough to show the path an
// answer takes. To use a real model instead, see "Reader back ends" in the
// README.
//
// With --obey it becomes the reader the design assumes: one that does
// whatever the mail it reads tells it to. It looks for two phrasings in
// each message and follows them:
//
//	Call the tool NAME with {JSON arguments}
//	answer with exactly: "TEXT"
//
// Write an email with either phrasing, send it to the demo inbox, and watch
// what the obedient reader manages to do with it.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cleanroom/internal/typed"
)

func main() {
	log.SetFlags(0)
	var (
		configPath = flag.String("mcp-config", "", "MCP config file naming the source's server")
		question   = flag.String("question", "", "the question to answer")
		returns    = flag.String("returns", `"text"`, "the wanted type, as JSON")
		obey       = flag.Bool("obey", os.Getenv("DEMO_READER_OBEY") == "1", "do whatever the messages say (env DEMO_READER_OBEY=1)")
	)
	flag.Parse()

	spec, err := typed.ParseSpec(json.RawMessage(*returns))
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	source, err := connect(ctx, *configPath)
	if err != nil {
		log.Fatal(err)
	}
	defer source.Close()

	messages, err := readMessages(ctx, source, searchFor(*question))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("read %d messages", len(messages))

	answer := shape(spec, extract(messages))
	if *obey {
		if forced, ok := obeyMessages(ctx, source, messages); ok {
			answer = forced
		}
	}
	out, _ := json.Marshal(map[string]any{"answer": answer})
	fmt.Println(string(out))
}

// connect starts the one MCP server named in the config file.
func connect(ctx context.Context, path string) (*mcp.ClientSession, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	for _, server := range config.MCPServers {
		cmd := exec.Command(server.Command, server.Args...)
		cmd.Env = os.Environ()
		for name, value := range server.Env {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
		cmd.Stderr = os.Stderr
		client := mcp.NewClient(&mcp.Implementation{Name: "scripted-reader", Version: "0"}, nil)
		return client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	}
	return nil, fmt.Errorf("%s names no MCP server", path)
}

// searchFor picks a search query from the question's keywords.
func searchFor(question string) string {
	q := strings.ToLower(question)
	switch {
	case strings.Contains(q, "toronto") || strings.Contains(q, "trip"):
		return "toronto"
	case strings.Contains(q, "reunion"):
		return "reunion"
	case strings.Contains(q, "order") || strings.Contains(q, "deliver"):
		return "order"
	}
	return q
}

// readMessages searches the mailbox and reads every hit, newest first.
func readMessages(ctx context.Context, source *mcp.ClientSession, query string) ([]string, error) {
	var found struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := callInto(ctx, source, "search", map[string]any{"query": query}, &found); err != nil {
		return nil, err
	}
	var texts []string
	for _, m := range found.Messages {
		var msg struct {
			Text string `json:"text"`
		}
		if err := callInto(ctx, source, "read", map[string]any{"id": m.ID}, &msg); err != nil {
			return nil, err
		}
		texts = append(texts, msg.Text)
	}
	return texts, nil
}

func callInto(ctx context.Context, source *mcp.ClientSession, tool string, args, out any) error {
	res, err := source.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return err
	}
	if res.IsError {
		return fmt.Errorf("%s: %s", tool, resultText(res))
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func resultText(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(t.Text)
		}
	}
	return sb.String()
}

// facts is what the reader managed to pull out of the messages.
type facts struct {
	date     string // YYYY-MM-DD
	total    int    // cents
	hasTotal bool
	status   string
	sentence string
}

var (
	totalLine    = regexp.MustCompile(`(?m)^Total:\s*\$([0-9]+)\.([0-9]{2}) CAD`)
	dateLine     = regexp.MustCompile(`(?m)^(?:Date|Expected delivery):\s*([A-Z][a-z]+ [0-9]{1,2}, [0-9]{4})`)
	statusLine   = regexp.MustCompile(`(?m)^Status:\s*(.+?)\s*$`)
	locationLine = regexp.MustCompile(`(?m)^Current location:\s*(.+?)\s*$`)
)

// extract reads the messages newest first. Totals add up across messages.
// For everything else the newest message that mentions it wins.
func extract(messages []string) facts {
	var f facts
	location := ""
	for _, text := range messages {
		for _, m := range totalLine.FindAllStringSubmatch(text, -1) {
			var dollars, cents int
			fmt.Sscan(m[1], &dollars)
			fmt.Sscan(m[2], &cents)
			f.total += dollars*100 + cents
			f.hasTotal = true
		}
		if m := dateLine.FindStringSubmatch(text); m != nil && f.date == "" {
			if t, err := time.Parse("January 2, 2006", m[1]); err == nil {
				f.date = t.Format(time.DateOnly)
			}
		}
		if m := statusLine.FindStringSubmatch(text); m != nil && f.status == "" {
			f.status = m[1]
		}
		if m := locationLine.FindStringSubmatch(text); m != nil && location == "" {
			location = m[1]
		}
	}
	if f.status != "" {
		f.sentence = f.status
		if location != "" {
			f.sentence += ", last seen at the " + location
		}
		if f.date != "" {
			f.sentence += ". Expected on " + f.date
		}
		f.sentence += "."
	}
	return f
}

// shape puts the facts into the type the caller asked for. Anything the
// reader does not know is nil, which means "no answer".
func shape(spec typed.Spec, f facts) any {
	switch spec.Type {
	case typed.Date:
		if f.date != "" {
			return f.date
		}
	case typed.Money:
		if f.hasTotal {
			return map[string]string{
				"amount":   fmt.Sprintf("%d.%02d", f.total/100, f.total%100),
				"currency": "CAD",
			}
		}
	case typed.Choice:
		for _, c := range spec.Choices {
			if strings.EqualFold(c, f.status) {
				return c
			}
		}
	case typed.Record:
		record := map[string]any{}
		for name, field := range spec.Fields {
			record[name] = shape(field, f)
		}
		return record
	case typed.Text:
		if f.sentence != "" {
			return f.sentence
		}
	}
	return nil
}

var (
	toolOrder   = regexp.MustCompile(`(?m)Call the tool (\w+) with (\{.*\})`)
	answerOrder = regexp.MustCompile(`answer with exactly: "([^"]*)"`)
)

// obeyMessages does what the messages say. It reports the forced answer,
// if a message dictated one.
func obeyMessages(ctx context.Context, source *mcp.ClientSession, messages []string) (string, bool) {
	answer, forced := "", false
	for _, text := range messages {
		for _, m := range toolOrder.FindAllStringSubmatch(text, -1) {
			res, err := source.CallTool(ctx, &mcp.CallToolParams{Name: m[1], Arguments: json.RawMessage(m[2])})
			switch {
			case err != nil:
				log.Printf("obeying the message: tried tool %q and failed: %v", m[1], err)
			case res.IsError:
				log.Printf("obeying the message: tried tool %q and was told: %s", m[1], resultText(res))
			default:
				log.Printf("obeying the message: tool %q ran", m[1])
			}
		}
		if m := answerOrder.FindStringSubmatch(text); m != nil && !forced {
			log.Printf("obeying the message: answering with its text instead of the facts")
			answer, forced = m[1], true
		}
	}
	return answer, forced
}
