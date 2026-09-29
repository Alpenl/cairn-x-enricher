#!/usr/bin/env bash
# Real local integration: a real Worker (wrangler dev, real migrations, local
# D1/R2) plus the actual Go client, classifier and processor. Only the paid
# TypeSafe endpoint is replaced by an in-process contract mock.
#
# Each test function runs against its own fresh Worker/D1 so target-generation
# state from one scenario cannot leak into another.
set -euo pipefail
# This runner uses only local workerd/D1. Wrangler telemetry and its npm
# version check would add unrelated network dependencies after migration.
export WRANGLER_SEND_METRICS=false
export npm_config_registry=http://127.0.0.1:9

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
enricher_root="$(cd "$here/../.." && pwd)"
share_root="$(cd "${CAIRN_SHARE_ROOT:-$enricher_root/../cairn-share}" && pwd)"

if [ ! -d "$share_root/worker/node_modules" ]; then
  echo "worker dependencies are not installed; run npm ci in $share_root/worker" >&2
  exit 1
fi

work_root="$(mktemp -d "${TMPDIR:-/tmp}/cairn-local-integration.XXXXXX")"
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
  local entry="$share_root/worker/src/index.ts"
  mkdir -p "$work"
  cp -r "$share_root/worker/migrations" "$work/migrations"
  if [[ "$name" == completionrace* ]]; then
    # A local-only entrypoint injects scheduling after the real Worker's final
    # preflight. Production code and its deployed entrypoint remain untouched.
    entry="$work/entry.ts"
    cat > "$entry" <<EOF
import worker from "$share_root/worker/src/index.ts";

