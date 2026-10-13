package skills

import (
	"archive/zip"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

var commands = []clitest.Build{New}

const (
	teamID  = "6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d"
	skillID = "3f9a41c2-7e58-4b0d-9a11-8d2c0e5b71c1"
)

const oneTeam = `{"items":[{"id":"` + teamID + `","name":"platform","role":"admin"}]}`

const twoSkills = `{"items":[
  {"id":"` + skillID + `","team_id":"` + teamID + `","name":"pdf","description":"Read PDFs","source":"upload","created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z"},
  {"id":"8b3d0a6e-9f4c-4f3a-9c3a-4d0a6e9f4c6f","team_id":"` + teamID + `","name":"docx","description":"Write Word","source":"github","created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z"}
]}`

const oneSkill = `{"id":"` + skillID + `","team_id":"` + teamID + `","name":"pdf","description":"Read PDFs",
  "source":"github","source_ref":"github:acme/skills/pdf@main","body":"# pdf","metadata":{},
  "files":[{"path":"scripts/read.py","size":120,"sha256":"ab","media_type":"text/plain"}],
  "created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z"}`

func routes(extra map[string]clitest.Reply) map[string]clitest.Reply {
	r := map[string]clitest.Reply{
		"GET /teams":             {Status: http.StatusOK, Body: oneTeam},
		"GET /teams/{id}/skills": {Status: http.StatusOK, Body: twoSkills},
	}
	for k, v := range extra {
		r[k] = v
	}
	return r
}

func TestSkillsListRendersEachSkill(t *testing.T) {
	clitest.Routes(t, routes(nil))

	out, err := clitest.Execute(t, commands, "skills", "list", "platform")
	if err != nil {
		t.Fatalf("skills list: %v\n%s", err, out)
	}
	for _, want := range []string{"pdf", "upload", "docx", "github", skillID} {
		if !strings.Contains(out, want) {
			t.Errorf("the table is missing %q:\n%s", want, out)
		}
	}
}

func TestSkillsListPrintsTheEnvelope(t *testing.T) {
	clitest.Routes(t, routes(nil))

	out, err := clitest.Execute(t, commands, "skills", "list", "platform", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.Items) != 2 {
		t.Fatalf("not the envelope: %v\n%s", err, out)
	}
}

// A skill named on the command line is looked up in that team's own list,
// and the id it resolves to is the one fetched.
func TestSkillsGetResolvesTheNameAndShowsTheFiles(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/skills/{skillID}": {Status: http.StatusOK, Body: oneSkill},
	}))

	out, err := clitest.Execute(t, commands, "skills", "get", "platform", "pdf")
	if err != nil {
		t.Fatalf("skills get: %v\n%s", err, out)
	}
	if script.Calls("GET", "/teams/"+teamID+"/skills/"+skillID) != 1 {
		t.Errorf("the resolved skill was not fetched: %+v", script.All())
	}
	for _, want := range []string{"SKILL.md", "scripts/read.py", "github:acme/skills/pdf@main", `"Read PDFs"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

// An id is never looked up: a script passing ids pays nothing.
func TestSkillsGetTakesAnIDWithoutALookup(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/skills/{skillID}": {Status: http.StatusOK, Body: oneSkill},
	}))

	if _, err := clitest.Execute(t, commands, "skills", "get", teamID, skillID); err != nil {
		t.Fatal(err)
	}
	if n := script.Calls("GET", "/teams/"+teamID+"/skills"); n != 0 {
		t.Errorf("an id was looked up %d times", n)
	}
}

func TestANameNobodyHasIsReportedAsAName(t *testing.T) {
	clitest.Routes(t, routes(nil))

	_, err := clitest.Execute(t, commands, "skills", "get", "platform", "xlsx")
	if err == nil || !strings.Contains(err.Error(), `no skill named "xlsx"`) || !strings.Contains(err.Error(), "oa skills list") {
		t.Errorf("err = %v", err)
	}
}

// --- uploading ---

func writeSkill(t *testing.T, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A directory becomes one part per file, each named by its path from the
// top of the skill, which is what the brain's upload reads.
func TestUploadingADirectorySendsEachFileByItsPath(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/skills": {Status: http.StatusCreated, Body: oneSkill},
	}))
	dir := writeSkill(t, map[string]string{
		"SKILL.md":           "---\nname: pdf\ndescription: Read PDFs\n---\n",
		"scripts/read.py":    "print(1)",
		".git/HEAD":          "ref: refs/heads/main",
		".git/objects/ab/cd": "x",
	})

	out, err := clitest.Execute(t, commands, "skills", "upload", "platform", dir)
	if err != nil {
		t.Fatalf("upload: %v\n%s", err, out)
	}
	if !strings.Contains(out, "uploaded") {
		t.Errorf("no confirmation:\n%s", out)
	}

	var body string
	for _, e := range script.All() {
		if e.Method == http.MethodPost {
			body = e.Body
		}
	}
	for _, want := range []string{`filename="SKILL.md"`, `filename="scripts/read.py"`, `name="files"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the request lacks %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, ".git") {
		t.Error("the .git directory was uploaded")
	}
}

func TestUploadingAZipSendsItAsItIs(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/skills": {Status: http.StatusCreated, Body: oneSkill},
	}))

	name := filepath.Join(t.TempDir(), "pdf.zip")
	f, err := os.Create(name) //nolint:gosec // a test's own temporary file
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("SKILL.md")
	_, _ = w.Write([]byte("---\nname: pdf\n---\n"))
	_ = zw.Close()
	_ = f.Close()

	if _, err := clitest.Execute(t, commands, "skills", "upload", "platform", name); err != nil {
		t.Fatal(err)
	}
	for _, e := range script.All() {
		if e.Method == http.MethodPost && !strings.HasPrefix(e.Body, "PK") {
			t.Errorf("the zip was not sent as it is: %q", e.Body[:min(20, len(e.Body))])
		}
	}
}

