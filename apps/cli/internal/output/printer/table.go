package printer

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type Table struct {
	rows [][]string
}

func (t *Table) Row(cells ...string) {
	t.rows = append(t.rows, cells)
}

func (t *Table) render() string {
	widths := t.widths()

	var out strings.Builder
	for _, row := range t.rows {
		for i, cell := range row {
			out.WriteString(cell)

			if i == len(row)-1 {
				continue
			}
			out.WriteString(strings.Repeat(" ", widths[i]-lipgloss.Width(cell)+2))
		}
		out.WriteString("\n")
	}
	return out.String()
}

func (t *Table) widths() []int {
	var widths []int

	for _, row := range t.rows {
		for i, cell := range row {
			for len(widths) <= i {
				widths = append(widths, 0)
			}
			if w := lipgloss.Width(cell); w > widths[i] {
				widths[i] = w
			}
		}
	}
	return widths
}

type tablePrinter struct {
	base
}

func (p tablePrinter) Print(_ any, opts Options) error {
	if opts.Table == nil {
		return nil
	}

	var table Table
	opts.Table(&table)

	_, err := fmt.Fprint(p.w, table.render())
	return err
}

func (p tablePrinter) Note(text string) {
	fmt.Fprintln(p.w, text)
}
