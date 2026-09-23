# Frozen training intervention: carrier answer boundaries

Protocol date: 2026-09-23. Commit this protocol, production definitions,
question change, identities, plan and comparison script before inference.

## One question, two fresh arms

Baseline is the objective-use compiler at Enricher
`d451e2129305eae41b374cf3c0586bd218431151`, old `taxonomy.json`, and current
unchanged **jev-policy-v3**. Its full canonical object is
`objective-use-spec.json` (`classify-736e775b96c2`). Previous role experiments
used a different use question and v2 policy; they cannot serve as this full
single-factor baseline. Do not repeat or overwrite those historical runs.

Variant uses `carrier-boundaries-taxonomy.json`, exported from the actual Worker
definition version 2. The real Worker integration test checks production API →
Go compilation equals this export and that only the carrier question differs.
The exact variant ID/hash are in `carrier-boundaries-intervention.json`.

This intervention changes only the **carrier answer contract**: term criteria,
explicit precedence when structures coexist, and the distinction between unknown
evidence and a known structure outside the available categories. It does not
separate these individual wording changes into causal claims. Every other
question, state construction, policy/threshold, model and reference stays fixed.
Keep all stable IDs, label meanings and historical registered spec payloads.

Visible source precedence: supplied confirmed same-author continuation wins even
with external material; otherwise supplied linked article body is external;
otherwise observed original post is single. Quotations/third-party comments are
not continuations; a URL/title alone is not a fetched article. Missing source or
unresolved relationships use unknown; observable unmatched structures use none.
These are rules over supplied evidence, not claims that retrieval is complete.

## Budget and execution

Use the same frozen 42 authored training variants from 7 independent groups,
reference hash `ea14264cbaabcedd13a328bfef6473cf31a18827ca0285b27c8047150370b414`.
First run the baseline arm, then the variant. Model `jev-1.13.0`; no alias.
Each arm: maximum 42 samples, 42 calls, 2,752,512 input-token reservation,
sequential, 60 seconds per call, stop on first failure, no retries or smoke.
**Combined maximum: 84 new calls / 5,505,024 reserved input tokens.** Historical
paid count is 127; maximum cumulative 211. A failed first arm stops the experiment;
inspect journals and recover only saved responses offline, never blindly rerun.

Official https://docs.typesafe.ai/models.md checked 2026-09-23: 64k request,
32k state-plus-longest-question, $0.042/M input tokens, output free.
Combined maximum input-price estimate **$0.231211008**, not a bill. Record both
arms' real usage, unknown usage, p50/p95, attempts and failures. New private 0700
directories and 0600 artifacts only; credentials stay in the explicit local
environment file, never outputs. Use each arm's correct source compiler.

## Analysis fixed before inference

Run `compare-carrier-boundaries.py BASELINE_DIR VARIANT_DIR NEW_PRIVATE_DIR`
from the candidate source. It validates both spec identities, 42 exact sample
pairs, unchanged reference/state/model, single attempts, known usage, v3 policy,
and full outgoing request equality after replacing only `questions.carriers`.
Report all six-dimensional metrics, errors, coverage, abstention/review burden,
language and context subgroups, even if unrelated outputs regress. Retain full
distributions; Brier/ECE currently cover topics only.

Only 7 independent groups: variants/translations are not independent. All 7
external-article examples are Chinese, confounding language and source role.
The fixed arm order and one run do not estimate provider/temporal variance.
Within-condition bootstrap intervals are not paired-delta intervals. The frozen
training set does not cover all mixed-role or missing-evidence boundary cases;
engineering contract checks do not establish their semantic model accuracy.

No post-hoc v1 relabeling, threshold fitting, new acceptance rule, policy
calibration, automatic promotion or broad quality-pass claim. Keep existing gate;
training cannot promote. No dev/holdout calls, fitting or quality scoring; CI may
read frozen files only for byte/schema/leakage integrity. Preserve all historical
negative results. Later independently authored boundary examples and dev/holdout
remain required for wider quality conclusions.

Official design reference: https://docs.typesafe.ai/primitives/choice.md,
checked 2026-09-23. Choice compares supplied competing options; distinct criteria
and an explicit no-match outcome are application responsibilities.
