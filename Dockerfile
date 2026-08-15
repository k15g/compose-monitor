# syntax=docker/dockerfile:1
#
# The syntax line pins the BuildKit frontend, which is what makes the cache
# mounts below available regardless of how old the local Docker is.

# Build ------------------------------------------------------------------
#
# Pinned to the machine doing the building rather than the machine being built
# for, so the compiler always runs natively. Go cross-compiles, and with cgo off
# there is nothing to link against — building arm64 on an amd64 runner costs no
# more than building amd64, where emulating the whole toolchain under QEMU would
# cost several minutes a build.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies first, so a source-only change does not re-download them.
#
# The layer split handles the common case; the cache mount handles the rest.
# `/go/pkg/mod` survives across builds — including the ones where go.mod did
# change and this layer was rebuilt anyway — so a new dependency costs one
# download on this machine rather than one per build.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# The templates' generated Go is committed and CI fails if it is stale, so the
# image build needs no code generation of its own.
#
# Static: the final stage carries no libc, and the CSS, JS and favicon are
# embedded in the binary, so this one file is the whole application.
#
# Two caches here: the module cache again, because the build reads from it, and
# Go's compile cache, which is the one that matters most — with it, a one-file
# change recompiles that package instead of the whole dependency tree.
#
# Both are per-builder and not part of the image. A fresh CI runner starts cold
# unless the workflow wires up a cache backend of its own.
#
# TARGETOS and TARGETARCH are supplied by the builder, one value per platform
# being built. They are what make the cross-compile a cross-compile: without
# them this stage, pinned to the build machine, would put that machine's binary
# into every image — an arm64 image carrying an amd64 executable, which fails
# only when something tries to run it.
ARG TARGETOS
ARG TARGETARCH

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/compose-monitor ./cmd/server

# Run --------------------------------------------------------------------
#
# No --platform here: this stage is the image being built, so it is pulled for
# the target architecture. Nothing runs in it — only a COPY — so no emulation is
# needed to assemble it either.
FROM gcr.io/distroless/static-debian12:nonroot

# Annotations on the final stage, so they survive onto the image that ships.
# A LABEL in the build stage would be thrown away with it.
#
# These are the ones that are true of every build. The three that are not —
# version, revision and created — are stamped by the workflow that builds the
# image, because only it knows which commit and which tag it is building.
LABEL org.opencontainers.image.title="Compose Monitor"
LABEL org.opencontainers.image.description="Lists the Docker services, networks and volumes of one Compose project on a page that updates itself over SSE."
LABEL org.opencontainers.image.url="https://github.com/k15g/compose-monitor"
LABEL org.opencontainers.image.source="https://github.com/k15g/compose-monitor"
LABEL org.opencontainers.image.documentation="https://github.com/k15g/compose-monitor#readme"
LABEL org.opencontainers.image.licenses="GPL-3.0-or-later"
LABEL org.opencontainers.image.vendor="Klakegg Consulting AS"

# The version of this build. A release passes its number; anything else passes a
# timestamp and the commit it was built from, which is enough to tell two edge
# images apart and to find the source of either.
#
# It has to be passed in rather than worked out here: .dockerignore keeps .git
# out of the build context, so the build has no commit to read.
ARG VERSION=unknown
LABEL org.opencontainers.image.version="${VERSION}"

COPY --from=build /out/compose-monitor /compose-monitor

EXPOSE 8080
USER nonroot:nonroot

# The image has no shell, so the usual `CMD curl ...` is not available — the
# binary probes itself instead. /healthz reports the process is serving, and
# deliberately stays healthy when the Docker socket is unreadable: that is a
# state the service recovers from on its own, and restarting it would not help.
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
	CMD ["/compose-monitor", "-healthcheck"]

ENTRYPOINT ["/compose-monitor"]
