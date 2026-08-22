package channels

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

var commands = []clitest.Build{New}

const (
	teamID    = "6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d"
	channelID = "3f9a41c2-7e58-4b0d-9a11-8d2c0e5b71c1"
)

const oneTeam = `{"items":[{"id":"` + teamID + `","name":"platform","role":"admin"}]}`

const twoChannels = `{
  "items": [
    {"id": "` + channelID + `", "team_id": "` + teamID + `", "provider": "custom", "name": "support"},
    {"id": "8b3d0a6e-9f4c-6f3a-9c3a-4d0a6e9f4c6f", "team_id": "` + teamID + `", "provider": "slack", "name": "engineering"}
  ],
  "next_cursor": "eyJjIjoiMjAyNi0wOC0xM1QwMDowMDowMFoifQ"
}`

func teamRoute() map[string]clitest.Reply {
	return map[string]clitest.Reply{"GET /teams": {Status: http.StatusOK, Body: oneTeam}}
}

// --- listing ---

func TestChannelsListRendersATable(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/channels"] = clitest.Reply{Status: http.StatusOK, Body: twoChannels}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "channels", "list", "platform")
	if err != nil {
		t.Fatalf("channels list: %v\n%s", err, out)
	}
	for _, want := range []string{"support", "custom", "engineering", "slack"} {
		if !strings.Contains(out, want) {
			t.Errorf("the table is missing %q:\n%s", want, out)
		}
	}
}

