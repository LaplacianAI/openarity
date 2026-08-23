package channels

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const userID = "a1c3e5f7-b2d4-4a6c-8e0f-1a3c5e7f9b1d"

const oneChannel = `{"items":[{"id":"` + channelID + `","team_id":"` + teamID + `","provider":"custom","name":"support"}]}`

const oneMember = `{"items":[{"user_id":"` + userID + `","subject":"asha","role":"member"}]}`

const twoPending = `{
  "items": [
    {"sender_ref":"U01ABC","sender_name":"Asha Menon","seen_count":3,
     "first_seen":"2026-08-20T09:15:00Z","last_seen":"2026-08-22T11:02:00Z"},
    {"sender_ref":"U02XYZ","sender_name":"Bala R","seen_count":1,
     "first_seen":"2026-08-21T14:00:00Z","last_seen":"2026-08-21T14:00:00Z"}
  ],
  "next_cursor": "eyJmIjoiMjAyNi0wOC0yMFQwOToxNTowMFoifQ"
}`

const oneApproved = `{
  "items": [
    {"sender_ref":"U01ABC","user_id":"` + userID + `","created_at":"2026-08-22T12:00:00Z"}
  ]
}`

// senderRoutes is the lookup chain every sender command walks: the team by
// name, then the channel by name within it.
func senderRoutes() map[string]clitest.Reply {
	routes := teamRoute()
	routes["GET /teams/{id}/channels"] = clitest.Reply{Status: http.StatusOK, Body: oneChannel}
	return routes
}

// --- the pending queue ---

func TestSendersPendingRendersATable(t *testing.T) {
	routes := senderRoutes()
	routes["GET /teams/{id}/channels/{channelID}/senders/pending"] = clitest.Reply{Status: http.StatusOK, Body: twoPending}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "channels", "senders", "pending", "platform", "support")
	if err != nil {
		t.Fatalf("senders pending: %v\n%s", err, out)
	}
	for _, want := range []string{"U01ABC", "Asha Menon", "U02XYZ", "Bala R"} {
		if !strings.Contains(out, want) {
			t.Errorf("the table is missing %q:\n%s", want, out)
		}
	}
}

// How many times somebody has been turned away is the signal that separates a
// person waiting for access from a bot hammering the endpoint.
func TestSendersPendingShowsHowOftenTheyHaveBeenSeen(t *testing.T) {
	routes := senderRoutes()
	routes["GET /teams/{id}/channels/{channelID}/senders/pending"] = clitest.Reply{Status: http.StatusOK, Body: twoPending}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "channels", "senders", "pending", "platform", "support")
	if err != nil {
		t.Fatalf("senders pending: %v\n%s", err, out)
	}
	if !strings.Contains(out, "3") {
		t.Errorf("seen_count is not shown:\n%s", out)
	}
}

func TestSendersPendingPrintsTheEnvelope(t *testing.T) {
	routes := senderRoutes()
	routes["GET /teams/{id}/channels/{channelID}/senders/pending"] = clitest.Reply{Status: http.StatusOK, Body: twoPending}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "channels", "senders", "pending",
		"platform", "support", "-o", "json")
	if err != nil {
		t.Fatalf("senders pending -o json: %v\n%s", err, out)
	}

	var got struct {
		Items      []map[string]any `json:"items"`
		NextCursor *string          `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %+v", got.Items)
	}
	if got.NextCursor == nil {
		t.Error("next_cursor was dropped, so a caller would stop at the first page")
	}
}

func TestSendersPendingSaysWhenTheQueueIsEmpty(t *testing.T) {
	routes := senderRoutes()
	routes["GET /teams/{id}/channels/{channelID}/senders/pending"] = clitest.Reply{Status: http.StatusOK, Body: `{"items":[]}`}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "channels", "senders", "pending", "platform", "support")
	if err != nil {
		t.Fatalf("senders pending: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("an empty queue printed nothing at all")
	}
}

// --- the approved list ---

func TestSendersListRendersATable(t *testing.T) {
	routes := senderRoutes()
	routes["GET /teams/{id}/channels/{channelID}/senders"] = clitest.Reply{Status: http.StatusOK, Body: oneApproved}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "channels", "senders", "list", "platform", "support")
	if err != nil {
		t.Fatalf("senders list: %v\n%s", err, out)
	}
	for _, want := range []string{"U01ABC", userID} {
		if !strings.Contains(out, want) {
			t.Errorf("the table is missing %q:\n%s", want, out)
		}
	}
}

// --- approving ---

func TestApproveSendsTheRefAndTheResolvedUser(t *testing.T) {
	routes := senderRoutes()
	routes["GET /teams/{id}/members"] = clitest.Reply{Status: http.StatusOK, Body: oneMember}

	routes["POST /teams/{id}/channels/{channelID}/senders"] = clitest.Reply{Status: http.StatusNoContent}
	script := clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "channels", "senders", "approve",
		"platform", "support", "U01ABC", "asha")
	if err != nil {
		t.Fatalf("senders approve: %v\n%s", err, out)
	}

	body := postBody(t, script)
	var got struct {
		SenderRef string `json:"sender_ref"`
		UserID    string `json:"user_id"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("the request body is not json: %v (%s)", err, body)
	}
	if got.SenderRef != "U01ABC" {
		t.Errorf("sender_ref = %q", got.SenderRef)
	}
	// Resolved from the team's own member list rather than from GET /users,
	// which would need user:read — a much larger authority than approving.
	if got.UserID != userID {
		t.Errorf("user_id = %q, want the subject resolved to %q", got.UserID, userID)
	}
}

