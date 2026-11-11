package setup

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
	"github.com/LaplacianAI/openarity/apps/cli/skills"
)

var commands = []clitest.Build{New}

func shipped(t *testing.T) []byte {
	t.Helper()
	b, err := fs.ReadFile(skills.FS, "build-a-team/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return string(b)
}

func TestSkillsGoWhereEveryHarnessLooksInTheHomeFolder(t *testing.T) {
	clitest.Isolate(t)
	home, _ := os.UserHomeDir()

	out, err := clitest.Execute(t, commands, "setup", "skills")
	if err != nil {
		t.Fatalf("setup skills: %v\n%s", err, out)
	}
	for _, dir := range []string{".agents/skills", ".claude/skills"} {
		for _, f := range []string{"SKILL.md", "reference.md"} {
			path := filepath.Join(home, dir, "build-a-team", f)
			if f == "SKILL.md" && read(t, path) != string(shipped(t)) {
				t.Errorf("%s is not the copy oa ships", path)
			}
			read(t, path)
		}
		if !strings.Contains(out, filepath.Join(home, dir)) {
			t.Errorf("output does not name %s:\n%s", dir, out)
		}
	}
}

func TestProjectAndDirChooseWhereInstead(t *testing.T) {
	clitest.Isolate(t)
	home, _ := os.UserHomeDir()
	project := t.TempDir()
	t.Chdir(project)

	if _, err := clitest.Execute(t, commands, "setup", "skills", "--project"); err != nil {
		t.Fatalf("--project: %v", err)
	}
	read(t, filepath.Join(project, ".agents/skills/build-a-team/SKILL.md"))
	read(t, filepath.Join(project, ".claude/skills/build-a-team/SKILL.md"))

	mine := filepath.Join(t.TempDir(), "my-agent")
	if _, err := clitest.Execute(t, commands, "setup", "skills", "--dir", mine); err != nil {
		t.Fatalf("--dir: %v", err)
	}
	read(t, filepath.Join(mine, "build-a-team/SKILL.md"))

	if _, err := os.Stat(filepath.Join(home, ".agents")); err == nil {
		t.Error("--project or --dir also wrote to the home folder")
	}
	if _, err := clitest.Execute(t, commands, "setup", "skills", "--project", "--dir", mine); err == nil {
		t.Error("--project with --dir was taken; it should say to pick one")
	}
}

// A copy someone edited is theirs until they say otherwise, and finding one
// stops the install before any folder is written.
func TestAChangedCopyIsKeptUnlessForced(t *testing.T) {
	clitest.Isolate(t)
	home, _ := os.UserHomeDir()
	if _, err := clitest.Execute(t, commands, "setup", "skills"); err != nil {
		t.Fatal(err)
	}
	if _, err := clitest.Execute(t, commands, "setup", "skills"); err != nil {
		t.Errorf("a second install of the same copy failed: %v", err)
	}

	edited := filepath.Join(home, ".claude/skills/build-a-team/SKILL.md")
	reference := filepath.Join(home, ".agents/skills/build-a-team/reference.md")
	if err := os.WriteFile(edited, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(reference); err != nil {
		t.Fatal(err)
	}

	_, err := clitest.Execute(t, commands, "setup", "skills")
	if err == nil || !strings.Contains(err.Error(), edited) || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v, want it to name %s and --force", err, edited)
	}
	if read(t, edited) != "mine" {
		t.Error("the edited copy was overwritten")
	}
	if _, err := os.Stat(reference); err == nil {
		t.Error("another folder was written before the refusal")
	}

	if _, err := clitest.Execute(t, commands, "setup", "skills", "--force"); err != nil {
		t.Fatalf("--force: %v", err)
	}
	if read(t, edited) != string(shipped(t)) {
		t.Error("--force did not replace the edited copy")
	}
}
