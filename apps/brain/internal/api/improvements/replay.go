package improvements

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// The state of a proposal's tries on past runs, as a whole.
const (
	// replayOff: it was never tried, as its team had trying off when its
	// runs would have been chosen.
	replayOff = "off"
	// replayTrying: a try is still to run.
	replayTrying = "trying"
	// replayDone: at least one try came to an outcome.
	replayDone = "done"
	// replayNone: none of its runs could be tried, or every try failed.
	replayNone = "none"
)

// What a try made of a past run, decided once here so every reader tells
// it the same way.
const (
	outcomeFixed    = "fixed"     // it went wrong, and now goes well
	outcomeKept     = "kept"      // it went well, and still does
	outcomeBroke    = "broke"     // it went well, and now goes wrong
	outcomeStillBad = "still_bad" // it went wrong, and still does
	outcomeUnclear  = "unclear"   // the try came to no outcome that could be told
)

// replayView is how a proposal fared on past runs before it was decided.
type replayView struct {
	Status string          `json:"status"`
	Runs   []replayRunView `json:"runs"`
}

// replayCost is what a run spent: tokens as a run's cost counts them,
// steps, and time working.
type replayCost struct {
	Tokens int64 `json:"tokens"`
	Steps  int32 `json:"steps"`
	WorkMS int64 `json:"work_ms"`
}

// replayRunView is one past run the proposal was tried on, beside what the
// run did itself. Its request and the judge's why are withheld from a
// caller who may not read the run's conversation.
type replayRunView struct {
	Run      runRef  `json:"run"`
	Chosen   string  `json:"chosen"`
	Status   string  `json:"status"`
	SkipWhy  *string `json:"skip_why"`
	Error    *string `json:"error"`
	Original string  `json:"original"`
	Replayed *string `json:"replayed"`
	// Outcome is the run's outcome beside the try's, for a try that is
	// done; nil for any other.
	Outcome  *string `json:"outcome"`
	Verdict  *string `json:"verdict"`
	Why      *string `json:"why"`
	Request  *string `json:"request"`
	Withheld bool    `json:"withheld"`
	// Diverged is a try that came to a call the past run never made and
	// that was not a read: the call was not made.
	Diverged   bool        `json:"diverged"`
	Recorded   int16       `json:"recorded"`
	Live       int16       `json:"live"`
	NotMade    int16       `json:"not_made"`
	Before     replayCost  `json:"before"`
	After      *replayCost `json:"after"`
	StartedAt  *time.Time  `json:"started_at"`
	FinishedAt *time.Time  `json:"finished_at"`
}

// replays reads the tries of a page's proposals at once, by proposal.
func (h *handler) replays(r *http.Request, u *auth.User, teamID uuid.UUID, proposals []db.ImprovementProposal) (map[uuid.UUID]*replayView, error) {
	ids := make([]uuid.UUID, len(proposals))
	for i, p := range proposals {
		ids[i] = p.ID
	}
	rows, err := h.reads.ListProposalReplays(r.Context(), ids)
	if err != nil {
		return nil, err
	}
	readable, err := h.visible(r, u, teamID, rows)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]*replayView, len(ids))
	chosen := make(map[uuid.UUID]bool, len(ids))
	for _, p := range proposals {
		out[p.ID] = &replayView{Status: replayOff, Runs: []replayRunView{}}
		chosen[p.ID] = p.ReplayChosenAt != nil
	}
	for _, row := range rows {
		v := out[row.ProposalID]
		if v == nil {
			continue
		}
		v.Runs = append(v.Runs, replayRunOf(row, readable[row.SourceRunID]))
	}
	for id, v := range out {
		v.Status = replayStatus(v.Runs, chosen[id])
	}
	return out, nil
}

// visible is which of the rows' runs the caller may read, with where.
func (h *handler) visible(r *http.Request, u *auth.User, teamID uuid.UUID, rows []db.ListProposalReplaysRow) (map[uuid.UUID]*uuid.UUID, error) {
	out := map[uuid.UUID]*uuid.UUID{}
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.SourceRunID
	}
	visible, err := h.reads.ListVisibleRuns(r.Context(), db.ListVisibleRunsParams{
		TeamID: teamID, Ids: ids, Moderator: h.can(r, u, teamID, readEveryConvo), Viewer: u.ID,
	})
	if err != nil {
		return nil, err
	}
	for _, v := range visible {
		out[v.ID] = &v.SessionID
	}
	return out, nil
}

