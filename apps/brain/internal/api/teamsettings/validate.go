package teamsettings

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"

	sandboxpkg "github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	providerNone           = "none"
	providerSearxng        = "searxng"
	providerBrave          = "brave"
	providerTavily         = "tavily"
	providerFirecrawlCloud = "firecrawl-cloud"
	providerFirecrawlOSS   = "firecrawl-oss"

	maxSampleShare = 100
	maxDailyPasses = 200
	// What an improver pass may spend: tokens across its tries, at least
	// enough for a few calls on a long setup; minutes for each try.
	minPassTokens  = 100_000
	maxPassTokens  = 2_000_000
	maxPassMinutes = 25
	maxDailyRuns   = 100

	// The efficiency pass's ranges, approved in the I5 design.
	minEfficiencyRuns     = 10
	maxEfficiencyRuns     = 500
	minEfficiencyDays     = 7
	maxEfficiencyDays     = 60
	minEfficiencySpread   = 125
	maxEfficiencySpread   = 1000
	minEfficiencyMarkRuns = 2
	maxEfficiencyMarkRuns = 50
	maxEfficiencyMargin   = 3
	maxReplayRuns         = 10
	minReplayTokens       = 20_000
	maxReplayTokens       = 1_000_000
	maxReplayMinutes      = 30
	maxReplayDaily        = 500

	minCompaction = 10
	maxCompaction = 10000
	// Below this a run would compact almost every step: a compaction keeps
	// about 15,000 tokens besides the prompt and the tools.
	minCompactionTokens = 30_000
	maxCompactionTokens = 1_000_000
	maxKeyBytes         = 4096
)

func validate(req settingsRequest, stored *db.TeamSetting, allow func(netip.Addr) bool) error {
	if req.Model != nil {
		if err := checkModel("model", *req.Model); err != nil {
			return err
		}
	}
	if req.JudgeModel.Model != nil {
		if err := checkModel("judge_model", *req.JudgeModel.Model); err != nil {
			return err
		}
	}
	if s := req.ImprovementSampleShare; s != nil && (*s < 0 || *s > maxSampleShare) {
		return fmt.Errorf("improvement_sample_share must be between 0 and %d", maxSampleShare)
	}
	if n := req.ImprovementDailyPasses; n != nil && (*n < 1 || *n > maxDailyPasses) {
		return fmt.Errorf("improvement_daily_passes must be between 1 and %d", maxDailyPasses)
	}
	if n := req.ImprovementPassTokens; n != nil && (*n < minPassTokens || *n > maxPassTokens) {
		return fmt.Errorf("improvement_pass_tokens must be between %d and %d", minPassTokens, maxPassTokens)
	}
	if n := req.ImprovementPassMinutes; n != nil && (*n < 1 || *n > maxPassMinutes) {
		return fmt.Errorf("improvement_pass_minutes must be between 1 and %d", maxPassMinutes)
	}
	if n := req.ImprovementDailyRuns; n != nil && (*n < 1 || *n > maxDailyRuns) {
		return fmt.Errorf("improvement_daily_runs must be between 1 and %d", maxDailyRuns)
	}
	if err := checkEfficiency(req, stored); err != nil {
		return err
	}
	if err := checkReplay(req); err != nil {
		return err
	}
	if req.CompactionMessages < minCompaction || req.CompactionMessages > maxCompaction {
		return fmt.Errorf("compaction_messages must be between %d and %d", minCompaction, maxCompaction)
	}
	if err := checkCompactionTokens(compactionTokens(req, stored)); err != nil {
		return err
	}
	if err := checkSearch(req.Search, stored, allow); err != nil {
		return err
	}
	if req.Sandbox != nil {
		if err := checkSandbox(*req.Sandbox, stored, allow); err != nil {
			return err
		}
	}
	if req.Browser != nil {
		return checkBrowser(*req.Browser, stored, allow)
	}
	return nil
}

