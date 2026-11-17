package guard

import (
	"errors"
	"io"
	"strings"
)

var (
	errNotTLS = errors.New("the connection does not start with a TLS ClientHello")
	errNoSNI  = errors.New("the ClientHello names no server")
)

// maxRecord is the largest TLS record there is.
const maxRecord = 1 << 14

// peekSNI reads the first TLS record from r, which must hold a ClientHello,
// and returns the server name in it with every byte it read, so the caller
// can replay them to the server. Nothing is decrypted: the name is sent in
// the clear.
func peekSNI(r io.Reader) (name string, read []byte, err error) {
	head := make([]byte, 5)
	if _, err := io.ReadFull(r, head); err != nil {
		return "", head[:0], err
	}
	read = head
	// 0x16 handshake; the record version is 0x03 0x0x.
	if head[0] != 0x16 || head[1] != 0x03 {
		return "", read, errNotTLS
	}
	n := int(head[3])<<8 | int(head[4])
	if n == 0 || n > maxRecord {
		return "", read, errNotTLS
	}
	body := make([]byte, n)
	got, err := io.ReadFull(r, body)
	read = append(read, body[:got]...)
	if err != nil {
		return "", read, err
	}
	name, err = parseClientHello(body)
	return name, read, err
}

// parseClientHello walks a handshake message to its server_name extension.
func parseClientHello(b []byte) (string, error) {
	s := reader{b: b}
	// handshake type 1 (client hello), a 3-byte length
	typ, ok := s.u8()
	if !ok || typ != 1 {
		return "", errNotTLS
	}
	if !s.skip(3) || !s.skip(2+32) { // length, version, random
		return "", errNotTLS
	}
	if !s.skipVec8() || !s.skipVec16() || !s.skipVec8() { // session id, ciphers, compression
		return "", errNotTLS
	}
	exts, ok := s.vec16()
	if !ok {
		return "", errNoSNI // an old hello with no extensions at all
	}
	e := reader{b: exts}
	for len(e.b) > 0 {
		id, ok1 := e.u16()
		data, ok2 := e.vec16()
		if !ok1 || !ok2 {
			return "", errNotTLS
		}
		if id != 0 {
			continue
		}
		return serverName(data)
	}
	return "", errNoSNI
}

func serverName(data []byte) (string, error) {
	r := reader{b: data}
	list, ok := r.vec16()
	if !ok {
		return "", errNotTLS
	}
	l := reader{b: list}
	for len(l.b) > 0 {
		typ, ok1 := l.u8()
		name, ok2 := l.vec16()
		if !ok1 || !ok2 {
			return "", errNotTLS
		}
		if typ == 0 {
			n := strings.ToLower(strings.TrimSuffix(string(name), "."))
			if n == "" {
				return "", errNoSNI
			}
			return n, nil
		}
	}
	return "", errNoSNI
}

type reader struct{ b []byte }

func (r *reader) u8() (int, bool) {
	if len(r.b) < 1 {
		return 0, false
	}
	v := int(r.b[0])
	r.b = r.b[1:]
	return v, true
}

func (r *reader) u16() (int, bool) {
	if len(r.b) < 2 {
		return 0, false
	}
	v := int(r.b[0])<<8 | int(r.b[1])
	r.b = r.b[2:]
	return v, true
}

func (r *reader) skip(n int) bool {
	if len(r.b) < n {
		return false
	}
	r.b = r.b[n:]
	return true
}

func (r *reader) skipVec8() bool {
	n, ok := r.u8()
	return ok && r.skip(n)
}

func (r *reader) skipVec16() bool {
	n, ok := r.u16()
	return ok && r.skip(n)
}

func (r *reader) vec16() ([]byte, bool) {
	n, ok := r.u16()
	if !ok || len(r.b) < n {
		return nil, false
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v, true
}
