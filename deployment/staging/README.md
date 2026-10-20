# Staging: main, or a pull request someone has read

One server runs the `make staging` stack (authentik, OpenBao, SeaweedFS,
Postgres, FalkorDB) once. Only the brain changes:

- **A merge to main** builds and publishes the image (`publish-image.yml`),
  and staging runs it on the `openarity` database.
- **A pull request labelled `ok to deploy`** is built on GitHub's machines with
  no secrets (`staging-build.yml`), pushed as `openarity-brain:pr-<n>`, and
  takes staging on a database of its own, `openarity_pr_<n>`
  (`staging-deploy.yml`).
- **New commits**, or reopening, remove the label. Staging keeps running the
  commit that was approved until somebody reads the new ones and labels it
  again.
- **Closing the pull request** drops its database and returns staging to main
  (`staging-pr.yml`).

One pull request holds staging at a time. A merge to main while one does is
recorded and deployed when that pull request is released.

## Why it is shaped like this

The repository is public, so anyone can open a pull request, and a pull
request can change the workflow files themselves.

- **Nothing from a pull request runs on the server, or near its key.** The
  build runs as a plain `pull_request` workflow: GitHub's runners, a
  read-only token, no secrets. The deploy runs as `workflow_run`, which always
  uses main's copy of the workflow, and only moves the built image as data.
- **There is no self-hosted runner.** On a public repository one is a machine
  that runs whatever a pull request's workflow says.
- **The label is the review, and approves one commit.** Adding it writes a
  commit status, `staging/approved`, on the head sha as it is at that moment
  (`staging-pr.yml`, running main's copy). The deploy ships only a sha that
  carries it, and only while the label is still on. The build workflow is not
  trusted for any of this: a pull request can rewrite it, and its own run uses
  its own copy, but a fork's token cannot write a status.
- **New commits, or reopening, take the label off.** Staging keeps running the
  approved commit; labelling again approves the new head.
- **The server's key can do four things.** It is an SSH forced command:
  `deploy.sh` reads `deploy`, `release` or `status` and refuses everything
  else, including a shell.
- **The image is pinned by digest**, so a tag moved after CI read it changes
  nothing.
- **The server's output stays on the server.** Actions logs on a public
  repository are public. Over SSH, `deploy.sh` writes what compose, curl and
  the brain print to `~/.openarity-staging/deploy.log` and answers CI with one
  line: what was deployed, or why not. Secrets are masked in logs, but an
  address printed by a tool is not a secret, so nothing would mask it.
- **Assume a deployed pull request is hostile.** Once it runs, its code can read
  everything the brain's environment holds. Give staging its own credentials:
  a model key with a spending cap, its own authentik, and nothing that reaches
  production.

## Setting up the server

Everything specific to the server — its address, its users, its keys —
belongs in the server's `deployment/.env` and in the `staging` environment's
secrets on GitHub. None of it goes in this repository.

### 1. The stack

On the server (Docker and the compose plugin installed, 8 GB of memory is
enough without the local embedding model):

```sh
sudo useradd --create-home --shell /bin/bash openarity
sudo usermod -aG docker openarity        # docker access is root-equivalent; this user is the deploy
sudo -iu openarity
git clone https://github.com/LaplacianAI/openarity.git
cd openarity/deployment
make start-docker                       # choose authentik, and a gateway
```

`make start-docker` writes `deployment/.env`, provisions authentik and
OpenBao, and starts the stack. Set `OPENARITY_ENVIRONMENT=staging` there if it
is not already, and give it a model key with a spending cap of its own.

Reach it over a private network rather than the internet: Tailscale, a
WireGuard tunnel, or `ssh -L`. authentik's issuer has to be the address
browsers use, so choose that address before provisioning it.

### 2. Keep the checkout on main

`deploy.sh` and the compose files are read from this checkout, never from a
pull request. Update it when main changes them:

```sh
sudo -iu openarity git -C ~/openarity pull --ff-only
```

### 3. The deploy key

On your own machine:

```sh
ssh-keygen -t ed25519 -N '' -C openarity-staging-ci -f staging-ci
```

On the server, as `openarity`, add the public half with the forced command:

```sh
cat >> ~/.ssh/authorized_keys <<EOF
command="$HOME/openarity/deployment/staging/deploy.sh",no-port-forwarding,no-agent-forwarding,no-X11-forwarding,no-pty $(cat staging-ci.pub)
EOF
```

Check it from your machine. The first command answers, the second is refused:

```sh
ssh -i staging-ci openarity@<server> status
ssh -i staging-ci openarity@<server> ls
```

### 4. GitHub

In **Settings → Environments**, create `staging`:

- **Deployment branches:** `main` only. Both privileged workflows run as main.
- **Secrets:**

  | Secret | Value |
  | --- | --- |
  | `STAGING_SSH_KEY` | the private half, `staging-ci` |
  | `STAGING_SSH_TARGET` | `openarity@<server>` |
  | `STAGING_SSH_PORT` | the SSH port, if it is not 22 |
  | `STAGING_SSH_KNOWN_HOSTS` | `ssh-keyscan -p <port> <server>`, checked against the server's own fingerprint |
  | `TS_OAUTH_CLIENT_ID`, `TS_OAUTH_SECRET` | only over Tailscale; see below |

- **Required reviewers** are optional: the label already is the review, and
  requiring approval would hold every merge to main as well.

In **Settings → Actions → General**, keep **Require approval for all
outside collaborators** for fork pull requests, and in **Issues → Labels**
create `ok to deploy`. Only people with triage access or above can add a
label.

The workflows only start once they are on main: `workflow_run` and
`pull_request_target` always run the default branch's copy.

### 5. A server on a tailnet

GitHub's runners are on the internet, so a server reachable only over
Tailscale needs the runner to join the tailnet for the one SSH call. It joins
as an ephemeral node tagged `tag:ci`, and the tailnet policy decides what that
tag can reach.

In the tailnet's access controls, let `tag:ci` reach the server's SSH port
and nothing else. With the server tagged `tag:staging`:

```json
"tagOwners": {
  "tag:ci":      ["autogroup:admin"],
  "tag:staging": ["autogroup:admin"]
},
"grants": [
  { "src": ["tag:ci"], "dst": ["tag:staging"], "ip": ["tcp:22"] }
]
```

Grants only add access, so this is narrow only if nothing else matches
`tag:ci`. The default policy's allow-all rule (`"src": ["*"]`) does; change
its source to `autogroup:member` so it covers people's devices and not tagged
ones.

Then, under **Settings → OAuth clients**, create a client with the
`auth_keys` write scope and the tag `tag:ci`, put its id and secret in the
`staging` environment as `TS_OAUTH_CLIENT_ID` and `TS_OAUTH_SECRET`, and set
the repository variable `STAGING_TAILSCALE` to `true`.

**Tailscale SSH must be off on the server.** It answers port 22 on the
tailnet address itself and never reads `authorized_keys`, so the forced
command, and everything it refuses, would not apply. The check in step 3
catches it: `ls` must be refused. `tailscale set --ssh=false` turns it off.

### 6. Using it

- Label a pull request `ok to deploy`. It is built, deployed, and the pull
  request says on which database.
- `ssh -i staging-ci openarity@<server> status` says what holds staging.
- A failed deploy says only that it failed. Why is in
  `~/.openarity-staging/deploy.log` on the server.
- Changing a compose file or `deploy.sh` in a pull request does not change
  what that pull request is deployed with: the server uses main's.
