package printer

import (
	"io"

	"github.com/LaplacianAI/openarity/apps/cli/internal/output"
)

type Options struct {
	Table func(*Table)
}

type Printer interface {
	Print(value any, opts Options) error
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
