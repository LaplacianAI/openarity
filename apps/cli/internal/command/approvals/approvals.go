package approvals

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

const (
	readPermission     = "approval:read"
	settingsPermission = "team:settings"
	stampLayout        = "2006-01-02 15:04"
	maxSummary         = 60
	maxReason          = 1000
)

func New(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "approvals",
		Short: "Answer the calls an agent asked a person about, and set when it asks",
		Long: "A team can make its agents ask before a call: every call that is not a\n" +
			"read, only those that destroy, or all of them. The run stops, holding\n" +
			"nothing, until a person answers here, on the dashboard or in a chat by\n" +
			"typing the request's four-character code. An unanswered request expires.\n\n" +
			"Seeing requests needs approval:read in the team. Answering one is up to\n" +
			"the brain, which says who may. Settings and rules need team:settings to\n" +
			"change.",
	}

	cmd.AddCommand(
		newListCmd(opts),
		newShowCmd(opts),
		newApproveCmd(opts),
		newDeclineCmd(opts),
		newSettingsCmd(opts),
		newRulesCmd(opts),
	)
	return cmd
}

// view is a request as the CLI prints it: the brain's null times, people and
// words arrive as zero values, and a script should see them as absent.
type view struct {
	ID           string         `json:"id" yaml:"id"`
	RunID        string         `json:"run_id" yaml:"run_id"`
	RootRunID    string         `json:"root_run_id" yaml:"root_run_id"`
	CallID       string         `json:"call_id" yaml:"call_id"`
	Tool         string         `json:"tool" yaml:"tool"`
	Class        string         `json:"class" yaml:"class"`
	Arguments    map[string]any `json:"arguments" yaml:"arguments"`
	Summary      string         `json:"summary" yaml:"summary"`
	Code         string         `json:"code" yaml:"code"`
	Status       string         `json:"status" yaml:"status"`
	RequestedFor *string        `json:"requested_for" yaml:"requested_for"`
	RequireOther bool           `json:"require_other" yaml:"require_other"`
	DecidedBy    *string        `json:"decided_by" yaml:"decided_by"`
	DecidedVia   *string        `json:"decided_via" yaml:"decided_via"`
	Scope        *string        `json:"scope" yaml:"scope"`
	Reason       *string        `json:"reason" yaml:"reason"`
	ExpiresAt    time.Time      `json:"expires_at" yaml:"expires_at"`
	DecidedAt    *time.Time     `json:"decided_at" yaml:"decided_at"`
	UsedAt       *time.Time     `json:"used_at" yaml:"used_at"`
	CreatedAt    time.Time      `json:"created_at" yaml:"created_at"`
}

func viewOf(a *client.Approval) view {
	args := a.Arguments
	if args == nil {
		args = map[string]any{}
	}
	return view{
		ID: a.ID.String(), RunID: a.RunID.String(), RootRunID: a.RootRunID.String(), CallID: a.CallID,
		Tool: a.Tool, Class: string(a.Class), Arguments: args, Summary: a.Summary, Code: a.Code,
		Status: string(a.Status), RequestedFor: idOrNil(a.RequestedFor), RequireOther: a.RequireOther,
		DecidedBy: idOrNil(a.DecidedBy), DecidedVia: textOrNil(a.DecidedVia),
		Scope: textOrNil(string(a.Scope)), Reason: textOrNil(a.Reason),
		ExpiresAt: a.ExpiresAt, DecidedAt: timeOrNil(a.DecidedAt), UsedAt: timeOrNil(a.UsedAt),
		CreatedAt: a.CreatedAt,
	}
}

func idOrNil(id uuid.UUID) *string {
	if id == uuid.Nil {
		return nil
	}
	s := id.String()
	return &s
}

func textOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func timeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// oneLine is text as a single cell: a summary is the model's words and may
// run across lines, so it is cut before cli.Safe sees it.
func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit-1]) + "…"
}

// when is what a person wants to know of a request's time: how long a
// pending one has left, and how long ago any other was asked.
func when(now time.Time, v view) string {
	if v.Status == string(client.ApprovalStatusPending) {
		if left := v.ExpiresAt.Sub(now); left > 0 {
			return "expires in " + span(left)
		}
		return "expiring"
	}
	return span(now.Sub(v.CreatedAt)) + " ago"
}

func span(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under 1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func statusCell(opts *cli.Options, s string) string {
	switch client.ApprovalStatus(s) {
	case client.ApprovalStatusApproved:
		return opts.Styles.OK.Render(s)
	case client.ApprovalStatusDenied:
		return opts.Styles.Err.Render(s)
	case client.ApprovalStatusPending:
		return opts.Styles.Warn.Render(s)
	case client.ApprovalStatusExpired, client.ApprovalStatusCancelled:
		return opts.Styles.Muted.Render(s)
	default:
		return cli.Safe(s)
	}
}

func parseID(ref, kind, team string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(ref))
	if err != nil {
		return uuid.Nil, fmt.Errorf("%q is not %s id — `oa approvals list %s` shows them", ref, kind, team)
	}
	return id, nil
}

