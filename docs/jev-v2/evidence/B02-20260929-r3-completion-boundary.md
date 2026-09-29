# R3-01: Go completion request through the final Worker/D1 race

The fixed Worker under test is Share `e0651504f242ac49b07bc2970d357052ea128510`.
The Go test is `TestLocalWorkerCompletionPreflightRace` in
`tests/localintegration/local_integration_test.go`; the runner starts a fresh
local Wrangler Worker and migrated D1 for each of `content`, `lease`, and
`target`.

The runner creates a temporary entrypoint that forwards to the unmodified
Worker. Only a completion request with the test header gets a D1 proxy. Its
first `batch` call is intercepted after the Worker's final preflight reads:

- `content`: increment only `links.content_revision`.
- `lease`: expire the claimed `classification_jobs.lease_until`.
- `target`: insert a new immutable target generation and advance the active
  pointer in one real D1 batch.

The original completion batch then executes in the real local D1. The test
uses the real Go classifier, Go client and HTTP serialization, with an
in-process TypeSafe contract fixture. For each race, the first completion
returns a conflict. The identical operation retried without the header still
conflicts, and its error is not `already_completed`. The D1 diagnostic read
shows `status=processing`, zero runs, zero decisions, zero operation receipts,
null `links.classification`, and an unchanged projection. There is exactly one
model request and two completion HTTP requests across the first attempt and
the retry. No real paid call is made.

`TestLocalWorkerProcessorCompletionPreflightRace` also runs the full staged
processor. Its successful control enables the entity extension and confirms
that a committed classification invokes the entity-state write. With the same
extension enabled, content and target races return a conflict through the
processor: `done=0`, `failed=1`, one model request, no entity-state write and
no success record. Replaying the exact serialized request captured from the
processor still conflicts and leaves the D1 state unchanged. Thus the control
makes the extension assertion observable rather than relying on the default
disabled configuration.
The `cancelcontent` variant cancels the scheduling context as the completion
HTTP call starts, then advances content at the same D1 barrier. The leased
processor work context still reaches the Worker, returns the conflict and
performs no entity extension or second model request.

`TestLocalWorkerClassificationCompletionSurvivesProcessExit` runs the staged
processor in a separate Go process. The child saves the exact serialized
completion request, lets the real Worker/D1 return HTTP 200, and exits before
the Go client reads that acknowledgment. A new Go client sees one stored run
and decision, cannot claim the completed job, and can replay the saved request
for HTTP 200 without another model request or run. Two concurrent identical
replays also return 200; the same key with a changed valid suggestion returns
`operation_conflict`, and a different key cannot claim the completed job.
The model fixture counts one call across both processes.

The existing Worker `completion-atomicity.test.ts` separately checks that
legacy and v2 jobs can be reclaimed after each failed guard, plus normal
lost-response recovery, SQL/transaction failure, and concurrent duplicate
operations. The existing `TestLocalWorkerFullLifecycle` checks a real Go
client's lost HTTP response after a successful D1 commit: one model request,
two identical completion requests, and one persisted run/decision.

Verification:

```text
CAIRN_INTEGRATION_CASE=completionracecontent tests/local-integration/run.sh  PASS
CAIRN_INTEGRATION_CASE=completionracelease   tests/local-integration/run.sh  PASS
CAIRN_INTEGRATION_CASE=completionracetarget  tests/local-integration/run.sh  PASS
CAIRN_INTEGRATION_CASE=completionraceprocessorsuccess tests/local-integration/run.sh  PASS
CAIRN_INTEGRATION_CASE=completionraceprocessorcontent tests/local-integration/run.sh  PASS
CAIRN_INTEGRATION_CASE=completionraceprocessortarget  tests/local-integration/run.sh  PASS
CAIRN_INTEGRATION_CASE=completionraceprocessorcancelcontent tests/local-integration/run.sh  PASS
CAIRN_INTEGRATION_CASE=completioncrash tests/local-integration/run.sh  PASS
make verify             PASS (vet, lint, race, 488/488 frontend, build)
make test-ablation      PASS
```

The process-exit case is a successful D1 commit with its HTTP acknowledgment
lost. It does not prove cross-process replay after a rejected stale result;
the latter must first obtain a new lease and matching evidence. The local
wrapper and diagnostic endpoint are created under the runner's temporary
directory; no production Worker route or remote D1 is changed. The runner
disables Wrangler telemetry and points its npm update check at a closed local
port so local migrations have no remote network dependency.
