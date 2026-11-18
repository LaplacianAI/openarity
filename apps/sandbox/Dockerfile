# Build context is apps/sandbox:
#
#   docker build -t openarity/sandboxd apps/sandbox
#
# This builds sandboxd, which runs `sandbox serve`. The room image, the one a
# command MCP server runs in (Node, Python, uv and the shim), is
# deployment/sandbox/Dockerfile, built from the repository root.

# --platform=$BUILDPLATFORM pins the build stage to the machine doing the
# building, so buildx does not run a Go compile under QEMU. Go cross-compiles
# natively.
FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine AS build

WORKDIR /src

# Declaring the ARGs is what brings them into scope; an undeclared
# TARGETARCH expands to empty and the build silently makes the builder's own.
ARG TARGETARCH
ARG TARGETOS

# CGO_ENABLED=0: no libc is needed, and the static binary runs in distroless
# and in a room image alike.
ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH

COPY go.mod go.sum ./
RUN go mod download

COPY . ./
RUN go build -trimpath -ldflags="-s -w" -o /sandbox ./cmd/sandbox

# sandboxd: distroless, no shell, nothing to exec into. It runs as the user
# that owns the Docker socket, which compose sets with `user:`.
FROM gcr.io/distroless/static-debian12:nonroot AS sandboxd
COPY --from=build /sandbox /sandbox
EXPOSE 9090
ENTRYPOINT ["/sandbox", "serve"]