func newListCmd(opts *cli.Options) *cobra.Command {
	var (
		paging   cli.Paging
		statuses []string
		run      string
	)

	cmd := &cobra.Command{
		Use:     "list <team>",
		Aliases: []string{"ls"},
		Short:   "List what agents asked a person about",
		Long: "Newest first. --status filters and may be repeated: pending, approved,\n" +
			"denied, expired or cancelled. --run keeps the requests asked under one\n" +
			"top-level run, by any agent in its tree. Needs approval:read.\n\n" +
			"    oa approvals list platform --status pending\n" +
			"    oa approvals list platform --run 0b7c3e52-8a41-4f6d-9e2b-5c1a7d3f8e60",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			params := &client.ListApprovalsParams{}
			for _, s := range statuses {
				status := client.ApprovalStatus(strings.ToLower(strings.TrimSpace(s)))
				if !status.Valid() {
					return fmt.Errorf("%q is not a status — pending, approved, denied, expired or cancelled", s)
				}
				if params.Status == nil {
					params.Status = &[]client.ApprovalStatus{}
				}
				*params.Status = append(*params.Status, status)
			}
			if run != "" {
				id, err := uuid.Parse(strings.TrimSpace(run))
				if err != nil {
					return fmt.Errorf("%q is not a run id", run)
				}
				params.Run = &id
			}

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}

			params.Limit, params.Cursor = paging.Values()
			page, err := cli.Result(api.ListApprovalsWithResponse(cmd.Context(), team, params))
			if err != nil {
				return cli.Needs(err, readPermission)
			}

			views := make([]view, 0, len(page.Items))
			for i := range page.Items {
				views = append(views, viewOf(&page.Items[i]))
			}
			now := time.Now()
			return cli.PrintPage(opts, cli.Page[view]{
				Items:      views,
				NextCursor: page.NextCursor,
				Empty:      "no approval requests",
				More:       "oa approvals list " + args[0] + flagsOf(statuses, run),
				Row: func(table *printer.Table, v view) {
					table.Row(
						opts.Styles.Value.Render(cli.Safe(v.Code)),
						cli.Safe(v.Tool),
						cli.Safe(oneLine(v.Summary, maxSummary)),
						statusCell(opts, v.Status),
						opts.Styles.Muted.Render(when(now, v)),
						opts.Styles.Muted.Render(v.RunID),
					)
				},
			})
		},
	}

	paging.Flags(cmd)
	cmd.Flags().StringArrayVar(&statuses, "status", nil,
		"only requests in this state: pending, approved, denied, expired or cancelled; repeatable")
	cmd.Flags().StringVar(&run, "run", "", "only requests asked under this top-level run, by id")
	return cmd
}

// flagsOf rebuilds the filters the next-page hint must carry, since the
// cursor alone would silently widen the listing.
func flagsOf(statuses []string, run string) string {
	var b strings.Builder
	for _, s := range statuses {
		b.WriteString(" --status " + s)
	}
	if run != "" {
		b.WriteString(" --run " + run)
	}
	return b.String()
}

func newShowCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <team> <approval>",
		Short: "Show one request: the call, its arguments and how it was answered",
		Long: "The arguments are exactly what the call is made with if it is approved.\n" +
			"Needs approval:read.\n\n" +
			"    oa approvals show platform 5d2c8a10-77be-4c1f-8e53-9b0a6f21d4e7",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[1], "an approval", args[0])
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
			a, err := cli.Result(api.GetApprovalWithResponse(cmd.Context(), team, id))
			if err != nil {
				return cli.Needs(err, readPermission)
			}
			return printOne(opts, viewOf(a), "")
		},
	}
}

func printOne(opts *cli.Options, v view, did string) error {
	err := opts.Out.Print(v, printer.Options{
		Table: func(table *printer.Table) {
			head := statusCell(opts, v.Status)
			if did != "" {
				head = opts.Styles.OK.Render(did)
			}
			table.Row(head, opts.Styles.Value.Render(cli.Safe(v.Tool)), opts.Styles.Muted.Render(v.ID))
			table.Row("code", cli.Safe(v.Code), "")
			table.Row("class", cli.Safe(v.Class), "")
			table.Row("status", statusCell(opts, v.Status), "")
			table.Row("summary", cli.Safe(oneLine(v.Summary, 200)), "")
			table.Row("run", v.RunID, "")
			if v.RootRunID != v.RunID {
				table.Row("root run", v.RootRunID, "")
			}
			if v.RequireOther {
				table.Row("answered by", "someone other than who the run works for", "")
			}
			table.Row("asked", v.CreatedAt.Format(stampLayout), "")
			if v.Status == string(client.ApprovalStatusPending) {
				table.Row("expires", v.ExpiresAt.Format(stampLayout), "")
			}
			if v.DecidedAt != nil {
				table.Row("answered", v.DecidedAt.Format(stampLayout), "")
			}
			if v.DecidedBy != nil {
				table.Row("by", *v.DecidedBy, "")
			}
			if v.DecidedVia != nil {
				table.Row("via", cli.Safe(*v.DecidedVia), "")
			}
			if v.Scope != nil {
				table.Row("scope", cli.Safe(*v.Scope), "")
			}
			if v.Reason != nil {
				table.Row("reason", cli.Safe(oneLine(*v.Reason, maxReason)), "")
			}
			if v.UsedAt != nil {
				table.Row("made", v.UsedAt.Format(stampLayout), "")
			}
		},
	})
	if err != nil {
		return err
	}

	// The arguments are the model's, often several lines: under the table,
	// and silent in json and yaml where they are already in the document.
	pretty, err := json.MarshalIndent(v.Arguments, "  ", "  ")
	if err != nil {
		return fmt.Errorf("show the arguments: %w", err)
	}
	text := "\n" + opts.Styles.Label.Render("Arguments")
	for _, l := range cli.Lines("  " + string(pretty)) {
		text += "\n" + l
	}
	opts.Out.Note(text)
	return nil
}

