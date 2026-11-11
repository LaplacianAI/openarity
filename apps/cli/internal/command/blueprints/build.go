package blueprints

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/command/runs"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

func NewBuild(opts *cli.Options) *cobra.Command {
	var (
		session string
		verbose bool
	)

	cmd := &cobra.Command{
		Use:   "build <team> [<ask>]",
		Short: "Ask the team's builder for agents, MCP servers or skills",
		Long: "The builder reads the team, asks what it cannot know, and proposes a\n" +
			"blueprint. It writes nothing: you apply a proposal, as yourself, with\n" +
			"oa proposals apply. Answer its questions in the same conversation with\n" +
			"--session, which the first build prints. It runs on the team's model\n" +
			"and needs run:write. - reads the ask from stdin.\n\n" +
			"    oa build support 'An agent that triages support email, using our FAQ.'\n" +
			"    oa build support 'Sonnet, and yes to the helpdesk server.' --session <id>\n\n" +
			"With no ask, it lists your recent conversations with the builder, each\n" +
			"with the session id to carry it on.\n\n" +
			"    oa build support",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				return conversations(cmd, opts, args[0])
			}
			input, err := runs.ReadInput(cmd.InOrStdin(), args[1])
			if err != nil {
				return err
			}
			builder := true
			body := client.StartRunJSONRequestBody{Input: input, Builder: &builder}
			if session != "" {
				id, err := uuid.Parse(strings.TrimSpace(session))
				if err != nil {
					return fmt.Errorf("%q is not a session id — the first oa build prints it", session)
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
				return cli.Needs(err, "run:write")
			}

			if !isTable(opts) {
				return opts.Out.Print(run, printer.Options{})
			}
			if err := runs.Follow(cmd.Context(), opts, team, run.ID, args[0], verbose); err != nil {
				return err
			}
			return proposed(cmd, opts, api, team, run, args[0])
		},
	}

	cmd.Flags().StringVar(&session, "session", "", "carry on the conversation the first build started, by id")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "also show each step and its tokens, and every tool result whole")
	return cmd
}

// conversationView is a builder conversation as a script reads it: its
// session, and its newest turn.
type conversationView struct {
	Session   uuid.UUID        `json:"session" yaml:"session"`
	Status    client.RunStatus `json:"status" yaml:"status"`
	LastAsked string           `json:"last_asked" yaml:"last_asked"`
	At        time.Time        `json:"at" yaml:"at"`
}

// conversations lists the builder conversations the caller can carry on,
// one per session, from its newest run.
func conversations(cmd *cobra.Command, opts *cli.Options, teamRef string) error {
	api, err := opts.API(cmd.Context())
	if err != nil {
		return err
	}
	team, err := cli.ResolveTeam(cmd.Context(), api, teamRef)
	if err != nil {
		return err
	}
	builder, limit := true, client.Limit(100)
	page, err := cli.Result(api.ListTeamRunsWithResponse(cmd.Context(), team,
		&client.ListTeamRunsParams{Builder: &builder, Limit: &limit}))
	if err != nil {
		return err
	}
	seen := map[uuid.UUID]bool{}
	views := make([]conversationView, 0, len(page.Items))
	for _, r := range page.Items {
		if seen[r.SessionID] {
			continue
		}
		seen[r.SessionID] = true
		views = append(views, conversationView{Session: r.SessionID, Status: r.Status, LastAsked: r.Input, At: r.CreatedAt})
	}
	if len(views) == 0 {
		opts.Out.Note(opts.Styles.Muted.Render("No conversations with the builder yet. Start one: oa build " +
			cli.Line(teamRef) + " '<what you want>'"))
	}
	err = opts.Out.Print(views, printer.Options{Table: func(t *printer.Table) {
		for _, v := range views {
			t.Row(opts.Styles.Value.Render(v.Session.String()), string(v.Status),
				opts.Styles.Muted.Render(v.At.Format("2006-01-02 15:04")), cli.Safe(clipAsk(v.LastAsked)))
		}
	}})
	if err != nil || len(views) == 0 {
		return err
	}
	opts.Out.Note(opts.Styles.Muted.Render("Carry one on: oa build " + cli.Line(teamRef) + " '<your message>' --session <session>"))
	return nil
}

func clipAsk(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 60 {
		return string(r[:59]) + "…"
	}
	return s
}

// proposed shows what the run proposed, if anything, as the plan applying it
// would make now, and says how to go on.
func proposed(cmd *cobra.Command, opts *cli.Options, api *client.ClientWithResponses,
	team uuid.UUID, run *client.Run, teamRef string,
) error {
	session := run.SessionID
	page, err := cli.Result(api.ListProposalsWithResponse(cmd.Context(), team, &client.ListProposalsParams{Session: &session}))
	if err != nil {
		return err
	}
	var latest *client.Proposal
	for i := range page.Items {
		if page.Items[i].RunID == run.ID && page.Items[i].State == client.ProposalStateOpen {
			latest = &page.Items[i]
			break
		}
	}
	more := "oa build " + cli.Line(teamRef) + " '<your answer>' --session " + session.String()
	if latest == nil {
		opts.Out.Note(opts.Styles.Muted.Render("No proposal yet. Answer with: " + more))
		return nil
	}
	got, err := cli.Result(api.GetProposalWithResponse(cmd.Context(), team, latest.ID))
	if err != nil {
		return err
	}
	opts.Out.Note(opts.Styles.Value.Render("Proposal "+latest.ID.String()) + ": " + cli.Line(got.Summary))
	if got.Plan != nil {
		if err := show(opts, got.Plan); err != nil {
			return err
		}
	}
	opts.Out.Note(opts.Styles.Muted.Render("Apply it: oa proposals apply " + cli.Line(teamRef) + " " + latest.ID.String() +
		"\nChange it: " + more))
	return nil
}

