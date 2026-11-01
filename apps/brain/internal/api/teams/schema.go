package teams

import (
	"time"

	"github.com/google/uuid"
)

type team struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	Role      *string    `json:"role,omitempty"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
	PurgeAt   *time.Time `json:"purge_at,omitempty"`
}

type createRequest struct {
	Name string `json:"name"`
}

type member struct {
	UserID  uuid.UUID `json:"user_id"`
	Subject string    `json:"subject"`
	Email   *string   `json:"email,omitempty"`
	Role    string    `json:"role"`
}

type addMemberRequest struct {
	UserID  *uuid.UUID `json:"user_id,omitempty"`
	Subject *string    `json:"subject,omitempty"`
	Role    string     `json:"role"`
}

type teamCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}

type memberCursor struct {
	Subject string    `json:"s"`
	ID      uuid.UUID `json:"i"`
}

type permissions struct {
	Actions    []string `json:"actions"`
	SuperAdmin bool     `json:"super_admin"`
}
