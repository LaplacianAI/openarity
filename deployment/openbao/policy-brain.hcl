# What the brain may do, and nothing more.
#
# The usual shortcut is to hand the server a root token in an environment
# variable. This is the whole reason not to: a compromised brain can read the
# credentials of channels that already exist and cannot create, change or
# enumerate anything.

# Channel credentials live at teams/<team_id>/channels/<channel_id>, and KV v2
# puts the current version under data/.
path "secret/data/teams/*" {
  capabilities = ["read"]
}

# Renewing its own token is how the brain avoids logging in on every read.
# The default policy already grants this, so it is redundant today — and it is
# what keeps renewal working the day the role sets token_no_default_policy.
path "auth/token/renew-self" {
  capabilities = ["update"]
}

# Deliberately absent:
#
#   create/update  the brain never writes a secret. Registering a channel is
#                  an administrative action and will get its own role.
#   delete         same.
#   list           a list on a KV path returns every team id. Read requires
#                  knowing the path already; list hands over the map.
#   metadata/*     version history is as good as the value for anything that
#                  was ever written.
