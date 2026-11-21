package teams

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

const maxKeyBytes = 4096

// defaultE2BTemplate is what the brain starts an E2B team's rooms from when
// the team names none, and what deployment/sandbox/e2b builds by default.
const defaultE2BTemplate = "openarity-sandbox"

// defaultJudgeMaxTokens is the output limit of a judge model named without
// one: a verdict is a few sentences, so this is generous.
const defaultJudgeMaxTokens = 4096

var settingFlags = []string{
	"model", "max-tokens", "temperature", "no-model",
	"search", "search-url", "search-key-stdin", "no-search-key",
	"improvement", "judge-model", "judge-max-tokens", "judge-temperature", "no-judge-model", "sample-share", "daily-passes", "pass-tokens", "pass-minutes",
	"compaction-messages", "compaction-tokens-min", "compaction-tokens-max", "hub", "no-hub",
	"sandbox", "sandbox-url", "sandbox-key-stdin", "e2b-template", "no-e2b-template",
	"keep-stopped-hours", "stop-idle-minutes", "no-stop-idle-minutes", "workspace-memory-mb", "workspace-cpus", "workspace-disk-mb", "no-workspace-size",
	"browser", "browser-url", "browser-key-stdin", "no-browser-key",
}

// sandboxFlags are the ones that send a sandbox at all. Without any, the
// request leaves it out and the brain keeps the stored one, key and all.
var sandboxFlags = []string{
	"sandbox", "sandbox-url", "sandbox-key-stdin", "e2b-template", "no-e2b-template",
	"keep-stopped-hours", "stop-idle-minutes", "no-stop-idle-minutes", "workspace-memory-mb", "workspace-cpus", "workspace-disk-mb", "no-workspace-size",
}

// sizeFlags set a workspace's size, which is sent whole.
var sizeFlags = []string{"workspace-memory-mb", "workspace-cpus", "workspace-disk-mb"}

// browserFlags are the ones that send a browser, as sandboxFlags a sandbox.
var browserFlags = []string{"browser", "browser-url", "browser-key-stdin", "no-browser-key"}

func newSettingsCmd(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "A team's model, web search, sandbox, browser, self-improvement, compaction and hub",
		Long: "Every member can read a team's settings. Changing them needs\n" +
			"team:settings in the team, which only admin holds by default.",
	}
	cmd.AddCommand(newSettingsShowCmd(opts), newSettingsSetCmd(opts))
	return cmd
}

func newSettingsShowCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <team>",
		Short: "Show a team's settings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}

			s, err := cli.Result(api.GetTeamSettingsWithResponse(cmd.Context(), team))
			if err != nil {
				return err
			}
			return printSettings(opts, s)
		},
	}
}

type settingsFlags struct {
	model       string
	maxTokens   int32
	temperature float64
	noModel     bool
	search      string
	searchURL   string
	keyStdin    bool
	noKey       bool
	improvement string

	judgeModel       string
	judgeMaxTokens   int32
	judgeTemperature float64
	noJudgeModel     bool
	sampleShare      int32
	dailyPasses      int32
	passTokens       int32
	passMinutes      int32

	compaction int32
	tokensMin  int32
	tokensMax  int32
	hub        string
	noHub      bool

	sandbox         string
	sandboxURL      string
	sandboxKeyStdin bool
	e2bTemplate     string
	noE2BTemplate   bool

	keepHours     int32
	idleMinutes   int32
	noIdleMinutes bool
	memoryMB      int32
	cpus          int32
	diskMB        int32
	noSize        bool

	browser         string
	browserURL      string
	browserKeyStdin bool
	noBrowserKey    bool
}

