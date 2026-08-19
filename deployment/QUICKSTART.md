# Quickstart

From nothing to a team created through the API, with the reason for every step
that usually goes wrong. [README.md](README.md) is the reference; this is the
path through it.

## The rule that explains most of the confusion

The brain compares a token's `iss` claim against `OPENARITY_OIDC_ISSUER` **as a
string**. Your browser and the brain must therefore reach the identity provider
at the *same* address.

A container's `127.0.0.1` is its own loopback, not your machine's. So a brain
running in Docker can never use `127.0.0.1:9000` for an authentik running
beside it — the address has to be one they both resolve to the same thing.

That leaves two setups that work. Mixing them is what breaks.

|                          | A — manual                 | B — compose                      |
| ------------------------ | -------------------------- | -------------------------------- |
| Brain runs               | `go run` on your machine   | in Docker                        |
| `OPENARITY_ENVIRONMENT`  | `development`              | `staging`                        |
| How you authenticate     | `OPENARITY_DEV_TOKEN`      | OIDC only, dev token empty       |
| authentik address        | `127.0.0.1:9000`           | your LAN address, `BIND_ADDR`    |
| `/docs` and `/openapi.yaml` | served                  | **not served** — development only |

Setup A is for writing code. Setup B is for testing what a deployment does.

---

## A — manual, in about a minute

No identity provider, no tokens to fetch. Use this unless you are specifically
testing authentication.

```sh
docker compose -f deployment/docker-compose.yml up -d      # postgres and friends

cd apps/brain
export OPENARITY_POSTGRES_DSN='postgres://postgres:postgres@127.0.0.1:5432/openarity?sslmode=disable'
export OPENARITY_DEV_TOKEN=letmein
export OPENARITY_SUPER_ADMINS=dev

go run ./cmd/brain migrate up
go run ./cmd/brain
```

Already running Postgres or Redis? Move the published ports instead of stopping
your own:

```sh
POSTGRES_PORT=15432 REDIS_PORT=16379 docker compose -f deployment/docker-compose.yml up -d
# and set the DSN to …@127.0.0.1:15432/…
```

Then:

```sh
curl -s -H 'Authorization: Bearer letmein' 127.0.0.1:21120/whoami
curl -s -H 'Authorization: Bearer letmein' -H 'Content-Type: application/json' \
     -d '{"name":"platform"}' 127.0.0.1:21120/teams
```

The dev principal's subject is `dev`, which is why `OPENARITY_SUPER_ADMINS=dev`
is what lets it create a team. Browse the API at <http://127.0.0.1:21120/docs>.

If you later turn OIDC on while keeping the dev token — a development machine
exercising the real login and still able to `curl` — the brain refuses to start
with `OPENARITY_SUPER_ADMINS=dev`. A super admin is named by subject, the dev
token's subject is fixed at `dev`, and an account called `dev` at your identity
provider would satisfy the same entry. Name yourself instead, by the `sub` your
provider issues.

---

## B — compose, with authentik

### 1. Choose the address

```sh
LAN_IP=$(ipconfig getifaddr en0)        # macOS
LAN_IP=$(hostname -I | cut -d' ' -f1)   # Linux
echo "$LAN_IP"
```

Every command below uses `$LAN_IP`, so run one of those first and keep the
shell. This publishes authentik and Postgres on your network while the stack
is up — fine at a desk, not on café wifi.

### 2. Write deployment/.env

```sh
cd deployment
cp .env.example .env
```

```sh
AUTHENTIK_SECRET_KEY=<openssl rand -base64 36>
AUTHENTIK_PG_PASS=<openssl rand -base64 36>

# Read only when authentik first initialises its database. The out-of-box
# setup flow returns 404 on current versions, so this is how the admin exists.
AUTHENTIK_BOOTSTRAP_PASSWORD=<choose one; this is a real login>
AUTHENTIK_BOOTSTRAP_TOKEN=<openssl rand -base64 36>
AUTHENTIK_BOOTSTRAP_EMAIL=you@example.com

BIND_ADDR=<your LAN address>
POSTGRES_PORT=15432
REDIS_PORT=16379

OPENARITY_ENVIRONMENT=staging
OPENARITY_OIDC_ENABLED=true
OPENARITY_OIDC_ISSUER=http://<your LAN address>:9000/application/o/openarity/
OPENARITY_OIDC_AUDIENCE=<filled in at step 4>
OPENARITY_SUPER_ADMINS=akadmin
OPENARITY_DEV_TOKEN=
```

