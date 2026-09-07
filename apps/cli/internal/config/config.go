package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const DefaultServer = "http://127.0.0.1:21120"

type Context struct {
	Server string `yaml:"server,omitempty"`
	Token  string `yaml:"token,omitempty"`
}

type Config struct {
	Current  string             `yaml:"current,omitempty"`
	Contexts map[string]Context `yaml:"contexts,omitempty"`
	Theme    string             `yaml:"theme,omitempty"`
	Output   string             `yaml:"output,omitempty"`
}

func (c Config) active() (string, Context) {
	if named, ok := c.Contexts[c.Current]; ok {
		return c.Current, named
	}

	if c.Current == "" && len(c.Contexts) == 1 {
		for name, only := range c.Contexts {
			return name, only
		}
	}

	return "", Context{}
}

func (c Config) Active() Context {
	_, active := c.active()
	return active
}

func (c Config) ActiveName() string {
	name, _ := c.active()
	return name
}

func (c Config) ContextNames() []string {
	names := make([]string, 0, len(c.Contexts))
	for name := range c.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", fmt.Errorf("locate the user config directory: %w", err)
	}
	return filepath.Join(dir, "config.yaml"), nil
}

func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}

	data, err := readFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config file %s: %w", path, err)
	}

	var config Config
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return Config{}, fmt.Errorf("parse config file %s: %w", path, err)
	}

	return config, nil
}

func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	err = os.MkdirAll(dir, 0o700)
	if err != nil {
		return fmt.Errorf("create config directory %s: %w", dir, err)
	}

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("serialize config: %w", err)
	}

	temp, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("create a temporary file in %s: %w", dir, err)
	}
	defer func() { _ = os.Remove(temp.Name()) }()

	if _, err := temp.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", temp.Name(), err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", temp.Name(), err)
	}

	if err := replace(temp.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}

	return nil
}

// replace renames over the destination, waiting briefly for a reader to
// finish.
//
// Windows refuses to rename over a file another handle has open, and Go opens
// files for reading without FILE_SHARE_DELETE — so `oa config set` in one
// terminal fails with "Access is denied" because `oa whoami` in another is
// reading config.yaml. Every command loads this file, so the window is real
// and it is not the caller's fault. The handle is held only for the length of
// a read, which is why waiting works.
//
// On Unix rename replaces an open file happily and the loop runs once.
func replace(from, to string) error {
	var err error
	for wait := time.Millisecond; ; wait *= 2 {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		if wait > longestWait {
			return err
		}
		time.Sleep(wait)
	}
}

// Nine attempts over a quarter of a second. Long enough for a read to finish,
// short enough that a rename which will never succeed still reports promptly.
const longestWait = 128 * time.Millisecond

func Dir() (string, error) {
	if custom := strings.TrimSpace(os.Getenv("OPENARITY_CONFIG_DIR")); custom != "" {
		return custom, nil
	}

	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate the user config directory: %w", err)
	}
	return filepath.Join(base, "openarity"), nil
}