// Refused here rather than after twenty megabytes reached the brain.
func TestWhatCannotBeASkillIsRefusedBeforeAnyRequest(t *testing.T) {
	notZip := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(notZip, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	many := map[string]string{}
	for i := range maxFiles + 1 {
		many[filepath.Join("refs", strings.Repeat("a", 1+i%5), "f"+string(rune('a'+i%26))+strings.Repeat("x", i/26))] = "x"
	}

	for name, path := range map[string]string{
		"a file that is not a zip": notZip,
		"nothing at all":           filepath.Join(t.TempDir(), "missing"),
		"an empty directory":       t.TempDir(),
		"too many files":           writeSkill(t, many),
	} {
		t.Run(name, func(t *testing.T) {
			script := clitest.Routes(t, routes(nil))
			if _, err := clitest.Execute(t, commands, "skills", "upload", "platform", path); err == nil {
				t.Fatal("accepted")
			}
			if n := len(script.All()); n != 0 {
				t.Errorf("%d requests were made for something that could not be sent", n)
			}
		})
	}
}

// --- what the brain refuses ---

// A 403 says which permission the route needed, because the brain does not.
func TestARefusedUploadNamesThePermission(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/skills": {Status: http.StatusForbidden, Body: `{"error":"forbidden"}`},
	}))
	dir := writeSkill(t, map[string]string{"SKILL.md": "---\nname: pdf\n---\n"})

	_, err := clitest.Execute(t, commands, "skills", "upload", "platform", dir)
	if err == nil || !strings.Contains(err.Error(), "skill:write") || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("err = %v", err)
	}
}

func TestEveryWriteNamesSkillWriteWhenRefused(t *testing.T) {
	dir := writeSkill(t, map[string]string{"SKILL.md": "---\nname: pdf\n---\n"})
	forbidden := clitest.Reply{Status: http.StatusForbidden, Body: `{"error":"forbidden"}`}

	for name, tc := range map[string]struct {
		route string
		args  []string
	}{
		"replace": {"PUT /teams/{id}/skills/{skillID}", []string{"replace", "platform", "pdf", dir}},
		"import":  {"POST /teams/{id}/skills/import", []string{"import", "platform", "github:acme/skills/pdf@main"}},
		"sync":    {"POST /teams/{id}/skills/{skillID}/sync", []string{"sync", "platform", "pdf"}},
		"delete":  {"DELETE /teams/{id}/skills/{skillID}", []string{"delete", "platform", "pdf"}},
	} {
		t.Run(name, func(t *testing.T) {
			clitest.Routes(t, routes(map[string]clitest.Reply{tc.route: forbidden}))
			_, err := clitest.Execute(t, commands, append([]string{"skills"}, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), "skill:write") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

// A read is never told it needed a write permission.
func TestARefusedReadDoesNotBlameSkillWrite(t *testing.T) {
	clitest.Routes(t, map[string]clitest.Reply{
		"GET /teams":             {Status: http.StatusOK, Body: oneTeam},
		"GET /teams/{id}/skills": {Status: http.StatusForbidden, Body: `{"error":"forbidden"}`},
	})

	_, err := clitest.Execute(t, commands, "skills", "list", "platform")
	if err == nil || strings.Contains(err.Error(), "skill:write") {
		t.Errorf("err = %v", err)
	}
}

func TestImportSendsTheSource(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"POST /teams/{id}/skills/import": {Status: http.StatusCreated, Body: oneSkill},
	}))

	if _, err := clitest.Execute(t, commands, "skills", "import", "platform", "github:acme/skills/pdf@main"); err != nil {
		t.Fatal(err)
	}
	for _, e := range script.All() {
		if e.Method == http.MethodPost && !strings.Contains(e.Body, `"source":"github:acme/skills/pdf@main"`) {
			t.Errorf("body = %s", e.Body)
		}
	}
}

// --- a file ---

// The bytes as stored, whatever the output format, so a redirect gives the
// file back.
func TestCatWritesTheFileAsItIs(t *testing.T) {
	script := clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/skills/{skillID}/files/{path...}": {Status: http.StatusOK, Body: "print(1)\n"},
	}))

	out, err := clitest.Execute(t, commands, "skills", "cat", "platform", "pdf", "/scripts/read.py", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if out != "print(1)\n" {
		t.Errorf("out = %q", out)
	}
	if script.Calls("GET", "/teams/"+teamID+"/skills/"+skillID+"/files/scripts/read.py") != 1 {
		t.Errorf("requests = %+v", script.All())
	}
}

func TestCatOfAMissingFileIsAnError(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"GET /teams/{id}/skills/{skillID}/files/{path...}": {Status: http.StatusNotFound, Body: `{"error":"no such file"}`},
	}))

	if _, err := clitest.Execute(t, commands, "skills", "cat", "platform", "pdf", "nope.txt"); err == nil {
		t.Error("a missing file printed nothing and succeeded")
	}
}

func TestDeletePrintsWhatItDeleted(t *testing.T) {
	clitest.Routes(t, routes(map[string]clitest.Reply{
		"DELETE /teams/{id}/skills/{skillID}": {Status: http.StatusNoContent},
	}))

	out, err := clitest.Execute(t, commands, "skills", "delete", "platform", "pdf")
	if err != nil || !strings.Contains(out, "deleted") || !strings.Contains(out, skillID) {
		t.Errorf("delete: %v\n%s", err, out)
	}
}
