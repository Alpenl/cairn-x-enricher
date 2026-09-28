# B02: provider result remains unknown after a process kill (2026-09-29)

`TestLocalWorkerUnknownProviderResultSurvivesProcessKill` exercises the real Go Responses adapter, Worker HTTP API and local D1. Only the paid provider is an in-process HTTP fixture. The fixture reads the complete POST body and withholds its response. The parent kills the separate Go worker process at that boundary, then checks the durable Worker state.

The fixture received one POST. The Worker provider-attempt ledger retained one `fetch` reservation with no response ID and reported one unresolved paid attempt. After the test expired the old lease in local D1, a fresh Go client could not claim that bookmark; the fixture still counted one POST. The test used `CAIRN_INTEGRATION_CASE=providerunknown bash tests/local-integration/run.sh` and passed. `make verify` also passed.

This proves the unknown-result boundary for one source attempt across an actual process kill. It does not establish whether a real provider would bill that POST. No real provider request, deployment or remote D1 migration occurred. The remaining acceptance matrix includes a response received before settlement failure, repeated restarts, canary accounting, fallback limits, backlog and production same-load measurements.
