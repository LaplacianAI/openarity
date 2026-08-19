# OpenBao, single node, file storage, auto-unsealing.
#
# Vault would work here unchanged — OpenBao is API compatible down to the
# X-Vault-Token header — but Vault is BUSL-1.1, source-available and not
# OSI-licensed. OpenBao is the MPL-2.0 Linux Foundation fork.

# /openbao/file, not a path of our choosing: the image declares this one as a
# volume and owns it as uid 100. Docker creates any other mount point as root,
# and OpenBao — which does not run as root — fails to initialise with
# "failed to persist keyring: mkdir: permission denied".
storage "file" {
  path = "/openbao/file"
}

listener "tcp" {
  address     = "0.0.0.0:8200"
  tls_disable = 1
}

api_addr      = "http://127.0.0.1:8200"
ui            = true
disable_mlock = true
log_level     = "info"

# Auto-unseal from a key file on this host.
#
# Without this OpenBao seals on every restart, and every secret read fails
# until a human runs `bao operator unseal`. That presents as a total outage
# rather than as a sealed store, at whatever hour the host rebooted.
#
# The same stanza serves every rung of the ladder — only where the key lives
# changes, which is why this file is also the Kubernetes one. See OPENBAO.md.
#
# Rotation is n-1 to n: add previous_key_id and previous_key pointing at the
# old file, restart, then drop them once re-encryption has run.
seal "static" {
  current_key_id = "2026-08-19-1"
  current_key    = "file:///openbao/keys/unseal.key"
}
