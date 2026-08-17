package teams

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const (
	teamID = "6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d"
	userID = "9c4e1a2b-3d5f-4a6b-8c7d-1e2f3a4b5c6d"
)

const twoMembers = `{
  "items": [
    {"user_id": "9c4e1a2b-3d5f-4a6b-8c7d-1e2f3a4b5c6d", "subject": "ak", "role": "admin",
     "email": "ak@example.com"},
    {"user_id": "0f1e2d3c-4b5a-4968-8776-655443332211", "subject": "ro", "role": "developer"}
  ],
  "next_cursor": "eyJjIjoiMjAyNi0wOC0xNFQwMDowMDowMFoifQ"
}`

func TestMembersListRendersATable(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, twoMembers)

	out, err := clitest.Execute(t, commands, "teams", "members", "list", teamID)
	if err != nil {
		t.Fatalf("teams members list: %v", err)
	}
	for _, want := range []string{"ak", "admin", "ak@example.com", "ro", "developer"} {
		if !strings.Contains(out, want) {
			t.Errorf("the table is missing %q:\n%s", want, out)
		}
	}
}

// The field-name half of the same defect. yaml.v3 lowercases the Go field
// name when there is no yaml tag, so UserID rendered as `userid` while json
// said `user_id` — one command describing the same field by two names, and
// only the json one matching the API it came from.
//
// The tags are generated from oapi-codegen.yaml, so this is asserted through
// a command: no file in this package fails if that setting goes away.
func TestYAMLNamesTheFieldsTheWayJSONDoes(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, twoMembers)

	out, err := clitest.Execute(t, commands, "teams", "members", "list", teamID, "-o", "yaml")
	if err != nil {
		t.Fatalf("teams members list -o yaml: %v", err)
	}

	if !strings.Contains(out, "user_id:") {
		t.Errorf("yaml does not carry user_id:\n%s", out)
	}
	if strings.Contains(out, "userid:") {
		t.Errorf("yaml renamed the field to userid:\n%s", out)
	}
	// The second member has no email. Absent has to mean absent here too.
	if strings.Contains(out, "null") {
		t.Errorf("an absent email rendered as null:\n%s", out)
	}

	var got struct {
		Items []struct {
			UserID  string `yaml:"user_id"`
			Subject string `yaml:"subject"`
		} `yaml:"items"`
	}
	if err := yaml.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not yaml: %v\n%s", err, out)
	}
	if len(got.Items) != 2 || got.Items[0].UserID != userID {
		t.Errorf("items = %+v", got.Items)
	}
}

// The team id is the whole point of the path — sending it to the wrong URL
// would list somebody else's members and look entirely plausible.
func TestMembersListAsksAboutTheTeamGiven(t *testing.T) {
	got := clitest.BrainStub(t, http.StatusOK, twoMembers)

	if _, err := clitest.Execute(t, commands, "teams", "members", "list", teamID); err != nil {
		t.Fatalf("teams members list: %v", err)
	}

	got.Lock()
	defer got.Unlock()

	if got.Path != "/teams/"+teamID+"/members" {
		t.Errorf("path = %q", got.Path)
	}
	if got.Method != http.MethodGet {
		t.Errorf("method = %s, want GET", got.Method)
	}
}

