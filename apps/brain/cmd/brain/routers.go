package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/agents"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/authconfig"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/blueprints"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/channels"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/docs"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/loops"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/mcpservers"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/runs"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/sessions"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/skills"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/teamgraph"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/teams"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/teamsettings"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/teamsummary"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/ui"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/users"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/whoami"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/server"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// channelSecrets reads as well as writes: deleting a Telegram channel reads
// its token to tell Telegram to stop posting.
type channelSecrets struct {
	secrets.Store
	secrets.Writer
}

func newRouters(
	cfg *config.Config,
	logger *slog.Logger,
	dbStore *store.Store,
	authorizer *authz.Authorizer,
	secretStore secrets.Store,
	secretWriter secrets.Writer,
	registry gateway.Registry,
	attachments *objects.Encrypted,
	importer skills.Importer,
	discoverer mcpservers.Discoverer,
	graphs teamgraph.Viewer,
	starter runs.Starter,
	notices gateway.Kicker,
) []server.Router {
	routers := []server.Router{
		whoami.New(logger),
		teams.New(logger, dbStore, authorizer, cfg.TeamPurgeAfter),
		channels.New(logger, dbStore, authorizer, channelSecrets{secretStore, secretWriter}, registry, cfg.WebhookPublicURL,
			reachable(cfg.MCPPrivateNetworks), replyClient(reachable(cfg.MCPPrivateNetworks)), notices),
		skills.New(logger, skillStore{dbStore}, attachments, importer),
		mcpservers.New(logger, mcpStore{dbStore}, discoverer, secretWriter),
		agents.New(logger, agentStore{dbStore}, runtime.Loops()),
		blueprints.New(logger, blueprintStore{dbStore}, authorizer, attachments,
			mcpservers.NewRefresher(logger, discoverer, mcpStore{dbStore}), runtime.Loops()),
		loops.New(logger, runtime.Loops()),
		runs.New(logger, runStore{dbStore}, authorizer, starter),
		teamgraph.New(logger, graphs),
		teamsummary.New(logger, dbStore, authorizer),
		teamsettings.New(logger, dbStore, secretWriter, reachable(cfg.MCPPrivateNetworks)),
		users.New(logger, dbStore),
		sessions.New(logger, dbStore, authorizer, attachments),
		authconfig.New(logger, cfg),
		ui.New(logger),
	}

	if cfg.Environment == config.EnvironmentDevelopment {
		routers = append(routers, docs.New(logger))
	}

	return routers
}

func newWebhookRouters(
	logger *slog.Logger,
	dbStore *store.Store,
	secretStore secrets.Store,
	registry gateway.Registry,
	attachments gateway.Objects,
	runs gateway.Enqueuer,
	notices gateway.Kicker,
) []server.Router {
	sink := gateway.NewInbox(inbox{dbStore}, runs, logger)
	ingest := gateway.NewIngest(attachments, logger)

	return []server.Router{
		gateway.New(logger, dbStore, secretStore, registry, sink, ingest, notices),
	}
}

type inbox struct{ *store.Store }

func (i inbox) InTx(ctx context.Context, fn func(gateway.Queries) error) error {
	return i.Store.InTx(ctx, func(q *db.Queries) error { return fn(q) })
}

type skillStore struct{ *store.Store }

func (s skillStore) InTx(ctx context.Context, fn func(skills.Queries) error) error {
	return s.Store.InTx(ctx, func(q *db.Queries) error { return fn(q) })
}

type mcpStore struct{ *store.Store }

func (s mcpStore) InTx(ctx context.Context, fn func(mcpservers.Queries) error) error {
	return s.Store.InTx(ctx, func(q *db.Queries) error { return fn(q) })
}

type agentStore struct{ *store.Store }

func (s agentStore) InTx(ctx context.Context, fn func(agents.Queries) error) error {
	return s.Store.InTx(ctx, func(q *db.Queries) error { return fn(q) })
}

type blueprintStore struct{ *store.Store }

func (s blueprintStore) InTx(ctx context.Context, fn func(blueprints.Queries) error) error {
	return s.Store.InTx(ctx, func(q *db.Queries) error { return fn(q) })
}

type runStore struct{ *store.Store }

func (s runStore) StartRun(ctx context.Context, arg runs.StartArgs) (db.Run, error) {
	var run db.Run
	err := s.InTx(ctx, func(q *db.Queries) error {
		// Before the first write, so a delete waits for this run or refuses it.
		if _, err := q.LockLiveTeam(ctx, arg.TeamID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return runs.ErrTeamDeleted
			}
			return err
		}
		sessionID := arg.SessionID
		if sessionID == nil {
			created, err := q.CreateDirectSession(ctx, db.CreateDirectSessionParams{TeamID: arg.TeamID, UserID: arg.UserID})
			if err != nil {
				return err
			}
			sessionID = &created.ID
		} else {
			// A session has one transcript, so one run works in it at a time:
			// the lock orders this against delivery and a run's finish.
			s, err := q.LockSession(ctx, *sessionID)
			if err != nil {
				return err
			}
			if session, err := q.GetSession(ctx, s); err != nil {
				return err
			} else if session.ChannelID != nil {
				return runs.ErrChannelSession
			}
			active, err := q.ActiveSessionRun(ctx, *sessionID)
			switch {
			case err == nil:
				if err := q.AddSteer(ctx, db.AddSteerParams{RunID: active, Text: arg.Input}); err != nil {
					return fmt.Errorf("steer run %s: %w", active, err)
				}
				run, err = q.GetRun(ctx, active)
				return err
			case !errors.Is(err, pgx.ErrNoRows):
				return err
			}
		}

		var err error
		if arg.Builder {
			run, err = q.CreateBuilderRun(ctx, db.CreateBuilderRunParams{
				ID: arg.ID, SessionID: *sessionID, UserID: &arg.UserID, Input: arg.Input, TeamID: arg.TeamID,
			})
			return err
		}
		if arg.Hub {
			run, err = q.CreateHubRun(ctx, db.CreateHubRunParams{
				ID: arg.ID, SessionID: *sessionID, UserID: &arg.UserID, Input: arg.Input, TeamID: arg.TeamID,
			})
			return err
		}
		run, err = q.CreateRun(ctx, db.CreateRunParams{
			ID: arg.ID, SessionID: *sessionID, UserID: &arg.UserID,
			Input: arg.Input, AgentID: arg.AgentID, TeamID: arg.TeamID,
		})
		return err
	})
	return run, err
}
