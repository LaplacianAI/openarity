package channels

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

func requestBody(t *testing.T, script *clitest.Transcript, method string) string {
	t.Helper()
	for _, e := range script.All() {
		if e.Method == method {
			return e.Body
		}
	}
	t.Fatalf("nothing was sent with %s", method)
	return ""
}

func TestApproveNewSendsTheRoleAndNameAndNoUser(t *testing.T) {
	routes := senderRoutes()
	routes["POST /teams/{id}/channels/{channelID}/senders"] = clitest.Reply{Status: http.StatusNoContent}
	script := clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "channels", "senders", "approve",
		"platform", "support", "919800000001", "--new", "--name", "Ravi")
	if err != nil {
		t.Fatalf("senders approve --new: %v\n%s", err, out)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(postBody(t, script)), &got); err != nil {
		t.Fatal(err)
	}
	n, _ := got["new"].(map[string]any)
	if n["role"] != "guest" || n["name"] != "Ravi" || got["user_id"] != nil {
		t.Errorf("body = %v; want new {guest, Ravi} and no user_id", got)
	}
	if script.Calls(http.MethodGet, "/teams/"+teamID+"/members") != 0 {
		t.Error("someone new needs no member looked up")
	}
}

func TestApproveTakesAUserOrNewButNotBoth(t *testing.T) {
	for name, args := range map[string][]string{
		"both":    {"U01ABC", "asha", "--new"},
		"neither": {"U01ABC"},
	} {
		t.Run(name, func(t *testing.T) {
			script := clitest.Routes(t, senderRoutes())
			_, err := clitest.Execute(t, commands, append([]string{"channels", "senders", "approve", "platform", "support"}, args...)...)
			if err == nil || !strings.Contains(err.Error(), "--new") {
				t.Fatalf("err = %v, want it to say to name a user or give --new", err)
			}
			if len(script.All()) != 0 {
				t.Error("a refused command still called the brain")
			}
		})
	}
}

// Another sender first, speaking as someone else, so finding the sender is
// by its ref and not by being the first row.
const oneApprovedSender = `{"items":[{"sender_ref":"919800000002","user_id":"0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f","created_at":"2026-11-17T08:00:00Z","name":"Mira","role":"member"},{"sender_ref":"919800000001","user_id":"` + userID + `","created_at":"2026-11-17T09:00:00Z","name":"Ravi","role":"guest","channel_user":true}]}`

// A role belongs to the user a sender speaks as, so update finds that user in
// the channel's approved list and changes the member in place.
func TestUpdateChangesTheRoleOfTheUserTheSenderSpeaksAs(t *testing.T) {
	routes := senderRoutes()
	routes["GET /teams/{id}/channels/{channelID}/senders"] = clitest.Reply{Status: http.StatusOK, Body: oneApprovedSender}
	routes["PATCH /teams/{id}/members/{userID}"] = clitest.Reply{Status: http.StatusNoContent}
	script := clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "channels", "senders", "update",
		"platform", "support", "919800000001", "--role", "member")
	if err != nil {
		t.Fatalf("senders update --role: %v\n%s", err, out)
	}
	if script.Calls(http.MethodPatch, "/teams/"+teamID+"/members/"+userID) != 1 {
		t.Fatalf("calls = %+v; want one PATCH of the sender's user", script.All())
	}
	if body := requestBody(t, script, http.MethodPatch); !strings.Contains(body, `"role":"member"`) {
		t.Errorf("body = %s, want the role", body)
	}
}

func TestUpdateWithAUserMovesTheSender(t *testing.T) {
	routes := senderRoutes()
	routes["POST /teams/{id}/channels/{channelID}/senders"] = clitest.Reply{Status: http.StatusNoContent}
	script := clitest.Routes(t, routes)

	if out, err := clitest.Execute(t, commands, "channels", "senders", "update",
		"platform", "support", "919800000001", "--user", userID); err != nil {
		t.Fatalf("senders update --user: %v\n%s", err, out)
	}
	if body := postBody(t, script); !strings.Contains(body, `"user_id":"`+userID+`"`) {
		t.Errorf("body = %s, want the sender moved to the user", body)
	}
}

func TestUpdateOfASenderNobodyApprovedSaysSo(t *testing.T) {
	routes := senderRoutes()
	routes["GET /teams/{id}/channels/{channelID}/senders"] = clitest.Reply{Status: http.StatusOK, Body: `{"items":[]}`}
	script := clitest.Routes(t, routes)

	_, err := clitest.Execute(t, commands, "channels", "senders", "update",
		"platform", "support", "919800000009", "--role", "member")
	if err == nil || !strings.Contains(err.Error(), "no approved sender") {
		t.Fatalf("err = %v, want it to say no sender has that ref", err)
	}
	if script.Calls(http.MethodPatch, "/teams/"+teamID+"/members/"+userID) != 0 {
		t.Error("a member was changed for a sender that does not exist")
	}
}

func TestUpdateTakesAUserOrARoleButNotBoth(t *testing.T) {
	for name, flags := range map[string][]string{
		"both":    {"--user", userID, "--role", "member"},
		"neither": {},
	} {
		t.Run(name, func(t *testing.T) {
			script := clitest.Routes(t, senderRoutes())
			_, err := clitest.Execute(t, commands, append([]string{"channels", "senders", "update", "platform", "support", "U01ABC"}, flags...)...)
			if err == nil {
				t.Fatal("a refused update succeeded")
			}
			if len(script.All()) != 0 {
				t.Error("a refused command still called the brain")
			}
		})
	}
}
