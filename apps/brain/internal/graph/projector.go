package graph

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	drainLockID int64 = 3384301360
	retryAfter        = 30 * time.Second
)

type entity string

const (
	entityTeam   entity = "team"
	entityServer entity = "mcp_server"
	entitySkill  entity = "skill"
	entityTool   entity = "mcp_tool"
	entityAgent  entity = "agent"
)

var projectionOrder = []entity{entityTeam, entityServer, entitySkill, entityTool, entityAgent}

type Store interface {
	WithAdvisoryLock(ctx context.Context, key int64, fn func(context.Context) error) (bool, error)
	ClaimGraphOutbox(ctx context.Context, arg db.ClaimGraphOutboxParams) ([]db.GraphOutbox, error)
	ForgetGraphOutbox(ctx context.Context, ids []int64) error
	GraphOutboxBacklog(ctx context.Context) ([]db.GraphOutboxBacklogRow, error)
	GetTeam(ctx context.Context, id uuid.UUID) (db.Team, error)
	GetAgent(ctx context.Context, id uuid.UUID) (db.Agent, error)
	GetMCPServer(ctx context.Context, id uuid.UUID) (db.McpServer, error)
	GetMCPTool(ctx context.Context, id uuid.UUID) (db.McpTool, error)
	GetSkill(ctx context.Context, id uuid.UUID) (db.Skill, error)
	ListAgentMCPServers(ctx context.Context, agentID uuid.UUID) ([]db.AgentMcpServer, error)
	ListAgentSkills(ctx context.Context, agentID uuid.UUID) ([]uuid.UUID, error)
	ListGraphEntities(ctx context.Context, teamID uuid.UUID) ([]db.ListGraphEntitiesRow, error)
}

type Projector struct {
	store  Store
	graph  *Graph
	embed  *Embedder
	logger *slog.Logger
	lockID int64
}

type Result struct {
	Projected int
	Failed    int
	Busy      bool
}

type target struct {
	entity entity
	id     uuid.UUID
}

type change struct {
	target
	rows     []int64
	attempts int32
}

func NewProjector(s Store, g *Graph, e *Embedder, logger *slog.Logger) *Projector {
	return &Projector{store: s, graph: g, embed: e, logger: logger, lockID: drainLockID}
}

func (p *Projector) Drain(ctx context.Context, batch int32) (Result, error) {
	var res Result
	retryBefore := time.Now().Add(-retryAfter)

	held, err := p.store.WithAdvisoryLock(ctx, p.lockID, func(ctx context.Context) error {
		for ctx.Err() == nil {
			claimed, err := p.store.ClaimGraphOutbox(ctx, db.ClaimGraphOutboxParams{
				RetryBefore: &retryBefore, BatchSize: batch,
			})
			if err != nil {
				return fmt.Errorf("graph: claiming outbox rows: %w", err)
			}
			if len(claimed) == 0 {
				return nil
			}
			if err := p.batch(ctx, claimed, &res); err != nil {
				return err
			}
		}
		return ctx.Err()
	})

	res.Busy = !held && err == nil
	return res, err
}

func (p *Projector) batch(ctx context.Context, claimed []db.GraphOutbox, res *Result) error {
	var teams []uuid.UUID
	byTeam := map[uuid.UUID][]db.GraphOutbox{}
	for _, row := range claimed {
		if _, seen := byTeam[row.TeamID]; !seen {
			teams = append(teams, row.TeamID)
		}
		byTeam[row.TeamID] = append(byTeam[row.TeamID], row)
	}

	for _, team := range teams {
		done := p.team(ctx, team, byTeam[team])
		res.Projected += len(done)
		res.Failed += len(byTeam[team]) - len(done)

		if len(done) == 0 {
			continue
		}
		if err := p.store.ForgetGraphOutbox(ctx, done); err != nil {
			return fmt.Errorf("graph: clearing projected outbox rows: %w", err)
		}
	}
	return nil
}

func ids(rows []db.GraphOutbox) []int64 {
	out := make([]int64, len(rows))
	for i, row := range rows {
		out[i] = row.ID
	}
	return out
}

func (p *Projector) team(ctx context.Context, team uuid.UUID, rows []db.GraphOutbox) []int64 {
	_, err := p.store.GetTeam(ctx, team)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := p.graph.DropTeam(ctx, team); err != nil {
			p.logger.ErrorContext(ctx, "graph: a deleted team's graph was not dropped", "team_id", team, "error", err)
			return nil
		}
		return ids(rows)
	}
	if err == nil {
		err = p.graph.EnsureIndexes(ctx, team)
	}
	if err == nil {
		err = p.graph.PutNode(ctx, team, Node{Ref: Ref{Label: LabelTeam, ID: team}})
	}

	if err != nil {
		p.logger.ErrorContext(ctx, "graph: a team's graph could not be prepared",
			"team_id", team, "rows", len(rows), "error", err)
		return nil
	}

	var done []int64
	for _, c := range coalesce(rows) {
		if ctx.Err() != nil {
			break
		}
		if err := p.project(ctx, team, c.entity, c.id); err != nil {
			level := slog.LevelWarn
			if c.attempts > 3 {
				level = slog.LevelError
			}
			p.logger.Log(ctx, level, "graph: a change was not projected",
				"team_id", team, "entity", string(c.entity), "entity_id", c.id,
				"attempts", c.attempts, "error", err)
			continue
		}
		done = append(done, c.rows...)
	}
	return done
}

