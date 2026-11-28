// Package schedules is oa schedules: runs at a time or on a rule, answered in
// their own conversation, a chat, a channel or the maker's direct messages.
package schedules

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

const (
	readPermission   = "schedule:read"
	writePermission  = "schedule:write"
	managePermission = "schedule:manage"
)

func New(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedules",
		Short: "Run an agent at a time or on a rule",
		Long: "A schedule runs an agent with an objective at a time, every so often or\n" +
			"on a cron rule, as the person who made it. It answers in a conversation\n" +
			"of its own, a chat, a channel or its maker's direct messages; `places`\n" +
			"lists where. One an agent proposed in a chat waits for `confirm`.",
	}
	cmd.AddCommand(
		newListCmd(opts),
		newGetCmd(opts),
		newCreateCmd(opts),
		newUpdateCmd(opts),
		newStatusCmd(opts, "pause", "Stop a schedule firing until it is resumed", client.UpdateScheduleRequestStatusPaused),
		newStatusCmd(opts, "resume", "Start a paused schedule firing again", client.UpdateScheduleRequestStatusActive),
		newDeleteCmd(opts),
		newRunCmd(opts),
		newFiresCmd(opts),
		newConfirmCmd(opts),
		newTakeOverCmd(opts),
		newPlacesCmd(opts),
		newSettingsCmd(opts),
	)
	return cmd
}

// one is the team and the schedule a command names.
func one(ctx context.Context, opts *cli.Options, args []string, everyone bool) (*client.ClientWithResponses, uuid.UUID, uuid.UUID, error) {
	api, err := opts.API(ctx)
	if err != nil {
		return nil, uuid.Nil, uuid.Nil, err
	}
	team, err := cli.ResolveTeam(ctx, api, args[0])
	if err != nil {
		return nil, uuid.Nil, uuid.Nil, err
	}
	id, err := cli.ResolveSchedule(ctx, api, team, args[1], everyone)
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
		Short:   "List your schedules, or everyone's with --all",
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
			params := &client.ListSchedulesParams{All: &all}
			params.Limit, params.Cursor = paging.Values()
			if len(statuses) > 0 {
				s := make([]client.ListSchedulesParamsStatus, 0, len(statuses))
				for _, v := range statuses {
					s = append(s, client.ListSchedulesParamsStatus(v))
				}
				params.Status = &s
			}
			page, err := cli.Result(api.ListSchedulesWithResponse(cmd.Context(), team, params))
			if err != nil {
				if all {
					return cli.Needs(err, managePermission)
				}
				return cli.Needs(err, readPermission)
			}
			return cli.PrintPage(opts, cli.Page[client.Schedule]{
				Items:      page.Items,
				NextCursor: page.NextCursor,
				Empty:      "no schedules",
				More:       "oa schedules list " + args[0] + allFlag(all),
				Row: func(table *printer.Table, s client.Schedule) {
					last := "-"
					if s.LastOutcome != nil {
						last = cli.Safe(*s.LastOutcome)
					}
					row := []string{
						opts.Styles.Value.Render(cli.Safe(s.Name)), string(s.Status), whenSays(s.When, s.Timezone),
						nextSays(s), last, WhereSays(s.Target),
					}
					if s.WaitingChange != nil {
						row[len(row)-1] += opts.Styles.Muted.Render(" · waits: " + cli.Safe(s.WaitingChange.Says))
					}
					table.Row(row...)
				},
			})
		},
	}
	paging.Flags(cmd)
	cmd.Flags().StringSliceVar(&statuses, "status", nil, "only these: proposed, active, paused, done, deleted")
	cmd.Flags().BoolVar(&all, "all", false, "everyone's, which takes "+managePermission)
	return cmd
}

func allFlag(all bool) string {
	if all {
		return " --all"
	}
	return ""
}

func newGetCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "get <team> <schedule>",
		Short: "Show a schedule: what it does, when, and where it answers",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, id, err := one(cmd.Context(), opts, args, false)
			if err != nil {
				return err
			}
			s, err := cli.Result(api.GetScheduleWithResponse(cmd.Context(), team, id))
			if err != nil {
				return cli.Needs(err, readPermission)
			}
			return printSchedule(opts, s, "")
		},
	}
}

