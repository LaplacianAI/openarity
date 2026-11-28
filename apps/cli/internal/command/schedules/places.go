package schedules

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/command/trigger"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

const (
	settingsPermission = "team:settings"

	// The brain's own ranges for a team's settings.
	minIntervalMinutes = 5
	maxIntervalMinutes = 7 * 24 * 60
	maxPerMember       = 100
	maxPerTeam         = 1000
	maxFiresPerHour    = 1000
)

func newPlacesCmd(opts *cli.Options) *cobra.Command {
	var (
		paging cli.Paging
		search string
		kind   string
	)
	cmd := &cobra.Command{
		Use:   "places <team>",
		Short: "List where a schedule or watcher of yours may answer, and its --target",
		Long: "Chats you talked in, channels and groups, and your direct messages with\n" +
			"the bot, each with the --target to pass to create. One you cannot pick\n" +
			"is listed with why. A platform that could not be read says so on stderr.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			params := &client.ListScheduleTargetsParams{}
			params.Limit, params.Cursor = paging.Values()
			if search != "" {
				params.Q = &search
			}
			switch kind {
			case "":
			case "chat", "channel", "direct":
				k := client.ListScheduleTargetsParamsKind(kind)
				params.Kind = &k
			default:
				return fmt.Errorf("--kind is chat, channel or direct, not %q", kind)
			}
			page, err := cli.Result(api.ListScheduleTargetsWithResponse(cmd.Context(), team, params))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			for _, p := range page.Problems {
				_, _ = fmt.Fprintf(opts.Stderr, "%s: %s\n", cli.Safe(p.Provider), cli.Line(p.Message))
			}
			return cli.PrintPage(opts, cli.Page[client.SchedulePlace]{
				Items:      page.Items,
				NextCursor: page.NextCursor,
				Empty:      "nowhere but a conversation of its own (--target own)",
				More:       "oa schedules places " + args[0],
				Row: func(table *printer.Table, p client.SchedulePlace) {
					pick := opts.Styles.Value.Render(trigger.PickSays(p.Target))
					if !p.Available {
						pick = opts.Styles.Muted.Render(reasonSays(p.Reason))
					}
					table.Row(cli.Safe(string(p.Kind)), cli.Safe(p.Label), cli.Line(p.Detail),
						cli.Safe(p.Channel.Name)+" ("+cli.Safe(p.Channel.Provider)+")", pick)
				},
			})
		},
	}
	paging.Flags(cmd)
	cmd.Flags().StringVar(&search, "search", "", "only places whose name has this in it")
	cmd.Flags().StringVar(&kind, "kind", "", "only chat, channel or direct")
	return cmd
}

func reasonSays(r *client.SchedulePlaceReason) string {
	if r == nil {
		return "not available"
	}
	switch *r {
	case client.NeedsChannelWrite:
		return "needs channel:write"
	case client.NotInChannel:
		return "the app is not in it"
	}
	return cli.Safe(string(*r))
}

func newSettingsCmd(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "See or change a team's limits on schedules and watchers",
	}
	cmd.AddCommand(newSettingsShowCmd(opts), newSettingsSetCmd(opts))
	return cmd
}

func newSettingsShowCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <team>",
		Short: "Show a team's limits on schedules, and whether agents may propose them",
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
			s, err := cli.Result(api.GetScheduleSettingsWithResponse(cmd.Context(), team))
			if err != nil {
				return err
			}
			return printSettings(opts, s, "")
		},
	}
}

