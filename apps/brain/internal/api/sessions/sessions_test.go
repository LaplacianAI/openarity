package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

var errAny = errors.New("connection reset by peer")

// --- fakes ---

type fakeStore struct {
	channels []db.Channel
	sessions []db.Session
	messages []db.Message

	channelErr error
	sessionErr error
	listErr    error

	listArgs        []db.ListMessagesBySessionParams
	channelListArgs []db.ListSessionsByChannelParams
	teamListArgs    []db.ListSessionsByTeamParams
}

func (f *fakeStore) GetChannel(_ context.Context, id uuid.UUID) (db.Channel, error) {
	if f.channelErr != nil {
		return db.Channel{}, f.channelErr
	}
	for _, c := range f.channels {
		if c.ID == id {
			return c, nil
		}
	}
	return db.Channel{}, pgx.ErrNoRows
}

func (f *fakeStore) GetSession(_ context.Context, id uuid.UUID) (db.Session, error) {
	if f.sessionErr != nil {
		return db.Session{}, f.sessionErr
	}
	for _, s := range f.sessions {
		if s.ID == id {
			return s, nil
		}
	}
	return db.Session{}, pgx.ErrNoRows
}

func (f *fakeStore) ListSessionsByChannel(
	_ context.Context, arg db.ListSessionsByChannelParams,
) ([]db.Session, error) {
	f.channelListArgs = append(f.channelListArgs, arg)
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []db.Session
	for _, s := range f.sessions {
		if s.ChannelID != nil && arg.ChannelID != nil && *s.ChannelID == *arg.ChannelID {
			out = append(out, s)
		}
	}
	return trim(out, arg.PageSize), nil
}

func (f *fakeStore) ListSessionsByTeam(
	_ context.Context, arg db.ListSessionsByTeamParams,
) ([]db.Session, error) {
	f.teamListArgs = append(f.teamListArgs, arg)
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []db.Session
	for _, s := range f.sessions {
		if s.TeamID == arg.TeamID {
			out = append(out, s)
		}
	}
	return trim(out, arg.PageSize), nil
}

func (f *fakeStore) ListMessagesBySession(
	_ context.Context, arg db.ListMessagesBySessionParams,
) ([]db.Message, error) {
	f.listArgs = append(f.listArgs, arg)
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []db.Message
	for _, m := range f.messages {
		if m.SessionID == arg.SessionID {
			out = append(out, m)
		}
	}
	return trim(out, arg.PageSize), nil
}

func trim[T any](rows []T, size int32) []T {
	if n := int(size); n < len(rows) {
		return rows[:n]
	}
	return rows
}

// Nothing here is team- or any_team-scoped: reading a conversation is
// `member`, which the guard answers from the memberships already on the
// request. Being asked either question is itself the failure.
type fakeAuthz struct{ super bool }

func (f *fakeAuthz) IsSuperAdmin(*auth.User) bool { return f.super }

func (f *fakeAuthz) Can(context.Context, *auth.User, authz.Action, authz.Resource) (bool, error) {
	panic("a sessions route asked for a permission; reading is membership alone")
}

func (f *fakeAuthz) CanInAnyTeam(context.Context, *auth.User, authz.Action) (bool, error) {
	panic("a sessions route used the strictly weaker CanInAnyTeam")
}

// --- the harness ---

type fixture struct {
	teamID    uuid.UUID
	channelID uuid.UUID
	sessionID uuid.UUID
	userID    uuid.UUID
	store     *fakeStore
	authz     *fakeAuthz
	caller    *auth.User
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{
		teamID: uuid.New(), channelID: uuid.New(),
		sessionID: uuid.New(), userID: uuid.New(),
	}
	ref := "D01ABC"
	f.store = &fakeStore{
		channels: []db.Channel{{
			ID: f.channelID, TeamID: f.teamID, Provider: "custom", Name: "support",
		}},
		sessions: []db.Session{{
			ID: f.sessionID, TeamID: f.teamID, ChannelID: &f.channelID,
			ProviderRef: &ref, Kind: "direct", Status: "open",
			StartedAt:     time.Unix(1755412000, 0).UTC(),
			LastMessageAt: time.Unix(1755412345, 0).UTC(),
		}},
		messages: []db.Message{{
			ID: uuid.New(), ChannelID: f.channelID, SessionID: f.sessionID,
			UserID: f.userID, ExternalID: "m-1", Text: "what's our deploy status?",
			ReceivedAt: time.Unix(1755412345, 0).UTC(),
		}},
	}
	f.authz = &fakeAuthz{}
	f.caller = memberOf(f.teamID, "member")
	return f
}

