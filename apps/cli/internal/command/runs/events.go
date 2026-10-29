package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

const (
	maxCell        = 120
	maxErrorBody   = 4 << 10
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
// transcript whatever the kind. Absent fields stay zero.
type payload struct {
	ID                string   `json:"id"`
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

	Items []planEntry `json:"items"`
}

// planEntry is one step of the hub's plan, as its plan event carries it.
type planEntry struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

// follow is how a run's events are printed: from where, with how much detail,
// and whether something is already on screen above them.
type follow struct {
	after      *int32
	verbose    bool
	afterStart bool
}

// followRun opens its own client rather than taking the caller's: the
// ordinary one gives up after 30 seconds, which is shorter than most runs.
//
// A table is a transcript, written straight to the terminal so a call can
// show it is running and be replaced by its result. json and yaml are one
// record per event, exactly as the brain recorded it.
func followRun(ctx context.Context, opts *cli.Options, team, run uuid.UUID, teamRef string, how follow) error {
	api, err := opts.Stream(ctx)
	if err != nil {
		return err
	}

	res, err := api.StreamRunEvents(ctx, team, run, &client.StreamRunEventsParams{After: how.after})
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

	var shown *transcript
	if format, _ := output.Parse(opts.Settings.Output.Value); !format.IsMachine() {
		shown = newTranscript(opts.Stdout, opts.Styles, how.verbose, teamRef)
		shown.printed = how.afterStart
	}

	stream := newFrames(res.Body)
	last := ""
	failure := ""
	for {
		f, err := stream.next()
		if err != nil {
			if shown != nil {
				shown.close()
			}
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

		if shown != nil {
			err = shown.event(f, p)
		} else {
			err = printEvent(opts, f)
		}
		if err != nil {
			return err
		}

		if f.event == endEvent {
			if shown != nil {
				if err := finish(ctx, shown, api, team, run, teamRef, p.Status, failure); err != nil {
					return err
				}
			}
			return ended(p.Status, failure, teamRef, run)
		}
	}
}

// finish ends the transcript. The finished event says how the run ended but
// not what it answered, so a run that succeeded is read once more for that.
// Not being able to read it costs the answer on screen, not the exit status:
// the run still succeeded.
func finish(ctx context.Context, shown *transcript, api *client.ClientWithResponses,
	team, run uuid.UUID, teamRef, status, failure string,
) error {
	if client.RunStatus(status) != client.Succeeded {
		return shown.finish(status, failure, nil, "")
	}
	got, err := cli.Result(api.GetRunWithResponse(ctx, team, run))
	if err != nil {
		return shown.finish(status, failure, nil,
			"the answer could not be read — `oa runs get "+cli.Line(teamRef)+" "+run.String()+"` shows it")
	}
	return shown.finish(status, failure, got, "")
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

func printEvent(opts *cli.Options, f frame) error {
	view := eventView{Event: f.event, Data: decode(f.data)}
	if n, err := strconv.ParseInt(f.id, 10, 64); err == nil {
		view.ID = &n
	}
	return opts.Out.Line(view, printer.Options{})
}

func decode(data string) any {
	var v any
	if err := json.Unmarshal([]byte(data), &v); err != nil {
		return data
	}
	return v
}

func clip(s string) string {
	return clipRunes(s, maxCell)
}
