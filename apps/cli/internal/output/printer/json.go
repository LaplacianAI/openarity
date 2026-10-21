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

func (p jsonPrinter) Line(value any, _ Options) error {
	if err := json.NewEncoder(p.w).Encode(value); err != nil {
		return fmt.Errorf("render json: %w", err)
	}
	return nil
}
