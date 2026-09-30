# Frozen training intervention: source-role descriptions

Protocol date: 2026-09-23. Commit the source change, this protocol, intervention
JSON, comparison script and zero-call plan before any new inference.

## Single intervention and preserved baseline

The objective state actually contains primary text and ordered context blocks
with role metadata. The prior shared instruction incorrectly describes all
context as quotes/comments. Replace only that shared instruction with an explicit
description of author_continuation, quoted, external_article, third_party and
legacy_unknown. It permits context to inform observable source structure while
keeping third-party attribution separate and primary topic authoritative.

Exact old/new prefixes and identities are in source-roles-intervention.json.
The baseline is the prior field-name experiment, frozen as state-fields-spec.json
(classify-0b02fbfce85d), not the original incorrect-field spec. The previous
field-name-only test now compares its two preserved immutable specs; the new
current-spec test checks that reversing only the role prefix restores every
historical question. All question-specific wording, kinds, criteria, catalog,
model, default policy and actual state construction stay fixed. In particular,
legacy use/note wording, broad affordance definitions and Score stay unchanged.

## Calls and budget

Use only the same 42 authored training variants from 7 independent groups,
reference hash ea14264cbaabcedd13a328bfef6473cf31a18827ca0285b27c8047150370b414.
Model jev-1.13.0; unchanged uncalibrated jev-policy-v2. Maximum 42 samples,
42 new calls, 2,752,512 reserved input tokens (65,536 per attempt), sequential,
60 seconds per call. Stop at first failure; no retry, baseline rerun or repeated
smoke. Recover saved responses offline only. Historical paid calls are 85.
The source-roles-plan.json contains exact sample/spec/model identities.

Official https://docs.typesafe.ai/models checked 2026-09-23: 64k context,
32k state-plus-longest-question, $0.042/M input tokens, output free.
Maximum input-price estimate $0.115605504, not a bill. Record actual usage,
unknown usage, p50/p95 and all failures without concealing costs.

## Analysis fixed before inference

Run compare-source-roles.py from this repository with the saved prior LIVE
output directory, new live output directory, and a new private output directory.
It must verify exact state equality and full request equality after reversing
only the role prefix in all 29 questions for every paired sample. Validate fixed
reference, model, policy, resolved model, single attempts and usage metadata.

Use the existing offline six-dimensional scorer for all samples and each
language/context-role subgroup, retaining all metrics and failures, including
coverage, abstention/review burden and per-dimension F1. Brier/ECE cover topics
only. No threshold fitting during the primary comparison. Zero support is not
comparable. Do not infer gains from macro aggregates hiding regressions.

The external_article subset also consists entirely of Chinese samples, so
language and role are confounded. The 7 scenario groups are the independent units;
translations/variants are not independent. Within-condition group-bootstrap
intervals are not paired-delta confidence intervals; provider/temporal variation
is unestimated with one run. Automated reference scope bias remains possible.
Any post-hoc diagnostic must be labelled as such. Preserve all v1 labels.

No dev/holdout model calls, fitting or quality scoring. CI byte/schema/leakage
integrity checks may read frozen files. Training cannot promote. Keep the gate
unchanged and do not claim all B08 tasks or classification quality pass.

Create new private 0700 directories and 0600 files; never overwrite journals.
Credentials, private source material and raw responses remain local. Publish
safe aggregate reports, protocol, source identity and verification evidence.
