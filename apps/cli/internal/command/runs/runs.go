package runs

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

const (
	writePermission = "run:write"
	stampLayout     = "2006-01-02 15:04"
	maxInput        = 100 << 10
)

func New(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runs",
		Short: "Start an agent on a task, and follow what it does",
		Long: "A run is one agent working through one input: it is queued, a worker\n" +
			"takes it, and it ends succeeded, failed or cancelled. Every run belongs\n" +
			"to a conversation — name one with --session, or the run gets a direct\n" +
			"session of its own.\n\n" +
			"A run is visible where its session is. Starting or cancelling one needs\n" +
			"run:write in the team.",
	}

	cmd.AddCommand(
		newStartCmd(opts),
		newListCmd(opts),
		newGetCmd(opts),
		newEventsCmd(opts),
		newCancelCmd(opts),
	)
	return cmd
}

type runView struct {
	ID           string     `json:"id" yaml:"id"`
	SessionID    string     `json:"session_id" yaml:"session_id"`
	Agent        string     `json:"agent" yaml:"agent"`
	Status       string     `json:"status" yaml:"status"`
	Input        string     `json:"input" yaml:"input"`
	Output       *string    `json:"output" yaml:"output"`
	Error        *string    `json:"error" yaml:"error"`
	InputTokens  int64      `json:"input_tokens" yaml:"input_tokens"`
	OutputTokens int64      `json:"output_tokens" yaml:"output_tokens"`
	CreatedAt    time.Time  `json:"created_at" yaml:"created_at"`
	StartedAt    *time.Time `json:"started_at" yaml:"started_at"`
	FinishedAt   *time.Time `json:"finished_at" yaml:"finished_at"`
	ParentRunID  *string    `json:"parent_run_id" yaml:"parent_run_id"`
	Depth        int32      `json:"depth" yaml:"depth"`
}

func viewOf(r *client.Run) runView {
	v := runView{
		ID: r.ID.String(), SessionID: r.SessionID.String(), Agent: r.Agent, Status: string(r.Status),
		Input: r.Input, Output: r.Output, Error: r.Error,
		InputTokens: r.InputTokens, OutputTokens: r.OutputTokens,
		CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Depth: r.Depth,
	}
	if r.ParentRunID != nil {
		parent := r.ParentRunID.String()
		v.ParentRunID = &parent
	}
	return v
}

func newStartCmd(opts *cli.Options) *cobra.Command {
	var (
		session   string
		following bool
		verbose   bool
	)

	cmd := &cobra.Command{
		Use:   "start <team> <agent> <input>",
		Short: "Start an agent on a task",
		Long: "Needs run:write in the team. The agent is named, not looked up: the\n" +
			"brain resolves it as it queues the run. The input is what the agent is\n" +
			"asked to do, at most 100 KiB; - reads it from stdin.\n\n" +
			"--follow prints the run's events as they happen and exits once it ends,\n" +
			"non-zero unless it succeeded. Ctrl-C stops following, not the run.\n" +
			"--verbose adds each step's tokens and prints tool results whole.\n\n" +
			"    oa runs start platform triage 'Sort the issues opened today.'\n" +
			"    oa runs start platform triage - --follow < task.md\n" +
			"    oa runs start platform triage 'And the ones from yesterday?' \\\n" +
			"        --session c7e2a940-51f6-4c3b-8d7a-0e9b3f1a6c52",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := readInput(cmd.InOrStdin(), args[2])
			if err != nil {
				return err
			}

			body := client.StartRunJSONRequestBody{Agent: strings.TrimSpace(args[1]), Input: input}
			if session != "" {
				id, err := uuid.Parse(strings.TrimSpace(session))
				if err != nil {
					return fmt.Errorf("%q is not a session id — `oa sessions list %s` shows them", session, args[0])
				}
				body.SessionID = &id
			}

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}

			run, err := cli.Accepted(api.StartRunWithResponse(cmd.Context(), team, body))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			if !following {
				return printRun(opts, run, "started", args[0])
			}

			err = opts.Out.Line(viewOf(run), printer.Options{
				Table: func(table *printer.Table) {
					table.Row(opts.Styles.OK.Render("started"),
						opts.Styles.Value.Render(cli.Safe(run.Agent)),
						opts.Styles.Muted.Render(run.ID.String()))
				},
			})
			if err != nil {
				return err
			}
			return followRun(cmd.Context(), opts, team, run.ID, args[0],
				follow{verbose: verbose, afterStart: true})
		},
	}

	cmd.Flags().StringVar(&session, "session", "",
		"the conversation to run in, by id; without it the run gets a session of its own")
	cmd.Flags().BoolVar(&following, "follow", false,
		"print the run's events until it ends, as oa runs events does")
	cmd.Flags().BoolVar(&verbose, "verbose", false,
		"with --follow, also show each step and its tokens, and every tool result whole")
	return cmd
}

