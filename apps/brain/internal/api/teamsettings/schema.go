package teamsettings

import "time"

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

type settings struct {
	Model              *modelRef  `json:"model"`
	Search             search     `json:"search"`
	Learning           bool       `json:"learning"`
	CompactionMessages int32      `json:"compaction_messages"`
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
}
