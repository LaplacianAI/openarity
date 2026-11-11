package main

import (
	"io"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/skills"
)

// The skills oa ships tell an agent which commands to run. One that names a
// command oa does not have would send it off to guess, so every `oa <verb>`
// and `oa <noun> <verb>` they mention must be on the real root.
func TestEveryCommandTheShippedSkillsNameExists(t *testing.T) {
	t.Parallel()
	root := cli.NewRoot(io.Discard, io.Discard, commands)
	mention := regexp.MustCompile(`\boa ([a-z][a-z-]*)(?: ([a-z][a-z-]*))?`)

	seen := 0
	err := fs.WalkDir(skills.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		text, err := fs.ReadFile(skills.FS, path)
		if err != nil {
			return err
		}
		for _, m := range mention.FindAllStringSubmatch(string(text), -1) {
			seen++
			cmd, _, err := root.Find([]string{m[1]})
			if err != nil || cmd == root {
				t.Errorf("%s names oa %s, which oa does not have", path, m[1])
				continue
			}
			if !cmd.HasSubCommands() || m[2] == "" {
				continue
			}
			if sub, _, err := cmd.Find([]string{m[2]}); err != nil || sub == cmd {
				t.Errorf("%s names oa %s %s, which oa does not have", path, m[1], m[2])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen < 10 {
		t.Errorf("found %d mentions of oa commands; the pattern no longer matches how the skills write them", seen)
	}
}
