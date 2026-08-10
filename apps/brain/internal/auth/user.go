package auth

import "github.com/google/uuid"

type Membership struct {
	TeamID uuid.UUID
	Name   string
	Role   string
}

type User struct {
	ID    uuid.UUID
	Email *string
	Teams []Membership
}