func readInput(in io.Reader, arg string) (string, error) {
	input := arg
	if arg == "-" {
		raw, err := io.ReadAll(io.LimitReader(in, maxInput+1))
		if err != nil {
			return "", fmt.Errorf("read the input: %w", err)
		}
		input = string(raw)
	}
	if strings.TrimSpace(input) == "" {
		return "", errors.New("the input is empty — say what the agent should do")
	}
	return input, nil
}

func newListCmd(opts *cli.Options) *cobra.Command {
	var paging cli.Paging

	cmd := &cobra.Command{
		Use:     "list <team> <session>",
		Aliases: []string{"ls"},
		Short:   "List the runs started in a conversation",
		Long: "Newest first. Only the runs started in the conversation are listed; a\n" +
			"run an agent delegated to belongs to the run that started it.\n\n" +
			"    oa runs list platform c7e2a940-51f6-4c3b-8d7a-0e9b3f1a6c52",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			session, err := uuid.Parse(strings.TrimSpace(args[1]))
			if err != nil {
				return fmt.Errorf("%q is not a session id — `oa sessions list %s` shows them", args[1], args[0])
			}

			limit, cursor := paging.Values()
			page, err := cli.Result(api.ListSessionRunsWithResponse(cmd.Context(), team, session,
				&client.ListSessionRunsParams{Limit: limit, Cursor: cursor}))
			if err != nil {
				return err
			}

			views := make([]runView, 0, len(page.Items))
			for i := range page.Items {
				views = append(views, viewOf(&page.Items[i]))
			}

			return cli.PrintPage(opts, cli.Page[runView]{
				Items:      views,
				NextCursor: page.NextCursor,
				Empty:      "no runs in this conversation",
				More:       "oa runs list " + args[0] + " " + args[1],
				Row: func(table *printer.Table, r runView) {
					table.Row(
						opts.Styles.Value.Render(r.ID),
						status(opts, r.Status),
						cli.Safe(r.Agent),
						opts.Styles.Muted.Render(r.CreatedAt.Format(stampLayout)),
						cli.Safe(clip(r.Input)),
					)
				},
			})
		},
	}

	paging.Flags(cmd)
	return cmd
}

func newGetCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "get <team> <run>",
		Short: "Show a run: its status, input, answer and what it spent",
		Long: "The answer is printed whole, as lines, with anything a terminal would\n" +
			"act on taken out; the error is quoted if it holds any. -o json has them\n" +
			"exactly as stored.\n\n" +
			"    oa runs get platform 0b7c3e52-8a41-4f6d-9e2b-5c1a7d3f8e60",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			id, err := parseRun(args)
			if err != nil {
				return err
			}

			run, err := cli.Result(api.GetRunWithResponse(cmd.Context(), team, id))
			if err != nil {
				return err
			}
			return printRun(opts, run, "", args[0])
		},
	}
}

func newEventsCmd(opts *cli.Options) *cobra.Command {
	var (
		after   int32
		verbose bool
	)

	cmd := &cobra.Command{
		Use:   "events <team> <run>",
		Short: "Follow what a run does, as it does it",
		Long: "Prints every event the run has recorded, then each new one as it\n" +
			"happens, and exits once the run ends — zero if it succeeded, non-zero\n" +
			"if it failed or was cancelled. A run that ended long ago prints its\n" +
			"whole history and exits at once.\n\n" +
			"The table reads like a transcript: each tool call with the start of\n" +
			"its result, then the answer and how the run ended. --verbose adds each\n" +
			"step's tokens and prints every result whole.\n\n" +
			"-o json prints one object per line, {id, event, data}, with data\n" +
			"exactly as the brain recorded it. Ctrl-C stops following; the run\n" +
			"carries on, and --after picks up where you left off.\n\n" +
			"    oa runs events platform 0b7c3e52-8a41-4f6d-9e2b-5c1a7d3f8e60\n" +
			"    oa runs events platform 0b7c3e52-8a41-4f6d-9e2b-5c1a7d3f8e60 -o json | jq .event",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if after < 0 {
				return errors.New("--after is an event number, and those start at 1")
			}

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			id, err := parseRun(args)
			if err != nil {
				return err
			}

			var from *int32
			if after > 0 {
				from = &after
			}
			return followRun(cmd.Context(), opts, team, id, args[0], follow{after: from, verbose: verbose})
		},
	}

	cmd.Flags().Int32Var(&after, "after", 0,
		"skip the events up to and including this number, to resume a stream")
	cmd.Flags().BoolVar(&verbose, "verbose", false,
		"also show each step and its tokens, and every tool result whole")
	return cmd
}

func newCancelCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <team> <run>",
		Short: "Stop a run",
		Long: "Needs run:write in the team. A queued run ends at once. A running one\n" +
			"is marked and stops at its worker's next heartbeat, within 15 seconds;\n" +
			"`oa runs events` shows it end.\n\n" +
			"    oa runs cancel platform 0b7c3e52-8a41-4f6d-9e2b-5c1a7d3f8e60",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			id, err := parseRun(args)
			if err != nil {
				return err
			}

			res, err := api.CancelRunWithResponse(cmd.Context(), team, id)
			if err == nil && res.StatusCode() == http.StatusConflict {
				return fmt.Errorf("the run has already finished — `oa runs get %s %s` shows how it ended",
					args[0], id)
			}
			run, err := cli.Accepted(res, err)
			if err != nil {
				return cli.Needs(err, writePermission)
			}

			did := "cancelled"
			if run.Status != client.Cancelled {
				did = "cancelling"
			}
			if err := printRun(opts, run, did, args[0]); err != nil {
				return err
			}
			if run.Status != client.Cancelled {
				opts.Out.Note(opts.Styles.Muted.Render(
					"it stops within 15 seconds — `oa runs events " + args[0] + " " + id.String() + "` shows it end"))
			}
			return nil
		},
	}
}

func parseRun(args []string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(args[1]))
	if err != nil {
		return uuid.Nil, fmt.Errorf("%q is not a run id — `oa runs list %s <session>` shows them", args[1], args[0])
	}
	return id, nil
}

// teamRef is the team as it was typed, for the command that reads a parent.
func printRun(opts *cli.Options, r *client.Run, did, teamRef string) error {
	view := viewOf(r)
	err := opts.Out.Print(view, printer.Options{
		Table: func(table *printer.Table) {
			head := status(opts, view.Status)
			if did != "" {
				head = opts.Styles.OK.Render(did)
			}
			table.Row(head, opts.Styles.Value.Render(cli.Safe(view.Agent)), opts.Styles.Muted.Render(view.ID))
			table.Row("status", status(opts, view.Status), "")
			table.Row("session", view.SessionID, "")
			if view.ParentRunID != nil {
				table.Row("delegated by", *view.ParentRunID,
					opts.Styles.Muted.Render("oa runs get "+cli.Safe(teamRef)+" "+*view.ParentRunID))
			}
			table.Row("input", cli.Safe(view.Input), "")
			if view.Error != nil {
				table.Row("error", opts.Styles.Err.Render(cli.Safe(*view.Error)), "")
			}
			table.Row("tokens",
				strconv.FormatInt(view.InputTokens, 10)+" in, "+strconv.FormatInt(view.OutputTokens, 10)+" out", "")
			table.Row("created", view.CreatedAt.Format(stampLayout), "")
			if view.StartedAt != nil {
				table.Row("started", view.StartedAt.Format(stampLayout), "")
			}
			if view.FinishedAt != nil {
				table.Row("finished", view.FinishedAt.Format(stampLayout), "")
			}
		},
	})
	if err != nil || view.Output == nil {
		return err
	}

	// The answer is prose, often many lines, so it goes under the table rather
	// than into a cell. Note is silent under json and yaml.
	lines := answer(paintOf(opts.Styles), *view.Output)
	if len(lines) == 0 {
		return nil
	}
	text := "\n" + opts.Styles.Label.Render("Answer")
	for _, l := range lines {
		text += "\n" + strings.TrimRight("  "+l, " ")
	}
	opts.Out.Note(text)
	return nil
}

func status(opts *cli.Options, s string) string {
	switch client.RunStatus(s) {
	case client.Succeeded:
		return opts.Styles.OK.Render(s)
	case client.Failed, client.Cancelled:
		return opts.Styles.Err.Render(s)
	case client.Queued, client.Running:
		return opts.Styles.Muted.Render(s)
	default:
		return cli.Safe(s)
	}
}