// compactionTokens is the pair a save leaves: each value the request gives,
// else the stored one, else the default.
func compactionTokens(req settingsRequest, stored *db.TeamSetting) (least, most int32) {
	least, most = defaultCompactionTokensMin, defaultCompactionTokensMax
	if stored != nil {
		least, most = stored.CompactionTokensMin, stored.CompactionTokensMax
	}
	if req.CompactionTokensMin != nil {
		least = *req.CompactionTokensMin
	}
	if req.CompactionTokensMax != nil {
		most = *req.CompactionTokensMax
	}
	return least, most
}

// checkCompactionTokens checks the pair a save leaves, so a request that
// changes one is held to the other as stored.
func checkCompactionTokens(least, most int32) error {
	for _, v := range []struct {
		name  string
		value int32
	}{{"compaction_tokens_min", least}, {"compaction_tokens_max", most}} {
		if v.value < minCompactionTokens || v.value > maxCompactionTokens {
			return fmt.Errorf("%s must be between %d and %d", v.name, minCompactionTokens, maxCompactionTokens)
		}
	}
	if least > most {
		return fmt.Errorf("compaction_tokens_min, %d, must not be above compaction_tokens_max, %d", least, most)
	}
	return nil
}

func checkModel(field string, m modelRef) error {
	switch {
	case m.Name == "" || strings.TrimSpace(m.Name) != m.Name || strings.ContainsRune(m.Name, 0):
		return errors.New(field + ".name must name a model")
	case m.MaxTokens <= 0:
		return errors.New(field + ".max_tokens must be greater than 0")
	case m.Temperature != nil && (*m.Temperature < 0 || *m.Temperature > 2):
		return errors.New(field + ".temperature must be between 0 and 2")
	}
	return nil
}

// judgeModel is the judge model a save leaves: the request's when it names
// one (null clears it), else the stored one.
func judgeModel(req settingsRequest, stored *db.TeamSetting) *modelRef {
	if req.JudgeModel.Set {
		return req.JudgeModel.Model
	}
	if stored != nil && stored.JudgeModelName != nil && stored.JudgeModelMaxTokens != nil {
		return &modelRef{Name: *stored.JudgeModelName, MaxTokens: *stored.JudgeModelMaxTokens, Temperature: stored.JudgeModelTemperature}
	}
	return nil
}

// sampleShare is the share a save leaves: the request's, else the stored
// one, else the default.
func sampleShare(req settingsRequest, stored *db.TeamSetting) int16 {
	switch {
	case req.ImprovementSampleShare != nil:
		return *req.ImprovementSampleShare
	case stored != nil:
		return stored.ImprovementSampleShare
	}
	return defaultSampleShare
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
		return checkSelfHostedURL("search", *s.URL, allow)
	case providerFirecrawlOSS:
		if s.URL == nil {
			return errors.New("search.url is required for firecrawl-oss")
		}
		if err := checkSelfHostedURL("search", *s.URL, allow); err != nil {
			return err
		}
		if s.APIKey == nil {
			if hadKey(stored, s.Provider) && !keepsKey(stored, s) {
				return errors.New("search.api_key is required when the url changes: " +
					"send the key again, or an empty one if the instance needs none")
			}
			return nil
		}
		if *s.APIKey == "" {
			return nil
		}
		return checkKey("search", *s.APIKey)
	case providerBrave, providerTavily, providerFirecrawlCloud:
		if s.URL != nil {
			return errUnwantedURL
		}
		if s.APIKey == nil {
			if keepsKey(stored, s) {
				return nil
			}
			return fmt.Errorf("search.api_key is required: no %s key is saved", s.Provider)
		}
		return checkKey("search", *s.APIKey)
	default:
		return errors.New("search.provider must be none, searxng, brave, tavily, firecrawl-cloud or firecrawl-oss")
	}
}

var (
	errUnwantedURL = errors.New("search.url is only for searxng and firecrawl-oss")
	errUnwantedKey = errors.New("search.api_key is only for brave, tavily and firecrawl")
)

func hadKey(stored *db.TeamSetting, provider string) bool {
	return stored != nil && stored.SearchProvider == provider && stored.SearchSecretID != nil
}

func keepsKey(stored *db.TeamSetting, s searchRequest) bool {
	return hadKey(stored, s.Provider) && sameURL(stored.SearchUrl, s.URL)
}

