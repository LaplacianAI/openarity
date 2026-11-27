package schedules

import (
	"net/http"
	"strings"
	"testing"
)

func pageWatcher(name string) map[string]any {
	return map[string]any{
		"name": name, "objective": "Tell me when the price moves.", "source": "web_page",
		"url": "HTTPS://Shop.Example.test:443/price#now", "selector": ".price", "every_seconds": 3600,
	}
}

// A watcher is made, read, listed and its checks paged through the same
// routes a schedule's are, its address normalised.
func TestAWatcherThroughItsRoutes(t *testing.T) {
	w := newWorld(t)
	rec := w.serve(t, w.maker, "POST", "/watchers", pageWatcher("price"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	v := decode[watcher](t, rec)
	if v.URL == nil || *v.URL != "https://shop.example.test/price" || v.Status != "active" || v.CooldownSeconds != 3600 ||
		v.Target.Kind != "own" || v.NextCheckAt == nil {
		t.Errorf("watcher = %+v", v)
	}
	path := "/watchers/" + v.ID.String()
	if got := decode[watcher](t, w.serve(t, w.maker, "GET", path, nil)); got.ID != v.ID {
		t.Errorf("get = %+v", got)
	}
	want(t, w.serve(t, w.other, "GET", path, nil), http.StatusNotFound, "no such watcher")
	list := decode[struct {
		Items []watcher `json:"items"`
	}](t, w.serve(t, w.maker, "GET", "/watchers", nil))
	if len(list.Items) != 1 {
		t.Errorf("list = %d", len(list.Items))
	}
	checks := decode[struct {
		Items []check `json:"items"`
	}](t, w.serve(t, w.maker, "GET", path+"/checks", nil))
	if checks.Items == nil || len(checks.Items) != 0 {
		t.Errorf("checks before any look = %+v, want an empty page", checks.Items)
	}
	if rec := w.serve(t, w.maker, "POST", path+"/run", nil); rec.Code != http.StatusOK {
		t.Errorf("check now = %d %s", rec.Code, rec.Body)
	}
	want(t, w.serve(t, w.other, "PATCH", path, map[string]any{"objective": "Else."}), http.StatusNotFound, "no such watcher")
	if rec := w.serve(t, w.maker, "PATCH", path, map[string]any{"status": "paused"}); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"by_person"`) {
		t.Errorf("pause = %d %s", rec.Code, rec.Body)
	}
	if rec := w.serve(t, w.maker, "DELETE", path, nil); rec.Code != http.StatusNoContent {
		t.Errorf("delete = %d", rec.Code)
	}
}

// What a watcher may not be is a 400 saying why, and nothing is made.
func TestAWatcherIsRefusedForWhatItMayNotBe(t *testing.T) {
	w := newWorld(t)
	for _, c := range []struct {
		edit func(map[string]any)
		says string
	}{
		{func(b map[string]any) { b["url"] = "file:///etc/passwd" }, "http or https"},
		{func(b map[string]any) { b["every_seconds"] = int64(1) << 40 }, "every_seconds is out of range"},
		{func(b map[string]any) { b["cooldown_seconds"] = -1 }, "cooldown_seconds is out of range"},
		{func(b map[string]any) { b["source"] = "webhook" }, "takes no url"},
		{func(b map[string]any) { b["selector"] = "[[" }, "selector"},
	} {
		body := pageWatcher("w")
		c.edit(body)
		want(t, w.serve(t, w.maker, "POST", "/watchers", body), http.StatusBadRequest, c.says)
	}
	want(t, w.serve(t, w.maker, "GET", "/watchers/nope", nil), http.StatusBadRequest, "watcher id must be a uuid")
	want(t, w.serve(t, w.maker, "GET", "/watchers?all=true", nil), http.StatusForbidden, "schedule:manage")
	var n int
	if err := w.raw.QueryRow(t.Context(), `SELECT count(*) FROM watchers`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d watchers after refusals, want none", n)
	}
}
