---
title: Install
weight: 4
next: /docs/install/personal
---

Three ways in, and the right one depends on who else is using it.

{{< cards >}}
  {{< card link="personal" title="One machine" icon="desktop-computer" subtitle="Your laptop or a home server. No Docker, everything on loopback, one command." >}}
  {{< card link="enterprise" title="A deployment" icon="office-building" subtitle="Compose or Kubernetes, an identity provider you already run, and a worker that must not be forgotten." >}}
{{< /cards >}}

Working on Openarity itself is a third path and a different shape — the brain
from source against a Postgres in Docker. That is the
[platform quick start](/docs/platform/quick-start).

## Choosing

|                        | One machine                    | A deployment                          |
| ---------------------- | ------------------------------ | ------------------------------------- |
| Who signs in           | you                            | anyone your identity provider knows   |
| Identity provider      | dex, configured for you        | yours — authentik, Keycloak, Okta     |
| Postgres               | downloaded and supervised      | yours                                 |
| Reachable from         | `127.0.0.1` only               | wherever you publish it               |
| Upgrades               | run setup again                | roll the image                        |
| Needs Docker           | no                             | yes, or Kubernetes                    |

A personal install binds to loopback and nothing else. That is not a default
you can move — the brain refuses a development token sent to any address that
is not loopback, and a personal install has no identity provider of its own to
put in front of it.

{{< callout type="warning" >}}
There is no release yet, so neither path installs from published binaries. Both
pages say exactly what you have to build first, and that instruction disappears
when the first release does.
{{< /callout >}}
