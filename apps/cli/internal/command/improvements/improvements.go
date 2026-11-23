package improvements

import (
	"fmt"
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
	decidePermission = "improvement:approve plus agent:write (or skill:write for a skill)"
	// Rejecting and clearing a flag write nothing to an agent or a skill.
	approvePermission = "improvement:approve"
	maxWhy            = 70
	stampLayout       = "2006-01-02 15:04"
	statusList        = "waiting, applied, rejected, reverted, withdrawn, or flagged (on its own)"
	filePrefix        = "files/"
	unreadableRunNote = "(not yours to open)"
)

func New(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "improvements",
		Short: "Review the changes agents propose to themselves, and decide them",
		Long: "A team can let its agents propose improvements to themselves from what\n" +
			"went wrong in their runs: a sharper instruction, a note on a tool, a\n" +
			"skill. A proposal waits here until somebody decides it. Approving\n" +
			"applies it; reverting puts the agent back as it was. An applied change\n" +
			"is watched: its next ten judged runs against the ones before. One that\n" +
			"went clearly worse, or costlier with no fewer failures, is flagged for a\n" +
			"person to clear or revert; nothing is reverted by itself.\n\n" +
			"Deciding needs improvement:approve in the team, and the write\n" +
			"permission of what the proposal changes: agent:write, or skill:write\n" +
			"for a skill. Every command takes the team first.",
	}

	cmd.AddCommand(
		newListCmd(opts),
		newShowCmd(opts),
		newPassesCmd(opts),
		newDecisionCmd(opts, "approve", client.ImprovementDecisionActionApprove, "approved", decidePermission,
			"Apply the proposal to the agent",
			"The change is made now, and can be reverted later. It is refused when\n"+
				"the agent has changed since the proposal was made."),
		newDecisionCmd(opts, "reject", client.ImprovementDecisionActionReject, "rejected", approvePermission,
			"Turn the proposal down",
			"The agent is left as it is, and the same proposal is not made again."),
		newDecisionCmd(opts, "revert", client.ImprovementDecisionActionRevert, "reverted", decidePermission,
			"Undo an applied proposal",
			"The agent goes back to what it was before, unless it has changed\n"+
				"since — then the brain refuses and says so."),
		newDecisionCmd(opts, "clear", client.ImprovementDecisionActionClear, "cleared", approvePermission,
			"Keep a change its watch flagged",
			"A person looked and keeps it applied; the flag is cleared and the\n"+
				"improver's record says so. Refused unless the watch flagged it."),
	)
	return cmd
}

// view is a proposal as the CLI prints it: ids as strings, absent people and
// times as null.
type view struct {
	ID         string       `json:"id" yaml:"id"`
	Agent      ref          `json:"agent" yaml:"agent"`
	Skill      *ref         `json:"skill" yaml:"skill"`
	PassID     *string      `json:"pass_id" yaml:"pass_id"`
	Kind       string       `json:"kind" yaml:"kind"`
	Status     string       `json:"status" yaml:"status"`
	Scope      string       `json:"scope" yaml:"scope"`
	Goal       string       `json:"goal" yaml:"goal"`
	Saves      []string     `json:"saves" yaml:"saves"`
	Marks      []string     `json:"marks" yaml:"marks"`
	Why        string       `json:"why" yaml:"why"`
	Hypothesis string       `json:"hypothesis" yaml:"hypothesis"`
	Reaches    []ref        `json:"reaches" yaml:"reaches"`
	Changes    []changeView `json:"changes" yaml:"changes"`
	Runs       []runView    `json:"runs" yaml:"runs"`
	Outsider   bool         `json:"outsider" yaml:"outsider"`
	DecidedBy  *string      `json:"decided_by" yaml:"decided_by"`
	DecidedAt  *time.Time   `json:"decided_at" yaml:"decided_at"`
	RevertedBy *string      `json:"reverted_by" yaml:"reverted_by"`
	RevertedAt *time.Time   `json:"reverted_at" yaml:"reverted_at"`
	CreatedAt  time.Time    `json:"created_at" yaml:"created_at"`
	Watch      *watchView   `json:"watch" yaml:"watch"`
}

type ref struct {
	ID   string `json:"id" yaml:"id"`
	Name string `json:"name" yaml:"name"`
}

type runView struct {
	ID       string `json:"id" yaml:"id"`
	Readable bool   `json:"readable" yaml:"readable"`
}

type changeView struct {
	Field  string `json:"field" yaml:"field"`
	Type   string `json:"type" yaml:"type"`
	Before any    `json:"before" yaml:"before"`
	After  any    `json:"after" yaml:"after"`
}

func refOf(r client.NamedRef) ref { return ref{ID: r.ID.String(), Name: r.Name} }

