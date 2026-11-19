package teamsettings

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

// withBrowser is a whole save whose browser is b: JSON, or "" to leave it out.
func withBrowser(b string) string {
	s := `{"model":null,"search":{"provider":"none"},`
	if b != "" {
		s += `"browser":` + b + `,`
	}
	return s + `"learning":false,"compaction_messages":100}`
}

func (e *env) storedBrowser(provider string, secretID *uuid.UUID, url *string) {
	e.stored("none", nil, nil)
	e.store.row.BrowserProvider, e.store.row.BrowserSecretID, e.store.row.BrowserUrl = provider, secretID, url
}

func TestATeamThatNeverSavedHasNoBrowser(t *testing.T) {
	got := decode(t, newEnv().get(t))
	if got.Browser.Provider != "none" || got.Browser.KeySet || got.Browser.URL != nil {
		t.Errorf("browser = %+v, want none", got.Browser)
	}
}

// The key goes to a path of its own under the browser kind, named by the id
// the row keeps, and never comes back out.
func TestABrowserKeyGoesToItsOwnPath(t *testing.T) {
	e := newEnv()
	rec := e.save(t, withBrowser(`{"provider":"browserbase","api_key":"bb-secret"}`))
	got := decode(t, rec)

	saved := e.store.upserts[0]
	if saved.BrowserSecretID == nil || len(e.secrets.puts) != 1 {
		t.Fatalf("secret id %v, puts %+v; want one key written and named", saved.BrowserSecretID, e.secrets.puts)
	}
	p := e.secrets.puts[0]
	if want := secrets.Path(e.team, secrets.KindBrowser, *saved.BrowserSecretID); p.path != want || p.key != KeyAPI || p.value != "bb-secret" {
		t.Errorf("put %+v, want %s %s = the key", p, want, KeyAPI)
	}
	if got.Browser.Provider != "browserbase" || !got.Browser.KeySet || got.Browser.URL != nil {
		t.Errorf("browser = %+v", got.Browser)
	}
	if strings.Contains(rec.Body.String(), "bb-secret") {
		t.Errorf("the response carries the key: %s", rec.Body)
	}
}

// A self-hosted browser may have no key: saved with none, or with an empty one.
func TestASelfHostedBrowserIsSavedWithNoKey(t *testing.T) {
	for name, b := range map[string]string{
		"no key":       `{"provider":"lightpanda","url":"ws://lightpanda.example:9222"}`,
		"an empty key": `{"provider":"steel","url":"http://steel.example:3000","api_key":""}`,
		"cdp":          `{"provider":"cdp","url":"ws://chrome.example:9222"}`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv()
			rec := e.save(t, withBrowser(b))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			if p := e.store.upserts[0]; p.BrowserSecretID != nil || p.BrowserUrl == nil || len(e.secrets.puts) != 0 {
				t.Errorf("saved %+v, puts %+v; want the url and no key", p, e.secrets.puts)
			}
		})
	}
}

// A client that predates browsers sends none, and the team's stays.
func TestASaveWithoutABrowserKeepsTheStoredOne(t *testing.T) {
	e := newEnv()
	id := uuid.New()
	e.storedBrowser("kernel", &id, nil)

	got := decode(t, e.save(t, withBrowser("")))

	if p := e.store.upserts[0]; p.BrowserProvider != "kernel" || p.BrowserSecretID == nil || *p.BrowserSecretID != id {
		t.Errorf("saved %q, %v; want kernel and the stored key kept", p.BrowserProvider, p.BrowserSecretID)
	}
	if len(e.secrets.puts) != 0 || got.Browser.Provider != "kernel" || !got.Browser.KeySet {
		t.Errorf("puts %+v, browser %+v", e.secrets.puts, got.Browser)
	}
}

func TestABrowserKeyIsKeptOnlyForTheSameProviderAndURL(t *testing.T) {
	id := uuid.New()
	for name, tc := range map[string]struct {
		browser string
		keeps   bool
		says    string
	}{
		"the same provider and url": {`{"provider":"browserless","url":"wss://bl.example"}`, true, ""},
		"another url":               {`{"provider":"browserless","url":"wss://other.example"}`, false, "send the key again"},
		"the cloud, not the url":    {`{"provider":"browserless"}`, false, "browser.api_key is required"},
		"another provider":          {`{"provider":"steel"}`, false, "browser.api_key is required"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv()
			e.storedBrowser("browserless", &id, ptr("wss://bl.example"))
			rec := e.save(t, withBrowser(tc.browser))
			switch {
			case tc.keeps && rec.Code != http.StatusOK:
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			case tc.keeps && (e.store.upserts[0].BrowserSecretID == nil || *e.store.upserts[0].BrowserSecretID != id):
				t.Errorf("the stored key was not kept")
			case !tc.keeps && (rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.says)):
				t.Errorf("status %d %q, want 400 saying %q: a key saved for one place must not reach another", rec.Code, rec.Body, tc.says)
			case !tc.keeps && len(e.store.upserts) != 0:
				t.Error("a refused save was written")
			}
		})
	}
}

// A self-hosted browser saved again with no key keeps none it never had, and
// a key it had is dropped only when an empty one says so.
func TestAnEmptyKeyDropsTheStoredOne(t *testing.T) {
	e := newEnv()
	id := uuid.New()
	e.storedBrowser("browserless", &id, ptr("ws://bl.example:3000"))

	got := decode(t, e.save(t, withBrowser(`{"provider":"browserless","url":"ws://bl.example:3000","api_key":""}`)))
	if p := e.store.upserts[0]; p.BrowserSecretID != nil {
		t.Errorf("saved key %v, want it dropped", p.BrowserSecretID)
	}
	if got.Browser.KeySet {
		t.Error("key_set after an empty key")
	}
}