func newSettingsSetCmd(opts *cli.Options) *cobra.Command {
	var f settingsFlags

	cmd := &cobra.Command{
		Use:   "set <team>",
		Short: "Change a team's settings",
		Long: "Reads the current settings, changes only the flags given, and saves\n" +
			"the whole. A stored search key is kept unless a new one is given, the\n" +
			"provider changes, or a self-hosted provider's URL moves. The sandbox\n" +
			"and browser keys follow the same rule.\n\n" +
			"The key comes from stdin and nowhere else:\n\n" +
			"    printf %s \"$BRAVE_KEY\" | oa teams settings set platform \\\n" +
			"        --search brave --search-key-stdin\n\n" +
			"    printf %s \"$E2B_KEY\" | oa teams settings set platform \\\n" +
			"        --sandbox e2b --sandbox-key-stdin\n\n" +
			"    oa teams settings set platform --browser lightpanda --browser-url ws://lightpanda:9222\n\n" +
			"There is deliberately no --search-key, --sandbox-key or --browser-key flag. Arguments are readable by\n" +
			"every process on the machine through ps, and they land in shell\n" +
			"history.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			changed := cmd.Flags().Changed
			if err := f.check(changed); err != nil {
				return err
			}

			// Read before any request, so an empty pipe fails without the
			// brain ever being asked.
			var key, sandboxKey, browserKey *string
			switch {
			case f.keyStdin:
				k, err := readKey(cmd.InOrStdin(), "search key",
					"pipe the key in, or use --no-search-key for an instance without one")
				if err != nil {
					return err
				}
				key = &k
			case f.noKey:
				empty := ""
				key = &empty
			}
			if f.sandboxKeyStdin {
				k, err := readKey(cmd.InOrStdin(), "sandbox key", "pipe the key in")
				if err != nil {
					return err
				}
				sandboxKey = &k
			}
			switch {
			case f.browserKeyStdin:
				k, err := readKey(cmd.InOrStdin(), "browser key",
					"pipe the key in, or use --no-browser-key for a self-hosted browser without one")
				if err != nil {
					return err
				}
				browserKey = &k
			case f.noBrowserKey:
				empty := ""
				browserKey = &empty
			}

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}

			current, err := cli.Result(api.GetTeamSettingsWithResponse(cmd.Context(), team))
			if err != nil {
				return err
			}

			req := f.apply(changed, current, keys{key, sandboxKey, browserKey})
			body, err := f.body(changed, current, req)
			if err != nil {
				return err
			}
			saved, err := cli.Result(api.SaveTeamSettingsWithBodyWithResponse(cmd.Context(), team,
				"application/json", bytes.NewReader(body)))
			if err != nil {
				return cli.Needs(err, "team:settings")
			}
			return printSettings(opts, saved)
		},
	}

	cmd.Flags().StringVar(&f.model, "model", "", "the model the built-in agents run on, as the gateway names it")
	cmd.Flags().Int32Var(&f.maxTokens, "max-tokens", 0, "the model's output limit per call")
	cmd.Flags().Float64Var(&f.temperature, "temperature", 0, "between 0 and 2")
	cmd.Flags().BoolVar(&f.noModel, "no-model", false, "clear the model, which stops the built-in agents running")
	cmd.Flags().StringVar(&f.search, "search", "",
		"none, searxng, brave, tavily, firecrawl-cloud or firecrawl-oss")
	cmd.Flags().StringVar(&f.searchURL, "search-url", "", "a self-hosted provider's address: searxng or firecrawl-oss")
	cmd.Flags().BoolVar(&f.keyStdin, "search-key-stdin", false, "read the provider's key from stdin")
	cmd.Flags().BoolVar(&f.noKey, "no-search-key", false, "firecrawl-oss only: run the instance with no key")
	cmd.Flags().StringVar(&f.sandbox, "sandbox", "",
		"where command MCP servers run: none, e2b, daytona or sandboxd")
	cmd.Flags().StringVar(&f.sandboxURL, "sandbox-url", "",
		"a self-hosted provider's API address: sandboxd, or a self-hosted daytona")
	cmd.Flags().StringVar(&f.e2bTemplate, "e2b-template", "",
		"e2b only: the template rooms start from, built in your E2B account; default openarity-sandbox")
	cmd.Flags().BoolVar(&f.noE2BTemplate, "no-e2b-template", false, "e2b only: return to the default template")
	cmd.Flags().BoolVar(&f.sandboxKeyStdin, "sandbox-key-stdin", false,
		"read the sandbox provider's key, or sandboxd's token, from stdin")
	cmd.Flags().Int32Var(&f.keepHours, "keep-stopped-hours", 0,
		"how long a stopped workspace keeps its files, 1 to 720 hours; 24 unless set")
	cmd.Flags().Int32Var(&f.idleMinutes, "stop-idle-minutes", 0,
		"stop a running workspace unused this long, 1 to 1440 minutes")
	cmd.Flags().BoolVar(&f.noIdleMinutes, "no-stop-idle-minutes", false, "return to the deployment's idle time")
	cmd.Flags().Int32Var(&f.memoryMB, "workspace-memory-mb", 0, "a workspace room's memory; set with --workspace-cpus and --workspace-disk-mb")
	cmd.Flags().Int32Var(&f.cpus, "workspace-cpus", 0, "a workspace room's CPUs")
	cmd.Flags().Int32Var(&f.diskMB, "workspace-disk-mb", 0, "a workspace room's disk")
	cmd.Flags().BoolVar(&f.noSize, "no-workspace-size", false, "return to the provider's own size")
	cmd.Flags().StringVar(&f.browser, "browser", "",
		"what runs drive pages in: none, browserless, steel, lightpanda, cdp, browserbase, hyperbrowser, anchor, kernel or brightdata")
	cmd.Flags().StringVar(&f.browserURL, "browser-url", "",
		"a self-hosted browser's address: browserless, steel or lightpanda of your own, or any cdp browser")
	cmd.Flags().BoolVar(&f.browserKeyStdin, "browser-key-stdin", false,
		"read the browser provider's key from stdin; brightdata's is the zone's USER:PASS")
	cmd.Flags().BoolVar(&f.noBrowserKey, "no-browser-key", false, "a self-hosted browser only: reach it with no key")
	cmd.Flags().StringVar(&f.improvement, "improvement", "", "on or off")
	cmd.Flags().StringVar(&f.judgeModel, "judge-model", "",
		"the model that judges and digests runs, as the gateway names it; default is the team's model")
	cmd.Flags().Int32Var(&f.judgeMaxTokens, "judge-max-tokens", 0,
		"the judge model's output limit per call; 4096 when --judge-model is given without it")
	cmd.Flags().Float64Var(&f.judgeTemperature, "judge-temperature", 0, "the judge model's, between 0 and 2")
	cmd.Flags().BoolVar(&f.noJudgeModel, "no-judge-model", false, "judge runs on the team's model again")
	cmd.Flags().Int32Var(&f.sampleShare, "sample-share", 0,
		"the percentage of runs judged done that are learned from anyway, 0 to 100")
	cmd.Flags().Int32Var(&f.dailyPasses, "daily-passes", 0,
		"the most improver passes the team may run in a day (UTC), 1 to 200; 20 until set")
	cmd.Flags().Int32Var(&f.passTokens, "pass-tokens", 0,
		"the tokens an improver pass may spend across its tries, 100000 to 2000000; 400000 until set")
	cmd.Flags().Int32Var(&f.passMinutes, "pass-minutes", 0,
		"the minutes one try at an improver pass may run, 1 to 25; 20 until set")
	cmd.Flags().Int32Var(&f.compaction, "compaction-messages", 0, "messages before a transcript compacts, 10 to 10000")
	cmd.Flags().Int32Var(&f.tokensMin, "compaction-tokens-min", 0,
		"the least a run's next request may read before it compacts, 30000 to 1000000")
	cmd.Flags().Int32Var(&f.tokensMax, "compaction-tokens-max", 0,
		"the most a run's next request may read before it compacts, 30000 to 1000000")
	cmd.Flags().StringVar(&f.hub, "hub", "", "the agent every message reaches first, by name")
	cmd.Flags().BoolVar(&f.noHub, "no-hub", false, "return the team to the built-in orchestrator")
	return cmd
}