func (f *fixture) channelSessions() string {
	return "/teams/" + f.teamID.String() + "/channels/" + f.channelID.String() + "/sessions"
}

func (f *fixture) teamSessions() string {
	return "/teams/" + f.teamID.String() + "/sessions"
}

func (f *fixture) sessionMessages() string {
	return "/teams/" + f.teamID.String() + "/sessions/" + f.sessionID.String() + "/messages"
}

func (f *fixture) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	New(discardLogger(), f.store).Register(mux, api.NewGuard(routes(t), f.authz, discardLogger()))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	if f.caller != nil {
		req = req.WithContext(auth.WithUser(req.Context(), f.caller))
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// routes is what rbac.json maps for this package, built through the real
// guard so a scope changed in the file fails here too.
func routes(t *testing.T) authz.Routes {
	t.Helper()

	rs := authz.NewRoutes()
	for _, path := range []string{
		"/teams/{id}/channels/{channelID}/sessions",
		"/teams/{id}/sessions",
		"/teams/{id}/sessions/{sessionID}/messages",
	} {
		if err := rs.Add("GET", path, "member", nil); err != nil {
			t.Fatalf("Add %s: %v", path, err)
		}
	}
	return rs
}

func memberOf(teamID uuid.UUID, role string) *auth.User {
	return &auth.User{
		ID: uuid.New(), Issuer: "dev", Subject: "someone",
		Teams: []auth.Membership{{TeamID: teamID, Name: "platform", Role: role}},
	}
}

func outsider() *auth.User {
	return &auth.User{ID: uuid.New(), Issuer: "dev", Subject: "outsider"}
}

func sessionPage(t *testing.T, rec *httptest.ResponseRecorder) api.Page[session] {
	t.Helper()

	var got api.Page[session]
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not a page of sessions: %v (%s)", err, rec.Body)
	}
	return got
}

func messagePage(t *testing.T, rec *httptest.ResponseRecorder) api.Page[message] {
	t.Helper()

	var got api.Page[message]
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not a page of messages: %v (%s)", err, rec.Body)
	}
	return got
}

// --- listing a channel's sessions ---

func TestAChannelsSessionsComeBack(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	rec := f.get(t, f.channelSessions())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	items := sessionPage(t, rec).Items
	if len(items) != 1 {
		t.Fatalf("%d sessions, want 1", len(items))
	}

	got := items[0]
	switch {
	case got.ID != f.sessionID:
		t.Errorf("ID = %s", got.ID)
	case got.ProviderRef != "D01ABC":
		t.Errorf("ProviderRef = %q", got.ProviderRef)
	case got.Kind != "direct":
		t.Errorf("Kind = %q", got.Kind)
	case got.LastMessageAt.IsZero():
		t.Error("LastMessageAt is zero, so nothing can be ordered by it")
	}
}

// A team's list includes a session with no channel, which is the only way one
// started from the dashboard or the API is ever reachable.
func TestATeamsSessionsIncludeOnesWithNoChannel(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.store.sessions = append(f.store.sessions, db.Session{
		ID: uuid.New(), TeamID: f.teamID, Kind: "direct", Status: "open",
		LastMessageAt: time.Unix(1755412999, 0).UTC(),
	})

	items := sessionPage(t, f.get(t, f.teamSessions())).Items
	if len(items) != 2 {
		t.Fatalf("%d sessions, want both", len(items))
	}

	var channelless int
	for _, s := range items {
		if s.ChannelID == nil {
			channelless++
		}
	}
	if channelless != 1 {
		t.Errorf("%d sessions with no channel, want 1", channelless)
	}
}

func TestAnEmptyListReturnsAnEmptyArray(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.store.sessions = nil

	for _, path := range []string{f.channelSessions(), f.teamSessions()} {
		body := f.get(t, path).Body.String()
		if strings.Contains(body, `"items":null`) {
			t.Errorf("%s returned a null items array: %s", path, body)
		}
	}
}

// --- reading a session ---

func TestASessionsMessagesComeBack(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	rec := f.get(t, f.sessionMessages())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	items := messagePage(t, rec).Items
	if len(items) != 1 {
		t.Fatalf("%d messages, want 1", len(items))
	}
	if items[0].Text != "what's our deploy status?" {
		t.Errorf("Text = %q", items[0].Text)
	}
	if items[0].UserID != f.userID {
		t.Errorf("UserID = %s, want the approved sender", items[0].UserID)
	}
}

