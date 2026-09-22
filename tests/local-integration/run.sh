#!/usr/bin/env bash
# Real local integration: a real Worker (wrangler dev, real migrations, local
# D1/R2) plus the actual Go client, classifier and processor. Only the paid
# TypeSafe endpoint is replaced by an in-process contract mock.
#
# Each test function runs against its own fresh Worker/D1 so target-generation
# state from one scenario cannot leak into another.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
enricher_root="$(cd "$here/../.." && pwd)"
share_root="$(cd "${CAIRN_SHARE_ROOT:-$enricher_root/../cairn-share}" && pwd)"

if [ ! -d "$share_root/worker/node_modules" ]; then
  echo "worker dependencies are not installed; run npm ci in $share_root/worker" >&2
  exit 1
fi

work_root="$(mktemp -d /tmp/opencode/cairn-local-integration.XXXXXX)"
worker_pid=""
cleanup() {
  if [ -n "$worker_pid" ] && kill -0 "$worker_pid" 2>/dev/null; then
    kill -- "-$worker_pid" 2>/dev/null || true
    wait "$worker_pid" 2>/dev/null || true
  fi
  rm -rf "$work_root"
}
trap cleanup EXIT

free_port() {
  python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
}

start_worker() {
  local name="$1" port="$2" work="$3"
  mkdir -p "$work"
  cp -r "$share_root/worker/migrations" "$work/migrations"
  cat > "$work/wrangler.jsonc" <<EOF
{
  "\$schema": "$share_root/worker/node_modules/wrangler/config-schema.json",
  "name": "cairn-share-$name",
  "main": "$share_root/worker/src/index.ts",
  "compatibility_date": "2026-08-26",
  "d1_databases": [
    { "binding": "DB", "database_name": "cairn-share-$name", "database_id": "$name" }
  ],
  "r2_buckets": [
    { "binding": "ENRICHMENT_IMAGES", "bucket_name": "cairn-x-enrichment-images-$name" }
  ],
  "vars": { "CAIRN_API_TOKEN": "app", "CAIRN_ENRICHER_TOKEN": "internal" }
}
EOF
  (cd "$share_root/worker" && npx wrangler d1 migrations apply "cairn-share-$name" --local --config "$work/wrangler.jsonc" >"$work/migrations.log" 2>&1) \
    || { cat "$work/migrations.log"; exit 1; }
  if [ "$name" = "imageprivacy" ]; then
    # A synthetic 1px PNG seeded through the real local R2 CLI. The test
    # creates/deletes its owner through authenticated Worker HTTP.
    python3 -c 'import base64,sys; open(sys.argv[1], "wb").write(base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aM7sAAAAASUVORK5CYII="))' "$work/pixel.png"
    (cd "$share_root/worker" && npx wrangler r2 object put "cairn-x-enrichment-images-$name/enrichment/1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png" --local --config "$work/wrangler.jsonc" --file "$work/pixel.png" --content-type image/png >"$work/seed-image.log" 2>&1) \
      || { cat "$work/seed-image.log"; exit 1; }
  fi
  (cd "$share_root/worker" && exec setsid "$share_root/worker/node_modules/.bin/wrangler" dev --local --port "$port" --ip 127.0.0.1 --config "$work/wrangler.jsonc" >"$work/dev.log" 2>&1) &
  worker_pid=$!
  local ready=""
  for _ in $(seq 1 120); do
    if ! kill -0 "$worker_pid" 2>/dev/null; then
      echo "wrangler dev exited early:" >&2; cat "$work/dev.log" >&2; exit 1
    fi
    local code
    code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/api/enrichment/jobs" -H 'Authorization: Bearer internal' || true)"
    if [ "$code" = "200" ]; then ready=1; break; fi
    sleep 1
  done
  if [ -z "$ready" ]; then
    echo "worker did not become ready:" >&2; tail -40 "$work/dev.log" >&2; exit 1
  fi
}

stop_worker() {
  if [ -n "$worker_pid" ] && kill -0 "$worker_pid" 2>/dev/null; then
    kill -- "-$worker_pid" 2>/dev/null || true
    wait "$worker_pid" 2>/dev/null || true
  fi
  worker_pid=""
}

run_case() {
  local name="$1" test_name="$2"
  if [ -n "${CAIRN_INTEGRATION_CASE:-}" ] && [ "$CAIRN_INTEGRATION_CASE" != "$name" ]; then return; fi
  local port work
  port="$(free_port)"
  work="$work_root/$name"
  echo "== $test_name: starting worker on 127.0.0.1:$port =="
  start_worker "$name" "$port" "$work"
  (
    cd "$enricher_root"
    CAIRN_WORKER_URL="http://127.0.0.1:$port" \
    CAIRN_APP_TOKEN=app \
    CAIRN_ENRICHER_TOKEN=internal \
    go test ./tests/localintegration/ -run "$test_name" -count=1 -v
  )
  stop_worker
}

run_case lifecycle TestLocalWorkerFullLifecycle
run_case competition TestLocalWorkerVersionCompetition
run_case rename TestLocalWorkerDisplayRenameKeepsSemantics
run_case evidence TestLocalWorkerEvidenceCheckpointAndBoundRead

run_case entities TestLocalWorkerEntitySnapshotIdentity

run_case reuse TestLocalWorkerStoredQuestionReuse

run_case decisions TestLocalWorkerDecisionReferences

run_case escalation TestLocalWorkerEvidenceExecutionRecovery

run_case filters TestLocalWorkerEffectiveClientFilters

run_case personal TestLocalWorkerObjectivePersonalBoundary

run_case cli TestLocalWorkerClassifyCLI

run_case carrier TestLocalWorkerCarrierDefinitionUpgrade

run_case imageprivacy TestLocalWorkerPrivateImageLifecycle

run_case extensionbudget TestLocalWorkerExtensionBudgetAcrossProcesses

run_case rerankcache TestLocalWorkerRerankCacheAcrossProcesses
