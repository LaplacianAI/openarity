package mcpservers

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
)

// fakeVault records each write, so a refusal can be shown to have written
// nothing.
type fakeVault struct {
	mu     sync.Mutex
	writes map[string]map[string]string
	err    error
}

func (v *fakeVault) PutAll(_ context.Context, path string, values map[string]string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.err != nil {
		return v.err
	}
	if v.writes == nil {
		v.writes = map[string]map[string]string{}
	}
	v.writes[path] = maps.Clone(values)
	return nil
}

func (v *fakeVault) count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.writes)
}

func secretPath(teamID uuid.UUID, name string) string {
	return "/teams/" + teamID.String() + "/mcp-secrets/" + name
}

func TestASecretIsWrittenWholeUnderTheTeamAndNeverSentBack(t *testing.T) {
	t.Parallel()
	teamID := uuid.New()
	s := newFakeStore()

	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPut, secretPath(teamID, "stripe"),
		map[string]any{"values": map[string]string{"token": "sk-live-1", "account": "acct-2"}})

	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("status %d body %q; want 204 and nothing back", rec.Code, rec.Body)
	}
	got := s.vault.writes["teams/"+teamID.String()+"/mcp/stripe"]
	if !maps.Equal(got, map[string]string{"token": "sk-live-1", "account": "acct-2"}) {
		t.Errorf("wrote %v; want both keys in one write at the team's path", s.vault.writes)
	}
}

func TestASecretWriteNeedsToolWrite(t *testing.T) {
	t.Parallel()
	teamID := uuid.New()
	s := newFakeStore()

	rec := call(t, s, &fakeAuthz{allowed: false}, memberOf(teamID), http.MethodPut, secretPath(teamID, "stripe"),
		map[string]any{"values": map[string]string{"token": "x"}})
	if rec.Code != http.StatusForbidden {
		t.Errorf("status %d, want 403", rec.Code)
	}
	if n := s.vault.count(); n != 0 {
		t.Errorf("%d secrets written without tool:write", n)
	}
}

func TestABadSecretWriteIsRefusedAndWritesNothing(t *testing.T) {
	t.Parallel()
	teamID := uuid.New()
	many := map[string]string{}
	for i := range maxSecretKeys + 1 {
		many["k"+strings.Repeat("x", i)] = "v"
	}

	for name, tc := range map[string]struct {
		name string
		body any
		says string
	}{
		"a nested name":    {"a%2Fb", map[string]any{"values": map[string]string{"token": "x"}}, "a secret's name"},
		"a dotted name":    {"stripe.prod", map[string]any{"values": map[string]string{"token": "x"}}, "a secret's name"},
		"no values":        {"stripe", map[string]any{"values": map[string]string{}}, "values is required"},
		"no body":          {"stripe", map[string]any{}, "values is required"},
		"a key with #":     {"stripe", map[string]any{"values": map[string]string{"to#ken": "x"}}, "a key is"},
		"an empty value":   {"stripe", map[string]any{"values": map[string]string{"token": ""}}, "a value is empty"},
		"a value too long": {"stripe", map[string]any{"values": map[string]string{"token": strings.Repeat("x", maxSecretValue+1)}}, "over 16 KiB"},
		"too many keys":    {"stripe", map[string]any{"values": many}, "at most 20 keys"},
		"an unknown field": {"stripe", map[string]any{"values": map[string]string{"token": "x"}, "value": "y"}, ""},
	} {
		s := newFakeStore()
		rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPut, secretPath(teamID, tc.name), tc.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.says) {
			t.Errorf("%s: %d %q; want 400 saying %q", name, rec.Code, rec.Body, tc.says)
		}
		if n := s.vault.count(); n != 0 {
			t.Errorf("%s: %d secrets written", name, n)
		}
	}
}

// The store's error can carry its address or the path; the caller learns
// only that it is down, and the log never holds the value.
func TestASecretStoreThatFailsIsUnavailableAndTheValueIsNotLogged(t *testing.T) {
	t.Parallel()
	teamID := uuid.New()
	s := newFakeStore()
	s.vault.err = errors.New("dial tcp 10.0.0.9:8200: refused")
	var logs bytes.Buffer

	rec := callWith(t, s, &fakeDiscoverer{store: s}, slog.New(slog.NewTextHandler(&logs, nil)), &fakeAuthz{allowed: true},
		memberOf(teamID), http.MethodPut, secretPath(teamID, "stripe"),
		map[string]any{"values": map[string]string{"token": "sk-live-secret-value"}})

	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "10.0.0.9") {
		t.Errorf("status %d body %q; want 503 without the store's detail", rec.Code, rec.Body)
	}
	if strings.Contains(logs.String(), "sk-live-secret-value") {
		t.Errorf("the value reached the log: %s", logs.String())
	}
}

func TestABrainThatWritesNoSecretsSaysSo(t *testing.T) {
	t.Parallel()
	teamID := uuid.New()
	s := newFakeStore()
	mux := http.NewServeMux()
	New(discardLogger(), s, &fakeDiscoverer{store: s}, nil).
		Register(mux, api.NewGuard(serverRoutes(t), &fakeAuthz{allowed: true}, liveTeams{}, discardLogger()))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, secretPath(teamID, "stripe"),
		strings.NewReader(`{"values":{"token":"x"}}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithUser(req.Context(), memberOf(teamID)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "secret store yourself") {
		t.Errorf("status %d %q, want 501 saying where to write it", rec.Code, rec.Body)
	}
}
