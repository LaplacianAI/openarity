package channels

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/custom"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/telegram"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const botToken = "7012345678:AAHk-example_token"

// telegramAPI stands in for Telegram: it answers getMe and keeps what it was
// asked, so a test sees exactly what left the brain.
type telegramAPI struct {
	mu     sync.Mutex
	asked  []*http.Request
	bodies []string
	status int
	body   string
	err    error
	// methods answers a method, such as "setWebhook", differently from the rest.
	methods map[string]answer
}

type answer struct {
	status int
	body   string
	err    error
}

func (f *telegramAPI) methodsAsked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.asked {
		out = append(out, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
	}
	return out
}

func (f *telegramAPI) client() *http.Client {
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
		}
		f.mu.Lock()
		f.asked = append(f.asked, r)
		f.bodies = append(f.bodies, string(body))
		f.mu.Unlock()
		a := answer{f.status, f.body, f.err}
		if m, ok := f.methods[r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]]; ok {
			a = m
		}
		if a.err != nil {
			return nil, a.err
		}
		return &http.Response{
			StatusCode: a.status, Body: io.NopCloser(strings.NewReader(a.body)), Header: http.Header{}, Request: r,
		}, nil
	})}
}

func knownBot() *telegramAPI {
	return &telegramAPI{status: http.StatusOK, body: `{"ok":true,"result":{"id":7012345678,"is_bot":true,"first_name":"Openarity","username":"OpenarityBot"}}`}
}

func both() Providers { return providers{"custom": custom.New(), "telegram": telegram.New()} }

func connect(t *testing.T, tg *telegramAPI, p Providers, s *fakeStore, sec *fakeSecrets, body string) *httptest.ResponseRecorder {
	t.Helper()
	return connectLogging(t, discardLogger(), tg, p, s, sec, body)
}

