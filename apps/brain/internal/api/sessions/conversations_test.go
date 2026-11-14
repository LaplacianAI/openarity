package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// fakeInbox records what it is handed and answers with out, or err.
type fakeInbox struct {
	got []gateway.Delivery
	chs []gateway.Channel
	out gateway.Delivered
	err error
}

func (f *fakeInbox) DeliverOne(_ context.Context, ch gateway.Channel, m gateway.Delivery) (gateway.Delivered, error) {
	f.got = append(f.got, m)
	f.chs = append(f.chs, ch)
	return f.out, f.err
}

func (f *fixture) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	New(discardLogger(), f.store, f.authz, f.objects, f.inbox, gateway.NewIngest(f.uploads, f.uploads, nil, discardLogger())).
		Register(mux, api.NewGuard(routes(t), f.authz, liveTeams{}, discardLogger()))

	var raw []byte
	if s, ok := body.(string); ok {
		raw = []byte(s)
	} else {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if f.caller != nil {
		req = req.WithContext(auth.WithUser(req.Context(), f.caller))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// dashboardSession makes the fixture's session one on the dashboard, held by
// the caller, and returns its ref.
func (f *fixture) dashboardSession() string {
	ref := f.caller.ID.String() + ":first"
	f.store.sessions[0].ChannelID = &f.dashboardID
	f.store.sessions[0].ProviderRef = &ref
	return ref
}

func decodeSaid(t *testing.T, rec *httptest.ResponseRecorder) said {
	t.Helper()
	var out said
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding %s: %v", rec.Body, err)
	}
	return out
}

func TestStartingAConversationDeliversItsFirstMessageToTheDashboard(t *testing.T) {
	f := newFixture(t)
	run := uuid.New()
	f.inbox.out = gateway.Delivered{Session: uuid.New(), Message: uuid.New(), Run: run}

	rec := f.do(t, http.MethodPost, f.teamSessions(), sayRequest{Text: "why did the deploy fail?", Key: "k1"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decodeSaid(t, rec)
	if got.RunID == nil || *got.RunID != run || got.Steered || got.SessionID != f.inbox.out.Session {
		t.Fatalf("answered %+v", got)
	}

	m := f.inbox.got[0]
	me := f.caller.ID.String()
	switch {
	case f.inbox.chs[0].ID != f.dashboardID || f.inbox.chs[0].TeamID != f.teamID:
		t.Errorf("delivered to channel %+v, want the team's dashboard", f.inbox.chs[0])
	case m.UserID != f.caller.ID || m.Author.Ref != me:
		t.Errorf("delivered as %v / %q, want the caller", m.UserID, m.Author.Ref)
	case m.Session.Kind != gateway.SessionDirect:
		t.Errorf("a %s session, want direct", m.Session.Kind)
	case m.Session.Ref != me+":k1" || m.ExternalID != me+":k1":
		t.Errorf("ref %q, external id %q; want both named by the caller and the key", m.Session.Ref, m.ExternalID)
	case m.Text != "why did the deploy fail?":
		t.Errorf("text %q", m.Text)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("the delivery is not one the gateway accepts: %v", err)
	}
}

// A retry carries the same key: the inbox drops it as the duplicate it is,
// and the caller is told what was stored the first time.
func TestASendRetriedWithItsKeyAnswersWhatWasStored(t *testing.T) {
	f := newFixture(t)
	me := f.caller.ID.String()
	first := db.Message{ID: uuid.New(), ChannelID: f.dashboardID, SessionID: uuid.New(), ExternalID: me + ":k1"}
	f.store.messages = append(f.store.messages, first)
	f.inbox.out = gateway.Delivered{Session: first.SessionID}

	rec := f.do(t, http.MethodPost, f.teamSessions(), sayRequest{Text: "again", Key: "k1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := decodeSaid(t, rec); got.MessageID != first.ID || got.SessionID != first.SessionID || got.RunID != nil {
		t.Fatalf("answered %+v, want the message stored first", got)
	}
}

func TestSendingInYourOwnDashboardConversation(t *testing.T) {
	f := newFixture(t)
	ref := f.dashboardSession()
	active := uuid.New()
	f.inbox.out = gateway.Delivered{Session: f.sessionID, Message: uuid.New(), Steered: active}

	rec := f.do(t, http.MethodPost, f.sessionMessages(), sayRequest{Text: "also check staging", Key: "k2"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := decodeSaid(t, rec); got.RunID == nil || *got.RunID != active || !got.Steered {
		t.Fatalf("answered %+v, want the working run, steered", got)
	}
	if m := f.inbox.got[0]; m.Session.Ref != ref || m.ExternalID != f.caller.ID.String()+":k2" {
		t.Fatalf("delivered to %q as %q", m.Session.Ref, m.ExternalID)
	}
}

func TestAMessageThatGetsNoAnswerSaysWhy(t *testing.T) {
	f := newFixture(t)
	f.dashboardSession()
	f.inbox.out = gateway.Delivered{Session: f.sessionID, Message: uuid.New(), Unanswered: "the team has no model set, so its hub cannot run"}

	rec := f.do(t, http.MethodPost, f.sessionMessages(), sayRequest{Text: "hello", Key: "k3"})
	if got := decodeSaid(t, rec); rec.Code != http.StatusAccepted || got.RunID != nil || !strings.Contains(got.Unanswered, "no model") {
		t.Fatalf("status %d, answered %+v", rec.Code, got)
	}
}

// Every refusal leaves the inbox untouched: nothing is said for someone else,
// on another channel, or that the brain would not store.
func TestSendingIsRefused(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(f *fixture)
		path   func(f *fixture) string
		body   any
		status int
	}{
		{
			"a channel's conversation", func(f *fixture) {}, (*fixture).sessionMessages,
			sayRequest{Text: "hi", Key: "k"},
			http.StatusConflict,
		},
		{"someone else's conversation, read by a moderator", func(f *fixture) {
			f.dashboardSession()
			other := uuid.New()
			f.store.sessions[0].UserID = &other
			f.authz.moderator = true
		}, (*fixture).sessionMessages, sayRequest{Text: "hi", Key: "k"}, http.StatusForbidden},
		{"someone else's conversation", func(f *fixture) {
			f.dashboardSession()
			other := uuid.New()
			f.store.sessions[0].UserID = &other
		}, (*fixture).sessionMessages, sayRequest{Text: "hi", Key: "k"}, http.StatusNotFound},
		{"a conversation of another team", func(f *fixture) {
			f.dashboardSession()
			f.store.sessions[0].TeamID = uuid.New()
		}, (*fixture).sessionMessages, sayRequest{Text: "hi", Key: "k"}, http.StatusNotFound},
		{
			"a member who may not start runs", func(f *fixture) { f.authz.noRunWrite = true }, (*fixture).teamSessions,
			sayRequest{Text: "hi", Key: "k"},
			http.StatusForbidden,
		},
		{"blank text", func(f *fixture) {}, (*fixture).teamSessions, sayRequest{Text: " \n ", Key: "k"}, http.StatusBadRequest},
		{
			"text over 100 KiB", func(f *fixture) {}, (*fixture).teamSessions,
			sayRequest{Text: strings.Repeat("a", maxSaid+1), Key: "k"},
			http.StatusBadRequest,
		},
		{"a NUL", func(f *fixture) {}, (*fixture).teamSessions, sayRequest{Text: "a\x00b", Key: "k"}, http.StatusBadRequest},
		{"no key", func(f *fixture) {}, (*fixture).teamSessions, sayRequest{Text: "hi"}, http.StatusBadRequest},
		{"a key with a colon", func(f *fixture) {}, (*fixture).teamSessions, sayRequest{Text: "hi", Key: "a:b"}, http.StatusBadRequest},
		{
			"a key too long", func(f *fixture) {}, (*fixture).teamSessions,
			sayRequest{Text: "hi", Key: strings.Repeat("k", 65)},
			http.StatusBadRequest,
		},
		{"a body that is not JSON", func(f *fixture) {}, (*fixture).teamSessions, "{", http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			c.setup(f)
			rec := f.do(t, http.MethodPost, c.path(f), c.body)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.status, rec.Body)
			}
			if len(f.inbox.got) != 0 {
				t.Fatalf("delivered %+v on a refusal", f.inbox.got)
			}
		})
	}
}

func TestTheLimitIsBytesNotCharacters(t *testing.T) {
	f := newFixture(t)
	f.inbox.out = gateway.Delivered{Session: uuid.New(), Message: uuid.New()}
	if rec := f.do(t, http.MethodPost, f.teamSessions(), sayRequest{Text: strings.Repeat("a", maxSaid), Key: "k"}); rec.Code != http.StatusCreated {
		t.Fatalf("exactly 100 KiB: status %d", rec.Code)
	}
	if rec := f.do(t, http.MethodPost, f.teamSessions(), sayRequest{Text: strings.Repeat("é", maxSaid/2+1), Key: "k"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("over 100 KiB in two-byte characters: status %d", rec.Code)
	}
}

func TestADeliveryThatFailsIsAServerError(t *testing.T) {
	for name, c := range map[string]struct {
		err    error
		status int
	}{
		"the database":     {errors.New("connection reset"), http.StatusInternalServerError},
		"a deleted team":   {gateway.ErrTeamDeleted, http.StatusNotFound},
		"no dashboard row": {nil, http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.inbox.err = c.err
			if c.err == nil {
				f.store.channels = f.store.channels[:1] // the dashboard's is gone
			}
			if rec := f.do(t, http.MethodPost, f.teamSessions(), sayRequest{Text: "hi", Key: "k"}); rec.Code != c.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.status, rec.Body)
			}
		})
	}
}

