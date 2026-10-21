package runs

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func readAll(t *testing.T, r io.Reader) []frame {
	t.Helper()

	stream := newFrames(r)
	var got []frame
	for {
		f, err := stream.next()
		if errors.Is(err, io.EOF) {
			return got
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		got = append(got, f)
	}
}

// chunks hands out the stream in the pieces given, the way a proxy flushing
// at arbitrary byte counts would.
type chunks struct{ parts []string }

func (c *chunks) Read(p []byte) (int, error) {
	if len(c.parts) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.parts[0])
	c.parts[0] = c.parts[0][n:]
	if c.parts[0] == "" {
		c.parts = c.parts[1:]
	}
	return n, nil
}

const twoFrames = ": following run x\n\n" +
	"id: 1\nevent: started\ndata: {\"agent\":\"triage\"}\n\n" +
	": keep-alive\n\n" +
	"id: 2\nevent: step\ndata: {\"step\":1}\n\n"

func TestFramesAreReadWholeHoweverTheBytesArrive(t *testing.T) {
	t.Parallel()

	for name, r := range map[string]io.Reader{
		"at once":          strings.NewReader(twoFrames),
		"a byte at a time": iotest.OneByteReader(strings.NewReader(twoFrames)),
		"cut mid-field": &chunks{parts: []string{
			": following run x\n\nid: 1\nev", "ent: started\ndata: {\"age", "nt\":\"triage\"}\n", "\n: keep-alive\n\nid: 2\nevent: step\ndata: {\"step\":1}\n\n",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := readAll(t, r)
			want := []frame{
				{id: "1", event: "started", data: `{"agent":"triage"}`},
				{id: "2", event: "step", data: `{"step":1}`},
			}
			if len(got) != len(want) {
				t.Fatalf("frames = %+v, want %+v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("frame %d = %+v, want %+v", i, got[i], want[i])
				}
			}
		})
	}
}

// A proxy or a Windows server may end lines with \r\n, and the spec allows a
// bare \r too. Left in, the \r becomes part of the event name and of the
// JSON — and a \r\n split across two reads must not read as two line ends,
// which would dispatch a frame early with half its fields.
func TestEveryLineEndingIsALineEnding(t *testing.T) {
	t.Parallel()

	crlf := "id: 1\r\nevent: started\r\ndata: {}\r\n\r\nid: 2\revent: step\rdata: {\"step\":1}\r\r"
	for name, r := range map[string]io.Reader{
		"whole":            strings.NewReader(crlf),
		"a byte at a time": iotest.OneByteReader(strings.NewReader(crlf)),
		"split inside \\r\\n": &chunks{parts: []string{
			"id: 1\r", "\nevent: started\r", "\ndata: {}\r", "\n\r", "\nid: 2\revent: step\rdata: {\"step\":1}\r\r",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := readAll(t, r)
			want := []frame{{id: "1", event: "started", data: "{}"}, {id: "2", event: "step", data: `{"step":1}`}}
			if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
				t.Errorf("frames = %q, want %q", got, want)
			}
		})
	}
}

// Data split over several lines is one value joined by \n, not the last line
// alone; a field with no space after the colon is still that field.
func TestMultiLineDataIsJoined(t *testing.T) {
	t.Parallel()

	got := readAll(t, strings.NewReader("event: note\ndata: {\"a\":\ndata:1}\ndata\n\n"))
	if len(got) != 1 || got[0].data != "{\"a\":\n1}\n" || got[0].event != "note" {
		t.Errorf("frames = %q", got)
	}
}

// A frame's id and event belong to it alone. Carrying them into the next
// frame would label an event with a kind it is not.
func TestAFrameStartsEmpty(t *testing.T) {
	t.Parallel()

	got := readAll(t, strings.NewReader("id: 1\nevent: started\ndata: {}\n\ndata: {\"status\":\"x\"}\n\n"))
	if len(got) != 2 {
		t.Fatalf("frames = %q", got)
	}
	if got[1].id != "" || got[1].event != "" {
		t.Errorf("the second frame inherited %q", got[1])
	}
}

// A blank line with no data dispatches nothing, as the spec says — and the
// fields gathered before it are dropped with it, not carried forward.
func TestAFrameWithNoDataIsDropped(t *testing.T) {
	t.Parallel()

	got := readAll(t, strings.NewReader("event: started\nid: 9\n\ndata: {}\n\n"))
	if len(got) != 1 || got[0].event != "" || got[0].id != "" {
		t.Errorf("frames = %q, want one frame with no event or id", got)
	}
}

// A comment is how the brain keeps the connection alive. It must never reach
// the printer as an event, and a comment holding "data:" is still a comment.
func TestCommentsAreIgnored(t *testing.T) {
	t.Parallel()

	got := readAll(t, strings.NewReader(":data: not an event\n: keep-alive\n\n:\n\n"))
	if len(got) != 0 {
		t.Errorf("comments became frames: %q", got)
	}
}

// The connection dropping mid-frame leaves half an event. Dispatching it
// would print an event that never finished arriving.
func TestAFrameCutOffByTheEndIsNotDispatched(t *testing.T) {
	t.Parallel()

	got := readAll(t, strings.NewReader("id: 1\nevent: started\ndata: {}\n\nid: 2\nevent: step\ndata: {\"st"))
	if len(got) != 1 || got[0].id != "1" {
		t.Errorf("frames = %q, want only the complete one", got)
	}
}

func TestALineTooLongIsAnError(t *testing.T) {
	t.Parallel()

	stream := newFrames(strings.NewReader("data: " + strings.Repeat("x", maxLine+1) + "\n\n"))
	if _, err := stream.next(); err == nil || errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want a read error rather than a quiet end", err)
	}
}