func idOrNil(id *uuid.UUID) *string {
	if id == nil || *id == uuid.Nil {
		return nil
	}
	s := id.String()
	return &s
}

func viewOf(p *client.Improvement) view {
	v := view{
		ID: p.ID.String(), Agent: refOf(p.Agent), Kind: string(p.Kind), Status: string(p.Status),
		Scope: string(p.Scope), Marks: make([]string, 0, len(p.Marks)), Why: p.Why, Hypothesis: p.Hypothesis,
		Reaches: make([]ref, 0, len(p.Reaches)), Changes: make([]changeView, 0, len(p.Changes)),
		Runs: make([]runView, 0, len(p.Runs)), Outsider: p.Outsider,
		DecidedBy: idOrNil(p.DecidedBy), DecidedAt: p.DecidedAt,
		RevertedBy: idOrNil(p.RevertedBy), RevertedAt: p.RevertedAt, CreatedAt: p.CreatedAt,
		PassID: idOrNil(p.PassID), Watch: watchOf(p.Watch),
		Goal: string(p.Goal), Saves: make([]string, 0, len(p.Saves)),
	}
	for _, s := range p.Saves {
		v.Saves = append(v.Saves, string(s))
	}
	if p.Skill != nil {
		s := refOf(*p.Skill)
		v.Skill = &s
	}
	for _, m := range p.Marks {
		v.Marks = append(v.Marks, string(m))
	}
	for _, r := range p.Reaches {
		v.Reaches = append(v.Reaches, refOf(r))
	}
	for _, c := range p.Changes {
		v.Changes = append(v.Changes, changeView{Field: c.Field, Type: string(c.Type), Before: c.Before, After: c.After})
	}
	for _, r := range p.Runs {
		v.Runs = append(v.Runs, runView{ID: r.ID.String(), Readable: r.Readable})
	}
	return v
}

// markWord is a mark as the one word a person scans for.
func markWord(m string) string {
	switch client.ImprovementMark(m) {
	case client.WidensAccess:
		return "access"
	case client.WidensReach:
		return "reach"
	case client.RaisesCost:
		return "cost"
	case client.ChangesCode:
		return "code"
	case client.ChangesDiscovery:
		return "discovery"
	case client.ChangesBehaviour:
		return "behaviour"
	default:
		return cli.Safe(m)
	}
}

func marksCell(marks []string) string {
	words := make([]string, 0, len(marks))
	for _, m := range marks {
		words = append(words, markWord(m))
	}
	if len(words) == 0 {
		return "-"
	}
	return strings.Join(words, ",")
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit-1]) + "…"
}

func age(d time.Duration) string {
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
	switch client.ImprovementStatus(s) {
	case client.ImprovementStatusApplied:
		return opts.Styles.OK.Render(s)
	case client.ImprovementStatusRejected:
		return opts.Styles.Err.Render(s)
	case client.ImprovementStatusWaiting:
		return opts.Styles.Warn.Render(s)
	case client.ImprovementStatusReverted, client.ImprovementStatusWithdrawn:
		return opts.Styles.Muted.Render(s)
	default:
		return cli.Safe(s)
	}
}

// scopeText is a scope as a phrase: invariant is jargon.
func scopeText(s string) string {
	switch client.ImprovementScope(s) {
	case client.Invariant:
		return "invariant (a general weakness of agents)"
	case client.Domain:
		return "domain (this team's own knowledge)"
	default:
		return cli.Safe(s)
	}
}

func parseID(ref, team string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(ref))
	if err != nil {
		return uuid.Nil, fmt.Errorf("%q is not a proposal id — `oa improvements list %s` shows them", ref, team)
	}
	return id, nil
}

