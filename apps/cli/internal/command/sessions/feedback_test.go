package sessions

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const (
	runID        = "0b7c3e52-8a41-4f6d-9e2b-5c1a7d3f8e60"
	feedbackPath = "/teams/" + teamID + "/runs/" + runID + "/feedback"
)

func feedbackRoutes(t *testing.T, put, del clitest.Reply) *clitest.Transcript {
	t.Helper()
	routes := teamRoute()
	routes["PUT "+feedbackPath] = put
	routes["DELETE "+feedbackPath] = del
	return clitest.Routes(t, routes)
}

var savedDown = clitest.Reply{
	Status: http.StatusOK,
	Body:   `{"value":"down","note":"missed one","updated_at":"2026-11-21T10:00:00Z"}`,
}

func sent(t *testing.T, script *clitest.Transcript, method string) map[string]any {
	t.Helper()
	for _, e := range script.All() {
		if e.Method == method && strings.HasSuffix(e.Path, "/feedback") {
			var body map[string]any
			if e.Body != "" {
				if err := json.Unmarshal([]byte(e.Body), &body); err != nil {
					t.Fatalf("body is not json: %v\n%s", err, e.Body)
				}
			}
			return body
		}
	}
	t.Fatalf("no %s was sent: %+v", method, script.All())
	return nil
}

func TestFeedbackSendsTheVerdictNoteAndVia(t *testing.T) {
	script := feedbackRoutes(t, savedDown, clitest.Reply{})

	out, err := clitest.Execute(t, commands, "sessions", "feedback", "platform", runID, "down", "--note", " missed one ")
	if err != nil {
		t.Fatalf("sessions feedback: %v\n%s", err, out)
	}
	body := sent(t, script, http.MethodPut)
	if body["value"] != "down" || body["note"] != "missed one" || body["via"] != "cli" {
		t.Errorf("body = %v", body)
	}
	if !strings.Contains(out, "thumbs down") || !strings.Contains(out, "missed one") {
		t.Errorf("no confirmation:\n%s", out)
	}
}

func TestFeedbackUpWithoutANoteSendsNone(t *testing.T) {
	script := feedbackRoutes(t, clitest.Reply{
		Status: http.StatusOK,
		Body:   `{"value":"up","note":"","updated_at":"2026-11-21T10:00:00Z"}`,
	}, clitest.Reply{})

	if _, err := clitest.Execute(t, commands, "sessions", "feedback", "platform", runID, "up"); err != nil {
		t.Fatalf("sessions feedback: %v", err)
	}
	body := sent(t, script, http.MethodPut)
	if body["value"] != "up" {
		t.Errorf("body = %v", body)
	}
	if _, has := body["note"]; has {
		t.Errorf("sent an empty note: %v", body)
	}
}

func TestFeedbackClearDeletes(t *testing.T) {
	script := feedbackRoutes(t, savedDown, clitest.Reply{Status: http.StatusNoContent})

	out, err := clitest.Execute(t, commands, "sessions", "feedback", "platform", runID, "--clear")
	if err != nil {
		t.Fatalf("sessions feedback --clear: %v\n%s", err, out)
	}
	if script.Calls(http.MethodDelete, feedbackPath) != 1 || script.Calls(http.MethodPut, feedbackPath) != 0 {
		t.Errorf("calls: %+v", script.All())
	}
	if !strings.Contains(out, "cleared") {
		t.Errorf("no confirmation:\n%s", out)
	}
}

func TestFeedbackRefusesBeforeAskingTheBrain(t *testing.T) {
	long := strings.Repeat("x", 1001)
	for name, args := range map[string][]string{
		"neither a verdict nor --clear": {},
		"both a verdict and --clear":    {"up", "--clear"},
		"a verdict that is not one":     {"sideways"},
		"a note with --clear":           {"--clear", "--note", "hm"},
		"a note over 1000 characters":   {"up", "--note", long},
		"a run that is not an id":       {"up", "--run-not-id"},
	} {
		t.Run(name, func(t *testing.T) {
			script := feedbackRoutes(t, savedDown, clitest.Reply{Status: http.StatusNoContent})

			run := runID
			if name == "a run that is not an id" {
				run, args = "nope", []string{"up"}
			}
			_, err := clitest.Execute(t, commands, append([]string{"sessions", "feedback", "platform", run}, args...)...)
			if err == nil {
				t.Fatal("accepted")
			}
			if len(script.All()) != 0 {
				t.Errorf("asked the brain anyway: %+v", script.All())
			}
		})
	}
}

func TestFeedbackNoteOfExactly1000CharactersIsSent(t *testing.T) {
	script := feedbackRoutes(t, savedDown, clitest.Reply{})

	if _, err := clitest.Execute(t, commands, "sessions", "feedback", "platform", runID, "down",
		"--note", strings.Repeat("é", 1000)); err != nil {
		t.Fatalf("a 1000-character note: %v", err)
	}
	if script.Calls(http.MethodPut, feedbackPath) != 1 {
		t.Error("not sent")
	}
}

func TestFeedbackShowsTheBrainsReasonForAConflict(t *testing.T) {
	feedbackRoutes(t, clitest.Reply{
		Status: http.StatusConflict,
		Body:   "self-improvement is off for this team",
	}, clitest.Reply{})

	_, err := clitest.Execute(t, commands, "sessions", "feedback", "platform", runID, "up")
	if err == nil || !strings.Contains(err.Error(), "self-improvement is off for this team") {
		t.Errorf("err = %v, want the brain's reason", err)
	}
}

func TestFeedbackClearShowsTheBrainsReasonForAConflict(t *testing.T) {
	feedbackRoutes(t, savedDown, clitest.Reply{
		Status: http.StatusConflict,
		Body:   "a delegate's run takes no feedback",
	})

	_, err := clitest.Execute(t, commands, "sessions", "feedback", "platform", runID, "--clear")
	if err == nil || !strings.Contains(err.Error(), "delegate") {
		t.Errorf("err = %v, want the brain's reason", err)
	}
}

func TestFeedbackNamesThePermissionItNeeded(t *testing.T) {
	feedbackRoutes(t, clitest.Reply{Status: http.StatusForbidden}, clitest.Reply{})

	_, err := clitest.Execute(t, commands, "sessions", "feedback", "platform", runID, "up")
	if err == nil || !strings.Contains(err.Error(), "run:write") {
		t.Errorf("err = %v, want run:write named", err)
	}
}

func TestFeedbackPrintsJSON(t *testing.T) {
	feedbackRoutes(t, savedDown, clitest.Reply{})

	out, err := clitest.Execute(t, commands, "sessions", "feedback", "platform", runID, "down", "-o", "json")
	if err != nil {
		t.Fatalf("sessions feedback: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || got["value"] != "down" || got["run_id"] != runID {
		t.Errorf("out = %s (%v)", out, err)
	}
}
