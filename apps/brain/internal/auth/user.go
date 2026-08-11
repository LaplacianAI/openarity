package auth

import "github.com/google/uuid"

type Membership struct {
	TeamID uuid.UUID
	Name   string
	Role   string
}

type User struct {
	ID      uuid.UUID
	Issuer  string
	Subject string
	Email   *string
	Teams   []Membership
}

func (u *User) RoleIn(teamID uuid.UUID) (string, bool) {
	for _, m := range u.Teams {
		if m.TeamID == teamID {
			return m.Role, true
		}
	}
	return "", false
}
