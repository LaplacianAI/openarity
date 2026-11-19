package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/approval"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// Approvals is what answering a request from the chat needs: the requests
// waiting in a conversation, its answer and event, and the brain's notice
// about it.
type Approvals interface {
	ListPendingApprovalsBySession(ctx context.Context, sessionID uuid.UUID) ([]db.Approval, error)
	GetApproval(ctx context.Context, arg db.GetApprovalParams) (db.Approval, error)
	DecideApproval(ctx context.Context, arg db.DecideApprovalParams) (db.Approval, error)
	InsertNotice(ctx context.Context, arg db.InsertNoticeParams) (int64, error)
}

// PollOptions is the options of the poll a platform with neither buttons
// nor a keyboard asks with, approve first. A vote comes back as the hash of
// the option, so the code in each is what names the request.
func PollOptions(code string) []string {
	return []string{"Approve " + code, "Decline " + code}
}

// typed reads a typed answer: the whole message, once the channel's word and
// any mention are taken out, is yes, no, y or n, and at most a code. Anything
// else, a "yes 7K2F" inside a sentence too, is not an answer.
func typed(text, trigger string) (Decision, string, bool) {
	var words []string
	for _, w := range strings.Fields(text) {
		bare := strings.Trim(w, ".,!?:;")
		switch {
		case trigger != "" && strings.EqualFold(bare, trigger):
		case strings.HasPrefix(w, "@"), strings.HasPrefix(w, "<@"):
		case bare != "":
			words = append(words, bare)
		}
	}
	if len(words) == 0 || len(words) > 2 {
		return "", "", false
	}
	var d Decision
	switch strings.ToLower(words[0]) {
	case "yes", "y":
		d = Approve
	case "no", "n":
		d = Decline
	default:
		return "", "", false
	}
	if len(words) == 1 {
		return d, "", true
	}
	code := strings.ToUpper(words[1])
	if len(code) != 4 {
		return "", "", false
	}
	return d, code, true
}

// chat is one answer given in a conversation, as it is being decided.
type chat struct {
	q       Queries
	ch      Channel
	s       db.Session
	m       Delivery
	message uuid.UUID
}

// answerTyped decides a typed answer, or says it is not one (handled false):
// the message is then an ordinary one. A yes or a no is an answer only while
// the sender has a request waiting in the conversation; with none, it is
// what they said.
func answerTyped(ctx context.Context, c chat) (outcome, bool, error) {
	d, code, ok := typed(c.m.Text, c.ch.answering().Trigger)
	if !ok {
		return outcome{}, false, nil
	}
	pending, err := c.q.ListPendingApprovalsBySession(ctx, c.s.ID)
	if err != nil {
		return outcome{}, false, fmt.Errorf("requests waiting in session %s: %w", c.s.ID, err)
	}
	var mine []db.Approval
	for _, a := range pending {
		if a.RequestedFor != nil && *a.RequestedFor == c.m.UserID {
			mine = append(mine, a)
		}
	}
	if len(mine) == 0 {
		return outcome{}, false, nil
	}
	if code != "" {
		for _, a := range pending {
			if a.Code == code {
				out, err := c.decide(ctx, a, d, "")
				return out, true, err
			}
		}
		out, err := c.tell(ctx, mine[0].RootRunID, "No request is waiting with the code "+code+".", ReplyNotice{})
		return out, true, err
	}

	maySelf, err := c.maySelf(ctx)
	if err != nil {
		return outcome{}, true, err
	}
	var askable []db.Approval
	for _, a := range mine {
		if approval.AsksInChat(a.RequireOther, maySelf) {
			askable = append(askable, a)
		}
	}
	switch len(askable) {
	case 1:
		out, err := c.decide(ctx, askable[0], d, "")
		return out, true, err
	case 0:
		out, err := c.tell(ctx, mine[0].RootRunID, waitsForApprover, ReplyNotice{})
		return out, true, err
	}
	codes := make([]string, len(askable))
	for i, a := range askable {
		codes[i] = fmt.Sprintf("%s %s (%s)", d.word(), a.Code, approval.ToolName(a.Tool))
	}
	out, err := c.tell(ctx, askable[0].RootRunID,
		fmt.Sprintf("%d requests are waiting. Reply with the code: %s.", len(askable), strings.Join(codes, ", ")),
		ReplyNotice{})
	return out, true, err
}

// answerTap decides an answer given with the platform's own control. It
// names its request by id, code or the hash of a poll's option; one that no
// longer waits is said to be over, and its controls are taken away.
func answerTap(ctx context.Context, c chat) (outcome, error) {
	ans := c.m.Answer
	pending, err := c.q.ListPendingApprovalsBySession(ctx, c.s.ID)
	if err != nil {
		return outcome{}, fmt.Errorf("requests waiting in session %s: %w", c.s.ID, err)
	}
	d := ans.Decision
	for _, a := range pending {
		switch {
		case ans.ApprovalID != "" && a.ID.String() == ans.ApprovalID,
			ans.ApprovalID == "" && ans.Code != "" && strings.EqualFold(a.Code, ans.Code):
			return c.decide(ctx, a, d, ans.Reason)
		}
		if chosen, ok := pollChoice(a.Code, ans.Hashes); ok {
			return c.decide(ctx, a, chosen, "")
		}
	}

	// Answered, expired or ended already: said once, where it was tapped.
	if id, err := uuid.Parse(ans.ApprovalID); err == nil {
		a, err := c.q.GetApproval(ctx, db.GetApprovalParams{ID: id, TeamID: c.ch.TeamID})
		switch {
		case err == nil && a.Status != "pending":
			return c.tell(ctx, a.RootRunID, over(a), ReplyNotice{Handle: ans.Handle, Settled: true})
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return outcome{}, fmt.Errorf("request %s: %w", id, err)
		}
	}
	return outcome{}, nil
}