// The envelope is the contract. Printing the items alone would drop
// next_cursor and every consumer would believe it had the last page.
func TestMembersListPrintsTheEnvelope(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, twoMembers)

	out, err := clitest.Execute(t, commands, "teams", "members", "list", teamID, "-o", "json")
	if err != nil {
		t.Fatalf("teams members list -o json: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}

	items, ok := got["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("items is not two rows: %#v", got["items"])
	}
	if got["next_cursor"] != "eyJjIjoiMjAyNi0wOC0xNFQwMDowMDowMFoifQ" {
		t.Errorf("next_cursor = %#v, want it carried through verbatim", got["next_cursor"])
	}
}

// The hint has to name the team it is a hint for, or pasting it lists the
// wrong one — and it must never reach a document a parser will read.
func TestTheMembersCursorHintNamesTheTeamAndReachesAPersonOnly(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, twoMembers)

	table, err := clitest.Execute(t, commands, "teams", "members", "list", teamID)
	if err != nil {
		t.Fatalf("teams members list: %v", err)
	}
	if !strings.Contains(table, "--cursor") || !strings.Contains(table, teamID) {
		t.Errorf("the hint does not say how to fetch the next page:\n%s", table)
	}

	asJSON, err := clitest.Execute(t, commands, "teams", "members", "list", teamID, "-o", "json")
	if err != nil {
		t.Fatalf("teams members list -o json: %v", err)
	}
	if strings.Contains(asJSON, "--cursor") {
		t.Errorf("the hint corrupted the document:\n%s", asJSON)
	}
}

// A provider that released no email is normal. An empty cell would read as a
// bug in the CLI rather than a fact about the account.
func TestAMemberWithNoEmailSaysSo(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, twoMembers)

	out, err := clitest.Execute(t, commands, "teams", "members", "list", teamID)
	if err != nil {
		t.Fatalf("teams members list: %v", err)
	}
	if !strings.Contains(out, "no email") {
		t.Errorf("a member with no email rendered as nothing:\n%s", out)
	}
}

// A team a super admin just created has no members, so this is the first thing
// they see. It must be an empty array under -o json: a nil slice marshals to
// null and `jq length` fails on null.
func TestMembersListWithNothingToShow(t *testing.T) {
	clitest.BrainStub(t, http.StatusOK, `{"items": []}`)

	table, err := clitest.Execute(t, commands, "teams", "members", "list", teamID)
	if err != nil {
		t.Fatalf("teams members list: %v", err)
	}
	if !strings.Contains(table, "no members") {
		t.Errorf("an empty list said nothing:\n%s", table)
	}

	asJSON, err := clitest.Execute(t, commands, "teams", "members", "list", teamID, "-o", "json")
	if err != nil {
		t.Fatalf("teams members list -o json: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(asJSON), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, asJSON)
	}
	if items, ok := got["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("items = %#v, want an empty array", got["items"])
	}
}

// Int32Var defaults to zero and the brain answers 400 to a limit of zero, so
// sending it unconditionally would break every default invocation.
func TestMembersListSendsNoLimitUnlessAsked(t *testing.T) {
	got := clitest.BrainStub(t, http.StatusOK, twoMembers)

	if _, err := clitest.Execute(t, commands, "teams", "members", "list", teamID); err != nil {
		t.Fatalf("teams members list: %v", err)
	}

	got.Lock()
	defer got.Unlock()

	if strings.Contains(got.Query, "limit") {
		t.Errorf("query = %q, want no limit when the flag was not given", got.Query)
	}
}

func TestMembersListSendsTheLimitAndCursor(t *testing.T) {
	got := clitest.BrainStub(t, http.StatusOK, twoMembers)

	_, err := clitest.Execute(t, commands,
		"teams", "members", "list", teamID, "--limit", "5", "--cursor", "abc")
	if err != nil {
		t.Fatalf("teams members list: %v", err)
	}

	got.Lock()
	defer got.Unlock()

	if !strings.Contains(got.Query, "limit=5") {
		t.Errorf("query = %q, want limit=5", got.Query)
	}
	if !strings.Contains(got.Query, "cursor=abc") {
		t.Errorf("query = %q, want cursor=abc", got.Query)
	}
}

// Anything that is not a uuid is a name now, so it is looked up rather than
// refused. A name nobody has has to say so in terms of the name — "not a uuid"
// would be describing an argument the person never intended to type.
func TestAnUnknownTeamNameIsReportedAsAName(t *testing.T) {
	script := clitest.Routes(t, map[string]clitest.Reply{
		"GET /teams": {Status: http.StatusOK, Body: `{"items":[{"id":"` + teamID + `","name":"platform"}]}`},
	})

	_, err := clitest.Execute(t, commands, "teams", "members", "list", "payments")
	if err == nil {
		t.Fatal("a team nobody has was accepted")
	}
	if !strings.Contains(err.Error(), "payments") {
		t.Errorf("the error does not name what was typed: %v", err)
	}
	if strings.Contains(err.Error(), "uuid") {
		t.Errorf("the error blames the format of a name: %v", err)
	}

	if n := script.Calls(http.MethodGet, "/teams/"+teamID+"/members"); n != 0 {
		t.Errorf("the members of some team were listed anyway (%d calls)", n)
	}
}

func TestMembersAddPostsTheUserAndRole(t *testing.T) {
	got := clitest.BrainStub(t, http.StatusNoContent, "")

	out, err := clitest.Execute(t, commands,
		"teams", "members", "add", teamID, userID, "--role", "developer")
	if err != nil {
		t.Fatalf("teams members add: %v", err)
	}
	if !strings.Contains(out, "added") {
		t.Errorf("the confirmation is missing:\n%s", out)
	}

	got.Lock()
	defer got.Unlock()

	if got.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", got.Method)
	}
	if got.Path != "/teams/"+teamID+"/members" {
		t.Errorf("path = %q", got.Path)
	}
	if !strings.Contains(got.Body, `"user_id":"`+userID+`"`) {
		t.Errorf("body = %q, want the user id", got.Body)
	}
	if !strings.Contains(got.Body, `"role":"developer"`) {
		t.Errorf("body = %q, want the role", got.Body)
	}
}

