# syntax=docker/dockerfile:1
# ============================================================================
# TrustAgent proxy (Go): the gateway binary, the demo traffic generator, the
# mock vendor APIs of the e2e and load stacks, the decision-model check and the
# replay exporter.
# ============================================================================
# Build context: the REPOSITORY ROOT (SPEC §3.4), so the image can generate the
# gRPC stubs from proto/ and compile the contracts module that backend-go/go.mod
# replaces with ../contracts. Build it the way compose and CI do:
#
#     docker build -f backend-go/Dockerfile .
#
# The context is filtered by backend-go/Dockerfile.dockerignore (BuildKit reads
# the Dockerfile-specific ignore file instead of the root .dockerignore).
#
# Stages:
#   proto  protoc + pinned Go plugins -> generated stubs (they are gitignored,
#          so a fresh clone builds with nothing but Docker)
#   build  compiles every command in CMDS into /out, on the build platform
#          (GOOS/GOARCH come from the target platform, so multi-arch is cheap)
#   final  alpine, non-root uid 1001, binaries at /<cmd> (/proxy, /trafficgen,
#          /mock-upstream, /jevcheck, /export-replay)
#
# Base images are pinned by their multi-arch INDEX digest (D125), so every
# platform of a release (linux/amd64, linux/arm64) builds from the same bytes;
# the tag stays next to the digest for readers and for Dependabot's docker
# ecosystem, which moves both together. Pinned 2026-10-21: the tag on each FROM
# line names the release line, and the index digest after it is the pin (a
# Dependabot digest refresh may move the patch release behind the same tag).

# --- Stage 1: gRPC stubs ------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd AS proto

# protobuf-dev ships the well-known .proto includes. Alpine packages are not
# version-pinned: the base image tag fixes the release branch.
# hadolint ignore=DL3018
RUN apk add --no-cache protobuf protobuf-dev

# Same pins as mk/go.mk (PROTOC_GEN_GO_VERSION / PROTOC_GEN_GO_GRPC_VERSION) and CI.
ARG PROTOC_GEN_GO_VERSION=v1.34.2
ARG PROTOC_GEN_GO_GRPC_VERSION=v1.5.1
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go install "google.golang.org/protobuf/cmd/protoc-gen-go@${PROTOC_GEN_GO_VERSION}" \
 && go install "google.golang.org/grpc/cmd/protoc-gen-go-grpc@${PROTOC_GEN_GO_GRPC_VERSION}"

WORKDIR /src
COPY proto/ proto/
RUN mkdir -p /out \
 && protoc --proto_path=proto \
      --go_out=/out --go_opt=module=github.com/truthtable/backend-go \
      --go-grpc_out=/out --go-grpc_opt=module=github.com/truthtable/backend-go \
      proto/evaluator.proto

# --- Stage 2: build -----------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd AS build

WORKDIR /src/backend-go

# Module graph first, for layer caching. contracts/go.mod is needed because
# backend-go/go.mod replaces github.com/truthtable/contracts with ../contracts.
COPY backend-go/go.mod backend-go/go.sum ./
COPY contracts/go.mod /src/contracts/go.mod
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY contracts/ /src/contracts/
COPY backend-go/ ./
COPY --from=proto /out/ ./

# VERSION is stamped into internal/version (compose passes TRUSTAGENT_VERSION).
# GIT_SHA (make passes `git rev-parse --short HEAD`; the build context has no
# .git, so Go's own VCS stamp is empty) goes to internal/version.GitSHA: the
# proxy reports it as status build_sha and names it as the git revision of a
# recording (GET /api/export/replay); the image label below carries it too.
# Empty leaves it unset.
# CMDS lists the ./cmd/<name> packages to ship; each lands at /<name>: the gateway, the demo
# traffic generator, the mock vendor APIs, the decision-model check (`make jev-check` runs
# /jevcheck in the proxy container) and the replay exporter (`make record-demo` runs
# /export-replay in it).
ARG VERSION=dev
ARG GIT_SHA=""
ARG CMDS="proxy trafficgen mock-upstream jevcheck export-replay"
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p /out \
 && for cmd in ${CMDS}; do \
      CGO_ENABLED=0 GOOS="${TARGETOS:-linux}" GOARCH="${TARGETARCH}" go build -trimpath \
        -ldflags="-s -w -X github.com/truthtable/backend-go/internal/version.Version=${VERSION} -X github.com/truthtable/backend-go/internal/version.GitSHA=${GIT_SHA}" \
        -o "/out/${cmd}" "./cmd/${cmd}" || exit 1; \
    done

# --- Stage 3: runtime ---------------------------------------------------------
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

ARG VERSION=dev
LABEL org.opencontainers.image.title="truthtable-proxy" \
      org.opencontainers.image.description="TrustAgent gateway proxy, demo traffic generator, mock vendor APIs, decision-model check and replay exporter" \
      org.opencontainers.image.source="https://github.com/timkush1/TrustAgent" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"

# ca-certificates: HTTPS upstreams and decision-model backends.
# uid/gid 1001 is shared with the engine image: the proxy writes the internal
# token into the trustagent-internal volume and the engine reads it.
# /var/lib/trustagent (TRUTHTABLE_DATA_DIR, holds secret.key) and
# /var/lib/trustagent-internal exist in the image with that owner, so empty
# named volumes mounted there take it over.
# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates \
 && addgroup -S -g 1001 trustagent \
 && adduser -S -D -H -u 1001 -G trustagent -s /sbin/nologin trustagent \
 && install -d -o 1001 -g 1001 -m 0700 /var/lib/trustagent \
 && install -d -o 1001 -g 1001 -m 0750 /var/lib/trustagent-internal

COPY --from=build /out/ /

USER 1001:1001
WORKDIR /var/lib/trustagent

# 8080: HTTP API, /v1 gateway and /ws. 8002: Prometheus metrics.
EXPOSE 8080 8002

# busybox wget ships with alpine; /health is public (SPEC §2.4). Port 8080 is
# the TRUTHTABLE_PORT default (compose pins it).
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=3 \
    CMD ["wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8080/health"]

CMD ["/proxy"]

# The git revision the image was built from (make passes `git rev-parse --short HEAD`). Declared
# and labelled last: a label is metadata only, so a new commit rebuilds no layer above it.
ARG GIT_SHA=""
LABEL org.opencontainers.image.revision="${GIT_SHA}"