func sameURL(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func checkKey(field, key string) error {
	switch {
	case key == "":
		return fmt.Errorf("%s.api_key must not be empty", field)
	case len(key) > maxKeyBytes:
		return fmt.Errorf("%s.api_key must be at most %d bytes", field, maxKeyBytes)
	case strings.ContainsRune(key, 0):
		return fmt.Errorf("%s.api_key must not contain a NUL character", field)
	}
	return nil
}

func checkSelfHostedURL(field, raw string, allow func(netip.Addr) bool) error {
	return checkURL(field, raw, allow, "http", "https")
}

// checkURL is a self-hosted service's address: one of schemes, a host, no
// credentials, query or fragment, and never a private address unless allow
// says it may be.
func checkURL(field, raw string, allow func(netip.Addr) bool, schemes ...string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil || !slices.Contains(schemes, u.Scheme) || u.Hostname() == "":
		return fmt.Errorf("%s.url must be a URL with a host, over %s", field, strings.Join(schemes, " or "))
	case u.User != nil:
		return fmt.Errorf("%s.url must not carry credentials", field)
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return fmt.Errorf("%s.url must not have a query or a fragment", field)
	}

	if addr, err := netip.ParseAddr(u.Hostname()); err == nil {
		addr = addr.Unmap()
		if addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || !allow(addr) {
			return fmt.Errorf("%s.url must not point at a private, loopback or link-local address", field)
		}
	}
	return nil
}

// checkSandbox is where the team's command servers run. Every provider takes
// a key; sandboxd also its URL, Daytona one when self-hosted, E2B none. A key
// is sent again when the provider or its URL changes, so a key is never sent
// somewhere it was not saved for.
func checkSandbox(s sandboxRequest, stored *db.TeamSetting, allow func(netip.Addr) bool) error {
	// Before the provider: the times are stored whatever it is, and a bad one
	// is the caller's to hear, not the table's CHECK.
	if err := checkLifecycle(s); err != nil {
		return err
	}
	switch s.Provider {
	case string(sandboxpkg.None):
		if s.URL != nil || s.APIKey != nil || s.Template != nil {
			return errors.New("sandbox.url, sandbox.template and sandbox.api_key are for a sandbox provider; none takes none")
		}
		return nil
	case string(sandboxpkg.E2B), string(sandboxpkg.Daytona), string(sandboxpkg.Sandboxd):
	default:
		return errors.New("sandbox.provider must be none, e2b, daytona or sandboxd")
	}

	if err := checkTemplate(s); err != nil {
		return err
	}
	switch {
	case s.URL != nil && s.Provider == string(sandboxpkg.E2B):
		return errors.New("sandbox.url is for daytona or sandboxd: e2b is hosted only")
	case s.URL == nil && s.Provider == string(sandboxpkg.Sandboxd):
		return errors.New("sandbox.url is required for sandboxd: where it runs")
	case s.URL != nil:
		if err := checkSelfHostedURL("sandbox", *s.URL, allow); err != nil {
			return err
		}
	}
	if s.APIKey == nil {
		if keepsSandboxKey(stored, s) {
			return nil
		}
		return fmt.Errorf("sandbox.api_key is required: no %s key is saved for this provider and url", s.Provider)
	}
	return checkKey("sandbox", *s.APIKey)
}

// checkLifecycle holds the stop and keep times and the size to what the
// table allows; a provider refuses a size past what it gives.
func checkLifecycle(s sandboxRequest) error {
	if s.KeepHours != nil && (*s.KeepHours < 1 || *s.KeepHours > 720) {
		return errors.New("sandbox.keep_hours must be between 1 and 720")
	}
	if v := s.IdleMinutes.Value; v != nil && (*v < 1 || *v > 1440) {
		return errors.New("sandbox.idle_minutes must be between 1 and 1440, or null for the deployment's")
	}
	z := s.WorkspaceSize.Value
	if z == nil {
		return nil
	}
	switch {
	case z.MemoryMB < 256 || z.MemoryMB > 65536:
		return errors.New("sandbox.workspace_size.memory_mb must be between 256 and 65536")
	case z.CPUs < 1 || z.CPUs > 16:
		return errors.New("sandbox.workspace_size.cpus must be between 1 and 16")
	case z.DiskMB < 256 || z.DiskMB > 262144:
		return errors.New("sandbox.workspace_size.disk_mb must be between 256 and 262144")
	}
	return nil
}