func (f *settingsFlags) check(changed func(string) bool) error {
	some := false
	for _, name := range settingFlags {
		some = some || changed(name)
	}
	switch {
	case !some:
		return errors.New("name at least one setting to change; --help lists them")
	case f.noModel && (changed("model") || changed("max-tokens") || changed("temperature")):
		return errors.New("--no-model cannot be combined with --model, --max-tokens or --temperature")
	case f.keyStdin && f.noKey:
		return errors.New("--search-key-stdin and --no-search-key contradict each other")
	case changed("e2b-template") && f.noE2BTemplate:
		return errors.New("--e2b-template and --no-e2b-template contradict each other")
	case changed("e2b-template") && strings.TrimSpace(f.e2bTemplate) == "":
		return errors.New("--e2b-template takes a template's name; --no-e2b-template returns to the default")
	case f.browserKeyStdin && f.noBrowserKey:
		return errors.New("--browser-key-stdin and --no-browser-key contradict each other")
	case countTrue(f.keyStdin, f.sandboxKeyStdin, f.browserKeyStdin) > 1:
		return errors.New("stdin holds one key: set the search, sandbox and browser keys in separate commands")
	case changed("improvement") && f.improvement != "on" && f.improvement != "off":
		return fmt.Errorf("--improvement takes on or off, not %q", f.improvement)
	case f.noJudgeModel && (changed("judge-model") || changed("judge-max-tokens") || changed("judge-temperature")):
		return errors.New("--no-judge-model cannot be combined with --judge-model, --judge-max-tokens or --judge-temperature")
	case changed("judge-model") && strings.TrimSpace(f.judgeModel) == "":
		return errors.New("--judge-model takes a model's name; --no-judge-model returns to the team's model")
	case changed("sample-share") && (f.sampleShare < 0 || f.sampleShare > 100):
		return fmt.Errorf("--sample-share is a percentage, 0 to 100, not %d", f.sampleShare)
	case changed("daily-passes") && (f.dailyPasses < 1 || f.dailyPasses > 200):
		return fmt.Errorf("--daily-passes is a count of passes a day, 1 to 200, not %d", f.dailyPasses)
	case changed("pass-tokens") && (f.passTokens < 100_000 || f.passTokens > 2_000_000):
		return fmt.Errorf("--pass-tokens is a count of tokens, 100000 to 2000000, not %d", f.passTokens)
	case changed("pass-minutes") && (f.passMinutes < 1 || f.passMinutes > 25):
		return fmt.Errorf("--pass-minutes is a count of minutes, 1 to 25, not %d", f.passMinutes)
	case changed("stop-idle-minutes") && f.noIdleMinutes:
		return errors.New("--stop-idle-minutes and --no-stop-idle-minutes contradict each other")
	case slices.ContainsFunc(sizeFlags, changed) && f.noSize:
		return errors.New("--no-workspace-size cannot be combined with a workspace size")
	case slices.ContainsFunc(sizeFlags, changed) && (!changed("workspace-memory-mb") || !changed("workspace-cpus") || !changed("workspace-disk-mb")):
		return errors.New("a workspace size is --workspace-memory-mb, --workspace-cpus and --workspace-disk-mb together")
	case changed("hub") && f.noHub:
		return errors.New("--hub and --no-hub contradict each other")
	case changed("hub") && strings.TrimSpace(f.hub) == "":
		return errors.New("--hub takes an agent's name; --no-hub returns the team to the built-in orchestrator")
	}
	return nil
}

