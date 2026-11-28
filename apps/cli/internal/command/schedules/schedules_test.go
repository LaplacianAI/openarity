package schedules

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
	"github.com/LaplacianAI/openarity/apps/cli/internal/command/trigger"
)

var commands = []clitest.Build{New}

const (
	teamID     = "6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d"
	scheduleID = "3f9a41c2-7e58-4b0d-9a11-8d2c0e5b71c1"
	otherID    = "4a0b52d3-8f69-4c1e-8b22-9e3d1f6c82d2"
	channelID  = "1c0e5b71-8d2c-4b0d-9a11-3f9a41c27e58"
	agentID    = "9a114b0d-7e58-4b0d-9a11-8d2c0e5b71c1"
	sessionID  = "7d2e8c90-1a2b-4c3d-8e4f-5a6b7c8d9e0f"
)

const oneTeam = `{"items":[{"id":"` + teamID + `","name":"acme","role":"admin"}]}`

func schedule(id, name string) string {
	return `{"id":"` + id + `","name":"` + name + `","objective":"Summarise the repo.","when":{"kind":"every","every_seconds":3600},
  "timezone":"Asia/Kolkata","hub":true,"target":{"kind":"own"},"session_id":"` + sessionID + `","status":"active",
  "next_fire_at":"2026-11-30T03:30:00Z","upcoming":["2026-11-30T03:30:00Z","2026-11-30T04:30:00Z","2026-11-30T05:30:00Z"],
  "created_at":"2026-11-28T00:00:00Z","updated_at":"2026-11-28T00:00:00Z"}`
}

const settings = `{"agents_may_schedule":true,"timezone":"Asia/Kolkata","schedule_min_interval_minutes":60,
  "schedules_per_member":5,"schedules_per_team":20,"fires_per_hour":20}`

func routes(extra map[string]clitest.Reply) map[string]clitest.Reply {
	r := map[string]clitest.Reply{
		"GET /teams":                        {Status: http.StatusOK, Body: oneTeam},
		"GET /teams/{id}/schedules":         {Status: http.StatusOK, Body: `{"items":[` + schedule(scheduleID, "standup") + `]}`},
		"GET /teams/{id}/schedules/{sid}":   {Status: http.StatusOK, Body: schedule(scheduleID, "standup")},
		"POST /teams/{id}/schedules":        {Status: http.StatusCreated, Body: schedule(scheduleID, "standup")},
		"PATCH /teams/{id}/schedules/{sid}": {Status: http.StatusOK, Body: schedule(scheduleID, "standup")},
		"GET /teams/{id}/schedule-settings": {Status: http.StatusOK, Body: settings},
		"GET /teams/{id}/channels":          {Status: http.StatusOK, Body: `{"items":[{"id":"` + channelID + `","name":"slack","provider":"slack"}]}`},
		"GET /teams/{id}/agents":            {Status: http.StatusOK, Body: `{"items":[{"id":"` + agentID + `","name":"triage"}]}`},
	}
	for k, v := range extra {
		r[k] = v
	}
	return r
}

// sent is the body of the one request to method path, or fails.
func sent(t *testing.T, script *clitest.Transcript, method, path string) map[string]any {
	t.Helper()
	for _, e := range script.All() {
		if e.Method == method && e.Path == path {
			var body map[string]any
			if err := json.Unmarshal([]byte(e.Body), &body); err != nil {
				t.Fatalf("the %s body is not json: %v\n%s", method, err, e.Body)
			}
			return body
		}
	}
	t.Fatalf("no %s %s in %+v", method, path, script.All())
	return nil
}

const schedulesPath = "/teams/" + teamID + "/schedules"

