package teamsettings

import (
	"time"

	"github.com/google/uuid"
)

type modelRef struct {
	Name        string   `json:"name"`
	MaxTokens   int32    `json:"max_tokens"`
	Temperature *float64 `json:"temperature,omitempty"`
}

type search struct {
	Provider string  `json:"provider"`
	URL      *string `json:"url"`
	KeySet   bool    `json:"key_set"`
}

// sandbox is where the team's command MCP servers run. The key is never
// shown; what a room starts from is the brain's choice, not the team's.
type sandbox struct {
	Provider string  `json:"provider"`
	URL      *string `json:"url"`
	Template *string `json:"template"`
	KeySet   bool    `json:"key_set"`
}

// browser is what the team's runs drive pages in. The key is never shown.
type browser struct {
	Provider string  `json:"provider"`
	URL      *string `json:"url"`
	KeySet   bool    `json:"key_set"`
}

type hubRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type settings struct {
	Model              *modelRef  `json:"model"`
	Search             search     `json:"search"`
	Sandbox            sandbox    `json:"sandbox"`
	Browser            browser    `json:"browser"`
	Learning           bool       `json:"learning"`
	CompactionMessages int32      `json:"compaction_messages"`
	Hub                *hubRef    `json:"hub"`
	UpdatedAt          *time.Time `json:"updated_at"`
}

type searchRequest struct {
	Provider string  `json:"provider"`
	URL      *string `json:"url"`
	APIKey   *string `json:"api_key"`
}

// sandboxRequest is the sandbox to save. Left out of a request, the stored one
// stays: a client that predates sandboxes cannot clear a team's.
type sandboxRequest struct {
	Provider string  `json:"provider"`
	URL      *string `json:"url"`
	Template *string `json:"template"`
	APIKey   *string `json:"api_key"`
}

// browserRequest is the browser to save. Left out of a request, the stored one
// stays, as a sandbox does.
type browserRequest struct {
	Provider string  `json:"provider"`
	URL      *string `json:"url"`
	APIKey   *string `json:"api_key"`
}

type settingsRequest struct {
	Model              *modelRef       `json:"model"`
	Search             searchRequest   `json:"search"`
	Sandbox            *sandboxRequest `json:"sandbox"`
	Browser            *browserRequest `json:"browser"`
	Learning           bool            `json:"learning"`
	CompactionMessages int32           `json:"compaction_messages"`
	Hub                *string         `json:"hub"`
}
