# B05/B06 S3: batched effective views for Markdown export

## Scope

The Go export previously fetched one effective view per bookmark. A 500-item
export made five list requests and up to 500 effective-view requests, with four
view requests in flight. The new Worker endpoint accepts at most 50 IDs and
folds their effective views in one SQLite statement. Go uses ten batch requests
for 500 items and falls back to the per-item endpoint when an older Worker
returns 404 or 405. The endpoint requires the Enricher token; App tokens cannot
read it. The response omits source bodies and reports D1 metadata explicitly
scoped to the effective-view query. The Worker sets an exact UTF-8 response
length so request diagnostics can record response bytes.

The batch and single-link endpoints share the same selection SQL and reducer.
A missing view from a successful batch makes export fail instead of returning a
file with silently missing tags. For a v1-only Worker, export marks each
effective view unavailable and counts it as partial.

## Local evidence

The 50-link workload in `worker/test/effective-batch-workload.test.ts` adds
1, 20, then 100 human history rows per link. These are local Workerd/D1 timings
from one run on 2026-09-29. The comparison uses 50 single-ID SQL calls in
groups of four; it excludes HTTP and Go rendering. No model calls were made.

| History rows per link | Batch SQL | 50 single SQL | Batch rows read | Single rows read | Batch elapsed | Singles elapsed | Batch response |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 1 | 50 | 299 | 249 | 8 ms | 203 ms | 17,815 bytes |
| 20 | 1 | 50 | 1,249 | 1,199 | 9 ms | 199 ms | 17,866 bytes |
| 100 | 1 | 50 | 5,249 | 5,199 | 14 ms | 218 ms | 17,916 bytes |

The batch removes 49 SQL invocations per 50 views but does **not** reduce rows
read in this fixture: its read count is 50 higher. Query work still grows with
history length, so these numbers do not justify the later A2 reading-model
change or prove the foreground p95 goal. The response remains nearly constant
in size because it does not serialize history rows.

Worker tests compare the exact batch payload to each single-link payload,
including legacy projection, a human override, missing IDs, access control and
invalid input. A Go test verifies that 500 items cause ten batch calls and zero
single calls, while an old Worker causes one failed batch probe followed by
500 single calls. The real local Wrangler/D1 ↔ Go filter/export scenario also
compares batch and single views and completed without model calls.

Verification on the working branches: Worker 36 test files / 364 tests,
TypeScript typecheck and Wrangler deploy dry-run passed. Go `make verify`
passed, including race tests, lint, 488/488 frontend checks and build. The
`filters` real local Wrangler/D1 ↔ Go integration case passed. The full Worker
run printed Workerd cleanup exceptions from `deleteAllDurableObjects()` but
completed with exit 0 and every test passing. No remote D1 migration occurred.

## Remaining acceptance

The full #20 S3 and #16 performance gates remain open. Measure actual Worker
memory, complete request-level SQL counts (including observability policy
refresh), response size and duration at 2,000/10,000 bookmarks and varied
history/body sizes. Compare the same absolute open, closed and mixed loads with
the old and new export path, including foreground p95/p99, failures and D1
queueing. These local timings are a direction check, not a production latency
claim. Deployment, remote D1 migration and paid calls were not performed.
