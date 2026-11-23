package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

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
	// Keep, when set, is handed every file the server's tools return, and
	// the model is shown each by the Ref it gives, never by its bytes.
	Keep Keeper
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
			return call(ctx, live, s.Keep, name, remote, args)
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

func call(ctx context.Context, live *sdk.ClientSession, keep Keeper, tool, name string, args json.RawMessage) (agent.ToolResult, error) {
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
		text := textOf(res)
		if text == "" {
			text = "the tool reported an error and said nothing about it"
		}
		return agent.ToolResult{}, errors.New(text)
	}
	// Every file attached is named in the text, so empty text is no answer.
	out := resultOf(ctx, keep, tool, res)
	if out.Output == "" {
		out.Output = "the tool returned no text content"
	}
	return out, nil
}

// What a model is passed of a tool's files, as the brain's read_attachment
// passes a conversation's: an image up to 5 MiB, a PDF up to 10 MiB, text up
// to 100 KiB, at most eight files and 10 MiB of them a call. The rest is
// named in the text.
const (
	MaxImageBytes = 5 << 20
	MaxDocBytes   = 10 << 20
	MaxTextBytes  = 100 << 10
	FilesPerCall  = 8
	BytesPerCall  = 10 << 20
	maxNameRunes  = 100
)

// The images a model reads, by what their bytes say they are.
var images = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

// textOf is an error's answer: its text, each file it came with named and
// never attached.
func textOf(res *sdk.CallToolResult) string {
	var lines []string
	seen := 0
	for _, c := range res.Content {
		if t, ok := c.(*sdk.TextContent); ok {
			if t.Text != "" {
				lines = append(lines, t.Text)
			}
			continue
		}
		seen++
		lines = append(lines, "["+describe(c, seen)+", not shown with an error]")
	}
	return strings.Join(lines, "\n")
}

// resultOf is a tool's answer as the model reads it: its text, in order; the
// images and PDFs it returned attached, each named in the text too; the text
// files it returned in the text, under a line naming each; and a line for
// each file a model cannot be passed, saying what it was and why. Text files
// count toward a call's files and bytes as attached ones do. With keep, every
// file is kept and named by its id, and the bytes of none are in the result.
func resultOf(ctx context.Context, keep Keeper, tool string, res *sdk.CallToolResult) agent.ToolResult {
	var (
		out   agent.ToolResult
		lines []string
		seen  int // every block not text, so each is named apart
		files int // passed on, attached or as text
		bytes int // of those
	)
	for _, c := range res.Content {
		if t, ok := c.(*sdk.TextContent); ok {
			if t.Text != "" {
				lines = append(lines, t.Text)
			}
			continue
		}
		seen++
		if keep != nil {
			if file, ok := fileOf(c, seen); ok {
				file.Tool = tool
				line, part := kept(ctx, keep, file, c, seen, &files)
				if part != nil {
					out.Attach = append(out.Attach, *part)
				}
				lines = append(lines, line)
				continue
			}
		}
		f, note := fileIn(c, seen)
		switch {
		case f == nil:
		case files >= FilesPerCall:
			note = fmt.Sprintf("[%s, past the %d files a call passes on]", described(f), FilesPerCall)
		case bytes+f.cost() > BytesPerCall:
			note = fmt.Sprintf("[%s, past the %s a call passes on]", described(f), sizeOf(BytesPerCall))
		default:
			files++
			bytes += f.cost()
			if f.text != "" {
				note = f.text
			} else {
				out.Attach = append(out.Attach, f.Content)
			}
		}
		lines = append(lines, note)
	}
	out.Output = strings.Join(lines, "\n")
	return out
}

// passed is a file a model may be passed: attached, or, when text is set,
// as that text in the tool's own, its Blob naming it and holding its bytes.
type passed struct {
	agent.Content
	text string
}

// cost is what a file adds to a call's bytes: what the model is sent of it.
func (f *passed) cost() int {
	if f.text != "" {
		return len(f.text)
	}
	return len(f.Blob.Data)
}

// fileIn is block c, the nth that is not text, as the model may be passed
// it, with the line naming it; or only a line saying what it was and why it
// is not passed.
func fileIn(c sdk.Content, n int) (*passed, string) {
	switch c := c.(type) {
	case *sdk.ImageContent:
		if !strings.HasPrefix(bareType(c.MIMEType), "image/") {
			return nil, "[" + describe(c, n) + ", an image block that is not an image]"
		}
		return readable(c.MIMEType, fmt.Sprintf("image-%d", n), c.Data)
	case *sdk.EmbeddedResource:
		if c.Resource == nil {
			return nil, fmt.Sprintf("[file-%d: empty]", n)
		}
		name := nameOf(c.Resource.URI, n)
		if len(c.Resource.Blob) == 0 && c.Resource.Text != "" {
			// Sent as text, so text whatever type it names.
			return inline(name, kindOf(c.Resource.MIMEType), []byte(c.Resource.Text))
		}
		return readable(c.Resource.MIMEType, name, c.Resource.Blob)
	case *sdk.AudioContent:
		return nil, "[" + describe(c, n) + ", which a model here cannot hear]"
	case *sdk.ResourceLink:
		return nil, "[" + describe(c, n) + ", not fetched]"
	default:
		return nil, "[a content block of a kind this client does not read]"
	}
}

