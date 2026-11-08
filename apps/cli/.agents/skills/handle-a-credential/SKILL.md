---
name: handle-a-credential
description: Anything that reads, writes, renews, prints or moves a login — a new credential store, a change to oa login or oa logout, a command that sends a token, a context lifecycle operation, or an OAuth grant. Covers the credential/store split, the keychain size limit and its fallback, which refresh failures are fatal, and the four tests every one of these owes. Use it every time; a credential wired into three of the four places leaks or silently logs someone out.
---

# Handle a credential

A login is a `credential.Credential` — a token, a refresh token, an expiry —
keyed by context name. Everything about how one is stored, sent, renewed and
discarded is in two packages and one method, and the failures here are all
quiet: a leak that nothing prints, a logout that nobody asked for, a renewal
that works once.

## Step 0 — where the pieces are

```text
internal/credential/         Credential, IsZero, IsExpired, CanRefresh, Store
  store/file.go              credentials.yaml at 0600, temp-file-then-rename
  store/keyring.go           the OS keychain, and ErrTooBig
  store/store.go             Open — probes the keychain, returns the fallback
internal/auth/oidc.go        discovery, post, exchange
internal/auth/device.go      StartDevice, WaitForToken
internal/auth/refresh.go     Refresh, ErrRefreshRejected
internal/cli/options.go      Credentials, credential, SaveLogin, renewIfExpired
internal/command/login/      the prompt and the wait
internal/command/logout/     one Delete
```

`internal/credential` imports `time` and nothing else. That is deliberate and
load-bearing: `config` embeds a `Credential` in its `Resolve` input, so a
keychain dependency in there would make `internal/config` link a C library.
Never add an import to that package.

## Step 1 — never print one

Not truncated. Not masked. Not in an error, not in a `--output json` document,
not in a table cell, not in a debug line you meant to delete.

- `config.Resolve` reports `set (N characters)` and a location. There is nothing
  further up to redact because nothing further up ever has the value.
- A view type carries `has_token bool`, never the token. See `add-command`.
- `#nosec G117` belongs on the `yaml.Marshal` of a `Credential` in
  `keyring.go` — `gosec` flags marshalling a struct with a `refresh_token`
  field, and that is the one place where marshalling it is the point.

**The test every command owes**: run it in a loop over table, json and yaml,
with a distinctive secret seeded, and assert the string is in none of the
output. `TestLoginNeverPrintsTheToken` and `TestLogoutNeverPrintsTheToken` are
the shape. Do not assert only on the table — json is where a struct field leaks.

## Step 2 — reading and writing

Always through `opts.Credentials`, never by touching a file:

```go
cred, err := opts.Credentials.Get(name)   // zero value when absent, not an error
err := opts.Credentials.Set(name, cred)
err := opts.Credentials.Delete(name)      // succeeds when there is nothing there
err := opts.Credentials.Rename(from, to)
```

Two semantics that differ from the underlying libraries and are ours to keep:

- **A missing credential is the zero value and a nil error.** `keyring.Delete`
  returns `ErrNotFound` on a missing entry; the store swallows it, because
  `FileStore` does not have that error to give and the two must agree. Logging
  out twice is not a failure.
- **An empty context name is refused on write and empty on read.**
  `keyring.Set` accepts an empty account name silently, which would put a
  credential somewhere no lookup finds. The guard is ours, in both stores.

## Step 3 — the keychain is smaller than you think

Measured on a real keychain, not assumed:

| Platform | Usable bytes | Why |
| -------- | ------------ | --- |
| macOS    | ~3009        | the whole `security` command line is capped at 4096, and the secret is hex-encoded |
| Windows  | 2560         | the credential blob limit |
| Linux    | large        | `libsecret` has no comparable cap |

An access token with twenty group claims passes on your machine and fails on
somebody else's. `ErrTooBig` is why `Set` falls back to the file rather than
failing the login.

**`keyring.MockInit()` accepts 16KB, so no test can reach the real cap.** The
limit is a constant with a comment. Do not write a test that claims to prove it.

## Step 4 — the fallback writes to exactly one store

`store.Open` returns a `fallback` when a keychain probe succeeds, and a bare
`FileStore` otherwise (or when `OPENARITY_NO_KEYCHAIN` is set).

- `Get` reads the keychain, then the file.
- `Set` writes one **and deletes from the other.**
- `Delete` hits both.

