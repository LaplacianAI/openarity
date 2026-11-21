package improve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// Reads is what building the evidence reads.
type Reads interface {
	GetRun(ctx context.Context, id uuid.UUID) (db.Run, error)
	GetRunFinishedSeq(ctx context.Context, runID uuid.UUID) (*int64, error)
	ListMessagesBefore(ctx context.Context, arg db.ListMessagesBeforeParams) ([]db.ListMessagesBeforeRow, error)
	ListMessagesAfter(ctx context.Context, arg db.ListMessagesAfterParams) ([]db.ListMessagesAfterRow, error)
	ListTreeRuns(ctx context.Context, rootRunID uuid.UUID) ([]db.ListTreeRunsRow, error)
	ListTreeApprovals(ctx context.Context, rootRunID uuid.UUID) ([]db.ListTreeApprovalsRow, error)
	ListRunEvents(ctx context.Context, arg db.ListRunEventsParams) ([]db.RunEvent, error)
	SessionHasOutsider(ctx context.Context, arg db.SessionHasOutsiderParams) (bool, error)
}

const (
	before        = 6     // messages before the run, for context
	after         = 2     // people's messages after it
	argChars      = 300   // a tool call's arguments, and its result
	answerChars   = 6000  // the root run's answer
	childChars    = 1500  // a delegate's input and answer, in a digest
	eventsPerRun  = 2000  // the trace read of one run
	checkBudget   = 24000 // characters a check reads
	digestBudget  = 48000 // and a digest
	contextChars  = 1000  // one message of context
	nextChars     = 1500  // one message after
	requestChars  = 4000  // the request
	errorChars    = 600   // a run's error
	traceLineMins = 40    // the trace is cut to this many lines before any fewer
)

// evidence is one run tree as a model reads it, in parts so the whole can
// be cut to a budget from its least important end.
type evidence struct {
	root     db.Run
	context  []string // oldest first
	request  string
	trace    []string
	answer   string
	ending   string
	next     []string
	runs     []db.ListTreeRunsRow
	held     map[uuid.UUID]held // what each run of the tree held
	outsider bool
}

// held is what a run was built with: the skills and tools a digest's
// target must name.
type held struct {
	skills []string
	tools  []string
}

type startedPayload struct {
	Tools  []string `json:"tools"`
	Skills []string `json:"skills"`
}

type toolCallPayload struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type toolResultPayload struct {
	Name   string `json:"name"`
	Output string `json:"output"`
	Error  string `json:"error"`
}

type stepPayload struct {
	Step int `json:"step"`
}

type namedReason struct {
	Name   string `json:"name"`
	What   string `json:"what"`
	Reason string `json:"reason"`
}

type delegatedPayload struct {
	Agent string    `json:"agent"`
	RunID uuid.UUID `json:"run_id"`
}

// gather reads one run tree. full is a digest's read: every delegate's
// input, steps and answer besides the root's.
func gather(ctx context.Context, r Reads, runID uuid.UUID, full bool) (evidence, error) {
	root, err := r.GetRun(ctx, runID)
	if err != nil {
		return evidence{}, err
	}
	ev := evidence{root: root, held: map[uuid.UUID]held{}}

	seq, err := r.GetRunFinishedSeq(ctx, runID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return evidence{}, fmt.Errorf("reading where the run finished: %w", err)
	}
	if seq != nil {
		prior, err := r.ListMessagesBefore(ctx, db.ListMessagesBeforeParams{SessionID: root.SessionID, Before: *seq, PageSize: before + 1})
		if err != nil {
			return evidence{}, fmt.Errorf("reading the conversation before the run: %w", err)
		}
		slices.Reverse(prior)
		for _, m := range prior {
			ev.context = append(ev.context, speaker(m.SenderName)+": "+clip(m.Text, contextChars))
		}
		later, err := r.ListMessagesAfter(ctx, db.ListMessagesAfterParams{SessionID: root.SessionID, After: *seq, PageSize: after})
		if err != nil {
			return evidence{}, fmt.Errorf("reading what came after the run: %w", err)
		}
		for _, m := range later {
			ev.next = append(ev.next, speaker(m.SenderName)+": "+clip(m.Text, nextChars))
		}
	}

	ev.runs, err = r.ListTreeRuns(ctx, runID)
	if err != nil {
		return evidence{}, fmt.Errorf("reading the run's tree: %w", err)
	}
	for _, run := range ev.runs {
		if run.ID != runID && !full {
			continue
		}
		lines, h, err := traceOf(ctx, r, run)
		if err != nil {
			return evidence{}, err
		}
		ev.held[run.ID] = h
		ev.trace = append(ev.trace, lines...)
	}

	approvals, err := r.ListTreeApprovals(ctx, runID)
	if err != nil {
		return evidence{}, fmt.Errorf("reading the run's approvals: %w", err)
	}
	for _, a := range approvals {
		if a.Status == "denied" || a.Status == "expired" {
			ev.trace = append(ev.trace, fmt.Sprintf("a person was asked to allow %s (%s): %s", a.Tool, clip(a.Summary, 200), a.Status))
		}
	}

	ev.request = clip(root.Input, requestChars)
	if root.Output != nil {
		ev.answer = clip(*root.Output, answerChars)
	}
	ev.ending = "status " + root.Status
	if root.Error != nil && *root.Error != "" {
		ev.ending += "; error: " + clip(*root.Error, errorChars)
	}
	ev.ending += fmt.Sprintf("; tokens in %d, out %d", root.InputTokens, root.OutputTokens)

	ev.outsider, err = r.SessionHasOutsider(ctx, db.SessionHasOutsiderParams{SessionID: root.SessionID, TeamID: root.TeamID})
	if err != nil {
		return evidence{}, fmt.Errorf("reading who spoke in the session: %w", err)
	}
	return ev, nil
}

