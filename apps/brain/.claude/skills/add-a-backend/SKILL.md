---
name: add-a-backend
description: Add a new implementation behind one of the brain's storage ports — a secret store or an object store. Covers the port, the adapter package, the enum value that names it, the per-backend validation, the wiring in cmd/brain, and the tests that catch a switch case falling through. Use whenever a new provider must be selectable by name.
---

# Add a backend to `internal/secrets` or `internal/objects`

Two ports, each with a package per implementation:

```text
internal/secrets/secrets.go            Store, Writer, Creator, Prober, Path, TeamPath
  datakeys.go                          DataKeys — a team's key, created once
  openbao/openbao.go                   package openbao,    New()
  static/static.go                     package static,     New()
internal/objects/objects.go            Store, Writer, ErrNotFound, TeamPrefix, InTeam
  encrypt.go                           Encrypted — a layer, not a backend
  s3/s3.go                             package s3,         New(Config)
  filesystem/filesystem.go             package filesystem, New(root)
  inmemory/inmemory.go                 package inmemory,   New()
```

The port file holds the interfaces and the key-shaping helpers and imports
nothing from its adapters. Each adapter imports the port. Nothing imports an
adapter except `cmd/brain`.

## This is not the gateway's pattern, and the difference is deliberate

`internal/gateway` picks a provider **per request**, from a registry keyed by
`Name()`, with many alive at once. A storage backend is chosen **once at boot**
and exactly one exists. So it is an enum plus a switch, not a registry. Do not
"make it consistent" — a registry here would let a typo select nothing and the
process start anyway, which is the failure this design exists to prevent.

## Step 0 — is it a backend at all?

A subdirectory of a port means **one package per backend**. The test is whether
somebody selects it by name:

```sh
OPENARITY_OBJECTS_BACKEND=s3          # a backend
OPENARITY_OBJECTS_BACKEND=encrypted   # not a thing, and never will be
```

Encryption wraps whichever backend was selected, so it is a file in the port
package — `internal/objects/encrypt.go` — and not a fourth directory beside
`s3`. Filing it as an adapter would have said it was a fourth choice.

Two consequences of getting this right, both mechanical:

- A layer inside the port can take the port's own interfaces and assert for
  the ones it needs. `NewEncrypted(inner Store, keys KeySource)` needs no
  import and no re-export. A wrapper outside the package it wraps grows a
  shadow copy of that package's interfaces within a week.
- A layer may declare an interface for what it needs, but must not import a
  subsystem to name a type. `objects` declares a one-method `KeySource`; the
  implementation lives in `secrets`, because a data key is a secret-store
  concern and the port should not depend on OpenBao to compile.

If it *is* a backend, carry on.

## Step 1 — decide whether you are adding an adapter or changing the port

Adding a method to the port changes every adapter. Before doing it, check that
the operation is one that every implementation can actually serve.

`objects` has three methods because whole-object put, get and delete are the
intersection every S3 clone implements. Multipart, ranged reads and lifecycle
rules are where compatibility stops being reliable. A port method that only
one backend can honour belongs on that backend's own type, not on the port.

If two backends are genuinely the same protocol, they do not need two
adapters — see step 3 on giving one adapter two names.

## Step 2 — the adapter package

```text
internal/objects/<name>/<name>.go
internal/objects/<name>/<name>_test.go
internal/objects/<name>/integration_test.go   if it talks to a real service
```

- Package name is the directory name. `package s3`, not `package s3store`.
- The concrete type is **unexported** and called `store`. Callers hold the
  port's interface; the type name never appears at a call site, so
  `objects.Store` returned from `s3.New` reads better than `*s3.S3Store`.
- `New` returns the interface, or `(interface, error)` if construction can
  fail. It must fail on a missing endpoint rather than at first use.
- Never take a credential chain that can hang. `aws-sdk-go-v2/config` looks
  like "the config types" and is actually the credential chain: it pulls in
  sso, ssooidc, sts, signin and imds, and turns a missing credential into a
  timeout against `169.254.169.254` on any non-EC2 host. Static credentials
  only.

Map the backend's "absent" to the port's sentinel, and map it more than one
way. An object store's missing key arrives as a typed error from AWS and as a
bare 404 from several clones:

```go
var noSuchKey *types.NoSuchKey
if errors.As(err, &noSuchKey) { return fmt.Errorf("%w: %s", objects.ErrNotFound, key) }

var resp interface{ HTTPStatusCode() int }
if errors.As(err, &resp) && resp.HTTPStatusCode() == http.StatusNotFound { ... }
```

## Step 3 — the enum value

`internal/config/enums.go`. Add the constant, add it to `UnmarshalText`, and
add it to the list the error prints. That list is the whole user interface for
a typo:

```go
return fmt.Errorf("invalid objects backend %q: want memory, filesystem or s3", s)
```

An adapter may also implement an **optional** interface, which is how a
capability that not every backend can honour gets expressed. `secrets.Prober`
is one; `secrets.Creator` — write only if absent — is the other, and it is
optional rather than part of `Writer` for a reason worth copying: a backend
without the primitive would have to fake it, and a faked compare-and-swap is
exactly the silent race the interface exists to prevent. The consumer type
asserts at construction and refuses to build, so "this backend cannot do it"
is a startup error naming the backend.