// keys are what was read from stdin, or an empty key meaning none, for each
// part of the settings that takes one; nil keeps the stored key.
type keys struct {
	search, sandbox, browser *string
}

func countTrue(bs ...bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}

// apply starts from what is stored and changes only what was asked. A new
// provider leaves the old URL behind; --search-url, applied after, can give
// it one in the same command, as --sandbox-url and --browser-url do for a new
// sandbox or browser provider.
func (f *settingsFlags) apply(changed func(string) bool, cur *client.TeamSettings, k keys) client.TeamSettingsRequest {
	key := k.search
	req := client.TeamSettingsRequest{
		Model:              cur.Model,
		Improvement:        cur.Improvement,
		CompactionMessages: cur.CompactionMessages,
		Search: client.SearchSettingsRequest{
			Provider: cur.Search.Provider,
			URL:      cur.Search.URL,
			APIKey:   key,
		},
	}
	// The PUT is whole: a hub not sent back is a hub cleared.
	if cur.Hub != nil {
		name := cur.Hub.Name
		req.Hub = &name
	}

	switch {
	case f.noModel:
		req.Model = nil
	case changed("model") || changed("max-tokens") || changed("temperature"):
		var m client.ModelRef
		if cur.Model != nil {
			m = *cur.Model
		}
		if changed("model") {
			m.Name = f.model
		}
		if changed("max-tokens") {
			m.MaxTokens = f.maxTokens
		}
		if changed("temperature") {
			t := f.temperature
			m.Temperature = &t
		}
		req.Model = &m
	}

	if changed("search") && client.SearchProvider(f.search) != cur.Search.Provider {
		req.Search.Provider = client.SearchProvider(f.search)
		req.Search.URL = nil
	}
	if changed("search-url") {
		u := f.searchURL
		req.Search.URL = &u
	}
	if changed("improvement") {
		req.Improvement = f.improvement == "on"
	}
	if changed("compaction-messages") {
		req.CompactionMessages = f.compaction
	}
	// Sent only when given: left out, the brain keeps what is stored.
	if changed("compaction-tokens-min") {
		req.CompactionTokensMin = &f.tokensMin
	}
	if changed("compaction-tokens-max") {
		req.CompactionTokensMax = &f.tokensMax
	}
	switch {
	case f.noHub:
		req.Hub = nil
	case changed("hub"):
		name := strings.TrimSpace(f.hub)
		req.Hub = &name
	}
	// Sent only when given: left out, the brain keeps what is stored.
	if changed("sample-share") {
		req.ImprovementSampleShare = &f.sampleShare
	}
	if changed("daily-passes") {
		req.ImprovementDailyPasses = &f.dailyPasses
	}
	if changed("pass-tokens") {
		req.ImprovementPassTokens = &f.passTokens
	}
	if changed("pass-minutes") {
		req.ImprovementPassMinutes = &f.passMinutes
	}
	req.Sandbox = f.applySandbox(changed, cur.Sandbox, k.sandbox)
	req.Browser = f.applyBrowser(changed, cur.Browser, k.browser)
	return req
}

