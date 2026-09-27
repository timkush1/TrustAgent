# syntax=docker/dockerfile:1
# ============================================================================
# TrustAgent audit engine (Python): the deep-audit gRPC server.
# ============================================================================
# Build context: the REPOSITORY ROOT (SPEC §3.4), so the image can generate the
# gRPC stubs from proto/ and ship contracts/ at /app/contracts. Build it the way
# compose and CI do:
#
#     docker build -f backend-python/Dockerfile .
#     docker build -f backend-python/Dockerfile --build-arg EXTRAS=knowledge .
#
# The context is filtered by backend-python/Dockerfile.dockerignore.
#
# Dependencies come from poetry.lock (D11): the exact locked versions, including
# the CPU-only torch wheel from the explicit pytorch-cpu source, with no
# hand-maintained pip list. EXTRAS names optional Poetry extras to add (the
# compose key ENGINE_EXTRAS; `knowledge` for the knowledge-base add-on).

ARG PYTHON_VERSION=3.11

# --- Stage 1: dependencies, stubs --------------------------------------------
FROM python:${PYTHON_VERSION}-slim AS build

ENV PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    POETRY_NO_INTERACTION=1 \
    POETRY_VIRTUALENVS_CREATE=1 \
    POETRY_VIRTUALENVS_IN_PROJECT=1 \
    POETRY_CACHE_DIR=/tmp/poetry-cache

# Same Poetry release that wrote poetry.lock.
ARG POETRY_VERSION=2.4.1
RUN pip install "poetry==${POETRY_VERSION}"

WORKDIR /app

# Lockfile first, for layer caching. README.md is declared by pyproject.toml.
COPY backend-python/pyproject.toml backend-python/poetry.lock backend-python/README.md ./
ARG EXTRAS=""
RUN --mount=type=cache,target=/tmp/poetry-cache \
    poetry install --only main --no-root ${EXTRAS:+--extras "${EXTRAS}"}

COPY backend-python/src/ src/
COPY proto/evaluator.proto /tmp/proto/evaluator.proto
# The stubs are gitignored; generate them with the locked grpcio-tools.
RUN .venv/bin/python -m grpc_tools.protoc -I/tmp/proto \
      --python_out=src/truthtable/grpc/pb \
      --grpc_python_out=src/truthtable/grpc/pb \
      /tmp/proto/evaluator.proto

# --- Stage 2: runtime ---------------------------------------------------------
FROM python:${PYTHON_VERSION}-slim

ARG VERSION=dev
LABEL org.opencontainers.image.title="truthtable-engine" \
      org.opencontainers.image.description="TrustAgent deep-audit engine (gRPC)" \
      org.opencontainers.image.source="https://github.com/timkush1/TrustAgent" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"

# uid/gid 1001 is shared with the proxy image: the proxy writes the internal
# token into the trustagent-internal volume (mounted read-only here) and the
# engine reads it. The directory exists in the image with that owner so an
# empty volume mounted there keeps it.
RUN groupadd --system --gid 1001 trustagent \
 && useradd --system --uid 1001 --gid 1001 --no-create-home --home-dir /nonexistent \
      --shell /usr/sbin/nologin trustagent \
 && install -d -o 1001 -g 1001 -m 0750 /var/lib/trustagent-internal

WORKDIR /app

# Application files stay root-owned and read-only for the service user.
COPY --from=build /app/.venv /app/.venv
COPY --from=build /app/src /app/src
COPY backend-python/data/ /app/data/
COPY backend-python/scripts/ /app/scripts/
# Evaluator templates, question sets, prompts, prices and the demo scenarios
# (mock judge fixtures). TRUTHTABLE_CONTRACTS_DIR=auto resolves /app/contracts.
COPY contracts/ /app/contracts/

ENV PATH="/app/.venv/bin:${PATH}" \
    PYTHONPATH="/app/src" \
    PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1

USER 1001:1001

# 50051: gRPC (proxy only). 8001: Prometheus metrics.
EXPOSE 50051 8001

# Real gRPC health probe (truthtable.healthcheck); the start period covers the
# first imports of the pipeline.
HEALTHCHECK --interval=10s --timeout=5s --start-period=60s --retries=5 \
    CMD ["python", "-m", "truthtable.healthcheck"]

CMD ["python", "-m", "truthtable.main"]
