package objects

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

const KeySize = 32

var (
	ErrWrongTeam = errors.New("object does not belong to this team")
	ErrCorrupt   = errors.New("object could not be decrypted")
)

type KeySource interface {
	TeamKey(ctx context.Context, teamID uuid.UUID) ([]byte, error)
}

type Encrypted struct {
	reader Store
	writer Writer
	keys   KeySource
}

func NewEncrypted(inner Store, keys KeySource) (*Encrypted, error) {
	writer, ok := inner.(Writer)
	if !ok {
		return nil, fmt.Errorf(
			"object store %T cannot write, so attachments have nowhere to go", inner)
	}
	return &Encrypted{reader: inner, writer: writer, keys: keys}, nil
}

func (e *Encrypted) Get(ctx context.Context, teamID uuid.UUID, key string) ([]byte, error) {
	if !InTeam(key, teamID) {
		return nil, fmt.Errorf("%w: %s", ErrWrongTeam, key)
	}

	sealed, err := e.reader.Get(ctx, key)
	if err != nil {
		return nil, err
	}

	teamKey, err := e.keys.TeamKey(ctx, teamID)
	if err != nil {
		return nil, err
	}

	plaintext, err := unseal(teamKey, key, sealed)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrCorrupt, key, err)
	}
	return plaintext, nil
}

func (e *Encrypted) Put(ctx context.Context, teamID uuid.UUID, key string, body []byte) error {
	if !InTeam(key, teamID) {
		return fmt.Errorf("%w: %s", ErrWrongTeam, key)
	}

	teamKey, err := e.keys.TeamKey(ctx, teamID)
	if err != nil {
		return err
	}

	sealed, err := seal(teamKey, key, body)
	if err != nil {
		return fmt.Errorf("encrypting %s: %w", key, err)
	}
	return e.writer.Put(ctx, key, sealed)
}

func (e *Encrypted) Delete(ctx context.Context, teamID uuid.UUID, key string) error {
	if !InTeam(key, teamID) {
		return fmt.Errorf("%w: %s", ErrWrongTeam, key)
	}
	return e.writer.Delete(ctx, key)
}

func gcmFor(teamKey []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(teamKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func seal(teamKey []byte, key string, plaintext []byte) ([]byte, error) {
	gcm, err := gcmFor(teamKey)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	rand.Read(nonce) //nolint:errcheck
	return gcm.Seal(nonce, nonce, plaintext, []byte(key)), nil
}

func unseal(teamKey []byte, key string, sealed []byte) ([]byte, error) {
	gcm, err := gcmFor(teamKey)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, fmt.Errorf("stored object is %d bytes, shorter than a nonce", len(sealed))
	}

	nonce, body := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	return gcm.Open(nil, nonce, body, []byte(key))
}
