# The room image: what a command MCP server runs in. Every sandbox provider
# starts its rooms from it, and the provider runs `sandbox shim -- <command>`
# itself, so there is no ENTRYPOINT here to hide the command.
#
# Build context is the repository root, so this file can see apps/sandbox.
#
#   docker build -f deployment/sandbox/Dockerfile -t openarity/sandbox .

# Pinned to the build machine, never the machine built for: see
# deployment/Dockerfile, which says why in full. Go cross-compiles natively.
FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine AS build

WORKDIR /src/apps/sandbox

ARG TARGETARCH
ARG TARGETOS

# GOWORK=off so the build uses apps/sandbox's own go.mod: the workspace at the
# repository root lists modules this image does not copy. CGO_ENABLED=0 makes
# the binary static, so it runs on any libc the room has, and cross-compiles.
ENV GOWORK=off CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH

# Dependencies first, so this layer survives most rebuilds.
COPY apps/sandbox/go.mod apps/sandbox/go.sum ./
RUN go mod download

COPY apps/sandbox/ ./
RUN go build -trimpath -ldflags="-s -w" -o /sandbox ./cmd/sandbox

# Stage names for the two things copied in whole, so each version is pinned in
# one place. Node and Python are both Debian bookworm, so the node binary runs
# against the glibc of the Python base.
FROM node:22.21.1-bookworm-slim AS node
FROM ghcr.io/astral-sh/uv:0.9.9 AS uv

# MCP servers are published as npm packages (npx) and Python packages (uvx);
# this is the smallest base that runs both. E2B needs a Debian, Ubuntu, Fedora,
# Arch or Alpine image with /etc/os-release, and bash for its login shell.
FROM python:3.12.12-slim-bookworm

COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
 && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx

COPY --from=uv /uv /uvx /usr/local/bin/
COPY --from=build /sandbox /usr/local/bin/sandbox

# A named user, not root. Its home is where npx and uvx keep their caches, so
# it has to be writable by it.
RUN useradd --uid 10001 --create-home --home-dir /home/sandbox --shell /bin/bash sandbox

USER sandbox
ENV HOME=/home/sandbox
WORKDIR /home/sandbox
