# B02-T08 classification commit window

The TypeSafe HTTP timeout can be three minutes, while the previous detached
classification context also expired after three minutes. A successful answer
near that limit reached `CompleteClassification` with an expired context. The
Worker completion is idempotent, but it cannot recover an answer that the Go
process never submitted.

The Go processor now gives each already leased classification at most five
minutes. Its inference context ends thirty seconds before the work context;
the Worker completion uses the still-live work context. The default three
minute provider timeout therefore leaves up to ninety seconds for snapshot
reading, budget reservation and local handling before the provider call. A
provider result that is unknown remains subject to the existing budget and
recovery rules; this change does not infer a result from a timeout.

The Worker keeps an exclusive half-open classification probe for six minutes.
That is longer than the Go work deadline, so a second process cannot start a
probe while the first process can still submit its result. Expired probes can
still be replaced after a crash; stale probe completion cannot close the new
gate.

Local regression: `TestClassificationCommitKeepsDeadlineAfterInference`
observes the actual processor's classifier and Worker completion contexts.
It checks the thirty-second deadline difference and that cancelling the
inference context leaves completion usable. The Worker classification gate
test checks that the claimed probe remains owned beyond the five-minute work
deadline. No paid provider call or remote deployment was made.

The broader B02-T08 matrix remains open: production latency calibration,
provider-result-unknown recovery, rolling binaries, shutdown and long-running
load need separate evidence. A configured Worker HTTP timeout above the
thirty-second completion margin also needs a compatible deadline policy before
it can be promised as a bounded successful commit.