func newCreateCmd(opts *cli.Options) *cobra.Command {
	var (
		name      string
		objective ObjectiveFlags
		when      WhenFlags
		zone      string
		agent     AgentFlags
		target    string
	)
	cmd := &cobra.Command{
		Use:   "create <team>",
		Short: "Make a schedule; it prints its next three times",
		Example: "  oa schedules create acme --name standup --objective-file brief.md \\\n" +
			"    --cron '0 9 * * 1-5' --tz Asia/Kolkata --target channel:slack:C0123ABCD",
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
			if when.given() == 0 {
				return errors.New("say when, with one of --at, --every or --cron")
			}
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			w, err := when.When(zoneOr(cmd.Context(), api, team, zone))
			if err != nil {
				return err
			}
			body := client.CreateScheduleRequest{Name: strings.TrimSpace(name), Objective: text, When: *w}
			if zone != "" {
				body.Timezone = &zone
			}
			if body.AgentID, err = agent.ID(cmd.Context(), api, team); err != nil {
				return err
			}
			if body.Target, err = Target(cmd.Context(), api, team, target); err != nil {
				return err
			}
			s, err := cli.Created(api.CreateScheduleWithResponse(cmd.Context(), team, body))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			return printSchedule(opts, s, "created")
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "a short name, unique among your own")
	objective.bind(cmd)
	when.bind(cmd)
	cmd.Flags().StringVar(&zone, "tz", "", "the IANA zone its times are read in; the team's unless given")
	agent.bind(cmd)
	cmd.Flags().StringVar(&target, "target", "",
		"where it answers: own (the default), chat:<session>, channel:<channel>:<ref> or direct:<channel>")
	return cmd
}

// zoneOr is what a wall time is read in: the zone given, or the team's.
func zoneOr(ctx context.Context, api *client.ClientWithResponses, team uuid.UUID, zone string) func() (string, error) {
	return func() (string, error) {
		if zone != "" {
			return zone, nil
		}
		return TeamZone(ctx, api, team)
	}
}

func newUpdateCmd(opts *cli.Options) *cobra.Command {
	var (
		name      string
		objective ObjectiveFlags
		when      WhenFlags
		zone      string
		agent     AgentFlags
	)
	cmd := &cobra.Command{
		Use:   "update <team> <schedule>",
		Short: "Change what a schedule does or when; what is not given stays",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, id, err := one(cmd.Context(), opts, args, false)
			if err != nil {
				return err
			}
			var body client.UpdateScheduleRequest
			if cmd.Flags().Changed("name") {
				body.Name = &name
			}
			if text, ok, err := objective.Read(cmd.InOrStdin()); err != nil {
				return err
			} else if ok {
				body.Objective = &text
			}
			if zone != "" {
				body.Timezone = &zone
			}
			// A wall time is read in the zone given, or the schedule's own.
			if body.When, err = when.When(func() (string, error) {
				if zone != "" {
					return zone, nil
				}
				s, err := cli.Result(api.GetScheduleWithResponse(cmd.Context(), team, id))
				if err != nil {
					return "", err
				}
				return s.Timezone, nil
			}); err != nil {
				return err
			}
			if agent.Hub {
				hub := true
				body.Hub = &hub
			} else if body.AgentID, err = agent.ID(cmd.Context(), api, team); err != nil {
				return err
			}
			if body == (client.UpdateScheduleRequest{}) {
				return errors.New("say what to change: --name, --objective, --at, --every, --cron, --tz, --agent or --hub")
			}
			s, err := cli.Result(api.UpdateScheduleWithResponse(cmd.Context(), team, id, body))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			return printSchedule(opts, s, "updated")
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "a new name")
	objective.bind(cmd)
	when.bind(cmd)
	cmd.Flags().StringVar(&zone, "tz", "", "a new zone")
	agent.bind(cmd)
	return cmd
}

func newStatusCmd(opts *cli.Options, verb, short string, status client.UpdateScheduleRequestStatus) *cobra.Command {
	return &cobra.Command{
		Use:   verb + " <team> <schedule>",
		Short: short,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, id, err := one(cmd.Context(), opts, args, false)
			if err != nil {
				return err
			}
			s, err := cli.Result(api.UpdateScheduleWithResponse(cmd.Context(), team, id,
				client.UpdateScheduleRequest{Status: &status}))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			return printSchedule(opts, s, verb+"d")
		},
	}
}

func newDeleteCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <team> <schedule>",
		Aliases: []string{"rm"},
		Short:   "Delete a schedule; its conversation and runs are kept",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, id, err := one(cmd.Context(), opts, args, false)
			if err != nil {
				return err
			}
			if err := cli.NoContent(api.DeleteScheduleWithResponse(cmd.Context(), team, id)); err != nil {
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
		Use:   "run <team> <schedule>",
		Short: "Run a schedule now, once, without moving its next time",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, id, err := one(cmd.Context(), opts, args, false)
			if err != nil {
				return err
			}
			run, err := cli.Accepted(api.RunScheduleWithResponse(cmd.Context(), team, id))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			opts.Out.Note("follow it with `oa runs get " + args[0] + " " + run.RunID.String() + "`")
			return opts.Out.Print(run, printer.Options{
				Table: func(table *printer.Table) {
					table.Row(opts.Styles.OK.Render("started"), opts.Styles.Value.Render(cli.Safe(args[1])),
						opts.Styles.Muted.Render(run.RunID.String()))
				},
			})
		},
	}
}

