---
name: update-api-spec
description: Update api/openapi.yaml when an endpoint is added, changed or removed — the hand-written contract the CLI and every other client are generated from. Covers what belongs in the spec, the conventions it already follows, how to validate it, the test that fails when it drifts from the routes, and when a change is breaking. Use alongside add-route for every endpoint.
---

# Update the API spec

`api/openapi.yaml` is the contract. It is hand-written, reviewed as a diff, and
**not** generated from code comments — a change to it is a deliberate change to
what callers may rely on, and it should be as hard to sneak past review as a
migration.

The spec is upstream of the CLI, not downstream. Generating it from annotations
would put the contract behind the code it exists to constrain.

## The rule

An endpoint is not done when it serves traffic. It is done when it is in the
spec, and the contract test proves the two agree. That test fails the build if
a route exists without a path, or a path exists without a route — so this is
not a discipline anyone has to remember.

## Step 1 — describe the operation

```yaml
  /teams/{id}/members:
    parameters:
      - $ref: "#/components/parameters/TeamID"
    post:
      operationId: addTeamMember
      tags: [teams]
      summary: Add a member
      description: |
        Requires `membership:write` in this team. The role is checked by the
        database rather than the service: roles are rows, so an unknown one is
        a rejected foreign key and comes back as a 400.
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/AddMemberRequest"
      responses:
        "204":
          description: Added
        "400": { description: ..., content: ... }
        "401": { $ref: "#/components/responses/Unauthenticated" }
        "403": { $ref: "#/components/responses/Forbidden" }
        "404": { $ref: "#/components/responses/NotFound" }
        "409": { description: Already a member, content: ... }
```

- **`operationId` is required and is an API name.** Generators turn it into the
  client's method name, so `addTeamMember` becomes `client.AddTeamMember(...)`
  while a missing one becomes `teamsIdMembersPost`. Renaming it later renames a
  method on every client.
- **List every status the handler can actually return.** Reuse the shared
  responses; a status that only this operation produces is written inline.
- **Path parameters shared by every operation on a path go on the path**, not
  repeated per verb.
- **The description says why, not what.** "Requires `membership:write`" is a fact a
  reader cannot get from the schema; "adds a member to a team" is the summary
  repeated.

## Step 2 — the conventions already in the file

Match these rather than inventing a second style:

- **Errors are `text/plain`, not JSON.** `http.Error` writes a bare sentence.
  Do not describe an error schema the service does not send. Clients branch on
  the status code.
- **Every list operation takes `Limit` and `Cursor`** and returns a `*Page`
  schema with `items` required and `next_cursor` optional. The absence of
  `next_cursor` is the end of the collection — never add a second flag.
- **Request schemas set `additionalProperties: false`**, because `DecodeJSON`
  rejects unknown fields. A spec that allows them promises something the
  service refuses.
- **Optional response fields are the ones genuinely absent** — `role` on a team
  the caller is not in, `email` from a provider that released none. Never mark
  a collection optional.
- **`security: []` on an operation makes it public.** Only the probes have it.
  Adding it anywhere else is a decision for review, and it belongs in the
  `SECURITY.md` list of unauthenticated endpoints too.
- **A body that accepts one of two fields is documented, not `oneOf`.** Mark
  only the always-required fields in `required`, make the alternatives
  optional, and state the rule in the `description` — then enforce it in the
  handler and test both spellings. See the note below for why.

## Step 3 — validate before committing

```sh
npx --yes @redocly/cli@latest lint api/openapi.yaml
```

Two warnings are expected and correct: the probes have no 4xx. Anything else is
a real finding — a missing `operationId`, a dangling `$ref`, a schema nothing
references.

Then run the contract test, which is the one that catches the mistake a linter
cannot:

```sh
make check db=openarity_test
```

## Step 4 — is the change breaking?

The CLI is generated from this file, so these are not equivalent:

| Change                                     | Breaking |
| ------------------------------------------ | -------- |
| Adding an operation                         | no       |
| Adding an optional response field           | no       |
| Adding an optional request field            | no       |
| Making a required request field optional    | no       |
| Renaming `operationId`                      | **yes**  |
| Adding a required request field             | **yes**  |
| Removing or renaming a response field       | **yes**  |
| Narrowing a type, or adding an enum member a client must handle | **yes** |
| Changing a status code for the same outcome | **yes**  |

A breaking change needs `info.version` raised and a note in the pull request
saying what a client has to do. There is no deprecation machinery yet, so
"nobody has generated a client" is the only reason a break is cheap — say so
explicitly rather than assuming it.

## Step 5 — the docs UI

`GET /openapi.yaml` serves this file in every environment. `GET /docs` renders
it and is mounted **only when `OPENARITY_ENVIRONMENT` is `development`** — the
spec names every endpoint and its authorisation rules, which is a map worth not
handing out.

The spec is embedded with `go:embed`, so the binary is self-contained and the
served copy cannot drift from the committed one.

## Things that have gone wrong here

- **`examples:` is plural in OpenAPI 3.1 and takes an array.** The 3.0 spelling
  `example:` is silently ignored, so the value never renders.
- **A `$ref` to a response and a `$ref` to a schema are different things.**
  `#/components/responses/NotFound` carries a description and content;
  `#/components/schemas/...` is the body alone.
- **The spec described a JSON error envelope the service never sent.** It was
  written from what the API ought to do rather than from a `curl`. Run the
  request and copy the output.
- **A top-level `oneOf` on a request schema wrecks the generated client.**
  `AddMemberRequest` takes `user_id` or `subject`, and writing that as

  ```yaml
      oneOf:
        - required: [user_id]
        - required: [subject]
  ```

  made oapi-codegen emit a `union json.RawMessage` field, a hand-rolled
  `MarshalJSON`, and two `AddMemberRequest0 = interface{}` aliases. Every
  client then has to build the body through generated accessors instead of a
  struct literal. Probed, not assumed — generate and read the output before
  keeping a `oneOf`.

  The constraint still belongs in the spec, as prose in the `description` and
  as a 400 the handler returns. A schema keyword that no client can use is
  worth less than a sentence a reader can.
- **A field added to a response schema fails nothing.** The contract test
  compares *routes* against paths, not bodies. `Whoami` grew `id` in the spec
  and the handler kept not sending it for three commits; the drift only
  surfaced when the generated client was read by hand. When a schema gains a
  required field, add the assertion to that route's test in the same change.
