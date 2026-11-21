package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A grep over minified lines is cut by its size, not only by its count, so
// the answer fits what the brain reads, and says it was cut.
func TestAGrepOfWideLinesIsCutBySize(t *testing.T) {
	r := newRoom(t)
	line := strings.Repeat("<x&y>", 1000) + "\n" // 5,000 bytes, every one matching
	r.put(t, "dist/app.min.js", strings.Repeat(line, 1500))
	for _, mode := range []string{"content", "files", "count"} {
		var got GrepResult
		if code := r.call(t, "/grep", map[string]any{"pattern": "x", "output": mode}, &got); code != 200 {
			t.Fatalf("%s: %d", mode, code)
		}
		if len(got.Output) > maxSearch {
			t.Errorf("%s: %d bytes, over %d", mode, len(got.Output), maxSearch)
		}
		if mode == "content" && (!got.Truncated || got.Files != 1) {
			t.Errorf("content: truncated %v, files %d; want cut and the one file counted", got.Truncated, got.Files)
		}
		if b, _ := json.Marshal(got); len(b) > 4<<20 {
			t.Errorf("%s: %d bytes as JSON, over the brain's 4 MiB", mode, len(b))
		}
	}
}

// A glob of many deep paths is cut by size too.
func TestAGlobOfDeepPathsIsCutBySize(t *testing.T) {
	r := newRoom(t)
	// About 700 bytes a path, so 900 of them, under the count cap, pass
	// the size cap.
	deep := filepath.Join(r.root, strings.Repeat("d", 200), strings.Repeat("e", 200))
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 900 {
		name := fmt.Sprintf("%03d", i) + strings.Repeat("f", 230) + ".txt"
		if err := os.WriteFile(filepath.Join(deep, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var got GlobResult
	if code := r.call(t, "/glob", map[string]any{"pattern": "**/*.txt"}, &got); code != 200 {
		t.Fatalf("glob: %d", code)
	}
	size := 0
	for _, p := range got.Paths {
		size += len(p) + 1
	}
	if !got.Truncated || size > maxSearch || len(got.Paths) == 0 {
		t.Fatalf("%d paths, %d bytes, truncated %v: want cut under %d", len(got.Paths), size, got.Truncated, maxSearch)
	}
}

// A file with more matching lines than a grep shows is cut at the line cap,
// counted, and its lines kept.
func TestAGrepCutAtItsLineCapCountsTheFile(t *testing.T) {
	r := newRoom(t)
	r.put(t, "many.txt", strings.Repeat("hit\n", maxGrepLines+500))
	var got GrepResult
	if code := r.call(t, "/grep", map[string]any{"pattern": "hit", "output": "content"}, &got); code != 200 {
		t.Fatalf("grep: %d", code)
	}
	if !got.Truncated || got.Files != 1 || strings.Count(got.Output, "\n") != maxGrepLines {
		t.Fatalf("truncated %v, files %d, %d lines: want cut at %d with the file counted", got.Truncated, got.Files, strings.Count(got.Output, "\n"), maxGrepLines)
	}
}

// Files and count modes are cut by size too, over files enough to pass it
// and not the count cap.
func TestAGrepOfManyLongPathsIsCutBySize(t *testing.T) {
	r := newRoom(t)
	dir := filepath.Join(r.root, strings.Repeat("d", 200), strings.Repeat("e", 200))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 900 {
		name := fmt.Sprintf("%03d", i) + strings.Repeat("f", 230) + ".txt"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("hit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"files", "count"} {
		var got GrepResult
		if code := r.call(t, "/grep", map[string]any{"pattern": "hit", "output": mode}, &got); code != 200 {
			t.Fatalf("%s: %d", mode, code)
		}
		if !got.Truncated || len(got.Output) > maxSearch || got.Files == 0 || got.Files >= 900 {
			t.Errorf("%s: truncated %v, %d bytes, %d files: want cut under %d", mode, got.Truncated, len(got.Output), got.Files, maxSearch)
		}
		if got.Files != strings.Count(got.Output, "\n") {
			t.Errorf("%s: %d files counted, %d shown", mode, got.Files, strings.Count(got.Output, "\n"))
		}
	}
}
