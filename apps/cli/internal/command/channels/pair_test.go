package channels

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

// quick makes pairing follow at once and give up soon, for one test.
func quick(t *testing.T, wait, noRole time.Duration) {
	t.Helper()
	poll, w, n := pairPoll, pairWait, pairNoRole
	pairPoll, pairWait, pairNoRole = time.Millisecond, wait, noRole
	t.Cleanup(func() { pairPoll, pairWait, pairNoRole = poll, w, n })
}

func pairRoutes(first string, then ...string) map[string]clitest.Reply {
	routes := teamRoute()
	routes["GET /teams/{id}/channels"] = clitest.Reply{Status: http.StatusOK, Body: twoChannels}
	routes["POST /teams/{id}/channels/{channelID}/pairing"] = clitest.Reply{Status: http.StatusAccepted, Body: first}
	get := clitest.Reply{Status: http.StatusOK, Body: first}
	for _, b := range then {
		get.Then = append(get.Then, clitest.Reply{Status: http.StatusOK, Body: b})
	}
	if len(then) > 0 {
		get.Body = then[0]
		get.Then = get.Then[1:]
	}
	routes["GET /teams/{id}/channels/{channelID}/pairing"] = get
	return routes
}

const (
	asked     = `{"state":"pairing","method":"qr","held":true}`
	qrOne     = `{"state":"pairing","method":"qr","qr":"2@first-code,abc=,def=,1","held":true}`
	qrTwo     = `{"state":"pairing","method":"qr","qr":"2@second-code,abc=,def=,1","held":true}`
	linked    = `{"state":"connected","phone":"919876543210","held":true}`
	codeShown = `{"state":"pairing","method":"code","code":"ABCD-1234","held":true}`
)

func sentBody(t *testing.T, script *clitest.Transcript) string {
	t.Helper()
	for _, e := range script.All() {
		if e.Method == http.MethodPost {
			if e.Path != "/teams/"+teamID+"/channels/"+channelID+"/pairing" {
				t.Errorf("POST %s, want the resolved channel's pairing", e.Path)
			}
			return e.Body
		}
	}
	t.Fatal("nothing was asked")
	return ""
}

func TestPairIsRegistered(t *testing.T) {
	out, err := clitest.Execute(t, commands, "channels", "--help")
	if err != nil || !strings.Contains(out, "pair") {
		t.Fatalf("channels --help = %v:\n%s", err, out)
	}
}

// A QR is drawn each time it changes, never twice for one code, and the
// command ends when the number is linked.
func TestPairByQRDrawsEachNewCodeUntilLinked(t *testing.T) {
	quick(t, time.Minute, time.Minute)
	script := clitest.Routes(t, pairRoutes(asked, qrOne, qrOne, qrTwo, linked))

	out, err := clitest.Execute(t, commands, "channels", "pair", "platform", "support")
	if err != nil {
		t.Fatalf("channels pair: %v\n%s", err, out)
	}
	if body := sentBody(t, script); body != `{"method":"qr"}` {
		t.Errorf("sent %s, want a QR pairing", body)
	}
	if n := strings.Count(out, "scan it from WhatsApp"); n != 2 {
		t.Errorf("drew %d QR codes; want two, one for each code\n%s", n, out)
	}
	if !strings.Contains(out, "linked") || !strings.Contains(out, "+919876543210") {
		t.Errorf("output does not say the number is linked:\n%s", out)
	}
	if strings.Contains(out, "first-code") {
		t.Error("the QR's text was printed; only its drawing should be")
	}
}

func TestPairByCodeSendsTheNumberAndShowsTheCode(t *testing.T) {
	quick(t, time.Minute, time.Minute)
	script := clitest.Routes(t, pairRoutes(asked, codeShown, linked))

	out, err := clitest.Execute(t, commands, "channels", "pair", "platform", "support", "--code", "--phone", " +91 98765 43210 ")
	if err != nil {
		t.Fatalf("channels pair --code: %v\n%s", err, out)
	}
	if body := sentBody(t, script); body != `{"method":"code","phone":"+91 98765 43210"}` {
		t.Errorf("sent %s, want a code for the number as typed", body)
	}
	if !strings.Contains(out, "ABCD-1234") || !strings.Contains(out, "Link with phone number") {
		t.Errorf("output does not show the code and where it goes:\n%s", out)
	}
}

