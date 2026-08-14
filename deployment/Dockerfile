# Build context is the repository root, so this file can see apps/brain.
#
#   docker build -f deployment/Dockerfile -t openarity/brain .

# --platform=$BUILDPLATFORM pins the build stage to the machine doing the
# building, never the machine being built for. Without it, buildx runs the
# whole arm64 stage under QEMU emulation and a Go compile takes minutes rather
# than seconds. Go cross-compiles natively, so emulating it buys nothing.
FROM --platform=$BUILDPLATFORM golang:1.26.6-alpine AS build

WORKDIR /src

# Set by buildx for each platform it is asked for: arm64, amd64. Declaring the
# ARG is what brings it into scope — an undeclared TARGETARCH expands to empty
# and the build silently produces the builder's own architecture.
ARG TARGETARCH
ARG TARGETOS

# GOWORK=off so the build uses apps/brain's own go.mod. The workspace at the
# repository root lists modules that are not copied into this image, and go
# refuses to build a workspace whose members are missing.
#
# CGO_ENABLED=0 was already required for the distroless base, which carries no
# libc. It is also what makes cross-compiling work at all.
ENV GOWORK=off CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH

# Dependencies first: they change far less often than the code, so this layer
# survives most rebuilds.
COPY apps/brain/go.mod apps/brain/go.sum ./
RUN go mod download

COPY apps/brain/ ./

# The migrations are embedded, so the binary carries its own schema.
#
# -trimpath keeps build paths out of panics; the ldflags drop DWARF and the
# symbol table, which is most of the binary size and nothing a container needs.
RUN go build -trimpath -ldflags="-s -w" -o /brain ./cmd/brain

# Distroless: no shell, no package manager, nothing to exec into. Debugging
# happens through logs and the probes.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /brain /brain

# 21120 is the API, 21121 the webhook listener. Both must be bound to 0.0.0.0
# in a container — the defaults are loopback, which nothing outside the
# container can reach.
EXPOSE 21120 21121

USER nonroot:nonroot
ENTRYPOINT ["/brain"]