// readable is data, declared as mediaType, as a model may be passed it. An
// image or a PDF is attached only when its bytes are what it says; text is
// held in the line, when it is UTF-8; anything else is only named.
func readable(mediaType, name string, data []byte) (*passed, string) {
	kind := kindOf(mediaType)
	if len(data) == 0 {
		return nil, fmt.Sprintf("[%s: %s, empty]", name, kind)
	}
	sniffed := bareType(http.DetectContentType(data))
	switch {
	case images[kind] != "":
		name = withExtension(name, images[kind])
		switch {
		case sniffed != kind:
			return nil, fmt.Sprintf("[%s: said to be %s, but its bytes are %s]", name, kind, sniffed)
		case len(data) > MaxImageBytes:
			return nil, fmt.Sprintf("[%s: %s, %s, more than the %s an image is passed up to]", name, kind, exactly(len(data)), sizeOf(MaxImageBytes))
		}
		return attach(agent.ContentImage, kind, name, data)
	case kind == "application/pdf":
		switch {
		case sniffed != kind:
			return nil, fmt.Sprintf("[%s: said to be %s, but its bytes are %s]", name, kind, sniffed)
		case len(data) > MaxDocBytes:
			return nil, fmt.Sprintf("[%s: %s, %s, more than the %s a PDF is passed up to]", name, kind, exactly(len(data)), sizeOf(MaxDocBytes))
		}
		return attach(agent.ContentFile, kind, name, data)
	case textual(kind):
		return inline(name, kind, data)
	case strings.HasPrefix(kind, "image/"):
		return nil, fmt.Sprintf("[%s: %s, %s, an image type a model is not passed]", name, kind, sizeOf(len(data)))
	default:
		return nil, fmt.Sprintf("[%s: %s, %s, which a model cannot read]", name, kind, sizeOf(len(data)))
	}
}

// inline is a text file as a model reads it: in the tool's own text, cut
// where a conversation's text file would be.
func inline(name, kind string, data []byte) (*passed, string) {
	if !utf8.Valid(data) {
		return nil, fmt.Sprintf("[%s: %s, %s, which is not UTF-8 text]", name, kind, sizeOf(len(data)))
	}
	f := &passed{Content: agent.Content{Type: agent.ContentText, Blob: &agent.Blob{MediaType: kind, Name: name, Data: data}}}
	body, head := data, fmt.Sprintf("[%s: %s, %s]", name, kind, sizeOf(len(data)))
	if len(data) > MaxTextBytes {
		cut := MaxTextBytes
		for cut > 0 && !utf8.RuneStart(data[cut]) {
			cut--
		}
		body, head = data[:cut], fmt.Sprintf("[%s: %s, %s, the first %s]", name, kind, sizeOf(len(data)), sizeOf(MaxTextBytes))
	}
	f.text = head + "\n" + strings.ReplaceAll(string(body), "\x00", "\uFFFD")
	return f, ""
}

func attach(kind agent.ContentType, mediaType, name string, data []byte) (*passed, string) {
	f := &passed{Content: agent.Content{Type: kind, Blob: &agent.Blob{MediaType: mediaType, Name: name, Data: data}}}
	return f, "[attached " + described(f) + "]"
}

// described is a file passed on, as its line names it.
func described(f *passed) string {
	return fmt.Sprintf("%s: %s, %s", f.Blob.Name, f.Blob.MediaType, sizeOf(len(f.Blob.Data)))
}

// withExtension is name ending in ext, which a provider reads the type by:
// added when it is not there already, within maxNameRunes.
func withExtension(name, ext string) string {
	have := strings.ToLower(path.Ext(name))
	if have == ext || (ext == ".jpg" && have == ".jpeg") {
		return name
	}
	if r := []rune(name); len(r) > maxNameRunes-len(ext) {
		name = string(r[:maxNameRunes-len(ext)])
	}
	return name + ext
}

