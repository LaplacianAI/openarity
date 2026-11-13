package stream

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// maxFrame is the longest line a Reader accepts: a tool's output can be
// large, and a stream that sends a longer one is not one this package wrote.
const maxFrame = 4 << 20

// Reader reads envelopes from a server-sent event stream, as Follow writes
// it. It keeps the last sequence number it read, to reconnect with.
type Reader struct {
	sc   *bufio.Scanner
	last int64
	seen bool
}

func NewReader(r io.Reader) *Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxFrame)
	return &Reader{sc: sc}
}

// Next is the next event, or io.EOF once the stream ends. Comments, such as a
// keep-alive, and frames with no event field are skipped.
func (r *Reader) Next() (Envelope, error) {
	var id, kind string
	var data []string
	for r.sc.Scan() {
		line := r.sc.Text()
		if line == "" {
			if kind == "" {
				id, data = "", nil
				continue
			}
			return r.dispatch(id, kind, data)
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			id = value
		case "event":
			kind = value
		case "data":
			data = append(data, value)
		}
	}
	if err := r.sc.Err(); err != nil {
		return Envelope{}, fmt.Errorf("stream: reading: %w", err)
	}
	return Envelope{}, io.EOF
}

// Last is the sequence number of the last event read, and whether one was:
// what to send as Last-Event-ID on reconnecting.
func (r *Reader) Last() (int64, bool) { return r.last, r.seen }

func (r *Reader) dispatch(id, kind string, data []string) (Envelope, error) {
	seq, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return Envelope{}, fmt.Errorf("stream: a %s frame whose id %q is not a sequence number", kind, id)
	}
	env, err := Decode(Kind(kind), []byte(strings.Join(data, "\n")))
	if err != nil {
		return Envelope{}, err
	}
	env.Seq = seq
	r.last, r.seen = seq, true
	return env, nil
}
