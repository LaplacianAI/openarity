---
title: Configuration
---

Configuration is environment-only, prefixed `OPENARITY_`, and validated at
startup. Credentials are redacted whenever config is printed.

Every setting has a working default except authentication, which has none on
purpose: the service refuses to start rather than serve an open API.

The full table — every variable, its default, and whether it is used yet — lives
in the [repository README](https://github.com/LaplacianAI/openarity#configuration).
It is kept there rather than copied here so there is one copy to be wrong.

The ports are unusual enough to be worth repeating:

| Listener | Default          | Serves                          |
| -------- | ---------------- | ------------------------------- |
| API      | `127.0.0.1:21120` | everything a caller authenticates for |
| Webhook  | `127.0.0.1:21121` | inbound channel deliveries      |

One entry, Redis, is present and marked *not used yet*: it is configured
ahead of the code that will read it, and the README says so.
