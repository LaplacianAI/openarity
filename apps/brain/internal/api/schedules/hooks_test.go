package schedules

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/custom"
)

func webhookWatcher(name string) map[string]any {
	return map[string]any{"name": name, "objective": "Tell me what was released.", "source": "webhook"}
}

// A webhook watcher is made with its URL and its secret, shown once; a new
// secret is its maker's to make, with the URL it signs for.
func TestAWebhookWatcherThroughItsRoutes(t *testing.T) {
	w := newWorld(t)
	rec := w.serve(t, w.maker, "POST", "/watchers", webhookWatcher("releases"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	v := decode[watcher](t, rec)
	if !strings.HasPrefix(v.Secret, "oawh_") || v.HookURL == nil ||
		!strings.HasPrefix(*v.HookURL, "https://hooks.example/hooks/watchers/") || v.URL != nil || v.EverySeconds != nil {
		t.Fatalf("watcher = %+v", v)
	}
	path := "/watchers/" + v.ID.String()
	got := decode[watcher](t, w.serve(t, w.maker, "GET", path, nil))
	if got.Secret != "" || got.HookURL == nil || *got.HookURL != *v.HookURL {
		t.Errorf("read back = %+v, want its URL and no secret", got)
	}
	want(t, w.serve(t, w.other, "POST", path+"/secret", nil), http.StatusNotFound, "no such watcher")
	rec = w.serve(t, w.maker, "POST", path+"/secret", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("roll = %d %s", rec.Code, rec.Body)
	}
	rolled := decode[hookSecret](t, rec)
	if rolled.Secret == "" || rolled.Secret == v.Secret || rolled.URL == nil || *rolled.URL != *v.HookURL {
		t.Errorf("rolled = %+v, want a new secret for the same URL", rolled)
	}
	page := decode[watcher](t, w.serve(t, w.maker, "POST", "/watchers", pageWatcher("price")))
	want(t, w.serve(t, w.maker, "POST", "/watchers/"+page.ID.String()+"/secret", nil), http.StatusBadRequest, "only a webhook")
}

// The hook answers 202 once a signed delivery is kept, and says no more than
// it must otherwise: 401 unsigned, 404 unknown, 409 paused, 413 too big.
func TestTheHookAnswersADelivery(t *testing.T) {
	w := newWorld(t)
	v := decode[watcher](t, w.serve(t, w.maker, "POST", "/watchers", webhookWatcher("releases")))
	hook := strings.TrimPrefix(*v.HookURL, "https://hooks.example")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	NewHooks(logger, w.service(logger)).Register(mux, nil)
	post := func(path, secret string, body []byte) int {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, bytes.NewReader(body))
		if secret != "" {
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			req.Header.Set(custom.HeaderTimestamp, ts)
			req.Header.Set(custom.HeaderSignature, custom.Sign(secret, ts, body))
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := post(hook, "", []byte("x")); got != http.StatusUnauthorized {
		t.Errorf("unsigned = %d, want 401", got)
	}
	var n int
	if err := w.raw.QueryRow(t.Context(), `SELECT count(*) FROM watcher_deliveries`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d deliveries kept from an unsigned one, want none", n)
	}
	if got := post(hook, v.Secret, []byte(`{"tag":"v1"}`)); got != http.StatusAccepted {
		t.Errorf("signed = %d, want 202", got)
	}
	if len(w.runs.ids) != 1 {
		t.Errorf("%d runs enqueued, want the delivery's", len(w.runs.ids))
	}
	if got := post("/hooks/watchers/nobody-has-this-one", v.Secret, []byte("x")); got != http.StatusNotFound {
		t.Errorf("unknown hook = %d, want 404", got)
	}
	if got := post(hook, v.Secret, bytes.Repeat([]byte("a"), 257<<10)); got != http.StatusRequestEntityTooLarge {
		t.Errorf("257 KiB = %d, want 413", got)
	}
	w.serve(t, w.maker, "PATCH", "/watchers/"+v.ID.String(), map[string]any{"status": "paused"})
	if got := post(hook, v.Secret, []byte("y")); got != http.StatusConflict {
		t.Errorf("paused = %d, want 409", got)
	}
}
