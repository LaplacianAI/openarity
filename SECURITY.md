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
- **An attachment's type comes from its bytes, and the sender's claim is never
  stored.** A file arriving on a webhook is sniffed after it is fetched and
  refused unless the sniffed type is on an allow list; the filename is stripped
  of directory components and control characters, and the size is measured
  rather than believed. A stored `media_type` that came from what the sender
  said, or a filename that still contains a path, is a bug. Note that an SVG
  carrying a script sniffs as `text/plain` and is therefore stored — it is
  inert only while served as the type recorded for it, so a read path that
  serves an attachment as anything but its recorded type, or without
  `X-Content-Type-Options: nosniff`, is a bug.
- **An attachment is served by the brain, never by a URL to the bucket.**
  `GET /teams/{id}/sessions/{sessionID}/attachments/{attachmentID}` returns the
  recorded `media_type` under `nosniff`, and `Content-Disposition: inline` only
  for `image/png`, `image/jpeg`, `image/gif`, `image/webp` and `text/plain` —
  each inert as itself. PDFs download, because a same-origin PDF runs
  JavaScript in every major viewer. A signed or public URL to an attachment is
  a bug, and so is an `inline` disposition on anything outside that list.
- **An attachment is readable by exactly whoever may read its session.** There
  is no separate attachment permission, so a file in somebody else's `direct`
  session answers the same 404 the session does, and an attachment id belonging
  to another conversation answers 404 rather than serving. An attachment
  readable by someone who cannot read the conversation it arrived in is a bug.
- **An attachment is encrypted before it leaves the process, under a key the
  object store never sees.** AES-256-GCM under a per-team key held in the
  secret store at `teams/<team_id>/attachments`, with the object's key as
  additional data so one attachment's bytes cannot be copied onto another's
  key. A plaintext attachment in the bucket is a bug, and so is a key in
  Postgres, in a log line or in an API response.
- **An unapproved sender's attachment is never fetched.** Attachments are
  resolved after the sender is checked, so a stranger cannot make the brain
  download and store bytes. An object written for a message that was dropped is
  a bug.
- **A message from an unrecognised sender is dropped, not stored.** Only their
  provider-side id and display name are queued for approval, bounded to 50 per
  channel — the text they sent is never written anywhere. A message body from
  an unapproved sender reaching the database is a bug.
- **A direct session is readable by its participant and by `session:read_all`.**
  Group and thread sessions belong to the team; a direct one is a private
  message to an agent, and every other member gets the same 404 they would get
  for a session that does not exist — a 403 there would confirm the
  conversation is real. A direct session whose participant was deleted has no
  participant, and is treated as nobody's rather than everybody's. A member
  reading somebody else's direct session, or any response that distinguishes a
  hidden one from an absent one, is a bug.

  A platform super admin reads every direct session, because `Can` answers yes
  for one before it looks at any role — the same rule as every other
  permission. That is deliberate rather than overlooked: an exception here
  would make super admin mean something different per permission, and a
  security property nobody can state is worse than a broad one everybody can.
  Deployments that cannot accept it should configure no super admins.
- **The brain stores a message exactly as it arrived; `oa` quotes it before
  printing.** Sanitising on write would put something nobody sent into the
  audit trail, so the text, the sender ref and the sender name are kept
  verbatim and escaped at the moment they reach a terminal — a terminal reads
  `\x1b[2J` as "clear the screen" and `\x1b]0;…\x07` as "set the window
  title". `-o json` is exempt on purpose: a script wants what arrived. An
  escape sequence, a C1 control or a bidi override surviving into `oa`'s table
  output is a bug.
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