// A member with no role can do nothing, so the brain would refuse it anyway —
// refusing here saves the round trip and names the flag.
func TestMembersAddRequiresARole(t *testing.T) {
	got := clitest.BrainStub(t, http.StatusNoContent, "")

	_, err := clitest.Execute(t, commands, "teams", "members", "add", teamID, userID)
	if err == nil {
		t.Fatal("a member was added with no role")
	}
	if !strings.Contains(err.Error(), "role") {
		t.Errorf("the error does not name the flag: %v", err)
	}

	got.Lock()
	defer got.Unlock()

	if got.Method != "" {
		t.Errorf("a request was sent with no role: %s %s", got.Method, got.Path)
	}
}

// A whitespace role would be posted as a blank string and rejected by a
// foreign key, which comes back as an unhelpful 400.
func TestMembersAddRefusesAWhitespaceRole(t *testing.T) {
	got := clitest.BrainStub(t, http.StatusNoContent, "")

	_, err := clitest.Execute(t, commands,
		"teams", "members", "add", teamID, userID, "--role", "   ")
	if err == nil {
		t.Fatal("a role of only whitespace was accepted")
	}

	got.Lock()
	defer got.Unlock()

	if got.Method != "" {
		t.Errorf("a request was sent for a role that could not be valid: %s", got.Method)
	}
}

// The whole point of the design: a subject travels in the body and the brain
// resolves it. Two requests, not three, and — the part that matters — no call
// to /users, so adding somebody never needs permission to read the directory.
func TestASubjectIsSentInTheBodyRatherThanLookedUp(t *testing.T) {
	script := clitest.Routes(t, map[string]clitest.Reply{
		"GET /teams":               {Status: http.StatusOK, Body: `{"items":[{"id":"` + teamID + `","name":"platform"}]}`},
		"GET /users":               {Status: http.StatusOK, Body: `{"items":[{"id":"` + userID + `","subject":"alice"}]}`},
		"POST /teams/{id}/members": {Status: http.StatusNoContent, Body: ""},
	})

	if _, err := clitest.Execute(t, commands,
		"teams", "members", "add", "platform", "alice", "--role", "member"); err != nil {
		t.Fatalf("teams members add: %v", err)
	}

	if n := script.Calls(http.MethodGet, "/users"); n != 0 {
		t.Errorf("the directory was read %d times to add somebody by name", n)
	}

	seen := script.All()
	if len(seen) != 2 {
		t.Fatalf("made %d requests, want one team lookup and one write: %+v", len(seen), seen)
	}

	last := seen[len(seen)-1]
	if last.Method != http.MethodPost || last.Path != "/teams/"+teamID+"/members" {
		t.Fatalf("the write was %s %s, want a POST against the resolved team", last.Method, last.Path)
	}
	if !strings.Contains(last.Body, `"subject":"alice"`) {
		t.Errorf("body = %q, want the subject verbatim", last.Body)
	}
	if strings.Contains(last.Body, "user_id") {
		t.Errorf("body = %q, want no user_id — exactly one of the two is allowed", last.Body)
	}
}

