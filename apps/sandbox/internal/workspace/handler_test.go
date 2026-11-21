package workspace

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const token = "the-workspace-token"

type room struct {
	root string
	url  string
}

func newRoom(t *testing.T) room {
	t.Helper()
	root := t.TempDir()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "VISIBLE=yes"}
	srv := httptest.NewServer(NewHandler(root, token, env, slog.New(slog.DiscardHandler)))
	t.Cleanup(srv.Close)
	return room{root: root, url: srv.URL}
}

// call posts body to route and decodes the answer into out, returning the
// status.
func (r room) call(t *testing.T, route string, body, out any) int {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, r.url+route, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s answered %d %q: %v", route, resp.StatusCode, raw, err)
		}
	}
	return resp.StatusCode
}

func (r room) health(t *testing.T) (*http.Response, error) {
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, r.url+"/healthz", nil)
	return http.DefaultClient.Do(req)
}

type failure struct {
	Error string `json:"error"`
}

func (r room) put(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(r.root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEveryRouteWantsTheToken(t *testing.T) {
	r := newRoom(t)
	for _, route := range []string{"/exec", "/read", "/write", "/edit", "/glob", "/grep"} {
		for _, auth := range []string{"", "Bearer wrong", token} {
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, r.url+route, strings.NewReader(`{"command":"touch pwned"}`))
			if auth != "" {
				req.Header.Set("Authorization", auth)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s with %q: %d, want 401", route, auth, resp.StatusCode)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(r.root, "pwned")); err == nil {
		t.Fatal("a request without the token ran a command")
	}
}

func TestHealthNeedsNoTokenAndTheDirectory(t *testing.T) {
	r := newRoom(t)
	resp, err := r.health(t)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz %d", resp.StatusCode)
	}
	if err := os.RemoveAll(r.root); err != nil {
		t.Fatal(err)
	}
	resp, err = r.health(t)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("healthz with no workspace %d, want 503", resp.StatusCode)
	}
}

func TestACommandRunsInTheWorkspaceWithItsEnvironment(t *testing.T) {
	r := newRoom(t)
	var out ExecResult
	if code := r.call(t, "/exec", map[string]any{"command": "pwd; echo $VISIBLE; echo oops >&2; exit 3"}, &out); code != 200 {
		t.Fatalf("exec %d", code)
	}
	want, _ := filepath.EvalSymlinks(r.root)
	got := strings.Fields(out.Stdout)
	if len(got) != 2 || (got[0] != r.root && got[0] != want) || got[1] != "yes" {
		t.Fatalf("stdout %q, want the workspace and the environment", out.Stdout)
	}
	if out.Stderr != "oops\n" || out.ExitCode != 3 || out.TimedOut {
		t.Fatalf("%+v", out)
	}
}

func TestATimedOutCommandTakesWhatItStartedWithIt(t *testing.T) {
	r := newRoom(t)
	marker := filepath.Join(r.root, "survived")
	start := time.Now()
	var out ExecResult
	r.call(t, "/exec", map[string]any{
		"command":         "(sleep 3; touch " + marker + ") & sleep 30",
		"timeout_seconds": 1,
	}, &out)
	if !out.TimedOut {
		t.Fatalf("%+v, want timed out", out)
	}
	if took := time.Since(start); took > 1*time.Second+pipeGrace+time.Second {
		t.Fatalf("took %s", took)
	}
	time.Sleep(3 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a process the command started outlived its timeout")
	}
}

func TestAFinishedCommandTakesItsBackgroundWithIt(t *testing.T) {
	r := newRoom(t)
	marker := filepath.Join(r.root, "survived")
	var out ExecResult
	r.call(t, "/exec", map[string]any{"command": "(sleep 2; touch " + marker + ") >/dev/null 2>&1 & echo started"}, &out)
	if out.ExitCode != 0 || out.Stdout != "started\n" {
		t.Fatalf("%+v", out)
	}
	time.Sleep(3 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a background process outlived its call")
	}
}

func TestLongOutputKeepsBothEnds(t *testing.T) {
	r := newRoom(t)
	var out ExecResult
	r.call(t, "/exec", map[string]any{"command": "echo FIRST; head -c 2000000 /dev/zero | tr '\\0' x; echo; echo LAST"}, &out)
	if !out.Truncated || !strings.HasPrefix(out.Stdout, "FIRST\n") || !strings.HasSuffix(out.Stdout, "LAST\n") {
		t.Fatalf("truncated %v, start %q, end %q", out.Truncated, out.Stdout[:10], out.Stdout[len(out.Stdout)-10:])
	}
	if len(out.Stdout) > maxOutput+100 {
		t.Fatalf("kept %d bytes", len(out.Stdout))
	}
}

func TestAnEmptyCommandIsTheCallersToFix(t *testing.T) {
	r := newRoom(t)
	var f failure
	if code := r.call(t, "/exec", map[string]any{"command": "  "}, &f); code != 422 || f.Error == "" {
		t.Fatalf("%d %q", code, f.Error)
	}
	if code := r.call(t, "/exec", map[string]any{"comand": "ls"}, &f); code != 422 || !strings.Contains(f.Error, "comand") {
		t.Fatalf("a misspelt field: %d %q", code, f.Error)
	}
}

func TestReadNumbersItsLinesFromTheOffset(t *testing.T) {
	r := newRoom(t)
	r.put(t, "a.txt", "one\ntwo\nthree\nfour\n")
	var out ReadResult
	if code := r.call(t, "/read", map[string]any{"path": "a.txt", "offset": 2, "limit": 2}, &out); code != 200 {
		t.Fatal(code)
	}
	if out.Content != "     2\ttwo\n     3\tthree\n" || out.Lines != 4 {
		t.Fatalf("%q %d", out.Content, out.Lines)
	}
}

func TestReadRefusesWhatIsNotText(t *testing.T) {
	r := newRoom(t)
	r.put(t, "x.bin", "PK\x03\x04\x00\x00")
	if err := os.Mkdir(filepath.Join(r.root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"x.bin": "binary", "dir": "directory", "nope.txt": "does not exist"} {
		var f failure
		if code := r.call(t, "/read", map[string]any{"path": path}, &f); code != 422 || !strings.Contains(f.Error, want) {
			t.Errorf("%s: %d %q, want %q", path, code, f.Error, want)
		}
	}
}

func TestWriteMakesItsDirectories(t *testing.T) {
	r := newRoom(t)
	if code := r.call(t, "/write", map[string]any{"path": "src/pkg/main.go", "content": "package main\n"}, nil); code != 200 {
		t.Fatal(code)
	}
	b, err := os.ReadFile(filepath.Join(r.root, "src/pkg/main.go"))
	if err != nil || string(b) != "package main\n" {
		t.Fatalf("%q %v", b, err)
	}
}

func TestAnEditThatIsNotOneMatchChangesNothing(t *testing.T) {
	r := newRoom(t)
	p := r.put(t, "f.txt", "a = 1\nb = 1\n")
	for _, c := range []struct {
		old, new, want string
	}{
		{"= 1", "= 2", "2 times"},
		{"c = 1", "c = 2", "is not in"},
		{"", "x", "empty"},
		{"a = 1", "a = 1", "the same"},
	} {
		var f failure
		if code := r.call(t, "/edit", map[string]any{"path": "f.txt", "old_string": c.old, "new_string": c.new}, &f); code != 422 || !strings.Contains(f.Error, c.want) {
			t.Errorf("%q: %d %q, want %q", c.old, code, f.Error, c.want)
		}
	}
	if b, _ := os.ReadFile(p); string(b) != "a = 1\nb = 1\n" {
		t.Fatalf("a refused edit changed the file: %q", b)
	}
}

func TestAnEditReplacesOnceOrEverywhere(t *testing.T) {
	r := newRoom(t)
	p := r.put(t, "f.txt", "a = 1\nb = 1\n")
	r.call(t, "/edit", map[string]any{"path": "f.txt", "old_string": "a = 1", "new_string": "a = 2"}, nil)
	var out struct{ Replaced int }
	r.call(t, "/edit", map[string]any{"path": p, "old_string": "1", "new_string": "3", "replace_all": true}, &out)
	if b, _ := os.ReadFile(p); string(b) != "a = 2\nb = 3\n" || out.Replaced != 1 {
		t.Fatalf("%q, replaced %d", b, out.Replaced)
	}
}

func TestGlobMatchesAcrossDirectoriesNewestFirst(t *testing.T) {
	r := newRoom(t)
	old := r.put(t, "a/old.go", "")
	r.put(t, "a/b/c/new.go", "")
	r.put(t, "top.go", "")
	r.put(t, "a/readme.md", "")
	r.put(t, ".git/hooks/x.go", "")
	r.put(t, "node_modules/m/y.go", "")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	var out GlobResult
	if code := r.call(t, "/glob", map[string]any{"pattern": "**/*.go"}, &out); code != 200 {
		t.Fatal(code)
	}
	if len(out.Paths) != 3 || out.Paths[2] != old {
		t.Fatalf("%v: want three .go files outside .git and node_modules, the old one last", out.Paths)
	}
	r.call(t, "/glob", map[string]any{"pattern": "*.go", "path": "a"}, &out)
	if len(out.Paths) != 1 || out.Paths[0] != old {
		t.Fatalf("one level under a: %v", out.Paths)
	}
}

func TestGrepFindsFilesLinesAndCounts(t *testing.T) {
	r := newRoom(t)
	r.put(t, "a.go", "package a\nfunc Hello() {}\nfunc other() {}\n")
	r.put(t, "b.txt", "hello there\nfunc in a text file\n")
	r.put(t, "c.bin", "Hello\x00binary")

	var out GrepResult
	r.call(t, "/grep", map[string]any{"pattern": "hello", "ignore_case": true}, &out)
	if out.Files != 2 || strings.Contains(out.Output, "c.bin") {
		t.Fatalf("files: %+v, want a.go and b.txt and never a binary", out)
	}
	r.call(t, "/grep", map[string]any{"pattern": "^func", "output": "count", "glob": "*.go"}, &out)
	if out.Output != filepath.Join(r.root, "a.go")+":2\n" {
		t.Fatalf("count %q", out.Output)
	}
	r.call(t, "/grep", map[string]any{"pattern": "Hello", "output": "content", "context": 1}, &out)
	a := filepath.Join(r.root, "a.go")
	want := a + "-1-package a\n" + a + ":2:func Hello() {}\n" + a + "-3-func other() {}\n"
	if out.Output != want {
		t.Fatalf("content\n%s\nwant\n%s", out.Output, want)
	}
	var f failure
	if code := r.call(t, "/grep", map[string]any{"pattern": "("}, &f); code != 422 {
		t.Fatalf("a broken pattern: %d", code)
	}
}

func TestSpansMergeAndMarkGaps(t *testing.T) {
	got := spans([]int{1, 2, 9}, 1, 12)
	var lines []int
	gaps := 0
	for _, s := range got {
		if s.gap {
			gaps++
			continue
		}
		lines = append(lines, s.line)
	}
	if want := []int{0, 1, 2, 3, 8, 9, 10}; !slices.Equal(lines, want) || gaps != 1 {
		t.Fatalf("%v, %d gaps", lines, gaps)
	}
}

func TestMatchPath(t *testing.T) {
	for _, c := range []struct {
		pattern, rel string
		want         bool
	}{
		{"**/*.go", "main.go", true},
		{"**/*.go", "a/b/main.go", true},
		{"src/**", "src/a/b", true},
		{"src/**/x.go", "src/x.go", true},
		{"*.go", "a/main.go", false},
		{"a/*/c", "a/b/c", true},
		{"a/*/c", "a/b/b/c", false},
	} {
		if got, _ := matchPath(c.pattern, c.rel); got != c.want {
			t.Errorf("%s %s: %v", c.pattern, c.rel, got)
		}
	}
}

// The file tools run in the workspace process, which can always read its own
// environ, where the token it was started with stays. So none of them reads
// /proc, by its name or through a link.
func TestTheFileToolsKeepOutOfProc(t *testing.T) {
	r := newRoom(t)
	link := filepath.Join(r.root, "env")
	if err := os.Symlink("/proc/self/environ", link); err != nil {
		t.Fatal(err)
	}
	calls := []struct {
		route string
		body  map[string]any
	}{
		{"/read", map[string]any{"path": "/proc/self/environ"}},
		{"/read", map[string]any{"path": "/proc/1/../self/environ"}},
		{"/write", map[string]any{"path": "/proc/self/attr", "content": "x"}},
		{"/edit", map[string]any{"path": "/proc/self/environ", "old_string": "a", "new_string": "b"}},
		{"/glob", map[string]any{"pattern": "*", "path": "/proc"}},
		{"/grep", map[string]any{"pattern": "TOKEN", "path": "/proc/self"}},
	}
	if _, err := os.Stat("/proc/self/environ"); err == nil {
		calls = append(calls, struct {
			route string
			body  map[string]any
		}{"/read", map[string]any{"path": "env"}})
	}
	for _, c := range calls {
		var f failure
		if code := r.call(t, c.route, c.body, &f); code != 422 || !strings.Contains(f.Error, "/proc") {
			t.Errorf("%s %v: %d %q, want it refused for /proc", c.route, c.body["path"], code, f.Error)
		}
	}
}

// A handler made with no token lets nobody in, though an empty bearer token
// compares equal to it. Go's server trims the header to "Bearer", so the
// request is built here, untrimmed, as another server might pass it on.
func TestAWorkspaceWithNoTokenLetsNobodyIn(t *testing.T) {
	h := NewHandler(t.TempDir(), "", nil, slog.New(slog.DiscardHandler))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/exec", nil)
	req.Header.Set("Authorization", "Bearer ")
	if h.authorized(req) {
		t.Error("an empty token was let in by a workspace with none")
	}
}

// An edit keeps the file's mode, and leaves nothing beside it.
func TestAnEditKeepsTheModeAndLeavesNoTemporaryFile(t *testing.T) {
	r := newRoom(t)
	r.put(t, "run.sh", "echo a\n")
	if err := os.Chmod(filepath.Join(r.root, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code := r.call(t, "/edit", map[string]any{"path": "run.sh", "old_string": "a", "new_string": "b"}, nil); code != 200 {
		t.Fatalf("edit: %d", code)
	}
	info, err := os.Stat(filepath.Join(r.root, "run.sh"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v, %v: want 0755 kept", info.Mode(), err)
	}
	if entries, _ := os.ReadDir(r.root); len(entries) != 1 {
		t.Fatalf("the workspace holds %d entries, want the file alone", len(entries))
	}
}

func TestAnEditRefusesAFileTooLargeToHold(t *testing.T) {
	r := newRoom(t)
	big := filepath.Join(r.root, "big.txt")
	if err := os.WriteFile(big, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, maxEdit+1); err != nil {
		t.Fatal(err)
	}
	var f failure
	if code := r.call(t, "/edit", map[string]any{"path": "big.txt", "old_string": "a", "new_string": "b"}, &f); code != 422 || !strings.Contains(f.Error, "Bash") {
		t.Fatalf("%d %q, want it refused with a way round", code, f.Error)
	}
}

// A read returns 2,000 lines at most, however many are asked for: more could
// pass what the brain reads of an answer.
func TestAReadReturnsAtMostSoManyLines(t *testing.T) {
	r := newRoom(t)
	r.put(t, "long.txt", strings.Repeat("line\n", DefaultLines+500))
	var got ReadResult
	if code := r.call(t, "/read", map[string]any{"path": "long.txt", "limit": 5000}, &got); code != 200 {
		t.Fatalf("read: %d", code)
	}
	if n := strings.Count(got.Content, "\n"); n != DefaultLines || got.Lines != DefaultLines+500 {
		t.Fatalf("%d lines of %d, want %d of %d", n, got.Lines, DefaultLines, DefaultLines+500)
	}
}

// A file of one enormous line, or of many long ones, is read in bounded
// memory and returned cut: the workspace is the room's main process, and a
// read that ran it out of memory would lose the room.
func TestAReadOfHugeLinesIsBounded(t *testing.T) {
	r := newRoom(t)
	r.put(t, "one.txt", strings.Repeat("x", 64<<20))
	var got ReadResult
	if code := r.call(t, "/read", map[string]any{"path": "one.txt"}, &got); code != 200 {
		t.Fatalf("read: %d", code)
	}
	want := "     1\t" + strings.Repeat("x", maxLine) + "… (line cut)\n"
	if got.Content != want || got.Lines != 1 {
		t.Fatalf("%d bytes over %d lines, want the line cut to %d", len(got.Content), got.Lines, maxLine)
	}

	r.put(t, "wide.txt", strings.Repeat(strings.Repeat("y", maxLine)+"\n", DefaultLines))
	if code := r.call(t, "/read", map[string]any{"path": "wide.txt"}, &got); code != 200 {
		t.Fatalf("read: %d", code)
	}
	if len(got.Content) > maxRead || got.Lines != DefaultLines || !strings.HasSuffix(got.Content, "\n") {
		t.Fatalf("%d bytes, %d lines: want at most %d bytes, whole lines, and every line counted", len(got.Content), got.Lines, maxRead)
	}
	if exact := strings.Repeat("y", maxLine); !strings.Contains(got.Content, "\t"+exact+"\n") || strings.Contains(got.Content, "line cut") {
		t.Fatal("a line exactly maxLine long was cut")
	}
}

// A search of a directory reached by a link finds what is in it.
func TestASearchOfALinkedDirectoryFindsItsFiles(t *testing.T) {
	r := newRoom(t)
	r.put(t, "real/a.go", "package a\n")
	if err := os.Symlink(filepath.Join(r.root, "real"), filepath.Join(r.root, "linked")); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Paths []string `json:"paths"`
	}
	if code := r.call(t, "/glob", map[string]any{"pattern": "*.go", "path": "linked"}, &got); code != 200 || len(got.Paths) != 1 {
		t.Fatalf("%d %v, want a.go found through the link", code, got.Paths)
	}
}

// Write onto a directory is the caller's to fix, and Write over a file keeps
// its mode and leaves nothing beside it.
func TestWriteOverWhatIsThere(t *testing.T) {
	r := newRoom(t)
	if err := os.Mkdir(filepath.Join(r.root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	var f failure
	if code := r.call(t, "/write", map[string]any{"path": "dir", "content": "x"}, &f); code != 422 || !strings.Contains(f.Error, "directory") {
		t.Fatalf("Write onto a directory: %d %q, want 422", code, f.Error)
	}
	r.put(t, "run.sh", "echo a\n")
	if err := os.Chmod(filepath.Join(r.root, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code := r.call(t, "/write", map[string]any{"path": "run.sh", "content": "echo b\n"}, nil); code != 200 {
		t.Fatalf("Write over a file: %d", code)
	}
	info, err := os.Stat(filepath.Join(r.root, "run.sh"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v, %v: want 0755 kept", info.Mode(), err)
	}
	if body, _ := os.ReadFile(filepath.Join(r.root, "run.sh")); string(body) != "echo b\n" {
		t.Fatalf("content %q", body)
	}
	if entries, _ := os.ReadDir(r.root); len(entries) != 2 {
		t.Fatalf("the workspace holds %d entries, want dir and run.sh", len(entries))
	}
}

// A file whose name is near the longest allowed is still replaced: the
// temporary name beside it is short.
func TestALongNamedFileIsReplaced(t *testing.T) {
	r := newRoom(t)
	name := strings.Repeat("n", 245)
	r.put(t, name, "a\n")
	if code := r.call(t, "/edit", map[string]any{"path": name, "old_string": "a", "new_string": "b"}, nil); code != 200 {
		t.Fatalf("Edit: %d", code)
	}
	if code := r.call(t, "/write", map[string]any{"path": name, "content": "c\n"}, nil); code != 200 {
		t.Fatalf("Write: %d", code)
	}
}

// A FIFO would hold a read until something wrote to it.
func TestAReadOfAFifoIsRefused(t *testing.T) {
	r := newRoom(t)
	if err := mkfifo(filepath.Join(r.root, "pipe")); err != nil {
		t.Skip("no mkfifo here:", err)
	}
	var f failure
	if code := r.call(t, "/read", map[string]any{"path": "pipe"}, &f); code != 422 || !strings.Contains(f.Error, "regular file") {
		t.Fatalf("%d %q, want it refused", code, f.Error)
	}
}

// Code is full of <, > and &; the answer carries them as they are.
func TestAnAnswerDoesNotEscapeCode(t *testing.T) {
	r := newRoom(t)
	r.put(t, "a.go", "if a < b && c > d {}\n")
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, r.url+"/read", strings.NewReader(`{"path":"a.go"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "a < b && c > d") {
		t.Fatalf("the answer escaped the code: %s", raw)
	}
}