// describe is a block that is not text, named for a line: what it was, and
// how big.
func describe(c sdk.Content, n int) string {
	switch c := c.(type) {
	case *sdk.ImageContent:
		return fmt.Sprintf("image-%d: %s, %s", n, kindOf(c.MIMEType), sizeOf(len(c.Data)))
	case *sdk.AudioContent:
		return fmt.Sprintf("audio-%d: %s, %s", n, kindOf(c.MIMEType), sizeOf(len(c.Data)))
	case *sdk.EmbeddedResource:
		if c.Resource == nil {
			return fmt.Sprintf("file-%d", n)
		}
		return fmt.Sprintf("%s: %s, %s", nameOf(c.Resource.URI, n), kindOf(c.Resource.MIMEType), sizeOf(max(len(c.Resource.Blob), len(c.Resource.Text))))
	case *sdk.ResourceLink:
		size := ""
		if c.Size != nil {
			size = ", " + sizeOf(max(0, *c.Size))
		}
		return fmt.Sprintf("a link to %s: %s (%s%s)", clean(c.Name, fmt.Sprintf("link-%d", n)), linkOf(c.URI), kindOf(c.MIMEType), size)
	}
	return fmt.Sprintf("block-%d", n)
}

// nameOf is the file a URI names, for a model and a provider to read: the
// last part of its path, never its query or fragment, cleaned; file-n when
// it names none.
func nameOf(uri string, n int) string {
	fallback := fmt.Sprintf("file-%d", n)
	u, err := url.Parse(uri)
	if err != nil {
		return clean(withoutUser(path.Base(withoutQuery(uri))), fallback)
	}
	// x://name.csv names its file where a host would be.
	switch {
	case u.Path != "" && u.Path != "/":
		return clean(path.Base(u.Path), fallback)
	case u.Host != "":
		return clean(u.Host, fallback)
	}
	return clean(u.Opaque, fallback)
}

// linkOf is a link as a line shows it: without its query or fragment, which
// can carry a token, and cleaned.
func linkOf(uri string) string {
	if u, err := url.Parse(uri); err == nil {
		u.RawQuery, u.Fragment, u.RawFragment, u.User = "", "", "", nil
		uri = u.String()
	} else {
		uri = withoutQuery(uri)
		if scheme, rest, ok := strings.Cut(uri, "://"); ok {
			host, p, _ := strings.Cut(rest, "/")
			uri = scheme + "://" + withoutUser(host)
			if p != "" {
				uri += "/" + p
			}
		}
	}
	return clean(uri, "a link")
}

// withoutQuery is a URI url.Parse refused, cut before its query or fragment.
func withoutQuery(uri string) string {
	if i := strings.IndexAny(uri, "?#"); i >= 0 {
		return uri[:i]
	}
	return uri
}

// withoutUser is a host with what comes before its last @, a user and a
// password, removed.
func withoutUser(host string) string {
	if i := strings.LastIndex(host, "@"); i >= 0 {
		return host[i+1:]
	}
	return host
}

// clean is a name a server chose, safe in a line of the tool's text: no
// control characters or brackets, which could end the line or forge one, no
// "." or ".." alone, and at most maxNameRunes; fallback when nothing is left.
func clean(name, fallback string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) || r == '[' || r == ']' || r == utf8.RuneError {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if r := []rune(name); len(r) > maxNameRunes {
		name = string(r[:maxNameRunes])
	}
	if name == "" || name == "." || name == ".." || name == "/" {
		return fallback
	}
	return name
}

// textual is whether a type is text by its name: any text/ type, and the
// structured formats written as text (JSON, XML, YAML, JavaScript, SVG).
func textual(kind string) bool {
	if strings.HasPrefix(kind, "text/") || strings.HasSuffix(kind, "+json") || strings.HasSuffix(kind, "+xml") {
		return true
	}
	switch kind {
	case "application/json", "application/xml", "application/yaml", "application/x-yaml",
		"application/javascript", "application/x-javascript", "application/toml", "application/sql":
		return true
	}
	return false
}

// bareType is a media type without its parameters, lowercase; "" when what
// is left is not type/subtype, so a type can never carry a line of its own.
func bareType(mediaType string) string {
	kind, _, _ := strings.Cut(mediaType, ";")
	kind = strings.ToLower(strings.TrimSpace(kind))
	if !mediaTypes.MatchString(kind) {
		return ""
	}
	return kind
}

var mediaTypes = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]*/[a-z0-9][a-z0-9!#$&^_.+-]*$`)

// kindOf is a declared media type as a line names it, the common misspelling
// image/jpg read as image/jpeg.
func kindOf(mediaType string) string {
	switch kind := bareType(mediaType); kind {
	case "":
		return "no type"
	case "image/jpg":
		return "image/jpeg"
	default:
		return kind
	}
}

// sizeOf is n bytes as a person reads it, in binary units.
func sizeOf[N int | int64](n N) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KiB", n>>10)
	case n == 1:
		return "1 byte"
	}
	return fmt.Sprintf("%d bytes", n)
}

// exactly is n bytes to the byte, for a size beside the limit it passed,
// where a rounded figure would read as the limit itself.
func exactly(n int) string {
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
