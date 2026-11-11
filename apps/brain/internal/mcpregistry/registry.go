// Package mcpregistry looks MCP servers up in a registry that speaks the
// official MCP registry's API, so a team's builder finds a server's address
// and what it needs rather than guessing them.
package mcpregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	// MaxResults is the most one search returns; the model reads every one.
	MaxResults = 10
	maxBody    = 1 << 20
	activeOnly = "active"
	officialNS = "io.modelcontextprotocol.registry/official"
)

// Server is a registry entry as the builder reads it: where it runs, and what
// it must be given.
type Server struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Version     string    `json:"version,omitempty"`
	Repository  string    `json:"repository,omitempty"`
	Remotes     []Remote  `json:"remotes,omitempty"`
	Packages    []Package `json:"packages,omitempty"`
}

// Remote is a server reached at an address.
type Remote struct {
	Type    string  `json:"type"`
	URL     string  `json:"url"`
	Headers []Input `json:"headers,omitempty"`
}

// Package is a server run as a command, from a package registry.
type Package struct {
	RegistryType string  `json:"registry_type"`
	Identifier   string  `json:"identifier"`
	Version      string  `json:"version,omitempty"`
	RuntimeHint  string  `json:"runtime_hint,omitempty"`
	Transport    string  `json:"transport"`
	Env          []Input `json:"env,omitempty"`
}

// Input is a header or variable a server takes. A secret one is never filled
// in here: the person sets it.
type Input struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	// Format is how the value is written, such as "Bearer {token}".
	Format string `json:"format,omitempty"`
}

// Client searches one registry.
type Client struct {
	base string
	http *http.Client
}

// New is a client for the registry at base, over a client the composition
// root guards.
func New(base string, c *http.Client) *Client {
	return &Client{base: strings.TrimRight(base, "/"), http: c}
}

var ErrRegistry = errors.New("the MCP registry could not be searched")

// Search returns the latest active servers matching query, at most limit.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]Server, error) {
	limit = max(1, min(limit, MaxResults))
	q := url.Values{"search": {query}, "limit": {strconv.Itoa(limit)}, "version": {"latest"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v0.1/servers?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRegistry, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRegistry, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: it answered %d", ErrRegistry, resp.StatusCode)
	}

	var page wirePage
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxBody))
	if err := dec.Decode(&page); err != nil {
		return nil, fmt.Errorf("%w: its answer is not a page of servers: %w", ErrRegistry, err)
	}

	out := []Server{}
	for _, e := range page.Servers {
		if m, ok := e.Meta[officialNS]; ok && m.Status != "" && m.Status != activeOnly {
			continue
		}
		out = append(out, e.Server.view())
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// The registry's own shape, kept apart from what the builder is shown.
type wirePage struct {
	Servers []struct {
		Server wireServer          `json:"server"`
		Meta   map[string]wireMeta `json:"_meta"`
	} `json:"servers"`
}

type wireMeta struct {
	Status string `json:"status"`
}

type wireServer struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Repository  struct {
		URL string `json:"url"`
	} `json:"repository"`
	Remotes []struct {
		Type    string      `json:"type"`
		URL     string      `json:"url"`
		Headers []wireInput `json:"headers"`
	} `json:"remotes"`
	Packages []struct {
		RegistryType string `json:"registryType"`
		Identifier   string `json:"identifier"`
		Version      string `json:"version"`
		RuntimeHint  string `json:"runtimeHint"`
		Transport    struct {
			Type string `json:"type"`
		} `json:"transport"`
		Env []wireInput `json:"environmentVariables"`
	} `json:"packages"`
}

type wireInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	IsRequired  bool   `json:"isRequired"`
	IsSecret    bool   `json:"isSecret"`
	Value       string `json:"value"`
}

func (w wireServer) view() Server {
	s := Server{Name: w.Name, Description: w.Description, Version: w.Version, Repository: w.Repository.URL}
	for _, r := range w.Remotes {
		s.Remotes = append(s.Remotes, Remote{Type: r.Type, URL: r.URL, Headers: inputs(r.Headers)})
	}
	for _, p := range w.Packages {
		s.Packages = append(s.Packages, Package{
			RegistryType: p.RegistryType, Identifier: p.Identifier, Version: p.Version,
			RuntimeHint: p.RuntimeHint, Transport: p.Transport.Type, Env: inputs(p.Env),
		})
	}
	return s
}

func inputs(in []wireInput) []Input {
	var out []Input
	for _, i := range in {
		out = append(out, Input{Name: i.Name, Description: i.Description, Required: i.IsRequired, Secret: i.IsSecret, Format: i.Value})
	}
	return out
}
