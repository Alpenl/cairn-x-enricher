# B02: provider reservation response boundaries (2026-09-29)

`TestLocalWorkerProviderReservationResponseBoundaries` uses the Go Responses adapter, real Worker HTTP API and local D1. A local provider fixture counts POSTs and would answer with an error if reached. The test claims a source job and admits its paid stage before injecting transport faults at the Worker reservation endpoint.

When the transport fails before forwarding the reservation request, the Worker ledger has no attempt and the provider receives no POST. The test then repeats the same leased model operation, this time letting Worker commit the reservation while discarding its successful HTTP response. The ledger retains one `fetch` attempt in `reserved` state; the provider still receives no POST. A new Go client retries the same operation, receives no second send permit and sends no POST. After local D1 expires the lease, a new claim is blocked by the unresolved reservation. The provider POST count remains zero throughout.

`CAIRN_INTEGRATION_CASE=providerreserve bash tests/local-integration/run.sh` and `make verify` passed. This covers the two reservation response boundaries in the local Go→Worker/D1 path. It does not use a real provider or exercise operator reconciliation, and it does not replace the remaining paid-call acceptance matrix and production measurements.
