package improvements

import (
	"strings"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
)

type op int

const (
	opSame op = iota
	opDel
	opAdd
)

type diffLine struct {
	op   op
	line string
}

// splitLines is the lines of s, with an empty string having none, so a file
// that was added shows no phantom blank line being removed. lineDiff makes
// each line safe to print, since the text is a model's.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n"), "\n")
}

// maxCells bounds the table: past it the changed middle is shown as removed,
// then added, which is still exact if less tidy. A proposal may carry 2 MiB.
const maxCells = 1_000_000

// lineDiff is a longest-common-subsequence line diff of what lies between the
// lines both sides share at the start and the end, which is all of an edit to
// one part of a long file.
func lineDiff(a, b []string) []diffLine {
	head := 0
	for head < len(a) && head < len(b) && a[head] == b[head] {
		head++
	}
	tail := 0
	for tail < len(a)-head && tail < len(b)-head && a[len(a)-1-tail] == b[len(b)-1-tail] {
		tail++
	}
	out := make([]diffLine, 0, len(a)+len(b)-head-tail)
	for _, l := range a[:head] {
		out = append(out, diffLine{opSame, cli.Line(l)})
	}
	out = append(out, middleDiff(a[head:len(a)-tail], b[head:len(b)-tail])...)
	for _, l := range a[len(a)-tail:] {
		out = append(out, diffLine{opSame, cli.Line(l)})
	}
	return out
}

func middleDiff(a, b []string) []diffLine {
	if len(a)*len(b) > maxCells {
		out := make([]diffLine, 0, len(a)+len(b))
		for _, l := range a {
			out = append(out, diffLine{opDel, cli.Line(l)})
		}
		for _, l := range b {
			out = append(out, diffLine{opAdd, cli.Line(l)})
		}
		return out
	}
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	out := make([]diffLine, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, diffLine{opSame, cli.Line(a[i])})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, diffLine{opDel, cli.Line(a[i])})
			i++
		default:
			out = append(out, diffLine{opAdd, cli.Line(b[j])})
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, diffLine{opDel, cli.Line(a[i])})
	}
	for ; j < len(b); j++ {
		out = append(out, diffLine{opAdd, cli.Line(b[j])})
	}
	return out
}
