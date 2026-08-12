package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const DefaultServer = "http://127.0.0.1:21120"

type Config struct {
	Server string `yaml:"server,omitempty"`
	Token  string `yaml:"token,omitempty"`
}

func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate the user config directory: %w", err)
	}
	return filepath.Join(dir, "openarity", "config.yaml"), nil
}

func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}

	// #nosec G304 -- the path is built by Path() from the user's own config
	// directory, not from input. Nothing reaches it from a flag or a server.
	data, err := os.ReadFile(path)
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

	if err := os.Rename(temp.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}

	return nil
}
