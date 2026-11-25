// Package roomfile is how a room's process names and opens one of the room's
// files for the brain: the rules every role keeps, and the download of one
// file whole.
package roomfile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	// MaxDownload is the largest file handed over whole: the most the brain
	// keeps as an attachment. Keep it equal to the brain's roomfile.Max.
	MaxDownload = 25 << 20
	// ProblemHeader marks an answer as the room's own refusal, whose text is
	// for the model, whatever its status.
	ProblemHeader = "X-Room-Problem"
)

// A Problem is the caller's to fix: a path that is not there, a file too
// large. It is answered as 422, or 404 and 413 where those say it better,
// with its message, which the model reads.
type Problem struct {
	msg    string
	status int
}

func (p *Problem) Error() string { return p.msg }

// Status is the HTTP status the problem is answered with.
func (p *Problem) Status() int {
	if p.status == 0 {
		return http.StatusUnprocessableEntity
	}
	return p.status
}

// Problemf is a Problem answered as 422.
func Problemf(format string, a ...any) error { return &Problem{msg: fmt.Sprintf(format, a...)} }

// Resolve is the absolute path a caller means: relative to root, or as
// given, with its links followed. The room is the boundary, not root, with
// one exception: /proc. A room's process can always read its own environ,
// where the token it was started with may stay.
func Resolve(root, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", Problemf("path is empty")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	p = filepath.Clean(p)
	if real, err := realPath(p); err == nil {
		p = real
	}
	if InProc(p) {
		return "", Problemf("%s is under /proc, which these tools do not read; use Bash", p)
	}
	return p, nil
}

// realPath is p with every link followed, or, for a file not there yet, its
// directory's.
func realPath(p string) (string, error) {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real, nil
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.Base(p)), nil
}

// InProc is whether a clean path is /proc or under it.
func InProc(p string) bool { return p == "/proc" || strings.HasPrefix(p, "/proc/") }

// Open opens a regular file for reading, or says why it cannot.
func Open(path string) (*os.File, error) {
	info, err := os.Stat(path) //nolint:gosec // G703: any path in the room is the brain's to read; Resolve refused /proc
	if err != nil {
		return nil, AsProblem(err)
	}
	if info.IsDir() {
		return nil, Problemf("%s is a directory; use Glob or Bash ls to list it", path)
	}
	if !info.Mode().IsRegular() {
		// A FIFO or a device would hold the call until it gave something.
		return nil, Problemf("%s is not a regular file (%s); use Bash", path, info.Mode().Type())
	}
	// Opened without blocking, so a FIFO swapped in after the check above
	// cannot hold the call; the open file is asked again below.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // any path in the room is the agent's to read
	if err != nil {
		return nil, AsProblem(err)
	}
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, Problemf("%s is not a regular file; use Bash", path)
	}
	if OnProc(f) {
		_ = f.Close()
		return nil, Problemf("%s is under /proc, which these tools do not read; use Bash", path)
	}
	return f, nil
}

// AsProblem turns a missing file or a refused one into the caller's
// problem, which it is; anything else stays an error of the room's.
func AsProblem(err error) error {
	var pe *fs.PathError
	var le *os.LinkError
	switch {
	case errors.As(err, &le):
		// A rename: the file replaced, or the one put in its place.
		return Problemf("%s could not be replaced: %v", le.New, le.Err)
	case errors.Is(err, fs.ErrNotExist) && errors.As(err, &pe):
		return &Problem{msg: fmt.Sprintf("%s does not exist", pe.Path), status: http.StatusNotFound}
	case errors.Is(err, fs.ErrPermission) && errors.As(err, &pe):
		return Problemf("%s may not be read or written here", pe.Path)
	case errors.As(err, &pe):
		return Problemf("%s: %v", pe.Path, pe.Err)
	}
	return err
}

// Download writes the file at path, relative to root, whole: its bytes with
// their length, for the brain to keep. A file over MaxDownload is refused
// with its size, before a byte of it is sent. A Problem is the caller's;
// any other error is the room's, and nothing has been written for it.
func Download(w http.ResponseWriter, root, path string) error {
	p, err := Resolve(root, path)
	if err != nil {
		return err
	}
	f, err := Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if size := info.Size(); size > MaxDownload {
		return &Problem{
			msg:    fmt.Sprintf("%s is %d bytes; a file is handed over up to %d", p, size, MaxDownload),
			status: http.StatusRequestEntityTooLarge,
		}
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// No more than the length said is sent, should the file grow; one that
	// shrank ends short of it, which the brain's read takes as a failure.
	// The headers are gone by now, so there is nothing left to answer.
	_, _ = io.CopyN(w, f, info.Size())
	return nil
}

// Serve answers GET /file?path= for a room whose files are under root, once
// the caller is known to be the brain: the file whole, or why not. The
// logger hears what failed on the room's side.
func Serve(w http.ResponseWriter, r *http.Request, root string, logger *slog.Logger) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	err := Download(w, root, r.URL.Query().Get("path"))
	if err == nil {
		return
	}
	var p *Problem
	if errors.As(err, &p) {
		// Marked as the room's own answer: a provider's proxy answers 404
		// too, for a room it no longer has, and the brain must tell a
		// missing file from a missing room.
		w.Header().Set(ProblemHeader, "1")
		http.Error(w, p.Error(), p.Status())
		return
	}
	logger.ErrorContext(r.Context(), "room: a file could not be read", "error", err)
	http.Error(w, "the file could not be read: "+err.Error(), http.StatusInternalServerError)
}
