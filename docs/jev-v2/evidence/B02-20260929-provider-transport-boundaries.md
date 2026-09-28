# B02: provider transport boundary events (2026-09-29)

This change adds two fixed-field Go events between the existing durable paid-attempt reservation and settlement events:

- `provider_attempt_dispatching` is emitted immediately before entering `http.Client.Do`. It does **not** prove that the request reached the provider, and a network error can still follow provider execution.
- `provider_response_headers_received` is emitted only when `Do` returns an HTTP response. It records the HTTP status, before response-body decoding or durable settlement. It does **not** prove that the body was usable or that accounting was committed.

The existing `provider_attempt_responded` event follows successful settlement. A transport error, decode error, or settlement failure keeps the existing `provider_attempt_unknown` outcome. The durable Worker ledger and provider records remain the accounting source of truth. These optional, bounded logs can be dropped and carry no operation key or response ID, so they cannot establish per-attempt billing or cross-process trace identity.

The events use the existing hot-controlled logger and its fixed-field allowlist. The new fields are limited to the event name, stage, provider variant, duration, and (for returned headers) HTTP status. Raw request/response bodies, URLs, credentials, leases, operation keys, and response IDs are excluded.

Verification: `make verify` passed (Go vet, golangci-lint with zero issues, race/coverage tests, 488 frontend checks, and build). Focused tests assert event order for a successful paid source request, no headers event after a lost response, a headers event without a completed event when settlement fails, and safe-handler removal of private provider fields. No live provider request, deployment, or same-load logging-overhead measurement was performed.
