package objects

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math/rand/v2"
	"runtime"
	"testing"

	"github.com/google/uuid"
)

// fileOf is n bytes no two runs of a chunk share, so a chunk moved or
// dropped shows.
func fileOf(n int) []byte {
	b := make([]byte, n)
	r := rand.New(rand.NewPCG(uint64(n), 7))
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

// streamed is the file back through Open, read to its end.
func streamed(t *testing.T, e *Encrypted, team uuid.UUID, key string) ([]byte, error) {
	t.Helper()
	r, err := e.Open(t.Context(), team, key)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// Every size round trips both ways in and both ways out, a chunk's edges
// among them: Put or PutStream, then Get or Open.
func TestEverySizeRoundTripsInChunks(t *testing.T) {
	t.Parallel()

	for _, n := range []int{0, 1, chunkSize - 1, chunkSize, chunkSize + 1, 3 * chunkSize, 10<<20 + 3} {
		inner := newFakeStore()
		e := newEncrypted(t, inner, newOneKey(0x11))
		team := uuid.New()
		want := fileOf(n)

		put, stream := objectKey(team, "put"), objectKey(team, "stream")
		if err := e.Put(t.Context(), team, put, want); err != nil {
			t.Fatalf("%d: Put: %v", n, err)
		}
		if err := e.PutStream(t.Context(), team, stream, bytes.NewReader(want)); err != nil {
			t.Fatalf("%d: PutStream: %v", n, err)
		}
		chunks := max(1, (n+chunkSize-1)/chunkSize)
		if got := len(inner.raw(t, put)); got != headerSize+n+16*chunks {
			t.Errorf("%d: stored %d bytes, want %d in %d chunks", n, got, headerSize+n+16*chunks, chunks)
		}
		for _, key := range []string{put, stream} {
			if got, err := e.Get(t.Context(), team, key); err != nil || !bytes.Equal(got, want) {
				t.Errorf("%d: Get = %d bytes, %v", n, len(got), err)
			}
			if got, err := streamed(t, e, team, key); err != nil || !bytes.Equal(got, want) {
				t.Errorf("%d: Open = %d bytes, %v", n, len(got), err)
			}
		}
	}
}

// The chunks are bound to their places: reordered, dropped, cut off at a
// chunk's end, added to, or opened with another salt, key or team's key, an
// object fails as ErrCorrupt, whole or streamed.
func TestAChunkOutOfPlaceDoesNotOpen(t *testing.T) {
	t.Parallel()

	sealedChunk := chunkSize + 16
	chunk := func(b []byte, i int) []byte {
		start := headerSize + i*sealedChunk
		return b[start:min(start+sealedChunk, len(b))]
	}
	for name, tc := range map[string]struct {
		change func(b []byte) []byte
		moved  bool // read from another key
		team   bool // read as another team
	}{
		"two chunks swapped": {change: func(b []byte) []byte {
			out := append([]byte{}, b[:headerSize]...)
			out = append(out, chunk(b, 1)...)
			out = append(out, chunk(b, 0)...)
			return append(out, chunk(b, 2)...)
		}},
		"a chunk dropped": {change: func(b []byte) []byte {
			out := append([]byte{}, b[:headerSize]...)
			out = append(out, chunk(b, 0)...)
			return append(out, chunk(b, 2)...)
		}},
		"cut off at a chunk's end": {change: func(b []byte) []byte {
			return b[:headerSize+2*sealedChunk]
		}},
		"a byte added after the last": {change: func(b []byte) []byte { return append(b, 0) }},
		"the salt changed": {change: func(b []byte) []byte {
			b[len(v2Magic)] ^= 1
			return b
		}},
		"a bit flipped in a later chunk": {change: func(b []byte) []byte {
			b[headerSize+sealedChunk+5] ^= 1
			return b
		}},
		"moved to another key":   {change: func(b []byte) []byte { return b }, moved: true},
		"read as another team's": {change: func(b []byte) []byte { return b }, team: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inner := newFakeStore()
			e := newEncrypted(t, inner, perTeamKeys{})
			team := uuid.New()
			key := objectKey(team, "f")
			if err := e.Put(t.Context(), team, key, fileOf(2*chunkSize+100)); err != nil {
				t.Fatalf("Put: %v", err)
			}
			readTeam, readKey := team, key
			if tc.team {
				readTeam = uuid.New()
				readKey = objectKey(readTeam, "f")
			}
			if tc.moved {
				readKey = objectKey(team, "elsewhere")
			}
			if err := inner.Put(t.Context(), readKey, tc.change(inner.raw(t, key))); err != nil {
				t.Fatal(err)
			}

			if got, err := e.Get(t.Context(), readTeam, readKey); !errors.Is(err, ErrCorrupt) || got != nil {
				t.Errorf("Get = %d bytes, %v; want ErrCorrupt and nothing", len(got), err)
			}
			if _, err := streamed(t, e, readTeam, readKey); !errors.Is(err, ErrCorrupt) {
				t.Errorf("Open and read = %v, want ErrCorrupt", err)
			}
		})
	}
}

// A damaged first chunk fails Open itself, before a caller answers anyone; a
// later one fails the read there, after the chunks before it.
func TestOpenFailsBeforeTheFirstByteOrAtTheChunk(t *testing.T) {
	t.Parallel()

	inner := newFakeStore()
	e := newEncrypted(t, inner, newOneKey(0x11))
	team := uuid.New()
	key := objectKey(team, "f")
	want := fileOf(2*chunkSize + 1)
	if err := e.Put(t.Context(), team, key, want); err != nil {
		t.Fatal(err)
	}
	good := inner.raw(t, key)

	first := append([]byte{}, good...)
	first[headerSize+3] ^= 1
	_ = inner.Put(t.Context(), key, first)
	if _, err := e.Open(t.Context(), team, key); !errors.Is(err, ErrCorrupt) {
		t.Errorf("Open of a damaged first chunk = %v, want ErrCorrupt", err)
	}

	later := append([]byte{}, good...)
	later[headerSize+chunkSize+16+3] ^= 1
	_ = inner.Put(t.Context(), key, later)
	r, err := e.Open(t.Context(), team, key)
	if err != nil {
		t.Fatalf("Open of a damaged second chunk: %v", err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if !errors.Is(err, ErrCorrupt) || !bytes.Equal(got, want[:chunkSize]) {
		t.Errorf("read %d bytes, %v; want the first chunk, then ErrCorrupt", len(got), err)
	}
}

// sealV1 is an object as it was written before v2: one GCM message, its
// nonce first and the object key as additional data.
func sealV1(t *testing.T, teamKey []byte, key string, nonce, plaintext []byte) []byte {
	t.Helper()
	gcm, err := gcmFor(teamKey)
	if err != nil {
		t.Fatal(err)
	}
	return gcm.Seal(append([]byte{}, nonce...), nonce, plaintext, []byte(key))
}

// An object written before v2 is read as it was, whole or streamed; so is
// one whose random nonce happens to begin with v2's magic.
func TestAnObjectWrittenBeforeV2StillOpens(t *testing.T) {
	t.Parallel()

	for name, nonce := range map[string][]byte{
		"a v1 object":                       []byte("0123456789ab"),
		"a v1 object that begins as v2":     []byte(v2Magic + "45678901"),
		"an empty v1 object that begins so": []byte(v2Magic + "zzzzzzzz"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			keys := newOneKey(0x11)
			inner := newFakeStore()
			e := newEncrypted(t, inner, keys)
			team := uuid.New()
			key := objectKey(team, "old")
			want := []byte("written before v2")
			if name == "an empty v1 object that begins so" {
				want = []byte{}
			}
			_ = inner.Put(t.Context(), key, sealV1(t, keys.key, key, nonce, want))

			if got, err := e.Get(t.Context(), team, key); err != nil || !bytes.Equal(got, want) {
				t.Errorf("Get = %q, %v", got, err)
			}
			if got, err := streamed(t, e, team, key); err != nil || !bytes.Equal(got, want) {
				t.Errorf("Open = %q, %v", got, err)
			}
		})
	}
}

// The format as the spec writes it, built here by code of its own: Get opens
// it, and an object Put writes opens by this reader. So v2 is what the spec
// says, and stays so: the digest pins the bytes this construction makes.
func TestV2IsTheFormatTheSpecWrites(t *testing.T) {
	t.Parallel()

	teamKey := bytes.Repeat([]byte{0x42}, KeySize)
	team := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	key := objectKey(team, "vector")
	salt := bytes.Repeat([]byte{0x07}, saltSize)
	file := append(bytes.Repeat([]byte("a"), chunkSize), "tail"...)

	aeadFor := func(salt []byte) cipher.AEAD {
		k, err := hkdf.Key(sha256.New, teamKey, salt, "openarity objects v2\x00"+key, 32)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := aes.NewCipher(k)
		g, _ := cipher.NewGCM(b)
		return g
	}
	nonce := func(i, last byte) []byte { return []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, i, last} }

	g := aeadFor(salt)
	vector := append([]byte("OAo2"), salt...)
	vector = g.Seal(vector, nonce(0, 0), file[:chunkSize], nil)
	vector = g.Seal(vector, nonce(1, 1), file[chunkSize:], nil)
	sum := sha256.Sum256(vector)
	if got := hex.EncodeToString(sum[:]); got != "c959276f23af2cbfc8e53be2f2bb98f3787b2d48ade86b1aea11f3f9fbf68a0a" {
		t.Errorf("the vector's digest is %s; the construction changed", got)
	}

	keys := &oneKey{key: teamKey}
	inner := newFakeStore()
	e := newEncrypted(t, inner, keys)
	_ = inner.Put(t.Context(), key, vector)
	if got, err := e.Get(t.Context(), team, key); err != nil || !bytes.Equal(got, file) {
		t.Fatalf("Get of the spec's bytes = %d bytes, %v", len(got), err)
	}

	if err := e.Put(t.Context(), team, key, file); err != nil {
		t.Fatal(err)
	}
	written := inner.raw(t, key)
	if string(written[:4]) != "OAo2" {
		t.Fatalf("written as %q, not v2", written[:4])
	}
	g = aeadFor(written[4:headerSize])
	body := written[headerSize:]
	first, err := g.Open(nil, nonce(0, 0), body[:chunkSize+16], nil)
	if err != nil {
		t.Fatalf("the first chunk does not open as the spec says: %v", err)
	}
	last, err := g.Open(nil, nonce(1, 1), body[chunkSize+16:], nil)
	if err != nil || !bytes.Equal(append(first, last...), file) {
		t.Fatalf("the last chunk does not open as the spec says: %v", err)
	}
}

// A reader that fails mid-way fails the write, and the store keeps nothing.
func TestAFailedReaderStoresNothing(t *testing.T) {
	t.Parallel()

	inner := newFakeStore()
	e := newEncrypted(t, inner, newOneKey(0x11))
	team := uuid.New()
	key := objectKey(team, "f")
	broke := errors.New("the room hung up")
	r := io.MultiReader(bytes.NewReader(fileOf(3*chunkSize)), errReader{broke})
	if err := e.PutStream(t.Context(), team, key, r); !errors.Is(err, broke) {
		t.Fatalf("PutStream = %v, want %v", err, broke)
	}
	if _, err := inner.Get(t.Context(), key); !errors.Is(err, ErrNotFound) {
		t.Errorf("something was stored: %v", err)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// zeros is n bytes of a file that is never held.
type zeros struct{ n int64 }

func (z *zeros) Read(p []byte) (int, error) {
	if z.n == 0 {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), z.n))
	clear(p[:n])
	z.n -= int64(n)
	return n, nil
}

// discarding stores nothing, counting what it is given.
type discarding struct {
	fakeStore
	got int64
}

func (d *discarding) PutStream(_ context.Context, _ string, r io.Reader) error {
	n, err := io.Copy(io.Discard, r)
	d.got = n
	return err
}

// A file of 100 MiB is sealed, and opened, without being held: what is
// allocated stays far below the file. A copy of the file anywhere fails it.
// Not parallel, so nothing else allocates meanwhile.
func TestALargeFileIsNeverHeldWhole(t *testing.T) {
	const size = 100 << 20
	keys := newOneKey(0x11)
	d := &discarding{fakeStore: *newFakeStore()}
	e := newEncrypted(t, d, keys)
	team := uuid.New()
	key := objectKey(team, "big")

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := e.PutStream(t.Context(), team, key, &zeros{n: size}); err != nil {
		t.Fatalf("PutStream: %v", err)
	}
	runtime.ReadMemStats(&after)
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 16<<20 {
		t.Errorf("sealing 100 MiB allocated %d MiB", alloc>>20)
	}
	if want := int64(headerSize + size + 16*(size/chunkSize)); d.got != want {
		t.Errorf("stored %d bytes, want %d", d.got, want)
	}

	sealed, err := newSealing(&zeros{n: size}, keys.key, key)
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, headerSize)
	_, _ = io.ReadFull(sealed, header)
	aead, _ := fileAEAD(keys.key, header[len(v2Magic):], key)
	runtime.ReadMemStats(&before)
	n, err := io.Copy(io.Discard, newOpening(io.NopCloser(sealed), aead, key))
	runtime.ReadMemStats(&after)
	if err != nil || n != size {
		t.Fatalf("opened %d bytes, %v", n, err)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 16<<20 {
		t.Errorf("opening 100 MiB allocated %d MiB", alloc>>20)
	}
}
