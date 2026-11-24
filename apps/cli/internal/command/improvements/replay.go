package improvements

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

// replayView is how a proposal fared on past runs before it was decided.
type replayView struct {
	Status string          `json:"status" yaml:"status"`
	Runs   []replayRunView `json:"runs" yaml:"runs"`
}

type costView struct {
	Tokens int64 `json:"tokens" yaml:"tokens"`
	Steps  int32 `json:"steps" yaml:"steps"`
	WorkMS int64 `json:"work_ms" yaml:"work_ms"`
}

type replayRunView struct {
	Run      string    `json:"run" yaml:"run"`
	Readable bool      `json:"readable" yaml:"readable"`
	Chosen   string    `json:"chosen" yaml:"chosen"`
	Status   string    `json:"status" yaml:"status"`
	SkipWhy  *string   `json:"skip_why" yaml:"skip_why"`
	Error    *string   `json:"error" yaml:"error"`
	Original string    `json:"original" yaml:"original"`
	Replayed *string   `json:"replayed" yaml:"replayed"`
	Outcome  *string   `json:"outcome" yaml:"outcome"`
	Verdict  *string   `json:"verdict" yaml:"verdict"`
	Why      *string   `json:"why" yaml:"why"`
	Request  *string   `json:"request" yaml:"request"`
	Withheld bool      `json:"withheld" yaml:"withheld"`
	Diverged bool      `json:"diverged" yaml:"diverged"`
	Recorded int32     `json:"recorded" yaml:"recorded"`
	Live     int32     `json:"live" yaml:"live"`
	NotMade  int32     `json:"not_made" yaml:"not_made"`
	Before   costView  `json:"before" yaml:"before"`
	After    *costView `json:"after" yaml:"after"`
}

func replayOf(r *client.ImprovementReplay) *replayView {
	if r == nil {
		return nil
	}
	v := &replayView{Status: string(r.Status), Runs: make([]replayRunView, 0, len(r.Runs))}
	for _, run := range r.Runs {
		rv := replayRunView{
			Run: run.Run.ID.String(), Readable: run.Run.Readable, Chosen: string(run.Chosen), Status: string(run.Status),
			SkipWhy: run.SkipWhy, Error: run.Error, Original: string(run.Original), Verdict: run.Verdict, Why: run.Why,
			Request: run.Request, Withheld: run.Withheld, Diverged: run.Diverged,
			Recorded: run.Recorded, Live: run.Live, NotMade: run.NotMade,
			Before: costView{Tokens: run.Before.Tokens, Steps: run.Before.Steps, WorkMS: run.Before.WorkMs},
		}
		if run.Replayed != nil {
			s := string(*run.Replayed)
			rv.Replayed = &s
		}
		if run.Outcome != nil {
			o := string(*run.Outcome)
			rv.Outcome = &o
		}
		if run.After != nil {
			rv.After = &costView{Tokens: run.After.Tokens, Steps: run.After.Steps, WorkMS: run.After.WorkMs}
		}
		v.Runs = append(v.Runs, rv)
	}
	return v
}

// What a done try made of its run, as the brain decides it.
const (
	outcomeFixed    = string(client.Fixed)
	outcomeKept     = string(client.Kept)
	outcomeBroke    = string(client.Broke)
	outcomeStillBad = string(client.StillBad)
)

// outcomeWords is what a try came to beside its run: "now done" for one
// that went wrong and now goes well, "now goes wrong" for the reverse.
func outcomeWords(r replayRunView) string {
	switch r.Status {
	case "queued", "trying":
		return "still to try"
	case "skipped":
		return "not tried"
	case "failed":
		return "the try failed"
	}
	switch outcome(r) {
	case outcomeFixed:
		return "went wrong, now done"
	case outcomeStillBad:
		return "went wrong, still does"
	case outcomeKept:
		return "went well, still does"
	case outcomeBroke:
		return "went well, now goes wrong"
	}
	return "could not tell"
}

func outcome(r replayRunView) string {
	if r.Outcome == nil {
		return ""
	}
	return *r.Outcome
}

// worse is whether a run that went well goes wrong with the change.
func (v *replayView) worse() bool {
	for _, r := range v.Runs {
		if outcome(r) == outcomeBroke {
			return true
		}
	}
	return false
}

