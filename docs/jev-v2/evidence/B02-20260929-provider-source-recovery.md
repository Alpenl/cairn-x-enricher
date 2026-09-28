# B02-T08: settled source recovery (local slice)

This Draft PR adds `provider-recover-source --operation-key ...`. Its default
mode reads the Worker permit and the provider's saved response without writing
business state. `--commit --actor ...` sends the decoded source to the Worker
through a separate Operator Token. The command never issues a model POST and
prints only safe counts and a content revision.

The command accepts only a ledger-bound, settled HTTP 200 fetch permit with a
saved response ID. It checks the provider response ID, model and completed
status and uses the ordinary source decoder, including completed search
evidence. The Worker owns the final lease/content fence and atomic write; see
Share's `B01-20260929-provider-source-recovery.md` for that transaction.

Local verification at this change: `make verify` passed, including vet, lint,
race tests, 488 frontend checks and build. The command fixture checks dry-run,
explicit commit, private output and zero provider POSTs. The saved-response
decoder rejects missing search evidence. No real provider call or remote D1
migration was made.

This slice covers **source** recovery only. Reading-result recovery, image
references, a real Go-to-Worker process restart fixture, production billing
reconciliation and the complete B02/OBS acceptance matrix remain open. A
response without a persisted ledger binding stays blocked; a provider GET
failure does not prove the request was unbilled. Deploy the compatible Worker
migration and route before upgrading Go, after separate deployment approval.
