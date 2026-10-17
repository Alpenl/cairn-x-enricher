# B07 processing gateway increment

The exact Worker contract is [source-bound processing lifecycle](https://github.com/Alpenl/cairn-share/blob/a54a57ec2e9371bc2b923f6cad38f4e90d4adf62/docs/jev-v2/processing.md). `deploy/stack-contract.json` pins that same Worker and retains migration 0068.

The App-facing `/api/bookmarks/{id}/processing` route proxies only that bounded contract, with `X-Cairn-App-Processing: 1`. Its read has no scheduling side effects. Each explicit action carries a durable operation key, source/queue/human fence, explicit content revision and target generation; only `save_primary` accepts text. The gateway rejects missing/null/unknown keys, invalid Unicode and encoded request overflow before sending.

The client disables redirects and automatic POST retry, bounds and validates responses, and treats unmarked/mismatched success or uncertain transport outcomes as unknown. It verifies the operation, action, input revision, target, source hash, queue flags and zero-immediate-model-call receipt. Reads and receipt recovery never wake a queue; a newly confirmed action wakes only its specified stages.

`save_primary` is deliberately **save and queue processing**: the endpoint itself makes zero model calls, but subsequent reading/classification may cost money. X/Twitter reading support is reported accurately; an arbitrary-site bookmark can gain a primary source and classification input without pretending that the current reading consumer supports it. Ordinary page reads do not call the route or silently execute unknown intents.

Verification includes client/gateway race tests, the required `make verify`, and the actual `processing` scenario in `tests/local-integration/run.sh`. The scenario uses a real migrated local Worker, D1, R2, the actual Go gateway and queue consumers; only the provider/reading outputs are contract fixtures. It drops the real committed source response, confirms the same receipt, preserves original/context/media/human state and runs reading and classification independently. It is included in the complete 49-scenario suite, with fresh storage per scenario.

This does not finish B07. Android Repository/ViewModel/Compose actions, durable intents and process recovery, a capability-bound local policy-replay interaction and the remaining compatibility/accessibility/flags matrix remain required. The direct Worker's truthful absence of a local policy executor is not a substitute for wiring the existing Go executor into a safe App action. No independent final review, real model quality acceptance or production verification is claimed.

No deploy, migration, live provider inference, merge/Ready, signing, image push or public release is performed. Prior failed SQL query/fixture/static checks are retained with the local evidence, and CI checks must be read for the exact pinned pair.