func newFiresCmd(opts *cli.Options) *cobra.Command {
	var paging cli.Paging
	cmd := &cobra.Command{
		Use:   "fires <team> <schedule>",
		Short: "List what each time a schedule fired did, newest first",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, id, err := one(cmd.Context(), opts, args, false)
			if err != nil {
				return err
			}
			limit, cursor := paging.Values()
			page, err := cli.Result(api.ListScheduleFiresWithResponse(cmd.Context(), team, id,
				&client.ListScheduleFiresParams{Limit: limit, Cursor: cursor}))
			if err != nil {
				return cli.Needs(err, readPermission)
			}
			return cli.PrintPage(opts, cli.Page[client.ScheduleFire]{
				Items:      page.Items,
				NextCursor: page.NextCursor,
				Empty:      "it has not fired",
				More:       "oa schedules fires " + args[0] + " " + args[1],
				Row: func(table *printer.Table, f client.ScheduleFire) {
					run := ""
					if f.RunID != nil {
						run = f.RunID.String()
					}
					table.Row(f.ScheduledFor.UTC().Format(shownTime+" UTC"), string(f.Outcome), opts.Styles.Muted.Render(run))
				},
			})
		},
	}
	paging.Flags(cmd)
	return cmd
}

func newConfirmCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "confirm <team> <schedule>",
		Short: "Confirm what an agent proposed or asked of one of your schedules",
		Long: "An agent's proposal starts, and what it asked of one that runs (a change,\n" +
			"a delete, a resume or a run now) is done, only once you confirm it. In\n" +
			"the chat you reply with its code; here you need none.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, id, err := one(cmd.Context(), opts, args, false)
			if err != nil {
				return err
			}
			s, err := cli.Result(api.ConfirmScheduleWithResponse(cmd.Context(), team, id))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			return printSchedule(opts, s, "confirmed")
		},
	}
}

func newTakeOverCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "take-over <team> <schedule>",
		Short: "Take a paused schedule whose maker left or may no longer run it",
		Long: "It becomes yours and stays paused until you resume it. Takes\n" +
			managePermission + "; the schedule is found among everyone's.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, id, err := one(cmd.Context(), opts, args, true)
			if err != nil {
				return cli.Needs(err, managePermission)
			}
			s, err := cli.Result(api.TakeOverScheduleWithResponse(cmd.Context(), team, id))
			if err != nil {
				return cli.Needs(err, managePermission)
			}
			return printSchedule(opts, s, "taken over")
		},
	}
}

func printSchedule(opts *cli.Options, s *client.Schedule, did string) error {
	return opts.Out.Print(s, printer.Options{
		Table: func(table *printer.Table) {
			head := "schedule"
			if did != "" {
				head = opts.Styles.OK.Render(did)
			}
			table.Row(head, opts.Styles.Value.Render(cli.Safe(s.Name)), opts.Styles.Muted.Render(s.ID.String()))
			why := ""
			if s.PausedReason != nil {
				why = string(*s.PausedReason)
			}
			table.Row("status", string(s.Status), why)
			table.Row("when", whenSays(s.When, s.Timezone), "in "+cli.Safe(s.Timezone))
			if len(s.Upcoming) > 0 {
				table.Row("next", upcomingSays(*s), "")
			}
			for i, line := range cli.Lines(s.Objective) {
				label := ""
				if i == 0 {
					label = "objective"
				}
				table.Row(label, line, "")
			}
			switch {
			case s.Hub:
				table.Row("agent", "the hub", "")
			case s.AgentID != nil:
				table.Row("agent", s.AgentID.String(), "")
			}
			table.Row("answers", WhereSays(s.Target), "")
			if s.LastOutcome != nil {
				at := ""
				if s.LastFiredAt != nil {
					at = In(*s.LastFiredAt, s.Timezone)
				}
				table.Row("last", cli.Safe(*s.LastOutcome), at)
			}
			if s.WaitingChange != nil {
				table.Row("waits", "an agent asked "+cli.Safe(s.WaitingChange.Says), "since "+In(s.WaitingChange.Since, s.Timezone))
			}
		},
	})
}
