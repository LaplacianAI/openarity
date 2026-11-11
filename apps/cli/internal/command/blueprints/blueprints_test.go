package blueprints

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

var commands = []clitest.Build{NewPlan, NewApply}

const teamID = "8b0b1a52-3c1f-4c55-9a8e-6f4e3f1d2a10"

const (
	planPath  = "/teams/" + teamID + "/blueprints/plan"
	applyPath = "/teams/" + teamID + "/blueprints/apply"
)

const skillMD = "---\nname: support-faq\ndescription: Answers from the FAQ.\n---\nRead faq.md.\n"

// blueprint writes a team.yaml beside a skill folder, as a person would lay
// them out, and returns the yaml's path.
func blueprint(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	skill := filepath.Join(dir, "skills", "support-faq")
	if err := os.MkdirAll(filepath.Join(skill, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		filepath.Join(skill, "SKILL.md"):     skillMD,
		filepath.Join(skill, ".git", "HEAD"): "ref: refs/heads/main\n",
		filepath.Join(dir, "team.yaml"):      yaml,
	} {
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "team.yaml")
}

const teamYAML = `
skills:
  - path: ./skills/support-faq
agents:
  - name: triage
    model: {name: m, max_tokens: 1024}
    pattern: react
    max_steps: 5
    skills: [support-faq]
`

const toChange = `{
  "changes": [
    {"kind": "skill", "name": "support-faq", "action": "create"},
    {"kind": "agent", "name": "triage", "action": "update", "fields": ["instructions", "skills"], "id": "11111111-1111-1111-1111-111111111111"},
    {"kind": "mcp_server", "name": "helpdesk", "action": "unchanged", "id": "22222222-2222-2222-2222-222222222222"}
  ],
  "refusals": [], "digest": "sha256:shown"
}`

const appliedBody = `{"changes": [
  {"kind": "skill", "name": "support-faq", "action": "create", "id": "33333333-3333-3333-3333-333333333333"},
  {"kind": "agent", "name": "triage", "action": "update", "id": "11111111-1111-1111-1111-111111111111"}
]}`

func routes(plan string, apply clitest.Reply) map[string]clitest.Reply {
	return map[string]clitest.Reply{
		"POST " + planPath:  {Status: http.StatusOK, Body: plan},
		"POST " + applyPath: apply,
	}
}

func TestPlanSendsEachSkillsFilesAndShowsEveryChange(t *testing.T) {
	script := clitest.Routes(t, routes(toChange, clitest.Reply{}))

	out, err := clitest.Execute(t, commands, "plan", teamID, "-f", blueprint(t, teamYAML))
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}

	body := script.All()[0].Body
	if want := base64.StdEncoding.EncodeToString([]byte(skillMD)); !strings.Contains(body, `{"data":"`+want+`","path":"SKILL.md"}`) {
		t.Errorf("body = %s, want SKILL.md read from the folder beside the file", body)
	}
	if strings.Contains(body, ".git") {
		t.Errorf("body = %s, want .git left out", body)
	}
	for _, want := range []string{
		"+  skill", "support-faq", "create",
		"~  agent", "update: instructions, skills",
		"helpdesk", "unchanged",
		"1 to create, 1 to change, 1 unchanged.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if n := script.Calls("POST", applyPath); n != 0 {
		t.Errorf("plan applied: %d calls", n)
	}
}

func TestAMisspeltKeyIsRefusedBeforeAnythingIsSent(t *testing.T) {
	script := clitest.Routes(t, routes(toChange, clitest.Reply{}))

	_, err := clitest.Execute(t, commands, "plan", teamID, "-f", blueprint(t, "agents:\n  - name: a\n    max_step: 5\n"))
	if err == nil || !strings.Contains(err.Error(), "max_step") {
		t.Fatalf("err = %v, want it to name max_step", err)
	}
	if len(script.All()) != 0 {
		t.Errorf("%d requests were sent", len(script.All()))
	}
}

