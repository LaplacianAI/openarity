// Package watchers is oa watchers: runs when a web page or a feed changes, or
// when a webhook is called.
package watchers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/command/schedules"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

const (
	readPermission   = "schedule:read"
	writePermission  = "schedule:write"
	managePermission = "schedule:manage"

	webhook = "webhook"
)

func New(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watchers",
		Short: "Run an agent when a page or a feed changes, or a webhook is called",
		Long: "A watcher looks at a web page (or part of it, by a CSS selector) or a\n" +
			"feed every so often, and runs an agent with an objective when it changes;\n" +
			"a webhook watcher runs when its URL is called with a signed request. It\n" +
			"answers where a schedule would: `oa schedules places` lists where.",
	}
	cmd.AddCommand(
		newListCmd(opts),
		newGetCmd(opts),
		newCreateCmd(opts),
		newUpdateCmd(opts),
		newStatusCmd(opts, "pause", "Stop a watcher looking until it is resumed", "paused"),
		newStatusCmd(opts, "resume", "Start a paused watcher looking again", "active"),
		newDeleteCmd(opts),
		newRunCmd(opts),
		newChecksCmd(opts),
		newConfirmCmd(opts),
		newTakeOverCmd(opts),
		newSecretCmd(opts),
	)
	return cmd
}

func one(ctx context.Context, opts *cli.Options, args []string, everyone bool) (*client.ClientWithResponses, uuid.UUID, uuid.UUID, error) {
	api, err := opts.API(ctx)
	if err != nil {
		return nil, uuid.Nil, uuid.Nil, err
	}
	team, err := cli.ResolveTeam(ctx, api, args[0])
	if err != nil {
		return nil, uuid.Nil, uuid.Nil, err
	}
	id, err := cli.ResolveWatcher(ctx, api, team, args[1], everyone)
	if err != nil {
		return nil, uuid.Nil, uuid.Nil, err
	}
	return api, team, id, nil
}

func newListCmd(opts *cli.Options) *cobra.Command {
	var (
		paging   cli.Paging
		statuses []string
		all      bool
	)
	cmd := &cobra.Command{
		Use:     "list <team>",
		Aliases: []string{"ls"},
		Short:   "List your watchers, or everyone's with --all",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			params := &client.ListWatchersParams{All: &all}
			params.Limit, params.Cursor = paging.Values()
			if len(statuses) > 0 {
				s := make([]client.ListWatchersParamsStatus, 0, len(statuses))
				for _, v := range statuses {
					s = append(s, client.ListWatchersParamsStatus(v))
				}
				params.Status = &s
			}
			page, err := cli.Result(api.ListWatchersWithResponse(cmd.Context(), team, params))
			if err != nil {
				if all {
					return cli.Needs(err, managePermission)
				}
				return cli.Needs(err, readPermission)
			}
			more := "oa watchers list " + args[0]
			if all {
				more += " --all"
			}
			return cli.PrintPage(opts, cli.Page[client.Watcher]{
				Items:      page.Items,
				NextCursor: page.NextCursor,
				Empty:      "no watchers",
				More:       more,
				Row: func(table *printer.Table, w client.Watcher) {
					last := "-"
					if w.LastOutcome != nil {
						last = cli.Safe(*w.LastOutcome)
					}
					where := schedules.WhereSays(w.Target)
					if w.WaitingChange != nil {
						where += opts.Styles.Muted.Render(" · waits: " + cli.Safe(w.WaitingChange.Says))
					}
					table.Row(opts.Styles.Value.Render(cli.Safe(w.Name)), cli.Safe(w.Status), watchesSays(w), last, where)
				},
			})
		},
	}
	paging.Flags(cmd)
	cmd.Flags().StringSliceVar(&statuses, "status", nil, "only these: proposed, waiting_secret, active, paused, deleted")
	cmd.Flags().BoolVar(&all, "all", false, "everyone's, which takes "+managePermission)
	return cmd
}

// watchesSays is what a watcher looks at, and how often.
func watchesSays(w client.Watcher) string {
	if w.Source == webhook {
		return "its URL is called"
	}
	every := ""
	if w.EverySeconds != nil {
		every = ", every " + schedules.LengthSays(int64(*w.EverySeconds))
	}
	what := "the page"
	if w.Source == "feed" {
		what = "the feed"
	}
	if w.URL != nil {
		what += " " + cli.Safe(*w.URL)
	}
	return what + every
}

func newGetCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "get <team> <watcher>",
		Short: "Show a watcher: what it watches, what it does, and where it answers",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, id, err := one(cmd.Context(), opts, args, false)
			if err != nil {
				return err
			}
			w, err := cli.Result(api.GetWatcherWithResponse(cmd.Context(), team, id))
			if err != nil {
				return cli.Needs(err, readPermission)
			}
			return printWatcher(opts, w, "")
		},
	}
}

func newCreateCmd(opts *cli.Options) *cobra.Command {
	var (
		name, source, url, selector, every, cooldown, target string
		objective                                            schedules.ObjectiveFlags
		agent                                                schedules.AgentFlags
	)
	cmd := &cobra.Command{
		Use:   "create <team>",
		Short: "Make a watcher; a webhook one prints its URL and secret, once",
		Example: "  oa watchers create acme --name prices --objective 'Tell me what moved.' \\\n" +
			"    --source web_page --url https://example.com/prices --selector '#table' --every 2h",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text, ok, err := objective.Read(cmd.InOrStdin())
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("say what each run is to do, with --objective or --objective-file")
			}
			if strings.TrimSpace(name) == "" {
				return errors.New("name it with --name")
			}
			body := client.CreateWatcherRequest{
				Name: strings.TrimSpace(name), Objective: text,
				Source: client.CreateWatcherRequestSource(source),
			}
			switch source {
			case "web_page", "feed":
				if url == "" {
					return fmt.Errorf("a %s watcher needs --url", source)
				}
				body.URL = &url
			case webhook:
				if url != "" || selector != "" || every != "" {
					return errors.New("a webhook watcher has no --url, --selector or --every: it runs when its own URL is called")
				}
			default:
				return fmt.Errorf("--source is web_page, feed or webhook, not %q", source)
			}
			if selector != "" {
				if source != "web_page" {
					return errors.New("--selector is for a web_page watcher")
				}
				body.Selector = &selector
			}
			if body.EverySeconds, err = optLength("every", every); err != nil {
				return err
			}
			if body.CooldownSeconds, err = optLength("cooldown", cooldown); err != nil {
				return err
			}
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			if body.AgentID, err = agent.ID(cmd.Context(), api, team); err != nil {
				return err
			}
			if body.Target, err = schedules.Target(cmd.Context(), api, team, target); err != nil {
				return err
			}
			w, err := cli.Created(api.CreateWatcherWithResponse(cmd.Context(), team, body))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			if w.Secret != nil {
				opts.Out.Note("its secret is shown this once; `oa watchers secret` makes a new one")
			}
			return printWatcher(opts, w, "created")
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "a short name, unique among your own")
	schedules.BindObjective(cmd, &objective)
	cmd.Flags().StringVar(&source, "source", "", "web_page, feed or webhook")
	cmd.Flags().StringVar(&url, "url", "", "the page or feed it looks at")
	cmd.Flags().StringVar(&selector, "selector", "", "a CSS selector: only what it matches on the page is compared")
	cmd.Flags().StringVar(&every, "every", "", "how often it looks, such as 1h; the team's minimum unless given")
	cmd.Flags().StringVar(&cooldown, "cooldown", "", "how long after a run a change waits, such as 1h")
	schedules.BindAgent(cmd, &agent)
	cmd.Flags().StringVar(&target, "target", "",
		"where it answers: own (the default), chat:<session>, channel:<channel>:<ref> or direct:<channel>")
	return cmd
}

func optLength(flag, v string) (*int64, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	sec, err := schedules.Length(flag, v)
	if err != nil {
		return nil, err
	}
	return &sec, nil
}

