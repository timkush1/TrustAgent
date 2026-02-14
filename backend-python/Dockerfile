# ============================================
# TruthTable Audit Engine - Python
# ============================================
# Multi-stage build:
#   Stage 1 (builder): Install dependencies into a venv
#   Stage 2 (runtime): Copy venv + app code into slim image
#
# WHY multi-stage? The builder stage has pip, compilers, etc.
# (hundreds of MB). The runtime stage only has Python + our deps.
# This makes the final image much smaller and more secure.

# --- Stage 1: Dependencies ---
FROM python:3.11-slim AS builder

# Install system dependencies needed for building Python packages
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy only dependency files first (Docker caching optimization)
# WHY? Docker caches each layer. If pyproject.toml hasn't changed,
# Docker reuses the cached layer and skips the slow pip install.
COPY pyproject.toml ./

# Create a virtual environment and install dependencies
RUN python -m venv /app/.venv && \
    /app/.venv/bin/pip install --no-cache-dir --upgrade pip && \
    /app/.venv/bin/pip install --no-cache-dir \
        grpcio>=1.60.0 \
        grpcio-tools>=1.60.0 \
        langgraph>=0.0.20 \
        langchain>=0.1.0 \
        langchain-community>=0.0.10 \
        pydantic>=2.5.0 \
        pydantic-settings>=2.1.0 \
        httpx>=0.26.0 \
        qdrant-client>=1.16.0 \
        redis>=5.0.0 \
        prometheus-client>=0.19.0 \
        sentence-transformers>=5.0.0 \
        torch>=2.0.0 \
        numpy>=1.24.0

# --- Stage 2: Application ---
FROM python:3.11-slim

WORKDIR /app

# Copy the virtual environment from builder
COPY --from=builder /app/.venv /app/.venv

# Copy application source code
COPY src/ /app/src/
COPY data/ /app/data/
COPY scripts/ /app/scripts/

# Create non-root user
RUN adduser --disabled-password --gecos '' --uid 1001 appuser && \
    chown -R appuser:appuser /app

# Make sure Python uses the virtual environment
ENV PATH="/app/.venv/bin:$PATH"
ENV PYTHONPATH="/app/src:$PYTHONPATH"
ENV PYTHONUNBUFFERED=1

USER appuser

# Expose gRPC port
EXPOSE 50051

# Run the audit engine
CMD ["python", "-m", "truthtable.main"]
