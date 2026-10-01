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

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	maxTools            = 500
	maxDescriptionBytes = 16 << 10
	maxSchemaBytes      = 64 << 10
)

var (
	ErrCommandServer = errors.New("a command server cannot be discovered until agents run in a sandbox")
	ErrUnusable      = errors.New("the server offers something that cannot be stored")
)

var toolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

type Secrets interface {
	Get(ctx context.Context, path, key string) (string, error)
}

type Discoverer struct {
	secrets   Secrets
	transport http.RoundTripper
	timeout   time.Duration
}

func New(sec Secrets, p Policy, timeout time.Duration) *Discoverer {
	return &Discoverer{secrets: sec, transport: newTransport(p), timeout: timeout}
}

func (d *Discoverer) Discover(ctx context.Context, s db.McpServer) ([]Tool, error) {
	if s.Url == nil {
		return nil, ErrCommandServer
	}

	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()

	token, err := d.token(ctx, s.AuthSecretRef)
	if err != nil {
		return nil, err
	}

	client := sdk.NewClient(&sdk.Implementation{Name: "openarity-brain", Version: "v0.1.0"}, nil)
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{
		Endpoint:             *s.Url,
		HTTPClient:           newClient(d.transport, token, deadline),
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("connecting: %w", err)
	}
	defer func() { _ = session.Close() }()

	return collect(session.Tools(ctx, nil))
}

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

	return Tool{Name: t.Name, Description: t.Description, InputSchema: schema}, nil
}