func connectLogging(
	t *testing.T, logger *slog.Logger, tg *telegramAPI, p Providers, s *fakeStore, sec *fakeSecrets, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	teamID := uuid.New()
	mux := http.NewServeMux()
	a := &fakeAuthz{allowed: true}
	New(logger, s, a, sec, p, "https://hooks.example.com", publicOnly, tg.client(), s).
		Register(mux, api.NewGuard(channelRoutes(t), a, liveTeams{}, discardLogger()))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/teams/"+teamID.String()+"/channels", strings.NewReader(body))
	req = req.WithContext(auth.WithUser(req.Context(), memberOf(teamID, "admin")))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The whole of it: the token is checked with Telegram, and the channel holds
// the token, the bot it names and the secret, written as one secret.
func TestATelegramChannelIsConnectedWithItsBotsIdentity(t *testing.T) {
	t.Parallel()

	tg, s, sec := knownBot(), &fakeStore{}, newFakeSecrets()
	rec := connect(t, tg, both(), s, sec,
		`{"provider":"telegram","name":"ops","credentials":{"bot_token":" `+botToken+` "}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	got := decodeCreated(t, rec)
	if got.SigningSecret == "" || got.CanReply != true || got.TakesReplyURL {
		t.Errorf("channel = %+v, want a secret shown once, replies, and no reply URL", got)
	}
	if strings.Contains(rec.Body.String(), botToken) {
		t.Error("the response shows the bot token")
	}

	if got := tg.methodsAsked(); len(got) != 2 || got[0] != "getMe" || got[1] != "setWebhook" ||
		tg.asked[0].URL.String() != "https://api.telegram.org/bot"+botToken+"/getMe" {
		t.Fatalf("asked Telegram %v, want getMe then setWebhook", got)
	}
	var hook struct {
		URL            string   `json:"url"`
		SecretToken    string   `json:"secret_token"`
		AllowedUpdates []string `json:"allowed_updates"`
	}
	if err := json.Unmarshal([]byte(tg.bodies[1]), &hook); err != nil {
		t.Fatalf("setWebhook body %q: %v", tg.bodies[1], err)
	}
	if hook.URL != got.HookURL || hook.SecretToken != got.SigningSecret ||
		len(hook.AllowedUpdates) != 1 || hook.AllowedUpdates[0] != "message" {
		t.Errorf("setWebhook = %+v, want the hook URL, the secret, and messages only", hook)
	}
	if !got.Connected {
		t.Error("a channel the brain pointed the bot at does not say it is connected")
	}
	path := secrets.Path(got.TeamID, secrets.KindChannel, got.ID)
	want := map[string]string{
		gateway.KeySigning: got.SigningSecret, telegram.KeyBotToken: botToken,
		telegram.KeyBotID: "7012345678", telegram.KeyBotUsername: "OpenarityBot",
	}
	if sec.writes != 1 || len(sec.put[path]) != len(want) {
		t.Fatalf("%d writes, stored %v; want one write of %v", sec.writes, sec.put[path], want)
	}
	for k, v := range want {
		if sec.put[path][k] != v {
			t.Errorf("stored %s = %q, want %q", k, sec.put[path][k], v)
		}
	}
}

// A token Telegram does not know is refused with Telegram's reason, and
// nothing is created: no row, no secret.
func TestATokenTelegramDoesNotKnowCreatesNothing(t *testing.T) {
	t.Parallel()

	tg := &telegramAPI{status: http.StatusUnauthorized, body: `{"ok":false,"error_code":401,"description":"Unauthorized"}`}
	s, sec := &fakeStore{}, newFakeSecrets()
	rec := connect(t, tg, both(), s, sec, `{"provider":"telegram","name":"ops","credentials":{"bot_token":"`+botToken+`"}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not one Telegram knows") {
		t.Errorf("POST = %d %s, want 400 saying the token is unknown", rec.Code, rec.Body)
	}
	if len(s.created) != 0 || sec.writes != 0 {
		t.Errorf("created %d channels and %d secrets for a refused token", len(s.created), sec.writes)
	}
}

// Telegram out of reach is ours to retry, and the answer never repeats the
// URL, which holds the token.
func TestTelegramOutOfReachIsABadGatewayThatNamesNoToken(t *testing.T) {
	t.Parallel()

	tg := &telegramAPI{err: errAny}
	s, sec := &fakeStore{}, newFakeSecrets()
	var log bytes.Buffer
	rec := connectLogging(t, slog.New(slog.NewTextHandler(&log, nil)), tg, both(), s, sec,
		`{"provider":"telegram","name":"ops","credentials":{"bot_token":"`+botToken+`"}}`)
	// The client wraps a failure with the URL, and the URL holds the token.
	if strings.Contains(log.String(), botToken) || !strings.Contains(log.String(), "connection reset") {
		t.Errorf("the log %q quotes the token, or not the failure", log.String())
	}
	if rec.Code != http.StatusBadGateway {
		t.Errorf("POST = %d %s, want 502", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), botToken) || strings.Contains(rec.Body.String(), "api.telegram.org") {
		t.Errorf("the answer %q quotes the request", rec.Body)
	}
	if len(s.created) != 0 || sec.writes != 0 {
		t.Error("something was created while Telegram was out of reach")
	}
}

// What a provider asks for, it must be given, and nothing else is taken: a key
// it would never read is not quietly stored. None of these reach Telegram.
func TestAChannelTakesExactlyTheCredentialsItsProviderAsksFor(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ body, says string }{
		"no token":          {`{"provider":"telegram","name":"ops"}`, "credentials.bot_token is required"},
		"a blank token":     {`{"provider":"telegram","name":"ops","credentials":{"bot_token":"  "}}`, "credentials.bot_token is required"},
		"a key not asked":   {`{"provider":"telegram","name":"ops","credentials":{"bot_token":"` + botToken + `","bot_username":"me"}}`, "credentials.bot_username is not something"},
		"custom with some":  {`{"provider":"custom","name":"ops","credentials":{"bot_token":"x"}}`, "takes no credentials"},
		"not a token shape": {`{"provider":"telegram","name":"ops","credentials":{"bot_token":"x/../y"}}`, "not a Telegram bot token"},
		"a reply URL":       {`{"provider":"telegram","name":"ops","reply_url":"https://x.example.com","credentials":{"bot_token":"` + botToken + `"}}`, "takes no reply_url"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tg, s, sec := knownBot(), &fakeStore{}, newFakeSecrets()
			rec := connect(t, tg, both(), s, sec, c.body)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.says) {
				t.Errorf("POST = %d %q, want 400 saying %q", rec.Code, rec.Body, c.says)
			}
			if len(tg.asked) != 0 || len(s.created) != 0 || sec.writes != 0 {
				t.Errorf("asked Telegram %d times, created %d, wrote %d", len(tg.asked), len(s.created), sec.writes)
			}
		})
	}
}

// greedy is a provider whose setup answer names more than it declares, and
// the signing secret: neither may reach the channel's secret.
type greedy struct{ gateway.Provider }

func (greedy) Name() string       { return "greedy" }
func (greedy) Supplied() []string { return []string{"token"} }
func (greedy) Keys() []string     { return []string{gateway.KeySigning, "token", "id"} }
func (greedy) SetupRequest(gateway.Credentials) (gateway.OutboundRequest, error) {
	return gateway.OutboundRequest{URL: "https://setup.example.com/"}, nil
}