func TestCreateSendsWhatItsFlagsSay(t *testing.T) {
	script := clitest.Routes(t, routes(nil))
	out, err := clitest.Execute(t, commands, "schedules", "create", "acme", "--name", "standup",
		"--objective", "Summarise the repo.", "--cron", "0 9 * * 1-5", "--tz", "Asia/Kolkata",
		"--agent", "triage", "--target", "channel:slack:C0123ABCD")
	if err != nil {
		t.Fatal(err)
	}
	body := sent(t, script, http.MethodPost, schedulesPath)
	want := map[string]any{
		"name": "standup", "objective": "Summarise the repo.", "timezone": "Asia/Kolkata", "agent_id": agentID,
		"when":   map[string]any{"kind": "cron", "cron": "0 9 * * 1-5"},
		"target": map[string]any{"kind": "channel", "channel_id": channelID, "ref": "C0123ABCD"},
	}
	if got, _ := json.Marshal(body); string(got) != mustJSON(t, want) {
		t.Errorf("sent %s\nwant %s", got, mustJSON(t, want))
	}
	for _, w := range []string{"created", "standup", "Mon 30 Nov 09:00, Mon 30 Nov 10:00, Mon 30 Nov 11:00", "in Asia/Kolkata"} {
		if !strings.Contains(out, w) {
			t.Errorf("the output has no %q:\n%s", w, out)
		}
	}
}

// when is the when a body sent.
func when(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	w, ok := body["when"].(map[string]any)
	if !ok {
		t.Fatalf("no when in %v", body)
	}
	return w
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A wall time is read in --tz, or the team's zone when none is given; an
// instant is sent as given, and the zone is not asked for.
func TestAtIsReadInItsZone(t *testing.T) {
	for name, tc := range map[string]struct {
		args      []string
		want      string
		readsZone bool
	}{
		"in --tz":       {[]string{"--at", "2026-12-01 09:00", "--tz", "Europe/London"}, "2026-12-01T09:00:00Z", false},
		"in the team's": {[]string{"--at", "2026-12-01 09:00"}, "2026-12-01T09:00:00+05:30", true},
		"an instant":    {[]string{"--at", "2026-12-01T09:00:00+05:30"}, "2026-12-01T09:00:00+05:30", false},
	} {
		t.Run(name, func(t *testing.T) {
			script := clitest.Routes(t, routes(nil))
			args := append([]string{"schedules", "create", "acme", "--name", "once", "--objective", "Do it."}, tc.args...)
			if _, err := clitest.Execute(t, commands, args...); err != nil {
				t.Fatal(err)
			}
			at := when(t, sent(t, script, http.MethodPost, schedulesPath))["at"]
			if at != tc.want {
				t.Errorf("at = %v, want %s", at, tc.want)
			}
			if got := script.Calls(http.MethodGet, "/teams/"+teamID+"/schedule-settings") > 0; got != tc.readsZone {
				t.Errorf("read the team's zone: %v, want %v", got, tc.readsZone)
			}
		})
	}
}

func TestEveryIsSentInWholeMinutes(t *testing.T) {
	for in, want := range map[string]float64{"1w": 604800, "2d": 172800, "90m": 5400, "1h30m": 5400} {
		t.Run(in, func(t *testing.T) {
			script := clitest.Routes(t, routes(nil))
			if _, err := clitest.Execute(t, commands, "schedules", "create", "acme", "--name", "s", "--objective", "o", "--every", in); err != nil {
				t.Fatal(err)
			}
			if got := when(t, sent(t, script, http.MethodPost, schedulesPath))["every_seconds"]; got != want {
				t.Errorf("every_seconds = %v, want %v", got, want)
			}
		})
	}
}

// What the flags cannot mean is refused before anything is sent.
func TestWhatTheFlagsCannotMeanIsRefusedAndNothingSent(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"part of a minute":       {[]string{"--every", "90s"}, "whole minutes"},
		"not a length":           {[]string{"--every", "often"}, "a length such as"},
		"two times":              {[]string{"--every", "1h", "--cron", "@daily"}, "not more"},
		"no time":                {nil, "say when"},
		"a bad wall time":        {[]string{"--at", "tomorrow"}, "RFC 3339 or YYYY-MM-DD HH:MM"},
		"an agent and a hub":     {[]string{"--every", "1h", "--agent", "triage", "--hub"}, "none of the others can be"},
		"a bare channel":         {[]string{"--every", "1h", "--target", "channel:slack"}, "--target is own"},
		"a channel, no ref":      {[]string{"--every", "1h", "--target", "channel:slack:"}, "--target is own"},
		"a ref, no channel":      {[]string{"--every", "1h", "--target", "channel::C01"}, "--target is own"},
		"a chat that is not":     {[]string{"--every", "1h", "--target", "chat:yesterday"}, "--target is own"},
		"a place unknown":        {[]string{"--every", "1h", "--target", "pigeon:home"}, "--target is own"},
		"own with more":          {[]string{"--every", "1h", "--target", "own:x"}, "--target is own"},
		"direct with a ref":      {[]string{"--every", "1h", "--target", "direct:slack:U1"}, "--target is own"},
		"days past a length":     {[]string{"--every", "8589934592d"}, "a length such as"},
		"weeks past a length":    {[]string{"--every", "20000w"}, "a length such as"},
		"a time with no offset":  {[]string{"--at", "2026-12-01T09:00"}, "has no offset"},
		"a time the clocks skip": {[]string{"--at", "2026-03-29 01:30", "--tz", "Europe/London"}, "the clocks skip it"},
		"an empty objective":     {[]string{"--every", "1h", "--objective", ""}, "the objective is empty"},
	} {
		t.Run(name, func(t *testing.T) {
			script := clitest.Routes(t, routes(nil))
			args := append([]string{"schedules", "create", "acme", "--name", "s", "--objective", "o"}, tc.args...)
			_, err := clitest.Execute(t, commands, args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to say %q", err, tc.want)
			}
			if n := script.Calls(http.MethodPost, schedulesPath); n != 0 {
				t.Errorf("%d schedules sent", n)
			}
		})
	}
}