func newApproveCmd(opts *cli.Options) *cobra.Command {
	var (
		forRun bool
		reason string
	)

	cmd := &cobra.Command{
		Use:   "approve <team> <approval>",
		Short: "Let the call be made",
		Long: "The run starts again and makes the call. --for-run also lets the run\n" +
			"make the same call again without asking; it ends with the run.\n\n" +
			"    oa approvals approve platform 5d2c8a10-77be-4c1f-8e53-9b0a6f21d4e7\n" +
			"    oa approvals approve platform 5d2c8a10-77be-4c1f-8e53-9b0a6f21d4e7 --for-run",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return decide(cmd, opts, args, true, forRun, reason)
		},
	}

	cmd.Flags().BoolVar(&forRun, "for-run", false, "also allow this call for the rest of the run")
	cmd.Flags().StringVar(&reason, "reason", "", "told to the agent with the answer, at most 1,000 characters")
	return cmd
}

func newDeclineCmd(opts *cli.Options) *cobra.Command {
	var reason string

	cmd := &cobra.Command{
		Use:     "decline <team> <approval>",
		Aliases: []string{"deny"},
		Short:   "Refuse the call",
		Long: "The run starts again and is told the call was refused, and why when\n" +
			"--reason says.\n\n" +
			"    oa approvals decline platform 5d2c8a10-77be-4c1f-8e53-9b0a6f21d4e7 \\\n" +
			"        --reason 'not on a Friday'",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return decide(cmd, opts, args, false, false, reason)
		},
	}

	cmd.Flags().StringVar(&reason, "reason", "", "told to the agent with the answer, at most 1,000 characters")
	return cmd
}

func decide(cmd *cobra.Command, opts *cli.Options, args []string, approve, forRun bool, reason string) error {
	id, err := parseID(args[1], "an approval", args[0])
	if err != nil {
		return err
	}
	if utf8.RuneCountInString(reason) > maxReason {
		return fmt.Errorf("--reason is at most %d characters", maxReason)
	}

	via := client.Cli
	body := client.DecideApprovalJSONRequestBody{Approve: approve, Via: &via}
	if forRun {
		scope := client.ApprovalDecisionScopeRun
		body.Scope = &scope
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		body.Reason = &reason
	}

	api, err := opts.API(cmd.Context())
	if err != nil {
		return err
	}
	team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
	if err != nil {
		return err
	}

	res, err := api.DecideApprovalWithResponse(cmd.Context(), team, id, body)
	if err != nil {
		return err
	}
	switch res.StatusCode() {
	case http.StatusForbidden:
		// The brain says why — a rule wants somebody else, or the role cannot
		// answer — and "not allowed" would lose it.
		if sentence := strings.TrimSpace(cli.Line(string(res.Body))); sentence != "" {
			return errors.New(sentence)
		}
	case http.StatusConflict:
		if res.JSON409 != nil {
			return errors.New(standing(viewOf(res.JSON409)))
		}
	}
	a, err := cli.Result(res, nil)
	if err != nil {
		return err
	}

	did := "approved"
	if !approve {
		did = "declined"
	}
	return printOne(opts, viewOf(a), did)
}

// standing is what a request already is, for the answer that came too late.
func standing(v view) string {
	switch client.ApprovalStatus(v.Status) {
	case client.ApprovalStatusExpired:
		return "the request expired before it was answered"
	case client.ApprovalStatusCancelled:
		return "the request was cancelled — its run ended or went on without it"
	case client.ApprovalStatusApproved, client.ApprovalStatusDenied:
		text := "already " + v.Status
		if v.DecidedBy != nil {
			text += " by " + cli.Line(*v.DecidedBy)
		}
		if v.DecidedVia != nil {
			text += " via " + cli.Line(*v.DecidedVia)
		}
		return text
	case client.ApprovalStatusPending:
		return "the request is still pending, but the brain refused the answer"
	default:
		return "the request is " + cli.Line(v.Status)
	}
}
