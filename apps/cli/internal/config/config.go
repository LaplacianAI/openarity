package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/LaplacianAI/openarity/apps/cli/internal/atomicfile"
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

	data, err := atomicfile.Read(path)
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

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("serialize config: %w", err)
	}
	return atomicfile.Write(path, data)
}

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