func TestEveryTargetFormIsSentAsItsPick(t *testing.T) {
	for in, want := range map[string]string{
		"own":                           `{"kind":"own"}`,
		"chat:" + sessionID:             `{"kind":"chat","session_id":"` + sessionID + `"}`,
		"channel:slack:C0123ABCD":       `{"channel_id":"` + channelID + `","kind":"channel","ref":"C0123ABCD"}`,
		"channel:" + channelID + ":C01": `{"channel_id":"` + channelID + `","kind":"channel","ref":"C01"}`,
		"direct:slack":                  `{"channel_id":"` + channelID + `","kind":"direct"}`,
	} {
		t.Run(in, func(t *testing.T) {
			script := clitest.Routes(t, routes(nil))
			if _, err := clitest.Execute(t, commands, "schedules", "create", "acme", "--name", "s", "--objective", "o",
				"--every", "1h", "--target", in); err != nil {
				t.Fatal(err)
			}
			if got := mustJSON(t, sent(t, script, http.MethodPost, schedulesPath)["target"]); got != want {
				t.Errorf("target = %s, want %s", got, want)
			}
		})
	}
	// Left out, it is the brain's default and nothing is sent.
	script := clitest.Routes(t, routes(nil))
	clitest.Seed(t, commands, "schedules", "create", "acme", "--name", "s", "--objective", "o", "--every", "1h")
	if _, ok := sent(t, script, http.MethodPost, schedulesPath)["target"]; ok {
		t.Error("a target was sent though none was given")
	}
}

