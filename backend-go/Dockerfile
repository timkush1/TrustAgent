# ============================================
# TruthTable Proxy - Go
# ============================================
# Multi-stage build:
#   Stage 1: Compile Go binary (needs Go compiler + deps)
#   Stage 2: Copy binary into tiny alpine image
#
# WHY? Go compiles to a single static binary, so the final image
# can be extremely small (~20MB) compared to the builder (~1GB).

# --- Stage 1: Build ---
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

# Copy dependency files first (Docker caching)
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
# CGO_ENABLED=0: static binary (no C library dependencies)
# -ldflags="-s -w": strip debug info for smaller binary
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /proxy \
    ./cmd/proxy

# --- Stage 2: Runtime ---
FROM alpine:3.19

# Install CA certificates (needed for HTTPS upstream calls)
RUN apk add --no-cache ca-certificates wget

# Create non-root user
RUN adduser -D -u 1001 appuser

WORKDIR /app

# Copy the compiled binary
COPY --from=builder /proxy /app/proxy
RUN chown appuser:appuser /app/proxy

USER appuser

# Expose ports (HTTP proxy + WebSocket)
EXPOSE 8080 8081

# Health check
HEALTHCHECK --interval=10s --timeout=3s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

# Run the proxy
CMD ["/app/proxy"]