func TestTurningTheBrowserOffDropsItsKey(t *testing.T) {
	e := newEnv()
	id := uuid.New()
	e.storedBrowser("browserless", &id, ptr("ws://bl.example:3000"))

	got := decode(t, e.save(t, withBrowser(`{"provider":"none"}`)))
	if p := e.store.upserts[0]; p.BrowserProvider != "none" || p.BrowserSecretID != nil || p.BrowserUrl != nil {
		t.Errorf("saved %+v, want nothing but none", p)
	}
	if got.Browser.KeySet {
		t.Error("key_set with no browser")
	}
}

// A refused browser is refused before anything is written.
func TestABadBrowserIsRefused(t *testing.T) {
	for name, tc := range map[string]struct{ browser, says string }{
		"an unknown provider":         {`{"provider":"chrome","api_key":"k"}`, "browser.provider must be one of none, browserless"},
		"a hosted one at a url":       {`{"provider":"kernel","url":"https://k.example","api_key":"k"}`, "kernel is hosted only"},
		"cdp nowhere":                 {`{"provider":"cdp"}`, "required for cdp"},
		"a hosted one with no key":    {`{"provider":"anchor"}`, "browser.api_key is required"},
		"a hosted one with an empty":  {`{"provider":"hyperbrowser","api_key":""}`, "must not be empty"},
		"none with a url":             {`{"provider":"none","url":"ws://x.example"}`, "none takes neither"},
		"a url with credentials":      {`{"provider":"cdp","url":"ws://u:p@chrome.example"}`, "must not carry credentials"},
		"a url with a token in it":    {`{"provider":"cdp","url":"ws://chrome.example?token=t"}`, "must not have a query"},
		"a url over ftp":              {`{"provider":"cdp","url":"ftp://chrome.example"}`, "over http or https or ws or wss"},
		"a private address":           {`{"provider":"cdp","url":"ws://10.0.0.5:9222"}`, "private"},
		"brightdata without USER:PAS": {`{"provider":"brightdata","api_key":"just-a-token"}`, "USER:PASS"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv()
			rec := e.save(t, withBrowser(tc.browser))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.says) {
				t.Errorf("status %d %q, want 400 saying %q", rec.Code, rec.Body, tc.says)
			}
			if len(e.secrets.puts) != 0 || len(e.store.upserts) != 0 {
				t.Error("a refused browser wrote something")
			}
		})
	}
}

// A save that fails after all three keys were written leaves none unnamed.
func TestAFailedSaveDestroysTheBrowserKeyToo(t *testing.T) {
	e := newEnv()
	e.store.upsertErr = errAny

	rec := e.save(t, `{"model":null,"search":{"provider":"brave","api_key":"b"},`+
		`"sandbox":{"provider":"e2b","api_key":"s"},"browser":{"provider":"kernel","api_key":"k"},`+
		`"learning":false,"compaction_messages":100}`)

	if rec.Code != http.StatusInternalServerError || len(e.secrets.puts) != 3 || len(e.store.recorded) != 3 {
		t.Fatalf("status %d, puts %+v, recorded %+v; want 500 and all three keys recorded", rec.Code, e.secrets.puts, e.store.recorded)
	}
	for i, r := range e.store.recorded {
		if r.Path != e.secrets.puts[i].path {
			t.Errorf("recorded %s, want %s", r.Path, e.secrets.puts[i].path)
		}
	}
}

// The keys written before the browser key are not left behind when it cannot be.
func TestABrowserKeyTheStoreRefusesForgetsTheOthers(t *testing.T) {
	e := newEnv()
	e.secrets.putErrAfter = 2

	rec := e.save(t, `{"model":null,"search":{"provider":"brave","api_key":"b"},`+
		`"sandbox":{"provider":"e2b","api_key":"s"},"browser":{"provider":"kernel","api_key":"k"},`+
		`"learning":false,"compaction_messages":100}`)

	if rec.Code != http.StatusServiceUnavailable || len(e.store.upserts) != 0 {
		t.Fatalf("status %d, upserts %d; want 503 and nothing saved", rec.Code, len(e.store.upserts))
	}
	if len(e.store.recorded) != 2 || e.store.recorded[0].Path != e.secrets.puts[0].path || e.store.recorded[1].Path != e.secrets.puts[1].path {
		t.Errorf("recorded %+v, want the search and sandbox keys just written", e.store.recorded)
	}
}

// A key belongs to the provider it was saved for: switching to a self-hosted
// browser that needs none leaves the old provider's key behind, rather than
// handing it to a browser of the team's own.
func TestAKeyIsNotCarriedToASelfHostedBrowser(t *testing.T) {
	e := newEnv()
	id := uuid.New()
	e.storedBrowser("kernel", &id, nil)

	rec := e.save(t, withBrowser(`{"provider":"lightpanda","url":"ws://lightpanda.example:9222"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if p := e.store.upserts[0]; p.BrowserSecretID != nil {
		t.Errorf("saved the kernel key %v for lightpanda, want none", *p.BrowserSecretID)
	}
	if decode(t, rec).Browser.KeySet {
		t.Error("key_set for a browser saved with no key")
	}
}