func TestTheObjectiveIsReadFromAFileOrStdin(t *testing.T) {
	script := clitest.Routes(t, routes(nil))
	if _, err := clitest.ExecuteWithStdin(t, commands, "  Summarise\nthe repo.\n", "schedules", "create", "acme",
		"--name", "s", "--objective-file", "-", "--every", "1h"); err != nil {
		t.Fatal(err)
	}
	if got := sent(t, script, http.MethodPost, schedulesPath)["objective"]; got != "Summarise\nthe repo." {
		t.Errorf("objective = %q", got)
	}

	long := filepath.Join(t.TempDir(), "long.md")
	if err := os.WriteFile(long, []byte(strings.Repeat("é", trigger.MaxObjective+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	script = clitest.Routes(t, routes(nil))
	_, err := clitest.Execute(t, commands, "schedules", "create", "acme", "--name", "s", "--objective-file", long, "--every", "1h")
	if err == nil || !strings.Contains(err.Error(), "4000 at most") || script.Calls(http.MethodPost, schedulesPath) != 0 {
		t.Errorf("a 4001-character objective: %v", err)
	}
}

// An id is used as given; a name is looked up, past the first page; a name
// nobody has says where to look.
func TestAScheduleIsNamedByNameOrID(t *testing.T) {
	t.Run("an id", func(t *testing.T) {
		script := clitest.Routes(t, routes(nil))
		clitest.Seed(t, commands, "schedules", "get", "acme", scheduleID)
		if n := script.Calls(http.MethodGet, schedulesPath); n != 0 {
			t.Errorf("an id was looked up %d times", n)
		}
	})
	t.Run("a name on the second page", func(t *testing.T) {
		script := clitest.Routes(t, routes(map[string]clitest.Reply{
			"GET /teams/{id}/schedules": {
				Status: http.StatusOK, Body: `{"items":[` + schedule(otherID, "digest") + `],"next_cursor":"p2"}`,
				Then: []clitest.Reply{{Status: http.StatusOK, Body: `{"items":[` + schedule(scheduleID, "standup") + `]}`}},
			},
		}))
		clitest.Seed(t, commands, "schedules", "get", "acme", "standup")
		if n := script.Calls(http.MethodGet, schedulesPath+"/"+scheduleID); n != 1 {
			t.Errorf("standup was read %d times", n)
		}
	})
	t.Run("a name nobody has", func(t *testing.T) {
		clitest.Routes(t, routes(nil))
		_, err := clitest.Execute(t, commands, "schedules", "get", "acme", "nightly")
		if err == nil || !strings.Contains(err.Error(), `no schedule named "nightly" among yours`) || !strings.Contains(err.Error(), "oa schedules list") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("two of a name among everyone's", func(t *testing.T) {
		script := clitest.Routes(t, routes(map[string]clitest.Reply{
			"GET /teams/{id}/schedules": {Status: http.StatusOK, Body: `{"items":[` + schedule(otherID, "standup") + `,` + schedule(scheduleID, "standup") + `]}`},
		}))
		_, err := clitest.Execute(t, commands, "schedules", "take-over", "acme", "standup")
		if err == nil || !strings.Contains(err.Error(), "pass the id") {
			t.Errorf("err = %v", err)
		}
		for _, e := range script.All() {
			if e.Path == schedulesPath && !strings.Contains(e.Query, "all=true") {
				t.Errorf("take-over looked among the caller's own: %s", e.Query)
			}
			if e.Method == http.MethodPost {
				t.Errorf("took over one of two: %s", e.Path)
			}
		}
	})
}

func TestUpdatePauseAndResumeSendOnlyWhatIsGiven(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"a name":   {[]string{"update", "acme", scheduleID, "--name", "daily"}, `{"name":"daily"}`},
		"the hub":  {[]string{"update", "acme", scheduleID, "--hub"}, `{"hub":true}`},
		"an agent": {[]string{"update", "acme", scheduleID, "--agent", "triage"}, `{"agent_id":"` + agentID + `"}`},
		"pause":    {[]string{"pause", "acme", scheduleID}, `{"status":"paused"}`},
		"resume":   {[]string{"resume", "acme", scheduleID}, `{"status":"active"}`},
		// A wall time is read in the schedule's own zone.
		"a time": {[]string{"update", "acme", scheduleID, "--at", "2026-12-01 09:00"}, `{"when":{"at":"2026-12-01T09:00:00+05:30","kind":"at"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			script := clitest.Routes(t, routes(nil))
			clitest.Seed(t, commands, append([]string{"schedules"}, tc.args...)...)
			if got := mustJSON(t, sent(t, script, http.MethodPatch, schedulesPath+"/"+scheduleID)); got != tc.want {
				t.Errorf("sent %s, want %s", got, tc.want)
			}
		})
	}
}

// What an update's flags cannot mean is refused before the brain is asked
// anything, the schedule's name not even looked up.
func TestAnUpdateIsReadBeforeAnythingIsAsked(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"nothing":            {nil, "say what to change"},
		"an empty name":      {[]string{"--name", "  "}, "--name is empty"},
		"an empty objective": {[]string{"--name", "daily", "--objective", ""}, "the objective is empty"},
		"part of a minute":   {[]string{"--every", "90s"}, "whole minutes"},
		"two times":          {[]string{"--every", "1h", "--cron", "@daily"}, "not more"},
		"a bad wall time":    {[]string{"--at", "soon"}, "RFC 3339 or YYYY-MM-DD HH:MM"},
	} {
		t.Run(name, func(t *testing.T) {
			script := clitest.Routes(t, routes(nil))
			_, err := clitest.Execute(t, commands, append([]string{"schedules", "update", "acme", "standup"}, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to say %q", err, tc.want)
			}
			if calls := script.All(); len(calls) != 0 {
				t.Errorf("the brain was asked %d times: %+v", len(calls), calls)
			}
		})
	}
	// A name is sent as it is trimmed, as create does.
	script := clitest.Routes(t, routes(nil))
	clitest.Seed(t, commands, "schedules", "update", "acme", scheduleID, "--name", " daily ")
	if got := mustJSON(t, sent(t, script, http.MethodPatch, schedulesPath+"/"+scheduleID)); got != `{"name":"daily"}` {
		t.Errorf("sent %s", got)
	}
}

// An objective longer than the cap can hold is not read past it, and says so.
func TestAnObjectiveFileIsReadNoFurtherThanTheCap(t *testing.T) {
	huge := filepath.Join(t.TempDir(), "huge.md")
	if err := os.WriteFile(huge, []byte(strings.Repeat("a", 1<<20)), 0o600); err != nil {
		t.Fatal(err)
	}
	script := clitest.Routes(t, routes(nil))
	_, err := clitest.Execute(t, commands, "schedules", "create", "acme", "--name", "s", "--every", "1h", "--objective-file", huge)
	if err == nil || !strings.Contains(err.Error(), "over 4000 characters") || script.Calls(http.MethodPost, schedulesPath) != 0 {
		t.Errorf("a 1 MiB objective: %v", err)
	}
	script = clitest.Routes(t, routes(nil))
	_, err = clitest.ExecuteWithStdin(t, commands, strings.Repeat("a", 1<<20), "schedules", "create", "acme", "--name", "s",
		"--every", "1h", "--objective-file", "-")
	if err == nil || !strings.Contains(err.Error(), "over 4000 characters") || script.Calls(http.MethodPost, schedulesPath) != 0 {
		t.Errorf("a 1 MiB objective on stdin: %v", err)
	}
}

// Where one may answer is listed with its --target, one that cannot be
// picked with why, and what a platform could not list on stderr.
func TestPlacesGiveTheirTargetOrWhyNot(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/schedule-targets": {Status: http.StatusOK, Body: `{"items":[
		  {"kind":"channel","label":"#releases","detail":"Public · 14 members","channel":{"id":"` + channelID + `","name":"slack","provider":"slack"},
		   "available":true,"target":{"kind":"channel","channel_id":"` + channelID + `","ref":"C0123ABCD"}},
		  {"kind":"channel","label":"#ops","detail":"Private","channel":{"id":"` + channelID + `","name":"slack","provider":"slack"},
		   "available":false,"reason":"not_in_channel","target":{"kind":"channel","channel_id":"` + channelID + `","ref":"C9"}}],
		 "problems":[{"channel_id":"` + channelID + `","provider":"slack","message":"missing_scope: channels:read"}]}`},
	}))
	out, err := clitest.Execute(t, commands, "schedules", "places", "acme", "--search", "re")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"#releases", "channel:" + channelID + ":C0123ABCD", "#ops", "the app is not in it", "slack: missing_scope: channels:read"} {
		if !strings.Contains(out, w) {
			t.Errorf("no %q in:\n%s", w, out)
		}
	}
	if strings.Contains(out, ":C9") {
		t.Errorf("a place that cannot be picked gave a --target:\n%s", out)
	}
}

// A ref is the platform's, so it cannot drive the terminal either.
func TestAPlacesRefIsMadeSafe(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/schedule-targets": {Status: http.StatusOK, Body: `{"items":[
		  {"kind":"channel","label":"#releases","detail":"","channel":{"id":"` + channelID + `","name":"slack","provider":"slack"},
		   "available":true,"target":{"kind":"channel","channel_id":"` + channelID + `","ref":"C1\u001b]0;pwned\u0007"}}]}`},
	}))
	out, err := clitest.Execute(t, commands, "schedules", "places", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(out, 0x1b) || strings.ContainsRune(out, 0x07) {
		t.Errorf("an escape reached the terminal: %q", out)
	}
}

// What a person or a page wrote cannot drive the terminal.
func TestWhatOthersWroteIsMadeSafe(t *testing.T) {
	evil := strings.ReplaceAll(schedule(scheduleID, `\u001b[2Jclear`), `"last_outcome"`, `"x"`)
	evil = strings.Replace(evil, `"status":"active"`, `"status":"active","last_outcome":"\u001b]0;pwned\u0007"`, 1)
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/schedules": {Status: http.StatusOK, Body: `{"items":[` + evil + `]}`},
	}))
	out, err := clitest.Execute(t, commands, "schedules", "list", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(out, 0x1b) || strings.ContainsRune(out, 0x07) {
		t.Errorf("an escape reached the terminal: %q", out)
	}
}

// A value the brain names from a set it may grow is printed safe too, as one
// this build has never seen.
func TestAnUnknownValueIsMadeSafe(t *testing.T) {
	odd := strings.NewReplacer(
		`"status":"active"`, `"status":"\u001b[2J","paused_reason":"\u001b]0;a\u0007"`,
		`"when":{"kind":"every","every_seconds":3600}`, `"when":{"kind":"\u001b[1m"}`,
		`"target":{"kind":"own"}`, `"target":{"kind":"\u001b[4m"}`,
	).Replace(schedule(scheduleID, "standup"))
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/schedules/{sid}": {Status: http.StatusOK, Body: odd},
		"GET /teams/{id}/schedule-targets": {Status: http.StatusOK, Body: `{"items":[
		  {"kind":"\u001b[5m","label":"x","detail":"","channel":{"id":"` + channelID + `","name":"slack","provider":"slack"},
		   "available":false,"reason":"\u001b[6m","target":{"kind":"own"}}]}`},
		"GET /teams/{id}/schedules/{sid}/fires": {Status: http.StatusOK, Body: `{"items":[{"scheduled_for":"2026-11-28T03:30:00Z","outcome":"\u001b[7m"}]}`},
	}))
	for _, args := range [][]string{
		{"schedules", "get", "acme", scheduleID},
		{"schedules", "places", "acme"},
		{"schedules", "fires", "acme", scheduleID},
	} {
		out, err := clitest.Execute(t, commands, args...)
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsRune(out, 0x1b) || strings.ContainsRune(out, 0x07) {
			t.Errorf("%s: an escape reached the terminal: %q", args[1], out)
		}
	}
}

const settingsPath = "/teams/" + teamID + "/schedule-settings"

// A bool given false is given: it says no, rather than being dropped.
func TestSettingsSetReadsABoolGivenFalse(t *testing.T) {
	was := map[bool]string{
		true:  settings,
		false: strings.Replace(settings, `"agents_may_schedule":true`, `"agents_may_schedule":false`, 1),
	}
	for _, tc := range []struct {
		was  bool
		arg  string
		want bool
	}{
		{true, "--agents-may-schedule=false", false},
		{false, "--agents-may-schedule", true},
		{false, "--agents-may-schedule=true", true},
		{true, "--no-agents-may-schedule", false},
		{false, "--no-agents-may-schedule=false", true},
	} {
		t.Run(tc.arg, func(t *testing.T) {
			script := clitest.Routes(t, routes(map[string]clitest.Reply{
				"GET /teams/{id}/schedule-settings": {Status: http.StatusOK, Body: was[tc.was]},
				"PUT /teams/{id}/schedule-settings": {Status: http.StatusOK, Body: settings},
			}))
			clitest.Seed(t, commands, "schedules", "settings", "set", "acme", tc.arg)
			if got := sent(t, script, http.MethodPut, settingsPath)["agents_may_schedule"]; got != tc.want {
				t.Errorf("agents_may_schedule = %v, want %v", got, tc.want)
			}
		})
	}
}

// The brain's ranges are said before a save, and its edges are taken.
func TestSettingsSetChecksTheBrainsRanges(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"an interval too short":   {[]string{"--min-interval", "4m"}, "between 5m and 7d"},
		"an interval too long":    {[]string{"--min-interval", "8d"}, "between 5m and 7d"},
		"nobody may have one":     {[]string{"--per-member", "0"}, "--per-member is between 1 and 100"},
		"too many for one":        {[]string{"--per-member", "101"}, "--per-member is between 1 and 100"},
		"too many for a team":     {[]string{"--per-team", "1001"}, "--per-team is between 1 and 1000"},
		"no fires":                {[]string{"--fires-per-hour", "0"}, "--fires-per-hour is between 1 and 1000"},
		"too many fires":          {[]string{"--fires-per-hour", "1001"}, "--fires-per-hour is between 1 and 1000"},
		"a team below one person": {[]string{"--per-team", "4"}, "fewer than the 5 one person may"},
	} {
		t.Run(name, func(t *testing.T) {
			script := clitest.Routes(t, routes(nil))
			_, err := clitest.Execute(t, commands, append([]string{"schedules", "settings", "set", "acme"}, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to say %q", err, tc.want)
			}
			if n := script.Calls(http.MethodPut, settingsPath); n != 0 {
				t.Errorf("%d saves", n)
			}
		})
	}
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"PUT /teams/{id}/schedule-settings": {Status: http.StatusOK, Body: settings},
	}))
	clitest.Seed(t, commands, "schedules", "settings", "set", "acme", "--min-interval", "7d", "--per-member", "100",
		"--per-team", "1000", "--fires-per-hour", "1000")
	want := `{"agents_may_schedule":true,"fires_per_hour":1000,"schedule_min_interval_minutes":10080,"schedules_per_member":100,` +
		`"schedules_per_team":1000,"timezone":"Asia/Kolkata"}`
	if got := mustJSON(t, sent(t, script, http.MethodPut, settingsPath)); got != want {
		t.Errorf("the highest of each: sent %s", got)
	}
	script = clitest.Routes(t, routes(map[string]clitest.Reply{
		"PUT /teams/{id}/schedule-settings": {Status: http.StatusOK, Body: settings},
	}))
	clitest.Seed(t, commands, "schedules", "settings", "set", "acme", "--min-interval", "5m", "--per-member", "1",
		"--per-team", "1", "--fires-per-hour", "1")
	if got := sent(t, script, http.MethodPut, settingsPath)["schedule_min_interval_minutes"]; got != float64(5) {
		t.Errorf("the lowest interval: sent %v", got)
	}
}

// Settings keep what was not given, as the brain takes the whole.
func TestSettingsSetKeepsWhatWasNotGiven(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"PUT /teams/{id}/schedule-settings": {Status: http.StatusOK, Body: settings},
	}))
	clitest.Seed(t, commands, "schedules", "settings", "set", "acme", "--no-agents-may-schedule", "--min-interval", "2h", "--per-team", "30")
	want := `{"agents_may_schedule":false,"fires_per_hour":20,"schedule_min_interval_minutes":120,"schedules_per_member":5,` +
		`"schedules_per_team":30,"timezone":"Asia/Kolkata"}`
	if got := mustJSON(t, sent(t, script, http.MethodPut, "/teams/"+teamID+"/schedule-settings")); got != want {
		t.Errorf("sent %s\nwant %s", got, want)
	}
	for name, args := range map[string][]string{
		"nothing":  nil,
		"both":     {"--agents-may-schedule", "--no-agents-may-schedule"},
		"too long": {"--min-interval", "40d"},
	} {
		t.Run(name, func(t *testing.T) {
			script := clitest.Routes(t, routes(nil))
			if _, err := clitest.Execute(t, commands, append([]string{"schedules", "settings", "set", "acme"}, args...)...); err == nil {
				t.Error("it was taken")
			}
			if n := script.Calls(http.MethodPut, "/teams/"+teamID+"/schedule-settings"); n != 0 {
				t.Errorf("%d saves", n)
			}
		})
	}
}

func TestRunConfirmDeleteAndFiresCallTheirRoutes(t *testing.T) {
	for name, tc := range map[string]struct {
		args   []string
		method string
		path   string
		reply  clitest.Reply
	}{
		"run":       {[]string{"run", "acme", scheduleID}, http.MethodPost, schedulesPath + "/" + scheduleID + "/run", clitest.Reply{Status: http.StatusAccepted, Body: `{"run_id":"` + otherID + `"}`}},
		"confirm":   {[]string{"confirm", "acme", scheduleID}, http.MethodPost, schedulesPath + "/" + scheduleID + "/confirm", clitest.Reply{Status: http.StatusOK, Body: schedule(scheduleID, "standup")}},
		"take-over": {[]string{"take-over", "acme", scheduleID}, http.MethodPost, schedulesPath + "/" + scheduleID + "/take-over", clitest.Reply{Status: http.StatusOK, Body: schedule(scheduleID, "standup")}},
		"delete":    {[]string{"delete", "acme", scheduleID}, http.MethodDelete, schedulesPath + "/" + scheduleID, clitest.Reply{Status: http.StatusNoContent}},
		"fires": {[]string{"fires", "acme", scheduleID}, http.MethodGet, schedulesPath + "/" + scheduleID + "/fires", clitest.Reply{
			Status: http.StatusOK,
			Body:   `{"items":[{"scheduled_for":"2026-11-28T03:30:00Z","fired_at":"2026-11-28T03:30:01Z","outcome":"answered"}]}`,
		}},
	} {
		t.Run(name, func(t *testing.T) {
			script := clitest.Routes(t, routes(map[string]clitest.Reply{tc.method + " " + tc.path: tc.reply}))
			out, err := clitest.Execute(t, commands, append([]string{"schedules"}, tc.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			if n := script.Calls(tc.method, tc.path); n != 1 {
				t.Errorf("%s %s called %d times:\n%s", tc.method, tc.path, n, out)
			}
		})
	}
}

// Every verb is reachable from oa: one built and never added passes every
// test of its body.
func TestEveryVerbIsRegistered(t *testing.T) {
	root := New(cli.NewOptions(&bytes.Buffer{}, &bytes.Buffer{}))
	var names []string
	for _, c := range root.Commands() {
		names = append(names, c.Name())
		if c.Name() == "settings" {
			for _, s := range c.Commands() {
				names = append(names, "settings "+s.Name())
			}
		}
	}
	for _, want := range []string{
		"list", "get", "create", "update", "pause", "resume", "delete", "run", "fires",
		"confirm", "take-over", "places", "settings show", "settings set",
	} {
		if !contains(names, want) {
			t.Errorf("no %q in %v", want, names)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
