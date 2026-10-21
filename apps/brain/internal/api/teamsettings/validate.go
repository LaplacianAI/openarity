package teamsettings

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	providerNone           = "none"
	providerSearxng        = "searxng"
	providerBrave          = "brave"
	providerTavily         = "tavily"
	providerFirecrawlCloud = "firecrawl-cloud"
	providerFirecrawlOSS   = "firecrawl-oss"

	minCompaction = 10
	maxCompaction = 10000
	maxKeyBytes   = 4096
)

func validate(req settingsRequest, stored *db.TeamSetting, allow func(netip.Addr) bool) error {
	if req.Model != nil {
		if err := checkModel(*req.Model); err != nil {
			return err
		}
	}
	if req.CompactionMessages < minCompaction || req.CompactionMessages > maxCompaction {
		return fmt.Errorf("compaction_messages must be between %d and %d", minCompaction, maxCompaction)
	}
	return checkSearch(req.Search, stored, allow)
}

func checkModel(m modelRef) error {
	switch {
	case m.Name == "" || strings.TrimSpace(m.Name) != m.Name || strings.ContainsRune(m.Name, 0):
		return errors.New("model.name must name a model")
	case m.MaxTokens <= 0:
		return errors.New("model.max_tokens must be greater than 0")
	case m.Temperature != nil && (*m.Temperature < 0 || *m.Temperature > 2):
		return errors.New("model.temperature must be between 0 and 2")
	}
	return nil
}

func checkSearch(s searchRequest, stored *db.TeamSetting, allow func(netip.Addr) bool) error {
	switch s.Provider {
	case providerNone:
		if s.URL != nil {
			return errUnwantedURL
		}
		if s.APIKey != nil {
			return errUnwantedKey
		}
		return nil
	case providerSearxng:
		if s.APIKey != nil {
			return errUnwantedKey
		}
		if s.URL == nil {
			return errors.New("search.url is required for searxng")
		}
		return checkSelfHostedURL(*s.URL, allow)
	case providerFirecrawlOSS:
		if s.URL == nil {
			return errors.New("search.url is required for firecrawl-oss")
		}
		if err := checkSelfHostedURL(*s.URL, allow); err != nil {
			return err
		}
		if s.APIKey == nil || *s.APIKey == "" {
			return nil
		}
		return checkKey(*s.APIKey)
	case providerBrave, providerTavily, providerFirecrawlCloud:
		if s.URL != nil {
			return errUnwantedURL
		}
		if s.APIKey == nil {
			if keepsKey(stored, s.Provider) {
				return nil
			}
			return fmt.Errorf("search.api_key is required: no %s key is saved", s.Provider)
		}
		return checkKey(*s.APIKey)
	default:
		return errors.New("search.provider must be none, searxng, brave, tavily, firecrawl-cloud or firecrawl-oss")
	}
}

var (
	errUnwantedURL = errors.New("search.url is only for searxng and firecrawl-oss")
	errUnwantedKey = errors.New("search.api_key is only for brave, tavily and firecrawl")
)

func keepsKey(stored *db.TeamSetting, provider string) bool {
	return stored != nil && stored.SearchProvider == provider && stored.SearchSecretID != nil
}

func checkKey(key string) error {
	switch {
	case key == "":
		return errors.New("search.api_key must not be empty")
	case len(key) > maxKeyBytes:
		return fmt.Errorf("search.api_key must be at most %d bytes", maxKeyBytes)
	case strings.ContainsRune(key, 0):
		return errors.New("search.api_key must not contain a NUL character")
	}
	return nil
}

func checkSelfHostedURL(raw string, allow func(netip.Addr) bool) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "":
		return errors.New("search.url must be an http or https URL with a host")
	case u.User != nil:
		return errors.New("search.url must not carry credentials")
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return errors.New("search.url must not have a query or a fragment")
	}

	if addr, err := netip.ParseAddr(u.Hostname()); err == nil {
		addr = addr.Unmap()
		if addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || !allow(addr) {
			return errors.New("search.url must not point at a private, loopback or link-local address")
		}
	}
	return nil
}