// The envelope is the contract. Printing the items alone would drop
// next_cursor and every consumer would believe it had the last page.
func TestChannelsListPrintsTheEnvelope(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/channels"] = clitest.Reply{Status: http.StatusOK, Body: twoChannels}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "channels", "list", "platform", "-o", "json")
	if err != nil {
		t.Fatalf("channels list -o json: %v\n%s", err, out)
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

// yaml.v3 ignores json tags and lowercases the Go field name, so an untagged
// TeamID prints as `teamid` in yaml and `team_id` in json. Two formats of the
// same command disagreeing about a key is worse than either being wrong.
func TestYAMLAndJSONAgreeOnEveryKey(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/channels"] = clitest.Reply{Status: http.StatusOK, Body: twoChannels}
	clitest.Routes(t, routes)

	asJSON, err := clitest.Execute(t, commands, "channels", "list", "platform", "-o", "json")
	if err != nil {
		t.Fatalf("-o json: %v", err)
	}
	asYAML, err := clitest.Execute(t, commands, "channels", "list", "platform", "-o", "yaml")
	if err != nil {
		t.Fatalf("-o yaml: %v", err)
	}

	var fromJSON, fromYAML map[string]any
	if err := json.Unmarshal([]byte(asJSON), &fromJSON); err != nil {
		t.Fatalf("not json: %v\n%s", err, asJSON)
	}
	if err := yaml.Unmarshal([]byte(asYAML), &fromYAML); err != nil {
		t.Fatalf("not yaml: %v\n%s", err, asYAML)
	}

	jsonKeys := itemKeys(t, fromJSON)
	yamlKeys := itemKeys(t, fromYAML)
	for _, key := range jsonKeys {
		if !contains(yamlKeys, key) {
			t.Errorf("json has %q and yaml does not: yaml keys %v", key, yamlKeys)
		}
	}
	for _, key := range yamlKeys {
		if !contains(jsonKeys, key) {
			t.Errorf("yaml has %q and json does not: json keys %v", key, jsonKeys)
		}
	}
}

func itemKeys(t *testing.T, doc map[string]any) []string {
	t.Helper()

	items, ok := doc["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("no items in %+v", doc)
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("the first item is not an object: %+v", items[0])
	}

	keys := make([]string, 0, len(first))
	for k := range first {
		keys = append(keys, k)
	}
	return keys
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// A team name has to become an id before the channels call can be made, and
// the order matters: looking it up afterwards would mean the write already
// went somewhere.
func TestListResolvesTheTeamNameFirst(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/channels"] = clitest.Reply{Status: http.StatusOK, Body: twoChannels}
	script := clitest.Routes(t, routes)

	if _, err := clitest.Execute(t, commands, "channels", "list", "platform"); err != nil {
		t.Fatalf("channels list: %v", err)
	}

	seen := script.All()
	if len(seen) != 2 {
		t.Fatalf("made %d calls, want a lookup then a list: %+v", len(seen), seen)
	}
	if seen[0].Path != "/teams" {
		t.Errorf("first call was %s %s, want GET /teams", seen[0].Method, seen[0].Path)
	}
	if seen[1].Path != "/teams/"+teamID+"/channels" {
		t.Errorf("second call was %s, want the channels of %s", seen[1].Path, teamID)
	}
}

// A uuid is already an id, so spending a round trip to confirm it is waste —
// and it would fail for somebody who can reach a channel but not list teams.
func TestAUUIDTeamIsNotLookedUp(t *testing.T) {
	script := clitest.Routes(t, map[string]clitest.Reply{
		"GET /teams/{id}/channels": {Status: http.StatusOK, Body: twoChannels},
	})

	if _, err := clitest.Execute(t, commands, "channels", "list", teamID); err != nil {
		t.Fatalf("channels list by id: %v", err)
	}

	if n := script.Calls("GET", "/teams"); n != 0 {
		t.Errorf("looked the team up %d times when it was already an id", n)
	}
}

// --- creating ---

const createdWithSecret = `{
  "id": "` + channelID + `", "team_id": "` + teamID + `",
  "provider": "custom", "name": "support",
  "signing_secret": "oawh_generated-by-the-brain"
}`

const createdWithoutSecret = `{
  "id": "` + channelID + `", "team_id": "` + teamID + `",
  "provider": "custom", "name": "support"
}`

func TestCreateSendsNoSecretWhenNoneIsPiped(t *testing.T) {
	routes := teamRoute()
	routes["POST /teams/{id}/channels"] = clitest.Reply{Status: http.StatusCreated, Body: createdWithSecret}
	script := clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands,
		"channels", "create", "platform", "support", "--provider", "custom")
	if err != nil {
		t.Fatalf("channels create: %v\n%s", err, out)
	}

	body := postBody(t, script)
	if strings.Contains(body, "signing_secret") {
		t.Errorf("a signing_secret was sent when none was given: %s", body)
	}
	if !strings.Contains(body, `"provider":"custom"`) || !strings.Contains(body, `"name":"support"`) {
		t.Errorf("body = %s", body)
	}
}

// The generated secret is the one thing that cannot be fetched again, so it
// has to reach stdout — a note on stderr would be lost the moment anyone
// redirected the output.
func TestCreatePrintsAGeneratedSecret(t *testing.T) {
	routes := teamRoute()
	routes["POST /teams/{id}/channels"] = clitest.Reply{Status: http.StatusCreated, Body: createdWithSecret}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands,
		"channels", "create", "platform", "support", "--provider", "custom")
	if err != nil {
		t.Fatalf("channels create: %v\n%s", err, out)
	}

	if !strings.Contains(out, "oawh_generated-by-the-brain") {
		t.Errorf("the generated secret was not shown, so it is lost:\n%s", out)
	}
	if !strings.Contains(out, "once") {
		t.Errorf("nothing said the secret is shown once:\n%s", out)
	}
}

// Nothing to warn about when the caller supplied it — they already have it.
func TestCreateSaysNothingAboutASecretItDidNotReceive(t *testing.T) {
	routes := teamRoute()
	routes["POST /teams/{id}/channels"] = clitest.Reply{Status: http.StatusCreated, Body: createdWithoutSecret}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands,
		"channels", "create", "platform", "support", "--provider", "custom")
	if err != nil {
		t.Fatalf("channels create: %v\n%s", err, out)
	}

	if strings.Contains(out, "secret") {
		t.Errorf("the output mentions a secret that was never returned:\n%s", out)
	}
}

