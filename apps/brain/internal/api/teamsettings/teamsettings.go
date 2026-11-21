package teamsettings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	browserpkg "github.com/LaplacianAI/openarity/apps/brain/internal/browser"
	sandboxpkg "github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	KeyAPI            = "api_key"
	defaultCompaction = 100
	// A tenth of the runs judged done are digested, beside every bad one, so
	// the improver has successes to contrast with.
	defaultSampleShare = 10
	defaultDailyPasses = 20
	defaultPassTokens  = 400_000
	defaultPassMinutes = 20
	// The brain's line before teams could set it, kept for a team that
	// never saved its settings.
	defaultCompactionTokensMin = 50_000
	// defaultKeepHours is how long a stopped workspace keeps its files, as
	// the table's default has it.
	defaultKeepHours           = 24
	defaultCompactionTokensMax = 60_000
)

type Store interface {
	GetTeamSettings(ctx context.Context, teamID uuid.UUID) (db.TeamSetting, error)
	UpsertTeamSettings(ctx context.Context, arg db.UpsertTeamSettingsParams) (db.TeamSetting, error)
	RecordDeletedSecret(ctx context.Context, arg db.RecordDeletedSecretParams) error
	GetAgent(ctx context.Context, id uuid.UUID) (db.Agent, error)
	GetAgentByName(ctx context.Context, arg db.GetAgentByNameParams) (db.Agent, error)
}

type Secrets interface {
	Put(ctx context.Context, path, key, value string) error
}

// Templates builds the room template in an E2B team's account.
type Templates interface {
	Want(ctx context.Context, team, key uuid.UUID, retry time.Duration) (db.E2BTemplate, error)
	Current(ctx context.Context, team, key uuid.UUID) (db.E2BTemplate, bool, error)
}

type handler struct {
	logger  *slog.Logger
	store   Store
	secrets Secrets
	allow   func(netip.Addr) bool
	// templates is nil in a brain that cannot keep rooms; an E2B team's
	// settings then show no build.
	templates Templates
}

func New(logger *slog.Logger, s Store, sec Secrets, allow func(netip.Addr) bool, tpl Templates) *api.Router {
	h := &handler{logger: logger, store: s, secrets: sec, allow: allow, templates: tpl}

	r := api.NewRouter("/teams")
	r.Get("/{id}/settings", h.get)
	r.Put("/{id}/settings", h.put)
	return r
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	row, found, err := h.stored(r.Context(), teamID)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read team settings", err)
		return
	}
	if !found {
		api.WriteJSON(w, h.logger, http.StatusOK, defaults())
		return
	}

	hub, err := h.hubOf(r.Context(), teamID, row.HubAgentID)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the team's hub", err)
		return
	}

	out := view(row, hub)
	out.Sandbox.TemplateBuild = h.build(r.Context(), row, readBuild)
	api.WriteJSON(w, h.logger, http.StatusOK, out)
}

