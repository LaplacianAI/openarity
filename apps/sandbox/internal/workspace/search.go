package workspace

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	// maxMatches is how many paths a glob, or files a grep, returns.
	maxMatches = 1000
	// maxGrepLines is how many lines a grep's content returns.
	maxGrepLines = 2000
	// maxGrepFile is the largest file a grep reads; past it, a file is a
	// data dump, not source.
	maxGrepFile = 10 << 20
	// maxSearch is the most text a glob or a grep returns, as a read's
	// is: the counts above bound how many, and this how much, so 2,000
	// minified lines or 1,000 deep paths still fit an answer.
	maxSearch = maxRead
)

// skipped are directories no search walks into: their contents are a
// tool's, not the work's.
var skipped = map[string]bool{".git": true, "node_modules": true, "__pycache__": true, ".venv": true}

// GlobResult is the files a pattern matched, newest first.
type GlobResult struct {
	Paths     []string `json:"paths"`
	Truncated bool     `json:"truncated"`
}

// glob returns the files under base whose path relative to it matches
// pattern, where ** stands for any number of directories.
func glob(base, pattern string) (GlobResult, error) {
	if _, err := splitPattern(pattern); err != nil {
		return GlobResult{}, err
	}
	type found struct {
		path string
		mod  int64
	}
	var all []found
	err := walk(base, func(p, rel string, d fs.DirEntry) error {
		if ok, _ := matchPath(pattern, rel); !ok {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr // a file gone mid-walk is left out
		}
		all = append(all, found{p, info.ModTime().UnixNano()})
		return nil
	})
	if err != nil {
		return GlobResult{}, err
	}
	slices.SortFunc(all, func(a, b found) int {
		return cmp.Or(cmp.Compare(b.mod, a.mod), strings.Compare(a.path, b.path))
	})
	res := GlobResult{Paths: []string{}}
	size := 0
	for i, f := range all {
		if i == maxMatches || size+len(f.path)+1 > maxSearch {
			res.Truncated = true
			break
		}
		size += len(f.path) + 1
		res.Paths = append(res.Paths, f.path)
	}
	return res, nil
}

// GrepQuery is what a grep looks for, and where.
type GrepQuery struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	Output     string `json:"output"` // files (the default), content or count
	Context    int    `json:"context"`
	IgnoreCase bool   `json:"ignore_case"`
}

// GrepResult is a grep's answer as text, one line per file or per line.
type GrepResult struct {
	Output    string `json:"output"`
	Files     int    `json:"files"`
	Truncated bool   `json:"truncated"`
}

// grep searches the text files under q.Path, already resolved, for q.Pattern.
func grep(q GrepQuery) (GrepResult, error) {
	expr := q.Pattern
	if q.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return GrepResult{}, problem("pattern is not a regular expression (RE2): %v", err)
	}
	if q.Glob != "" {
		if _, err := splitPattern(q.Glob); err != nil {
			return GrepResult{}, err
		}
	}
	switch q.Output {
	case "":
		q.Output = "files"
	case "files", "content", "count":
	default:
		return GrepResult{}, problem("output is files, content or count, not %q", q.Output)
	}
	q.Context = max(0, min(q.Context, 20))

	var out strings.Builder
	res := GrepResult{}
	lines := 0
	// emit adds a line of output, or says the output is full.
	emit := func(line string) bool {
		if out.Len()+len(line) > maxSearch {
			res.Truncated = true
			return false
		}
		out.WriteString(line)
		return true
	}
	err = walk(q.Path, func(p, rel string, d fs.DirEntry) error {
		if q.Glob != "" && !globMatches(q.Glob, rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxGrepFile {
			return nil //nolint:nilerr // a file gone mid-walk, or a data dump, is left out
		}
		body, err := readWhole(p)
		if err != nil || binary(body[:min(len(body), sniff)]) {
			return nil //nolint:nilerr // an unreadable file, or a binary one, is left out
		}
		text := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
		var hits []int
		for i, l := range text {
			if re.MatchString(l) {
				hits = append(hits, i)
			}
		}
		if len(hits) == 0 {
			return nil
		}
		if res.Files == maxMatches {
			res.Truncated = true
			return fs.SkipAll
		}
		switch q.Output {
		case "files":
			if !emit(p + "\n") {
				return fs.SkipAll
			}
		case "count":
			if !emit(fmt.Sprintf("%s:%d\n", p, len(hits))) {
				return fs.SkipAll
			}
		case "content":
			shown := false
			// stop ends the search part-way through this file, which is
			// counted if any of it was shown.
			stop := func() error {
				if shown {
					res.Files++
				}
				return fs.SkipAll
			}
			for _, r := range spans(hits, q.Context, len(text)) {
				if lines >= maxGrepLines {
					res.Truncated = true
					return stop()
				}
				if r.gap {
					if !emit("--\n") {
						return stop()
					}
					continue
				}
				sep := "-"
				if r.hit {
					sep = ":"
				}
				l := text[r.line]
				if len(l) > maxLine {
					l = l[:maxLine] + "… (line cut)"
				}
				if !emit(fmt.Sprintf("%s%s%d%s%s\n", p, sep, r.line+1, sep, l)) {
					return stop()
				}
				shown = true
				lines++
			}
		}
		res.Files++
		return nil
	})
	if err != nil {
		return GrepResult{}, err
	}
	res.Output = out.String()
	return res, nil
}

