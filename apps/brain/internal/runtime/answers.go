package runtime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// maxAnswered caps what one approved call's result adds to the model's next
// message, as a report is capped.
const maxAnswered = 32 << 10

// carryOn takes up each child that was running when the run's worker
// stopped, as the run resumes: its worker is gone, so it has not beaten for
// lostAfter, and its call in the run's last turn was closed as ended. Each
// carries on from its own transcript, together as a pattern that runs a
// turn's calls together ran them, and its report is that call's result, so
// the run's model reads what it asked for rather than asking again. The
// reports are saved at once: a child carried on to its end is never listed
// again, so a report kept only in memory would be lost with the worker. A
// child that beat since, whose call is not open, or that could not be carried
// on is left to the reap, its call still ended.
func (e *Executor) carryOn(ctx context.Context, row db.Run, msgs []agent.Message, l Lineage) ([]agent.Message, error) {
	orphans, err := e.store.ListOrphanedChildren(ctx, db.ListOrphanedChildrenParams{
		ParentID: row.ID, StaleSeconds: e.lostAfter.Seconds(),
	})
	if err != nil {
		return nil, fmt.Errorf("reading which of run %s's delegates its worker left: %w", row.ID, err)
	}
	open := openCalls(msgs)
	var (
		g       errgroup.Group
		mu      sync.Mutex
		written []int
	)
	for _, c := range orphans {
		at, ok := open[startingCall(c)]
		if !ok {
			continue
		}
		delete(open, startingCall(c)) // one child per call
		g.Go(func() error {
			said, ok, err := e.carry(ctx, row, c, l)
			if err != nil || !ok {
				return err
			}
			// Each goroutine writes only its own call's message.
			msgs[at].Content = []agent.Content{{Type: agent.ContentText, Text: said}}
			mu.Lock()
			written = append(written, at)
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	if len(written) > 0 {
		if err := e.saveFrom(ctx, transcriptOf(row), slices.Min(written), msgs); err != nil {
			return nil, fmt.Errorf("keeping what run %s's delegates reported: %w", row.ID, err)
		}
	}
	return msgs, nil
}

// carry takes up one child its worker left and runs it to its report: false
// when it is not the run's to report, as it beat since or another worker took
// it. A store that fails to hand it over fails the run, as asking again could
// repeat what the child did. A child that fails to carry on costs the run
// nothing but its report, unless the worker is stopping, which stops the run.
func (e *Executor) carry(ctx context.Context, row, c db.Run, l Lineage) (string, bool, error) {
	taken, err := e.store.AdoptOrphan(ctx, db.AdoptOrphanParams{
		ID: c.ID, ParentID: row.ID, Worker: l.Worker, StaleSeconds: e.lostAfter.Seconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("taking up %s's run %s: %w", c.AgentName, c.ID, err)
	}
	got, err := e.takeUp(ctx, taken, l)
	if err != nil {
		if ctx.Err() != nil {
			return "", false, fmt.Errorf("carrying on %s's run %s: %w", c.AgentName, c.ID, err)
		}
		e.logger.WarnContext(ctx, "A delegate was not carried on; its call stays ended",
			"run", c.ID, "agent", c.AgentName, "err", err)
		return "", false, nil
	}
	if got.Status == "running" { // lost to another worker: it reports there
		return "", false, nil
	}
	return reportOf(got), true, nil
}

// reportOf is what a delegate said, as its parent reads it: its report, or,
// when it waits for a person, what it waits for.
func reportOf(child db.Run) string {
	if child.Status == "waiting" {
		return waitingReport(child)
	}
	return report(child, reportEnd())
}

// openCalls are the calls of the transcript's last turn that closeCalls
// closed as ended, by call id, at their result's index: read as closeCalls
// writes them, past a trailing files message, the results directly after the
// turn's calls.
func openCalls(msgs []agent.Message) map[string]int {
	end := len(msgs)
	if end > 0 && msgs[end-1].Role == agent.RoleUser && msgs[end-1].Attached {
		end--
	}
	out := map[string]int{}
	for i := end - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != agent.RoleTool {
			break
		}
		if m.Text() == endedMidCall {
			out[m.ToolCallID] = i
		}
	}
	return out
}

// startingCall is the id of the call that started a child: its
// parent_tool_call is that id, then "#" and its own run's. A call id may hold
// a "#" of its own, so the cut is at the last.
func startingCall(child db.Run) string {
	if child.ParentToolCall == nil {
		return ""
	}
	call := *child.ParentToolCall
	if i := strings.LastIndex(call, "#"); i >= 0 {
		call = call[:i]
	}
	return call
}

// catchUp is what happened while a run waited, for its model to read before
// anything else: each child that waited carries on, to its report or to
// waiting again; and each of the run's own requests that was answered is
// acted on, an approved call made then, with exactly the arguments that were
// approved. Empty when nothing happened, as on a resume after a crash.
func (e *Executor) catchUp(ctx context.Context, row db.Run, built Built, l Lineage) ([]agent.Content, error) {
	var (
		parts []string
		files []agent.Content
	)

	children, err := e.store.ListWaitingChildren(ctx, row.ID)
	if err != nil {
		return nil, fmt.Errorf("reading which of run %s's delegates wait: %w", row.ID, err)
	}
	for _, c := range children {
		got, err := e.adopt(ctx, c, l)
		if err != nil {
			return nil, err
		}
		parts = append(parts, reportOf(got))
	}

	untold, err := e.store.ListUntoldApprovals(ctx, row.ID)
	if err != nil {
		return nil, fmt.Errorf("reading what run %s's requests came to: %w", row.ID, err)
	}
	ids := make([]uuid.UUID, 0, len(untold))
	for _, a := range untold {
		said, attached, err := e.answered(ctx, row, built, l.Worker, a)
		if err != nil {
			return nil, err
		}
		parts = append(parts, fmt.Sprintf("Your request to call %s (%s): %s", a.Tool, oneLine(a.Summary), said))
		// As a turn's own calls attach theirs: named by the call, after what
		// was said.
		call := agent.ToolCall{ID: a.CallID, Name: a.Tool}
		if m, ok := agent.Attached(map[string][]agent.Content{call.ID: attached}, []agent.ToolCall{call}); ok {
			files = append(files, m.Content...)
		}
		ids = append(ids, a.ID)
	}
	// Told once: a crash after this and before the transcript is saved loses
	// the telling, never repeats the call, which is spent.
	if len(ids) > 0 {
		if err := e.store.MarkApprovalsTold(context.WithoutCancel(ctx), ids); err != nil {
			return nil, fmt.Errorf("recording that run %s was told its answers: %w", row.ID, err)
		}
	}

	if len(parts) == 0 {
		return nil, nil
	}
	said := agent.Content{Type: agent.ContentText, Text: "While you waited:\n\n" + strings.Join(parts, "\n\n")}
	return append([]agent.Content{said}, files...), nil
}

// answered acts on one answer and says what came of it. An approved call is
// recorded as any call is, a tool_call and its tool_result, so the trace
// shows it was made, when, and what it returned.
func (e *Executor) answered(ctx context.Context, row db.Run, built Built, worker string, a db.Approval) (string, []agent.Content, error) {
	switch a.Status {
	case "denied":
		return declinedWith(a.Reason), nil, nil
	case "expired":
		return expiredCall, nil, nil
	case "cancelled":
		switch {
		case a.DecidedBy == nil:
			return cancelledCall, nil, nil
		case row.ParentRunID != nil:
			return supersededDeep, nil, nil
		}
		return supersededCall, nil, nil
	case "approved":
	default:
		return "", nil, fmt.Errorf("request %s is %s, which has no answer to act on", a.ID, a.Status)
	}

	tool, ok := built.answerable[a.Tool]
	if !ok {
		return "It was approved, but this run no longer has " + a.Tool + ", so it was not made.", nil, nil
	}
	// Spent before it is made: a crash during the call never makes it twice.
	if _, err := e.store.ClaimApprovalUse(ctx, a.ID); errors.Is(err, pgx.ErrNoRows) {
		return usedCall, nil, nil
	} else if err != nil {
		return "", nil, fmt.Errorf("spending the approval for %s: %w", a.Tool, err)
	}
	if err := appendEvent(ctx, e.store, row.ID, worker, "tool_call", toolCallPayload{
		ID: a.CallID, Name: a.Tool, Arguments: string(a.Arguments),
	}); err != nil {
		return "", nil, err
	}
	began := time.Now()
	result, callable, err := tool.Call(agent.WithToolCallID(ctx, a.CallID), a.Arguments)
	recorded := toolResultPayload{ID: a.CallID, Name: a.Tool, DurationMS: time.Since(began).Milliseconds()}
	var (
		said     string
		attached []agent.Content
	)
	switch {
	case !callable:
		recorded.Error = "not callable here"
		said = "It was approved, but " + a.Tool + " cannot be called here, so it was not made."
	case err != nil:
		recorded.Error, _ = clip(err.Error())
		said = "A person approved it, and it was made, but it failed: " + recorded.Error
	default:
		out, cut := withinBytes(result.Output, maxAnswered)
		recorded.Output, recorded.Truncated = out, cut
		if cut {
			out += "\n[the result was cut at 32 KiB]"
		}
		said = "A person approved it, and it was made. Its result:\n" + out
		attached = result.Attach
	}
	if err := appendEvent(ctx, e.store, row.ID, worker, "tool_result", recorded); err != nil {
		return "", nil, err
	}
	return said, attached, nil
}

// withinBytes is s within limit bytes, cut at a character's start.
func withinBytes(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