`OPENARITY_DEV_TOKEN` must be **empty**. A shared password outside development
is refused outright, and the brain will not start with both set.

### 3. Start authentik

```sh
docker compose -f docker-compose.yml -f docker-compose.authentik.yml up -d
until curl -sf "$LAN_IP:9000/-/health/ready/" >/dev/null; do sleep 5; done
```

If authentik has already initialised once, the bootstrap variables are ignored.
Start over with `docker volume rm openarity_authentik-database`.

### 4. Create the OIDC provider and application

```sh
TOKEN=$(grep AUTHENTIK_BOOTSTRAP_TOKEN .env | cut -d= -f2)
HOST=http://$LAN_IP:9000

curl -s -H "Authorization: Bearer $TOKEN" "$HOST/api/v3/flows/instances/?designation=authorization"
curl -s -H "Authorization: Bearer $TOKEN" "$HOST/api/v3/flows/instances/?designation=invalidation"
curl -s -H "Authorization: Bearer $TOKEN" "$HOST/api/v3/crypto/certificatekeypairs/"
```

Take the `pk` of `default-provider-authorization-implicit-consent`, of
`default-provider-invalidation-flow`, and of the self-signed certificate:

```sh
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  "$HOST/api/v3/providers/oauth2/" -d '{
    "name": "openarity",
    "authorization_flow": "<authorization pk>",
    "invalidation_flow": "<invalidation pk>",
    "client_type": "public",
    "grant_types": [
      "authorization_code",
      "refresh_token",
      "urn:ietf:params:oauth:grant-type:device_code"
    ],
    "signing_key": "<certificate pk>",
    "sub_mode": "user_username",
    "redirect_uris": [{"matching_mode": "strict", "url": "http://localhost:8080/callback"}]
  }'

curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  "$HOST/api/v3/core/applications/" \
  -d '{"name":"Openarity","slug":"openarity","provider":<provider pk>}'
```

Four fields decide whether login works at all:

- **`client_type` is `public`.** `oa` ships to other people's machines, so a
  client secret in it would not be secret. The person approving in the browser
  is what stands in for one.
- **`grant_types` defaults to `[]`**, and an empty list refuses every login with
  "the request is otherwise malformed". The real reason,
  `Invalid grant_type for provider`, appears only in authentik's container log.
  `oa login` needs the device code URN *and* `refresh_token` — without the
  second, the login works and dies an hour later with nothing to renew from.
- **`invalidation_flow` is required** and cannot be null.
- **`sub_mode`** decides what `sub` holds. The default `hashed_user_id` is an
  opaque hash, so `OPENARITY_SUPER_ADMINS=akadmin` never matches and every
  privileged call returns 403. `user_username` puts the username there.

Put the returned `client_id` in `OPENARITY_OIDC_AUDIENCE`, then check discovery:

```sh
curl -s "$HOST/application/o/openarity/.well-known/openid-configuration" | jq .issuer
# must equal OPENARITY_OIDC_ISSUER exactly, trailing slash included
```

### 5. Give the device flow somewhere to enter the code