export default {
  scheduled: worker.scheduled,
  async fetch(request: Request, env: any): Promise<Response> {
    const diagnostic = request.method === "GET" &&
      new URL(request.url).pathname.match(/^\\/__test__\\/completion-race\\/(\\d+)$/);
    if (diagnostic && request.headers.get("Authorization") === "Bearer " + env.CAIRN_ENRICHER_TOKEN) {
      const id = Number(diagnostic[1]);
      const [job, link, projection, runs, decisions, operations] = await Promise.all([
        env.DB.prepare("SELECT status FROM classification_jobs WHERE link_id=?").bind(id).first(),
        env.DB.prepare("SELECT classification FROM links WHERE id=?").bind(id).first(),
        env.DB.prepare("SELECT effective FROM current_projections WHERE link_id=?").bind(id).first(),
        env.DB.prepare("SELECT COUNT(*) AS n FROM classification_runs WHERE link_id=?").bind(id).first(),
        env.DB.prepare("SELECT COUNT(*) AS n FROM classification_decisions WHERE link_id=?").bind(id).first(),
        env.DB.prepare("SELECT COUNT(*) AS n FROM classification_operations WHERE link_id=?").bind(id).first()
      ]);
      return Response.json({ status: job?.status, classification: link?.classification,
        projection: projection?.effective ?? null, runs: runs?.n, decisions: decisions?.n,
        operations: operations?.n });
    }
    const match = request.method === "POST" &&
      new URL(request.url).pathname.match(/^\\/api\\/enrichment\\/classifications\\/(\\d+)\\/complete$/);
    const change = request.headers.get("X-Cairn-Test-Completion-Race");
    if (!match || !["target", "content", "lease"].includes(change ?? ""))
      return worker.fetch(request, env);
    const id = Number(match[1]);
    let batches = 0;
    const db = new Proxy(env.DB, {
      get(target, property) {
        if (property === "batch") return async (statements: D1PreparedStatement[]) => {
          batches++;
          if (batches === 1) {
            if (change === "content") {
              await target.prepare("UPDATE links SET content_revision=content_revision+1 WHERE id=?").bind(id).run();
            } else if (change === "lease") {
              await target.prepare("UPDATE classification_jobs SET lease_until='2000-01-01' WHERE link_id=?").bind(id).run();
            } else {
              await target.batch([
                target.prepare(\`INSERT INTO classification_targets
                  (generation,spec_id,spec_hash,taxonomy_version,policy_version,requested_model,protocol,created_at,note)
                  SELECT s.generation+1,t.spec_id,t.spec_hash,t.taxonomy_version,
                    'r3-race',t.requested_model,t.protocol,
                    strftime('%Y-%m-%dT%H:%M:%fZ','now'),'integration target race'
                  FROM classification_target_state s JOIN classification_targets t ON t.generation=s.generation WHERE s.id=1\`),
                target.prepare("UPDATE classification_target_state SET generation=generation+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1")
              ]);
            }
          }
          return target.batch(statements);
        };
        const value = Reflect.get(target, property);
        return typeof value === "function" ? value.bind(target) : value;
      }
    });
    const response = await worker.fetch(request, { ...env, DB: db });
    response.headers.set("X-Cairn-Test-Barrier-Batches", String(batches));
    return response;
  }
};
EOF
  fi
  cat > "$work/wrangler.jsonc" <<EOF
{
  "\$schema": "$share_root/worker/node_modules/wrangler/config-schema.json",
  "name": "cairn-share-$name",
  "main": "$entry",
  "compatibility_date": "2026-08-26",
  "d1_databases": [
    { "binding": "DB", "database_name": "cairn-share-$name", "database_id": "$name" }
  ],
  "r2_buckets": [
    { "binding": "ENRICHMENT_IMAGES", "bucket_name": "cairn-x-enrichment-images-$name" }
  ],
  "vars": { "CAIRN_API_TOKEN": "app", "CAIRN_ENRICHER_TOKEN": "internal", "CAIRN_OPERATOR_TOKEN": "operator" }
}
EOF
  (cd "$share_root/worker" && ./node_modules/.bin/wrangler d1 migrations apply "cairn-share-$name" --local --config "$work/wrangler.jsonc" >"$work/migrations.log" 2>&1) \
    || { cat "$work/migrations.log"; exit 1; }
  if [ "$name" = "imageprivacy" ]; then
    # A synthetic 1px PNG seeded through the real local R2 CLI. The test
    # creates/deletes its owner through authenticated Worker HTTP.
    python3 -c 'import base64,sys; open(sys.argv[1], "wb").write(base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aM7sAAAAASUVORK5CYII="))' "$work/pixel.png"
    (cd "$share_root/worker" && ./node_modules/.bin/wrangler r2 object put "cairn-x-enrichment-images-$name/enrichment/1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png" --local --config "$work/wrangler.jsonc" --file "$work/pixel.png" --content-type image/png >"$work/seed-image.log" 2>&1) \
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
  local port work race_change
  port="$(free_port)"
  work="$work_root/$name"
  race_change="${name#completionrace}"
  race_change="${race_change#processor}"
  echo "== $test_name: starting worker on 127.0.0.1:$port =="
  start_worker "$name" "$port" "$work"
  (
    cd "$enricher_root"
    CAIRN_WORKER_URL="http://127.0.0.1:$port" \
    CAIRN_WRANGLER_CONFIG="$work/wrangler.jsonc" \
    CAIRN_SHARE_ROOT="$share_root" \
    CAIRN_RACE_CHANGE="$race_change" \
    CAIRN_APP_TOKEN=app \
    CAIRN_ENRICHER_TOKEN=internal \
    go test ./tests/localintegration/ -run "$test_name" -count=1 -v
  )
  stop_worker
}

run_case lifecycle TestLocalWorkerFullLifecycle
run_case completionracecontent TestLocalWorkerCompletionPreflightRace
run_case completionracelease TestLocalWorkerCompletionPreflightRace
run_case completionracetarget TestLocalWorkerCompletionPreflightRace
run_case completionraceprocessorsuccess TestLocalWorkerProcessorCompletionPreflightRace
run_case completionraceprocessorcontent TestLocalWorkerProcessorCompletionPreflightRace
run_case completionraceprocessortarget TestLocalWorkerProcessorCompletionPreflightRace
run_case completionraceprocessorcancelcontent TestLocalWorkerProcessorCompletionPreflightRace
run_case completioncrash TestLocalWorkerClassificationCompletionSurvivesProcessExit
run_case halfopen TestLocalWorkerHalfOpenClaimFaultsAndIndependentSource
run_case retryafter TestLocalWorkerProviderRetryHintSurvivesProcessorRestart
run_case sourcelease TestLocalWorkerSourceLeaseAdmission
run_case sourcegate TestLocalWorkerSourceGateKeepsReadingAvailable
run_case localstage TestLocalWorkerInstanceStagePauseDoesNotBlockHealthyInstance
run_case refreshcheckpoint TestLocalWorkerRefreshCheckpointConsumesIntent
run_case refreshcrash TestLocalWorkerRefreshCheckpointSurvivesProcessExit
run_case providerledger TestLocalWorkerProviderAttemptLedger
run_case providerreserve TestLocalWorkerProviderReservationResponseBoundaries
run_case providerunknown TestLocalWorkerUnknownProviderResultSurvivesProcessKill
run_case providersettle TestLocalWorkerProviderSettlementFailureSurvivesProcessExit
run_case providerrecovery TestLocalWorkerProviderReadingRecovery
run_case manualrestart TestLocalWorkerManualSourceSurvivesProcessExit
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
run_case onceempty TestLocalWorkerOnceSkipsEmptySourceCanary
run_case servetarget TestLocalWorkerServeTargetSwitchWithManualSource

run_case carrier TestLocalWorkerCarrierDefinitionUpgrade

run_case imageprivacy TestLocalWorkerPrivateImageLifecycle

run_case extensionbudget TestLocalWorkerExtensionBudgetAcrossProcesses

run_case rerankcache TestLocalWorkerRerankCacheAcrossProcesses

run_case classificationbudget TestLocalWorkerClassificationBudgetAcrossProcesses

run_case entitycache TestLocalWorkerEntityCacheCLI

run_case sourcerevision TestLocalWorkerSourceRevisionOnce

run_case canonicalentities TestLocalWorkerCanonicalEntitiesCLI
