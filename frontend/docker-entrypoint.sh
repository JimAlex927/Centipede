#!/bin/sh
set -eu

escape_json() {
  printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

cat > /usr/share/nginx/html/config.js <<EOF
window.CONFIG = Object.assign({}, window.CONFIG || {}, {
  API_BASE_URL: "$(escape_json "${API_BASE_URL:-}")",
  REALTIME_URL: "$(escape_json "${REALTIME_URL:-}")",
  COLLAB_URL: "$(escape_json "${COLLAB_URL:-}")"
});
EOF

exec "$@"
