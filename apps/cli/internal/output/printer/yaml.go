package printer

import (
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

type yamlPrinter struct {
	base
}

func (p yamlPrinter) Print(value any, _ Options) error {
	encoder := yaml.NewEncoder(p.w)
	encoder.SetIndent(2)

	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("render yaml: %w", err)
	}
	return encoder.Close()
}

func (p yamlPrinter) Line(value any, opts Options) error {
	if _, err := io.WriteString(p.w, "---\n"); err != nil {
		return err
	}
	return p.Print(value, opts)
}