// What is wrong with the flags is said before anything is asked.
func TestPairRefusesFlagsThatDoNotGoTogether(t *testing.T) {
	quick(t, 50*time.Millisecond, time.Minute) // a refusal missed must fail, not wait five minutes
	for name, args := range map[string][]string{
		"a code with no number": {"--code"},
		"a number with no code": {"--phone", "+919876543210"},
	} {
		script := clitest.Routes(t, pairRoutes(asked))
		out, err := clitest.Execute(t, commands, append([]string{"channels", "pair", "platform", "support"}, args...)...)
		if err == nil {
			t.Errorf("%s: succeeded:\n%s", name, out)
		}
		if n := len(script.All()); n != 0 {
			t.Errorf("%s: sent %d requests", name, n)
		}
	}
}

func TestAPairingThatStopsSaysWhy(t *testing.T) {
	quick(t, time.Minute, time.Minute)
	clitest.Routes(t, pairRoutes(asked, `{"state":"unpaired","error":"The QR code expired before it was scanned. Pair again.","held":true}`))

	_, err := clitest.Execute(t, commands, "channels", "pair", "platform", "support")
	if err == nil || !strings.Contains(err.Error(), "expired before it was scanned") {
		t.Fatalf("err = %v; want why it stopped", err)
	}
}

// The error is the brain's, written from what WhatsApp said: an escape in it
// never reaches the terminal as one.
func TestAPairingErrorIsMadeSafe(t *testing.T) {
	quick(t, time.Minute, time.Minute)
	clitest.Routes(t, pairRoutes(asked, `{"state":"unpaired","error":"bad\u001b[2Jnews","held":true}`))

	_, err := clitest.Execute(t, commands, "channels", "pair", "platform", "support")
	if err == nil || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("err = %q; want it without the escape", err)
	}
}

func TestPairWithNoRoleRunningSaysWhatTheDeploymentNeeds(t *testing.T) {
	quick(t, time.Minute, 0)
	clitest.Routes(t, pairRoutes(`{"state":"pairing","method":"qr","held":false}`))

	_, err := clitest.Execute(t, commands, "channels", "pair", "platform", "support")
	if err == nil || !strings.Contains(err.Error(), "WHATSAPP_ENABLED=1") {
		t.Fatalf("err = %v; want the setting named", err)
	}
}

func TestPairGivesUpAfterItsWait(t *testing.T) {
	quick(t, 20*time.Millisecond, time.Minute)
	clitest.Routes(t, pairRoutes(asked, qrOne))

	_, err := clitest.Execute(t, commands, "channels", "pair", "platform", "support")
	if err == nil || !strings.Contains(err.Error(), "not linked within") {
		t.Fatalf("err = %v; want it to give up", err)
	}
}

func TestPairRefusedSaysThePermission(t *testing.T) {
	routes := pairRoutes(asked)
	routes["POST /teams/{id}/channels/{channelID}/pairing"] = clitest.Reply{Status: http.StatusForbidden, Body: "forbidden", ContentType: "text/plain"}
	clitest.Routes(t, routes)

	_, err := clitest.Execute(t, commands, "channels", "pair", "platform", "support")
	if err == nil || !strings.Contains(err.Error(), "channel:write") {
		t.Fatalf("err = %v; want the permission named", err)
	}
}

// Under json each state is a document of its own, so a script can follow it,
// and nothing of the drawing is mixed in.
func TestPairPrintsEachStateAsADocument(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		quick(t, time.Minute, time.Minute)
		clitest.Routes(t, pairRoutes(asked, qrOne, linked))
		out, err := clitest.Execute(t, commands, "-o", format, "channels", "pair", "platform", "support")
		if err != nil {
			t.Fatalf("%s: %v\n%s", format, err, out)
		}
		if strings.Contains(out, "█") || !strings.Contains(out, "connected") {
			t.Errorf("%s output:\n%s", format, out)
		}
		if format == "json" {
			dec := json.NewDecoder(strings.NewReader(out))
			n := 0
			for dec.More() {
				var v pairingView
				if err := dec.Decode(&v); err != nil {
					t.Fatalf("json line %d: %v\n%s", n, err, out)
				}
				n++
			}
			if n != 3 {
				t.Errorf("printed %d documents; want one per state", n)
			}
		}
	}
}

func TestAQRIsDrawnWithItsMarginAndInverts(t *testing.T) {
	dark, err := drawQR("2@abc", false)
	if err != nil {
		t.Fatal(err)
	}
	light, _ := drawQR("2@abc", true)
	lines := strings.Split(strings.TrimRight(dark, "\n"), "\n")
	if lines[0] != strings.Repeat("█", len([]rune(lines[0]))) {
		t.Errorf("the top margin is not light on a dark terminal: %q", lines[0])
	}
	if dark == light || strings.Contains(dark, "2@abc") {
		t.Error("inverting changed nothing, or the text itself was drawn")
	}
	if _, err := drawQR(strings.Repeat("x", 5000), false); err == nil {
		t.Error("text too long for a QR code was drawn")
	}
}
