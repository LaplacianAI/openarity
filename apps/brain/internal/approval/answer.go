package approval

// Who may answer a request is one rule, whichever door the answer comes in
// by: the dashboard, oa, or the chat.

// MayAnswer is whether someone may answer a request: anyone who decides the
// team's requests, or the person it was made for (own) when they may approve
// their own, unless a rule wants someone else to (requireOther). Deciding
// grants nothing beyond the one call the agent was already allowed to make,
// so there is no ceiling to check.
func MayAnswer(own, requireOther, mayDecide, maySelf bool) bool {
	switch {
	case own && requireOther:
		return false
	case mayDecide:
		return true
	default:
		return own && maySelf
	}
}

// AsksInChat is whether the chat a request came from asks its person to
// answer it there: only when they may approve their own and no rule wants
// someone else to. Otherwise the chat is told it went to an approver, who
// answers where the arguments can be read, never with a word in passing.
func AsksInChat(requireOther, maySelf bool) bool {
	return maySelf && !requireOther
}
