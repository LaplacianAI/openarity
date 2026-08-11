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

---

## B — compose, with authentik

### 1. Choose the address

```sh
ipconfig getifaddr en0        # macOS, e.g. 192.168.1.4
hostname -I | cut -d' ' -f1   # Linux
```

Everything below uses `192.168.1.4`; substitute yours. This publishes authentik
and Postgres on your network while the stack is up — fine at a desk, not on
café wifi.

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

BIND_ADDR=192.168.1.4
POSTGRES_PORT=15432
REDIS_PORT=16379

OPENARITY_ENVIRONMENT=staging
OPENARITY_OIDC_ENABLED=true
OPENARITY_OIDC_ISSUER=http://192.168.1.4:9000/application/o/openarity/
OPENARITY_OIDC_AUDIENCE=<filled in at step 4>
OPENARITY_SUPER_ADMINS=akadmin
OPENARITY_DEV_TOKEN=
```

`OPENARITY_DEV_TOKEN` must be **empty**. A shared password outside development
is refused outright, and the brain will not start with both set.

### 3. Start authentik

```sh
docker compose -f docker-compose.yml -f docker-compose.authentik.yml up -d
until curl -sf 192.168.1.4:9000/-/health/ready/ >/dev/null; do sleep 5; done
```

If authentik has already initialised once, the bootstrap variables are ignored.
Start over with `docker volume rm openarity_authentik-database`.

### 4. Create the OIDC provider and application

```sh
TOKEN=$(grep AUTHENTIK_BOOTSTRAP_TOKEN .env | cut -d= -f2)
HOST=http://192.168.1.4:9000

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
    "grant_types": ["authorization_code", "refresh_token"],
    "signing_key": "<certificate pk>",
    "sub_mode": "user_username",
    "redirect_uris": [{"matching_mode": "strict", "url": "http://localhost:8080/callback"}]
  }'

curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  "$HOST/api/v3/core/applications/" \
  -d '{"name":"Openarity","slug":"openarity","provider":<provider pk>}'
```

Three fields decide whether login works at all:

- **`grant_types` defaults to `[]`**, and an empty list refuses every login with
  "the request is otherwise malformed". The real reason,
  `Invalid grant_type for provider`, appears only in authentik's container log.
- **`invalidation_flow` is required** and cannot be null.
- **`sub_mode`** decides what `sub` holds. The default `hashed_user_id` is an
  opaque hash, so `OPENARITY_SUPER_ADMINS=akadmin` never matches and every
  privileged call returns 403. `user_username` puts the username there.

Put the returned `client_id` in `OPENARITY_OIDC_AUDIENCE`, then check discovery:

```sh
curl -s "$HOST/application/o/openarity/.well-known/openid-configuration" | jq .issuer
# must equal OPENARITY_OIDC_ISSUER exactly, trailing slash included
```

### 5. Start the brain

```sh
docker compose -f docker-compose.yml -f docker-compose.authentik.yml --profile brain up -d
docker compose -f docker-compose.yml logs brain | tail -3
```

Expect `Listening on API bind`. Anything else, read the log before retrying —
a failing brain restarts forever and the reason scrolls past.

The `migrate` service runs to completion first and the brain waits for it, so
the schema is always applied before anything serves.

### 6. Get a token

Logging into the authentik dashboard is **not** enough: that is a browser
cookie, and the brain wants a JWT from the OIDC flow. Run the authorization
code flow. Until the CLI exists, any OIDC client will do, or:

```sh
python3 - <<'PY'
import base64, hashlib, http.server, json, secrets, urllib.parse, urllib.request

CLIENT_ID = "<your client_id>"
HOST = "http://192.168.1.4:9000"
REDIRECT = "http://localhost:8080/callback"

verifier = base64.urlsafe_b64encode(secrets.token_bytes(48)).decode().rstrip("=")
challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).decode().rstrip("=")

print(HOST + "/application/o/authorize/?" + urllib.parse.urlencode({
    "client_id": CLIENT_ID, "response_type": "code", "scope": "openid profile email",
    "redirect_uri": REDIRECT, "state": "qa",
    "code_challenge": challenge, "code_challenge_method": "S256"}), flush=True)

class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        code = urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query)["code"][0]
        body = urllib.parse.urlencode({
            "grant_type": "authorization_code", "code": code, "redirect_uri": REDIRECT,
            "client_id": CLIENT_ID, "code_verifier": verifier}).encode()
        req = urllib.request.Request(HOST + "/application/o/token/", data=body, method="POST")
        req.add_header("Content-Type", "application/x-www-form-urlencoded")
        with urllib.request.urlopen(req) as r:
            open("/tmp/openarity-token.txt", "w").write(json.load(r)["access_token"])
        self.send_response(200); self.end_headers()
        self.wfile.write(b"token written to /tmp/openarity-token.txt")
    def log_message(self, *a): pass

http.server.HTTPServer(("127.0.0.1", 8080), H).handle_request()
PY
```

The client is **public**, so authentik requires PKCE — that is what
`code_challenge` is. Open the printed URL, log in as `akadmin`, and the token
lands in the file.

### 7. Who does the brain think you are?

```sh
TOKEN=$(cat /tmp/openarity-token.txt)
curl -s -H "Authorization: Bearer $TOKEN" http://192.168.1.4:21120/whoami
```

```json
{"kind":"user","issuer":"http://192.168.1.4:9000/application/o/openarity/","subject":"akadmin","teams":[]}
```

`teams: []` is correct. A first login creates the user row and grants nothing —
registration is not a privilege.

### 8. Create a team

```sh
curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
     -d '{"name":"platform"}' http://192.168.1.4:21120/teams
```

A 403 means the `subject` from step 7 is not in `OPENARITY_SUPER_ADMINS`. Copy
it in exactly and restart the brain.

### 9. Add its first member

A new team has nobody in it, and only a super admin can put the first person
there. `POST /members` needs a user id, and no endpoint exposes one yet:

```sh
docker compose -f docker-compose.yml exec postgres \
  psql -U postgres -d openarity -Atc "select id, subject from users"

curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"user_id":"<uuid>","role":"admin"}' \
  http://192.168.1.4:21120/teams/<team id>/members
# 204 No Content
```

From then on that person manages the team's membership themselves, without
being a super admin.

---

## Federating Google

authentik federates; the brain never learns about Google. It keeps issuing its
own tokens, so `OPENARITY_OIDC_ISSUER` and the audience do not change.

**Google rejects `http` redirect URIs except on `localhost`.** A LAN address
therefore cannot be used, so federation testing needs setup A — authentik on
`127.0.0.1:9000` with the brain on the host — or TLS in front of authentik.

In Google Cloud, create an OAuth client (Web application) with:

```text
http://localhost:9000/source/oauth/callback/google/
```

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
| 403 on `POST /teams` | your `subject` is not in `OPENARITY_SUPER_ADMINS` |
| 404 for a team that exists | you are not a member and not a super admin — deliberate, a 403 would confirm the id |
| `/docs` 404s | the environment is not `development` |
| Ports already in use | set `POSTGRES_PORT`, `REDIS_PORT`, and the rest |

Read the brain's log before changing anything. It refuses to start on a bad
configuration rather than serving in a half-configured state, and the reason is
always on the first lines.

## What is not reproducible yet

The authentik provider and application created here live only in that
container's database. Someone cloning the repository starts from nothing.
authentik's answer is blueprints — YAML applied at startup — and moving this
configuration into one is the right next step before anyone else uses the stack.