// An id given directly is sent as user_id, which is the unambiguous form and
// the only one that works when two providers issue the same subject.
func TestAUserIDIsSentAsAnIDNotASubject(t *testing.T) {
	script := clitest.Routes(t, map[string]clitest.Reply{
		"POST /teams/{id}/members": {Status: http.StatusNoContent, Body: ""},
	})

	if _, err := clitest.Execute(t, commands,
		"teams", "members", "add", teamID, userID, "--role", "member"); err != nil {
		t.Fatalf("teams members add: %v", err)
	}

	seen := script.All()
	if len(seen) != 1 {
		t.Fatalf("made %d requests, want one: %+v", len(seen), seen)
	}
	if !strings.Contains(seen[0].Body, `"user_id":"`+userID+`"`) {
		t.Errorf("body = %q, want the id as user_id", seen[0].Body)
	}
	if strings.Contains(seen[0].Body, "subject") {
		t.Errorf("body = %q, want no subject alongside the id", seen[0].Body)
	}
}

// A subject nobody has is the brain's answer now, not the CLI's. What the CLI
// still owes is not swallowing it — reporting 404 as success would leave an
// admin believing somebody was added.
func TestAnUnknownSubjectIsReportedRatherThanSwallowed(t *testing.T) {
	clitest.Routes(t, map[string]clitest.Reply{
		"POST /teams/{id}/members": {Status: http.StatusNotFound, Body: "no user has that subject"},
	})

	_, err := clitest.Execute(t, commands,
		"teams", "members", "add", teamID, "nobody", "--role", "member")
	if err == nil {
		t.Fatal("a 404 was reported as a successful add")
	}
}

// Two providers can issue the same subject, and the brain refuses rather than
// guessing. The ids it names are the way forward, so they have to survive into
// what the person reads.
func TestAnAmbiguousSubjectKeepsTheIDsTheBrainOffered(t *testing.T) {
	other := "0f1e2d3c-4b5a-4968-8776-655443332211"
	clitest.Routes(t, map[string]clitest.Reply{
		"POST /teams/{id}/members": {
			Status: http.StatusConflict,
			Body:   "2 users have that subject — retry with user_id: " + userID + ", " + other,
		},
	})

	_, err := clitest.Execute(t, commands,
		"teams", "members", "add", teamID, "alice", "--role", "member")
	if err == nil {
		t.Fatal("an ambiguous subject was reported as a successful add")
	}
	for _, want := range []string{userID, other} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error drops %s, which is the way forward: %v", want, err)
		}
	}
}

// Both are knowable from the argument list, so rejecting them must not cost a
// round trip.
func TestArgumentsAreCheckedBeforeAnythingIsSent(t *testing.T) {
	for name, args := range map[string][]string{
		"an empty role":    {"teams", "members", "add", "platform", "alice", "--role", "   "},
		"an empty subject": {"teams", "members", "add", "platform", "   ", "--role", "member"},
	} {
		t.Run(name, func(t *testing.T) {
			script := clitest.Routes(t, map[string]clitest.Reply{
				"GET /teams": {Status: http.StatusOK, Body: `{"items":[{"id":"` + teamID + `","name":"platform"}]}`},
			})

			if _, err := clitest.Execute(t, commands, args...); err == nil {
				t.Fatalf("%s was accepted", name)
			}
			if seen := script.All(); len(seen) != 0 {
				t.Errorf("a request went out before the arguments were checked: %+v", seen)
			}
		})
	}
}

