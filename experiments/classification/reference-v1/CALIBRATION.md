# Probability diagnostics v1 (2026-09-23)

This is a fixed descriptive analysis, not a fitted policy or a calibration claim.
Config: `calibration-v1.json`; 10 equal-width bins and cutoffs
0, .15, .3, .5, .65, .8, .9, 1. Keep these settings unchanged after observing
the current analysis. No best cutoff is selected or exported for serving.

## Evidence and scope

The CLI requires the frozen dataset, exact catalog and recovered full raw journal.
It shares `PreparePolicyReplay` with fitting: validate sample/source identity,
complete typed answers, question/spec/model/batch metadata, actual bounded wire
state equality and hashes. A transport that cannot send network requests backs
this verification. A journal cannot replace missing or differently bound samples.

This exploratory command accepts train/dev only. It rejects holdout before reading
replay files, and rejects combination with live, dry-run, recovery, gate or fit.
Final frozen holdout validation remains a separate task; this tool does not make
that access or prove a frozen policy. Production questions, policy thresholds and
reference files stay unchanged. Invalid confidence outside [0,1] and nonfinite
Score now fail the shared typed-answer validator instead of entering diagnostics.

## Metrics fixed before analysis

- Noul: per-question and pooled dimension binary Brier `(p-y)^2`, and reliability
  of statement probability against binary reference truth. All raw observations,
  including policy abstentions, enter the metric. Pooling many negative labels
  can hide rare positives, so keep per-question support and group counts.
- Choice: multiclass Brier `sum_c (p_c-y_c)^2` on the full distribution, without
  dividing by two. Its ideal range [0,2] differs from binary [0,1]; do not combine
  the scales. Reliability/ECE use the **reported chosen option's probability**
  and its correctness, not provider confidence or an assumed argmax.
- Unknown/unspecified reference fields are excluded with counts. Multiple
  acceptable Choice alternatives are excluded from one-hot calibration; no
  uniform pseudo-truth is invented. Known empty/not-applicable Choice maps to
  the actual none candidate. Missing required positive candidates are rejected.
- Bins use `[lower, upper)` with 1 included in the final bin. Export every bin's
  count, positives, mean probability and observed rate; empty means are null.
  Brier/ECE are null without observations, not a perfect zero. Groups, not label
  observations or translation variants, are the independence unit. Fewer than
  20 groups/support marks metrics inconclusive. No new confidence intervals.
- Rounding: preserve original probabilities without renormalization, including
  the existing provider rounding tolerance. Formal score bounds assume unit mass.
- Choice selective risk starts from the **actual production Decide verdict**,
  including explicit none acceptance. Add exactly one cutoff on chosen
  probability, distribution margin, or returned confidence. Count coverage,
  errors, support, missing features and null error when nothing is accepted.
  Coverage denominator is known, unambiguous Choice observations, not all fields.
  Confidence is a concentration feature, never multiplied into probability or
  evaluated as a truth probability. Curves are descriptive, not significance tests
  or evidence of independent causal signal beyond the same distribution.

Existing top-level topic Brier/ECE reports and quality gates remain unchanged.
The new artifact states model_calls=0, promote=false and calibrated=false.

## Ordered Score engineering check

`ScoreOrdinal` accepts a typed Score against one explicit level reference and
the exact ordered rubric. It preserves level order, full probabilities and
confidence, and reports absolute error of the returned mean, distribution-expected
absolute distance, distribution mean, mean discrepancy, and normalized RPS:

`RPS = sum_{k=0}^{K-2} (sum_{j=0}^k p_j - 1[y <= k])^2 / (K-1)`.

For expected level 1 on three levels, `[0,1,0]` and `[.5,0,.5]` both have mean 1
and mean absolute error 0, but RPS is 0 versus .25, and expected absolute
distance is 0 versus 1. Adjacent versus distant point-mass errors also differ.
Typed legend mismatch, invalid reference, invalid probabilities and invalid
confidence/nonfinite Score are rejected. Mean discrepancy is exposed without
silently changing the supplied float or rounded probabilities.

This is a tested mathematical helper. The current production spec has Score
disabled, so saved training judgments have **zero Score observations** and the
artifact explicitly says ordinal_quality_evaluated=false. It does not establish
source/reference provenance for arbitrary helper callers. A meaningful product
rubric, separately frozen ordinal references, end-to-end Score-enabled input
binding and actual controlled inference/ablation remain required for Score quality.
The previous weak test comparing two Bernoulli ECES was replaced with these actual
same-mean typed Score vectors, not cited as historical ordinal evidence.

## Reproducible command

First recover the already-paid candidate arm's saved wires **offline** into a new
private directory using its matching catalog and compiler. Then:

```sh
go run ./experiments/classification/main \
  -dataset "$PRIVATE_RECOVERY/dataset.json" \
  -replay-journal "$PRIVATE_RECOVERY/recovery.json" \
  -catalog experiments/classification/reference-v1/carrier-boundaries-taxonomy.json \
  -calibration experiments/classification/reference-v1/calibration-v1.json \
  -output "$NEW_PRIVATE_OUTPUT"
```

Output must be new, directory 0700/files 0600. `calibration.json` contains only
aggregates; `inputs.json` binds config/journal/catalog and dataset/reference hashes.
Do not publish private source material, raw provider responses or credentials.
The current analysis uses only the 42 carrier-candidate training variants from
7 authored groups. No new model calls, dev/holdout quality use or v1 relabeling.

## Primary references checked 2026-09-23

- [TypeSafe Score](https://docs.typesafe.ai/primitives/score.md): ordered levels,
  probability-weighted float and full distribution; equal means can differ.
- [TypeSafe confidence](https://docs.typesafe.ai/confidence.md): distribution
  concentration feature, not a correctness guarantee.
- [scikit-learn Brier reference](https://scikit-learn.org/stable/modules/generated/sklearn.metrics.brier_score_loss.html): binary versus multiclass scale and formulas.
- [IRI/Columbia forecast verification definitions](https://iri.columbia.edu/wp-content/uploads/2013/07/scoredescriptions.pdf): cumulative ordinal error normalized by category count minus one.
