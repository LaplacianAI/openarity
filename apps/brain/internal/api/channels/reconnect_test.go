package channels

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/custom"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/telegram"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type liveChannels struct {
	rows  []db.Channel
	asked []string
}

func (l *liveChannels) ListLiveChannelsOf(_ context.Context, providers []string) ([]db.Channel, error) {
	l.asked = providers
	return l.rows, nil
}

// setWebhooks answers each setWebhook, ok or not by the bot token, and keeps
// what it was asked.
type setWebhooks struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (s *setWebhooks) RoundTrip(r *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	s.mu.Lock()
	s.bodies = append(s.bodies, body)
	s.mu.Unlock()
	answer := `{"ok":true,"result":true}`
	if strings.Contains(r.URL.Path, "refused") {
		answer = `{"ok":false,"description":"Unauthorized"}`
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(answer)), Header: http.Header{}}, nil
}

func telegramChannel(t *testing.T, sec *fakeSecrets, token string) db.Channel {
	t.Helper()
	row := db.Channel{ID: uuid.New(), TeamID: uuid.New(), Provider: "telegram"}
	if err := sec.PutAll(t.Context(), secrets.Path(row.TeamID, secrets.KindChannel, row.ID), map[string]string{
		gateway.KeySigning: "secret", telegram.KeyBotToken: token,
	}); err != nil {
		t.Fatal(err)
	}
	return row
}

// Every bot is pointed at its channel again, now asking for the taps on a
// prompt's keyboard as well as messages; a bot Telegram refuses is logged and
// the rest still go.
func TestEveryBotIsPointedAgainAskingForTaps(t *testing.T) {
	t.Parallel()

	sec := newFakeSecrets()
	refused, ok := telegramChannel(t, sec, "34:refused"), telegramChannel(t, sec, "12:ok")
	store := &liveChannels{rows: []db.Channel{refused, ok}}
	hooks := &setWebhooks{}

	n, err := Reconnect(t.Context(), discardLogger(), store, sec,
		providers{"custom": custom.New(), "telegram": telegram.New()}, []string{"custom", "telegram"},
		"https://brain.example.com/", &http.Client{Transport: hooks})
	if err != nil || n != 1 {
		t.Fatalf("Reconnect = %d, %v; want 1 pointed (the other refused)", n, err)
	}
	if len(store.asked) != 1 || store.asked[0] != "telegram" {
		t.Errorf("listed channels of %v, want only the providers that can be pointed", store.asked)
	}
	if len(hooks.bodies) != 2 {
		t.Fatalf("%d setWebhooks, want one for each bot", len(hooks.bodies))
	}
	got := hooks.bodies[1]
	if got["url"] != "https://brain.example.com/hooks/telegram/"+ok.ID.String() {
		t.Errorf("pointed at %v", got["url"])
	}
	updates, _ := got["allowed_updates"].([]any)
	if len(updates) != 2 || updates[0] != "message" || updates[1] != "callback_query" {
		t.Errorf("allowed_updates = %v, want message and callback_query", updates)
	}
}

// Nowhere public to point at, or nothing that can be pointed: nothing is
// asked.
func TestNothingIsPointedWithoutAnAddressOrAConnector(t *testing.T) {
	t.Parallel()

	sec := newFakeSecrets()
	store := &liveChannels{rows: []db.Channel{telegramChannel(t, sec, "12:ok")}}
	hooks := &setWebhooks{}
	client := &http.Client{Transport: hooks}

	if n, err := Reconnect(t.Context(), discardLogger(), store, sec, providers{"telegram": telegram.New()},
		[]string{"telegram"}, "", client); err != nil || n != 0 {
		t.Errorf("with no address = %d, %v", n, err)
	}
	if n, err := Reconnect(t.Context(), discardLogger(), store, sec, providers{"custom": custom.New()},
		[]string{"custom"}, "https://brain.example.com", client); err != nil || n != 0 {
		t.Errorf("with no provider that can be pointed = %d, %v", n, err)
	}
	if len(hooks.bodies) != 0 {
		t.Errorf("%d requests sent", len(hooks.bodies))
	}
}

// A secret store that cannot be read points nothing, rather than a bot with
// no token.
func TestABotWhoseSecretsCannotBeReadIsLeftAsItWas(t *testing.T) {
	t.Parallel()

	sec := newFakeSecrets()
	store := &liveChannels{rows: []db.Channel{telegramChannel(t, sec, "12:ok")}}
	sec.getErr = errors.New("sealed")
	hooks := &setWebhooks{}
	var logs strings.Builder

	if n, err := Reconnect(t.Context(), slog.New(slog.NewTextHandler(&logs, nil)), store, sec, providers{"telegram": telegram.New()},
		[]string{"telegram"}, "https://brain.example.com", &http.Client{Transport: hooks}); err != nil || n != 0 {
		t.Errorf("Reconnect = %d, %v", n, err)
	}
	if len(hooks.bodies) != 0 {
		t.Errorf("%d requests sent with no secrets", len(hooks.bodies))
	}
	if !strings.Contains(logs.String(), "its credentials could not be read") {
		t.Errorf("the log does not say the credentials could not be read:\n%s", logs.String())
	}
}
