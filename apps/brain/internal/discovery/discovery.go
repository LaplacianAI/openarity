package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	maxTools            = 500
	maxDescriptionBytes = 16 << 10
	maxSchemaBytes      = 64 << 10
)

var (
	ErrCommandServer = errors.New("this server runs a command, and its team has no sandbox to run it in: choose one in Settings, or register a URL instead")
	ErrUnusable      = errors.New("the server offers something that cannot be stored")
	// ErrRoom is a command server whose room could not be made or started.
	ErrRoom = errors.New("the server could not be run in its sandbox")
)

var toolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	// Annotations are the server's hints about the tool, as MCP's
	// ToolAnnotations put them, or nil when it gave none. They are believed
	// only for a server an admin trusts.
	Annotations json.RawMessage
}

type Secrets interface {
	Get(ctx context.Context, path, key string) (string, error)
}

type Discoverer struct {
	secrets     Secrets
	transport   http.RoundTripper
	calls       http.RoundTripper
	timeout     time.Duration
	connections Connections
	rooms       Rooms
}

// Rooms is where a command server runs: Ensure returns the address of the
// server in its team's sandbox, which is started if it is not running. A cold
// start can take longer than a discovery's own timeout, so it is waited for
// before that timeout starts.
type Rooms interface {
	Ensure(ctx context.Context, team uuid.UUID, s db.McpServer) (sandbox.Endpoint, error)
}

// RunsCommandsIn gives the discoverer somewhere to run command servers. Without
// it a command server is refused with ErrCommandServer.
func (d *Discoverer) RunsCommandsIn(r Rooms) *Discoverer {
	d.rooms = r
	return d
}

// PlainClient is a client for calls that carry no server's credentials, such
// as asking a sandbox room whether it is ready, or a sandbox provider for a
// room: it dials only what the policy allows, takes no redirect and reads at
// most 8 MiB.
func PlainClient(p Policy) *http.Client {
	// A provider can take minutes to answer a request that makes a room, so
	// this waits as long as a tool call does; each caller bounds its own.
	return newClient(newTransport(p, callHeaderTimeout), "", time.Time{})
}

func New(sec Secrets, p Policy, timeout time.Duration) *Discoverer {
	return &Discoverer{
		secrets:   sec,
		transport: newTransport(p, headerTimeout),
		calls:     newTransport(p, callHeaderTimeout),
		timeout:   timeout,
	}
}

// Reach is where a run calls a server, and the client to call it with. A URL
// server is its URL. A command server is started in its team's sandbox, if it
// is not running, and is that room's address, called with the headers the
// provider and the room want.
func (d *Discoverer) Reach(ctx context.Context, s db.McpServer) (string, *http.Client, error) {
	room, err := d.room(ctx, s)
	if err != nil {
		return "", nil, err
	}
	return d.dial(ctx, s, room, d.calls, time.Time{})
}

// room is a command server's endpoint, or nil for a URL server.
func (d *Discoverer) room(ctx context.Context, s db.McpServer) (*sandbox.Endpoint, error) {
	if s.Url != nil {
		return nil, nil
	}
	if d.rooms == nil {
		return nil, ErrCommandServer
	}

	ep, err := d.rooms.Ensure(ctx, s.TeamID, s)
	if errors.Is(err, sandbox.ErrNoProvider) {
		return nil, ErrCommandServer
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRoom, err)
	}
	return &ep, nil
}

// dial is the address and the client of a server, once its room, if it has
// one, is ready.
func (d *Discoverer) dial(
	ctx context.Context, s db.McpServer, room *sandbox.Endpoint, base http.RoundTripper, deadline time.Time,
) (string, *http.Client, error) {
	if room == nil {
		c, err := d.httpClient(ctx, s, base, deadline)
		return *s.Url, c, err
	}

	// A room is called with the headers its provider and its own token ask
	// for, whatever the scheme: the address is the provider's, which the
	// transport's policy has already allowed, and the token is the room's own
	// and reaches nothing else.
	c := newClient(base, "", deadline)
	c.Transport = headers{h: room.Header, next: c.Transport}
	return room.URL, c, nil
}

// httpClient calls a server as it is reached: with its sign-in's token,
// renewed as it runs out, or with its static token, or with none. A sign-in
// that cannot give a token now fails here, before any call is made.
func (d *Discoverer) httpClient(ctx context.Context, s db.McpServer, base http.RoundTripper, deadline time.Time) (*http.Client, error) {
	if s.AuthKind != "oauth" || d.connections == nil {
		token, err := d.tokenFor(ctx, s)
		if err != nil {
			return nil, err
		}
		return newClient(base, token, deadline), nil
	}
	t := &tokens{d: d, s: s}
	if _, err := t.get(ctx, ""); err != nil {
		return nil, err
	}
	c := newClient(base, "", deadline)
	c.Transport = renewing{t: t, next: c.Transport}
	return c, nil
}