func (greedy) Complete(int, []byte) (gateway.Credentials, error) {
	return gateway.Credentials{"id": "1", "token": "replaced", gateway.KeySigning: "chosen", "admin": "yes"}, nil
}

func TestAProvidersAnswerCannotWidenWhatAChannelHolds(t *testing.T) {
	t.Parallel()

	tg, s, sec := &telegramAPI{status: http.StatusOK}, &fakeStore{}, newFakeSecrets()
	rec := connect(t, tg, providers{"greedy": greedy{custom.New()}}, s, sec,
		`{"provider":"greedy","name":"g","credentials":{"token":"mine"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	got := decodeCreated(t, rec)
	stored := sec.put[secrets.Path(got.TeamID, secrets.KindChannel, got.ID)]
	if stored["token"] != "mine" || stored["id"] != "1" || stored["admin"] != "" || stored[gateway.KeySigning] != got.SigningSecret {
		t.Errorf("stored %v, want the supplied token, the declared id, our secret, and nothing else", stored)
	}
}

// A Telegram channel replies in the chat, so it says so and refuses an address.
func TestATelegramChannelTakesNoReplyURL(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	ch := tgChannel(teamID)
	s := &fakeStore{channels: []db.Channel{ch}}
	rec := callWith(t, both(), "", s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
		http.MethodPatch, channelPath(teamID, ch.ID), `{"reply_url":"https://x.example.com/r"}`)
	if rec.Code != http.StatusBadRequest || s.channels[0].ReplyUrl != nil {
		t.Errorf("PATCH = %d %s, want 400 and no URL", rec.Code, rec.Body)
	}

	rec = callWith(t, both(), "", s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
		http.MethodGet, "/teams/"+teamID.String()+"/channels", "")
	var page struct {
		Items []channel `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 {
		t.Fatalf("list %s: %v", rec.Body, err)
	}
	if c := page.Items[0]; !c.CanReply || c.TakesReplyURL {
		t.Errorf("a telegram channel = %+v, want it to reply and take no URL", c)
	}
}

func tgChannel(teamID uuid.UUID) db.Channel {
	return db.Channel{ID: uuid.New(), TeamID: teamID, Provider: "telegram", Name: "ops"}
}

// custom posts to an address, so its channel says it takes one: a client
// that read false here would hide the only place to set it.
func TestACustomChannelTakesAReplyURL(t *testing.T) {
	t.Parallel()

	rec := connect(t, &telegramAPI{}, both(), &fakeStore{}, newFakeSecrets(), `{"provider":"custom","name":"support"}`)
	if got := decodeCreated(t, rec); rec.Code != http.StatusCreated || !got.TakesReplyURL {
		t.Errorf("POST = %d, takes_reply_url = %v; want 201 and true", rec.Code, got.TakesReplyURL)
	}
}

// A channel Telegram will not post to is one nothing reaches: it is taken
// away again, row and secret, and the person is told Telegram's own reason.
func TestABotTelegramWillNotPointAtTheChannelLeavesNoChannel(t *testing.T) {
	t.Parallel()

	for name, a := range map[string]answer{
		"refused":      {status: http.StatusBadRequest, body: `{"ok":false,"error_code":400,"description":"Bad Request: bad webhook: Failed to resolve host"}`},
		"out of reach": {err: errAny},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tg := knownBot()
			tg.methods = map[string]answer{"setWebhook": a}
			s, sec := &fakeStore{}, newFakeSecrets()
			rec := connect(t, tg, both(), s, sec, `{"provider":"telegram","name":"ops","credentials":{"bot_token":"`+botToken+`"}}`)
			if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), botToken) {
				t.Fatalf("POST = %d %q, want 502 naming no token", rec.Code, rec.Body)
			}
			if name == "refused" && !strings.Contains(rec.Body.String(), "Failed to resolve host") {
				t.Errorf("the answer %q does not say why", rec.Body)
			}
			if len(s.created) != 1 || len(s.deleted) != 1 || len(s.channels) != 0 {
				t.Fatalf("created %d, deleted %v, left %d: want the channel taken away", len(s.created), s.deleted, len(s.channels))
			}
			if len(sec.deleted) != 1 || sec.deleted[0] != secrets.Path(s.created[0].TeamID, secrets.KindChannel, s.deleted[0]) {
				t.Errorf("deleted secrets %v, want the channel's", sec.deleted)
			}
		})
	}
}