// pollChoice is the decision a poll vote makes on the request with this code:
// the options are hashed as the platform hashes them.
func pollChoice(code string, hashes [][]byte) (Decision, bool) {
	opts := PollOptions(code)
	for _, h := range hashes {
		for i, o := range opts {
			sum := sha256.Sum256([]byte(o))
			if bytes.Equal(h, sum[:]) {
				if i == 0 {
					return Approve, true
				}
				return Decline, true
			}
		}
	}
	return "", false
}

const waitsForApprover = "This one waits for an approver, who answers it on the dashboard."

// decide answers one request for the sender, in the transaction that stored
// their message, which holds the conversation's session. Only the person it
// was asked for answers it here, and only when they may approve their own.
func (c chat) decide(ctx context.Context, a db.Approval, d Decision, reason string) (outcome, error) {
	handle := ReplyNotice{}
	if c.m.Answer != nil {
		handle.Handle = c.m.Answer.Handle
	}
	if a.RequestedFor == nil || *a.RequestedFor != c.m.UserID {
		handle.Private = true
		return c.tell(ctx, a.RootRunID, "Only the person this was asked for can answer it here.", handle)
	}
	maySelf, err := c.maySelf(ctx)
	if err != nil {
		return outcome{}, err
	}
	if !approval.AsksInChat(a.RequireOther, maySelf) {
		handle.Private = true
		return c.tell(ctx, a.RootRunID, waitsForApprover, handle)
	}
	if d == DeclineAsk {
		form := approval.PromptRequest{ID: a.ID.String(), Code: a.Code, Tool: a.Tool, Summary: a.Summary, ExpiresAt: a.ExpiresAt, Ask: true}
		handle.Form, handle.Private = &form, true
		return c.tell(ctx, a.RootRunID, "Why decline "+approval.ToolName(a.Tool)+"?", handle)
	}

	// a was read as pending in this transaction, which holds the session
	// every writer of a request takes first (a person deciding elsewhere,
	// the sweep, a tree ending), so it is still pending: nothing to lock.
	held := a
	handle.Settled = true

	status, scope, via, by, why := "denied", "once", "channel", &c.m.UserID, optional(reason)
	switch d {
	case Approve:
		status = "approved"
	case ApproveRun:
		status, scope = "approved", "run"
	case Decline, DeclineAsk:
		// DeclineAsk opened its form above; what comes back is a Decline.
	}
	expired := !time.Now().Before(held.ExpiresAt)
	params := db.DecideApprovalParams{
		ID: held.ID, TeamID: held.TeamID, Status: status, DecidedBy: by, DecidedVia: &via,
		DecisionScope: &scope, Reason: why,
	}
	if expired {
		sweep := "sweep"
		params = db.DecideApprovalParams{ID: held.ID, TeamID: held.TeamID, Status: "expired", DecidedVia: &sweep}
	}
	decided, err := c.q.DecideApproval(ctx, params)
	if err != nil {
		return outcome{}, fmt.Errorf("answer request %s: %w", held.ID, err)
	}
	if err := c.q.AppendDecidedEvents(ctx, []uuid.UUID{decided.ID}); err != nil {
		return outcome{}, fmt.Errorf("record the answer to request %s: %w", decided.ID, err)
	}
	out, err := c.tell(ctx, decided.RootRunID, over(decided), handle)
	if err != nil {
		return outcome{}, err
	}
	row, err := c.q.RequeueAnswered(ctx, decided.RootRunID)
	switch {
	case err == nil:
		out.resumed = &row
	case !errors.Is(err, pgx.ErrNoRows):
		return outcome{}, fmt.Errorf("queue run %s again: %w", decided.RootRunID, err)
	}
	return out, nil
}

func (c chat) maySelf(ctx context.Context) (bool, error) {
	may, err := c.q.MemberMay(ctx, db.MemberMayParams{TeamID: c.ch.TeamID, UserID: c.m.UserID, Action: "approval:self"})
	if err != nil {
		return false, fmt.Errorf("may %s approve their own: %w", c.m.UserID, err)
	}
	return may, nil
}

// tell queues the brain's notice about this message, on the run that asked.
func (c chat) tell(ctx context.Context, runID uuid.UUID, text string, n ReplyNotice) (outcome, error) {
	var detail []byte
	if len(n.Handle) > 0 || n.Private || n.Settled || n.Form != nil {
		raw, err := json.Marshal(n)
		if err != nil {
			return outcome{}, fmt.Errorf("encode the notice: %w", err)
		}
		detail = raw
	}
	if _, err := c.q.InsertNotice(ctx, db.InsertNoticeParams{
		TeamID: c.ch.TeamID, RunID: runID, SessionID: c.s.ID, ChannelID: c.ch.ID,
		MessageID: c.message, Text: text, Detail: detail,
	}); err != nil {
		return outcome{}, fmt.Errorf("tell the answer to message %s: %w", c.message, err)
	}
	return outcome{notice: c.message}, nil
}

// over is where a request stands once it is not waiting, as a sentence.
func over(a db.Approval) string {
	name := approval.ToolName(a.Tool)
	switch a.Status {
	case "approved":
		if a.DecisionScope != nil && *a.DecisionScope == "run" {
			return "Approved for this run: " + name + "."
		}
		return "Approved: " + name + "."
	case "denied":
		return "Declined: " + name + "."
	case "expired":
		return "That request for " + name + " expired before it was answered."
	default:
		return "That request for " + name + " is no longer waiting."
	}
}

func (d Decision) word() string {
	if d == Decline {
		return "no"
	}
	return "yes"
}

func optional(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if r := []rune(s); len(r) > 1000 {
		s = string(r[:1000])
	}
	return &s
}
