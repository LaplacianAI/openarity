package runs

import (
	"time"

	"github.com/google/uuid"
)

type startRequest struct {
	Agent     string     `json:"agent"`
	Input     string     `json:"input"`
	SessionID *uuid.UUID `json:"session_id"`
}

type run struct {
	ID        uuid.UUID `json:"id"`
	SessionID uuid.UUID `json:"session_id"`
	Agent     string    `json:"agent"`
	Status    string    `json:"status"`
	Input     string    `json:"input"`
	Output    *string   `json:"output"`
	Error     *string   `json:"error"`

	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`

	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`

	ParentRunID *uuid.UUID `json:"parent_run_id"`
	Depth       int32      `json:"depth"`
}

type runCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}
