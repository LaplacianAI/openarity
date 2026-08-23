package sessions

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

var commands = []clitest.Build{New}

const (
	teamID    = "6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d"
	channelID = "3f9a41c2-7e58-4b0d-9a11-8d2c0e5b71c1"
	sessionID = "c7e2a940-51f6-4c3b-8d7a-0e9b3f1a6c52"
	userID    = "a1c3e5f7-b2d4-4a6c-8e0f-1a3c5e7f9b1d"
)

const oneTeam = `{"items":[{"id":"` + teamID + `","name":"platform","role":"admin"}]}`

const oneChannel = `{"items":[{"id":"` + channelID + `","team_id":"` + teamID + `","provider":"custom","name":"support"}]}`

// One session that arrived on a channel and one that did not. The second is
// the shape a dashboard session has, and the reason channel_id and
// provider_ref are pointers.
const twoSessions = `{
  "items": [
    {"id":"` + sessionID + `","channel_id":"` + channelID + `","provider_ref":"C123:1699999999.000100",
     "kind":"thread","status":"open",
     "started_at":"2026-08-22T09:00:00Z","last_message_at":"2026-08-22T11:30:00Z"},
    {"id":"9d4b2e60-3a17-4f8c-b2e5-7c1d0a4f8b93",
     "kind":"direct","status":"open",
     "started_at":"2026-08-21T08:00:00Z","last_message_at":"2026-08-21T08:05:00Z"}
  ],
  "next_cursor": "eyJsIjoiMjAyNi0wOC0yMVQwODowNTowMFoifQ"
}`

// One message with a sent_at and one without: a provider that does not say
// when the sender sent it is the common case, not the exception.
const twoMessages = `{
  "items": [
    {"id":"1b0f5c8a-2d64-4e19-9f37-5a8c6b204e71","external_id":"m-2","user_id":"` + userID + `",
     "text":"deploying now","sent_at":"2026-08-22T11:29:58Z","received_at":"2026-08-22T11:30:00Z"},
    {"id":"2c1a6d9b-3e75-4f2a-8043-6b9d7c315f82","external_id":"m-1","user_id":"` + userID + `",
     "text":"what's our deploy status?","received_at":"2026-08-22T09:00:00Z"}
  ]
}`

func teamRoute() map[string]clitest.Reply {
	return map[string]clitest.Reply{"GET /teams": {Status: http.StatusOK, Body: oneTeam}}
}

// --- listing a team's sessions ---

func TestSessionsListRendersATable(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/sessions"] = clitest.Reply{Status: http.StatusOK, Body: twoSessions}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "sessions", "list", "platform")
	if err != nil {
		t.Fatalf("sessions list: %v\n%s", err, out)
	}
	for _, want := range []string{sessionID, "thread", "direct"} {
		if !strings.Contains(out, want) {
			t.Errorf("the table is missing %q:\n%s", want, out)
		}
	}
}

// A session belongs to a team, and a channel is only one way one starts. The
// team-wide list is therefore the default, and it must not need a channel to
// answer — nor look one up.
func TestSessionsListWithoutAChannelAsksTheTeam(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/sessions"] = clitest.Reply{Status: http.StatusOK, Body: twoSessions}
	script := clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "sessions", "list", "platform")
	if err != nil {
		t.Fatalf("sessions list: %v\n%s", err, out)
	}
	if n := script.Calls(http.MethodGet, "/teams/"+teamID+"/channels"); n != 0 {
		t.Errorf("a channel was looked up %d times for a team-wide list", n)
	}
}

func TestSessionsListWithAChannelAsksThatChannel(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/channels"] = clitest.Reply{Status: http.StatusOK, Body: oneChannel}
	routes["GET /teams/{id}/channels/{channelID}/sessions"] = clitest.Reply{Status: http.StatusOK, Body: twoSessions}
	script := clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "sessions", "list", "platform", "--channel", "support")
	if err != nil {
		t.Fatalf("sessions list --channel: %v\n%s", err, out)
	}

	if n := script.Calls(http.MethodGet, "/teams/"+teamID+"/channels/"+channelID+"/sessions"); n != 1 {
		t.Errorf("the channel's sessions were asked for %d times:\n%s", n, out)
	}
	// Asking both would show a session twice and double the cost of a page.
	if n := script.Calls(http.MethodGet, "/teams/"+teamID+"/sessions"); n != 0 {
		t.Errorf("the team-wide list was asked for as well, %d times", n)
	}
}

