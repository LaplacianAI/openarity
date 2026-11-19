package approvals

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// approval is one request, as a person who may see its run reads it.
type approval struct {
	ID           uuid.UUID       `json:"id"`
	RunID        uuid.UUID       `json:"run_id"`
	RootRunID    uuid.UUID       `json:"root_run_id"`
	CallID       string          `json:"call_id"`
	Tool         string          `json:"tool"`
	Class        string          `json:"class"`
	Arguments    json.RawMessage `json:"arguments"`
	Summary      string          `json:"summary"`
	Code         string          `json:"code"`
	Status       string          `json:"status"`
	RequestedFor *uuid.UUID      `json:"requested_for"`
	RequireOther bool            `json:"require_other"`
	DecidedBy    *uuid.UUID      `json:"decided_by"`
	DecidedVia   *string         `json:"decided_via"`
	Scope        *string         `json:"scope"`
	Reason       *string         `json:"reason"`
	ExpiresAt    time.Time       `json:"expires_at"`
	DecidedAt    *time.Time      `json:"decided_at"`
	UsedAt       *time.Time      `json:"used_at"`
	CreatedAt    time.Time       `json:"created_at"`
}

func view(a db.Approval) approval {
	args := json.RawMessage(a.Arguments)
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	return approval{
		ID: a.ID, RunID: a.RunID, RootRunID: a.RootRunID, CallID: a.CallID, Tool: a.Tool, Class: a.Class,
		Arguments: args, Summary: a.Summary, Code: a.Code, Status: a.Status,
		RequestedFor: a.RequestedFor, RequireOther: a.RequireOther,
		DecidedBy: a.DecidedBy, DecidedVia: a.DecidedVia, Scope: a.DecisionScope, Reason: a.Reason,
		ExpiresAt: a.ExpiresAt, DecidedAt: a.DecidedAt, UsedAt: a.UsedAt, CreatedAt: a.CreatedAt,
	}
}

type approvalCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}

// decisionRequest is one person's answer. Approve is required: a body that
// forgets it is not taken as a no.
type decisionRequest struct {
	Approve *bool  `json:"approve"`
	Scope   string `json:"scope"`
	Reason  string `json:"reason"`
	// Via is where the person answered: dashboard, the default, or cli. It
	// is a record of where, never a permission.
	Via string `json:"via"`
}

// settings is how a team's agents are asked before they act.
type settings struct {
	Mode       string `json:"mode"`
	TTLMinutes int32  `json:"ttl_minutes"`
}

type rule struct {
	ID           uuid.UUID `json:"id"`
	TargetKind   string    `json:"target_kind"`
	Target       string    `json:"target"`
	Mode         string    `json:"mode"`
	RequireOther bool      `json:"require_other"`
	CreatedAt    time.Time `json:"created_at"`
}

func ruleView(r db.ApprovalRule) rule {
	return rule{
		ID: r.ID, TargetKind: r.TargetKind, Target: r.Target, Mode: r.Mode,
		RequireOther: r.RequireOther, CreatedAt: r.CreatedAt,
	}
}

type ruleRequest struct {
	TargetKind   string `json:"target_kind"`
	Target       string `json:"target"`
	Mode         string `json:"mode"`
	RequireOther bool   `json:"require_other"`
}
