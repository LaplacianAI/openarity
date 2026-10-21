package runs

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

// A tool's output is clipped at 64 KiB by the brain, and JSON can spell one
// byte in six, so a line well past that is still an honest event.
const maxLine = 4 << 20

type frame struct {
	id    string
	event string
	data  string
}

// frames reads a text/event-stream. Lines end in \n, \r\n or a lone \r, data
// lines join with \n, and a blank line dispatches. A comment is a line whose
// field name is empty — it starts with : — so the switch ignores it like any
// field it does not know. A frame with no data is dropped, as the spec says.
type frames struct {
	scan *bufio.Scanner
}

func newFrames(r io.Reader) *frames {
	scan := bufio.NewScanner(r)
	scan.Buffer(make([]byte, 0, 64<<10), maxLine)
	scan.Split(splitLines)
	return &frames{scan: scan}
}

// next returns the next frame, or io.EOF once the stream ends between frames.
// A frame cut off by the end of the stream is not dispatched.
func (f *frames) next() (frame, error) {
	var (
		cur  frame
		data []string
	)

	for f.scan.Scan() {
		line := f.scan.Text()
		if line == "" {
			if data == nil {
				cur = frame{}
				continue
			}
			cur.data = strings.Join(data, "\n")
			return cur, nil
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			cur.id = value
		case "event":
			cur.event = value
		case "data":
			data = append(data, value)
		}
	}

	if err := f.scan.Err(); err != nil {
		return frame{}, err
	}
	return frame{}, io.EOF
}

func splitLines(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}

	i := bytes.IndexAny(data, "\r\n")
	if i < 0 {
		if atEOF {
			return len(data), data, nil
		}
		return 0, nil, nil
	}
	if data[i] == '\n' {
		return i + 1, data[:i], nil
	}

	// A \r may be the first half of a \r\n split across two reads.
	if i+1 == len(data) && !atEOF {
		return 0, nil, nil
	}
	if i+1 < len(data) && data[i+1] == '\n' {
		return i + 2, data[:i], nil
	}
	return i + 1, data[:i], nil
}
