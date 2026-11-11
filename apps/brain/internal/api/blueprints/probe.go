package blueprints

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// ErrProbe is a server the builder may not ask, or that did not answer; the
// model reads the reason.
var ErrProbe = errors.New("the server could not be asked for its tools")

// ToolsOf asks a server, which need not be the team's yet, for its tools, so
// a proposal can grant them by name in one go.
//
// A secret goes only where the team already sends it: to a server of the
// team's at that same address holding that same secret. Otherwise a page the
// builder read could steer one of the team's tokens to a host of its choosing.
func (ws *Workshop) ToolsOf(ctx context.Context, teamID uuid.UUID, url string, authSecret *string) ([]discovery.Tool, error) {
	if ws.discoverer == nil {
		return nil, fmt.Errorf("%w: this brain does not discover MCP servers", ErrProbe)
	}
	f, err := serverFields(teamID, serverSpec{Name: "probe", URL: &url, AuthSecret: authSecret})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrProbe, err)
	}
	if f.AuthSecretRef != nil {
		ok, err := ws.sendsTo(ctx, teamID, *f.URL, *f.AuthSecretRef)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: a secret is sent only to a server the team already has at that address with that secret; "+
				"ask without auth_secret, or propose the server and let the person apply it", ErrProbe)
		}
	}

	tools, err := ws.discoverer.Discover(ctx, db.McpServer{TeamID: teamID, Name: "probe", Url: f.URL, AuthSecretRef: f.AuthSecretRef})
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrProbe, probeFailure(err))
	}
	return tools, nil
}

func (ws *Workshop) sendsTo(ctx context.Context, teamID uuid.UUID, url, ref string) (bool, error) {
	rows, err := ws.store.ListTeamMCPServers(ctx, teamID)
	if err != nil {
		return false, err
	}
	for _, r := range rows {
		if r.Url != nil && *r.Url == url && r.AuthSecretRef != nil && *r.AuthSecretRef == ref {
			return true, nil
		}
	}
	return false, nil
}

// probeFailure says why, as the discover route would, without the dial
// detail a refusal logs.
func probeFailure(err error) string {
	switch {
	case errors.Is(err, discovery.ErrRefused):
		return "the brain may not reach that address: it is private or link-local, it redirected, or it needs a token over plain http"
	case errors.Is(err, context.DeadlineExceeded):
		return "the server did not answer in time"
	case errors.Is(err, discovery.ErrUnusable):
		return err.Error()
	default:
		return "the server did not answer as an MCP server; it may need a token, which only the person can set"
	}
}
