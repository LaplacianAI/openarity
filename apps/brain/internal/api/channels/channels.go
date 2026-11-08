package channels

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	maxNameBytes        = 200
	codeUniqueViolation = "23505"

	secretBytes  = 32
	secretPrefix = "oawh_"

	setupReadLimit = 64 << 10
)

type Store interface {
	CreateChannel(ctx context.Context, arg db.CreateChannelParams) (db.Channel, error)
	SetChannelReplyURL(ctx context.Context, arg db.SetChannelReplyURLParams) (db.Channel, error)
	GetChannel(ctx context.Context, id uuid.UUID) (db.Channel, error)
	ListChannelsByTeam(ctx context.Context, arg db.ListChannelsByTeamParams) ([]db.ListChannelsByTeamRow, error)
	DeleteChannel(ctx context.Context, id uuid.UUID) error

	ListPendingSenders(ctx context.Context, arg db.ListPendingSendersParams) ([]db.PendingSender, error)
	ListTeamPendingSenders(ctx context.Context, arg db.ListTeamPendingSendersParams) ([]db.ListTeamPendingSendersRow, error)
	ListChannelSenders(ctx context.Context, arg db.ListChannelSendersParams) ([]db.ChannelSender, error)
	FindTeamMember(ctx context.Context, arg db.FindTeamMemberParams) (string, error)
	FindChannelSender(ctx context.Context, arg db.FindChannelSenderParams) (uuid.UUID, error)
	ListRolePermissions(ctx context.Context, role string) ([]string, error)
	ApproveSender(ctx context.Context, arg db.ApproveSenderParams) (int64, error)
	RemoveSender(ctx context.Context, arg db.RemoveSenderParams) (bool, error)
}

type Secrets interface {
	PutAll(ctx context.Context, path string, values map[string]string) error
	Delete(ctx context.Context, path string) error
}

type Providers interface {
	Get(name string) (gateway.Provider, bool)
	Replier(name string) (gateway.Replier, bool)
	Setup(name string) (gateway.Setup, bool)
	TakesReplyURL(name string) bool
}

type handler struct {
	logger    *slog.Logger
	store     Store
	authz     api.Authorizer
	secrets   Secrets
	providers Providers
	hookBase  string
	// allow is which addresses a reply URL may name; the sender's dialer
	// asks the same at send time.
	allow func(netip.Addr) bool
	// setup asks a provider about the credentials a channel is connected
	// with. It refuses what a team may not reach and follows no redirect.
	setup *http.Client
}

