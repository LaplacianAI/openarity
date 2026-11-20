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
// that was added shows no phantom blank line being removed. Each line is made
// safe: the text is a model's.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n"), "\n")
}

// lineDiff is a longest-common-subsequence line diff. Proposals change
// instructions and skill files, tens of lines, so the quadratic table costs
// nothing.
func lineDiff(a, b []string) []diffLine {
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
