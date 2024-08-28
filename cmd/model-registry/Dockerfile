# Dockerfile for the production model-registry binary.
# Build context is the repo root: `docker build -f cmd/model-registry/Dockerfile .`
#
# Two stages: a golang build stage compiles the binary (CGO_ENABLED=0
# for static linking, -trimpath + -ldflags="-s -w" for reproducibility
# and image size), and a distroless static runtime stage. The runtime
# image inherits a non-root user (65532) pre-configured by distroless's
# :nonroot variant so Kubernetes manifests do not need to set
# securityContext.runAsUser explicitly.
#
# Multi-arch: the build stage runs on the BUILD platform (linux/amd64
# on a GitHub-hosted runner) but cross-compiles for the TARGET platform
# (linux/amd64 OR linux/arm64). buildx is required (the CI workflow
# installs it via docker/setup-buildx-action).

# --platform=$BUILDPLATFORM keeps the build stage on the runner's
# native architecture (no QEMU emulation during go build) while
# cross-compiling via GOARCH below.
FROM --platform=$BUILDPLATFORM golang:1.25 AS build
WORKDIR /src

# BUILDPLATFORM, TARGETOS, TARGETARCH are provided automatically by
# buildx when the build is launched with --platform. Without buildx
# the args fall through with empty values; the go build command below
# defaults GOARCH to the host arch in that case so a plain
# `docker build` still produces a working amd64-only image.
ARG BUILDPLATFORM
ARG TARGETOS
ARG TARGETARCH

# Copy module manifest first to make the module-download layer
# cacheable across source-only changes.
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the repo and compile.
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" \
    -o /out/model-registry ./cmd/model-registry

# Pre-create the default --store-root with the runtime user's
# ownership. Distroless has no shell + no mkdir, so an empty
# directory has to be materialised in the build stage and copied
# in with --chown. Without this, a `docker compose` named volume
# mounted at /data inherits root:root ownership and the registry's
# fsstate / fsaudit / fsstore opens fail with errno 14 ("unable to
# open database file"). Operators who override --store-root to a
# different path are unaffected.
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/model-registry /usr/local/bin/model-registry
COPY --from=build --chown=65532:65532 /out/data /data

# Distroless :nonroot ships USER 65532:65532. The explicit USER line
# documents the inherited posture and survives a base-image change
# that might drop the implicit default.
USER 65532:65532

# 8090: HTTP surface (/healthz, /readyz, /metrics, /artifacts,
# /env/*/state, /audit, /upload, /promote, /rollback).
EXPOSE 8090
ENTRYPOINT ["/usr/local/bin/model-registry"]
