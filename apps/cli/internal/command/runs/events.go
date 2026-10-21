package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

const (
	maxCell        = 120
	maxErrorBody   = 4 << 10
	kindWidth      = 16
	seqWidth       = 4
	endEvent       = "end"
	finishedEvent  = "finished"
	unsetEventName = "message"
)

// eventView is one frame as a script reads it. data is decoded rather than
// carried as raw bytes so yaml prints a mapping and not a byte list; a frame
// whose data is not JSON keeps it as a string.
type eventView struct {
	ID    *int64 `json:"id,omitempty" yaml:"id,omitempty"`
	Event string `json:"event" yaml:"event"`
	Data  any    `json:"data" yaml:"data"`
}

// payload is every field any event kind carries, so one decode serves the
// table whatever the kind. Absent fields stay zero.
type payload struct {
	Agent             string   `json:"agent"`
	Model             string   `json:"model"`
	Pattern           string   `json:"pattern"`
	Tools             []string `json:"tools"`
	Skills            []string `json:"skills"`
	Messages          int      `json:"messages"`
	Name              string   `json:"name"`
	Reason            string   `json:"reason"`
	Step              int      `json:"step"`
	Arguments         string   `json:"arguments"`
	Output            string   `json:"output"`
	Error             string   `json:"error"`
	Truncated         bool     `json:"truncated"`
	DurationMS        int64    `json:"duration_ms"`
	InputTokens       int64    `json:"input_tokens"`
	OutputTokens      int64    `json:"output_tokens"`
	CachedInputTokens int64    `json:"cached_input_tokens"`
	Spent             int64    `json:"spent"`
	Status            string   `json:"status"`
}

// followRun opens its own client rather than taking the caller's: the
// ordinary one gives up after 30 seconds, which is shorter than most runs.
func followRun(ctx context.Context, opts *cli.Options, team, run uuid.UUID, teamRef string, after *int32) error {
	api, err := opts.Stream(ctx)
	if err != nil {
		return err
	}

	res, err := api.StreamRunEvents(ctx, team, run, &client.StreamRunEventsParams{After: after})
	if err != nil {
		if ctx.Err() != nil {
			return stopped(teamRef, run, "")
		}
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
		return cli.APIError(res.StatusCode, body)
	}

	stream := newFrames(res.Body)
	last := ""
	failure := ""
	for {
		f, err := stream.next()
		if err != nil {
			if ctx.Err() != nil {
				return stopped(teamRef, run, last)
			}
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("the stream closed before the run ended — `%s` picks it up again",
					resume(teamRef, run, last))
			}
			return fmt.Errorf("read the run's events: %w", err)
		}

		if f.id != "" {
			last = f.id
		}
		if f.event == "" {
			f.event = unsetEventName
		}

		var p payload
		_ = json.Unmarshal([]byte(f.data), &p)
		if f.event == finishedEvent {
			failure = p.Error
		}

		if err := printEvent(opts, f, p); err != nil {
			return err
		}
		if f.event == endEvent {
			return ended(p.Status, failure, teamRef, run)
		}
	}
}

func stopped(teamRef string, run uuid.UUID, last string) error {
	return fmt.Errorf("stopped following — the run carries on; `%s` picks it up again", resume(teamRef, run, last))
}

func resume(teamRef string, run uuid.UUID, last string) string {
	command := "oa runs events " + teamRef + " " + run.String()
	if _, err := strconv.ParseInt(last, 10, 32); err == nil {
		command += " --after " + last
	}
	return command
}

// ended turns how the run finished into the exit status: a script that
// follows a run is waiting to know whether it worked.
func ended(status, failure, teamRef string, run uuid.UUID) error {
	switch client.RunStatus(status) {
	case client.Succeeded:
		return nil
	case client.Failed:
		if failure != "" {
			return fmt.Errorf("the run failed: %s", cli.Safe(clip(failure)))
		}
		return fmt.Errorf("the run failed — `oa runs get %s %s` says why", teamRef, run)
	case client.Cancelled:
		return errors.New("the run was cancelled")
	case client.Queued, client.Running:
		return fmt.Errorf("the stream ended with the run still %s", status)
	default:
		return fmt.Errorf("the run ended with a status this oa does not know: %s", cli.Safe(status))
	}
}

func printEvent(opts *cli.Options, f frame, p payload) error {
	view := eventView{Event: f.event, Data: decode(f.data)}
	if n, err := strconv.ParseInt(f.id, 10, 64); err == nil {
		view.ID = &n
	}

	return opts.Out.Line(view, printer.Options{
		Table: func(table *printer.Table) {
			text, failed := summary(f.event, p, f.data)
			if failed {
				text = opts.Styles.Err.Render(text)
			}
			table.Row(
				opts.Styles.Muted.Render(pad(cli.Safe(f.id), seqWidth)),
				opts.Styles.Value.Render(pad(cli.Safe(f.event), kindWidth)),
				text,
			)
		},
	})
}

func decode(data string) any {
	var v any
	if err := json.Unmarshal([]byte(data), &v); err != nil {
		return data
	}
	return v
}

// summary is one event as a line a person reads. Every value in it came from
// the run — a tool's output, a model's arguments — so each is clipped and
// quoted on its own; -o json has them whole and unquoted.
func summary(kind string, p payload, raw string) (string, bool) {
	safe := func(s string) string { return cli.Safe(clip(s)) }
	cut := ""
	if p.Truncated {
		cut = " (truncated by the brain)"
	}

	switch kind {
	case "started":
		text := safe(p.Agent) + " on " + safe(p.Model) + ", " + safe(p.Pattern)
		if len(p.Tools) > 0 {
			text += " — tools: " + safe(strings.Join(p.Tools, ", "))
		}
		if len(p.Skills) > 0 {
			text += " — skills: " + safe(strings.Join(p.Skills, ", "))
		}
		return text, false
	case "resumed":
		return "after " + strconv.Itoa(p.Messages) + " messages", false
	case "step":
		return strconv.Itoa(p.Step), false
	case "tool_call":
		return safe(p.Name) + " " + safe(p.Arguments) + cut, false
	case "tool_result":
		took := " (" + strconv.FormatInt(p.DurationMS, 10) + "ms)"
		if p.Error != "" {
			return safe(p.Name) + " failed: " + safe(p.Error) + took + cut, true
		}
		return safe(p.Name) + " → " + safe(p.Output) + took + cut, false
	case "tool_unavailable":
		return safe(p.Name) + ": " + safe(p.Reason), true
	case "usage":
		return fmt.Sprintf("%s %d in, %d out, %d cached — %d spent",
			safe(p.Model), p.InputTokens, p.OutputTokens, p.CachedInputTokens, p.Spent), false
	case finishedEvent, endEvent:
		text := safe(p.Status)
		if p.Error != "" {
			text += ": " + safe(p.Error)
		}
		return text, client.RunStatus(p.Status) != client.Succeeded
	default:
		return safe(raw), false
	}
}

func clip(s string) string {
	if utf8.RuneCountInString(s) <= maxCell {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxCell]) + "…"
}

func pad(s string, width int) string {
	if n := utf8.RuneCountInString(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}
