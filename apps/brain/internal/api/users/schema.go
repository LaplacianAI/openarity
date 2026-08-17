package users

import "github.com/google/uuid"

type user struct {
	ID      uuid.UUID `json:"id"`
	Issuer  string    `json:"issuer"`
	Subject string    `json:"subject"`
	Email   *string   `json:"email,omitempty"`
}

type userCursor struct {
	Subject string    `json:"s"`
	ID      uuid.UUID `json:"i"`
}
