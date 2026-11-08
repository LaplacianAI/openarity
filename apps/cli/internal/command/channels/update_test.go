package channels

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

const repliesTo = "https://support.example.com/replies"

func updated(url string) string {
	field := ""
	if url != "" {
		field = `, "reply_url": "` + url + `"`
	}
	return `{"id": "` + channelID + `", "team_id": "` + teamID + `", "provider": "custom", "name": "support", "can_reply": true` + field + `}`
}

func patchBody(t *testing.T, script *clitest.Transcript) string {
	t.Helper()
	for _, e := range script.All() {
		if e.Method == http.MethodPatch {
			if e.Path != "/teams/"+teamID+"/channels/"+channelID {
				t.Errorf("PATCH %s, want the resolved channel", e.Path)
			}
			return e.Body
		}
	}
	t.Fatal("no PATCH was sent")
	return ""
}

func updateRoutes(reply clitest.Reply) map[string]clitest.Reply {
	routes := teamRoute()
	routes["GET /teams/{id}/channels"] = clitest.Reply{Status: http.StatusOK, Body: twoChannels}
	routes["PATCH /teams/{id}/channels/{channelID}"] = reply
	return routes
}

func TestUpdateSetsWhereRepliesGo(t *testing.T) {
	script := clitest.Routes(t, updateRoutes(clitest.Reply{Status: http.StatusOK, Body: updated(repliesTo)}))

	out, err := clitest.Execute(t, commands,
		"channels", "update", "platform", "support", "--reply-url", "  "+repliesTo+" ")
	if err != nil {
		t.Fatalf("channels update: %v\n%s", err, out)
	}
	if body := patchBody(t, script); body != `{"reply_url":"`+repliesTo+`"}` {
		t.Errorf("sent %s, want the trimmed URL", body)
	}
	if !strings.Contains(out, "replies to") || !strings.Contains(out, repliesTo) {
		t.Errorf("output does not say where replies go:\n%s", out)
	}
}

// Stopping replies is said with its own flag, and sent as the empty string
// the brain reads as "none".
func TestUpdateStopsReplies(t *testing.T) {
	script := clitest.Routes(t, updateRoutes(clitest.Reply{Status: http.StatusOK, Body: updated("")}))

	out, err := clitest.Execute(t, commands, "channels", "update", "platform", "support", "--no-reply-url")
	if err != nil {
		t.Fatalf("channels update --no-reply-url: %v\n%s", err, out)
	}
	if body := patchBody(t, script); body != `{"reply_url":""}` {
		t.Errorf("sent %s, want an empty reply_url", body)
	}
	if !strings.Contains(out, "replies stopped") {
		t.Errorf("output does not say replies stopped:\n%s", out)
	}
}

// Neither flag, both, or an empty URL: nothing is sent, and the error says
// what to do. An empty --reply-url silently stopping replies would be a
// surprise to anyone whose variable was unset.
func TestUpdateNeedsExactlyOneOfItsFlags(t *testing.T) {
	for name, args := range map[string][]string{
		"neither":   {},
		"both":      {"--reply-url", repliesTo, "--no-reply-url"},
		"empty url": {"--reply-url", "  "},
	} {
		t.Run(name, func(t *testing.T) {
			script := clitest.Routes(t, updateRoutes(clitest.Reply{Status: http.StatusOK, Body: updated(repliesTo)}))

			_, err := clitest.Execute(t, commands, append([]string{"channels", "update", "platform", "support"}, args...)...)
			if err == nil {
				t.Fatal("update succeeded")
			}
			if !strings.Contains(err.Error(), "--no-reply-url") {
				t.Errorf("the error does not say how to stop replies: %v", err)
			}
			if len(script.All()) != 0 {
				t.Errorf("made %d calls before refusing", len(script.All()))
			}
		})
	}
}

// The brain's reason for refusing a URL is the one thing worth reading.
func TestARefusedReplyURLSaysWhy(t *testing.T) {
	clitest.Routes(t, updateRoutes(clitest.Reply{
		Status: http.StatusBadRequest,
		Body:   "reply_url must not have a query or a fragment; the signature is what authenticates a reply",
	}))

	_, err := clitest.Execute(t, commands, "channels", "update", "platform", "support", "--reply-url", repliesTo+"?t=1")
	if err == nil || !strings.Contains(err.Error(), "must not have a query") {
		t.Errorf("err = %v, want the brain's reason", err)
	}
}

// A refused write names the permission it needed.
func TestAnUpdateWithoutChannelWriteSaysSo(t *testing.T) {
	clitest.Routes(t, updateRoutes(clitest.Reply{Status: http.StatusForbidden, Body: "forbidden"}))

	_, err := clitest.Execute(t, commands, "channels", "update", "platform", "support", "--no-reply-url")
	if err == nil || !strings.Contains(err.Error(), "channel:write") {
		t.Errorf("err = %v, want it to name channel:write", err)
	}
}

func TestCreateSendsTheReplyURLOnlyWhenGiven(t *testing.T) {
	routes := teamRoute()
	routes["POST /teams/{id}/channels"] = clitest.Reply{Status: http.StatusCreated, Body: createdWithSecret}
	script := clitest.Routes(t, routes)

	if out, err := clitest.Execute(t, commands,
		"channels", "create", "platform", "support", "--provider", "custom", "--reply-url", repliesTo); err != nil {
		t.Fatalf("channels create --reply-url: %v\n%s", err, out)
	}
	if body := postBody(t, script); !strings.Contains(body, `"reply_url":"`+repliesTo+`"`) {
		t.Errorf("body = %s, want the reply URL", body)
	}
}
