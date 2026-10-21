package printer

import (
	"io"

	"github.com/LaplacianAI/openarity/apps/cli/internal/output"
)

type Options struct {
	Table func(*Table)
}

// Line prints one record of a stream as it arrives: a json object on one
// line, a yaml document behind its own ---, or the table's rows. Print is for
// a whole value; a stream has no end to wait for.
type Printer interface {
	Print(value any, opts Options) error
	Line(value any, opts Options) error
	Note(text string)
}

func New(w io.Writer, format output.Format) Printer {
	shared := base{w: w}

	switch format {
	case output.JSON:
		return jsonPrinter{shared}
	case output.YAML:
		return yamlPrinter{shared}
	case output.Table:
		return tablePrinter{shared}
	default:
		return tablePrinter{shared}
	}
}

type base struct {
	w io.Writer
}

func (b base) Note(string) {}