func speaker(name string) string {
	if strings.TrimSpace(name) == "" {
		return "person"
	}
	return name
}

// traceOf is one run's steps as lines, and what it was built with.
func traceOf(ctx context.Context, r Reads, run db.ListTreeRunsRow) ([]string, held, error) {
	events, err := r.ListRunEvents(ctx, db.ListRunEventsParams{RunID: run.ID, After: 0, PageSize: eventsPerRun})
	if err != nil {
		return nil, held{}, fmt.Errorf("reading run %s's events: %w", run.ID, err)
	}
	who := run.AgentName
	lines := []string{}
	if run.Depth > 0 {
		lines = append(lines, fmt.Sprintf("[%s was handed: %s]", who, clip(run.Input, childChars)))
	}
	var h held
	for _, e := range events {
		switch e.Kind {
		case "started":
			var p startedPayload
			if json.Unmarshal(e.Payload, &p) == nil {
				h = held{skills: p.Skills, tools: p.Tools}
				lines = append(lines, fmt.Sprintf("%s started with tools [%s] and skills [%s]",
					who, strings.Join(p.Tools, ", "), strings.Join(p.Skills, ", ")))
			}
		case "step":
			var p stepPayload
			if json.Unmarshal(e.Payload, &p) == nil {
				lines = append(lines, fmt.Sprintf("%s step %d", who, p.Step))
			}
		case "tool_call":
			var p toolCallPayload
			if json.Unmarshal(e.Payload, &p) == nil {
				lines = append(lines, fmt.Sprintf("  %s calls %s(%s)", who, p.Name, clip(p.Arguments, argChars)))
			}
		case "tool_result":
			var p toolResultPayload
			if json.Unmarshal(e.Payload, &p) == nil {
				if p.Error != "" {
					lines = append(lines, fmt.Sprintf("  %s failed: %s", p.Name, clip(p.Error, argChars)))
				} else {
					lines = append(lines, fmt.Sprintf("  %s returned: %s", p.Name, clip(p.Output, argChars)))
				}
			}
		case "tool_unavailable", "skipped":
			var p namedReason
			if json.Unmarshal(e.Payload, &p) == nil {
				lines = append(lines, fmt.Sprintf("  %s could not %s: %s", who, firstOf(p.Name, p.What), clip(p.Reason, argChars)))
			}
		case "delegated":
			var p delegatedPayload
			if json.Unmarshal(e.Payload, &p) == nil {
				lines = append(lines, fmt.Sprintf("  %s handed work to %s", who, p.Agent))
			}
		}
	}
	if run.Depth > 0 {
		end := "status " + run.Status
		if run.Output != nil && *run.Output != "" {
			end += "; answered: " + clip(*run.Output, childChars)
		}
		if run.Error != nil && *run.Error != "" {
			end += "; error: " + clip(*run.Error, errorChars)
		}
		lines = append(lines, fmt.Sprintf("[%s ended: %s]", who, end))
	}
	return lines, h, nil
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// render is the evidence as the model reads it, within budget characters,
// with any extra sections after it. What goes first is the least needed:
// the oldest context, then the middle of the trace, down to none of it.
// The request, the answer, how it ended and what came next are kept.
func (ev evidence) render(budget int, extra ...section) string {
	ctxLines, keep := ev.context, len(ev.trace)
	for {
		text := ev.text(ctxLines, cut(ev.trace, keep), extra)
		if utf8.RuneCountInString(text) <= budget {
			return text
		}
		switch {
		case len(ctxLines) > 0:
			ctxLines = ctxLines[1:]
		case keep > traceLineMins:
			keep = max(traceLineMins, keep*3/4)
		case keep > 0:
			keep = 0
		default:
			return clip(text, budget) // the kept parts are clipped each, so this is a backstop
		}
	}
}

// cut keeps keep lines of a trace, the first and last halves, and says how
// many were left out between them.
func cut(lines []string, keep int) []string {
	if keep >= len(lines) {
		return lines
	}
	head := keep / 2
	return slices.Concat(lines[:head],
		[]string{fmt.Sprintf("  … %d lines left out …", len(lines)-keep)},
		lines[len(lines)-(keep-head):])
}

// section is one part of what the model reads. Its title is ours; its
// lines are evidence, written by people, agents and tools.
type section struct {
	title string
	lines []string
}

// evidenceMark starts every line of evidence, so nothing in it can pass for
// one of the section headings: the prompts say only an unmarked "## " line
// is a heading.
const evidenceMark = "| "

func (ev evidence) text(ctxLines, trace []string, extra []section) string {
	answer := ev.answer
	if answer == "" {
		answer = "(no answer)"
	}
	parts := append([]section{
		{"Conversation before", ctxLines},
		{"Request", []string{ev.request}},
		{"What the agent did", trace},
		{"Answer", []string{answer}},
		{"How it ended", []string{ev.ending}},
		{"What people said next", ev.next},
	}, extra...)
	var b strings.Builder
	for _, p := range parts {
		writeSection(&b, p)
	}
	return b.String()
}

// writeSection writes one section, its lines marked as evidence; a section
// with no lines is left out.
func writeSection(b *strings.Builder, p section) {
	lines := slices.DeleteFunc(slices.Clone(p.lines), func(l string) bool { return l == "" })
	if len(lines) == 0 {
		return
	}
	b.WriteString("## " + p.title + "\n")
	for _, l := range lines {
		for line := range strings.SplitSeq(l, "\n") {
			b.WriteString(evidenceMark + line + "\n")
		}
	}
	b.WriteString("\n")
}
