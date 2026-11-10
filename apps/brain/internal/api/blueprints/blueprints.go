package blueprints

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/agents"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/skills"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// uploaded is where a blueprint's skills come from: their bytes are sent.
var uploaded = skills.Origin{Source: "upload"}

const (
	codeUniqueViolation = "23505"
	maxItems            = 50
	// Skills travel as base64, a third larger than their bytes.
	maxBodyBytes = 64 << 20
)

type Store interface {
	Reads
	skills.Reserver
	InTx(ctx context.Context, fn func(Queries) error) error
}

// Queries is every write an apply makes, in one transaction.
type Queries interface {
	agents.Queries
	skills.Queries
	CreateMCPServer(ctx context.Context, arg db.CreateMCPServerParams) (db.McpServer, error)
	UpdateMCPServer(ctx context.Context, arg db.UpdateMCPServerParams) (db.McpServer, error)
}

type handler struct {
	logger  *slog.Logger
	store   Store
	authz   api.Authorizer
	objects skills.Objects
	loops   []string
}

func New(logger *slog.Logger, s Store, a api.Authorizer, o skills.Objects, loops []string) *api.Router {
	h := &handler{logger: logger, store: s, authz: a, objects: o, loops: loops}

	r := api.NewRouter("/teams")
	r.Post("/{id}/blueprints/plan", h.plan)
	r.Post("/{id}/blueprints/apply", h.apply)

	return r
}

func (h *handler) plan(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	var bp Blueprint
	if !api.DecodeJSONWithin(w, r, &bp, maxBodyBytes) || !withinLimits(w, bp) {
		return
	}

	p, ok := h.planned(w, r, u, teamID, bp)
	if !ok {
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, p.Plan)
}

func (h *handler) apply(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	var req applyRequest
	if !api.DecodeJSONWithin(w, r, &req, maxBodyBytes) || !withinLimits(w, req.Blueprint) {
		return
	}
	if req.PlanDigest == "" {
		http.Error(w, "plan_digest is required: plan first, and apply the plan you were shown", http.StatusBadRequest)
		return
	}

	p, ok := h.planned(w, r, u, teamID, req.Blueprint)
	switch {
	case !ok:
		return
	case len(p.Refusals) > 0:
		api.WriteJSON(w, h.logger, http.StatusUnprocessableEntity, p.Plan)
		return
	case p.Digest != req.PlanDigest:
		api.WriteJSON(w, h.logger, http.StatusConflict, p.Plan)
		return
	}

	err := h.write(r.Context(), teamID, &p)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == codeUniqueViolation {
		http.Error(w, "the team changed while this was applied: plan again", http.StatusConflict)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to apply blueprint", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, applied{Changes: p.Changes})
}

func withinLimits(w http.ResponseWriter, bp Blueprint) bool {
	if len(bp.MCPServers) > maxItems || len(bp.Skills) > maxItems || len(bp.Agents) > maxItems {
		http.Error(w, fmt.Sprintf("a blueprint holds at most %d of each kind", maxItems), http.StatusBadRequest)
		return false
	}
	return true
}

func (h *handler) planned(w http.ResponseWriter, r *http.Request, u *auth.User, teamID uuid.UUID, bp Blueprint) (planned, bool) {
	m := may{}
	for k, perm := range permissionOf {
		ok, err := h.authz.Can(r.Context(), u, authz.Action(perm), authz.Resource{TeamID: teamID})
		if err != nil {
			api.Fail(w, h.logger, u, "failed to check "+perm, err)
			return planned{}, false
		}
		m[k] = ok
	}

	t, err := load(r.Context(), h.store, teamID)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the team", err)
		return planned{}, false
	}
	return planOf(t, bp, h.loops, m), true
}

// write makes a plan's changes: changed skills' files first, reserved so the
// reaper takes them if nothing commits, then every row in one transaction.
// Agents are written before any grant, so one may delegate to another the
// same blueprint creates.
func (h *handler) write(ctx context.Context, teamID uuid.UUID, p *planned) error {
	keys := make([][]string, len(p.skills))
	for i, s := range p.skills {
		k, err := skills.Reserve(ctx, h.store, h.objects, teamID, s.dir.Files)
		if err != nil {
			return err
		}
		keys[i] = k
	}

	ids := map[Kind]map[string]uuid.UUID{KindMCPServer: {}, KindSkill: {}, KindAgent: {}}
	for _, c := range p.Changes {
		if c.ID != nil {
			ids[c.Kind][c.Name] = *c.ID
		}
	}

	return h.store.InTx(ctx, func(q Queries) error {
		if err := writeServers(ctx, q, teamID, p, ids); err != nil {
			return err
		}
		if err := writeSkills(ctx, q, teamID, p, keys, ids); err != nil {
			return err
		}
		return writeAgents(ctx, q, p, ids)
	})
}

func writeServers(ctx context.Context, q Queries, teamID uuid.UUID, p *planned, ids map[Kind]map[string]uuid.UUID) error {
	for _, s := range p.servers {
		f := s.fields
		var row db.McpServer
		var err error
		if s.id == uuid.Nil {
			row, err = q.CreateMCPServer(ctx, db.CreateMCPServerParams{
				TeamID: teamID, Name: f.Name, Url: f.URL, Command: f.Command, Env: f.Env,
				AuthSecretRef: f.AuthSecretRef, Bare: f.Bare,
			})
		} else {
			row, err = q.UpdateMCPServer(ctx, db.UpdateMCPServerParams{
				ID: s.id, Name: f.Name, Url: f.URL, Command: f.Command, Env: f.Env,
				AuthSecretRef: f.AuthSecretRef, Bare: f.Bare,
			})
		}
		if err != nil {
			return err
		}
		p.made(s.change, row.ID, ids)
	}
	return nil
}

func writeSkills(ctx context.Context, q Queries, teamID uuid.UUID, p *planned, keys [][]string, ids map[Kind]map[string]uuid.UUID) error {
	for i, s := range p.skills {
		var row db.Skill
		var err error
		if s.id == uuid.Nil {
			row, err = skills.Create(ctx, q, teamID, s.dir, uploaded, keys[i])
		} else {
			row, err = skills.Replace(ctx, q, s.id, s.dir, uploaded, keys[i])
		}
		if err != nil {
			return err
		}
		p.made(s.change, row.ID, ids)
	}
	return nil
}

func writeAgents(ctx context.Context, q Queries, p *planned, ids map[Kind]map[string]uuid.UUID) error {
	rows := make([]db.Agent, len(p.agents))
	for i, a := range p.agents {
		var err error
		if a.id == uuid.Nil {
			rows[i], err = q.CreateAgent(ctx, a.params)
		} else {
			rows[i], err = q.UpdateAgent(ctx, agents.UpdateParams(a.id, a.params))
		}
		if err != nil {
			return err
		}
		p.made(a.change, rows[i].ID, ids)
	}

	// What was just written, then what the team already held; never a
	// stand-in, which is only for planning.
	idOf := func(k Kind, name string) (uuid.UUID, bool) {
		if id, ok := ids[k][name]; ok {
			return id, true
		}
		return p.team.existing(k, name)
	}
	for i, a := range p.agents {
		req, err := resolve(a.spec, idOf)
		if err != nil {
			return err
		}
		if err := agents.Regrant(ctx, q, rows[i], req); err != nil {
			return err
		}
	}
	return nil
}

// made records the id a write gave a change.
func (p *planned) made(change int, id uuid.UUID, ids map[Kind]map[string]uuid.UUID) {
	c := &p.Changes[change]
	c.ID = &id
	ids[c.Kind][c.Name] = id
}
