package mcpservers

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type server struct {
	ID            uuid.UUID         `json:"id"`
	TeamID        uuid.UUID         `json:"team_id"`
	Name          string            `json:"name"`
	URL           *string           `json:"url,omitempty"`
	Command       []string          `json:"command,omitempty"`
	Env           map[string]string `json:"env"`
	AuthSecretRef *string           `json:"auth_secret_ref,omitempty"`
	Bare          bool              `json:"bare"`
	DiscoveredAt  *time.Time        `json:"discovered_at"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

type serverRequest struct {
	Name          string            `json:"name"`
	URL           *string           `json:"url"`
	Command       []string          `json:"command"`
	Env           map[string]string `json:"env"`
	AuthSecretRef *string           `json:"auth_secret_ref"`
	Bare          bool              `json:"bare"`
}

type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type toolList struct {
	Items []tool `json:"items"`
}

type serverCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}