func (d *Discoverer) Discover(ctx context.Context, s db.McpServer) ([]Tool, error) {
	// Before the timeout starts: a room's cold start is not the server being slow.
	room, err := d.room(ctx, s)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()

	endpoint, httpClient, err := d.dial(ctx, s, room, d.transport, deadline)
	if err != nil {
		return nil, err
	}

	client := sdk.NewClient(&sdk.Implementation{Name: "openarity-brain", Version: "v0.1.0"}, nil)
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           httpClient,
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("connecting: %w", err)
	}
	defer func() { _ = session.Close() }()

	return collect(session.Tools(ctx, nil))
}

// ErrNotSignedIn is a server that signs in and has no sign-in: never made,
// or disconnected. Someone connects it on the dashboard.
var ErrNotSignedIn = errors.New("the server needs signing in: connect it on the dashboard")

// tokenFor is the token a server is called with: its sign-in's access token,
// or the static one its auth_secret_ref names, or none.
func (d *Discoverer) tokenFor(ctx context.Context, s db.McpServer) (string, error) {
	if s.AuthKind != "oauth" {
		return d.token(ctx, s.AuthSecretRef)
	}
	v, err := d.secrets.Get(ctx, secrets.Path(s.TeamID, secrets.KindMCPOAuth, s.ID), OAuthAccessKey)
	if errors.Is(err, secrets.ErrNotFound) || (err == nil && v == "") {
		return "", ErrNotSignedIn
	}
	if err != nil {
		return "", fmt.Errorf("reading the server's sign-in: %w", err)
	}
	return v, nil
}

// The keys of a sign-in's secret, written whole.
const (
	OAuthAccessKey  = "access_token"
	OAuthRefreshKey = "refresh_token"
	OAuthExpiryKey  = "expires_at"
)

func (d *Discoverer) token(ctx context.Context, ref *string) (string, error) {
	if ref == nil {
		return "", nil
	}

	i := strings.LastIndexByte(*ref, '#')
	if i < 0 {
		return "", errors.New("auth_secret_ref is not path#key")
	}

	v, err := d.secrets.Get(ctx, (*ref)[:i], (*ref)[i+1:])
	if err != nil {
		return "", fmt.Errorf("reading the server's token: %w", err)
	}
	if v == "" {
		return "", fmt.Errorf("%w: the server's token is empty", secrets.ErrNotFound)
	}
	return v, nil
}

func collect(tools iter.Seq2[*sdk.Tool, error]) ([]Tool, error) {
	var out []Tool
	seen := map[string]bool{}

	for t, err := range tools {
		if err != nil {
			return nil, fmt.Errorf("listing tools: %w", err)
		}
		if len(out) == maxTools {
			return nil, fmt.Errorf("%w: more than %d tools", ErrUnusable, maxTools)
		}

		tool, err := convert(t)
		if err != nil {
			return nil, err
		}
		if seen[tool.Name] {
			return nil, fmt.Errorf("%w: %s is offered twice", ErrUnusable, tool.Name)
		}
		seen[tool.Name] = true
		out = append(out, tool)
	}

	return out, nil
}

func convert(t *sdk.Tool) (Tool, error) {
	if !toolName.MatchString(t.Name) {
		return Tool{}, fmt.Errorf("%w: a tool name that is not 1-64 letters, digits, hyphens or underscores", ErrUnusable)
	}

	schema := json.RawMessage(`{"type":"object"}`)
	if t.InputSchema != nil {
		raw, err := json.Marshal(t.InputSchema)
		if err != nil {
			return Tool{}, fmt.Errorf("%w: %s has an input schema that is not JSON", ErrUnusable, t.Name)
		}
		schema = raw
	}

	switch {
	case len(t.Description) > maxDescriptionBytes:
		return Tool{}, fmt.Errorf("%w: %s has a description over 16 KiB", ErrUnusable, t.Name)
	case len(schema) > maxSchemaBytes:
		return Tool{}, fmt.Errorf("%w: %s has an input schema over 64 KiB", ErrUnusable, t.Name)
	case strings.ContainsRune(t.Description, 0) || bytes.Contains(schema, []byte(`\u0000`)):
		return Tool{}, fmt.Errorf("%w: %s contains a NUL character", ErrUnusable, t.Name)
	}

	var hints json.RawMessage
	if t.Annotations != nil {
		raw, err := json.Marshal(t.Annotations)
		if err != nil {
			return Tool{}, fmt.Errorf("%w: %s has annotations that are not JSON", ErrUnusable, t.Name)
		}
		hints = raw
	}

	return Tool{Name: t.Name, Description: t.Description, InputSchema: schema, Annotations: hints}, nil
}
