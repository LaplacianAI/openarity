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

| Stage                | Target             |
| -------------------- | ------------------ |
| Acknowledgement      | within 72 hours    |
| Initial assessment   | within 7 days      |
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
  that is a bug.