func TestCreateSendsWhatWasPipedIn(t *testing.T) {
	routes := teamRoute()
	routes["POST /teams/{id}/channels"] = clitest.Reply{Status: http.StatusCreated, Body: createdWithoutSecret}
	script := clitest.Routes(t, routes)

	out, err := clitest.ExecuteWithStdin(t, commands, "theirs-not-ours\n",
		"channels", "create", "platform", "slack", "--provider", "custom", "--secret-stdin")
	if err != nil {
		t.Fatalf("channels create --secret-stdin: %v\n%s", err, out)
	}

	if body := postBody(t, script); !strings.Contains(body, `"signing_secret":"theirs-not-ours"`) {
		t.Errorf("body = %s, want the piped secret with its newline trimmed", body)
	}
}

func TestCreateRefusesAnEmptyPipe(t *testing.T) {
	routes := teamRoute()
	routes["POST /teams/{id}/channels"] = clitest.Reply{Status: http.StatusCreated, Body: createdWithoutSecret}
	script := clitest.Routes(t, routes)

	_, err := clitest.ExecuteWithStdin(t, commands, "",
		"channels", "create", "platform", "support", "--provider", "custom", "--secret-stdin")
	if err == nil {
		t.Fatal("an empty pipe was accepted, so the channel would verify nothing")
	}
	if n := script.Calls("POST", "/teams/"+teamID+"/channels"); n != 0 {
		t.Errorf("a channel was created with no secret: %d calls", n)
	}
}

func TestCreateNeedsAProvider(t *testing.T) {
	clitest.Routes(t, teamRoute())

	if _, err := clitest.Execute(t, commands, "channels", "create", "platform", "support"); err == nil {
		t.Fatal("a channel was created with no provider, so nothing could parse it")
	}
}

// The whole reason the secret arrives on stdin. On Linux /proc/<pid>/cmdline
// is world-readable, so a flag would expose it to every account on the box for
// as long as the command runs — and to the shell history afterwards.
func TestThereIsNoSecretFlag(t *testing.T) {
	clitest.Isolate(t)

	create := findCommand(t, "create")
	for _, name := range []string{"secret", "signing-secret", "password", "token"} {
		if create.Flags().Lookup(name) != nil {
			t.Errorf("--%s exists; a credential in argv is readable by every process on the machine", name)
		}
	}
	if create.Flags().Lookup("secret-stdin") == nil {
		t.Error("--secret-stdin is missing, so a provider's own secret cannot be supplied at all")
	}
}

// --- deleting ---

func TestDeleteResolvesBothNamesThenDeletes(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/channels"] = clitest.Reply{Status: http.StatusOK, Body: twoChannels}
	routes["DELETE /teams/{id}/channels/{channelID}"] = clitest.Reply{Status: http.StatusNoContent}
	script := clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "channels", "delete", "platform", "support")
	if err != nil {
		t.Fatalf("channels delete: %v\n%s", err, out)
	}

	seen := script.All()
	if len(seen) != 3 {
		t.Fatalf("made %d calls, want a team lookup, a channel lookup and a delete: %+v", len(seen), seen)
	}
	if seen[2].Method != http.MethodDelete || seen[2].Path != "/teams/"+teamID+"/channels/"+channelID {
		t.Errorf("last call was %s %s, want the delete of %s", seen[2].Method, seen[2].Path, channelID)
	}
}

func TestDeletingAChannelThatIsNotThereSaysSo(t *testing.T) {
	routes := teamRoute()
	routes["GET /teams/{id}/channels"] = clitest.Reply{Status: http.StatusOK, Body: twoChannels}
	clitest.Routes(t, routes)

	_, err := clitest.Execute(t, commands, "channels", "delete", "platform", "nosuch")
	if err == nil {
		t.Fatal("deleting a channel that is not in the list succeeded")
	}
	if !strings.Contains(err.Error(), "nosuch") {
		t.Errorf("the error does not name the channel: %v", err)
	}
}

// --- helpers ---

func postBody(t *testing.T, script *clitest.Transcript) string {
	t.Helper()

	for _, e := range script.All() {
		if e.Method == http.MethodPost {
			return e.Body
		}
	}
	t.Fatal("nothing was posted")
	return ""
}

func findCommand(t *testing.T, name string) *cobra.Command {
	t.Helper()

	for _, sub := range New(nil).Commands() {
		if sub.Name() == name {
			return sub
		}
	}
	t.Fatalf("channels has no %q subcommand", name)
	return nil
}
