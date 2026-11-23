package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"path"
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
	client := sdk.NewClient(&sdk.Implementation{Name: "openarity", Version: "v0.3.0"}, nil)
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
		Result: func(ctx context.Context, args json.RawMessage) (agent.ToolResult, error) {
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

func call(ctx context.Context, live *sdk.ClientSession, name string, args json.RawMessage) (agent.ToolResult, error) {
	arguments := map[string]any{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &arguments); err != nil {
			return agent.ToolResult{}, fmt.Errorf("the arguments were not a JSON object: %w", err)
		}
	}

	res, err := live.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("calling %s: %w", name, err)
	}

	if res.IsError {
		// An error is its text: whatever came with it is counted, never shown.
		text := resultOf(res, false).Output
		if text == "" {
			text = "the tool reported an error and said nothing about it"
		}
		return agent.ToolResult{}, errors.New(text)
	}
	// Every file attached is named in the text, so empty text is no answer.
	out := resultOf(res, true)
	if out.Output == "" {
		out.Output = "the tool returned no text content"
	}
	return out, nil
}

// What a model is passed of a tool's files, as the brain's read_attachment
// passes a conversation's: an image up to 5 MB, a PDF or a text file up to
// 10 MB, and at most eight files a call. The rest is named in the text.
const (
	maxImageBytes = 5 << 20
	maxDocBytes   = 10 << 20
	filesPerCall  = 8
)

// resultOf is a tool's answer as the model reads it: its text blocks, in
// order, and the images and documents it returned attached, each named in
// the text too. A file a model cannot be passed is a line saying what it was
// and why; with keep false, every file is.
func resultOf(res *sdk.CallToolResult, keep bool) agent.ToolResult {
	var (
		out   agent.ToolResult
		lines []string
		files int // attached
		seen  int // every block not text, so each is named apart
	)
	for _, c := range res.Content {
		if text, ok := textIn(c); ok {
			lines = append(lines, text)
			continue
		}
		seen++
		f := fileIn(c, seen)
		switch {
		case f.line != "":
			lines = append(lines, f.line)
		case !keep:
			lines = append(lines, fmt.Sprintf("[%s: %s, %s, not shown with an error]", f.name, f.mediaType, sizeOf(len(f.data))))
		case files >= filesPerCall:
			lines = append(lines, fmt.Sprintf("[%s: %s, %s, past the %d files a call passes on]", f.name, f.mediaType, sizeOf(len(f.data)), filesPerCall))
		case len(f.data) > f.limit:
			lines = append(lines, fmt.Sprintf("[%s: %s, %s, more than the %s %s is passed up to]", f.name, f.mediaType, sizeOf(len(f.data)), sizeOf(f.limit), f.what))
		default:
			files++
			out.Attach = append(out.Attach, agent.Content{
				Type: f.kind,
				Blob: &agent.Blob{MediaType: f.mediaType, Name: f.name, Data: f.data},
			})
			lines = append(lines, fmt.Sprintf("[attached %s: %s, %s]", f.name, f.mediaType, sizeOf(len(f.data))))
		}
	}
	out.Output = strings.Join(lines, "\n")
	return out
}

// textIn is the text a content block carries: a text block's, or an embedded
// resource's when it is text rather than a blob, as a file a server returns.
func textIn(c sdk.Content) (string, bool) {
	switch c := c.(type) {
	case *sdk.TextContent:
		return c.Text, true
	case *sdk.EmbeddedResource:
		if c.Resource != nil && c.Resource.Blob == nil {
			return c.Resource.Text, true
		}
	}
	return "", false
}

// A file is a block that is not text: what it would be attached as, or, in
// line, why it never can be.
type file struct {
	kind      agent.ContentType
	mediaType string
	name      string
	data      []byte
	limit     int
	what      string
	line      string
}

// fileIn is block c as a file, the nth of its result.
func fileIn(c sdk.Content, n int) file {
	switch c := c.(type) {
	case *sdk.ImageContent:
		return readableAs(c.MIMEType, fmt.Sprintf("image-%d%s", n, extension(c.MIMEType)), c.Data)
	case *sdk.EmbeddedResource:
		if c.Resource == nil {
			return file{line: "[an embedded resource with nothing in it]"}
		}
		name := path.Base(c.Resource.URI)
		if name == "." || name == "/" || name == "" {
			name = fmt.Sprintf("file-%d", n)
		}
		return readableAs(c.Resource.MIMEType, name, c.Resource.Blob)
	case *sdk.AudioContent:
		return file{line: fmt.Sprintf("[audio-%d: %s, %s, which a model here cannot hear]", n, bareType(c.MIMEType), sizeOf(len(c.Data)))}
	case *sdk.ResourceLink:
		size := ""
		if c.Size != nil {
			size = ", " + sizeOf(int(*c.Size))
		}
		return file{line: fmt.Sprintf("[a link to %s: %s (%s%s), not fetched]", c.Name, c.URI, bareType(c.MIMEType), size)}
	default:
		return file{line: "[a content block of a kind this client does not read]"}
	}
}

// readableAs is data of mediaType as a model can be passed it: an image other
// than SVG, a PDF or plain text. Any other type is a line naming it.
func readableAs(mediaType, name string, data []byte) file {
	kind := bareType(mediaType)
	switch {
	case strings.HasPrefix(kind, "image/") && kind != "image/svg+xml":
		return file{kind: agent.ContentImage, mediaType: kind, name: name, data: data, limit: maxImageBytes, what: "an image"}
	case kind == "application/pdf" || kind == "text/plain":
		return file{kind: agent.ContentFile, mediaType: kind, name: name, data: data, limit: maxDocBytes, what: "a PDF or a text file"}
	default:
		if kind == "" {
			kind = "no type"
		}
		return file{line: fmt.Sprintf("[%s: %s, %s, which a model cannot read]", name, kind, sizeOf(len(data)))}
	}
}

func bareType(mediaType string) string {
	kind, _, _ := strings.Cut(mediaType, ";")
	return strings.ToLower(strings.TrimSpace(kind))
}

func extension(mediaType string) string {
	switch bareType(mediaType) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	return ""
}

// sizeOf is n bytes as a person reads it.
func sizeOf(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
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
