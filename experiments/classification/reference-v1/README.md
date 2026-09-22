# Cairn automatic reference v1

Owner authorization: [2026-09-22](../../../docs/jev-v2/AUTHORIZATION-20260922.md). No manual annotation prerequisite. This is an authored synthetic benchmark, **not human gold, private user preferences or measured real-bookmark quality**. No candidate model output was used to author these labels.

## Frozen inputs

`scenarios.json` contains 40 scenario families; `taxonomy.json` is the executable Share taxonomy at `136faae21a8bddf101fb67977e2c4fabb2b98569`. The six variants per family cover English, Chinese, mixed language, external articles, author continuation and long text with unrelated quotation. Scenarios include all 17 topic labels, methods/opinions, multiple topics, incidental mentions, non-evaluative praise, quotation, out-of-vocabulary content and unavailable image text. There are 240 samples: train 42 (7 groups), dev 36 (6 groups), holdout 162 (27 groups). Translations, repetitions and related variants remain within a family and never cross splits. A repeated passage is a length stress case, not a claim of natural long-document diversity.

`frozen/manifest.json` records seed, baseline SHAs, hashes, spec identity and the gate fixed before inference. `HashReference` excludes predictions. Each full material has a separate source hash, checked on load. `ValidateSplits` rejects duplicate snapshots, duplicate IDs and groups crossing sets. The generator refuses to overwrite a freeze directory:

```sh
go run ./experiments/classification/referencegen -source experiments/classification/reference-v1 -output /tmp/cairn-reference-new
```

The generator makes zero model calls. A new candidate or threshold must not modify these references after evaluation; revise a future benchmark under a new version and disclose prior holdout access.

## Reference rules

- `automatic_reference`, method `authored_synthetic_scenario`, basis, version and source family are explicit. An automatic reference cannot claim a second human annotator.
- Topics describe substantive subject matter, not incidental names, all concepts in a quotation or the article carrier itself. Evaluation requires meaningful methods/criteria/comparison, not merely saying something is useful.
- Content functions describe the primary text. A fictional worked example is not automatically a real-world case. Where precise function or affordance is debatable, the dimension is unknown.
- Carriers follow the explicitly constructed source structure. Form is function first; article/thread structure alone does not replace method/opinion.
- Potential uses are content suggestions, never inferred personal intent. `contra` is never a reference from objective material. Multiple acceptable single choices are alternatives, not simultaneous required labels.
- `unknown` and unspecified dimensions are excluded from quality denominators. `not_applicable` is an explicit valid empty answer. Missing image contents remain unknown; URLs are never treated as retrieved contents.

## Metrics

The scorer reports per-label and per-dimension counts, micro and macro precision/recall, single-choice confusion and correct-empty counts. Accepted error is false positive tags across **all** scored dimensions divided by accepted scored tags. Coverage is decided known fields divided by all known fields, including valid empty answers; any dimension flagged as abstained is undecided even if it has another accepted label. Missing predictions reduce coverage, contribute missed positives and make the whole report inconclusive. Unknown labels never become false negatives/positives or calibration negatives.

Review burden counts explicit abstentions and missing predictions on known fields per referenced sample. Brier/ECE apply to known topic reference probabilities; the ten bins and occupied-bin count are reported. They do not measure human-reference reliability. Deterministic 500-replicate group bootstrap intervals describe variation across authored families; they cannot quantify reference bias or unseen rare failures, and zero observed errors can yield a degenerate bootstrap interval. Small strata and the limited scenario diversity must be reported.

Threshold fitting accepts train/dev only, replays stored probabilities with no inference and updates topic abstentions. A report with missing references/predictions or inconclusive evidence cannot pass the gate. Statistical eligibility is not authorization to enable a production feature; the remaining engineering invariants and final cross-repository acceptance still apply.

## Live budget and private artifacts

The runner uses the production classifier with pinned `jev-1.13.0`. As verified on 2026-09-22, [official model documentation](https://docs.typesafe.ai/models) specifies a 64k input context per request and pricing of $0.042 per million input tokens (outputs free). The runner reserves **65,536 input tokens for every attempt**, without refunding unused reservation. Thus `max-tokens` is a conservative input-token ceiling based on that documented pinned-model contract, not a character-based token guess. Missing/invalid usage or any larger reported input stops the run. Other models/aliases need a new limit review. Requests also use production state and HTTP-byte bounds.

```sh
go run ./experiments/classification/main \
  -dataset experiments/classification/reference-v1/frozen/train.json \
  -catalog experiments/classification/reference-v1/taxonomy.json \
  -live -dry-run -max-samples 42 -max-calls 42 -max-tokens 2752512
```

Removing `-dry-run` requires credentials and a **new** explicit `-output` directory. Use `-env-file .env` only when intentionally authorizing that local credential source. Each call writes a synced reservation before HTTP, then real usage, requested/resolved model, source/state/spec identity, latency, attempt and typed judgments. Actual request/response bytes are stored separately without auth headers. No retry/cache/implicit resumption or policy fitting occurs. Failure or cancellation stops further calls, retaining prior costs and incomplete results. Existing output directories are refused so a restart does not silently repeat paid work. Raw artifacts are private (0700 directory, 0600 files); ordinary tests and CI never enable live calls.

The original v1 historical outputs and real-user retrieval relevance labels are absent. They are not fabricated by this corpus. Controlled v1/v2 comparisons, ablations, retrieval, Score-distribution evaluation, performance/cost reporting and final quality interpretation are tracked separately in B08.

### Recovering a stopped run without repeating paid calls

The offline recovery command rereads saved HTTP responses through the production
decoder and decision policy. It requires the same frozen references, exact
outbound request, model/spec, source and wire-state hashes, and a complete
reserved/finished journal. It has an in-memory transport with no network fallback
and reads no API key. It preserves the original failure, reported usage and
latency, and hashes the source artifacts. It never overwrites the original run.

```sh
go run ./experiments/classification/main \
  -dataset experiments/classification/reference-v1/frozen/train.json \
  -recover-from /private/run-a,/private/run-b \
  -output /private/new-recovery
```

`dataset.json` contains the recovered predictions; `unattempted.json` contains
only samples that have never been attempted in those runs. A failed attempt stays
out of that file even if its response still cannot be decoded. Missing/truncated
journals and overlapping samples are rejected, not retried. A deliberate live run
over `unattempted.json` needs its own explicit opt-in and budget. Combine the
original and continuation directories in a later offline recovery to score the
full original reference hash. Never include the separate smoke test in that merge;
its extra call belongs in total cost accounting.

The gate requires at least 20 independent groups as well as 20 reference samples.
The train (7 groups) and dev (6 groups) splits are therefore tuning/diagnostic
sets, not sufficient promotion evidence. The 27-group holdout remains separate.
# Historical question identity

`baseline-spec.json` preserves the exact `classify-80156c157660` question set
used when this corpus was frozen and first evaluated. The v1 generator validates
that immutable identity instead of recompiling the latest production questions.
Question changes belong to separately recorded experimental variants; they must
not rewrite the original manifest or reference labels. Regeneration still compares
all four frozen JSON files byte for byte in CI.
