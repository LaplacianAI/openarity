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

type hubRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type settings struct {
	Model              *modelRef  `json:"model"`
	Search             search     `json:"search"`
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

type settingsRequest struct {
	Model              *modelRef     `json:"model"`
	Search             searchRequest `json:"search"`
	Learning           bool          `json:"learning"`
	CompactionMessages int32         `json:"compaction_messages"`
	Hub                *string       `json:"hub"`
}
