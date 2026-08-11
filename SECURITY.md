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
  that is a bug. Every other endpoint requires a bearer token; one that is
  reachable without one is a bug.
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