func (h *handler) put(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	var req settingsRequest
	if !api.DecodeJSON(w, r, &req) {
		return
	}

	row, found, err := h.stored(r.Context(), teamID)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read team settings", err)
		return
	}
	var stored *db.TeamSetting
	if found {
		stored = &row
	}

	if err := validate(req, stored, h.allow); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	hub, ok := h.hub(w, r, teamID, req.Hub)
	if !ok {
		return
	}

	secretID, written, ok := h.searchKey(w, r, teamID, req.Search, stored)
	if !ok {
		return
	}
	box, boxWritten, ok := h.sandboxOf(w, r, teamID, req.Sandbox, stored)
	if !ok {
		if written != "" {
			h.forget(r.Context(), teamID, written)
		}
		return
	}
	web, webWritten, ok := h.browserOf(w, r, teamID, req.Browser, stored)
	if !ok {
		for _, path := range []string{written, boxWritten} {
			if path != "" {
				h.forget(r.Context(), teamID, path)
			}
		}
		return
	}

	p := params(teamID, req, secretID, hub)
	p.CompactionTokensMin, p.CompactionTokensMax = compactionTokens(req, stored)
	if m := judgeModel(req, stored); m != nil {
		p.JudgeModelName, p.JudgeModelMaxTokens, p.JudgeModelTemperature = &m.Name, &m.MaxTokens, m.Temperature
	}
	p.ImprovementSampleShare = sampleShare(req, stored)
	p.ImprovementDailyPasses = req.ImprovementDailyPasses
	p.ImprovementPassTokens, p.ImprovementPassMinutes = req.ImprovementPassTokens, req.ImprovementPassMinutes
	p.SandboxProvider, p.SandboxUrl, p.SandboxTemplate, p.SandboxSecretID = box.provider, box.url, box.template, box.secretID
	lifecycle(&p, req.Sandbox, stored)
	p.BrowserProvider, p.BrowserUrl, p.BrowserSecretID = web.provider, web.url, web.secretID
	saved, err := h.store.UpsertTeamSettings(r.Context(), p)
	if err != nil {
		for _, path := range []string{written, boxWritten, webWritten} {
			if path != "" {
				h.forget(r.Context(), teamID, path)
			}
		}
		if raced(err) {
			http.Error(w, "the team's compaction settings changed while saving; read them again and retry", http.StatusConflict)
			return
		}
		api.Fail(w, h.logger, u, "failed to save team settings", err)
		return
	}
	out := view(saved, hub)
	when := readBuild
	if req.Sandbox != nil {
		when = askForBuild
	}
	out.Sandbox.TemplateBuild = h.build(r.Context(), saved, when)
	api.WriteJSON(w, h.logger, http.StatusOK, out)
}

// buildRequest is whether a call only reads the room template's build or
// may ask for one.
type buildRequest bool

const (
	readBuild   buildRequest = false
	askForBuild buildRequest = true
)

// build is the room template's build for an E2B team that names no template
// of its own. A save of the sandbox asks for one when there is none or the
// last failed, so the team's first room does not wait for it; a read, and a
// save of anything else, only reads. Trouble is logged and shown as no
// build: the settings were saved, and the next room asks again.
func (h *handler) build(ctx context.Context, row db.TeamSetting, req buildRequest) *templateBuild {
	if h.templates == nil || row.SandboxProvider != string(sandboxpkg.E2B) ||
		row.SandboxTemplate != nil || row.SandboxSecretID == nil {
		return nil
	}
	var b db.E2BTemplate
	var err error
	found := true
	if req == askForBuild {
		b, err = h.templates.Want(ctx, row.TeamID, *row.SandboxSecretID, 0)
	} else {
		b, found, err = h.templates.Current(ctx, row.TeamID, *row.SandboxSecretID)
	}
	if err != nil {
		h.logger.WarnContext(ctx, "the team's room template was not read or asked for", "team_id", row.TeamID, "error", err)
		return nil
	}
	if !found {
		return nil
	}
	return &templateBuild{Name: b.Name, Status: b.Status, Error: b.Error, UpdatedAt: b.UpdatedAt}
}

// raced is a save whose token lines were checked against settings another
// save changed before this one wrote: one sent only the lowest, the other
// only the highest, and together they cross. The table refuses the pair, so
// nothing is wrong in it; the caller reads the settings again and retries.
func raced(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23514" &&
		pg.ConstraintName == "team_settings_compaction_tokens_order"
}

func (h *handler) stored(ctx context.Context, teamID uuid.UUID) (db.TeamSetting, bool, error) {
	row, err := h.store.GetTeamSettings(ctx, teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.TeamSetting{}, false, nil
	}
	if err != nil {
		return db.TeamSetting{}, false, err
	}
	return row, true, nil
}

func (h *handler) hub(w http.ResponseWriter, r *http.Request, teamID uuid.UUID, name *string) (*hubRef, bool) {
	if name == nil {
		return nil, true
	}
	a, err := h.store.GetAgentByName(r.Context(), db.GetAgentByNameParams{TeamID: teamID, Name: *name})
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, fmt.Sprintf("no agent in this team is named %q", *name), http.StatusBadRequest)
		return nil, false
	}
	if err != nil {
		api.Fail(w, h.logger, api.Caller(r), "failed to read the hub agent", err)
		return nil, false
	}
	return &hubRef{ID: a.ID, Name: a.Name}, true
}