// A span line is one line to print around the matches, or a gap between two
// groups of them.
type spanLine struct {
	line int
	hit  bool
	gap  bool
}

// spans is the lines to print for hits with context lines around each, the
// overlapping ones merged, and a gap marked between groups.
func spans(hits []int, context, n int) []spanLine {
	isHit := map[int]bool{}
	for _, h := range hits {
		isHit[h] = true
	}
	var out []spanLine
	next := 0 // the first line not printed yet
	for _, h := range hits {
		from, to := max(0, h-context), min(n-1, h+context)
		if from < next {
			from = next
		}
		if len(out) > 0 && from > next {
			out = append(out, spanLine{gap: true})
		}
		for l := from; l <= to; l++ {
			out = append(out, spanLine{line: l, hit: isHit[l]})
		}
		next = max(next, to+1)
	}
	return out
}

// walk calls fn for each regular file under base, with its path and its path
// relative to base in slashes. A base that is a file is walked as itself.
func walk(base string, fn func(p, rel string, d fs.DirEntry) error) error {
	info, err := os.Stat(base)
	if err != nil {
		return asProblem(err)
	}
	if !info.IsDir() {
		return ignoreSkipAll(fn(base, filepath.Base(base), fs.FileInfoToDirEntry(info)))
	}
	return ignoreSkipAll(filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable directory is left out, not a failed search.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != base && (skipped[d.Name()] || inProc(p)) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return nil //nolint:nilerr // cannot happen under base; left out if it does
		}
		return fn(p, filepath.ToSlash(rel), d)
	}))
}

// readWhole is a file searched: opened as Read opens one, so not on /proc,
// and no more than maxGrepFile of it.
func readWhole(p string) ([]byte, error) {
	f, err := open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxGrepFile+1))
}

func ignoreSkipAll(err error) error {
	if errors.Is(err, fs.SkipAll) {
		return nil
	}
	return err
}

// globMatches is a grep's file filter: a pattern with a slash matches the
// relative path, one without matches the file's name wherever it is, as
// ripgrep's --glob does.
func globMatches(pattern, rel string) bool {
	if !strings.Contains(pattern, "/") {
		ok, _ := path.Match(pattern, path.Base(rel))
		return ok
	}
	ok, _ := matchPath(pattern, rel)
	return ok
}

// splitPattern checks a pattern and splits it into its parts.
func splitPattern(pattern string) ([]string, error) {
	if pattern == "" {
		return nil, problem("the pattern is empty")
	}
	parts := strings.Split(strings.TrimPrefix(pattern, "./"), "/")
	for _, p := range parts {
		if p == "**" {
			continue
		}
		if _, err := path.Match(p, ""); err != nil {
			return nil, problem("%q is not a glob pattern: %v", pattern, err)
		}
	}
	return parts, nil
}

// matchPath matches rel, a slash path, against pattern part by part, with
// ** matching any number of parts, none included.
func matchPath(pattern, rel string) (bool, error) {
	parts, err := splitPattern(pattern)
	if err != nil {
		return false, err
	}
	return matchParts(parts, strings.Split(rel, "/")), nil
}

func matchParts(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(name); i++ {
				if matchParts(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], name[0]); !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}
