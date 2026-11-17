# The room image

A command MCP server (one started with `npx`, `uvx` or any other command) runs
in a room: a sandbox the brain makes for it. Every sandbox provider starts its
rooms from this image, which holds the `sandbox` binary (the shim), Node 22
with npm and npx, Python 3.12 with `uv` and `uvx`, and a non-root user. It has
no entrypoint. The brain runs `sandbox shim --listen :8080 -- <command>` itself.

```sh
# from the repository root
docker build -f deployment/sandbox/Dockerfile -t openarity/sandbox .
```

CI publishes `ghcr.io/laplacianai/openarity-sandbox:latest` and `:sha-<commit>`
on every push to `main` that touches `apps/sandbox/` or the image's files
(`.github/workflows/publish-sandbox-image.yml`). Use the `sha-` tag when a room
must be one known image.

## Daytona and sandboxd

Both start rooms from the image directly. Put the image reference, such as
`ghcr.io/laplacianai/openarity-sandbox:latest`, in the team's sandbox image
field in settings. Nothing else is built.

## E2B

E2B does not run an image by name; it runs a template built in your own E2B
account. `e2b/` builds one from the published image with E2B's template SDK
(`Template().fromImage(...)`), the method E2B's current docs describe.

```sh
cd deployment/sandbox/e2b
npm ci
E2B_API_KEY=e2b_... npm run build
```

- `SANDBOX_IMAGE` builds from another tag, for example
  `ghcr.io/laplacianai/openarity-sandbox:sha-<commit>`. The default is `latest`.
- `TEMPLATE_NAME` names the template; the default is `openarity-sandbox`.
- The build prints the template's name and ID. E2B accepts either wherever it
  asks for a template.

Then, in the team's settings, choose E2B, give the API key and put the template
name or ID in the sandbox image field. Build again to pick up a newer image: a
template is a snapshot, and does not follow `latest` by itself.

E2B sandboxes cannot reach the internet unless the team's egress rules allow
it, and `npx` has to fetch its package: the rules must include the npm registry
(`registry.npmjs.org:443`), and whatever the server itself talks to.
