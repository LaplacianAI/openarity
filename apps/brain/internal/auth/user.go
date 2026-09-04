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

	// SuperAdmin is the grant held in the database, which is the half of the
	// answer that can change while the brain is running. The other half lives
	// in OPENARITY_SUPER_ADMINS and is matched on Subject, so neither one can
	// see the other and only authz.Authorizer knows both. Nothing outside the
	// store may set this: it is written by a promotion inside the transaction
	// that resolves the user, and read everywhere else.
	SuperAdmin bool
}

func (u *User) RoleIn(teamID uuid.UUID) (string, bool) {
	for _, m := range u.Teams {
		if m.TeamID == teamID {
			return m.Role, true
		}
	}
	return "", false
}
