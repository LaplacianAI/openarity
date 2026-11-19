package teamsettings

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"

	"github.com/google/uuid"

	browserpkg "github.com/LaplacianAI/openarity/apps/brain/internal/browser"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// checkBrowser is what the team's runs drive pages in. A hosted browser takes
// a key and no url; Browserless, Steel and Lightpanda also run at a url of the
// team's own, cdp only there, and one there may need no key. A key is sent
// again when the provider or its url changes, so it never reaches somewhere
// it was not saved for.
func checkBrowser(b browserRequest, stored *db.TeamSetting, allow func(netip.Addr) bool) error {
	p := browserpkg.Provider(b.Provider)
	switch {
	case p == browserpkg.None:
		if b.URL != nil || b.APIKey != nil {
			return errors.New("browser.url and browser.api_key are for a browser provider; none takes neither")
		}
		return nil
	case !p.Known():
		names := make([]string, len(browserpkg.Providers))
		for i, q := range browserpkg.Providers {
			names[i] = string(q)
		}
		return fmt.Errorf("browser.provider must be one of %s", strings.Join(names, ", "))
	case b.URL != nil && !p.TakesURL():
		return fmt.Errorf("browser.url is for browserless, steel, lightpanda or cdp: %s is hosted only", p)
	case b.URL == nil && p.NeedsURL():
		return errors.New("browser.url is required for cdp: where the browser is")
	case b.URL != nil:
		if err := checkURL("browser", *b.URL, allow, "http", "https", "ws", "wss"); err != nil {
			return err
		}
	}

	url := ""
	if b.URL != nil {
		url = *b.URL
	}
	if b.APIKey == nil {
		switch {
		case keepsBrowserKey(stored, b):
			return nil
		case p.NeedsKey(url):
			return fmt.Errorf("browser.api_key is required: no %s key is saved for this provider and url", p)
		case hadBrowserKey(stored, p):
			return errors.New("browser.api_key is required when the url changes: " +
				"send the key again, or an empty one if the browser needs none")
		}
		return nil
	}
	if *b.APIKey == "" {
		if p.NeedsKey(url) {
			return errors.New("browser.api_key must not be empty: a hosted browser is reached with its key")
		}
		return nil
	}
	if err := checkKey("browser", *b.APIKey); err != nil {
		return err
	}
	if p == browserpkg.BrightData && !strings.Contains(*b.APIKey, ":") {
		return errors.New("browser.api_key for brightdata is the zone's username and password, as USER:PASS")
	}
	return nil
}

func hadBrowserKey(stored *db.TeamSetting, p browserpkg.Provider) bool {
	return stored != nil && stored.BrowserSecretID != nil && stored.BrowserProvider == string(p)
}

func keepsBrowserKey(stored *db.TeamSetting, b browserRequest) bool {
	return hadBrowserKey(stored, browserpkg.Provider(b.Provider)) && sameURL(stored.BrowserUrl, b.URL)
}

type browserRow struct {
	provider string
	url      *string
	secretID *uuid.UUID
}

// browserOf is the browser to save, and the path of a key it wrote, if any,
// for the caller to forget when the save fails. A request without a browser
// keeps the stored one.
func (h *handler) browserOf(
	w http.ResponseWriter, r *http.Request, teamID uuid.UUID, b *browserRequest, stored *db.TeamSetting,
) (browserRow, string, bool) {
	if b == nil {
		if stored == nil {
			return browserRow{provider: string(browserpkg.None)}, "", true
		}
		return browserRow{stored.BrowserProvider, stored.BrowserUrl, stored.BrowserSecretID}, "", true
	}
	row := browserRow{provider: b.Provider, url: b.URL}
	switch {
	case b.Provider == string(browserpkg.None), b.APIKey != nil && *b.APIKey == "":
		return row, "", true
	case b.APIKey != nil:
		id := uuid.New()
		path := secrets.Path(teamID, secrets.KindBrowser, id)
		if err := h.secrets.Put(r.Context(), path, KeyAPI, *b.APIKey); err != nil {
			h.logger.Warn("browser key not stored", "team", teamID, "error", err)
			http.Error(w, "the secret store is unavailable", http.StatusServiceUnavailable)
			return browserRow{}, "", false
		}
		row.secretID = &id
		return row, path, true
	case keepsBrowserKey(stored, *b):
		row.secretID = stored.BrowserSecretID
	}
	return row, "", true
}

func browserView(row db.TeamSetting) browser {
	return browser{Provider: row.BrowserProvider, URL: row.BrowserUrl, KeySet: row.BrowserSecretID != nil}
}
