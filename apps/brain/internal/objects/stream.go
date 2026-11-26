package objects

import (
	"bytes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// v2, the format every object is written in: a header, then the file in
// chunks, each sealed on its own, so a file is written and read without
// being held whole.
//
//	magic "OAo2"(4) ‖ salt(32) ‖ chunk₀ ‖ … ‖ chunkₙ
//	fileKey = HKDF-SHA256(teamKey, salt, "openarity objects v2\x00" ‖ object key)
//	chunkᵢ  = GCM(fileKey, nonce = i as 11 bytes ‖ last?(1), 64 KiB of the file)
//
// Every chunk is 64 KiB but the last, which holds what is left: 1 to 64 KiB,
// or nothing for an empty file.
//
// The STREAM construction, as age and Tink's streaming AEAD use it. A key of
// its own per object makes a counter a safe nonce. The counter stops chunks
// being reordered or dropped, the last-chunk flag stops the file being cut
// off at a chunk's end, and the object key in the derivation stops bytes
// moved to another key from opening. The format is fixed: a change is a new
// magic, read beside this one.
const (
	chunkSize  = 64 << 10
	saltSize   = 32
	headerSize = len(v2Magic) + saltSize
	v2Info     = "openarity objects v2\x00"
	v2Magic    = "OAo2"
)

// ErrTruncated is a source that stopped before its end: a connection that
// closed early, which io.ReadFull would otherwise take as a short last chunk.
var ErrTruncated = errors.New("the file stopped before its end")

// strict is a source whose only end is io.EOF, and whose first error is its
// every error after: a reader that fails once may not then read on, or seal a
// cut-off file as a whole one.
type strict struct {
	r   io.Reader
	err error
}

// Strict is r ended only by io.EOF. io.ReadFull turns a source's own EOF into
// io.ErrUnexpectedEOF for a short read, which is a file's end; a source that
// says io.ErrUnexpectedEOF itself is cut off, and Strict says ErrTruncated.
func Strict(r io.Reader) io.Reader {
	if s, ok := r.(*strict); ok {
		return s
	}
	return &strict{r: r}
}

func (s *strict) Read(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	n, err := s.r.Read(p)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		// Its text only, so the chain no longer holds io.ErrUnexpectedEOF.
		err = fmt.Errorf("%w: %s", ErrTruncated, err.Error())
	}
	s.err = err
	return n, err
}

// fileAEAD is the object's own cipher, derived from the team's key.
func fileAEAD(teamKey, salt []byte, key string) (cipher.AEAD, error) {
	if len(teamKey) != KeySize {
		return nil, fmt.Errorf("the team key is %d bytes; the key size is %d", len(teamKey), KeySize)
	}
	k, err := hkdf.Key(sha256.New, teamKey, salt, v2Info+key, KeySize)
	if err != nil {
		return nil, err
	}
	return gcmFor(k)
}

// chunkNonce is chunk i's nonce: its index, then whether it is the last.
func chunkNonce(i uint64, last bool) []byte {
	n := make([]byte, 12)
	binary.BigEndian.PutUint64(n[3:11], i)
	if last {
		n[11] = 1
	}
	return n
}

// sealing is r as v2, sealed a chunk at a time as it is read.
type sealing struct {
	src    io.Reader
	aead   cipher.AEAD
	in     []byte // a chunk and one byte past it, which says it is not the last
	have   int
	i      uint64
	sealed []byte
	out    []byte // what is sealed and not yet read
	done   bool
}

func newSealing(src io.Reader, teamKey []byte, key string) (*sealing, error) {
	header := make([]byte, headerSize, headerSize+chunkSize+16)
	copy(header, v2Magic)
	rand.Read(header[len(v2Magic):]) //nolint:errcheck // never fails; it panics instead
	aead, err := fileAEAD(teamKey, header[len(v2Magic):], key)
	if err != nil {
		return nil, err
	}
	return &sealing{
		src: Strict(src), aead: aead, in: make([]byte, chunkSize+1),
		sealed: make([]byte, 0, chunkSize+aead.Overhead()), out: header,
	}, nil
}

func (s *sealing) Read(p []byte) (int, error) {
	for len(s.out) == 0 {
		if s.done {
			return 0, io.EOF
		}
		if err := s.next(); err != nil {
			return 0, err
		}
	}
	n := copy(p, s.out)
	s.out = s.out[n:]
	return n, nil
}

// next seals the next chunk. One more byte than a chunk is read, so the last
// chunk is known as the last when it is sealed.
func (s *sealing) next() error {
	n, err := io.ReadFull(s.src, s.in[s.have:])
	s.have += n
	switch {
	case err == nil:
		s.out = s.aead.Seal(s.sealed[:0], chunkNonce(s.i, false), s.in[:chunkSize], nil)
		s.in[0], s.have = s.in[chunkSize], 1
		s.i++
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		s.out = s.aead.Seal(s.sealed[:0], chunkNonce(s.i, true), s.in[:s.have], nil)
		s.done = true
	default:
		return err
	}
	return nil
}

// opening is a v2 object's file, each chunk opened before any of it is read.
// A chunk that does not open is ErrCorrupt, after the chunks before it.
type opening struct {
	src    io.ReadCloser
	key    string
	aead   cipher.AEAD
	in     []byte // a sealed chunk and one byte past it
	have   int
	i      uint64
	opened []byte
	out    []byte
	done   bool
}

func newOpening(src io.ReadCloser, aead cipher.AEAD, key string) *opening {
	sealed := chunkSize + aead.Overhead()
	src = struct {
		io.Reader
		io.Closer
	}{Strict(src), src}
	return &opening{src: src, key: key, aead: aead, in: make([]byte, sealed+1), opened: make([]byte, 0, chunkSize)}
}

func (o *opening) Read(p []byte) (int, error) {
	for len(o.out) == 0 {
		if o.done {
			return 0, io.EOF
		}
		if err := o.next(); err != nil {
			return 0, err
		}
	}
	n := copy(p, o.out)
	o.out = o.out[n:]
	return n, nil
}

func (o *opening) next() error {
	sealed := len(o.in) - 1
	n, err := io.ReadFull(o.src, o.in[o.have:])
	o.have += n
	var last bool
	switch {
	case err == nil:
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		last = true
	default:
		return err
	}
	chunk := o.in[:sealed]
	if last {
		chunk = o.in[:o.have]
	}
	opened, err := o.aead.Open(o.opened[:0], chunkNonce(o.i, last), chunk, nil)
	if err != nil {
		return fmt.Errorf("%w: %s: chunk %d: %w", ErrCorrupt, o.key, o.i, err)
	}
	o.out = opened
	if last {
		o.done = true
		return nil
	}
	o.in[0], o.have = o.in[sealed], 1
	o.i++
	return nil
}

func (o *opening) Close() error { return o.src.Close() }

// isV2 is whether a stored object begins as v2 does. A v1 object begins with
// a random nonce, which these four bytes begin one time in 2³²; the reader
// tries v1 when such an object does not open as v2.
func isV2(head []byte) bool { return bytes.HasPrefix(head, []byte(v2Magic)) }
