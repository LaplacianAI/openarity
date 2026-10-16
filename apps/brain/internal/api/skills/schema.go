package skills

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type summary struct {
	ID          uuid.UUID `json:"id"`
	TeamID      uuid.UUID `json:"team_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Source      string    `json:"source"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type file struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	MediaType string `json:"media_type"`
}

type detail struct {
	summary
	License       *string         `json:"license,omitempty"`
	Compatibility *string         `json:"compatibility,omitempty"`
	Metadata      json.RawMessage `json:"metadata"`
	AllowedTools  *string         `json:"allowed_tools,omitempty"`
	SourceRef     *string         `json:"source_ref,omitempty"`
	SourceSHA     *string         `json:"source_sha,omitempty"`
	Body          string          `json:"body"`
	Files         []file          `json:"files"`
}

type skillCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}

type importRequest struct {
	Source string `json:"source"`
}