// A channel is named by name or by id everywhere else, and a session list is
// not the place to make somebody find a uuid.
func TestSessionsListResolvesTheChannelByName(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/channels"] = clitest.Reply{Status: http.StatusOK, Body: oneChannel}
	routes["GET /teams/{id}/channels/{channelID}/sessions"] = clitest.Reply{Status: http.StatusOK, Body: twoSessions}
	script := clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "sessions", "list", "platform", "--channel", "support")
	if err != nil {
		t.Fatalf("sessions list --channel support: %v\n%s", err, out)
	}
	if n := script.Calls(http.MethodGet, "/teams/"+teamID+"/channels"); n != 1 {
		t.Errorf("the channel name was resolved %d times, want once", n)
	}
}

// A session that started somewhere other than a channel has no channel_id and
// no provider_ref. Both are pointers, so a row that reads them without asking
// panics on exactly the session the team started from the dashboard.
func TestSessionsListShowsASessionThatArrivedOnNoChannel(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/sessions"] = clitest.Reply{
		Status: http.StatusOK,
		Body: `{"items":[{"id":"` + sessionID + `","kind":"direct","status":"open",
		  "started_at":"2026-08-21T08:00:00Z","last_message_at":"2026-08-21T08:05:00Z"}]}`,
	}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "sessions", "list", "platform")
	if err != nil {
		t.Fatalf("a session with no channel: %v\n%s", err, out)
	}
	if !strings.Contains(out, sessionID) {
		t.Errorf("the session is missing:\n%s", out)
	}
}

func TestSessionsListPrintsTheEnvelope(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/sessions"] = clitest.Reply{Status: http.StatusOK, Body: twoSessions}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "sessions", "list", "platform", "-o", "json")
	if err != nil {
		t.Fatalf("sessions list -o json: %v\n%s", err, out)
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

func TestSessionsListSaysWhenThereAreNone(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/sessions"] = clitest.Reply{Status: http.StatusOK, Body: `{"items":[]}`}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "sessions", "list", "platform")
	if err != nil {
		t.Fatalf("sessions list: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("an empty list printed nothing at all")
	}
}

func TestSessionsListForwardsTheCursor(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/sessions"] = clitest.Reply{Status: http.StatusOK, Body: `{"items":[]}`}
	script := clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "sessions", "list", "platform", "--cursor", "abc123")
	if err != nil {
		t.Fatalf("sessions list --cursor: %v\n%s", err, out)
	}

	if query := queryFor(t, script, "/teams/"+teamID+"/sessions"); !strings.Contains(query, "cursor=abc123") {
		t.Errorf("query = %q, want the cursor in it", query)
	}
}

// --- reading one ---

func TestSessionsReadRendersTheConversation(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/sessions/{sessionID}/messages"] = clitest.Reply{Status: http.StatusOK, Body: twoMessages}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "sessions", "read", "platform", sessionID)
	if err != nil {
		t.Fatalf("sessions read: %v\n%s", err, out)
	}
	for _, want := range []string{"deploying now", "deploy status"} {
		if !strings.Contains(out, want) {
			t.Errorf("the conversation is missing %q:\n%s", want, out)
		}
	}
}

// sent_at is absent whenever the provider did not say, which is most of them.
// Reading it without asking would panic on the ordinary case.
func TestSessionsReadShowsAMessageWithNoSentAt(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/sessions/{sessionID}/messages"] = clitest.Reply{
		Status: http.StatusOK,
		Body: `{"items":[{"id":"1b0f5c8a-2d64-4e19-9f37-5a8c6b204e71","external_id":"m-1",
		  "user_id":"` + userID + `","text":"no clock here","received_at":"2026-08-22T09:00:00Z"}]}`,
	}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "sessions", "read", "platform", sessionID)
	if err != nil {
		t.Fatalf("a message with no sent_at: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no clock here") {
		t.Errorf("the message is missing:\n%s", out)
	}
}

// The hook URL is public, so text is bytes a stranger chose. A terminal reads
// \x1b[2J as "clear the screen"; this is the only command that renders such
// text, and it is the last place it can be made safe.
func TestSessionsReadNeutralisesHostileText(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/sessions/{sessionID}/messages"] = clitest.Reply{
		Status: http.StatusOK,
		Body: `{"items":[{"id":"1b0f5c8a-2d64-4e19-9f37-5a8c6b204e71","external_id":"m-1",
		  "user_id":"` + userID + `","text":"deploy \u001b[2J\u001b[H now","received_at":"2026-08-22T09:00:00Z"}]}`,
	}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "sessions", "read", "platform", sessionID)
	if err != nil {
		t.Fatalf("sessions read: %v\n%s", err, out)
	}
	if strings.Contains(out, "\x1b[2J") {
		t.Errorf("an escape sequence reached the terminal:\n%q", out)
	}
	if !strings.Contains(out, "deploy") {
		t.Errorf("the readable part was lost:\n%s", out)
	}
}

