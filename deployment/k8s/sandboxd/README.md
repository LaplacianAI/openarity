# sandboxd on Kubernetes

Optional. These manifests are not applied by `kubectl apply -f deployment/k8s/`:
they live in a subdirectory, like `examples/`, because most clusters should not
run them. Apply them on purpose:

```sh
kubectl apply -f deployment/k8s/sandboxd/secret.yaml     # a template: make the real one, see the file
kubectl apply -f deployment/k8s/sandboxd/deployment.yaml
kubectl apply -f deployment/k8s/sandboxd/service.yaml
```

sandboxd runs a command MCP server (`npx`, `uvx`) in a container of its own, a
room, with no network but the hosts it lists. It needs a Docker daemon to make
rooms on. This directory gives it one inside its own pod: a rootless
Docker-in-Docker (DinD) daemon. A Kubernetes-native backend, a pod per room,
is future work; until then this is the price of self-hosting rooms on a
cluster, and a team that does not want to pay it should use E2B or Daytona.

## What it needs, and what it costs

Read this before applying. DinD-rootless on Kubernetes is the weakest
arrangement sandboxd runs in.

- **A privileged pod.** `securityContext.privileged: true` is in
  `deployment.yaml`, and it is not optional: Docker's rootless image has to
  mount `/proc` and create user, mount and network namespaces, and a default
  container profile forbids that. A privileged container has every capability
  and sees the node's devices. The rootless daemon inside runs as uid 1000 in
  a user namespace, so a room that escapes its container lands as an
  unprivileged user *in that pod*, but the pod itself is not confined: anyone
  who gets code execution as root in this container, through a bug in the
  daemon or in the image, has a path to the node. Treat the namespace it runs
  in as trusted infrastructure. Pod Security Admission's `baseline` and
  `restricted` levels both refuse this pod; the namespace has to be
  `privileged`. Put it in a namespace of its own, not the brain's.
- **Nested isolation is thinner than it looks.** A room is a container inside
  a user namespace inside a privileged container. Each layer is real, but the
  kernel is the node's, shared by all of them. A kernel bug is a bug for
  every layer at once. If your threat model includes hostile MCP servers
  rather than merely buggy ones, run this on a node pool that holds nothing
  else (a taint and a toleration), or use a hosted provider.
- **A hardening you can try, untested here.** On Kubernetes 1.30+ with a
  runtime that supports it, `hostUsers: false` (a user namespace for the pod)
  plus `seccompProfile: Unconfined` and `appArmorProfile: Unconfined`
  can replace `privileged: true`. That removes the capabilities and the
  devices; whether the daemon still starts depends on the runtime and the
  node's kernel.
- **Kernel settings on the node.** `user.max_user_namespaces` above zero, and
  on some distributions `kernel.unprivileged_userns_clone=1`. The pod exits
  with the daemon's own message if they are not.
- **No persistence.** The daemon's images and rooms live in an `emptyDir`. A
  pod restart loses every room; the brain's next run makes new ones. Images
  are pulled again on the first room that uses them.
- **One replica.** Rooms live on one daemon. The memory budget
  (`SANDBOXD_MEMORY_BUDGET`, default 3 GiB) is per pod, and the pod's memory
  limit has to cover it plus the daemon: `deployment.yaml` sets 4 GiB.
- **Slower networking.** Rootless networking goes through a user-space stack
  (`vpnkit` here). It is fine for MCP traffic and not for bulk transfer.
- **sandboxd pulls its rooms' images from a registry**, only from
  `SANDBOXD_IMAGES` (default `ghcr.io/laplacianai/openarity-sandbox`). The
  daemon has to reach the registry, and the first start of an image is slow:
  list it in `SANDBOXD_PULL` to have it pulled at start. `npx -y
  pkg@version` inside a room needs the room's egress list to reach npm.

## How it is put together

One container, `dind`, from `docker:<version>-dind-rootless`. It starts the
daemon with only a unix socket, then runs sandboxd as a container on that
daemon and waits.

This is not a sidecar that shares its socket with a separate sandboxd
container, which is what one might draw first, and it cannot be. sandboxd
joins each room's network so a room has one address it can dial, sandboxd's
egress guard. A container can only join networks of the daemon it runs on,
and a container outside a rootless daemon cannot reach the addresses inside
its network namespace at all. So sandboxd is a container of the daemon whose
rooms it makes, as it is in `deployment/docker-compose.sandboxd.yml`.

The Service `sandboxd` exposes port 9090 inside the cluster only.

## Wiring the brain

The brain refuses to dial a private address unless `OPENARITY_MCP_PRIVATE_NETWORKS`
names it, and a Service's cluster IP is private. Name that one address, not
the pod network:

```sh
kubectl get svc sandboxd -o jsonpath='{.spec.clusterIP}'
# OPENARITY_MCP_PRIVATE_NETWORKS=10.96.12.34/32   (your address; keep the /32)
```

A broader range would let any URL a team saves reach the cluster's other
services.

Then admit only the brain and the worker to sandboxd's port, with a
NetworkPolicy of your own (the labels in `worker.yaml` and `deployment.yaml`
are `app.kubernetes.io/name: brain`, and the worker's). A room reaches
sandboxd's port too, from inside, as it can reach nothing else of the
cluster's; every route needs a token a room does not have.

## What is not here

- Not tested on a cluster. The compose overlay and the integration tests in
  `apps/sandbox/internal/integration` were run against Docker; this
  Deployment is built from Docker's documented rootless DinD behaviour and
  the image's entrypoint script, and has not been applied to a real cluster.
- No image for sandboxd is published yet. Build it (`docker build --target
  sandboxd apps/sandbox`), push it, and set `SANDBOXD_IMAGE`.
- No Kubernetes-native backend. A pod per room, with a NetworkPolicy for its
  egress, would drop the privileged pod. It needs an egress guard that works
  without joining a network, so it is a second implementation of the same API,
  not a change to this one.