func replayRunOf(row db.ListProposalReplaysRow, session *uuid.UUID) replayRunView {
	v := replayRunView{
		Run: runRef{ID: row.SourceRunID, Readable: session != nil, SessionID: session}, Chosen: row.Chosen,
		Status: row.Status, SkipWhy: row.SkipWhy, Error: row.Error, Original: row.Original,
		Replayed: row.Replayed, Verdict: row.Verdict, Diverged: row.Diverged,
		Recorded: row.Recorded, Live: row.Live, NotMade: row.NotMade,
		Before:    replayCost{Tokens: row.OriginalSpent, Steps: row.OriginalSteps, WorkMS: row.OriginalWorkMs},
		StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
	}
	if row.Status == "done" {
		v.After = &replayCost{Tokens: row.Spent, Steps: row.Steps, WorkMS: row.WorkMs}
		o := outcomeOf(row.Original, row.Replayed)
		v.Outcome = &o
	}
	if session == nil {
		// What failed may quote the conversation: only that it failed is
		// said.
		v.Withheld = true
		if v.Error != nil {
			withheld := "the try failed"
			v.Error = &withheld
		}
		return v
	}
	request := row.Request
	v.Request, v.Why = &request, row.Why
	return v
}

func outcomeOf(original string, replayed *string) string {
	switch {
	case replayed == nil || (*replayed != "good" && *replayed != "bad"):
		return outcomeUnclear
	case original == "bad" && *replayed == "good":
		return outcomeFixed
	case original == "good" && *replayed == "good":
		return outcomeKept
	case original == "good":
		return outcomeBroke
	default:
		return outcomeStillBad
	}
}

// replayStatus is a proposal's tries as a whole. One with none was either
// proposed while trying was off, or had none of its runs fit to try.
func replayStatus(runs []replayRunView, chosen bool) string {
	if len(runs) == 0 {
		if chosen {
			return replayNone
		}
		return replayOff
	}
	done := false
	for _, r := range runs {
		switch r.Status {
		case "queued", "trying":
			return replayTrying
		case "done":
			done = true
		}
	}
	if done {
		return replayDone
	}
	return replayNone
}

// withReplay adds a proposal's tries to its view.
func (h *handler) withReplay(r *http.Request, u *auth.User, v *view, p db.ImprovementProposal) error {
	byID, err := h.replays(r, u, p.TeamID, []db.ImprovementProposal{p})
	if err != nil {
		return err
	}
	v.Replay = byID[v.ID]
	return nil
}

// replayTraceView is one try whole: each call as it was served, and the
// answer it came to.
type replayTraceView struct {
	Run    runRef          `json:"run"`
	Answer *string         `json:"answer"`
	Calls  json.RawMessage `json:"calls"`
}

// replay is one try's calls and answer, to a caller who may read the past
// run's conversation; to anyone else it is withheld, as a pass's report is.
func (h *handler) replay(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	p, ok := h.find(w, r, u)
	if !ok {
		return
	}
	runID, err := uuid.Parse(r.PathValue("runID"))
	if err != nil {
		http.Error(w, "runID must be a uuid", http.StatusBadRequest)
		return
	}
	row, err := h.reads.GetProposalReplay(r.Context(), db.GetProposalReplayParams{TeamID: p.TeamID, ProposalID: p.ID, SourceRunID: runID})
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the try", err)
		return
	}
	visible, err := h.reads.ListVisibleRuns(r.Context(), db.ListVisibleRunsParams{
		TeamID: p.TeamID, Ids: []uuid.UUID{runID}, Moderator: h.can(r, u, p.TeamID, readEveryConvo), Viewer: u.ID,
	})
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read whether the run is visible", err)
		return
	}
	if len(visible) == 0 {
		http.Error(w, "the run's conversation is not one you can read", http.StatusForbidden)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, replayTraceView{
		Run:    runRef{ID: runID, Readable: true, SessionID: &visible[0].SessionID},
		Answer: row.Answer, Calls: callsOf(row.Trace),
	})
}

// callsOf is a try's kept calls as a list, whatever kept them: a reader
// counts them, so never null.
func callsOf(trace []byte) json.RawMessage {
	if t := bytes.TrimSpace(trace); len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return json.RawMessage("[]")
	}
	return trace
}
