package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/tools/mcp"
)

// runServers are the MCP servers a run may call. Each is opened by the first
// call to one of its tools, so a server the run never calls is never
// reached, and one that is down fails the call that needed it rather than
// the run.
type runServers struct {
	b      *Builder
	mu     sync.Mutex
	open   []*mcp.Session
	closed bool
}

func (r *runServers) server(s db.McpServer) *lazyServer {
	return &lazyServer{run: r, server: s}
}

// keep holds a session for Close, or closes it at once when the run is over.
func (r *runServers) keep(s *mcp.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		_ = s.Close()
		return errors.New("the run is over")
	}
	r.open = append(r.open, s)
	return nil
}

func (r *runServers) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	for _, s := range r.open {
		_ = s.Close()
	}
	r.open = nil
}

type lazyServer struct {
	run    *runServers
	server db.McpServer

	// mu is held only while opening, so calls to a server already open run
	// side by side.
	mu      sync.Mutex
	session *mcp.Session
}

// opened opens the server once. A failure is not remembered: the next call
// tries again, each try bounded by the connect timeout.
func (l *lazyServer) opened(ctx context.Context) (*mcp.Session, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.session != nil {
		return l.session, nil
	}

	live, err := l.run.b.connect(ctx, l.server)
	if err != nil {
		return nil, err
	}
	if err := l.run.keep(live); err != nil {
		return nil, err
	}
	l.session = live
	return live, nil
}

// tool is a tool as discovery recorded it, called on the live server under
// the name the run gives it.
func (l *lazyServer) tool(name string, row db.ListGrantedMCPToolsRow) agent.Tool {
	return agent.Tool{
		Name:        name,
		Description: row.Description,
		Schema:      json.RawMessage(row.InputSchema),
		Invoke: func(ctx context.Context, args json.RawMessage) (string, error) {
			live, err := l.opened(ctx)
			if err != nil {
				return "", err
			}
			for _, t := range live.Tools() {
				if t.Name == name {
					return t.Invoke(ctx, args)
				}
			}
			return "", fmt.Errorf("the MCP server %q no longer offers %s; discover its tools again", l.server.Name, row.Name)
		},
	}
}
