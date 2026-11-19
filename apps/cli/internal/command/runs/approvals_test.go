package runs

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const approvalIDInStream = "5d2c8a10-77be-4c1f-8e53-9b0a6f21d4e7"

func approvalStream(decision string) string {
	return frameOf("1", "started", `{"agent":"ops","model":"m","pattern":"react","tools":["github__delete_repo"]}`) +
		frameOf("2", "approval_requested", `{"approval_id":"`+approvalIDInStream+`","call_id":"c1","tool":"github__delete_repo","class":"destructive","summary":"repo: acme/site","code":"k7qm","expires_at":"2026-10-21T10:00:00Z","run":"`+runID+`"}`) +
		frameOf("3", "waiting", `{"reason":"approval","said":"I need a person to say yes before I delete **acme/site**."}`) +
		frameOf("4", "approval_decided", decision) +
		frameOf("5", "finished", `{"status":"succeeded"}`) +
		frameOf("", "end", `{"status":"succeeded"}`)
}

// A run that stops for a person is the one moment a follower has to act, so
// the request must name the tool, what it would do, the code and the command.
func TestTheTranscriptShowsARequestAndTheRunWaiting(t *testing.T) {
	clitest.Routes(t, sampleRoutes(approvalStream(`{"approval_id":"`+approvalIDInStream+`","decision":"approved","by":"9c4e1a2b-3d5f-4a6b-8c7d-1e2f3a4b5c6d","via":"cli","scope":"run"}`)))

	out, err := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	if err != nil {
		t.Fatalf("runs events: %v\n%s", err, out)
	}
	for _, want := range []string{
		"? approval needed · github__delete_repo (destructive)",
		"repo: acme/site",
		"reply k7qm in chat",
		"`oa approvals approve platform " + approvalIDInStream + "`",
		"expires 2026-10-21 10:00 UTC",
		"⏸ waiting for a person to answer",
		"I need a person to say yes before I delete acme/site.",
		"◆ approved for the rest of the run by 9c4e1a2b-3d5f-4a6b-8c7d-1e2f3a4b5c6d via cli",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("transcript is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "approval_requested") || strings.Contains(out, "approval_decided") {
		t.Errorf("an approval event printed as a raw frame:\n%s", out)
	}
}

func TestTheTranscriptSaysHowAnUnansweredRequestEnded(t *testing.T) {
	for decision, want := range map[string]string{
		`{"approval_id":"x","decision":"denied","by":"u1","via":"channel"}`:     "◆ denied by u1 via channel",
		`{"approval_id":"x","decision":"expired","by":null,"via":"sweep"}`:      "◆ expired, nobody answered in time",
		`{"approval_id":"x","decision":"cancelled","by":null,"via":null}`:       "◆ cancelled, the run no longer needs an answer",
		`{"approval_id":"x","decision":"approved","by":"u1","superseded":true}`: "(superseded)",
	} {
		clitest.Routes(t, sampleRoutes(approvalStream(decision)))
		out, err := clitest.Execute(t, commands, "runs", "events", "platform", runID)
		if err != nil {
			t.Fatalf("runs events: %v\n%s", err, out)
		}
		if !strings.Contains(out, want) {
			t.Errorf("decision %s: missing %q:\n%s", decision, want, out)
		}
	}
}

// A request's text is the model's and the tool's: it reaches a terminal.
func TestARequestCannotMoveTheCursor(t *testing.T) {
	stream := frameOf("1", "approval_requested", `{"approval_id":"a","tool":"t\u001b[2J","class":"write","summary":"x\u001b]52;c;AAAA\u0007y","code":"k7qm"}`) +
		frameOf("", "end", `{"status":"succeeded"}`)
	clitest.Routes(t, sampleRoutes(stream))
	out, _ := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	if strings.Contains(out, "\u001b") || strings.Contains(out, "\u0007") {
		t.Errorf("an escape reached the terminal: %q", out)
	}
}

func TestJSONKeepsTheApprovalFramesAsRecorded(t *testing.T) {
	clitest.Routes(t, sampleRoutes(approvalStream(`{"approval_id":"x","decision":"approved","by":"u1","via":"cli"}`)))
	out, err := clitest.Execute(t, commands, "runs", "events", "platform", runID, "-o", "json")
	if err != nil {
		t.Fatalf("runs events -o json: %v", err)
	}
	for _, want := range []string{`"event":"approval_requested"`, `"event":"waiting"`, `"code":"k7qm"`} {
		if !strings.Contains(strings.ReplaceAll(out, `": "`, `":"`), want) {
			t.Errorf("json is missing %s:\n%s", want, out)
		}
	}
}

// A run waiting is not a run that ended; if a stream ever closes on one, the
// exit status says what to do rather than "unknown status".
func TestAStreamEndingOnAWaitingRunPointsAtTheApprovals(t *testing.T) {
	routes := eventsRoute(http.StatusOK, frameOf("", "end", `{"status":"waiting"}`))
	routes["GET /teams/{id}/runs/{runID}"] = clitest.Reply{Status: http.StatusOK, Body: runJSON(runID, "waiting", "")}
	clitest.Routes(t, routes)

	_, err := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	if err == nil || !strings.Contains(err.Error(), "oa approvals list platform --run "+runID) {
		t.Errorf("err = %v", err)
	}
}

func TestAWaitingRunIsShownAsWaiting(t *testing.T) {
	clitest.Routes(t, with("GET /teams/{id}/runs/{runID}", clitest.Reply{Status: http.StatusOK, Body: runJSON(runID, "waiting", "")}))
	out, err := clitest.Execute(t, commands, "runs", "get", "platform", runID)
	if err != nil {
		t.Fatalf("runs get: %v", err)
	}
	if !strings.Contains(out, "waiting") || strings.Contains(out, "ended with a status") {
		t.Errorf("out:\n%s", out)
	}
}
