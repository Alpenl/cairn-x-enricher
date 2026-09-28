# B02: provider response received, settlement failed (2026-09-29)

`TestLocalWorkerProviderSettlementFailureSurvivesProcessExit` exercises the Go staged processor, real Worker HTTP API and local D1. A local provider fixture reads one complete POST and returns a completed response with an ID and usage. In a separate Go process, the Worker client's settlement request is intercepted and answered with a fixture 503 before it reaches Worker. The processor reports an error and that Go process exits.

The provider fixture counted one POST. The Worker ledger retained a `fetch` reservation with no settled response ID and reported one unresolved paid attempt. After the old lease expired in local D1, a fresh Go client could not claim the bookmark and the provider POST count remained one. `CAIRN_INTEGRATION_CASE=providersettle bash tests/local-integration/run.sh` and `make verify` passed.

This covers a completed provider response followed by a failed settlement request across processes. It does not prove recovery of the returned response, nor does it use a real paid provider or a remote deployment. Operator reconciliation, response-loss after a successful settlement, repeated restarts and production cost/performance evidence remain separate acceptance work.
