// Package stack runs Openarity on one machine: Postgres, dex, the brain and
// its worker, as supervised child processes rather than containers. It is the
// personal install, and it knows nothing about cobra — the command package
// above it does that, so everything here is testable without a terminal.
package stack

import (
	"errors"
	"os"
	"path/filepath"
)

// The single directory everything lives under. A personal install is one
// thing to back up and one thing to delete.
const dirName = "openarity"

// Dir takes the platform and the environment rather than reading either,
// because all three platforms have to be reachable from whichever runner the
// tests happen to be on. A function that called runtime.GOOS directly would
// leave two of the three untested everywhere.
func Dir(goos string, env func(string) string) (string, error) {
	switch goos {
	case "windows":
		// LOCALAPPDATA, not APPDATA — and so not os.UserConfigDir(), which
		// returns the Roaming profile on Windows. A domain-joined machine
		// syncs Roaming between logins, and a Postgres cluster inside it
		// would be copied across the network mid-write.
		base := env("LOCALAPPDATA")
		if base == "" {
			return "", errors.New("stack: LOCALAPPDATA is unset, so there is nowhere to install")
		}
		return filepath.Join(base, dirName), nil

	case "darwin":
		home := env("HOME")
		if home == "" {
			return "", errors.New("stack: HOME is unset, so there is nowhere to install")
		}
		return filepath.Join(home, "Library", "Application Support", dirName), nil

	default:
		// XDG_DATA_HOME rather than XDG_CONFIG_HOME: a Postgres cluster is
		// not configuration, and a config directory is the one people sync
		// between machines.
		if data := env("XDG_DATA_HOME"); data != "" {
			return filepath.Join(data, dirName), nil
		}
		home := env("HOME")
		if home == "" {
			return "", errors.New("stack: neither XDG_DATA_HOME nor HOME is set, so there is nowhere to install")
		}
		return filepath.Join(home, ".local", "share", dirName), nil
	}
}

// Layout is where each part of the install lives, derived from one root so
// that moving the root moves everything.
type Layout struct {
	Root  string
	Bin   string // downloaded binaries: postgres/, dex, brain
	Data  string // the Postgres cluster
	Dex   string // dex's generated config, which holds a password hash
	Logs  string
	State string // stack.yaml
}

// NewLayout derives the paths. It does not touch the filesystem — Create
// does, and keeping them apart means the paths can be asserted in a test that
// writes nothing.
func NewLayout(root string) Layout {
	return Layout{
		Root:  root,
		Bin:   filepath.Join(root, "bin"),
		Data:  filepath.Join(root, "data"),
		Dex:   filepath.Join(root, "dex"),
		Logs:  filepath.Join(root, "logs"),
		State: filepath.Join(root, "stack.yaml"),
	}
}

// Create makes the directories. 0o700 throughout: the dex directory holds a
// bcrypt hash and the data directory is a database. On Windows the mode is
// ignored, and the per-user ACL on LOCALAPPDATA is what protects them.
func (l Layout) Create() error {
	for _, dir := range []string{l.Root, l.Bin, l.Data, l.Dex, l.Logs} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Installed reports whether setup has already run here. It asks about the
// state file rather than the directory, because a half-finished setup leaves
// directories behind and re-running over those is the resumable case.
func (l Layout) Installed() bool {
	_, err := os.Stat(l.State)
	return err == nil
}