func (h *handler) hubOf(ctx context.Context, teamID uuid.UUID, id *uuid.UUID) (*hubRef, error) {
	if id == nil {
		return nil, nil
	}
	a, err := h.store.GetAgent(ctx, *id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if a.TeamID != teamID {
		return nil, nil
	}
	return &hubRef{ID: a.ID, Name: a.Name}, nil
}

func (h *handler) searchKey(
	w http.ResponseWriter, r *http.Request, teamID uuid.UUID, s searchRequest, stored *db.TeamSetting,
) (*uuid.UUID, string, bool) {
	switch {
	case s.APIKey != nil && *s.APIKey != "":
		id := uuid.New()
		path := secrets.Path(teamID, secrets.KindSearch, id)
		if err := h.secrets.Put(r.Context(), path, KeyAPI, *s.APIKey); err != nil {
			h.logger.Warn("search key not stored", "team", teamID, "error", err)
			http.Error(w, "the secret store is unavailable", http.StatusServiceUnavailable)
			return nil, "", false
		}
		return &id, path, true
	case s.APIKey == nil && keepsKey(stored, s):
		return stored.SearchSecretID, "", true
	default:
		return nil, "", true
	}
}

// stored sandbox, as the row keeps it.
type sandboxRow struct {
	provider string
	url      *string
	template *string
	secretID *uuid.UUID
}

// sandboxOf is the sandbox to save, and the path of a key it wrote. A request
// without one keeps what is stored; a key is written fresh, or kept when the
// provider and its URL are the same.
func (h *handler) sandboxOf(
	w http.ResponseWriter, r *http.Request, teamID uuid.UUID, s *sandboxRequest, stored *db.TeamSetting,
) (sandboxRow, string, bool) {
	if s == nil {
		if stored == nil {
			return sandboxRow{provider: string(sandboxpkg.None)}, "", true
		}
		return sandboxRow{stored.SandboxProvider, stored.SandboxUrl, stored.SandboxTemplate, stored.SandboxSecretID}, "", true
	}
	row := sandboxRow{provider: s.Provider, url: s.URL, template: s.Template}
	switch {
	case s.Provider == string(sandboxpkg.None):
		return row, "", true
	case s.APIKey != nil:
		id := uuid.New()
		path := secrets.Path(teamID, secrets.KindSandbox, id)
		if err := h.secrets.Put(r.Context(), path, KeyAPI, *s.APIKey); err != nil {
			h.logger.Warn("sandbox key not stored", "team", teamID, "error", err)
			http.Error(w, "the secret store is unavailable", http.StatusServiceUnavailable)
			return sandboxRow{}, "", false
		}
		row.secretID = &id
		return row, path, true
	default:
		row.secretID = stored.SandboxSecretID // validate made sure it is kept
		return row, "", true
	}
}

func (h *handler) forget(ctx context.Context, teamID uuid.UUID, path string) {
	err := h.store.RecordDeletedSecret(ctx, db.RecordDeletedSecretParams{Path: path, TeamID: teamID})
	if err != nil {
		h.logger.Error("a key outlived its failed save", "team", teamID, "error", err)
	}
}

func params(teamID uuid.UUID, req settingsRequest, secretID *uuid.UUID, hub *hubRef) db.UpsertTeamSettingsParams {
	p := db.UpsertTeamSettingsParams{
		TeamID:             teamID,
		SearchProvider:     req.Search.Provider,
		SearchUrl:          req.Search.URL,
		SearchSecretID:     secretID,
		Improvement:        req.Improvement,
		CompactionMessages: req.CompactionMessages,
	}
	if m := req.Model; m != nil {
		p.ModelName, p.ModelMaxTokens, p.ModelTemperature = &m.Name, &m.MaxTokens, m.Temperature
	}
	if hub != nil {
		p.HubAgentID = &hub.ID
	}
	return p
}

func view(row db.TeamSetting, hub *hubRef) settings {
	s := settings{
		Search: search{
			Provider: row.SearchProvider,
			URL:      row.SearchUrl,
			KeySet:   row.SearchSecretID != nil,
		},
		Sandbox: sandbox{
			Provider: row.SandboxProvider,
			URL:      row.SandboxUrl,
			Template: row.SandboxTemplate,
			KeySet:   row.SandboxSecretID != nil,

			KeepHours:     row.SandboxKeepHours,
			IdleMinutes:   row.SandboxIdleMinutes,
			WorkspaceSize: sizeOf(row),
		},
		Browser:                browserView(row),
		Improvement:            row.Improvement,
		ImprovementSampleShare: row.ImprovementSampleShare,
		ImprovementDailyPasses: row.ImprovementDailyPasses,
		ImprovementPassTokens:  row.ImprovementPassTokens,
		ImprovementPassMinutes: row.ImprovementPassMinutes,
		ImprovementSince:       row.ImprovementSince,
		CompactionMessages:     row.CompactionMessages,
		CompactionTokensMin:    row.CompactionTokensMin,
		CompactionTokensMax:    row.CompactionTokensMax,
		Hub:                    hub,
		UpdatedAt:              &row.UpdatedAt,
	}
	if row.ModelName != nil && row.ModelMaxTokens != nil {
		s.Model = &modelRef{Name: *row.ModelName, MaxTokens: *row.ModelMaxTokens, Temperature: row.ModelTemperature}
	}
	if row.JudgeModelName != nil && row.JudgeModelMaxTokens != nil {
		s.JudgeModel = &modelRef{Name: *row.JudgeModelName, MaxTokens: *row.JudgeModelMaxTokens, Temperature: row.JudgeModelTemperature}
	}
	return s
}

func defaults() settings {
	return settings{
		Search:                 search{Provider: providerNone},
		Sandbox:                sandbox{Provider: string(sandboxpkg.None), KeepHours: defaultKeepHours},
		Browser:                browser{Provider: string(browserpkg.None)},
		ImprovementSampleShare: defaultSampleShare,
		ImprovementDailyPasses: defaultDailyPasses,
		ImprovementPassTokens:  defaultPassTokens,
		ImprovementPassMinutes: defaultPassMinutes,
		CompactionMessages:     defaultCompaction,
		CompactionTokensMin:    defaultCompactionTokensMin,
		CompactionTokensMax:    defaultCompactionTokensMax,
	}
}

// lifecycle is the stop and keep times and the size to save: what the
// request sends, and what is stored for what it leaves out. A save of the
// model or anything else that sends no sandbox keeps all of them.
func lifecycle(p *db.UpsertTeamSettingsParams, s *sandboxRequest, stored *db.TeamSetting) {
	if stored != nil {
		p.SandboxKeepHours = &stored.SandboxKeepHours
		p.SandboxIdleMinutes = stored.SandboxIdleMinutes
		p.SandboxWorkspaceMemoryMb, p.SandboxWorkspaceCpus, p.SandboxWorkspaceDiskMb =
			stored.SandboxWorkspaceMemoryMb, stored.SandboxWorkspaceCpus, stored.SandboxWorkspaceDiskMb
	}
	if s == nil {
		return
	}
	if s.KeepHours != nil {
		p.SandboxKeepHours = s.KeepHours
	}
	if s.IdleMinutes.Set {
		p.SandboxIdleMinutes = s.IdleMinutes.Value
	}
	if s.WorkspaceSize.Set {
		p.SandboxWorkspaceMemoryMb, p.SandboxWorkspaceCpus, p.SandboxWorkspaceDiskMb = nil, nil, nil
		if z := s.WorkspaceSize.Value; z != nil {
			p.SandboxWorkspaceMemoryMb, p.SandboxWorkspaceCpus, p.SandboxWorkspaceDiskMb = &z.MemoryMB, &z.CPUs, &z.DiskMB
		}
	}
}

func sizeOf(row db.TeamSetting) *size {
	if row.SandboxWorkspaceMemoryMb == nil || row.SandboxWorkspaceCpus == nil || row.SandboxWorkspaceDiskMb == nil {
		return nil
	}
	return &size{MemoryMB: *row.SandboxWorkspaceMemoryMb, CPUs: *row.SandboxWorkspaceCpus, DiskMB: *row.SandboxWorkspaceDiskMb}
}
