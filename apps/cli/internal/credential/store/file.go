package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/LaplacianAI/openarity/apps/cli/internal/credential"
)

const FileName = "credentials.yaml"

type FileStore struct {
	path string
}

var _ credential.Store = (*FileStore)(nil)

type fileContents struct {
	Credentials map[string]credential.Credential `yaml:"credentials,omitempty"`
}

func NewFileStore(dir string) *FileStore {
	return &FileStore{path: filepath.Join(dir, FileName)}
}

func (s *FileStore) read() (fileContents, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileContents{Credentials: map[string]credential.Credential{}}, nil
	}
	if err != nil {
		return fileContents{}, fmt.Errorf("read %s: %w", s.path, err)
	}

	var found fileContents
	if err := yaml.Unmarshal(data, &found); err != nil {
		return fileContents{}, fmt.Errorf("parse %s: %w", s.path, err)
	}

	if found.Credentials == nil {
		found.Credentials = map[string]credential.Credential{}
	}
	return found, nil
}

func (s *FileStore) write(contents fileContents) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	data, err := yaml.Marshal(&contents)
	if err != nil {
		return fmt.Errorf("serialize credentials: %w", err)
	}

	temp, err := os.CreateTemp(dir, ".credentials-*.yaml")
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
	if err := os.Rename(temp.Name(), s.path); err != nil {
		return fmt.Errorf("replace %s: %w", s.path, err)
	}

	return nil
}

func (s *FileStore) Location() string {
	return s.path
}

func (s *FileStore) Get(context string) (credential.Credential, error) {
	if context == "" {
		return credential.Credential{}, nil
	}

	found, err := s.read()
	if err != nil {
		return credential.Credential{}, err
	}
	return found.Credentials[context], nil
}

func (s *FileStore) Set(context string, cred credential.Credential) error {
	if context == "" {
		return errors.New("a credential needs a context to belong to")
	}

	found, err := s.read()
	if err != nil {
		return err
	}

	found.Credentials[context] = cred
	return s.write(found)
}

func (s *FileStore) Delete(context string) error {
	found, err := s.read()
	if err != nil {
		return err
	}
	if _, ok := found.Credentials[context]; !ok {
		return nil
	}

	delete(found.Credentials, context)
	return s.write(found)
}

func (s *FileStore) Rename(from, to string) error {
	if to == "" {
		return errors.New("a credential needs a context to belong to")
	}

	if from == to {
		return nil
	}

	found, err := s.read()
	if err != nil {
		return err
	}

	moving, ok := found.Credentials[from]
	if !ok {
		return nil
	}

	if _, taken := found.Credentials[to]; taken {
		return fmt.Errorf("%s already has a credential", to)
	}

	found.Credentials[to] = moving
	delete(found.Credentials, from)
	return s.write(found)
}