// body is the request as JSON. The judge model is the one field with three
// states: left out keeps the stored one, null clears it, an object sets it,
// and a nil pointer cannot tell the first two apart, so it is written here.
func (f *settingsFlags) body(changed func(string) bool, cur *client.TeamSettings, req client.TeamSettingsRequest) ([]byte, error) {
	judge, err := f.judge(changed, cur)
	if err != nil {
		return nil, err
	}
	sandbox, err := f.sandboxBody(req.Sandbox)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		client.TeamSettingsRequest
		JudgeModel json.RawMessage `json:"judge_model,omitempty"`
		Sandbox    json.RawMessage `json:"sandbox,omitempty"`
	}{req, judge, sandbox})
}

// sandboxBody is the sandbox as JSON with its nulls written in, or nil to
// leave it out.
func (f *settingsFlags) sandboxBody(s *client.SandboxSettingsRequest) (json.RawMessage, error) {
	if s == nil {
		return nil, nil
	}
	raw, err := json.Marshal(s) //nolint:gosec // the key is sent to the brain, which is what this request is for
	if err != nil {
		return nil, err
	}
	nulls := f.sandboxNulls()
	if len(nulls) == 0 {
		return raw, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for _, k := range nulls {
		m[k] = nil
	}
	return json.Marshal(m)
}

// judge is the judge_model to send, or nil to leave it out.
func (f *settingsFlags) judge(changed func(string) bool, cur *client.TeamSettings) (json.RawMessage, error) {
	switch {
	case f.noJudgeModel:
		return json.RawMessage("null"), nil
	case !changed("judge-model") && !changed("judge-max-tokens") && !changed("judge-temperature"):
		return nil, nil
	}

	m := client.ModelRef{MaxTokens: defaultJudgeMaxTokens}
	switch {
	case cur.JudgeModel != nil:
		m = *cur.JudgeModel
	case !changed("judge-model"):
		return nil, errors.New("no judge model is set — name one with --judge-model, " +
			"which the other judge flags then adjust")
	}
	if changed("judge-model") {
		m.Name = strings.TrimSpace(f.judgeModel)
	}
	if changed("judge-max-tokens") {
		m.MaxTokens = f.judgeMaxTokens
	}
	if changed("judge-temperature") {
		t := f.judgeTemperature
		m.Temperature = &t
	}
	return json.Marshal(m)
}

func (f *settingsFlags) applyBrowser(
	changed func(string) bool, cur client.BrowserSettings, key *string,
) *client.BrowserSettingsRequest {
	if !slices.ContainsFunc(browserFlags, changed) {
		return nil
	}
	b := client.BrowserSettingsRequest{Provider: cur.Provider, URL: cur.URL, APIKey: key}
	if changed("browser") && client.BrowserProvider(f.browser) != cur.Provider {
		b.Provider = client.BrowserProvider(f.browser)
		b.URL = nil
	}
	if changed("browser-url") {
		u := f.browserURL
		b.URL = &u
	}
	return &b
}

func (f *settingsFlags) applySandbox(
	changed func(string) bool, cur client.SandboxSettings, key *string,
) *client.SandboxSettingsRequest {
	if !slices.ContainsFunc(sandboxFlags, changed) {
		return nil
	}
	s := client.SandboxSettingsRequest{Provider: cur.Provider, URL: cur.URL, Template: cur.Template, APIKey: key}
	if changed("sandbox") && client.SandboxProvider(f.sandbox) != cur.Provider {
		s.Provider = client.SandboxProvider(f.sandbox)
		s.URL, s.Template = nil, nil
	}
	if changed("sandbox-url") {
		u := f.sandboxURL
		s.URL = &u
	}
	switch {
	case f.noE2BTemplate:
		s.Template = nil
	case changed("e2b-template"):
		t := strings.TrimSpace(f.e2bTemplate)
		s.Template = &t
	}
	if changed("keep-stopped-hours") {
		s.KeepHours = &f.keepHours
	}
	if changed("stop-idle-minutes") {
		s.IdleMinutes = &f.idleMinutes
	}
	if changed("workspace-memory-mb") {
		s.WorkspaceSize = &client.WorkspaceSize{MemoryMb: f.memoryMB, Cpus: f.cpus, DiskMb: f.diskMB}
	}
	return &s
}

// sandboxNulls are the sandbox fields a request sends as null, which the
// generated type would leave out: null is the provider's, or the
// deployment's, and left out keeps what is stored.
func (f *settingsFlags) sandboxNulls() []string {
	var out []string
	if f.noIdleMinutes {
		out = append(out, "idle_minutes")
	}
	if f.noSize {
		out = append(out, "workspace_size")
	}
	return out
}

func readKey(in io.Reader, what, hint string) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(in, maxKeyBytes+1))
	if err != nil {
		return "", fmt.Errorf("read the %s from stdin: %w", what, err)
	}
	if len(raw) > maxKeyBytes {
		return "", fmt.Errorf("the %s on stdin is over %d bytes — is that the right pipe?", what, maxKeyBytes)
	}

	key := strings.TrimRight(string(raw), "\r\n")
	if strings.TrimSpace(key) == "" {
		return "", errors.New("nothing arrived on stdin — " + hint)
	}
	return key, nil
}