func New(
	logger *slog.Logger, s Store, a api.Authorizer, sec Secrets, p Providers, hookBase string,
	allow func(netip.Addr) bool, setup *http.Client,
) *api.Router {
	h := &handler{
		logger: logger, store: s, authz: a, secrets: sec, providers: p,
		hookBase: strings.TrimSuffix(hookBase, "/"), allow: allow, setup: setup,
	}

	r := api.NewRouter("/teams")
	r.Get("/{id}/channels", h.list)
	r.Post("/{id}/channels", h.create)
	r.Patch("/{id}/channels/{channelID}", h.update)
	r.Delete("/{id}/channels/{channelID}", h.delete)

	senders := "/{id}/channels/{channelID}/senders"
	r.Get(senders+"/pending", h.listPending)
	r.Get(senders, h.listSenders)
	r.Get("/{id}/senders/pending", h.listTeamPending)
	r.Post(senders, h.approve)
	r.Delete(senders, h.removeSender)

	return r
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	var req createRequest
	if !api.DecodeJSON(w, r, &req) {
		return
	}

	name, ok := api.Name(req.Name, maxNameBytes)
	if !ok {
		http.Error(w, "name must be between 1 and 200 characters", http.StatusBadRequest)
		return
	}

	if _, known := h.providers.Get(req.Provider); !known {
		http.Error(w, "unknown provider", http.StatusBadRequest)
		return
	}
	var replyURL *string
	if req.ReplyURL != nil {
		v := strings.TrimSpace(*req.ReplyURL)
		if !h.replyURLOK(w, req.Provider, v) {
			return
		}
		replyURL = &v
	}

	secret, generated, ok := h.signingSecret(w, req)
	if !ok {
		return
	}
	creds, ok := h.credentials(w, r, req)
	if !ok {
		return
	}
	creds[gateway.KeySigning] = secret

	row, err := h.store.CreateChannel(r.Context(), db.CreateChannelParams{
		TeamID: teamID, Provider: req.Provider, Name: name, ReplyUrl: replyURL,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == codeUniqueViolation {
			http.Error(w, "a channel with that name already exists", http.StatusConflict)
			return
		}
		api.Fail(w, h.logger, u, "failed to create channel", err)
		return
	}

	// One write: a store that keeps a path as one secret would keep only the
	// last of several.
	if err := h.secrets.PutAll(r.Context(),
		secrets.Path(teamID, secrets.KindChannel, row.ID), creds); err != nil {

		if err := h.store.DeleteChannel(r.Context(), row.ID); err != nil {
			h.logger.Error("channel row left behind with no secret",
				"channel_id", row.ID, "error", err)
		}
		h.logger.Error("failed to store the channel secret",
			"subject", u.Subject, "channel_id", row.ID, "error", err)
		http.Error(w, "the secret store is unavailable", http.StatusServiceUnavailable)
		return
	}

	out := created{channel: h.view(row)}
	if generated {
		out.SigningSecret = secret
	}
	api.WriteJSON(w, h.logger, http.StatusCreated, out)
}

func (h *handler) signingSecret(w http.ResponseWriter, req createRequest) (string, bool, bool) {
	if req.SigningSecret == nil {
		secret, err := newSecret()
		if err != nil {
			h.logger.Error("failed to generate a signing secret", "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return "", false, false
		}
		return secret, true, true
	}
	supplied := strings.TrimSpace(*req.SigningSecret)
	if supplied == "" {
		http.Error(w, "signing_secret must not be empty; omit it to have one generated", http.StatusBadRequest)
		return "", false, false
	}
	return supplied, false, true
}

// credentials is what a channel is connected with besides its signing secret:
// the keys its provider asks a person for, completed from the provider's own
// API, Telegram's bot from getMe. A provider that asks for nothing takes
// nothing, so a key it would never read is not stored.
func (h *handler) credentials(w http.ResponseWriter, r *http.Request, req createRequest) (map[string]string, bool) {
	s, asks := h.providers.Setup(req.Provider)
	if !asks {
		if len(req.Credentials) > 0 {
			http.Error(w, "the "+req.Provider+" provider takes no credentials", http.StatusBadRequest)
			return nil, false
		}
		return map[string]string{}, true
	}

	supplied := gateway.Credentials{}
	for k := range req.Credentials {
		if !slices.Contains(s.Supplied(), k) {
			http.Error(w, "credentials."+k+" is not something a "+req.Provider+" channel takes", http.StatusBadRequest)
			return nil, false
		}
	}
	for _, k := range s.Supplied() {
		v := strings.TrimSpace(req.Credentials[k])
		if v == "" {
			http.Error(w, "credentials."+k+" is required for a "+req.Provider+" channel", http.StatusBadRequest)
			return nil, false
		}
		supplied[k] = v
	}

	out, err := s.SetupRequest(supplied)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, false
	}
	status, body, err := h.ask(r.Context(), out)
	if err != nil {
		h.logger.WarnContext(r.Context(), "could not reach a provider to set up a channel",
			"provider", req.Provider, "error", err)
		http.Error(w, "could not reach "+req.Provider+" to check the credentials; try again", http.StatusBadGateway)
		return nil, false
	}
	completed, err := s.Complete(status, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, false
	}

	// Only keys the provider declares, and never the signing secret: what a
	// provider's answer says cannot widen what the channel holds.
	p, _ := h.providers.Get(req.Provider)
	creds := map[string]string{}
	for k, v := range completed {
		if k != gateway.KeySigning && slices.Contains(p.Keys(), k) {
			creds[k] = v
		}
	}
	maps.Copy(creds, supplied)
	return creds, true
}

// ask sends a provider's setup request. The URL can hold a credential, as a
// bot token does, so an error never quotes it.
func (h *handler) ask(ctx context.Context, out gateway.OutboundRequest) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, out.URL, bytes.NewReader(out.Body))
	if err != nil {
		return 0, nil, errors.New("the provider's setup request is malformed")
	}
	req.Header = out.Header.Clone()
	resp, err := h.setup.Do(req) //nolint:gosec // G704: the client's dialer refuses what the team may not reach
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, setupReadLimit))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

