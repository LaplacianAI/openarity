package cli

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

func ParseUUID(what, raw string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("%q is not a %s", raw, what)
	}
	return parsed, nil
}