func keepsSandboxKey(stored *db.TeamSetting, s sandboxRequest) bool {
	return stored != nil && stored.SandboxSecretID != nil && stored.SandboxProvider == s.Provider &&
		sameURL(stored.SandboxUrl, s.URL)
}

const maxTemplateBytes = 128

// templateName is what E2B calls a template: its name, or another team's
// public one as owner/name.
var templateName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*(/[a-z0-9][a-z0-9_.-]*)?$`)

// checkTemplate allows a template for E2B alone: every other provider is
// given the brain's own image, and E2B cannot be handed an image at all.
func checkTemplate(s sandboxRequest) error {
	switch {
	case s.Template == nil:
		return nil
	case s.Provider != string(sandboxpkg.E2B):
		return errors.New("sandbox.template is for e2b; the brain chooses every other provider's image")
	case len(*s.Template) > maxTemplateBytes || !templateName.MatchString(*s.Template):
		return fmt.Errorf("sandbox.template must be an E2B template name, such as openarity-sandbox, at most %d bytes", maxTemplateBytes)
	}
	return nil
}

// checkReplay holds each replay number a request sends to its range.
func checkReplay(req settingsRequest) error {
	for _, v := range replayRanges(req) {
		if v.value != nil && (*v.value < v.min || *v.value > v.max) {
			return fmt.Errorf("%s must be between %d and %d", v.name, v.min, v.max)
		}
	}
	return nil
}

type replayRange struct {
	name     string
	value    *int32
	min, max int32
}

// replayRanges are the replay numbers a request may send, with their ranges:
// the migration's checks and the API's description hold the same.
func replayRanges(req settingsRequest) []replayRange {
	return []replayRange{
		{"improvement_replay_runs", req.ImprovementReplayRuns, 1, maxReplayRuns},
		{"improvement_replay_tokens", req.ImprovementReplayTokens, minReplayTokens, maxReplayTokens},
		{"improvement_replay_minutes", req.ImprovementReplayMinutes, 1, maxReplayMinutes},
		{"improvement_replay_daily", req.ImprovementReplayDaily, 1, maxReplayDaily},
	}
}

// checkEfficiency holds each efficiency number a request sends to its
// range, and the mark runs a save leaves to at most the runs it leaves, so a
// request that changes one is held to the other as stored.
func checkEfficiency(req settingsRequest, stored *db.TeamSetting) error {
	for _, v := range []struct {
		name     string
		value    *int16
		min, max int16
	}{
		{"improvement_efficiency_runs", req.ImprovementEfficiencyRuns, minEfficiencyRuns, maxEfficiencyRuns},
		{"improvement_efficiency_days", req.ImprovementEfficiencyDays, minEfficiencyDays, maxEfficiencyDays},
		{"improvement_efficiency_spread", req.ImprovementEfficiencySpread, minEfficiencySpread, maxEfficiencySpread},
		{"improvement_efficiency_mark_runs", req.ImprovementEfficiencyMarkRuns, minEfficiencyMarkRuns, maxEfficiencyMarkRuns},
		{"improvement_efficiency_margin", req.ImprovementEfficiencyMargin, 0, maxEfficiencyMargin},
	} {
		if v.value != nil && (*v.value < v.min || *v.value > v.max) {
			return fmt.Errorf("%s must be between %d and %d", v.name, v.min, v.max)
		}
	}
	runs, marks := int16(defaultEfficiencyRuns), int16(defaultEfficiencyMarkRuns)
	if stored != nil {
		runs, marks = stored.ImprovementEfficiencyRuns, stored.ImprovementEfficiencyMarkRuns
	}
	if req.ImprovementEfficiencyRuns != nil {
		runs = *req.ImprovementEfficiencyRuns
	}
	if req.ImprovementEfficiencyMarkRuns != nil {
		marks = *req.ImprovementEfficiencyMarkRuns
	}
	if marks > runs {
		return fmt.Errorf("improvement_efficiency_mark_runs, %d, must not be above improvement_efficiency_runs, %d", marks, runs)
	}
	return nil
}
