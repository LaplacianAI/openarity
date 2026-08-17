package store

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/zalando/go-keyring"
	"gopkg.in/yaml.v3"

	"github.com/LaplacianAI/openarity/apps/cli/internal/credential"
)

const service = "openarity"

const probeAccount = "openarity.probe"

type KeyringStore struct{}

var (
	_         credential.Store = (*KeyringStore)(nil)
	ErrTooBig                  = errors.New("the credential is too large for the keychain")
)

func NewKeyringStore() (*KeyringStore, error) {
	_, err := keyring.Get(service, probeAccount)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return nil, fmt.Errorf("no usable keychain on this machine: %w", err)
	}
	return &KeyringStore{}, nil
}

func (s *KeyringStore) Location() string {
	switch runtime.GOOS {
	case "darwin":
		return "the macOS keychain"
	case "windows":
		return "Windows Credential Manager"
	default:
		return "the system keyring"
	}
}

func (s *KeyringStore) Get(context string) (credential.Credential, error) {
	if context == "" {
		return credential.Credential{}, nil
	}

	secret, err := keyring.Get(service, context)
	if errors.Is(err, keyring.ErrNotFound) {
		return credential.Credential{}, nil
	}
	if err != nil {
		return credential.Credential{}, fmt.Errorf("read the credential for %s from %s: %w",
			context, s.Location(), err)
	}

	var cred credential.Credential
	if err := yaml.Unmarshal([]byte(secret), &cred); err != nil {
		return credential.Credential{}, fmt.Errorf(
			"parse the credential stored for %s: %w", context, err)
	}
	return cred, nil
}

func (s *KeyringStore) Set(context string, cred credential.Credential) error {
	if context == "" {
		return errors.New("a credential needs a context to belong to")
	}

	// #nosec G117 -- serialising the credential is the point: the destination
	// is the OS keychain, which is the most protected place on the machine.
	// file.go marshals the same type and escapes the check only because the
	// field sits one level deeper, inside a map.
	secret, err := yaml.Marshal(&cred)
	if err != nil {
		return fmt.Errorf("serialize the credential for %s: %w", context, err)
	}

	err = keyring.Set(service, context, string(secret))
	if errors.Is(err, keyring.ErrSetDataTooBig) {
		return fmt.Errorf("%w: %d bytes", ErrTooBig, len(secret))
	}
	if err != nil {
		return fmt.Errorf("store the credential for %s in %s: %w", context, s.Location(), err)
	}
	return nil
}

func (s *KeyringStore) Delete(context string) error {
	err := keyring.Delete(service, context)
	if err == nil || errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return fmt.Errorf("remove the credential for %s from %s: %w", context, s.Location(), err)
}

func (s *KeyringStore) Rename(from, to string) error {
	if to == "" {
		return errors.New("a credential needs a context to belong to")
	}
	if from == to {
		return nil
	}

	moving, err := s.Get(from)
	if err != nil {
		return err
	}
	if moving.IsZero() {
		return nil
	}

	existing, err := s.Get(to)
	if err != nil {
		return err
	}
	if !existing.IsZero() {
		return fmt.Errorf("%s already has a credential", to)
	}

	if err := s.Set(to, moving); err != nil {
		return err
	}
	return s.Delete(from)
}
