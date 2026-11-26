package objects

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"io"

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

// Get is the object's file, read and opened whole: v2, or v1 as it was
// written before v2. A large file is Open's to read.
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

	plaintext, err := openWhole(teamKey, key, sealed)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrCorrupt, key, err)
	}
	return plaintext, nil
}

// Open is the object's file as a stream, each chunk authenticated before any
// of it is read. Its first chunk is opened before Open returns, so a wrong
// key or a damaged header fails here, before a caller has answered anyone. A
// chunk damaged later fails the read at that chunk, after the ones before it.
func (e *Encrypted) Open(ctx context.Context, teamID uuid.UUID, key string) (io.ReadCloser, error) {
	if !InTeam(key, teamID) {
		return nil, fmt.Errorf("%w: %s", ErrWrongTeam, key)
	}

	src, err := e.reader.Open(ctx, key)
	if err != nil {
		return nil, err
	}

	teamKey, err := e.keys.TeamKey(ctx, teamID)
	if err != nil {
		_ = src.Close()
		return nil, err
	}

	header := make([]byte, headerSize)
	n, err := io.ReadFull(src, header)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		_ = src.Close()
		return nil, fmt.Errorf("reading %s: %w", key, err)
	}
	if n == headerSize && isV2(header) {
		aead, err := fileAEAD(teamKey, header[len(v2Magic):], key)
		if err != nil {
			_ = src.Close()
			return nil, fmt.Errorf("%w: %s: %w", ErrCorrupt, key, err)
		}
		o := newOpening(src, aead, key)
		if err := o.next(); err == nil {
			return o, nil
		} else if !errors.Is(err, ErrCorrupt) {
			_ = src.Close()
			return nil, fmt.Errorf("reading %s: %w", key, err)
		}
		// Perhaps a v1 object whose nonce begins as v2's magic: read again,
		// whole, as v1 objects are small.
		_ = src.Close()
		return e.openV1(ctx, teamKey, key)
	}

	// v1, which is never over the most a file was then: read whole.
	rest, err := io.ReadAll(src)
	_ = src.Close()
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", key, err)
	}
	plaintext, err := unseal(teamKey, key, append(header[:n], rest...))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrCorrupt, key, err)
	}
	return io.NopCloser(bytes.NewReader(plaintext)), nil
}

func (e *Encrypted) openV1(ctx context.Context, teamKey []byte, key string) (io.ReadCloser, error) {
	sealed, err := e.reader.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	plaintext, err := unseal(teamKey, key, sealed)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrCorrupt, key, err)
	}
	return io.NopCloser(bytes.NewReader(plaintext)), nil
}

// Put writes body as v2, as every object is written now.
func (e *Encrypted) Put(ctx context.Context, teamID uuid.UUID, key string, body []byte) error {
	return e.PutStream(ctx, teamID, key, bytes.NewReader(body))
}

// PutStream seals r as v2 as it is read, a chunk at a time, into the
// backend's PutStream: a file of any size is written without being held. A
// reader that fails leaves no object.
func (e *Encrypted) PutStream(ctx context.Context, teamID uuid.UUID, key string, r io.Reader) error {
	if !InTeam(key, teamID) {
		return fmt.Errorf("%w: %s", ErrWrongTeam, key)
	}

	teamKey, err := e.keys.TeamKey(ctx, teamID)
	if err != nil {
		return err
	}

	sealed, err := newSealing(r, teamKey, key)
	if err != nil {
		return fmt.Errorf("encrypting %s: %w", key, err)
	}
	return e.writer.PutStream(ctx, key, sealed)
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

// openWhole opens a sealed object held whole: v2, or v1, which is tried as
// well when a v2 object does not open, for a v1 nonce that begins as the
// magic.
func openWhole(teamKey []byte, key string, sealed []byte) ([]byte, error) {
	if len(sealed) >= headerSize && isV2(sealed) {
		aead, err := fileAEAD(teamKey, sealed[len(v2Magic):headerSize], key)
		if err != nil {
			return nil, err
		}
		o := newOpening(io.NopCloser(bytes.NewReader(sealed[headerSize:])), aead, key)
		plaintext, err := io.ReadAll(o)
		if err == nil {
			return plaintext, nil
		}
		if p, v1err := unseal(teamKey, key, sealed); v1err == nil {
			return p, nil
		}
		return nil, err
	}
	return unseal(teamKey, key, sealed)
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
