# syntax=docker/dockerfile:1
# ============================================================================
# TrustAgent dashboard (React): static build served by unprivileged nginx.
# ============================================================================
# Build context: the REPOSITORY ROOT (SPEC §3.4), because the build imports
# contracts/ (the @contracts alias) and docs/benchmarks/ (@bench). Build it the
# way compose and CI do:
#
#     docker build -f frontend-react/Dockerfile .
#
# The context is filtered by frontend-react/Dockerfile.dockerignore.
#
# The browser talks to the dashboard's own origin only: nginx proxies /v1,
# /api, /ws and /health to the proxy (DASHBOARD_PROXY_UPSTREAM), so no API URL
# or key is compiled into the bundle. Runtime settings reach the app through
# /config.json, written at container start (nginx/entrypoint.d).

ARG NODE_VERSION=22
ARG NGINX_VERSION=1.31

# --- Stage 1: build -----------------------------------------------------------
FROM --platform=$BUILDPLATFORM node:${NODE_VERSION}-alpine AS build

WORKDIR /src/frontend-react
COPY frontend-react/package.json frontend-react/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci --no-audit --no-fund

# Sources (frontend-react/, contracts/, docs/benchmarks/ when present) keep the
# repository layout, so the ../contracts and ../docs/benchmarks aliases resolve.
WORKDIR /src
COPY . .

WORKDIR /src/frontend-react
# Build-time switches (SPEC §2.7, group deploy): full app vs static site, and
# the base path (GitHub Pages uses /TrustAgent/).
ARG VITE_APP_MODE=full
ARG VITE_BASE_PATH=/
ENV VITE_APP_MODE=${VITE_APP_MODE} \
    VITE_BASE_PATH=${VITE_BASE_PATH}
RUN npm run build

# --- Stage 2: serve -----------------------------------------------------------
# nginx-unprivileged runs as uid 101, keeps its pid and temp files in /tmp, and
# lets that user render templates into /etc/nginx/conf.d.
FROM nginxinc/nginx-unprivileged:${NGINX_VERSION}-alpine

ARG VERSION=dev
LABEL org.opencontainers.image.title="truthtable-dashboard" \
      org.opencontainers.image.description="TrustAgent dashboard" \
      org.opencontainers.image.source="https://github.com/timkush1/TrustAgent" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"

# Container settings (SPEC §2.7, dashboard keys). The entrypoint renders
# /etc/nginx/templates/*.template with envsubst, restricted by
# NGINX_ENVSUBST_FILTER to these keys plus the resolver list, so nginx's own
# $variables are left alone. The local-resolvers hook exports
# NGINX_LOCAL_RESOLVERS from /etc/resolv.conf (Docker's embedded DNS), which
# lets nginx re-resolve the proxy when its container is recreated.
ENV DASHBOARD_API_BASE="" \
    DASHBOARD_WS_URL="" \
    DASHBOARD_PROXY_UPSTREAM="http://proxy:8080" \
    DASHBOARD_CONNECT_SRC="" \
    NGINX_ENVSUBST_FILTER="^(DASHBOARD_|NGINX_LOCAL_RESOLVERS)" \
    NGINX_ENTRYPOINT_LOCAL_RESOLVERS=1 \
    NGINX_ENTRYPOINT_QUIET_LOGS=1

USER 0
# The packaged default server is replaced by the template; /etc/nginx/runtime
# receives /config.json at start (the html directory stays read-only).
RUN rm -f /etc/nginx/conf.d/default.conf \
 && install -d -o 101 -g 0 -m 0775 /etc/nginx/runtime
COPY frontend-react/nginx/templates/ /etc/nginx/templates/
COPY --chmod=0755 frontend-react/nginx/entrypoint.d/ /docker-entrypoint.d/
COPY --from=build /src/frontend-react/dist/ /usr/share/nginx/html/
USER 101

EXPOSE 3000

# /healthz is answered by nginx itself (it does not depend on the proxy).
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=3 \
    CMD ["wget", "-q", "-O", "/dev/null", "http://127.0.0.1:3000/healthz"]