`oa login` uses [RFC 8628](https://datatracker.ietf.org/doc/html/rfc8628), and
**authentik ships no flow for it.** Without this step the provider is configured
correctly, `oa login` prints a code, and the address it prints is a 404. Nothing
in either log says why.

In the web UI, because this is two objects and a link between them:

1. **Flows and Stages → Flows → Create**, with
   **Designation: `Stage Configuration`** and
   **Authentication: `Require authentication`**. Name it `device-code`.
2. **System → Brands →** edit the active brand → **Default code flow** → the
   flow you just made.

"Require authentication" is what makes the sign-in page appear first; without
it the code page is reached by an anonymous visitor and has nobody to attach
the approval to. The brand field is the one that is easy to miss — it is far
from the provider, and the provider looks complete without it.

### 6. Start the brain

```sh
docker compose -f docker-compose.yml -f docker-compose.authentik.yml --profile brain up -d
docker compose -f docker-compose.yml logs brain | tail -3
```

Expect `Listening on API bind`. Anything else, read the log before retrying —
a failing brain restarts forever and the reason scrolls past.

The `migrate` service runs to completion first and the brain waits for it, so
the schema is always applied before anything serves.

### 7. Log in

Logging into the authentik dashboard is **not** enough: that is a browser
cookie, and the brain wants a JWT from the OIDC flow. `oa login` runs that flow.

```sh
cd ../apps/cli && make install && cd -

oa context create staging --server "http://$LAN_IP:21120"
oa login
# open  http://<your LAN address>:9000/device
# code  WXYZ-ABCD
# waiting for approval, up to 5m0s…
```

Open the address, sign in as `akadmin`, enter the code, approve. `oa` stores the
result in your keychain under the context name, and renews it from then on
without asking again.

`oa` asks the brain at `/auth/config` which issuer and client id to use, so
nothing here is configured twice. If it reports that the server has no identity
provider, the brain is running with `OPENARITY_OIDC_ENABLED=false`.

### 8. Who does the brain think you are?

```sh
oa whoami
```

```text
kind     user
subject  akadmin
issuer   http://<your LAN address>:9000/application/o/openarity/
teams    none
```

`teams none` is correct. A first login creates the user row and grants nothing —
registration is not a privilege.

### 9. Create a team

```sh
oa teams create platform
```

A 403 means the `subject` from step 8 is not in `OPENARITY_SUPER_ADMINS`. Copy
it in exactly and restart the brain. It is the *subject*, not the email address.

### 10. Add its first member

A new team has nobody in it, and only a super admin can put the first person
there. Both arguments take a name — the team's, and the subject the person
signs in as:

```sh
oa teams members add platform akadmin --role admin
```

No ids anywhere. The team name is resolved against `oa teams list`, and the
subject is sent to the brain, which resolves it while adding. That second part
matters: adding somebody you can name needs `membership:write` in that team and
nothing else — never permission to read the whole directory.

They must have logged in at least once. A user row is created on first sight
and never synced from authentik, so somebody who has an account there but has
never run `oa login` cannot be added yet:

```sh
oa users list                    # everyone who has logged in
oa users list akadmin            # one exact subject
```

Two answers you may get instead of a 204:

- **`no user has that subject`** — they have not logged in yet.
- **`2 users have that subject`** — two identity providers issued the same
  name. The reply lists the ids; retry with one of them in place of the
  subject. Only possible once a second issuer is configured.

The roles that exist are `admin` and `member`, and they are rows: anything else
comes back as a 400 from a rejected foreign key, not as a CLI error.

From then on that person manages the team's membership themselves, without
being a super admin.

---

## Federating Google

authentik federates; the brain never learns about Google. It keeps issuing its
own tokens, so `OPENARITY_OIDC_ISSUER` and the audience do not change.

**Google rejects `http` redirect URIs except on `localhost`, and rejects private
IP addresses outright** — a LAN address returns `device_id and device_name are
required for private IP`. Two ways out:

- **`localhost`** — setup A, authentik on `127.0.0.1:9000` with the brain on the
  host. Simplest, and it is why setup A exists.
- **A tunnel with a stable hostname** — needed if the brain is in Docker. Point
  it at authentik and make `OPENARITY_OIDC_ISSUER`, the authentik callback URL
  and the Google client all use the tunnel's address:

  ```sh
  ngrok http "$LAN_IP:9000"        # a reserved domain, not a random one
  ```

  **Do not pass `--host-header=rewrite`.** authentik builds `iss` and its
  callback URLs from the `Host` header, so rewriting it makes discovery
  advertise `http://<your LAN address>:9000/...` while the browser is on the tunnel —
  and the brain then rejects every token for a mismatched issuer.

  A changed tunnel address means `OPENARITY_OIDC_ISSUER` changes, and that
  is a one-way door: users are keyed by `(issuer, subject)`, so every existing
  user row is orphaned along with its team memberships. Reserve the hostname.

In Google Cloud, create an OAuth client (Web application) whose redirect URI is
authentik's callback at whichever address you chose:

```text
http://localhost:9000/source/oauth/callback/google/
https://<your-tunnel>/source/oauth/callback/google/
```

Google takes a few minutes to publish a change to that list; a
`redirect_uri_mismatch` immediately after editing is usually just early.

Then:

```sh
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  "$HOST/api/v3/sources/oauth/" -d '{
    "name": "Google", "slug": "google", "provider_type": "google",
    "consumer_key": "<google client id>", "consumer_secret": "<google client secret>",
    "authentication_flow": "<default-source-authentication pk>",
    "enrollment_flow": "<default-source-enrollment pk>",
    "user_matching_mode": "email_link"
  }'
```

Creating a source does not show it anywhere. Attach it to the login page:

```sh
SOURCE=$(curl -s -H "Authorization: Bearer $TOKEN" "$HOST/api/v3/sources/oauth/?slug=google" \
  | jq -r '.results[0].pk')
STAGE=$(curl -s -H "Authorization: Bearer $TOKEN" "$HOST/api/v3/stages/identification/" \
  | jq -r '.results[0].pk')

curl -s -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  "$HOST/api/v3/stages/identification/$STAGE/" -d "{\"sources\":[\"$SOURCE\"]}"
```

`email_link` attaches a Google login to an existing authentik user with the
same address rather than creating a second account.

---

## When it will not work

| Symptom | Cause |
| --- | --- |
| Brain restarts forever | `DEV_TOKEN` set while `ENVIRONMENT` is not `development` |
| `no authentication configured` | neither OIDC nor a dev token is set |
| Fails at boot fetching discovery | the issuer is unreachable from where the brain runs |
| 401 with a token you just got | the token's `iss` is not `OPENARITY_OIDC_ISSUER`, usually `127.0.0.1` against a LAN address |
| 401 on the dev token | it is empty, or the environment is not development |
| Login is "otherwise malformed" | the provider's `grant_types` is empty |
| `oa login`: `Client authentication failed` | the provider is missing the device code grant, or is not `public` |
| The device page 404s | no flow with designation `Stage Configuration`, or the brand's **Default code flow** is unset |
| Logged in, then logged out an hour later | `refresh_token` is not in the provider's `grant_types` |
| `oa`: connection refused to the brain | the brain crash-looped; it fetches discovery at boot and exits if the IdP is down |
| Everyone lost their teams | `OPENARITY_OIDC_ISSUER` changed — users are keyed by `(issuer, subject)` |
| 403 on `POST /teams` | your `subject` is not in `OPENARITY_SUPER_ADMINS` |
| 404 for a team that exists | you are not a member and not a super admin — deliberate, a 403 would confirm the id |
| `/docs` 404s | the environment is not `development` |
| Ports already in use | set `POSTGRES_PORT`, `REDIS_PORT`, and the rest |

Read the brain's log before changing anything. It refuses to start on a bad
configuration rather than serving in a half-configured state, and the reason is
always on the first lines.

## What is not reproducible yet

The authentik provider, application, device-code flow and brand setting created
here live only in that container's database. Someone cloning the repository
starts from nothing. authentik's answer is blueprints — YAML applied at startup
— and moving this configuration into one is the right next step before anyone
else uses the stack.

Two things worth knowing before you build on this:

- **`OPENARITY_OIDC_ISSUER` is part of user identity.** The users table is
  unique on `(issuer, subject)`, so changing the issuer creates a second row for
  every person and silently orphans their memberships. Nothing warns. Treat it
  as a one-way door until there is a remap step.
- **The brain fetches OIDC discovery at boot and exits if it fails.** Start
  authentik first, and expect a restart loop if it is down when the brain rolls.