// summary is the tries in one line: "tried on 4 past runs: 2 that went
// wrong now done · 2 that went well still do"; while some are still to run,
// how many of them are done.
func (v *replayView) summary() string {
	tried, total := 0, 0
	counts := map[string]int{}
	for _, r := range v.Runs {
		if r.Status != "skipped" {
			total++
		}
		if r.Status != "done" {
			continue
		}
		tried++
		counts[outcome(r)]++
	}
	switch v.Status {
	case "off":
		return "not tried: trying on past runs was off"
	case "trying":
		if tried > 0 {
			return fmt.Sprintf("trying on past runs… %d of %d tried", tried, total)
		}
		return "trying on past runs…"
	case "none":
		return "none of its past runs could be tried"
	}
	var parts []string
	for _, p := range []struct {
		n    int
		what string
	}{
		{counts[outcomeFixed], "that went wrong now done"},
		{counts[outcomeStillBad], "that went wrong still do"},
		{counts[outcomeKept], "that went well still do"},
		{counts[outcomeBroke], "that went well now go wrong"},
		{tried - counts[outcomeFixed] - counts[outcomeStillBad] - counts[outcomeKept] - counts[outcomeBroke], "could not tell"},
	} {
		if p.n > 0 {
			parts = append(parts, strconv.Itoa(p.n)+" "+p.what)
		}
	}
	return fmt.Sprintf("tried on %d past run%s: %s", tried, plural(tried), strings.Join(parts, " · "))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// costLine is a try's cost beside its run's: tokens, steps and time.
func costLine(r replayRunView) string {
	if r.After == nil {
		return ""
	}
	return fmt.Sprintf("tokens %d → %d · steps %d → %d · time %s → %s",
		r.Before.Tokens, r.After.Tokens, r.Before.Steps, r.After.Steps,
		secondsOf(&r.Before.WorkMS), secondsOf(&r.After.WorkMS))
}

// replayRows prints the tries under a proposal: the summary, then one row a
// past run.
func replayRows(opts *cli.Options, table *printer.Table, v *replayView) {
	// A team that has trying off sees nothing of it.
	if v == nil || v.Status == "off" {
		return
	}
	head := v.summary()
	if v.worse() {
		head = opts.Styles.Warn.Render("worse on past runs") + " · " + head
	}
	table.Row("past runs", head, "")
	for _, r := range v.Runs {
		what := opts.Styles.Muted.Render(unreadableRunNote)
		if r.Request != nil {
			what = oneLine(cli.Safe(*r.Request), 60)
		}
		cell := what + " · " + outcomeWords(r)
		if line := costLine(r); line != "" {
			cell += " · " + line
		}
		if r.Diverged {
			cell += fmt.Sprintf(" · went a different way: %d call%s not made", r.NotMade, plural(int(r.NotMade)))
		}
		if r.SkipWhy != nil {
			cell += " · " + cli.Safe(*r.SkipWhy)
		}
		table.Row("", cell, opts.Styles.Muted.Render(r.Run))
	}
}

func newReplayCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "replay <team> <proposal> <run>",
		Short: "Show one try of a proposal on a past run: each call, and the answer",
		Long: "Before a proposal is decided, a team with trying on past runs on has it\n" +
			"tried on a few of its agent's past runs, sending and changing nothing.\n" +
			"Each call the try made is shown with how it was served: recorded (the\n" +
			"past run's same call, its result given back), live (a new call that\n" +
			"only reads, made for real) or not made (anything else). Only someone\n" +
			"who may read the past run's conversation can see it.\n\n" +
			"    oa improvements replay platform 5d2c8a10-77be-4c1f-8e53-9b0a6f21d4e7 1b7e…",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[1], args[0])
			if err != nil {
				return err
			}
			run, err := parseID(args[2], args[0])
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
			tr, err := cli.Result(api.GetImprovementReplayWithResponse(cmd.Context(), team, id, run))
			if err != nil {
				return err
			}
			return printTrace(opts, tr)
		},
	}
}

var howWords = map[client.ImprovementReplayCallHow]string{
	client.Recorded: "recorded",
	client.Live:     "live",
	client.NotMade:  "not made",
}

func printTrace(opts *cli.Options, tr *client.ImprovementReplayTrace) error {
	err := opts.Out.Print(tr, printer.Options{
		Table: func(table *printer.Table) {
			label := opts.Styles.Label.Render
			table.Row(label("CALL"), label("SERVED"), label("ARGUMENTS"), label("RESULT"))
			for _, c := range tr.Calls {
				result := ""
				switch {
				case c.Error != nil:
					result = opts.Styles.Err.Render(oneLine(cli.Safe(*c.Error), 60))
				case c.Output != nil:
					result = oneLine(cli.Safe(*c.Output), 60)
				}
				table.Row(cli.Safe(c.Name), howWords[c.How], oneLine(cli.Safe(c.Arguments), 40), result)
			}
		},
	})
	if err != nil {
		return err
	}
	if tr.Answer != nil {
		var b strings.Builder
		section(&b, opts, "Answer", *tr.Answer)
		opts.Out.Note(b.String())
	}
	return nil
}
