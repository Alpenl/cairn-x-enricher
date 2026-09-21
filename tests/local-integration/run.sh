#!/usr/bin/env bash
# Real local integration: a real Worker (wrangler dev, real migrations, local
# D1/R2) plus the actual Go client, classifier and processor. Only the paid
# TypeSafe endpoint is replaced by an in-process contract mock.
#
# Usage: tests/local-integration/run.sh
# It is safe to run offline. The Worker is started on a random free port and
# every temporary file lives under a mktemp directory that is removed on exit.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
enricher_root="$(cd "$here/../.." && pwd)"
share_root="$(cd "$enricher_root/../cairn-share" && pwd)"

if [ ! -d "$share_root/worker/node_modules" ]; then
  echo "worker dependencies are not installed; run npm ci in $share_root/worker" >&2
  exit 1
fi

work="$(mktemp -d /tmp/opencode/cairn-local-integration.XXXXXX)"
worker_pid=""
cleanup() {
  if [ -n "$worker_pid" ] && kill -0 "$worker_pid" 2>/dev/null; then
    kill "$worker_pid" 2>/dev/null || true
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
  "name": "cairn-share-local-integration",
  "main": "$share_root/worker/src/index.ts",
  "compatibility_date": "2026-08-26",
  "d1_databases": [
    { "binding": "DB", "database_name": "cairn-share-local-integration", "database_id": "local-integration" }
  ],
  "r2_buckets": [
    { "binding": "ENRICHMENT_IMAGES", "bucket_name": "cairn-x-enrichment-images-local-integration" }
  ],
  "vars": { "CAIRN_API_TOKEN": "app", "CAIRN_ENRICHER_TOKEN": "internal" }
}
EOF

echo "== applying real migrations to the local D1 =="
(cd "$share_root/worker" && npx wrangler d1 migrations apply cairn-share-local-integration --local --config "$work/wrangler.jsonc" >"$work/migrations.log" 2>&1) \
  || { cat "$work/migrations.log"; exit 1; }

echo "== starting wrangler dev on 127.0.0.1:$port =="
(cd "$share_root/worker" && npx wrangler dev --local --port "$port" --ip 127.0.0.1 --config "$work/wrangler.jsonc" >"$work/dev.log" 2>&1) &
worker_pid=$!

ready=""
for _ in $(seq 1 120); do
  if ! kill -0 "$worker_pid" 2>/dev/null; then
    echo "wrangler dev exited early:" >&2
    cat "$work/dev.log" >&2
    exit 1
  fi
  code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/api/enrichment/jobs" -H 'Authorization: Bearer internal' || true)"
  if [ "$code" = "200" ]; then ready=1; break; fi
  sleep 1
done
if [ -z "$ready" ]; then
  echo "worker did not become ready:" >&2
  tail -40 "$work/dev.log" >&2
  exit 1
fi
echo "== worker ready =="

cd "$enricher_root"
CAIRN_WORKER_URL="http://127.0.0.1:$port" \
CAIRN_APP_TOKEN=app \
CAIRN_ENRICHER_TOKEN=internal \
go test ./tests/localintegration/ -run TestLocalWorkerFullLifecycle -count=1 -v