// The session is in the URL, so repeating it in every row says what the caller
// already knows.
func TestAMessageDoesNotRepeatItsSession(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	body := f.get(t, f.sessionMessages()).Body.String()
	if strings.Contains(body, f.sessionID.String()) {
		t.Errorf("the session id is repeated in every row: %s", body)
	}
}

// sent_at is the sender's clock and may be absent. Null rather than year 1,
// which is a value somebody eventually plots.
func TestAMessageWithNoSentAtReportsNull(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	if body := f.get(t, f.sessionMessages()).Body.String(); !strings.Contains(body, `"sent_at":null`) {
		t.Errorf("body %q does not report a null sent_at", body)
	}
}

// And a message that has one keeps it. Testing only the null case leaves the
// field free to be dropped entirely without anything noticing.
func TestAMessageWithASentAtKeepsIt(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	at := time.Unix(1755400000, 0).UTC()
	f.store.messages[0].SentAt = &at

	items := messagePage(t, f.get(t, f.sessionMessages())).Items
	if items[0].SentAt == nil {
		t.Fatal("sent_at was dropped")
	}
	if !items[0].SentAt.Equal(at) {
		t.Errorf("sent_at = %v, want %v", *items[0].SentAt, at)
	}
}

// Message text is whatever somebody typed. Carried as data, so a body full of
// quotes and control characters still parses on the other side.
func TestHostileTextIsReturnedAsData(t *testing.T) {
	t.Parallel()

	hostile := `","injected":true,"x":"` + "\t\x1b[2K" + `<script>`

	f := newFixture(t)
	f.store.messages[0].Text = hostile

	rec := f.get(t, f.sessionMessages())
	items := messagePage(t, rec).Items
	if len(items) != 1 {
		t.Fatalf("the body did not survive hostile text: %s", rec.Body)
	}
	if items[0].Text != hostile {
		t.Errorf("Text = %q, want it round-tripped unchanged", items[0].Text)
	}
}

// --- who may read ---

func TestAnyMemberMayRead(t *testing.T) {
	t.Parallel()

	for _, role := range []string{"member", "admin", "viewer"} {
		t.Run(role, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			f.caller = memberOf(f.teamID, role)

			for _, path := range []string{f.channelSessions(), f.teamSessions(), f.sessionMessages()} {
				if rec := f.get(t, path); rec.Code != http.StatusOK {
					t.Errorf("a %s got %d on %s, want 200", role, rec.Code, path)
				}
			}
		})
	}
}

func TestAnOutsiderIsToldTheTeamDoesNotExist(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.caller = outsider()

	for _, path := range []string{f.channelSessions(), f.teamSessions(), f.sessionMessages()} {
		if rec := f.get(t, path); rec.Code != http.StatusNotFound {
			t.Errorf("an outsider got %d on %s, want 404", rec.Code, path)
		}
	}
}

func TestASuperAdminMayRead(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.caller = outsider()
	f.authz.super = true

	if rec := f.get(t, f.sessionMessages()); rec.Code != http.StatusOK {
		t.Errorf("a super admin got %d, want 200", rec.Code)
	}
}

// The guard authorised the caller for the team in the path, not for this
// session. Without the check, a member of any team reads any conversation by
// naming their own team in the URL.
func TestASessionInAnotherTeamIsNotFound(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.store.sessions[0].TeamID = uuid.New()

	if rec := f.get(t, f.sessionMessages()); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if len(f.store.listArgs) != 0 {
		t.Error("the conversation was read anyway")
	}
}

// The same for the channel, which is a separate lookup and a separate mistake.
func TestAChannelInAnotherTeamIsNotFound(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.store.channels[0].TeamID = uuid.New()

	if rec := f.get(t, f.channelSessions()); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestSomethingThatDoesNotExistIsNotFound(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	paths := map[string]string{
		"a channel": "/teams/" + f.teamID.String() + "/channels/" + uuid.New().String() + "/sessions",
		"a session": "/teams/" + f.teamID.String() + "/sessions/" + uuid.New().String() + "/messages",
	}
	for name, path := range paths {
		if rec := f.get(t, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", name, rec.Code)
		}
	}
}

func TestAMalformedIDIsRefused(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	paths := map[string]string{
		"a channel": "/teams/" + f.teamID.String() + "/channels/not-a-uuid/sessions",
		"a session": "/teams/" + f.teamID.String() + "/sessions/not-a-uuid/messages",
	}
	for name, path := range paths {
		if rec := f.get(t, path); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, rec.Code)
		}
	}
}

