package authz

type Action string

const (
	ActionAgentWrite      Action = "agent:write"
	ActionToolWrite       Action = "tool:write"
	ActionChannelWrite    Action = "channel:write"
	ActionMembershipWrite Action = "membership:write"
)

var AllActions = []Action{
	ActionAgentWrite, ActionToolWrite, ActionChannelWrite, ActionMembershipWrite,
}
