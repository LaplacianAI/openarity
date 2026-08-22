# Security Policy

## Supported versions

Openarity has not had a tagged release yet. Until it does, only the current
`main` is supported, and fixes land there.

| Version | Supported |
| ------- | --------- |
| `main`  | yes       |

## Reporting a vulnerability

**Do not open a public issue for a security problem.**

Report it privately through GitHub:

1. Go to the [Security tab](https://github.com/LaplacianAI/openarity/security)
2. Click **Report a vulnerability**

That opens a private advisory visible only to the maintainers. If private
reporting is unavailable to you, email `shrijeethsuresh@gmail.com` instead.

Please include:

- what the problem is and what an attacker gains
- the affected version or commit
- steps to reproduce, ideally a minimal case
- any known workaround

## What to expect

| Stage                | Target              |
| -------------------- | ------------------- |
| Acknowledgement      | within 72 hours     |
| Initial assessment   | within 7 days       |
| Fix or mitigation    | depends on severity |

You will be credited in the advisory unless you ask not to be. Please give us a
chance to ship a fix before disclosing publicly — 90 days is the usual window,
and we will tell you if we need longer.

## Scope

In scope: anything in this repository — the `brain` service, its dependencies
as we pin them, the migrations, the CI configuration, and the deployment
manifests once they exist.

Out of scope: findings against your own deployment's configuration (an exposed
port, a weak Postgres password, a Vault instance left unsealed), and reports
from automated scanners with no demonstrated impact.

## Things worth knowing before you report

These are deliberate, documented decisions rather than oversights:

- **Secrets are never stored in Postgres or the graph.** Rows hold Vault path
  references only. A row containing a credential is a bug — report it.
- **Bind addresses default to loopback.** `OPENARITY_API_BIND` and
  `OPENARITY_WEBHOOK_BIND` default to `127.0.0.1`. Binding them to `0.0.0.0` is
  a deployment choice, not a default.
- **The health and readiness endpoints are unauthenticated by design**, and
  neither returns internal detail — `/readyz` logs the underlying error and
  returns a bare `not ready`. If either leaks anything about the database,
  that is a bug. Every endpoint on the API listener requires a bearer token;
  one that is reachable without one is a bug.
- **The webhook listener is unauthenticated by design and authenticates by
  request signature instead.** `/hooks/{provider}/{channel_id}` proves that a
  delivery came from someone holding that one channel's signing secret; there
  is no caller, no session and no permission to check. A delivery accepted
  without a valid signature is a bug, and so is one accepted with a signature
  belonging to a different channel or a different provider.
- **The gateway answers 403 identically for a bad signature, an unknown
  channel, a deleted channel and a channel belonging to another provider.** The
  status and the body are the same in all four cases, because any difference
  confirms which channel ids are real. The request body is also read before the
  channel is looked up, so how long a refusal takes does not depend on whether
  the channel exists — though the path is not constant-time overall, and we do
  not claim it is. A difference in status or body between those four is a bug.
- **A channel's signing secret is shown once, when the channel is created.** It
  is written to the secret store, and no endpoint reads it back — the API
  returns it only in the response that created it. An API response, log line or
  error message containing one afterwards is a bug.
- **A message from an unrecognised sender is dropped, not stored.** Only their
  provider-side id and display name are queued for approval, bounded to 50 per
  channel — the text they sent is never written anywhere. A message body from
  an unapproved sender reaching the database is a bug.
- **The service refuses to start with no authentication configured.** It fails
  closed rather than serving an open API, so a missing setting is an outage
  rather than an exposure.
- **`OPENARITY_DEV_TOKEN` is a development-only shared secret**, compared in
  constant time. It exists so a laptop does not need an identity provider.
  Using it in a deployment is a misconfiguration, not a vulnerability in the
  service — but tell us if it is ever accepted while OIDC is enabled.
- **A user row is created on first successful authentication** and carries no
  permissions until an administrator grants them. Registration is therefore
  not a privilege.
- **Asking for a team you are not in returns 404, not 403.** The status and
  body are identical to a team that does not exist, because a 403 would
  confirm the id is real. If the two ever differ — in status, body, or timing
  — that is a bug worth reporting.
- **A failed permission lookup is a 500, never a 403.** Unknown is not denied,
  and a database blip must not read as a permissions decision.
- **`oa` stores a login in the OS keychain**, and in a `0600` file beside the
  config only where there is no keychain or the token does not fit one. It is
  never written to `config.yaml`, which is the file people sync and paste into
  issues. A credential appearing there is a bug — report it.
- **`oa` never prints a credential.** Not truncated, not masked, not in an error
  or a `--output json` document. `oa config show` reports a length and a
  location. Anything that echoes a token value is a bug.
- **`oa` is a public OAuth client and holds no client secret.** It ships to
  other people's machines, so a secret in it would not be one. Approval by the
  human at the identity provider is what stands in for it, which is why every
  login needs a browser.
- **The device flow can be phished, and that is inherent to it.** Someone who
  talks you into approving a code they generated gets a token. `oa` prints the
  code and asks you to type it rather than opening a browser silently, so an
  approval you did not start looks like one — but no client-side change can
  remove this. Do not approve a code you were sent.
- **Renewal is trusted less than it looks.** A stored login is discarded only
  when the provider says the grant itself is invalid; a 500 or an outage leaves
  it alone. If a transient provider failure ever deletes a credential, that is a
  bug — it would log out an entire fleet during a restart.
