package channels

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

func TestCreateSendsTheTriggerWordTrimmed(t *testing.T) {
	routes := teamRoute()
	routes["POST /teams/{id}/channels"] = clitest.Reply{Status: http.StatusCreated, Body: createdWithoutSecret}
	script := clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "channels", "create", "platform", "mine",
		"--provider", "whatsapp", "--trigger-word", " @Fduv ")
	if err != nil {
		t.Fatalf("channels create: %v\n%s", err, out)
	}
	if body := postBody(t, script); !strings.Contains(body, `"trigger_word":"@Fduv"`) {
		t.Errorf("sent %s; want the word, trimmed", body)
	}
}

func TestCreateWithoutAWordSendsNone(t *testing.T) {
	routes := teamRoute()
	routes["POST /teams/{id}/channels"] = clitest.Reply{Status: http.StatusCreated, Body: createdWithoutSecret}
	script := clitest.Routes(t, routes)

	if _, err := clitest.Execute(t, commands, "channels", "create", "platform", "bot", "--provider", "whatsapp"); err != nil {
		t.Fatal(err)
	}
	if body := postBody(t, script); strings.Contains(body, "trigger_word") {
		t.Errorf("sent %s; want no word", body)
	}
}

// The word alone is a change; an empty one takes it away, and says so.
func TestUpdateSetsAndTakesAwayTheTriggerWord(t *testing.T) {
	for _, tc := range []struct {
		arg, sent, reply, says string
	}{
		{"@Fduv", `{"trigger_word":"@Fduv"}`, `"@Fduv"`, "called by @Fduv"},
		{"", `{"trigger_word":""}`, `null`, "called as before"},
	} {
		reply := `{"id": "` + channelID + `", "team_id": "` + teamID + `", "provider": "whatsapp", "name": "support", "trigger_word": ` + tc.reply + `}`
		script := clitest.Routes(t, updateRoutes(clitest.Reply{Status: http.StatusOK, Body: reply}))

		out, err := clitest.Execute(t, commands, "channels", "update", "platform", "support", "--trigger-word", tc.arg)
		if err != nil {
			t.Fatalf("channels update %q: %v\n%s", tc.arg, err, out)
		}
		if body := patchBody(t, script); body != tc.sent {
			t.Errorf("--trigger-word %q sent %s; want %s", tc.arg, body, tc.sent)
		}
		if !strings.Contains(out, tc.says) {
			t.Errorf("--trigger-word %q printed:\n%s\nwant it to say %q", tc.arg, out, tc.says)
		}
	}
}
