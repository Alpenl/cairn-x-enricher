# B02-T08: settled reading recovery (local slice)

`provider-recover-reading --operation-key ...` defaults to validation without
writing business state. It inspects a settled, ledger-bound reading permit,
reads the current persisted source, then retrieves the provider's saved
response by its exact response ID. It verifies the response ID, model and
completed status and decodes the reading against that source. Only
`--commit --actor ...` sends the reading fields through the separate Operator
Token. The command never makes a model POST and prints only a safe phase,
source-image count and content revision.

The Worker fences the original expired lease, current content and evidence,
and reconstructs R2 image references before its atomic D1 write; see Share's
`B01-20260929-provider-reading-recovery.md`. The Go command does not treat a
provider GET failure as proof that the request was unbilled.

`make verify` passed: vet, lint, race tests, 488 frontend checks and build.
The real local integration fixture uses Worker/D1 over HTTP and an independent
Go subprocess. The subprocess performs one simulated paid POST and settles the
permit, then exits before Complete. A new client retrieves the stored response
and commits the reading; replay returns the same receipt and no second POST
occurs. This fixture simulates the external provider and expires the local
lease directly. No real paid request, deployment or remote D1 migration was
made.

A response without a persisted ledger binding stays blocked. Production
billing reconciliation, migration/deployment acceptance and the complete
B02/OBS matrix remain open. Deploy the compatible Worker migration and route
before upgrading Go, after separate deployment approval.
