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

	"github.com/LaplacianAI/openarity/apps/sandbox/internal/roomfile"
)

const (
	// DefaultLines is how many lines a read returns when the caller names
	// no limit, and the most it returns.
	DefaultLines = 2000
	// maxLine is the longest line a read returns whole; past it, the line is
	// cut, as a minified file's would flood the model.
	maxLine = 2000
	// sniff is how much of a file is looked at to tell text from binary.
	sniff = 8000
	// maxRead is the most a read returns of a file's text: as much as a
	// command's output, and inside what the brain reads of an answer
	// however its bytes are escaped as JSON (six to one at worst).
	maxRead = 256 << 10
	// maxEdit is the largest file Edit takes: it holds the file twice in
	// memory, in a room of a fixed size.
	maxEdit = 8 << 20
)

// A Problem is the caller's to fix: a path that is not there, an edit that
// matches twice. It is answered as 422 with its message, which the model
// reads.
type Problem = roomfile.Problem

func problem(format string, a ...any) error { return roomfile.Problemf(format, a...) }

// ReadResult is a part of a text file, its lines numbered as cat -n numbers
// them.
type ReadResult struct {
	Content string `json:"content"`
	// Lines is how many lines the file has, so the caller knows whether
	// there is more to read.
	Lines int `json:"lines"`
}

// read returns limit lines of the file from line offset, counting from one,
// and counts the file's lines to its end. It holds at most a line's first
// maxLine bytes and maxRead bytes of output, however the file is shaped: one
// that has no newline is one line, cut.
func read(path string, offset, limit int) (ReadResult, error) {
	f, err := open(path)
	if err != nil {
		return ReadResult{}, err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, sniff)
	head, _ := br.Peek(sniff)
	if binary(head) {
		if info, err := f.Stat(); err == nil {
			return ReadResult{}, problem("%s is a binary file of %d bytes; Read shows text only", path, info.Size())
		}
		return ReadResult{}, problem("%s is a binary file; Read shows text only", path)
	}
	if offset < 1 {
		offset = 1
	}
	if limit < 1 || limit > DefaultLines {
		// More would pass what the brain reads of an answer.
		limit = DefaultLines
	}
	var out strings.Builder
	n, full := 0, false
	for {
		line, more, err := readLine(br)
		if more {
			n++
			if !full && n >= offset && n < offset+limit {
				if out.Len()+len(line) > maxRead {
					// The rest is counted, not shown: the note says where to
					// read on from.
					full = true
				} else {
					fmt.Fprintf(&out, "%6d\t%s\n", n, line)
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return ReadResult{}, err
		}
	}
	return ReadResult{Content: out.String(), Lines: n}, nil
}

// readLine is the next line without its line ending, cut to maxLine bytes;
// the rest of a longer line is read past and dropped, so a line of any
// length costs maxLine. more is whether there was a line at all.
func readLine(br *bufio.Reader) (line string, more bool, err error) {
	var b []byte
	length := 0 // the whole line's, with its newline
	var part []byte
	for {
		part, err = br.ReadSlice('\n')
		length += len(part)
		if room := maxLine + 2 - len(b); room > 0 {
			b = append(b, part[:min(len(part), room)]...)
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			break
		}
	}
	if length == 0 {
		return "", false, err
	}
	if len(part) > 0 && part[len(part)-1] == '\n' {
		length--
	}
	line = strings.TrimRight(string(b), "\r\n")
	if length > maxLine && len(line) > maxLine {
		line = line[:maxLine] + "… (line cut)"
	}
	return line, true, err
}

// write makes the file hold content, making its directories first. The
// file is replaced by rename, as an edit's is, so a write that fails leaves
// it whole and a write never goes through a link into /proc.
func write(path, content string) (int, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // the room is the boundary: its files are the agent's
		return 0, asProblem(err)
	}
	perm := fs.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return 0, problem("%s is a directory; Write makes a file", path)
		}
		perm = info.Mode().Perm()
	}
	if err := replaceFile(path, []byte(content), perm); err != nil {
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
	f, err := open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, asProblem(err)
	}
	if info.Size() > maxEdit {
		return 0, problem("%s is %d bytes, over the %d MiB Edit takes; change it with Bash (sed, a script)", path, info.Size(), maxEdit>>20)
	}
	body, err := io.ReadAll(io.LimitReader(f, maxEdit+1))
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
	if err := replaceFile(path, []byte(out), info.Mode().Perm()); err != nil {
		return 0, asProblem(err)
	}
	return n, nil
}

// replaceFile writes the file's new content beside it and renames it over
// the old, so a write that fails (the workspace full) leaves the file as it
// was rather than cut short.
func replaceFile(path string, data []byte, perm fs.FileMode) error {
	// A short name of its own, so a file whose name is near the longest
	// allowed can still be replaced.
	f, err := os.CreateTemp(filepath.Dir(path), ".oa-edit-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, perm)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// open opens a regular file for reading, or says why it cannot.
func open(path string) (*os.File, error) { return roomfile.Open(path) }

// binary is whether the start of a file looks like anything but text: a NUL
// byte, as git and grep judge it.
func binary(head []byte) bool { return bytes.IndexByte(head, 0) >= 0 }

// asProblem turns a missing file or a refused one into the caller's
// problem, which it is; anything else stays an error of the room's.
func asProblem(err error) error { return roomfile.AsProblem(err) }
