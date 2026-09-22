#!/usr/bin/env bash
# Real browser end-to-end: starts the real Worker (wrangler dev, real
# migrations, local D1/R2), builds and starts the real Go service, and drives
# the actual dashboard in a real Chrome. Only the paid model endpoints are
# replaced by tests/local-integration/mock-model.mjs.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
enricher_root="$(cd "$here/../.." && pwd)"
share_root="$(cd "$enricher_root/../cairn-share" && pwd)"

if [ ! -d "$share_root/worker/node_modules" ]; then
  echo "worker dependencies are not installed; run npm ci in $share_root/worker" >&2
  exit 1
fi

work="$(mktemp -d /tmp/opencode/cairn-browser-e2e.XXXXXX)"
worker_pid=""
cleanup() {
  if [ -n "$worker_pid" ] && kill -0 "$worker_pid" 2>/dev/null; then
    kill -- "-$worker_pid" 2>/dev/null || true
    wait "$worker_pid" 2>/dev/null || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

port="$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)"

cp -r "$share_root/worker/migrations" "$work/migrations"
cat > "$work/wrangler.jsonc" <<EOF
{
  "\$schema": "$share_root/worker/node_modules/wrangler/config-schema.json",
  "name": "cairn-share-browser-e2e",
  "main": "$share_root/worker/src/index.ts",
  "compatibility_date": "2026-08-26",
  "d1_databases": [
    { "binding": "DB", "database_name": "cairn-share-browser-e2e", "database_id": "browser-e2e" }
  ],
  "r2_buckets": [
    { "binding": "ENRICHMENT_IMAGES", "bucket_name": "cairn-x-enrichment-images-browser-e2e" }
  ],
  "vars": { "CAIRN_API_TOKEN": "app", "CAIRN_ENRICHER_TOKEN": "internal" }
}
EOF

echo "== applying real migrations to the local D1 =="
(cd "$share_root/worker" && npx wrangler d1 migrations apply cairn-share-browser-e2e --local --config "$work/wrangler.jsonc" >"$work/migrations.log" 2>&1) \
  || { cat "$work/migrations.log"; exit 1; }

echo "== starting wrangler dev on 127.0.0.1:$port =="
(cd "$share_root/worker" && exec setsid "$share_root/worker/node_modules/.bin/wrangler" dev --local --port "$port" --ip 127.0.0.1 --config "$work/wrangler.jsonc" >"$work/dev.log" 2>&1) &
worker_pid=$!
ready=""
for _ in $(seq 1 120); do
  if ! kill -0 "$worker_pid" 2>/dev/null; then
    echo "wrangler dev exited early:" >&2; cat "$work/dev.log" >&2; exit 1
  fi
  code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/api/enrichment/jobs" -H 'Authorization: Bearer internal' || true)"
  if [ "$code" = "200" ]; then ready=1; break; fi
  sleep 1
done
if [ -z "$ready" ]; then
  echo "worker did not become ready:" >&2; tail -40 "$work/dev.log" >&2; exit 1
fi

echo "== building the real Go service =="
(cd "$enricher_root" && go build -o "$work/cairn-x-enricher" ./cmd/cairn-x-enricher)

echo "== driving the real browser against the real stack =="
cd "$enricher_root"
CAIRN_WORKER_URL="http://127.0.0.1:$port" \
CAIRN_APP_TOKEN=app \
CAIRN_ENRICHER_TOKEN=internal \
GO_BIN="$work/cairn-x-enricher" \
CHROME_PATH="${CHROME_PATH:-$(command -v google-chrome || command -v chromium || command -v chromium-browser)}" \
node tests/local-integration/browser-e2e.mjs
