package store

import (
	"errors"
	"os"

	"github.com/LaplacianAI/openarity/apps/cli/internal/credential"
)

func Open(dir string) credential.Store {
	file := NewFileStore(dir)
	if os.Getenv("OPENARITY_NO_KEYCHAIN") != "" {
		return file
	}

	if keychain, err := NewKeyringStore(); err == nil {
		return &fallback{preferred: keychain, file: file}
	}
	return file
}

type fallback struct {
	preferred credential.Store
	file      credential.Store
}

var _ credential.Store = (*fallback)(nil)

func (f *fallback) Location() string {
	return f.preferred.Location()
}

func (f *fallback) Get(context string) (credential.Credential, error) {
	cred, err := f.preferred.Get(context)
	if err != nil {
		return credential.Credential{}, err
	}
	if !cred.IsZero() {
		return cred, nil
	}

	return f.file.Get(context)
}

func (f *fallback) Set(context string, cred credential.Credential) error {
	err := f.preferred.Set(context, cred)
	if err == nil {
		return f.file.Delete(context)
	}
	if !errors.Is(err, ErrTooBig) {
		return err
	}

	if err := f.file.Set(context, cred); err != nil {
		return err
	}
	return f.preferred.Delete(context)
}

func (f *fallback) Delete(context string) error {
	if err := f.preferred.Delete(context); err != nil {
		return err
	}
	return f.file.Delete(context)
}

func (f *fallback) Rename(from, to string) error {
	if err := f.preferred.Rename(from, to); err != nil {
		return err
	}
	return f.file.Rename(from, to)
}
