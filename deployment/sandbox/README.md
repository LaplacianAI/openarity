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
beside the brain's image, in the same workflow
(`.github/workflows/publish-image.yml`), for every commit of `main` that has a
brain image. A brain starts rooms from the sandbox image of its own commit.

## Which image a room starts from

The brain's choice, never a team's: a team picks a provider, its address and
its key, and nothing else.

- **sandboxd** pulls the image itself the first time a room needs it, and only
  an image under its `SANDBOXD_IMAGES` prefixes.
- **Daytona** is sent the image reference and builds and caches it itself.
- **E2B** does not run an image by name; it runs a template built in the
  team's own E2B account, and it is the one provider a team says something
  about: the template's name, in Settings or with `oa teams settings set
  --e2b-template`. Left empty, the brain uses `openarity-sandbox`, which is
  what `e2b/` builds by default:

  ```sh
  cd deployment/sandbox/e2b
  npm ci
  E2B_API_KEY=e2b_... npm run build
  ```

  - `SANDBOX_IMAGE` builds from another tag, for example
    `ghcr.io/laplacianai/openarity-sandbox:sha-<commit>`. The default is `latest`.
  - `TEMPLATE_NAME` names the template; the default is `openarity-sandbox`.
  - Another team's public template is named `owner/name`
    ([public templates](https://e2b.dev/docs/faq/public-templates)).
  - Build again for a newer image: a template is a snapshot.

E2B sandboxes cannot reach the internet unless the team's egress rules allow
it, and `npx` has to fetch its package: the rules must include the npm registry
(`registry.npmjs.org:443`), and whatever the server itself talks to.
