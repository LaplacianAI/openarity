/**
 * The roles the brain ships, nested: each can do what the one before it can.
 * A guest only talks to the agents from a channel; a member also builds; an
 * approver also lets waiting senders in; an admin also manages the team.
 */
export const ROLES = ["guest", "member", "approver", "admin"] as const;
