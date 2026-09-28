# syntax=docker/dockerfile:1
# ============================================================================
# TrustAgent proxy (Go): the gateway binary plus the demo traffic generator.
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
#   final  alpine, non-root uid 1001, binaries at /<cmd> (/proxy, /trafficgen)

ARG GO_VERSION=1.25.11
ARG ALPINE_VERSION=3.24

# --- Stage 1: gRPC stubs ------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS proto

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
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build

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
# CMDS lists the ./cmd/<name> packages to ship; each lands at /<name>.
ARG VERSION=dev
ARG CMDS="proxy trafficgen"
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p /out \
 && for cmd in ${CMDS}; do \
      CGO_ENABLED=0 GOOS="${TARGETOS:-linux}" GOARCH="${TARGETARCH}" go build -trimpath \
        -ldflags="-s -w -X github.com/truthtable/backend-go/internal/version.Version=${VERSION}" \
        -o "/out/${cmd}" "./cmd/${cmd}" || exit 1; \
    done

# --- Stage 3: runtime ---------------------------------------------------------
FROM alpine:${ALPINE_VERSION}

ARG VERSION=dev
LABEL org.opencontainers.image.title="truthtable-proxy" \
      org.opencontainers.image.description="TrustAgent gateway proxy and demo traffic generator" \
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
