#!/bin/sh
# Writes the dashboard's runtime settings to /etc/nginx/runtime/config.json,
# which nginx serves as /config.json (SPEC §3.3). Run by the nginx image's
# /docker-entrypoint.sh after the templates are rendered.
#
# Only non-secret settings go here; the file is public:
#   apiBase  <- DASHBOARD_API_BASE  ("" = same origin, the default)
#   wsUrl    <- DASHBOARD_WS_URL    ("" = derived from the page origin)
# A cross-origin value also needs DASHBOARD_CONNECT_SRC for the CSP.
#
# An invalid value stops the container, so a typo is visible at start instead
# of as a dashboard that cannot reach the API.
set -eu

ME=$(basename "$0")
OUT_DIR=/etc/nginx/runtime
OUT="$OUT_DIR/config.json"

log() {
    if [ -z "${NGINX_ENTRYPOINT_QUIET_LOGS:-}" ]; then
        echo "$ME: $*"
    fi
}

fail() {
    echo "$ME: ERROR: $*" >&2
    exit 1
}

# check_url NAME VALUE SCHEMES: VALUE is empty, or an absolute URL with one of
# SCHEMES and none of the characters that would need escaping in JSON.
check_url() {
    name=$1
    value=$2
    schemes=$3
    [ -n "$value" ] || return 0
    case "$value" in
        *[![:graph:]]* | *\"* | *\\*)
            fail "$name must be a URL without spaces, quotes or backslashes" ;;
    esac
    scheme=${value%%://*}
    if [ "$scheme" = "$value" ] || [ -z "${value#*://}" ]; then
        fail "$name must be an absolute URL ($schemes), got: $value"
    fi
    for allowed in $schemes; do
        if [ "$scheme" = "$allowed" ]; then
            return 0
        fi
    done
    fail "$name must start with one of: $schemes (got $scheme://)"
}

api_base=${DASHBOARD_API_BASE:-}
ws_url=${DASHBOARD_WS_URL:-}
check_url DASHBOARD_API_BASE "$api_base" "http https"
check_url DASHBOARD_WS_URL "$ws_url" "ws wss"

tmp="$OUT.tmp"
printf '{"apiBase":"%s","wsUrl":"%s"}\n' "$api_base" "$ws_url" > "$tmp"
mv -f "$tmp" "$OUT"
log "wrote $OUT (apiBase=${api_base:-same origin}, wsUrl=${ws_url:-derived})"
