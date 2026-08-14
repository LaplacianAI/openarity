package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

type Paging struct {
	limit  int32
	cursor string
}

func (p *Paging) Flags(cmd *cobra.Command) {
	cmd.Flags().Int32Var(&p.limit, "limit", 0,
		"rows per page; the brain clamps anything above its maximum")
	cmd.Flags().StringVar(&p.cursor, "cursor", "",
		"an opaque position, taken verbatim from a previous page")
}

func (p *Paging) Values() (*int32, *string) {
	var limit *int32
	if p.limit > 0 {
		limit = &p.limit
	}

	var cursor *string
	if trimmed := strings.TrimSpace(p.cursor); trimmed != "" {
		cursor = &trimmed
	}

	return limit, cursor
}

type Page[T any] struct {
	Items      []T
	NextCursor *string
	Empty      string
	More       string
	Row        func(*printer.Table, T)
}

type pageView[T any] struct {
	Items      []T     `json:"items" yaml:"items"`
	NextCursor *string `json:"next_cursor,omitempty" yaml:"next_cursor,omitempty"`
}

func PrintPage[T any](opts *Options, page Page[T]) error {
	items := page.Items
	if items == nil {
		// A nil slice marshals to null and `jq length` fails on null, which is
		// exactly what a fresh install would produce.
		items = make([]T, 0)
	}

	if len(items) == 0 && page.Empty != "" {
		opts.Out.Note(opts.Styles.Muted.Render(page.Empty))
	}

	err := opts.Out.Print(
		pageView[T]{Items: items, NextCursor: page.NextCursor},
		printer.Options{
			Table: func(table *printer.Table) {
				for _, item := range items {
					page.Row(table, item)
				}
			},
		})
	if err != nil {
		return err
	}

	if page.NextCursor != nil && page.More != "" {
		opts.Out.Note(opts.Styles.Muted.Render(
			fmt.Sprintf("more — `%s --cursor %s`", page.More, *page.NextCursor)))
	}
	return nil
}