func (p *Projector) project(ctx context.Context, team uuid.UUID, e entity, id uuid.UUID) error {
	switch e {
	case entityTeam:
		return nil
	case entityServer:
		return p.server(ctx, team, id)
	case entitySkill:
		return p.skill(ctx, team, id)
	case entityTool:
		return p.tool(ctx, team, id)
	case entityAgent:
		return p.agent(ctx, team, id)
	default:
		return fmt.Errorf("graph: the outbox names a %q, which nothing here projects", string(e))
	}
}

func (p *Projector) server(ctx context.Context, team, id uuid.UUID) error {
	ref := Ref{Label: LabelToolkit, ID: id}

	row, err := p.store.GetMCPServer(ctx, id)
	if gone(err, row.TeamID, team) {
		return p.graph.DeleteNode(ctx, team, ref)
	}
	if err != nil {
		return err
	}

	return p.owned(ctx, team, Node{Ref: ref, Props: map[string]any{"name": row.Name, "kind": "mcp"}})
}

func (p *Projector) skill(ctx context.Context, team, id uuid.UUID) error {
	ref := Ref{Label: LabelSkill, ID: id}

	row, err := p.store.GetSkill(ctx, id)
	if gone(err, row.TeamID, team) {
		return p.graph.DeleteNode(ctx, team, ref)
	}
	if err != nil {
		return err
	}

	n, err := p.embedded(ctx, team, ref, row.Name, row.Description)
	if err != nil {
		return err
	}
	return p.owned(ctx, team, n)
}

func (p *Projector) tool(ctx context.Context, team, id uuid.UUID) error {
	ref := Ref{Label: LabelTool, ID: id}

	row, err := p.store.GetMCPTool(ctx, id)
	if gone(err, row.TeamID, team) {
		return p.graph.DeleteNode(ctx, team, ref)
	}
	if err != nil {
		return err
	}

	n, err := p.embedded(ctx, team, ref, row.Name, row.Description)
	if err != nil {
		return err
	}
	if err := p.graph.PutNode(ctx, team, n); err != nil {
		return err
	}
	return p.graph.Link(ctx, team, Ref{Label: LabelToolkit, ID: row.McpServerID}, EdgeContains, ref)
}

func (p *Projector) agent(ctx context.Context, team, id uuid.UUID) error {
	ref := Ref{Label: LabelAgent, ID: id}

	row, err := p.store.GetAgent(ctx, id)
	if gone(err, row.TeamID, team) {
		return p.graph.DeleteNode(ctx, team, ref)
	}
	if err != nil {
		return err
	}

	grants, err := p.store.ListAgentMCPServers(ctx, id)
	if err != nil {
		return err
	}
	skills, err := p.store.ListAgentSkills(ctx, id)
	if err != nil {
		return err
	}

	n, err := p.embedded(ctx, team, ref, row.Name, row.Description)
	if err != nil {
		return err
	}
	n.Props["kind"] = row.Kind
	if err := p.owned(ctx, team, n); err != nil {
		return err
	}

	granted := make([]Edge, len(grants))
	for i, g := range grants {
		granted[i] = Edge{To: g.McpServerID}
		if g.Allow != nil {
			granted[i].Props = map[string]any{"only": g.Allow}
		}
	}
	if err := p.graph.SetOutEdges(ctx, team, ref, EdgeGranted, LabelToolkit, granted); err != nil {
		return err
	}

	has := make([]Edge, len(skills))
	for i, s := range skills {
		has[i] = Edge{To: s}
	}
	return p.graph.SetOutEdges(ctx, team, ref, EdgeHasSkill, LabelSkill, has)
}

func (p *Projector) owned(ctx context.Context, team uuid.UUID, n Node) error {
	if err := p.graph.PutNode(ctx, team, n); err != nil {
		return err
	}
	return p.graph.Link(ctx, team, Ref{Label: LabelTeam, ID: team}, EdgeOwns, n.Ref)
}

func (p *Projector) embedded(ctx context.Context, team uuid.UUID, ref Ref, name, description string) (Node, error) {
	n := Node{Ref: ref, Props: map[string]any{"name": name, "description": description}}

	text := name + "\n" + description
	sum := sha256.Sum256([]byte(text))

	model, sha, _, err := p.graph.Fingerprint(ctx, team, ref)
	if err != nil {
		return Node{}, err
	}
	if model == p.embed.Model() && bytes.Equal(sha, sum[:]) {
		return n, nil
	}

	vectors, err := p.embed.Embed(ctx, []string{text})
	if err != nil {
		return Node{}, err
	}
	n.Embedding, n.Model, n.ContentSHA256 = vectors[0], p.embed.Model(), sum[:]
	return n, nil
}

func (p *Projector) Backlog(ctx context.Context) (outstanding int64, oldest time.Time, err error) {
	rows, err := p.store.GraphOutboxBacklog(ctx)
	if err != nil || len(rows) == 0 {
		return 0, time.Time{}, err
	}
	return rows[0].Outstanding, rows[0].Oldest, nil
}

func gone(err error, owner, team uuid.UUID) bool {
	return errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != team)
}

func coalesce(rows []db.GraphOutbox) []change {
	var out []change
	at := map[target]int{}

	for _, row := range rows {
		t := target{entity: entity(row.Entity), id: row.EntityID}
		i, seen := at[t]
		if !seen {
			i = len(out)
			at[t] = i
			out = append(out, change{target: t})
		}
		out[i].rows = append(out[i].rows, row.ID)
		out[i].attempts = max(out[i].attempts, row.Attempts)
	}

	slices.SortStableFunc(out, func(a, b change) int {
		return slices.Index(projectionOrder, a.entity) - slices.Index(projectionOrder, b.entity)
	})
	return out
}