func TestApplyAppliesThePlanItShowedOnlyWhenTheAnswerIsYes(t *testing.T) {
	for answer, applies := range map[string]bool{"y\n": true, "yes\n": true, "n\n": false, "\n": false} {
		script := clitest.Routes(t, routes(toChange, clitest.Reply{Status: http.StatusOK, Body: appliedBody}))

		out, err := clitest.ExecuteWithStdin(t, commands, answer, "apply", teamID, "-f", blueprint(t, teamYAML))
		if n := script.Calls("POST", applyPath); (n == 1) != applies {
			t.Errorf("%q: %d applies, want applied=%v", answer, n, applies)
		}
		if !applies {
			if err == nil || !strings.Contains(err.Error(), "nothing was applied") {
				t.Errorf("%q: err = %v", answer, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v\n%s", answer, err, out)
		}
		last := script.All()[len(script.All())-1].Body
		if !strings.Contains(last, `"plan_digest":"sha256:shown"`) {
			t.Errorf("apply body = %s, want the digest of the plan shown", last)
		}
		if !strings.Contains(out, "1 created, 1 changed.") {
			t.Errorf("output:\n%s", out)
		}
	}
}

func TestARefusedPlanIsNeverApplied(t *testing.T) {
	refused := `{"changes": [{"kind": "agent", "name": "triage", "action": "create"}],
	  "refusals": [{"kind": "agent", "name": "triage", "message": "skills: \"faq\" is not a skill in this team or this blueprint"}],
	  "digest": "sha256:x"}`
	script := clitest.Routes(t, routes(refused, clitest.Reply{}))

	out, err := clitest.Execute(t, commands, "apply", teamID, "-f", blueprint(t, teamYAML), "--yes")
	if err == nil || !strings.Contains(err.Error(), "1 refusal") {
		t.Errorf("err = %v, want it to count the refusal", err)
	}
	if !strings.Contains(out, `"faq" is not a skill`) {
		t.Errorf("the refusal is not shown:\n%s", out)
	}
	if n := script.Calls("POST", applyPath); n != 0 {
		t.Errorf("a refused plan was applied: %d calls", n)
	}
}

func TestNothingToChangeAsksNothingAndAppliesNothing(t *testing.T) {
	same := `{"changes": [{"kind": "agent", "name": "triage", "action": "unchanged", "id": "11111111-1111-1111-1111-111111111111"}], "refusals": [], "digest": "sha256:x"}`
	script := clitest.Routes(t, routes(same, clitest.Reply{}))

	out, err := clitest.Execute(t, commands, "apply", teamID, "-f", blueprint(t, teamYAML))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(out, "Nothing to change.") || script.Calls("POST", applyPath) != 0 {
		t.Errorf("output %q, %d applies", out, script.Calls("POST", applyPath))
	}
}

func TestATeamThatChangedShowsTheNewPlanAndSaysNothingWasApplied(t *testing.T) {
	newer := `{"changes": [{"kind": "agent", "name": "triage", "action": "update", "fields": ["model"]}], "refusals": [], "digest": "sha256:new"}`
	clitest.Routes(t, routes(toChange, clitest.Reply{Status: http.StatusConflict, Body: newer}))

	out, err := clitest.Execute(t, commands, "apply", teamID, "-f", blueprint(t, teamYAML), "--yes")
	if err == nil || !strings.Contains(err.Error(), "nothing was applied") {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(out, "update: model") {
		t.Errorf("the new plan is not shown:\n%s", out)
	}
}

// Under json, apply prints one document a script can read, so it cannot
// show a plan and ask about it.
func TestApplyAsJSONNeedsYesAndPrintsOneDocument(t *testing.T) {
	script := clitest.Routes(t, routes(toChange, clitest.Reply{Status: http.StatusOK, Body: appliedBody}))
	file := blueprint(t, teamYAML)

	if _, err := clitest.Execute(t, commands, "apply", teamID, "-f", file, "-o", "json"); err == nil {
		t.Error("apply -o json without --yes asked a question nobody could see")
	}
	if len(script.All()) != 0 {
		t.Fatalf("%d requests before --yes", len(script.All()))
	}

	out, err := clitest.Execute(t, commands, "apply", teamID, "-f", file, "-o", "json", "--yes")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var doc struct {
		Changes []json.RawMessage `json:"changes"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || len(doc.Changes) != 2 {
		t.Errorf("output is not one applied document (%v):\n%s", err, out)
	}
}

func TestABlueprintOnStdinNeedsYes(t *testing.T) {
	script := clitest.Routes(t, routes(toChange, clitest.Reply{}))

	_, err := clitest.ExecuteWithStdin(t, commands, "agents: []\n", "apply", teamID, "-f", "-")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("err = %v, want it to ask for --yes", err)
	}
	if n := script.Calls("POST", applyPath); n != 0 {
		t.Errorf("%d applies", n)
	}
}

// A name in a plan is the team's own text: it reaches the terminal without
// the escapes in it.
func TestANameIsMadeSafeForTheTerminal(t *testing.T) {
	evil := `{"changes": [{"kind": "agent", "name": "tri\u001b[2Jage", "action": "create"}], "refusals": [], "digest": "sha256:x"}`
	clitest.Routes(t, routes(evil, clitest.Reply{}))

	out, err := clitest.Execute(t, commands, "plan", teamID, "-f", blueprint(t, teamYAML))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if strings.Contains(out, "\x1b[2J") {
		t.Errorf("the escape reached the terminal: %q", out)
	}
}

func TestASkillWithNoPathIsRefused(t *testing.T) {
	clitest.Routes(t, routes(toChange, clitest.Reply{}))

	_, err := clitest.Execute(t, commands, "plan", teamID, "-f", blueprint(t, "skills:\n  - {}\n"))
	if err == nil || !strings.Contains(err.Error(), "no path") {
		t.Errorf("err = %v", err)
	}
}

// Each server the apply wrote says what it answered, and one that could not
// be asked says why and how to ask again; the reason is the brain's text, so
// it is made safe.
func TestApplySaysWhatEachServerItWroteAnswered(t *testing.T) {
	body := `{"changes": [{"kind": "mcp_server", "name": "billing", "action": "create", "id": "44444444-4444-4444-4444-444444444444"}],
 "discovered": [
  {"server": "billing", "tools": ["refund", "charge"]},
  {"server": "crm", "tools": [], "error": "auth_secret_ref names a secret\u001b[2J that is not set"}
 ]}`
	clitest.Routes(t, routes(toChange, clitest.Reply{Status: http.StatusOK, Body: body}))

	out, err := clitest.Execute(t, commands, "apply", teamID, "-f", blueprint(t, teamYAML), "--yes")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	for _, want := range []string{
		"discovered billing: 2 tools.",
		"not discovered crm: auth_secret_ref names a secret",
		"oa mcp-servers discover " + teamID + " crm",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b[2J") {
		t.Errorf("the reason reached the terminal raw:\n%q", out)
	}
}