func printSettings(opts *cli.Options, s *client.TeamSettings) error {
	return opts.Out.Print(s, printer.Options{
		Table: func(table *printer.Table) {
			label, value, muted := opts.Styles.Label.Render, opts.Styles.Value.Render, opts.Styles.Muted.Render

			if s.Model == nil {
				table.Row(label("model"), muted("none — the built-in agents do not run"))
			} else {
				m := cli.Safe(s.Model.Name) + " · " + strconv.Itoa(int(s.Model.MaxTokens)) + " tokens"
				if s.Model.Temperature != nil {
					m += " · temperature " + strconv.FormatFloat(*s.Model.Temperature, 'g', -1, 64)
				}
				table.Row(label("model"), value(m))
			}

			table.Row(label("search"), value(string(s.Search.Provider)))
			if s.Search.URL != nil {
				table.Row(label("url"), value(cli.Safe(*s.Search.URL)))
			}
			key := "not set"
			if s.Search.KeySet {
				key = "set"
			}
			table.Row(label("key"), value(key))

			printSandbox(table, opts, s.Sandbox)
			printBrowser(table, opts, s.Browser)

			improvement := "off"
			if s.Improvement {
				improvement = "on"
			}
			table.Row(label("self-improvement"), value(improvement))
			if s.ImprovementSince != nil {
				table.Row(label("improving since"), value(s.ImprovementSince.Format("2006-01-02 15:04")))
			}
			printJudge(table, opts, s)
			table.Row(label("compaction"), value(compactionLine(s)))
			if s.Hub == nil {
				table.Row(label("hub"), muted("orchestrator (built in)"))
			} else {
				table.Row(label("hub"), value(cli.Safe(s.Hub.Name)))
			}
		},
	})
}

