# Frozen training intervention: state field names

Protocol date: 2026-09-23. This file, the two-field correction, the preserved
historical spec and the zero-call dry-run plan are committed before new inference.

## One changed factor

All 29 questions refer to `original_text` and `context_text`, while the actual
production request always supplies `primary` and `context`. The intervention
replaces **only these two backticked field names** in their shared instruction.
An executable test compares every question with the preserved historical spec
after reversing those two replacements. Question IDs, kinds, criteria, terms,
other instruction text, state construction, catalog, model and policy are fixed.

This experiment does not change the broad affordance definitions, remaining
legacy note wording, the description of context roles, or the score rubric.
Those require separate interventions. The field contract is mechanically wrong
even if correcting it produces no measurable quality gain on this small corpus.

## Fixed identities and budget

- Baseline spec: `classify-80156c157660`, hash
  `2a39cb299aa0bf916bb4ac9642c1e515dd07f35883a61da403b4c53c05ce4d26`.
- Variant spec: `classify-0b02fbfce85d`, hash
  `c760533cfd880ba0201f97d41c3a1d4d6856bce6c816feb30328d160a7cdead5`.
- Reference: original **train only**, 42 samples / 7 independent groups, hash
  `ea14264cbaabcedd13a328bfef6473cf31a18827ca0285b27c8047150370b414`.
  The exact sample list is in the committed dry-run plan.
- Model: pinned `jev-1.13.0`; policy: unchanged `jev-policy-v2` default,
  explicitly uncalibrated. No threshold fitting within the primary comparison.
- Maximum **42 new calls**, **42 samples**, **2,752,512 input tokens reserved**,
  65,536 reserved per attempt, 60-second timeout per call, sequential execution.
  Stop at the first failure; no retries or repeated smoke samples. Any failure
  stays recorded. Saved-wire recovery is offline and cannot add calls.
- Official [model limits and price](https://docs.typesafe.ai/models), checked
  2026-09-23: 64k total input context and 32k state-plus-longest-question;
  $0.042 per million input tokens, output free. The reservation gives a maximum
  input-price estimate of **$0.115605504**, not a bill. Actual usage is reported
  separately; unknown usage stays unknown. The existing budgeted production
  request builder is used without changing state or question limits.

## Analysis fixed before inference

Use the saved baseline outputs; do not call the baseline again or retroactively
change its predictions. Compare six-dimensional metrics on the same frozen
references and policy. Separately inspect language and context-role groups,
keeping six variants of a family together when estimating uncertainty.
Report actual request state equality, the two-field question difference, tokens,
question counts, latency, resolved model and failures. Do not infer a causal gain
from one small paired run without acknowledging temporal/provider variation.

Keep the original reference labels and frozen files unchanged. No dev or holdout
model calls, fitting or quality scoring occur in this intervention. Ordinary CI
may compare frozen bytes and validate split integrity; that is not a holdout
quality evaluation. The original promotion gate remains unchanged and training
results cannot promote. Improved engineering consistency alone does not establish
classification quality or satisfy all B08 tasks.

Use a new private 0700 output directory, 0600 response/journal files and an
explicit existing credential file. Credentials and source text are never printed
or committed. Publish only safe aggregate metrics, hashes and test evidence.
