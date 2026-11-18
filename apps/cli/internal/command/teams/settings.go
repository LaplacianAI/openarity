package teams

import (
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

var settingFlags = []string{
	"model", "max-tokens", "temperature", "no-model",
	"search", "search-url", "search-key-stdin", "no-search-key",
	"learning", "compaction-messages", "hub", "no-hub",
	"sandbox", "sandbox-url", "sandbox-key-stdin", "e2b-template", "no-e2b-template",
}

// sandboxFlags are the ones that send a sandbox at all. Without any, the
// request leaves it out and the brain keeps the stored one, key and all.
var sandboxFlags = []string{"sandbox", "sandbox-url", "sandbox-key-stdin", "e2b-template", "no-e2b-template"}

func newSettingsCmd(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "A team's model, web search, sandbox, learning, compaction and hub",
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
	learning    string
	compaction  int32
	hub         string
	noHub       bool

	sandbox         string
	sandboxURL      string
	sandboxKeyStdin bool
	e2bTemplate     string
	noE2BTemplate   bool
}

func newSettingsSetCmd(opts *cli.Options) *cobra.Command {
	var f settingsFlags

	cmd := &cobra.Command{
		Use:   "set <team>",
		Short: "Change a team's settings",
		Long: "Reads the current settings, changes only the flags given, and saves\n" +
			"the whole. A stored search key is kept unless a new one is given, the\n" +
			"provider changes, or a self-hosted provider's URL moves. The sandbox\n" +
			"key follows the same rule.\n\n" +
			"The key comes from stdin and nowhere else:\n\n" +
			"    printf %s \"$BRAVE_KEY\" | oa teams settings set platform \\\n" +
			"        --search brave --search-key-stdin\n\n" +
			"    printf %s \"$E2B_KEY\" | oa teams settings set platform \\\n" +
			"        --sandbox e2b --sandbox-key-stdin\n\n" +
			"There is deliberately no --search-key or --sandbox-key flag. Arguments are readable by\n" +
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
			var key, sandboxKey *string
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

			saved, err := cli.Result(api.SaveTeamSettingsWithResponse(cmd.Context(), team,
				f.apply(changed, current, key, sandboxKey)))
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
	cmd.Flags().StringVar(&f.learning, "learning", "", "on or off")
	cmd.Flags().Int32Var(&f.compaction, "compaction-messages", 0, "messages before a transcript compacts, 10 to 10000")
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
	case f.sandboxKeyStdin && f.keyStdin:
		return errors.New("stdin holds one key: set the search key and the sandbox key in two commands")
	case changed("learning") && f.learning != "on" && f.learning != "off":
		return fmt.Errorf("--learning takes on or off, not %q", f.learning)
	case changed("hub") && f.noHub:
		return errors.New("--hub and --no-hub contradict each other")
	case changed("hub") && strings.TrimSpace(f.hub) == "":
		return errors.New("--hub takes an agent's name; --no-hub returns the team to the built-in orchestrator")
	}
	return nil
}

// apply starts from what is stored and changes only what was asked. A new
// provider leaves the old URL behind; --search-url, applied after, can give
// it one in the same command, as --sandbox-url does for a new sandbox provider.
func (f *settingsFlags) apply(
	changed func(string) bool, cur *client.TeamSettings, key, sandboxKey *string,
) client.TeamSettingsRequest {
	req := client.TeamSettingsRequest{
		Model:              cur.Model,
		Learning:           cur.Learning,
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
	if changed("learning") {
		req.Learning = f.learning == "on"
	}
	if changed("compaction-messages") {
		req.CompactionMessages = f.compaction
	}
	switch {
	case f.noHub:
		req.Hub = nil
	case changed("hub"):
		name := strings.TrimSpace(f.hub)
		req.Hub = &name
	}
	req.Sandbox = f.applySandbox(changed, cur.Sandbox, sandboxKey)
	return req
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
	return &s
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

			learning := "off"
			if s.Learning {
				learning = "on"
			}
			table.Row(label("learning"), value(learning))
			table.Row(label("compaction"), value(strconv.Itoa(int(s.CompactionMessages))+" messages"))
			if s.Hub == nil {
				table.Row(label("hub"), muted("orchestrator (built in)"))
			} else {
				table.Row(label("hub"), value(cli.Safe(s.Hub.Name)))
			}
		},
	})
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
	}
}
