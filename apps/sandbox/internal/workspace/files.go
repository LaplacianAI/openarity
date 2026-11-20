package workspace

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultLines is how many lines a read returns when the caller names
	// no limit.
	DefaultLines = 2000
	// maxLine is the longest line a read returns whole; past it, the line is
	// cut, as a minified file's would flood the model.
	maxLine = 2000
	// sniff is how much of a file is looked at to tell text from binary.
	sniff = 8000
)

// A Problem is the caller's to fix: a path that is not there, an edit that
// matches twice. It is answered as 422 with its message, which the model
// reads.
type Problem struct{ msg string }

func (p *Problem) Error() string { return p.msg }

func problem(format string, a ...any) error { return &Problem{msg: fmt.Sprintf(format, a...)} }

// ReadResult is a part of a text file, its lines numbered as cat -n numbers
// them.
type ReadResult struct {
	Content string `json:"content"`
	// Lines is how many lines the file has, so the caller knows whether
	// there is more to read.
	Lines int `json:"lines"`
}

// read returns limit lines of the file from line offset, counting from one.
func read(path string, offset, limit int) (ReadResult, error) {
	f, err := open(path)
	if err != nil {
		return ReadResult{}, err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	head, _ := br.Peek(sniff)
	if binary(head) {
		info, _ := f.Stat()
		return ReadResult{}, problem("%s is a binary file of %d bytes; Read shows text only", path, info.Size())
	}
	if offset < 1 {
		offset = 1
	}
	if limit < 1 {
		limit = DefaultLines
	}
	var out strings.Builder
	n := 0
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			n++
			if n >= offset && n < offset+limit {
				line = strings.TrimRight(line, "\r\n")
				if len(line) > maxLine {
					line = line[:maxLine] + "… (line cut)"
				}
				fmt.Fprintf(&out, "%6d\t%s\n", n, line)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ReadResult{}, err
		}
	}
	return ReadResult{Content: out.String(), Lines: n}, nil
}

// write makes the file hold content, making its directories first.
func write(path, content string) (int, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // the room is the boundary: its files are the agent's
		return 0, asProblem(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // as above
		return 0, asProblem(err)
	}
	return len(content), nil
}

// edit replaces old with new in the file. Unless all is set, old must be
// there exactly once: a model that matched twice meant one of them, and
// guessing which corrupts the file.
func edit(path, old, new string, all bool) (int, error) {
	if old == "" {
		return 0, problem("old_string is empty; use Write to make a file")
	}
	if old == new {
		return 0, problem("old_string and new_string are the same; nothing would change")
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, asProblem(err)
	}
	body, err := os.ReadFile(path) //nolint:gosec // any path in the room is the agent's to read
	if err != nil {
		return 0, asProblem(err)
	}
	n := strings.Count(string(body), old)
	switch {
	case n == 0:
		return 0, problem("old_string is not in %s; Read it and copy the text exactly", path)
	case n > 1 && !all:
		return 0, problem("old_string is in %s %d times; give more of the text around it, or set replace_all", path, n)
	}
	if !all {
		n = 1
	}
	out := strings.Replace(string(body), old, new, n)
	if err := os.WriteFile(path, []byte(out), info.Mode().Perm()); err != nil { //nolint:gosec // as above
		return 0, asProblem(err)
	}
	return n, nil
}

// open opens a regular file for reading, or says why it cannot.
func open(path string) (*os.File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, asProblem(err)
	}
	if info.IsDir() {
		return nil, problem("%s is a directory; use Glob or Bash ls to list it", path)
	}
	f, err := os.Open(path) //nolint:gosec // as above
	if err != nil {
		return nil, asProblem(err)
	}
	return f, nil
}

// binary is whether the start of a file looks like anything but text: a NUL
// byte, as git and grep judge it.
func binary(head []byte) bool { return bytes.IndexByte(head, 0) >= 0 }

// asProblem turns a missing file or a refused one into the caller's
// problem, which it is; anything else stays an error of the room's.
func asProblem(err error) error {
	var pe *fs.PathError
	switch {
	case errors.Is(err, fs.ErrNotExist) && errors.As(err, &pe):
		return problem("%s does not exist", pe.Path)
	case errors.Is(err, fs.ErrPermission) && errors.As(err, &pe):
		return problem("%s may not be read or written here", pe.Path)
	case errors.As(err, &pe):
		return problem("%s: %v", pe.Path, pe.Err)
	}
	return err
}
