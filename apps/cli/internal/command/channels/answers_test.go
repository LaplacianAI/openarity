package channels

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

// A WhatsApp number that is also a person's own is connected to answer in
// groups alone and tell nobody anything.
func TestCreateSendsWhereItAnswersAndNoNotices(t *testing.T) {
	routes := teamRoute()
	routes["POST /teams/{id}/channels"] = clitest.Reply{Status: http.StatusCreated, Body: createdWithoutSecret}
	script := clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "channels", "create", "platform", "mine",
		"--provider", "whatsapp", "--answers-in", " groups ", "--no-notify-strangers")
	if err != nil {
		t.Fatalf("channels create: %v\n%s", err, out)
	}
	body := postBody(t, script)
	if !strings.Contains(body, `"answers_in":"groups"`) || !strings.Contains(body, `"notify_strangers":false`) {
		t.Errorf("sent %s; want groups and notices off", body)
	}
}

// Left out, neither is sent: the brain's defaults stand.
func TestCreateWithoutThemSendsNeither(t *testing.T) {
	routes := teamRoute()
	routes["POST /teams/{id}/channels"] = clitest.Reply{Status: http.StatusCreated, Body: createdWithoutSecret}
	script := clitest.Routes(t, routes)

	if _, err := clitest.Execute(t, commands, "channels", "create", "platform", "bot", "--provider", "whatsapp"); err != nil {
		t.Fatal(err)
	}
	if body := postBody(t, script); strings.Contains(body, "answers_in") || strings.Contains(body, "notify_strangers") {
		t.Errorf("sent %s; want neither", body)
	}
}

func TestAPlaceItCannotAnswerIsRefusedBeforeAnythingIsSent(t *testing.T) {
	for name, args := range map[string][]string{
		"create": {"create", "platform", "mine", "--provider", "whatsapp", "--answers-in", "nowhere"},
		"update": {"update", "platform", "support", "--answers-in", "Groups"},
	} {
		script := clitest.Routes(t, updateRoutes(clitest.Reply{Status: http.StatusOK, Body: updated("")}))
		_, err := clitest.Execute(t, commands, append([]string{"channels"}, args...)...)
		if err == nil || !strings.Contains(err.Error(), "everywhere, groups or direct") {
			t.Errorf("%s: err = %v; want the places named", name, err)
		}
		for _, e := range script.All() {
			if e.Method != http.MethodGet {
				t.Errorf("%s: sent %s %s", name, e.Method, e.Path)
			}
		}
	}
}

func TestUpdateChangesWhereItAnswers(t *testing.T) {
	answered := `{"id": "` + channelID + `", "team_id": "` + teamID + `", "provider": "whatsapp", "name": "support", "answers_in": "direct"}`
	script := clitest.Routes(t, updateRoutes(clitest.Reply{Status: http.StatusOK, Body: answered}))

	out, err := clitest.Execute(t, commands, "channels", "update", "platform", "support", "--answers-in", "direct")
	if err != nil {
		t.Fatalf("channels update: %v\n%s", err, out)
	}
	if body := patchBody(t, script); body != `{"answers_in":"direct"}` {
		t.Errorf("sent %s; want answers_in alone", body)
	}
	if !strings.Contains(out, "only in direct chats") {
		t.Errorf("output does not say where it answers:\n%s", out)
	}
}