func newSettingsSetCmd(opts *cli.Options) *cobra.Command {
	var (
		agents, noAgents                 bool
		zone                             string
		interval                         string
		perMember, perTeam, firesPerHour int32
	)
	cmd := &cobra.Command{
		Use:   "set <team>",
		Short: "Change a team's limits on schedules; what is not given stays",
		Long: "Takes " + settingsPermission + ". Schedules already made keep their rules; the\n" +
			"limits hold from their next fire or change. The settings are read, changed\n" +
			"and saved whole, so a change someone else saves in between is lost.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := cmd.Flags()
			if !f.Changed("agents-may-schedule") && !f.Changed("no-agents-may-schedule") && !f.Changed("tz") &&
				!f.Changed("min-interval") && !f.Changed("per-member") && !f.Changed("per-team") && !f.Changed("fires-per-hour") {
				return errors.New("say what to change: --agents-may-schedule, --no-agents-may-schedule, --tz, " +
					"--min-interval, --per-member, --per-team or --fires-per-hour")
			}
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			// A PUT of the whole: what was not given is kept as it is.
			s, err := cli.Result(api.GetScheduleSettingsWithResponse(cmd.Context(), team))
			if err != nil {
				return err
			}
			body := *s
			// What was given, not its value: --agents-may-schedule=false is given
			// and means no.
			switch {
			case f.Changed("agents-may-schedule"):
				body.AgentsMaySchedule = agents
			case f.Changed("no-agents-may-schedule"):
				body.AgentsMaySchedule = !noAgents
			}
			if f.Changed("tz") {
				body.Timezone = zone
			}
			if f.Changed("min-interval") {
				sec, err := trigger.Length("min-interval", interval)
				if err != nil {
					return err
				}
				minutes := sec / 60
				if minutes < minIntervalMinutes || minutes > maxIntervalMinutes {
					return fmt.Errorf("--min-interval is between %s and %s, not %q",
						trigger.LengthSays(minIntervalMinutes*60), trigger.LengthSays(maxIntervalMinutes*60), interval)
				}
				body.ScheduleMinIntervalMinutes = int32(minutes)
			}
			for _, l := range []struct {
				name       string
				given, max int32
				to         *int32
			}{
				{"per-member", perMember, maxPerMember, &body.SchedulesPerMember},
				{"per-team", perTeam, maxPerTeam, &body.SchedulesPerTeam},
				{"fires-per-hour", firesPerHour, maxFiresPerHour, &body.FiresPerHour},
			} {
				if !f.Changed(l.name) {
					continue
				}
				if l.given < 1 || l.given > l.max {
					return fmt.Errorf("--%s is between 1 and %d, not %d", l.name, l.max, l.given)
				}
				*l.to = l.given
			}
			if body.SchedulesPerTeam < body.SchedulesPerMember {
				return fmt.Errorf("a team may have %d, fewer than the %d one person may: "+
					"raise --per-team or lower --per-member", body.SchedulesPerTeam, body.SchedulesPerMember)
			}
			saved, err := cli.Result(api.SaveScheduleSettingsWithResponse(cmd.Context(), team, body))
			if err != nil {
				return cli.Needs(err, settingsPermission)
			}
			return printSettings(opts, saved, "saved")
		},
	}
	cmd.Flags().BoolVar(&agents, "agents-may-schedule", false, "let agents propose schedules and watchers from a chat")
	cmd.Flags().BoolVar(&noAgents, "no-agents-may-schedule", false, "stop agents proposing them")
	cmd.MarkFlagsMutuallyExclusive("agents-may-schedule", "no-agents-may-schedule")
	cmd.Flags().StringVar(&zone, "tz", "", "the team's IANA zone, for a schedule made without one")
	cmd.Flags().StringVar(&interval, "min-interval", "", "the shortest a schedule may repeat, or a watcher look: 5m to 7d")
	cmd.Flags().Int32Var(&perMember, "per-member", 0, "how many schedules and watchers one person may have: 1 to 100")
	cmd.Flags().Int32Var(&perTeam, "per-team", 0, "how many the team may have: 1 to 1000, and no fewer than --per-member")
	cmd.Flags().Int32Var(&firesPerHour, "fires-per-hour", 0, "how many runs they may start in an hour, across the team: 1 to 1000")
	return cmd
}

func printSettings(opts *cli.Options, s *client.ScheduleSettings, did string) error {
	return opts.Out.Print(s, printer.Options{
		Table: func(table *printer.Table) {
			if did != "" {
				table.Row(opts.Styles.OK.Render(did), "", "")
			}
			agents := "no"
			if s.AgentsMaySchedule {
				agents = "yes"
			}
			table.Row("agents may schedule", agents, "propose from a chat, for the person to confirm")
			table.Row("timezone", cli.Safe(s.Timezone), "for a schedule made without one")
			table.Row("min interval", trigger.LengthSays(int64(s.ScheduleMinIntervalMinutes)*60), "")
			table.Row("per member", strconv.Itoa(int(s.SchedulesPerMember)), "")
			table.Row("per team", strconv.Itoa(int(s.SchedulesPerTeam)), "")
			table.Row("fires per hour", strconv.Itoa(int(s.FiresPerHour)), "across the team")
		},
	})
}