**Two names for one adapter is allowed and sometimes right.** `openbao` and
`vault` both build the OpenBao adapter: OpenBao is the fork of the last
MPL-2.0 Vault, so the API and KV v2 semantics are unchanged. The names are
separate so the day they diverge, nobody has to edit their configuration. Add
the alias when the two are independently developed — not merely when two words
mean the same thing today.

## Step 4 — validation, per backend

`internal/config/validate.go`. Each backend requires **only its own**
settings, and a setting belonging to an unselected backend is ignored rather
than refused — otherwise switching backends becomes a two-step change.

```go
switch c.ObjectsBackend {
case ObjectsBackendS3:
	if c.ObjectsEndpoint == "" { ... }
case ObjectsBackendFilesystem:
	// Nothing to require. OBJECTS_PATH carries a default.
case ObjectsBackendMemory:
	// Nothing to configure, and refused outside development above.
}
```

Empty cases, not `default:` — `exhaustive` runs with
`default-signifies-exhaustive: false`, so a `default` would stop it telling
you about the next backend somebody adds.

Two traps:

- **A field with an `envDefault` can never be empty.** An env var set to the
  empty string falls back to its default; measured, not assumed. So a
  "required" check on such a field is unreachable code that reads as
  protection. Delete it rather than testing it.
- **An in-memory backend must be refused outside development.** `static` and
  `memory` lose everything on restart with no error at either end. A brain
  that starts and silently loses every attachment is worse than one that does
  not start.

## Step 5 — wire it in `cmd/brain`, and check that `serve` calls it

`cmd/brain/objects.go` and `cmd/brain/secrets.go` each hold one constructor
switching on the enum. A backend that loses data warns at `WARN` and says so
in words an operator will act on — `lost on restart`, not `in-memory store`.
A durable backend says nothing: a warning printed on every start is a warning
nobody reads by the third deploy.

**Then follow the constructor upward until you reach `serve`.** `newObjectStore`
was written with its adapters, its enum, its validation and its wiring tests
across four pull requests, and `serve.go` never called it. Everything was
green the whole time: `unused` assumes an exported identifier has a caller in
another package, and a `main`-package constructor exercised only by its own
tests looks exactly like one in use.

```sh
grep -rn "newObjectStore" cmd/brain/ | grep -v _test.go
```

One line — the definition — means the subsystem does not exist at runtime. The
wiring tests in step 6 all pass in that state, because they call the
constructor themselves.

This is the same failure the `add-middleware` skill's step 4 exists for, and it
has now happened in two packages. The general form: **a test that calls the
thing under test cannot tell you whether production does.** For anything that
must run at boot, the assertion belongs on the boot path — build the real
`serve` dependencies and check the subsystem is among them.

## Step 6 — the tests that matter

Adapter tests, in its own package: round trip, missing key, delete is
idempotent, overwrite replaces, and **binary content survives** — 256 bytes
`0x00`–`0xff`. An adapter that mangles a NUL or a high byte passes every
string-based test, and attachments are photographs.

If the adapter talks to a real service, add `integration_test.go` that skips
unless its env var is set, and assert against the real thing what a stub
cannot prove: what *this* store returns for an absent key. `make objects`
starts MinIO for the object store; `make bao` for the secret store.

Then the wiring tests in `cmd/brain`, which are the ones that catch the real
failure:

1. **Every name produces a working store** — otherwise naming it is theatre.
2. **Different names produce different stores.** A case that silently falls
   through to the fallback passes test 1 completely. Compare the *package* the
   type came from; the concrete types are all unexported and all called
   `store`.
3. **A misconfigured backend fails at startup**, not at first attachment.
4. **The lossy backend warns; the durable ones are silent.**
5. **Something on the boot path constructs it.** The four above pass against a
   backend nothing uses. This is the one that fails when step 5 was skipped.

And in `internal/config`: every value parses, an unknown value is refused with
the bad value *and* the valid list in the message, and the backend name
appears in `Config.String()` — it is not a secret and it answers "why are my
attachments not appearing" before anything else does.

## Step 7 — the documentation, which is part of the change

- `README.md` — the secrets and object storage table. Every variable, its
  default copied from the struct tag, not typed from memory.
- `apps/brain/.env.example` — a commented block showing the new backend.
- `deployment/` — a compose overlay and a `make` target if it needs a service,
  following `docker-compose.objects.yml`. Publish on a `1`-prefixed host port
  (15432, 16379, 19000): the stack must be able to run all its own overlays at
  once, and MinIO's 9000 collides with authentik.
- `apps/brain/CLAUDE.md` — one line, if the choice changes how the brain is
  run locally.

## Known rough edge

`asWriter` is copy-pasted into all three `objects` adapter test files, and the
contract tests around it are near-identical. The gateway solved the same
problem with `internal/gateway/providertest`. If a fourth object adapter
arrives, extract `internal/objects/objectstest` first — three copies is the
point at which the copies start disagreeing.