// A message that spans two lines would put its second half where a column is
// not, and a sender chooses how many lines they send.
func TestSessionsReadKeepsAMessageOnOneRow(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/sessions/{sessionID}/messages"] = clitest.Reply{
		Status: http.StatusOK,
		Body: `{"items":[{"id":"1b0f5c8a-2d64-4e19-9f37-5a8c6b204e71","external_id":"m-1",
		  "user_id":"` + userID + `","text":"first\nsecond\nthird","received_at":"2026-08-22T09:00:00Z"}]}`,
	}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "sessions", "read", "platform", sessionID)
	if err != nil {
		t.Fatalf("sessions read: %v\n%s", err, out)
	}
	if n := strings.Count(strings.TrimSpace(out), "\n"); n != 0 {
		t.Errorf("one message became %d rows:\n%s", n+1, out)
	}
}

// -o json is what a script reads, and a script wants what arrived rather than
// what is safe to print. Quoting there would corrupt the data twice over,
// since json.Marshal escapes it again.
func TestSessionsReadPrintsTheTextUnquotedAsJSON(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/sessions/{sessionID}/messages"] = clitest.Reply{Status: http.StatusOK, Body: twoMessages}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "sessions", "read", "platform", sessionID, "-o", "json")
	if err != nil {
		t.Fatalf("sessions read -o json: %v\n%s", err, out)
	}

	var got struct {
		Items []struct {
			Text string `json:"text"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %+v", got.Items)
	}
	if got.Items[0].Text != "deploying now" {
		t.Errorf("text = %q, want it exactly as it arrived", got.Items[0].Text)
	}
}

// A session has no name to resolve — only an id, copied from the list. A name
// therefore cannot be looked up, and saying so locally is better than sending
// the brain something it will refuse.
func TestSessionsReadRefusesANameWithoutAskingTheBrain(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/sessions/{sessionID}/messages"] = clitest.Reply{Status: http.StatusOK, Body: twoMessages}
	script := clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "sessions", "read", "platform", "support")
	if err == nil {
		t.Fatalf("a session name was accepted:\n%s", out)
	}
	if !strings.Contains(err.Error(), "id") {
		t.Errorf("the error does not say an id is wanted: %v", err)
	}

	for _, e := range script.All() {
		if strings.Contains(e.Path, "/sessions/") {
			t.Errorf("the brain was asked anyway: %s %s", e.Method, e.Path)
		}
	}
}

// A session in another team answers 404 rather than 403, and the reason must
// survive to the person reading it.
func TestSessionsReadReportsARefusal(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/sessions/{sessionID}/messages"] = clitest.Reply{
		Status: http.StatusNotFound,
		Body:   "no such session",
	}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "sessions", "read", "platform", sessionID)
	if err == nil {
		t.Fatalf("a refused read reported success:\n%s", out)
	}
	if !strings.Contains(err.Error(), "no such session") {
		t.Errorf("the brain's reason was dropped: %v", err)
	}
}

func TestSessionCommandsNeedTheirArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"list with no team":    {"sessions", "list"},
		"read with no session": {"sessions", "read", "platform"},
		"read with no team":    {"sessions", "read"},
	} {
		t.Run(name, func(t *testing.T) {
			clitest.Routes(t, teamRoute())

			if out, err := clitest.Execute(t, commands, args...); err == nil {
				t.Errorf("%s was accepted:\n%s", name, out)
			}
		})
	}
}

func queryFor(t *testing.T, script *clitest.Transcript, path string) string {
	t.Helper()

	for _, e := range script.All() {
		if e.Path == path {
			return e.Query
		}
	}
	t.Fatalf("nothing was sent to %s", path)
	return ""
}

// A subcommand missed in AddCommand gets no warning from any linter — the
// constructor is called, so nothing is unused. --help is what notices.
func TestEveryVerbIsRegistered(t *testing.T) {
	clitest.Routes(t, teamRoute())

	out, err := clitest.Execute(t, commands, "sessions", "--help")
	if err != nil {
		t.Fatalf("sessions --help: %v\n%s", err, out)
	}
	for _, verb := range []string{"list", "read"} {
		if !strings.Contains(out, verb) {
			t.Errorf("%q is not on `oa sessions`:\n%s", verb, out)
		}
	}
}

func TestEveryFormatPrintsTheSameConversation(t *testing.T) {
	for _, format := range []string{"table", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			routes := teamRoute()
			routes["GET /teams/{id}/sessions/{sessionID}/messages"] = clitest.Reply{
				Status: http.StatusOK, Body: twoMessages,
			}
			clitest.Routes(t, routes)

			out, err := clitest.Execute(t, commands, "sessions", "read", "platform", sessionID, "-o", format)
			if err != nil {
				t.Fatalf("sessions read -o %s: %v\n%s", format, err, out)
			}
			if !strings.Contains(out, "deploying now") {
				t.Errorf("-o %s lost the message:\n%s", format, out)
			}
		})
	}
}

func TestEveryFormatPrintsTheSameSessions(t *testing.T) {
	for _, format := range []string{"table", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			routes := teamRoute()
			routes["GET /teams/{id}/sessions"] = clitest.Reply{Status: http.StatusOK, Body: twoSessions}
			clitest.Routes(t, routes)

			out, err := clitest.Execute(t, commands, "sessions", "list", "platform", "-o", format)
			if err != nil {
				t.Fatalf("sessions list -o %s: %v\n%s", format, err, out)
			}
			if !strings.Contains(out, sessionID) {
				t.Errorf("-o %s lost the session:\n%s", format, out)
			}
		})
	}
}

// A ref is whatever the adapter decided identifies a conversation. For the
// custom adapter that is whatever a public unauthenticated hook sent, so its
// charset is guaranteed by nothing but a length check.
func TestSessionsListNeutralisesAHostileRef(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/sessions"] = clitest.Reply{
		Status: http.StatusOK,
		Body: `{"items":[{"id":"` + sessionID + `","channel_id":"` + channelID + `",
		  "provider_ref":"C123\u001b[2J\u001b[H","kind":"thread","status":"open",
		  "started_at":"2026-08-22T09:00:00Z","last_message_at":"2026-08-22T11:30:00Z"}]}`,
	}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "sessions", "list", "platform")
	if err != nil {
		t.Fatalf("sessions list: %v\n%s", err, out)
	}
	if strings.Contains(out, "\x1b[2J") {
		t.Errorf("an escape sequence reached the terminal:\n%q", out)
	}
	if !strings.Contains(out, "C123") {
		t.Errorf("the readable part of the ref was lost:\n%s", out)
	}
}

// received_at is the order, and it is the only timestamp the brain controls.
// A table that shows nothing, or shows the sender's own clock, hides that.
func TestSessionsReadShowsWhenAMessageArrived(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/sessions/{sessionID}/messages"] = clitest.Reply{Status: http.StatusOK, Body: twoMessages}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "sessions", "read", "platform", sessionID)
	if err != nil {
		t.Fatalf("sessions read: %v\n%s", err, out)
	}
	// 11:30 is received_at; the sender claims 11:29:58, so the two differ.
	if !strings.Contains(out, "2026-08-22 11:30") {
		t.Errorf("received_at is not shown:\n%s", out)
	}
	if strings.Contains(out, "11:29") {
		t.Errorf("sent_at is shown, so the order is the sender's to choose:\n%s", out)
	}
}