// Removing needs an id in the path, so a subject still has to be resolved —
// but against the team's own members, never the directory. You can only take
// out somebody you can already see.
func TestRemoveResolvesASubjectAgainstTheTeamsMembers(t *testing.T) {
	script := clitest.Routes(t, map[string]clitest.Reply{
		"GET /teams":                        {Status: http.StatusOK, Body: `{"items":[{"id":"` + teamID + `","name":"platform"}]}`},
		"GET /teams/{id}/members":           {Status: http.StatusOK, Body: twoMembers},
		"DELETE /teams/{id}/members/{user}": {Status: http.StatusNoContent, Body: ""},
	})

	if _, err := clitest.Execute(t, commands,
		"teams", "members", "remove", "platform", "ak"); err != nil {
		t.Fatalf("teams members remove: %v", err)
	}

	if n := script.Calls(http.MethodGet, "/users"); n != 0 {
		t.Errorf("the directory was read %d times to remove a member", n)
	}

	seen := script.All()
	last := seen[len(seen)-1]
	if last.Method != http.MethodDelete || last.Path != "/teams/"+teamID+"/members/"+userID {
		t.Errorf("deleted %s %s, want the id resolved from the member list",
			last.Method, last.Path)
	}
}

// Somebody who is not in the team has no id to put in the path, and the
// message has to say that rather than blaming the format of what was typed.
func TestRemovingSomebodyNotInTheTeamSaysSo(t *testing.T) {
	script := clitest.Routes(t, map[string]clitest.Reply{
		"GET /teams":                        {Status: http.StatusOK, Body: `{"items":[{"id":"` + teamID + `","name":"platform"}]}`},
		"GET /teams/{id}/members":           {Status: http.StatusOK, Body: `{"items":[]}`},
		"DELETE /teams/{id}/members/{user}": {Status: http.StatusNoContent, Body: ""},
	})

	_, err := clitest.Execute(t, commands, "teams", "members", "remove", "platform", "stranger")
	if err == nil {
		t.Fatal("a subject nobody in the team has was accepted")
	}
	if !strings.Contains(err.Error(), "stranger") {
		t.Errorf("the error does not name what was typed: %v", err)
	}
	if n := script.Calls(http.MethodDelete, "/teams/"+teamID+"/members/"+userID); n != 0 {
		t.Error("something was deleted anyway")
	}
}

// The brain answers 204 and nothing else, so the status is the only thing that
// says it worked. Anything else reported as success would have the caller
// believe a role was granted that was not.
func TestMembersAddReportsAnythingButA204AsAFailure(t *testing.T) {
	for _, status := range []int{
		http.StatusOK,
		http.StatusBadRequest,
		http.StatusForbidden,
		http.StatusConflict,
	} {
		clitest.BrainStub(t, status, `{"error":"no"}`)

		out, err := clitest.Execute(t, commands,
			"teams", "members", "add", teamID, userID, "--role", "developer")
		if err == nil {
			t.Errorf("a %d was reported as success", status)
		}
		if strings.Contains(out, "added") {
			t.Errorf("a %d printed a confirmation:\n%s", status, out)
		}
	}
}

// An unknown role is a rejected foreign key, and the brain's sentence is the
// only thing that says which part of the request was wrong.
func TestMembersAddCarriesTheBrainsReason(t *testing.T) {
	clitest.BrainStub(t, http.StatusBadRequest, "unknown role")

	_, err := clitest.Execute(t, commands,
		"teams", "members", "add", teamID, userID, "--role", "wizard")
	if err == nil {
		t.Fatal("a 400 was reported as success")
	}
	if !strings.Contains(err.Error(), "unknown role") {
		t.Errorf("the brain's reason was dropped: %v", err)
	}
}

