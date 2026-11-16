package teams

import "errors"

// MemberChange is a change to one member: a role, a name, or both. Nil leaves
// it as it is.
type MemberChange struct {
	Role *string
	Name *string
}

var (
	// ErrNotMember is UpdateMember naming someone outside the team.
	ErrNotMember = errors.New("not a member of the team")
	// ErrLastAdmin is UpdateMember refusing to leave a team with no admin.
	ErrLastAdmin = errors.New("the team's last admin keeps their role")
	// ErrNotChannelUser is UpdateMember naming a user who logs in, whose name
	// is their identity provider's.
	ErrNotChannelUser = errors.New("only a channel user's name can be set")
)
