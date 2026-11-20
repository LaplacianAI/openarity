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