func newUpdateCmd(opts *cli.Options) *cobra.Command {
	var (
		name, selector, every, cooldown string
		objective                       schedules.ObjectiveFlags
		agent                           schedules.AgentFlags
	)
	cmd := &cobra.Command{
		Use:   "update <team> <watcher>",
		Short: "Change what a watcher does or how often it looks; what is not given stays",
		Long:  "What it watches and where it answers cannot change: delete it and make another.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, id, err := one(cmd.Context(), opts, args, false)
			if err != nil {
				return err
			}
			var body client.UpdateWatcherRequest
			if cmd.Flags().Changed("name") {
				body.Name = &name
			}
			if text, ok, err := objective.Read(cmd.InOrStdin()); err != nil {
				return err
			} else if ok {
				body.Objective = &text
			}
			// Empty clears it, so the whole page is compared again.
			if cmd.Flags().Changed("selector") {
				body.Selector = &selector
			}
			if body.EverySeconds, err = optLength("every", every); err != nil {
				return err
			}
			if body.CooldownSeconds, err = optLength("cooldown", cooldown); err != nil {
				return err
			}
			if agent.Hub {
				hub := true
				body.Hub = &hub
			} else if body.AgentID, err = agent.ID(cmd.Context(), api, team); err != nil {
				return err
			}
			if body == (client.UpdateWatcherRequest{}) {
				return errors.New("say what to change: --name, --objective, --selector, --every, --cooldown, --agent or --hub")
			}
			w, err := cli.Result(api.UpdateWatcherWithResponse(cmd.Context(), team, id, body))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			return printWatcher(opts, w, "updated")
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "a new name")
	schedules.BindObjective(cmd, &objective)
	cmd.Flags().StringVar(&selector, "selector", "", "a new CSS selector; empty compares the whole page")
	cmd.Flags().StringVar(&every, "every", "", "how often it looks")
	cmd.Flags().StringVar(&cooldown, "cooldown", "", "how long after a run a change waits")
	schedules.BindAgent(cmd, &agent)
	return cmd
}

func newStatusCmd(opts *cli.Options, verb, short, status string) *cobra.Command {
	return &cobra.Command{
		Use:   verb + " <team> <watcher>",
		Short: short,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, id, err := one(cmd.Context(), opts, args, false)
			if err != nil {
				return err
			}
			w, err := cli.Result(api.UpdateWatcherWithResponse(cmd.Context(), team, id,
				client.UpdateWatcherRequest{Status: &status}))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			return printWatcher(opts, w, verb+"d")
		},
	}
}

func newDeleteCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <team> <watcher>",
		Aliases: []string{"rm"},
		Short:   "Delete a watcher; a webhook one's URL stops answering",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, id, err := one(cmd.Context(), opts, args, false)
			if err != nil {
				return err
			}
			if err := cli.NoContent(api.DeleteWatcherWithResponse(cmd.Context(), team, id)); err != nil {
				return cli.Needs(err, writePermission)
			}
			return opts.Out.Print(map[string]string{"id": id.String(), "status": "deleted"}, printer.Options{
				Table: func(table *printer.Table) {
					table.Row(opts.Styles.OK.Render("deleted"), opts.Styles.Value.Render(cli.Safe(args[1])),
						opts.Styles.Muted.Render(id.String()))
				},
			})
		},
	}
}

func newRunCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "run <team> <watcher>",
		Short: "Have a page or feed watcher look now; it runs only if something changed",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, id, err := one(cmd.Context(), opts, args, false)
			if err != nil {
				return err
			}
			w, err := cli.Result(api.CheckWatcherNowWithResponse(cmd.Context(), team, id))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			opts.Out.Note("`oa watchers checks " + args[0] + " " + args[1] + "` shows what it found")
			return printWatcher(opts, w, "looking")
		},
	}
}

func newChecksCmd(opts *cli.Options) *cobra.Command {
	var paging cli.Paging
	cmd := &cobra.Command{
		Use:   "checks <team> <watcher>",
		Short: "List what each look or delivery found, newest first",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, id, err := one(cmd.Context(), opts, args, false)
			if err != nil {
				return err
			}
			limit, cursor := paging.Values()
			page, err := cli.Result(api.ListWatcherChecksWithResponse(cmd.Context(), team, id,
				&client.ListWatcherChecksParams{Limit: limit, Cursor: cursor}))
			if err != nil {
				return cli.Needs(err, readPermission)
			}
			return cli.PrintPage(opts, cli.Page[client.WatcherCheck]{
				Items:      page.Items,
				NextCursor: page.NextCursor,
				Empty:      "it has not looked yet",
				More:       "oa watchers checks " + args[0] + " " + args[1],
				Row: func(table *printer.Table, c client.WatcherCheck) {
					detail, run := "", ""
					if c.Detail != nil {
						detail = cli.Line(*c.Detail)
					}
					if c.RunID != nil {
						run = c.RunID.String()
					}
					table.Row(c.CheckedAt.UTC().Format("Mon 2 Jan 15:04 UTC"), cli.Safe(c.Outcome), detail, opts.Styles.Muted.Render(run))
				},
			})
		},
	}
	paging.Flags(cmd)
	return cmd
}

func newConfirmCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "confirm <team> <watcher>",
		Short: "Confirm what an agent proposed or asked of one of your watchers",
		Long: "An agent's proposal starts, and what it asked of one that runs is done,\n" +
			"only once you confirm it. In the chat you reply with its code; here you\n" +
			"need none. A webhook one is confirmed by making its secret: `secret`.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, id, err := one(cmd.Context(), opts, args, false)
			if err != nil {
				return err
			}
			w, err := cli.Result(api.ConfirmWatcherWithResponse(cmd.Context(), team, id))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			return printWatcher(opts, w, "confirmed")
		},
	}
}

func newTakeOverCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "take-over <team> <watcher>",
		Short: "Take a paused watcher whose maker left or may no longer run it",
		Long: "It becomes yours and stays paused until you resume it; a webhook one\n" +
			"gets a new secret, shown once. Takes " + managePermission + ".",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, id, err := one(cmd.Context(), opts, args, true)
			if err != nil {
				return cli.Needs(err, managePermission)
			}
			w, err := cli.Result(api.TakeOverWatcherWithResponse(cmd.Context(), team, id))
			if err != nil {
				return cli.Needs(err, managePermission)
			}
			if w.Secret != nil {
				opts.Out.Note("its new secret is shown this once")
			}
			return printWatcher(opts, w, "taken over")
		},
	}
}

func newSecretCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "secret <team> <watcher>",
		Short: "Make a webhook watcher a new secret; prints its URL and secret only",
		Long: "The old secret stops working at once. One an agent proposed starts with\n" +
			"its first secret. The secret is printed once, to stdout, and nowhere else:\n" +
			"  oa watchers secret acme deploys > secret.txt",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, id, err := one(cmd.Context(), opts, args, false)
			if err != nil {
				return err
			}
			s, err := cli.Created(api.RollWatcherSecretWithResponse(cmd.Context(), team, id))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			_, _ = fmt.Fprintln(opts.Stderr, "a new secret; the old one no longer works")
			return opts.Out.Print(s, printer.Options{
				Table: func(table *printer.Table) {
					if s.URL != nil {
						table.Row("url", *s.URL)
					}
					table.Row("secret", s.Secret)
				},
			})
		},
	}
}

func printWatcher(opts *cli.Options, w *client.Watcher, did string) error {
	return opts.Out.Print(w, printer.Options{
		Table: func(table *printer.Table) {
			head := "watcher"
			if did != "" {
				head = opts.Styles.OK.Render(did)
			}
			table.Row(head, opts.Styles.Value.Render(cli.Safe(w.Name)), opts.Styles.Muted.Render(w.ID.String()))
			why := ""
			if w.PausedReason != nil {
				why = cli.Safe(*w.PausedReason)
			}
			table.Row("status", cli.Safe(w.Status), why)
			table.Row("watches", watchesSays(*w), "")
			if w.Selector != nil {
				table.Row("selector", cli.Safe(*w.Selector), "")
			}
			table.Row("cooldown", schedules.LengthSays(int64(w.CooldownSeconds)), "after a run, before a change runs it again")
			for i, line := range cli.Lines(w.Objective) {
				label := ""
				if i == 0 {
					label = "objective"
				}
				table.Row(label, line, "")
			}
			switch {
			case w.Hub:
				table.Row("agent", "the hub", "")
			case w.AgentID != nil:
				table.Row("agent", w.AgentID.String(), "")
			}
			table.Row("answers", schedules.WhereSays(w.Target), "")
			if w.LastOutcome != nil {
				table.Row("last", cli.Safe(*w.LastOutcome), "")
			}
			if w.WaitingChange != nil {
				table.Row("waits", "an agent asked "+cli.Safe(w.WaitingChange.Says), "")
			}
			if w.HookURL != nil {
				table.Row("url", *w.HookURL, "")
			}
			if w.Secret != nil {
				table.Row("secret", *w.Secret, "shown this once")
			}
		},
	})
}