// With no public https address there is nothing to point Telegram at: the
// channel is made, says it is not connected, and the step is the person's.
func TestWithNoPublicAddressTheBotIsLeftForThePersonToPoint(t *testing.T) {
	t.Parallel()

	for name, hookBase := range map[string]string{"none": "", "plain http": "http://brain.internal:21121"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tg, s, sec := knownBot(), &fakeStore{}, newFakeSecrets()
			teamID := uuid.New()
			mux := http.NewServeMux()
			a := &fakeAuthz{allowed: true}
			New(discardLogger(), s, a, sec, both(), hookBase, publicOnly, tg.client(), s).
				Register(mux, api.NewGuard(channelRoutes(t), a, liveTeams{}, discardLogger()))
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/teams/"+teamID.String()+"/channels",
				strings.NewReader(`{"provider":"telegram","name":"ops","credentials":{"bot_token":"`+botToken+`"}}`))
			req = req.WithContext(auth.WithUser(req.Context(), memberOf(teamID, "admin")))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusCreated || decodeCreated(t, rec).Connected {
				t.Errorf("POST = %d %s, want 201 and not connected", rec.Code, rec.Body)
			}
			if got := tg.methodsAsked(); len(got) != 1 || got[0] != "getMe" {
				t.Errorf("asked Telegram %v, want only getMe", got)
			}
		})
	}
}

func deleteChannel(t *testing.T, tg *telegramAPI, s *fakeStore, sec *fakeSecrets, ch db.Channel) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	a := &fakeAuthz{allowed: true}
	New(discardLogger(), s, a, sec, both(), "https://hooks.example.com", publicOnly, tg.client(), s).
		Register(mux, api.NewGuard(channelRoutes(t), a, liveTeams{}, discardLogger()))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, channelPath(ch.TeamID, ch.ID), nil)
	req = req.WithContext(auth.WithUser(req.Context(), memberOf(ch.TeamID, "admin")))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func storedBot(sec *fakeSecrets, ch db.Channel) {
	sec.put[secrets.Path(ch.TeamID, secrets.KindChannel, ch.ID)] = map[string]string{
		gateway.KeySigning: "oawh_x", telegram.KeyBotToken: botToken,
		telegram.KeyBotID: "7012345678", telegram.KeyBotUsername: "OpenarityBot",
	}
}

// Deleting a Telegram channel tells Telegram to stop posting, with the
// channel's own token, before the row goes.
func TestDeletingATelegramChannelLetsTheBotGo(t *testing.T) {
	t.Parallel()

	ch := tgChannel(uuid.New())
	tg, s, sec := &telegramAPI{status: http.StatusOK, body: `{"ok":true,"result":true}`}, &fakeStore{channels: []db.Channel{ch}}, newFakeSecrets()
	storedBot(sec, ch)
	if rec := deleteChannel(t, tg, s, sec, ch); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d %s", rec.Code, rec.Body)
	}
	if got := tg.methodsAsked(); len(got) != 1 || got[0] != "deleteWebhook" ||
		tg.asked[0].URL.String() != "https://api.telegram.org/bot"+botToken+"/deleteWebhook" {
		t.Errorf("asked Telegram %v, want deleteWebhook with the channel's token", got)
	}
	if len(s.deleted) != 1 {
		t.Error("the channel was not deleted")
	}
}

// Letting the bot go is best effort: the person asked for the channel to go,
// so Telegram refusing, out of reach, or a token that cannot be read never
// keeps it.
func TestATelegramChannelIsDeletedEvenIfTelegramCannotBeTold(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		tg      *telegramAPI
		noToken bool
	}{
		"refused":         {tg: &telegramAPI{status: http.StatusUnauthorized, body: `{"ok":false,"description":"Unauthorized"}`}},
		"out of reach":    {tg: &telegramAPI{err: errAny}},
		"no token stored": {tg: &telegramAPI{status: http.StatusOK, body: `{"ok":true}`}, noToken: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ch := tgChannel(uuid.New())
			s, sec := &fakeStore{channels: []db.Channel{ch}}, newFakeSecrets()
			if !c.noToken {
				storedBot(sec, ch)
			}
			if rec := deleteChannel(t, c.tg, s, sec, ch); rec.Code != http.StatusNoContent || len(s.deleted) != 1 {
				t.Errorf("DELETE = %d, deleted %v; want it gone", rec.Code, s.deleted)
			}
			if c.noToken && len(c.tg.asked) != 0 {
				t.Error("Telegram was asked with no token")
			}
		})
	}
}
