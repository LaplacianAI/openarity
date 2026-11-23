package improvements

import (
	"fmt"
	"strings"
	"time"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

// watchView is how an applied change went, as the CLI prints it.
type watchView struct {
	Status       string     `json:"status" yaml:"status"`
	Flag         *string    `json:"flag" yaml:"flag"`
	EndedWhy     *string    `json:"ended_why" yaml:"ended_why"`
	Needed       int        `json:"needed" yaml:"needed"`
	BaselineRuns int        `json:"baseline_runs" yaml:"baseline_runs"`
	BaselineBad  int        `json:"baseline_bad" yaml:"baseline_bad"`
	Bar          *int       `json:"bar" yaml:"bar"`
	BaselineP90  *int64     `json:"baseline_p90" yaml:"baseline_p90"`
	Before       sideView   `json:"before" yaml:"before"`
	After        sideView   `json:"after" yaml:"after"`
	Overlapping  []string   `json:"overlapping" yaml:"overlapping"`
	StartedAt    time.Time  `json:"started_at" yaml:"started_at"`
	FinishedAt   *time.Time `json:"finished_at" yaml:"finished_at"`
	ClearedBy    *string    `json:"cleared_by" yaml:"cleared_by"`
	ClearedAt    *time.Time `json:"cleared_at" yaml:"cleared_at"`
}

type sideView struct {
	Runs      int    `json:"runs" yaml:"runs"`
	Bad       int    `json:"bad" yaml:"bad"`
	Up        int    `json:"up" yaml:"up"`
	Down      int    `json:"down" yaml:"down"`
	Tokens    *int64 `json:"tokens" yaml:"tokens"`
	Steps     *int   `json:"steps" yaml:"steps"`
	Signature *int   `json:"signature" yaml:"signature"`
	WorkMS    *int64 `json:"work_ms" yaml:"work_ms"`
}

func sideOf(s client.ImprovementWatchSide) sideView {
	return sideView{Runs: s.Runs, Bad: s.Bad, Up: s.Up, Down: s.Down, Tokens: s.Tokens, Steps: s.Steps, Signature: s.Signature, WorkMS: s.WorkMs}
}

func watchOf(w *client.ImprovementWatch) *watchView {
	if w == nil {
		return nil
	}
	v := &watchView{
		Status: string(w.Status), Needed: w.Needed, BaselineRuns: w.BaselineRuns, BaselineBad: w.BaselineBad,
		Bar: w.Bar, BaselineP90: w.BaselineP90, Before: sideOf(w.Before), After: sideOf(w.After),
		Overlapping: make([]string, 0, len(w.Overlapping)), StartedAt: w.StartedAt, FinishedAt: w.FinishedAt,
		ClearedBy: idOrNil(w.ClearedBy), ClearedAt: w.ClearedAt,
	}
	if w.Flag != nil {
		f := string(*w.Flag)
		v.Flag = &f
	}
	if w.EndedWhy != nil {
		e := string(*w.EndedWhy)
		v.EndedWhy = &e
	}
	for _, id := range w.Overlapping {
		v.Overlapping = append(v.Overlapping, id.String())
	}
	return v
}

// flagWords is why a change was flagged, in words.
func flagWords(flag *string) string {
	if flag == nil {
		return ""
	}
	switch *flag {
	case "worse":
		return "its runs went worse"
	case "costlier":
		return "its runs cost more, with no fewer failures"
	case "both":
		return "its runs went worse and cost more"
	case "no_saving":
		return "its good runs did not get cheaper or quicker, as it said they would"
	}
	return cli.Safe(*flag)
}

// watchLine is the watch's state in one line: what it is, and the counts.
func watchLine(w *watchView) string {
	counts := fmt.Sprintf("%d of %d went wrong after, %d of %d before", w.After.Bad, w.After.Runs, w.Before.Bad, w.Before.Runs)
	switch w.Status {
	case "watching":
		return fmt.Sprintf("watching: %d of %d runs so far", w.After.Runs, w.Needed)
	case "clear":
		return "clear: " + counts
	case "flagged":
		return "flagged, " + flagWords(w.Flag) + ": " + counts
	case "cleared":
		return "flagged (" + flagWords(w.Flag) + "), then cleared: " + counts
	case "ended":
		if w.EndedWhy != nil && *w.EndedWhy == "reverted" {
			return fmt.Sprintf("ended when reverted: %d of %d runs counted", w.After.Runs, w.Needed)
		}
		return fmt.Sprintf("ended with too few runs: %d of %d in 30 days", w.After.Runs, w.Needed)
	}
	return cli.Safe(w.Status)
}

// usual is the agent's usual rate before the change and the bar it sets.
func usual(w *watchView) string {
	if w.Bar == nil {
		return fmt.Sprintf("too little history to judge against (%d runs before)", w.BaselineRuns)
	}
	return fmt.Sprintf("%d of %d went wrong before; flagged at %d of %d", w.BaselineBad, w.BaselineRuns, *w.Bar, w.Needed)
}

func median(v *int64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprint(*v)
}

func medianInt(v *int) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprint(*v)
}

// watchRows prints the watch under the proposal: its state, the agent's
// usual rate, cost, the targeted signature and the changes beside it.
func watchRows(opts *cli.Options, table *printer.Table, w *watchView, goal string) {
	if w == nil {
		return
	}
	line := watchLine(w)
	if w.Status == "flagged" {
		line = opts.Styles.Warn.Render(line)
	}
	table.Row("watch", line, "")
	table.Row("usual", usual(w), "")
	// An efficiency change is judged on its good runs, so its tokens are
	// theirs.
	a := "a run"
	if goal == string(client.ImprovementGoalEfficiency) {
		a = "a good run"
	}
	if w.Before.Tokens != nil || w.After.Tokens != nil {
		table.Row("cost", fmt.Sprintf("tokens %s %s → %s, steps %s → %s",
			a, median(w.Before.Tokens), median(w.After.Tokens), medianInt(w.Before.Steps), medianInt(w.After.Steps)), "")
	}
	if w.Before.WorkMS != nil || w.After.WorkMS != nil {
		table.Row("time", fmt.Sprintf("work a good run %s → %s", secondsOf(w.Before.WorkMS), secondsOf(w.After.WorkMS)), "")
	}
	if w.Before.Signature != nil && w.After.Signature != nil {
		table.Row("pattern", fmt.Sprintf("in %d of %d runs before, %d of %d after",
			*w.Before.Signature, w.Before.Runs, *w.After.Signature, w.After.Runs), "")
	}
	if w.Before.Up+w.Before.Down+w.After.Up+w.After.Down > 0 {
		table.Row("votes", fmt.Sprintf("before 👍 %d 👎 %d, after 👍 %d 👎 %d", w.Before.Up, w.Before.Down, w.After.Up, w.After.Down), "")
	}
	if len(w.Overlapping) > 0 {
		table.Row("beside", strings.Join(w.Overlapping, ", "), "")
	}
}

// secondsOf is a work time in seconds, "—" for none.
func secondsOf(ms *int64) string {
	if ms == nil {
		return "—"
	}
	return fmt.Sprintf("%.1f s", float64(*ms)/1000)
}