func newListCmd(opts *cli.Options) *cobra.Command {
	var (
		paging   cli.Paging
		statuses []string
		agent    string
		query    string
	)

	cmd := &cobra.Command{
		Use:     "list <team>",
		Aliases: []string{"ls"},
		Short:   "List the improvements agents proposed",
		Long: "Newest first. Without --status an approver sees what is waiting and\n" +
			"everyone else what was applied. --status may be repeated: " + statusList + ".\n" +
			"Seeing waiting, rejected or withdrawn proposals needs improvement:approve.\n" +
			"--agent keeps one agent's, by name or id; --query keeps those whose why\n" +
			"or hypothesis contains the text.\n\n" +
			"    oa improvements list platform\n" +
			"    oa improvements list platform --status applied --status reverted --agent support-bot",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			params := &client.ListImprovementsParams{}
			for _, s := range statuses {
				status := client.ImprovementFilter(strings.ToLower(strings.TrimSpace(s)))
				if !status.Valid() {
					return fmt.Errorf("%q is not a status — %s", s, statusList)
				}
				if params.Status == nil {
					params.Status = &[]client.ImprovementFilter{}
				}
				*params.Status = append(*params.Status, status)
			}
			query = strings.TrimSpace(query)
			if query != "" {
				params.Q = &query
			}

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			if agent != "" {
				id, err := cli.ResolveAgent(cmd.Context(), api, team, agent)
				if err != nil {
					return err
				}
				params.Agent = &id
			}

			params.Limit, params.Cursor = paging.Values()
			page, err := cli.Result(api.ListImprovementsWithResponse(cmd.Context(), team, params))
			if err != nil {
				return cli.Needs(err, "improvement:approve to see proposals in that state")
			}

			views := make([]view, 0, len(page.Items))
			for i := range page.Items {
				views = append(views, viewOf(&page.Items[i]))
			}
			now := time.Now()
			return cli.PrintPage(opts, cli.Page[view]{
				Items:      views,
				NextCursor: page.NextCursor,
				Empty:      "no improvements",
				More:       "oa improvements list " + args[0] + flagsOf(statuses, agent, query),
				Row: func(table *printer.Table, v view) {
					table.Row(
						opts.Styles.Value.Render(cli.Safe(v.Agent.Name)),
						cli.Safe(v.Kind),
						statusCell(opts, v.Status),
						v.Scope,
						marksCell(v.Marks),
						opts.Styles.Muted.Render(age(now.Sub(v.CreatedAt))),
						cli.Safe(oneLine(v.Why, maxWhy)),
						opts.Styles.Muted.Render(v.ID),
					)
				},
			})
		},
	}

	paging.Flags(cmd)
	cmd.Flags().StringArrayVar(&statuses, "status", nil, "only proposals in this state: "+statusList+"; repeatable")
	cmd.Flags().StringVar(&agent, "agent", "", "only this agent's proposals, by name or id")
	cmd.Flags().StringVar(&query, "query", "", "only proposals whose why or hypothesis contains this text")
	return cmd
}

// flagsOf rebuilds the filters the next-page hint must carry, since the
// cursor alone would silently widen the listing.
func flagsOf(statuses []string, agent, query string) string {
	var b strings.Builder
	for _, s := range statuses {
		b.WriteString(" --status " + s)
	}
	if agent != "" {
		b.WriteString(" --agent " + shellWord(agent))
	}
	if query != "" {
		b.WriteString(" --query " + shellWord(query))
	}
	return b.String()
}

func shellWord(s string) string {
	if strings.ContainsAny(s, " \t'\"\\$`") {
		return "'" + strings.ReplaceAll(cli.Line(s), "'", `'\''`) + "'"
	}
	return cli.Line(s)
}

func newShowCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <team> <proposal>",
		Short: "Show one proposal: why, what it reaches, and what it changes",
		Long: "The why and hypothesis, the marks that say what kind of change it is,\n" +
			"the agents it reaches, the runs it cites, and then each change: a line\n" +
			"diff for text (red - and green +, plain when output is not a terminal),\n" +
			"the names removed and added for a list, before and after for a value.\n\n" +
			"    oa improvements show platform 5d2c8a10-77be-4c1f-8e53-9b0a6f21d4e7",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[1], args[0])
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
			p, err := cli.Result(api.GetImprovementWithResponse(cmd.Context(), team, id))
			if err != nil {
				return err
			}
			return printOne(opts, viewOf(p), "")
		},
	}
}

