# What the brain may do, and nothing more.
#
# The usual shortcut is to hand the server a root token in an environment
# variable. This is the whole reason not to: the capabilities below are the
# entire set, and every path outside them is refused whatever the brain asks.

# Channel credentials live at teams/<team_id>/channels/<channel_id>, and KV v2
# puts the current version under data/.
#
# `+` matches exactly one path segment, which is what makes this a real
# restriction rather than a longer way of writing teams/*. Verified against
# this OpenBao rather than taken from the documentation:
#
#   teams/T1/channels/C1          read and write   the shape a channel uses
#   teams/T1/tokens/K1            denied           a sibling kind is not ours
#   teams/T1/channels/C1/extra    denied           + is not greedy
#   teams/T1                      denied           the team itself
#   teams/T2/channels/C9          read and write   any team, which the brain serves
#
# The brain writes here because registering a channel returns its signing
# secret once and stores it in the same request. That is a deliberate change
# from the original read-only policy: it said channel registration would get
# its own role, and it will not while the brain is the thing serving the
# endpoint. A second credential in the same process is not a boundary — the
# process holds both — so the boundary is the path shape instead, and the
# authorisation that matters is RBAC's channel:write on the route.
path "secret/data/teams/+/channels/+" {
  capabilities = ["read", "create", "update"]
}

# Deleting a channel destroys its secret rather than soft-deleting a version.
# That is a separate path in KV v2: `delete` on data/ hides the latest version
# and leaves it recoverable, while `delete` on metadata/ removes every version.
# A disconnected channel's signing secret should not survive in history.
#
# Only delete. Granting it does not grant read or list here — checked, not
# assumed — so version history stays as closed as it was before.
path "secret/metadata/teams/+/channels/+" {
  capabilities = ["delete"]
}

# Renewing its own token is how the brain avoids logging in on every read.
# The default policy already grants this, so it is redundant today — and it is
# what keeps renewal working the day the role sets token_no_default_policy.
path "auth/token/renew-self" {
  capabilities = ["update"]
}

# Deliberately absent:
#
#   list        a list on a KV path returns every team id. Read requires
#               knowing the path already; list hands over the map.
#   metadata read  version history is as good as the value for anything that
#               was ever written, so the delete above is the only thing
#               granted on that path.
#   sys/*, auth/*  the brain never administers OpenBao. Creating roles,
#               writing policies and unsealing are operator actions.
