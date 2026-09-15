package stack

import (
	"errors"
	"os"
	"path/filepath"
)

const dirName = "openarity"

func Dir(goos string, env func(string) string) (string, error) {
	switch goos {
	case "windows":
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

type Layout struct {
	Root   string
	Bin    string
	Data   string
	Dex    string
	Logs   string
	State  string
	Secret string
	Env    string
}

func NewLayout(root string) Layout {
	return Layout{
		Root:   root,
		Bin:    filepath.Join(root, "bin"),
		Data:   filepath.Join(root, "data"),
		Dex:    filepath.Join(root, "dex"),
		Logs:   filepath.Join(root, "logs"),
		State:  filepath.Join(root, "stack.yaml"),
		Secret: filepath.Join(root, "postgres.pw"),
		Env:    filepath.Join(root, "secrets.env"),
	}
}

func (l Layout) Create() error {
	for _, dir := range []string{l.Root, l.Bin, l.Data, l.Dex, l.Logs} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (l Layout) Installed() bool {
	_, err := os.Stat(l.State)
	return err == nil
}