func newDecisionCmd(
	opts *cli.Options, verb string, action client.ImprovementDecisionAction, did, needs, short, long string,
) *cobra.Command {
	return &cobra.Command{
		Use:   verb + " <team> <proposal>",
		Short: short,
		Long: long + "\n\nNeeds " + needs + ".\n\n" +
			"    oa improvements " + verb + " platform 5d2c8a10-77be-4c1f-8e53-9b0a6f21d4e7",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[1], args[0])
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
			// A 409 is the brain's own plain-text reason, and APIError
			// already prints a 4xx body as the sentence.
			p, err := cli.Result(api.DecideImprovementWithResponse(cmd.Context(), team, id,
				client.DecideImprovementJSONRequestBody{Action: action}))
			if err != nil {
				return cli.Needs(err, needs)
			}
			return printOne(opts, viewOf(p), did)
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
			table.Row(head, opts.Styles.Value.Render(cli.Safe(v.Kind)), opts.Styles.Muted.Render(v.ID))
			table.Row("agent", cli.Safe(v.Agent.Name), "")
			if v.Skill != nil {
				table.Row("skill", cli.Safe(v.Skill.Name), "")
			}
			table.Row("status", statusCell(opts, v.Status), "")
			table.Row("scope", scopeText(v.Scope), "")
			if len(v.Saves) > 0 {
				table.Row("goal", "efficiency: saves "+strings.Join(v.Saves, " and "), "")
			}
			table.Row("marks", marksCell(v.Marks), "")
			if len(v.Reaches) > 0 {
				names := make([]string, 0, len(v.Reaches))
				for _, r := range v.Reaches {
					names = append(names, cli.Safe(r.Name))
				}
				table.Row("reaches", strings.Join(names, ", "), "")
			}
			if v.PassID != nil {
				table.Row("pass", *v.PassID, "")
			}
			table.Row("proposed", v.CreatedAt.Format(stampLayout), "")
			if v.DecidedAt != nil {
				table.Row("decided", v.DecidedAt.Format(stampLayout), "")
			}
			if v.DecidedBy != nil {
				table.Row("by", *v.DecidedBy, "")
			}
			if v.RevertedAt != nil {
				table.Row("reverted", v.RevertedAt.Format(stampLayout), "")
			}
			if v.RevertedBy != nil {
				table.Row("reverted by", *v.RevertedBy, "")
			}
			watchRows(opts, table, v.Watch, v.Goal)
			for i, r := range v.Runs {
				label := ""
				if i == 0 {
					label = "runs"
				}
				cell := r.ID
				if !r.Readable {
					cell += " " + opts.Styles.Muted.Render(unreadableRunNote)
				}
				table.Row(label, cell, "")
			}
		},
	})
	if err != nil {
		return err
	}

	// The prose and the diffs are a model's words, often several lines: under
	// the table, and silent in json and yaml where they are in the document.
	var text strings.Builder
	if v.Outsider {
		text.WriteString("\n" + opts.Styles.Warn.Render(
			"Warning: someone outside the team spoke in a session this proposal came from."))
	}
	section(&text, opts, "Why", v.Why)
	section(&text, opts, "Hypothesis", v.Hypothesis)
	for _, c := range v.Changes {
		text.WriteString("\n\n" + opts.Styles.Label.Render(heading(c.Field)))
		writeChange(&text, opts, c)
	}
	opts.Out.Note(text.String())
	return nil
}

func section(b *strings.Builder, opts *cli.Options, title, body string) {
	b.WriteString("\n\n" + opts.Styles.Label.Render(title))
	for _, l := range cli.Lines(body) {
		b.WriteString("\n  " + l)
	}
}

// heading is what a change is called: a skill's file shows its path alone.
func heading(field string) string {
	if path, ok := strings.CutPrefix(field, filePrefix); ok {
		return path
	}
	return cli.Line(field)
}

func writeChange(b *strings.Builder, opts *cli.Options, c changeView) {
	switch client.ImprovementChangeType(c.Type) {
	case client.Text:
		before, after := text(c.Before), text(c.After)
		for _, d := range lineDiff(splitLines(before), splitLines(after)) {
			switch d.op {
			case opDel:
				b.WriteString("\n" + opts.Styles.Err.Render("- "+d.line))
			case opAdd:
				b.WriteString("\n" + opts.Styles.OK.Render("+ "+d.line))
			case opSame:
				b.WriteString("\n  " + d.line)
			}
		}
	case client.List:
		before, after := names(c.Before), names(c.After)
		removed, added := minus(before, after), minus(after, before)
		for _, n := range removed {
			b.WriteString("\n" + opts.Styles.Err.Render("- "+n))
		}
		for _, n := range added {
			b.WriteString("\n" + opts.Styles.OK.Render("+ "+n))
		}
		if len(removed)+len(added) == 0 {
			b.WriteString("\n  " + opts.Styles.Muted.Render("no names change"))
		}
	case client.Value:
		b.WriteString("\n  " + show(c.Before) + " → " + show(c.After))
	default:
		b.WriteString("\n  " + show(c.Before) + " → " + show(c.After))
	}
}

// text is a text change's side: null is a file that was not there.
func text(v any) string {
	s, _ := v.(string)
	return s
}

func names(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, cli.Line(s))
		}
	}
	return out
}

// minus is what is in a and not in b, in a's order.
func minus(a, b []string) []string {
	in := make(map[string]bool, len(b))
	for _, s := range b {
		in[s] = true
	}
	out := make([]string, 0, len(a))
	for _, s := range a {
		if !in[s] {
			out = append(out, s)
		}
	}
	return out
}

func show(v any) string {
	switch x := v.(type) {
	case nil:
		return "(none)"
	case string:
		return cli.Line(x)
	case float64:
		return fmt.Sprintf("%g", x)
	default:
		return cli.Line(fmt.Sprint(x))
	}
}
