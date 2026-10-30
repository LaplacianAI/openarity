package runtime

import (
	"slices"

	"github.com/google/uuid"
)

type Narrowing struct {
	Tools  []string
	Skills []uuid.UUID
}

func (n *Narrowing) keepsTool(name string) bool {
	return n == nil || slices.Contains(n.Tools, name)
}

func (n *Narrowing) keepsSkill(id uuid.UUID) bool {
	return n == nil || slices.Contains(n.Skills, id)
}