// Managing members needs member:write, so a refusal is a normal path here.
// Suggesting a re-login is a loop the caller cannot escape.
func TestAForbiddenAddNeverSuggestsLoggingIn(t *testing.T) {
	clitest.BrainStub(t, http.StatusForbidden, `{"error":"forbidden"}`)

	out, err := clitest.Execute(t, commands,
		"teams", "members", "add", teamID, userID, "--role", "developer")
	if err == nil {
		t.Fatal("a 403 was reported as success")
	}
	if strings.Contains(out+err.Error(), "log in") {
		t.Errorf("a 403 told the user to log in: %v", err)
	}
}

func TestMembersRemoveDeletesTheUser(t *testing.T) {
	got := clitest.BrainStub(t, http.StatusNoContent, "")

	out, err := clitest.Execute(t, commands, "teams", "members", "remove", teamID, userID)
	if err != nil {
		t.Fatalf("teams members remove: %v", err)
	}
	if !strings.Contains(out, "removed") {
		t.Errorf("the confirmation is missing:\n%s", out)
	}

	got.Lock()
	defer got.Unlock()

	if got.Method != http.MethodDelete {
		t.Errorf("method = %s, want DELETE", got.Method)
	}
	if got.Path != "/teams/"+teamID+"/members/"+userID {
		t.Errorf("path = %q", got.Path)
	}
}

// Removing someone who is not a member succeeds — the caller asked for a state
// and that state holds. A script that runs twice must not fail the second time.
func TestRemovingSomeoneTwiceSucceeds(t *testing.T) {
	clitest.BrainStub(t, http.StatusNoContent, "")

	for range 2 {
		if _, err := clitest.Execute(t, commands,
			"teams", "members", "remove", teamID, userID); err != nil {
			t.Fatalf("teams members remove: %v", err)
		}
	}
}

func TestMembersRemoveReportsAnythingButA204AsAFailure(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden, http.StatusUnauthorized} {
		clitest.BrainStub(t, status, `{"error":"no"}`)

		out, err := clitest.Execute(t, commands, "teams", "members", "remove", teamID, userID)
		if err == nil {
			t.Errorf("a %d was reported as success", status)
		}
		if strings.Contains(out, "removed") {
			t.Errorf("a %d printed a confirmation:\n%s", status, out)
		}
	}
}

func TestEveryMembersSubcommandIsRegistered(t *testing.T) {
	clitest.Isolate(t)

	out, err := clitest.Execute(t, commands, "teams", "members", "--help")
	if err != nil {
		t.Fatalf("teams members --help: %v", err)
	}
	for _, verb := range []string{"list", "add", "remove"} {
		if !strings.Contains(out, verb) {
			t.Errorf("%q is not registered:\n%s", verb, out)
		}
	}
}

// Every format, one loop. A command that works as a table and crashes as yaml
// is a command nobody tests the second way.
func TestMembersWorkInEveryFormat(t *testing.T) {
	for _, format := range []string{"table", "json", "yaml"} {
		clitest.BrainStub(t, http.StatusOK, twoMembers)

		out, err := clitest.Execute(t, commands, "teams", "members", "list", teamID, "-o", format)
		if err != nil {
			t.Errorf("teams members list -o %s: %v", format, err)
		} else if !strings.Contains(out, "ak") {
			t.Errorf("-o %s lost the data:\n%s", format, out)
		}

		clitest.BrainStub(t, http.StatusNoContent, "")

		out, err = clitest.Execute(t, commands,
			"teams", "members", "add", teamID, userID, "--role", "developer", "-o", format)
		if err != nil {
			t.Errorf("teams members add -o %s: %v", format, err)
		} else if !strings.Contains(out, userID) {
			t.Errorf("-o %s lost the user id:\n%s", format, out)
		}
	}
}
