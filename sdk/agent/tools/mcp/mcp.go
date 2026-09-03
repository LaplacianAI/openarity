package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

type Server struct {
	Name       string
	Command    []string
	Env        []string
	URL        string
	HTTPClient *http.Client
	Bare       bool
}

type Session struct {
	name    string
	session *sdk.ClientSession
	tools   []agent.Tool
}

func Connect(ctx context.Context, s Server) (*Session, error) {
	if s.Name == "" {
		return nil, errors.New("the server has no name, so its tools could not be attributed")
	}
	if (len(s.Command) == 0) == (s.URL == "") {
		return nil, fmt.Errorf("server %q needs exactly one of Command or URL", s.Name)
	}

	transport := transportFor(s)
	client := sdk.NewClient(&sdk.Implementation{Name: "openarity", Version: "v0.1.0"}, nil)
	live, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", s.Name, err)
	}

	tools, err := list(ctx, s, live)
	if err != nil {
		_ = live.Close()
		return nil, err
	}

	return &Session{name: s.Name, session: live, tools: tools}, nil
}

func transportFor(s Server) sdk.Transport {
	if s.URL != "" {
		return &sdk.StreamableClientTransport{Endpoint: s.URL, HTTPClient: s.HTTPClient}
	}

	cmd := exec.Command(s.Command[0], s.Command[1:]...) //nolint:gosec,noctx
	cmd.Env = s.Env
	if cmd.Env == nil {
		cmd.Env = []string{}
	}
	return &sdk.CommandTransport{Command: cmd}
}

func list(ctx context.Context, s Server, live *sdk.ClientSession) ([]agent.Tool, error) {
	var (
		out    []agent.Tool
		cursor string
		seen   = map[string]bool{}
	)

	for {
		page, err := live.ListTools(ctx, &sdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("listing the tools on %s: %w", s.Name, err)
		}

		for _, t := range page.Tools {
			tool, err := convert(s, live, t)
			if err != nil {
				return nil, err
			}
			if seen[tool.Name] {
				return nil, fmt.Errorf("server %s offers %q twice", s.Name, tool.Name)
			}
			seen[tool.Name] = true
			out = append(out, tool)
		}

		if page.NextCursor == "" {
			return out, nil
		}
		cursor = page.NextCursor
	}
}

var wellFormed = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func convert(s Server, live *sdk.ClientSession, t *sdk.Tool) (agent.Tool, error) {
	name := t.Name
	if !s.Bare {
		name = s.Name + "__" + t.Name
	}
	if !wellFormed.MatchString(name) {
		return agent.Tool{}, fmt.Errorf(
			"server %s offers %q, which is not a usable tool name once prefixed (%q)", s.Name, t.Name, name)
	}

	remote := t.Name
	return agent.Tool{
		Name:        name,
		Description: t.Description,
		Schema:      schemaOf(t),
		Invoke: func(ctx context.Context, args json.RawMessage) (string, error) {
			return call(ctx, live, remote, args)
		},
	}, nil
}

func schemaOf(t *sdk.Tool) json.RawMessage {
	if t.InputSchema == nil {
		return json.RawMessage(`{"type":"object"}`)
	}
	raw, _ := json.Marshal(t.InputSchema)
	return raw
}

func call(ctx context.Context, live *sdk.ClientSession, name string, args json.RawMessage) (string, error) {
	arguments := map[string]any{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &arguments); err != nil {
			return "", fmt.Errorf("the arguments were not a JSON object: %w", err)
		}
	}

	res, err := live.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return "", fmt.Errorf("calling %s: %w", name, err)
	}

	text := textOf(res)
	if res.IsError {
		if text == "" {
			text = "the tool reported an error and said nothing about it"
		}
		return "", errors.New(text)
	}
	if text == "" {
		return "the tool returned no text content", nil
	}
	return text, nil
}

func textOf(res *sdk.CallToolResult) string {
	var (
		b       strings.Builder
		skipped int
	)
	for _, c := range res.Content {
		if t, ok := c.(*sdk.TextContent); ok {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(t.Text)
			continue
		}
		skipped++
	}
	if skipped > 0 {
		fmt.Fprintf(&b, "\n[%d non-text content block(s) omitted]", skipped)
	}
	return b.String()
}

func (s *Session) Tools() []agent.Tool { return s.tools }

func (s *Session) Name() string { return s.name }

func (s *Session) Close() error { return s.session.Close() }

func Gather(sessions ...*Session) ([]agent.Tool, error) {
	var (
		out   []agent.Tool
		owner = map[string]string{}
	)
	for _, s := range sessions {
		for _, t := range s.Tools() {
			if first, taken := owner[t.Name]; taken {
				return nil, fmt.Errorf("%s and %s both offer %q", first, s.name, t.Name)
			}
			owner[t.Name] = s.name
			out = append(out, t)
		}
	}
	return out, nil
}