That delete is the whole design. Without it, a token too big for the keychain
lands in the file while a stale one stays in the keychain — and since reads go
keychain-first, every subsequent command sends the old token. A login that
reports success and changes nothing.

**The test that catches it must seed the other store first.** A test that writes
an oversized credential into an empty keychain asserts a `Delete` that had
nothing to remove, and passes with the delete deleted.

`Rename` writes the new key before removing the old one, so an interruption
duplicates a credential rather than losing it.

## Step 5 — renewal, and which failures are fatal

`renewIfExpired` runs inside `opts.API(ctx)`, before every authenticated call.

```go
if o.Settings.Token.Source != o.Credentials.Location() {
	return nil
}
```

**Reuse that comparison; do not write a second precedence chain.** A token from
`--token` or `OPENARITY_TOKEN` has no refresh token behind it and is not ours to
replace. `Resolve` already decided which source won, and one comparison means
the renewal rule cannot drift from the sending rule.

Then, from `auth.Refresh`:

| The provider said | Do |
| ----------------- | -- |
| `invalid_grant` | `ErrRefreshRejected` — delete the stored credential |
| any other OAuth error, a 500, a timeout | wrap and return; **leave the credential alone** |
| a token with no `refresh_token` | keep the old refresh token |

The second row is the one that is quietly wrong for months. Treating a provider
outage as a dead login logs out everyone the moment the identity provider
restarts, and from the outside that is indistinguishable from a genuinely
revoked grant.

The third row is the other half. Rotation is optional in OAuth: providers that
rotate send a new refresh token and kill the old, providers that do not send
none and expect reuse. Storing what came back gives a login that renews exactly
once — which looks identical to the rotation bug and needs the opposite fix.
Both cases need their own test; one of them will be the only thing standing
between you and a fleet of dead logins.

## Step 6 — the lifecycle moves the pair

A credential is keyed by context name, so every `oa context` operation has a
credential half:

| Command | Also |
| ------- | ---- |
| `context rename` | `Credentials.Rename(from, to)` |
| `context delete` | `Credentials.Delete(name)` |
| `context create` | nothing — a new context has no login |

A rename that moves the config entry and leaves the credential behind is a
context that is silently logged out, and a stale secret nothing will ever clean
up.

## Step 7 — an OAuth grant

If you are adding one (`authorization_code`, a token exchange), it goes through
`(*Provider).exchange` with a `url.Values`. The endpoint is not a parameter —
every grant posts to the token endpoint. Discovery reads the endpoint from
`.well-known/openid-configuration` rather than building it, because only the
issuer is standardised.

For a polling grant, exactly two error codes continue —
`authorization_pending` and `slow_down` — and everything else stops. Write the
`switch` with a `default:` that stops, never a list of terminal codes:
a provider's unknown error must not become an infinite loop.
`authorization_pending` arrives as an HTTP 400 that means "keep waiting", so the
status alone can never be the answer.

**Select on cancellation before the first poll, not after.** A test that proves
"it eventually stops" proves nothing — the same context reaches the HTTP client,
so `time.Sleep` fails too, just late. Assert on elapsed time: a one-minute
interval, a 20ms deadline, and a check that it returned in well under a second.

## Step 8 — the tests

Everything in this file is isolated by `clitest.Isolate`, which sets
`OPENARITY_CONFIG_DIR` **and** `OPENARITY_NO_KEYCHAIN`. Both. `make check` on a
Mac once wrote real Keychain entries that nothing cleaned up, because the
filesystem was isolated and the keychain was not, and no test output said so.
If you add a store, isolate it here or it will run against the real thing.

```sh
security find-generic-password -s openarity    # should find nothing after a run
```

Four tests every change in this area owes:

1. **No format prints the secret** — a loop over table, json and yaml.
2. **The other contexts are untouched** — seed two, act on one, assert the
   second still has its credential. This is the entire reason credentials are
   keyed by context.
3. **The failure path stores nothing** — a refused login, a rejected renewal,
   a store that errors. Assert the stored value afterwards, not just the error.
4. **The mutation fails the test.** Delete the `Delete`, invert the
   `invalid_grant` check, drop the "keep the old refresh token" line — and
   confirm something goes red. Three of the mutations tried against this code
   survived the first time, and all three were tests asserting on state that was
   never seeded.

```sh
make check
```