// A user id passed directly must not send the CLI to the member list first.
func TestApproveAcceptsAUserIDWithoutALookup(t *testing.T) {
	routes := senderRoutes()
	routes["POST /teams/{id}/channels/{channelID}/senders"] = clitest.Reply{Status: http.StatusNoContent}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "channels", "senders", "approve",
		"platform", "support", "U01ABC", userID)
	if err != nil {
		t.Fatalf("senders approve with an id: %v\n%s", err, out)
	}
}

func TestApproveReportsARefusal(t *testing.T) {
	routes := senderRoutes()
	routes["GET /teams/{id}/members"] = clitest.Reply{Status: http.StatusOK, Body: oneMember}
	routes["POST /teams/{id}/channels/{channelID}/senders"] = clitest.Reply{
		Status: http.StatusBadRequest,
		Body:   "that user is not in this team",
	}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "channels", "senders", "approve",
		"platform", "support", "U01ABC", "asha")
	if err == nil {
		t.Fatalf("a refused approval reported success:\n%s", out)
	}
	if !strings.Contains(err.Error(), "not in this team") {
		t.Errorf("the brain's reason was dropped: %v", err)
	}
}

// --- removing ---

func TestRemoveSendsTheRefAsAQueryParameter(t *testing.T) {
	routes := senderRoutes()

	routes["DELETE /teams/{id}/channels/{channelID}/senders"] = clitest.Reply{Status: http.StatusNoContent}
	script := clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "channels", "senders", "remove",
		"platform", "support", "U01ABC")
	if err != nil {
		t.Fatalf("senders remove: %v\n%s", err, out)
	}
	if query := deleteQuery(t, script); !strings.Contains(query, "ref=U01ABC") {
		t.Errorf("query = %q, want the ref in it", query)
	}
}

// The ref is provider-controlled text and the whole reason it is not a path
// segment. It has to survive the wire exactly, or an admin cannot remove the
// sender they can see.
func TestRemoveSurvivesARefThatNeedsEscaping(t *testing.T) {
	for name, ref := range map[string]string{
		"a slash":      "team/alice",
		"dot dot":      "..",
		"a space":      "Asha Menon",
		"an ampersand": "a&ref=b",
		"a hash":       "a#b",
		"non-ascii":    "señor",
	} {
		t.Run(name, func(t *testing.T) {
			routes := senderRoutes()

			routes["DELETE /teams/{id}/channels/{channelID}/senders"] = clitest.Reply{Status: http.StatusNoContent}
			script := clitest.Routes(t, routes)

			out, err := clitest.Execute(t, commands, "channels", "senders", "remove",
				"platform", "support", ref)
			if err != nil {
				t.Fatalf("removing %q: %v\n%s", ref, err, out)
			}

			values, err := url.ParseQuery(deleteQuery(t, script))
			if err != nil {
				t.Fatalf("the query does not parse: %v", err)
			}
			if got := values.Get("ref"); got != ref {
				t.Errorf("the brain received %q, want %q", got, ref)
			}
		})
	}
}

func TestSenderCommandsNeedTheirArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"pending with no channel": {"channels", "senders", "pending", "platform"},
		"list with no channel":    {"channels", "senders", "list", "platform"},
		"approve with no user":    {"channels", "senders", "approve", "platform", "support", "U01ABC"},
		"approve with no ref":     {"channels", "senders", "approve", "platform", "support"},
		"remove with no ref":      {"channels", "senders", "remove", "platform", "support"},
	} {
		t.Run(name, func(t *testing.T) {
			clitest.Routes(t, senderRoutes())

			if out, err := clitest.Execute(t, commands, args...); err == nil {
				t.Errorf("%s was accepted:\n%s", name, out)
			}
		})
	}
}

func deleteQuery(t *testing.T, script *clitest.Transcript) string {
	t.Helper()

	for _, e := range script.All() {
		if e.Method == http.MethodDelete {
			return e.Query
		}
	}
	t.Fatal("nothing was deleted")
	return ""
}

// A pending sender is by definition somebody nobody has approved, reached
// through a public hook. Their display name and their ref are both text they
// chose, and this is the one command that puts both on a terminal.
func TestSendersPendingNeutralisesHostileText(t *testing.T) {
	routes := senderRoutes()
	routes["GET /teams/{id}/channels/{channelID}/senders/pending"] = clitest.Reply{
		Status: http.StatusOK,
		Body: `{"items":[{"sender_ref":"U01\u001b[2JABC","sender_name":"Asha\u001b]0;pwned\u0007 Menon",
		  "seen_count":3,"first_seen":"2026-08-20T09:15:00Z","last_seen":"2026-08-22T11:02:00Z"}]}`,
	}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "channels", "senders", "pending", "platform", "support")
	if err != nil {
		t.Fatalf("senders pending: %v\n%s", err, out)
	}

	for _, escape := range []string{"\x1b[2J", "\x1b]0;", "\x07"} {
		if strings.Contains(out, escape) {
			t.Errorf("%q reached the terminal:\n%q", escape, out)
		}
	}
	// Neutralised, not dropped: an admin has to be able to read the ref in
	// order to type it into `oa channels senders approve`.
	for _, want := range []string{"U01", "ABC", "Asha", "Menon"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q was lost, so the row cannot be acted on:\n%s", want, out)
		}
	}
}

func TestSendersListNeutralisesAHostileRef(t *testing.T) {
	routes := senderRoutes()
	routes["GET /teams/{id}/channels/{channelID}/senders"] = clitest.Reply{
		Status: http.StatusOK,
		Body: `{"items":[{"sender_ref":"U01\u001b[2JABC","user_id":"` + userID + `",
		  "created_at":"2026-08-22T12:00:00Z"}]}`,
	}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "channels", "senders", "list", "platform", "support")
	if err != nil {
		t.Fatalf("senders list: %v\n%s", err, out)
	}
	if strings.Contains(out, "\x1b[2J") {
		t.Errorf("an escape sequence reached the terminal:\n%q", out)
	}
}

// The ref reaching these two came from argv, which looks like it makes it
// trustworthy. It does not: an admin copies it out of the pending queue, so an
// escape sequence takes one trip through their clipboard and lands on the
// least-scrutinised output in the tool — the line that says it worked.
func TestApproveNeutralisesTheRefItEchoes(t *testing.T) {
	routes := senderRoutes()
	routes["POST /teams/{id}/channels/{channelID}/senders"] = clitest.Reply{Status: http.StatusNoContent}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "channels", "senders", "approve",
		"platform", "support", "U01\x1b[2JABC", userID)
	if err != nil {
		t.Fatalf("senders approve: %v\n%s", err, out)
	}
	if strings.Contains(out, "\x1b[2J") {
		t.Errorf("an escape sequence reached the terminal:\n%q", out)
	}
	if !strings.Contains(out, "approved") {
		t.Errorf("the confirmation is missing:\n%s", out)
	}
}

func TestRemoveNeutralisesTheRefItEchoes(t *testing.T) {
	routes := senderRoutes()
	routes["DELETE /teams/{id}/channels/{channelID}/senders"] = clitest.Reply{Status: http.StatusNoContent}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "channels", "senders", "remove",
		"platform", "support", "U01\x1b[2JABC")
	if err != nil {
		t.Fatalf("senders remove: %v\n%s", err, out)
	}
	if strings.Contains(out, "\x1b[2J") {
		t.Errorf("an escape sequence reached the terminal:\n%q", out)
	}
	if !strings.Contains(out, "removed") {
		t.Errorf("the confirmation is missing:\n%s", out)
	}
}
