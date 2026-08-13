package printer

import (
	"encoding/json"
	"fmt"
)

type jsonPrinter struct {
	base
}

func (p jsonPrinter) Print(value any, _ Options) error {
	encoder := json.NewEncoder(p.w)
	encoder.SetIndent("", "  ")

	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("render json: %w", err)
	}
	return nil
}