func NewProposals(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proposals",
		Short: "What the team's builder proposed, and applying it",
	}
	cmd.AddCommand(newProposalsList(opts), newProposalsGet(opts), newProposalsApply(opts))
	return cmd
}

func newProposalsList(opts *cli.Options) *cobra.Command {
	var (
		paging  cli.Paging
		session string
	)
	cmd := &cobra.Command{
		Use:     "list <team>",
		Aliases: []string{"ls"},
		Short:   "The builder's proposals, newest first",
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
			limit, cursor := paging.Values()
			params := &client.ListProposalsParams{Limit: limit, Cursor: cursor}
			if session != "" {
				id, err := uuid.Parse(strings.TrimSpace(session))
				if err != nil {
					return fmt.Errorf("%q is not a session id", session)
				}
				params.Session = &id
			}
			page, err := cli.Result(api.ListProposalsWithResponse(cmd.Context(), team, params))
			if err != nil {
				return err
			}
			return cli.PrintPage(opts, cli.Page[client.Proposal]{
				Items:      page.Items,
				NextCursor: page.NextCursor,
				Empty:      "no proposals yet — oa build asks the builder for one",
				More:       "oa proposals list " + args[0],
				Row: func(table *printer.Table, p client.Proposal) {
					table.Row(opts.Styles.Value.Render(string(p.State)), cli.Safe(p.Summary),
						opts.Styles.Muted.Render(p.CreatedAt.Format("2006-01-02 15:04")), opts.Styles.Muted.Render(p.ID.String()))
				},
			})
		},
	}
	paging.Flags(cmd)
	cmd.Flags().StringVar(&session, "session", "", "only this conversation's proposals")
	return cmd
}

func proposalID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return uuid.Nil, fmt.Errorf("%q is not a proposal id — oa proposals list shows them", raw)
	}
	return id, nil
}

func newProposalsGet(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "get <team> <proposal>",
		Short: "A proposal and its plan: for an open one, what applying it would do now",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := proposalID(args[1])
			if err != nil {
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
			got, err := cli.Result(api.GetProposalWithResponse(cmd.Context(), team, id))
			if err != nil {
				return err
			}
			if !isTable(opts) {
				return opts.Out.Print(got, printer.Options{})
			}
			opts.Out.Note(opts.Styles.Value.Render(string(got.State)) + "  " + cli.Line(got.Summary))
			if got.Plan == nil {
				return nil
			}
			return show(opts, got.Plan)
		},
	}
}

func newProposalsApply(opts *cli.Options) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "apply <team> <proposal>",
		Short: "Apply a proposal, as yourself, after showing its plan",
		Long: "Plans the proposal again as you, shows it, asks, then applies exactly that\n" +
			"plan. Each write needs its own permission, as oa apply's do. If the team\n" +
			"changed in between, nothing is written and the new plan is shown.\n\n" + rules,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			table := isTable(opts)
			if (opts.NonInteractive || !table) && !yes {
				return errors.New("apply asks before it writes, showing the plan as a table — pass --yes to skip that")
			}
			id, err := proposalID(args[1])
			if err != nil {
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
			got, err := cli.Result(api.GetProposalWithResponse(cmd.Context(), team, id))
			if err != nil {
				return err
			}
			if got.State != client.ProposalStateOpen || got.Plan == nil {
				return fmt.Errorf("this proposal is %s, so it cannot be applied", got.State)
			}
			plan := got.Plan
			if table || len(plan.Refusals) > 0 || !changes(plan) {
				if err := show(opts, plan); err != nil {
					return err
				}
			}
			if len(plan.Refusals) > 0 {
				return fmt.Errorf("fix the %s first; nothing was applied", count(len(plan.Refusals), "refusal"))
			}
			if !changes(plan) {
				return nil
			}
			if !yes {
				if err := confirm(cmd, opts, args[0], false); err != nil {
					return err
				}
			}
			res, err := api.ApplyProposalWithResponse(cmd.Context(), team, id, client.ProposalApply{PlanDigest: plan.Digest})
			if err != nil {
				return err
			}
			if res.StatusCode() == http.StatusConflict && res.JSON409 == nil {
				return cli.APIError(res.StatusCode(), res.Body)
			}
			return applied(opts, args[0], &client.ApplyBlueprintResponse{
				Body: res.Body, HTTPResponse: res.HTTPResponse,
				JSON200: res.JSON200, JSON409: res.JSON409, JSON422: res.JSON422,
			})
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "apply without asking")
	return cmd
}
