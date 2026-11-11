// Package setup is oa setup: putting what oa ships where other tools find it.
package setup

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/skills"
)

func New(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Put what oa ships where other tools find it",
	}
	cmd.AddCommand(newSkillsCmd(opts))
	return cmd
}

// The folders coding agents read skills from: .agents/skills is the shared
// one (Codex, GitHub Copilot, Gemini CLI, OpenCode, Cursor and others), and
// Claude Code reads its own.
var harnessDirs = []string{filepath.Join(".agents", "skills"), filepath.Join(".claude", "skills")}

func newSkillsCmd(opts *cli.Options) *cobra.Command {
	var (
		project bool
		dir     string
		force   bool
	)

	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Install oa's agent skills for your coding agents",
		Long: "Writes the skills oa ships, such as build-a-team, in the Agent Skills format\n" +
			"that most coding agents read. By default they go in your home folder, under\n" +
			".agents/skills (Codex, GitHub Copilot, Gemini CLI, OpenCode, Cursor and\n" +
			"others) and .claude/skills (Claude Code). --project writes them in this\n" +
			"folder instead; --dir writes them in one folder of your choosing, for an\n" +
			"agent that reads skills from somewhere else.\n\n" +
			"A copy you changed is not overwritten without --force.\n\n" +
			"    oa setup skills\n" +
			"    oa setup skills --project\n" +
			"    oa setup skills --dir ~/.config/my-agent/skills",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			roots, err := targets(project, dir)
			if err != nil {
				return err
			}
			return install(opts, roots, force)
		},
	}

	cmd.Flags().BoolVar(&project, "project", false, "install in this folder, for this project only")
	cmd.Flags().StringVar(&dir, "dir", "", "install in this skills folder only")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite a copy that differs")
	cmd.MarkFlagsMutuallyExclusive("project", "dir")
	return cmd
}

func targets(project bool, dir string) ([]string, error) {
	if dir != "" {
		return []string{dir}, nil
	}
	base, err := os.UserHomeDir()
	if project {
		base, err = os.Getwd()
	}
	if err != nil {
		return nil, err
	}
	roots := make([]string, len(harnessDirs))
	for i, d := range harnessDirs {
		roots[i] = filepath.Join(base, d)
	}
	return roots, nil
}

// install checks every file it would write before writing any, so a copy
// someone changed stops the whole install rather than half of it.
func install(opts *cli.Options, roots []string, force bool) error {
	files := map[string][]byte{}
	err := fs.WalkDir(skills.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		files[path], err = fs.ReadFile(skills.FS, path)
		return err
	})
	if err != nil {
		return err
	}
	names := slices.Sorted(maps.Keys(files))

	var changed []string
	for _, root := range roots {
		for _, f := range names {
			want := files[f]
			got, err := os.ReadFile(filepath.Join(root, f)) //nolint:gosec // a folder the user chose, or their own skills folder
			if err == nil && !bytes.Equal(got, want) {
				changed = append(changed, filepath.Join(root, f))
			}
		}
	}
	if len(changed) > 0 && !force {
		return fmt.Errorf("%s differs from the copy oa ships, so nothing was written: pass --force to replace it", changed[0])
	}

	for _, root := range roots {
		for _, f := range names {
			to := filepath.Join(root, f)
			if err := os.MkdirAll(filepath.Dir(to), 0o750); err != nil {
				return err
			}
			if err := os.WriteFile(to, files[f], 0o600); err != nil {
				return errors.Join(fmt.Errorf("writing %s", to), err)
			}
		}
		opts.Out.Note("installed in " + root)
	}
	return nil
}