func newSecret() (string, error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return secretPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}

	params, ok := h.page(w, r, teamID, limit)
	if !ok {
		return
	}
	params.Moderator = h.readsEveryConversation(r, u, teamID)
	params.Viewer = u.ID

	rows, err := h.store.ListChannelsByTeam(r.Context(), params)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list channels", err)
		return
	}

	page, err := api.MapPage(rows, limit,
		func(row db.ListChannelsByTeamRow) any {
			return channelCursor{CreatedAt: row.Channel.CreatedAt, ID: row.Channel.ID}
		},
		h.viewListed,
	)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to page channels", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusOK, page)
}

func (h *handler) page(w http.ResponseWriter, r *http.Request, teamID uuid.UUID, limit int32) (db.ListChannelsByTeamParams, bool) {
	params := db.ListChannelsByTeamParams{TeamID: teamID, PageSize: limit + 1}

	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return params, true
	}

	var c channelCursor
	if !api.DecodeCursor(w, raw, &c) {
		return params, false
	}

	params.UseCursor = true
	params.AfterCreatedAt = c.CreatedAt
	params.AfterID = c.ID
	return params, true
}

// update sets where the channel's replies go, or with null stops them.
func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	row, ok := h.channelInTeam(w, r)
	if !ok {
		return
	}

	var req updateRequest
	if !api.DecodeJSON(w, r, &req) {
		return
	}
	if !req.ReplyURL.Set {
		http.Error(w, "reply_url is required; send null to stop replies", http.StatusBadRequest)
		return
	}
	// "" stops replies as null does: a client generated from the spec cannot
	// always send a null for a required field.
	if v := req.ReplyURL.Value; v != nil && *v == "" {
		req.ReplyURL.Value = nil
	}
	if v := req.ReplyURL.Value; v != nil && !h.replyURLOK(w, row.Provider, *v) {
		return
	}

	updated, err := h.store.SetChannelReplyURL(r.Context(), db.SetChannelReplyURLParams{
		ID: row.ID, TeamID: row.TeamID, ReplyUrl: req.ReplyURL.Value,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to update channel", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, h.view(updated))
}

func (h *handler) replyURLOK(w http.ResponseWriter, provider, raw string) bool {
	if _, ok := h.providers.Replier(provider); !ok {
		http.Error(w, "the "+provider+" provider sends no replies, so it takes no reply_url", http.StatusBadRequest)
		return false
	}
	if !h.providers.TakesReplyURL(provider) {
		http.Error(w, "the "+provider+" provider replies in the conversation itself, so it takes no reply_url",
			http.StatusBadRequest)
		return false
	}
	if err := checkReplyURL(raw, h.allow); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	channelID, err := uuid.Parse(r.PathValue("channelID"))
	if err != nil {
		http.Error(w, "channel id must be a uuid", http.StatusBadRequest)
		return
	}

	row, err := h.store.GetChannel(r.Context(), channelID)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read channel", err)
		return
	}

	if row.TeamID != teamID {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if err := h.store.DeleteChannel(r.Context(), channelID); err != nil {
		api.Fail(w, h.logger, u, "failed to delete channel", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) view(row db.Channel) channel {
	c := channel{ID: row.ID, TeamID: row.TeamID, Provider: row.Provider, Name: row.Name}
	if row.ReplyUrl != nil {
		c.ReplyURL = *row.ReplyUrl
	}
	_, c.CanReply = h.providers.Replier(row.Provider)
	c.TakesReplyURL = h.providers.TakesReplyURL(row.Provider)
	if h.hookBase == "" {
		return c
	}

	p, ok := h.providers.Get(row.Provider)
	if !ok || len(p.Routes()) == 0 {
		return c
	}
	c.HookURL = h.hookBase + "/hooks/" + row.Provider + "/" + row.ID.String() + p.Routes()[0].Suffix
	return c
}

func (h *handler) viewListed(row db.ListChannelsByTeamRow) listedChannel {
	return listedChannel{
		channel: h.view(row.Channel), LastMessageAt: row.LastMessageAt, Sessions24h: row.Sessions24h,
	}
}

// readsEveryConversation is whether the caller counts direct sessions they are
// not in. A failed check counts less and says so in the log, as the summary does.
func (h *handler) readsEveryConversation(r *http.Request, u *auth.User, teamID uuid.UUID) bool {
	ok, err := h.authz.Can(r.Context(), u, authz.Action("session:read_all"), authz.Resource{TeamID: teamID})
	if err != nil {
		h.logger.ErrorContext(r.Context(), "failed to check session:read_all", "error", err, "team_id", teamID)
		return false
	}
	return ok
}