// A database that cannot be reached is not a session that is gone. Answering
// 404 sends somebody looking for a wrong id during an outage.
func TestAFailedLookupIsNotAFourOhFour(t *testing.T) {
	t.Parallel()

	t.Run("the session", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		f.store.sessionErr = errAny
		if rec := f.get(t, f.sessionMessages()); rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})

	t.Run("the channel", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		f.store.channelErr = errAny
		if rec := f.get(t, f.channelSessions()); rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})
}

func TestAFailedListIsReported(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.store.listErr = errAny

	for _, path := range []string{f.channelSessions(), f.teamSessions(), f.sessionMessages()} {
		if rec := f.get(t, path); rec.Code != http.StatusInternalServerError {
			t.Errorf("%s = %d, want 500", path, rec.Code)
		}
	}
}

// --- paging ---

func TestPagesCarryACursorThatReachesTheQuery(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	for i := range 3 {
		f.store.messages = append(f.store.messages, db.Message{
			ID: uuid.New(), ChannelID: f.channelID, SessionID: f.sessionID,
			UserID: f.userID, ExternalID: "m-" + string(rune('2'+i)), Text: "more",
			ReceivedAt: time.Unix(int64(1755412000-i*60), 0).UTC(),
		})
	}

	first := messagePage(t, f.get(t, f.sessionMessages()+"?limit=2"))
	if len(first.Items) != 2 || first.NextCursor == nil {
		t.Fatalf("first page = %d items, cursor %v", len(first.Items), first.NextCursor)
	}

	if rec := f.get(t, f.sessionMessages()+"?limit=2&cursor="+*first.NextCursor); rec.Code != http.StatusOK {
		t.Fatalf("second page = %d: %s", rec.Code, rec.Body)
	}

	last := f.store.listArgs[len(f.store.listArgs)-1]
	if !last.UseCursor {
		t.Error("the cursor was decoded and then not used — paging restarts from the top")
	}
}

// Sessions page too, and their cursor is a different one — it orders on
// last_message_at rather than received_at. Testing only the message cursor
// leaves this one free to be decoded and then ignored, which reads as a caller
// looping forever on page one.
func TestSessionsPageFromTheirOwnCursor(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		path string
		used func(*fakeStore) bool
	}{
		"a channel's": {
			path: "",
			used: func(f *fakeStore) bool {
				return f.channelListArgs[len(f.channelListArgs)-1].UseCursor
			},
		},
		"a team's": {
			path: "",
			used: func(f *fakeStore) bool {
				return f.teamListArgs[len(f.teamListArgs)-1].UseCursor
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			for i := range 3 {
				ref := "c-" + string(rune('2'+i))
				f.store.sessions = append(f.store.sessions, db.Session{
					ID: uuid.New(), TeamID: f.teamID, ChannelID: &f.channelID,
					ProviderRef: &ref, Kind: "group", Status: "open",
					LastMessageAt: time.Unix(int64(1755412000-i*60), 0).UTC(),
				})
			}

			path := f.channelSessions()
			if name == "a team's" {
				path = f.teamSessions()
			}

			first := sessionPage(t, f.get(t, path+"?limit=2"))
			if len(first.Items) != 2 || first.NextCursor == nil {
				t.Fatalf("first page = %d items, cursor %v", len(first.Items), first.NextCursor)
			}

			if rec := f.get(t, path+"?limit=2&cursor="+*first.NextCursor); rec.Code != http.StatusOK {
				t.Fatalf("second page = %d: %s", rec.Code, rec.Body)
			}
			if !tc.used(f.store) {
				t.Error("the cursor was decoded and then not used")
			}
		})
	}
}

func TestAMangledCursorIsRefused(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	for _, path := range []string{f.channelSessions(), f.teamSessions(), f.sessionMessages()} {
		if rec := f.get(t, path+"?cursor=not-base64"); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", path, rec.Code)
		}
	}
}

func TestAnUnreadableLimitIsRefused(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	for _, path := range []string{f.channelSessions(), f.teamSessions(), f.sessionMessages()} {
		if rec := f.get(t, path+"?limit=lots"); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", path, rec.Code)
		}
	}
}

// Every cursor this package encodes is a plain struct, which is what the
// unreachable MapPage error branches rest on. If somebody puts a channel, a
// func or a NaN in one, this fails rather than the branch quietly going live.
func TestEveryCursorCanBeEncoded(t *testing.T) {
	t.Parallel()

	for name, cursor := range map[string]any{
		"session": sessionCursor{LastMessageAt: time.Now(), ID: uuid.New()},
		"message": messageCursor{ReceivedAt: time.Now(), ID: uuid.New()},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := api.EncodeCursor(cursor); err != nil {
				t.Errorf("a %s cursor no longer encodes: %v", name, err)
			}
		})
	}
}
