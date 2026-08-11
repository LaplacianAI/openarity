package teams

import "github.com/google/uuid"

type team struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Role *string   `json:"role,omitempty"`
}

type createRequest struct {
	Name string `json:"name"`
}
