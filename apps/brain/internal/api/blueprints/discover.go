package blueprints

import (
	"context"
	"errors"
	"log/slog"

	"golang.org/x/sync/errgroup"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/mcpservers"
	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// discoverAtOnce bounds the servers asked for their tools at the same time
// after an apply: each waits up to the discovery timeout.
const discoverAtOnce = 4

// Refresher asks a server for its tools and records them: the MCP servers'
// discover, run for each server an apply wrote.
type Refresher interface {
	Refresh(ctx context.Context, row db.McpServer) ([]discovery.Tool, error)
}

// Discovered is what asking a written server for its tools came to: the
// tools by name, or why there are none yet.
type Discovered struct {
	Server string   `json:"server"`
	Tools  []string `json:"tools"`
	Error  string   `json:"error,omitempty"`
}

// discoverWritten asks every server the apply wrote for its tools, so an
// agent granted them can use them at once rather than after someone presses
// discover. The apply has committed: a failure here is reported, never undone.
func discoverWritten(ctx context.Context, logger *slog.Logger, rf Refresher, rows []db.McpServer) []Discovered {
	out := make([]Discovered, len(rows))
	// The write is done; a caller who leaves should not cut discovery short.
	ctx = context.WithoutCancel(ctx)
	var g errgroup.Group
	g.SetLimit(discoverAtOnce)
	for i, row := range rows {
		g.Go(func() error {
			out[i] = discoverOne(ctx, logger, rf, row)
			return nil
		})
	}
	_ = g.Wait()
	return out
}

func discoverOne(ctx context.Context, logger *slog.Logger, rf Refresher, row db.McpServer) Discovered {
	d := Discovered{Server: row.Name, Tools: []string{}}
	tools, err := rf.Refresh(ctx, row)
	var f *mcpservers.Failure
	switch {
	case err == nil:
		for _, t := range tools {
			d.Tools = append(d.Tools, t.Name)
		}
	case errors.As(err, &f):
		d.Error = f.Message
	case errors.Is(err, mcpservers.ErrChanged):
		d.Error = "the server changed while it was being discovered; discover it again"
	default:
		logger.ErrorContext(ctx, "failed to discover an applied MCP server", "server", row.ID, "error", err)
		d.Error = "the brain could not discover it; discover it again"
	}
	return d
}
