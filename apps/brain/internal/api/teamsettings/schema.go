package teamsettings

import (
	"encoding/json"
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
	Model       *modelRef `json:"model"`
	Search      search    `json:"search"`
	Sandbox     sandbox   `json:"sandbox"`
	Browser     browser   `json:"browser"`
	Improvement bool      `json:"improvement"`
	// The model that judges and digests runs; null is the team's model.
	JudgeModel             *modelRef `json:"judge_model"`
	ImprovementSampleShare int16     `json:"improvement_sample_share"`
	ImprovementDailyPasses int16     `json:"improvement_daily_passes"`
	// What a pass may spend: tokens across its tries, the screen's
	// included, and minutes for each try.
	ImprovementPassTokens  int32 `json:"improvement_pass_tokens"`
	ImprovementPassMinutes int16 `json:"improvement_pass_minutes"`
	// When the switch last went on; runs before it are never judged.
	ImprovementSince    *time.Time `json:"improvement_since"`
	CompactionMessages  int32      `json:"compaction_messages"`
	CompactionTokensMin int32      `json:"compaction_tokens_min"`
	CompactionTokensMax int32      `json:"compaction_tokens_max"`
	Hub                 *hubRef    `json:"hub"`
	UpdatedAt           *time.Time `json:"updated_at"`
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
	Model       *modelRef       `json:"model"`
	Search      searchRequest   `json:"search"`
	Sandbox     *sandboxRequest `json:"sandbox"`
	Browser     *browserRequest `json:"browser"`
	Improvement bool            `json:"improvement"`
	// Left out, the stored judge model stays; null clears it to the team's.
	JudgeModel maybeModel `json:"judge_model"`
	// Left out, the stored share stays.
	ImprovementSampleShare *int16 `json:"improvement_sample_share"`
	// The improver passes a day at most; left out keeps what is stored.
	ImprovementDailyPasses *int16 `json:"improvement_daily_passes"`
	// What a pass may spend; left out keeps what is stored.
	ImprovementPassTokens  *int32 `json:"improvement_pass_tokens"`
	ImprovementPassMinutes *int16 `json:"improvement_pass_minutes"`
	CompactionMessages     int32  `json:"compaction_messages"`
	// Left out, the stored value stays, as a sandbox does.
	CompactionTokensMin *int32  `json:"compaction_tokens_min"`
	CompactionTokensMax *int32  `json:"compaction_tokens_max"`
	Hub                 *string `json:"hub"`
}

// maybeModel tells a model left out of a request from one sent as null: its
// UnmarshalJSON runs only for a key that is present.
type maybeModel struct {
	Set   bool
	Model *modelRef
}

func (m *maybeModel) UnmarshalJSON(b []byte) error {
	m.Set = true
	return json.Unmarshal(b, &m.Model)
}