// printJudge prints nothing for a brain that predates the judge, which sends
// no share.
func printJudge(table *printer.Table, opts *cli.Options, s *client.TeamSettings) {
	label, value := opts.Styles.Label.Render, opts.Styles.Value.Render

	if s.ImprovementSampleShare == nil {
		return
	}
	if s.JudgeModel == nil {
		table.Row(label("judge model"), opts.Styles.Muted.Render("same as the team's model"))
	} else {
		m := cli.Safe(s.JudgeModel.Name) + " · " + strconv.Itoa(int(s.JudgeModel.MaxTokens)) + " tokens"
		if s.JudgeModel.Temperature != nil {
			m += " · temperature " + strconv.FormatFloat(*s.JudgeModel.Temperature, 'g', -1, 64)
		}
		table.Row(label("judge model"), value(m))
	}
	table.Row(label("sample share"), value(strconv.Itoa(int(*s.ImprovementSampleShare))+"%"))
	if s.ImprovementDailyPasses != nil {
		table.Row(label("daily passes"), value(strconv.Itoa(int(*s.ImprovementDailyPasses))+" at most"))
	}
	if s.ImprovementPassTokens != nil && s.ImprovementPassMinutes != nil {
		table.Row(label("pass budget"), value(strconv.Itoa(int(*s.ImprovementPassTokens))+" tokens a pass, "+
			strconv.Itoa(int(*s.ImprovementPassMinutes))+" minutes a try"))
	}
}

// printSandbox prints nothing for a brain that predates sandboxes, which
// sends none, rather than a blank provider with a key "not set".
func printSandbox(table *printer.Table, opts *cli.Options, s client.SandboxSettings) {
	if s.Provider == "" {
		return
	}
	label, value := opts.Styles.Label.Render, opts.Styles.Value.Render

	table.Row(label("sandbox"), value(string(s.Provider)))
	if s.URL != nil {
		table.Row(label("sandbox url"), value(cli.Safe(*s.URL)))
	}
	if s.Provider == client.SandboxProviderE2B {
		if s.Template != nil {
			table.Row(label("e2b template"), value(cli.Safe(*s.Template)))
		} else {
			table.Row(label("e2b template"), opts.Styles.Muted.Render(defaultE2BTemplate+" (default)"))
		}
	}
	if s.Provider != client.SandboxProviderNone {
		key := "not set"
		if s.KeySet {
			key = "set"
		}
		table.Row(label("sandbox key"), value(key))
		printWorkspaceLife(table, opts, s)
	}
}

// printWorkspaceLife is a workspace's size and when it stops and is cleared.
func printWorkspaceLife(table *printer.Table, opts *cli.Options, s client.SandboxSettings) {
	label, value, muted := opts.Styles.Label.Render, opts.Styles.Value.Render, opts.Styles.Muted.Render
	if z := s.WorkspaceSize; z != nil {
		table.Row(label("workspace size"), value(fmt.Sprintf("%d MB · %d CPU · %d MB disk", z.MemoryMb, z.Cpus, z.DiskMb)))
	} else {
		table.Row(label("workspace size"), muted("the provider's"))
	}
	if s.IdleMinutes != nil {
		table.Row(label("stop when idle"), value(fmt.Sprintf("after %d min", *s.IdleMinutes)))
	} else {
		table.Row(label("stop when idle"), muted("the deployment's"))
	}
	if s.KeepHours != nil {
		table.Row(label("keep stopped"), value(fmt.Sprintf("%d h", *s.KeepHours)))
	}
}

// printBrowser prints nothing for a brain that predates browsers, as
// printSandbox does for sandboxes.
func printBrowser(table *printer.Table, opts *cli.Options, b client.BrowserSettings) {
	if b.Provider == "" {
		return
	}
	label, value := opts.Styles.Label.Render, opts.Styles.Value.Render

	table.Row(label("browser"), value(string(b.Provider)))
	if b.URL != nil {
		table.Row(label("browser url"), value(cli.Safe(*b.URL)))
	}
	if b.Provider != client.BrowserProviderNone {
		key := "not set"
		if b.KeySet {
			key = "set"
		}
		table.Row(label("browser key"), value(key))
	}
}

// compactionLine is when a run compacts, as a person reads it: "100 messages,
// or 50,000 to 60,000 tokens", the token line being a tenth of the run's
// budget held between the two.
func compactionLine(s *client.TeamSettings) string {
	line := strconv.Itoa(int(s.CompactionMessages)) + " messages, or "
	if s.CompactionTokensMin == s.CompactionTokensMax {
		return line + cli.Grouped(int64(s.CompactionTokensMax)) + " tokens"
	}
	return line + cli.Grouped(int64(s.CompactionTokensMin)) + " to " +
		cli.Grouped(int64(s.CompactionTokensMax)) + " tokens, a tenth of the run's budget"
}