func TestADirectSessionSaysWhoItIsWith(t *testing.T) {
	f := newFixture(t)
	rec := f.get(t, f.session())
	var got session
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.UserID == nil || *got.UserID != f.caller.ID {
		t.Fatalf("user_id %v, want the caller's", got.UserID)
	}

	f.store.sessions[0].Kind = "group"
	rec = f.get(t, f.session())
	if strings.Contains(rec.Body.String(), `"user_id"`) {
		t.Fatalf("a group session names somebody: %s", rec.Body)
	}
}

// A conversation with the builder starts as the builder's: the delivery says
// so, and the gateway makes the session the builder's when it makes it.
func TestAConversationStartedWithTheBuilderIsTheBuilders(t *testing.T) {
	f := newFixture(t)
	f.inbox.out = gateway.Delivered{Session: uuid.New(), Message: uuid.New(), Run: uuid.New()}

	rec := f.do(t, http.MethodPost, f.teamSessions(), sayRequest{Text: "an agent for support", Key: "k1", Builder: true})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if m := f.inbox.got[0]; !m.Session.Builder {
		t.Errorf("delivered %+v, want a session the builder answers", m.Session)
	}

	f.do(t, http.MethodPost, f.teamSessions(), sayRequest{Text: "hello", Key: "k2"})
	if m := f.inbox.got[1]; m.Session.Builder {
		t.Errorf("delivered %+v without builder, want the hub's", m.Session)
	}
}

// Who answers is decided when a conversation starts. A message in one cannot
// change it, and nothing is delivered when it tries.
func TestAMessageCannotMakeAConversationTheBuilders(t *testing.T) {
	f := newFixture(t)
	f.dashboardSession()

	rec := f.do(t, http.MethodPost, f.sessionMessages(), sayRequest{Text: "now you", Key: "k2", Builder: true})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "when a conversation starts") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(f.inbox.got) != 0 {
		t.Errorf("delivered %+v on a refusal", f.inbox.got)
	}
}
