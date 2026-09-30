# Six-dimensional offline policy fitting

`policy-fit-v1.json` freezes the candidate grid, scope and loss rules for the
authored automatic reference corpus. Commit its exact contents before fitting.
The 336 combinations share Noul accept/reject rules across topics, content
functions and affordances, and Choice acceptance across carriers, form and use,
as the production policy does. No per-rare-label parameters are fitted.

The loss is the dimension-weighted mean of
`(5 * false_positive + false_negative + 0.25 * abstained) / known_reference_samples`.
Each dimension has equal weight; missed positives under abstention still count as
false negatives, while the additional review term prices the undecided field.
These are explicit engineering risk preferences, not learned utility or measured
human review time. Each dimension needs known support and at least 35% decided
coverage; aggregate accepted error must be at most 10%. Unknown references are
excluded from support. A candidate failing any constraint is not selectable.

Use the existing zero-network recovery command first when historical evaluations
lack versioned wire-state metadata. The fit command accepts a **complete** recovery
journal matching the same reference hash; it verifies source material, actual
bounded wire state, question hashes, call provenance, resolved model, spec and
batching before considering candidates. It never replaces missing evidence with
a model request. Old raw values alone are insufficient for this mode.

```sh
go run ./experiments/classification/main \
  -dataset /private/revalidated/dataset.json \
  -replay-journal /private/revalidated/recovery.json \
  -catalog experiments/classification/reference-v1/taxonomy.json \
  -fit-policy experiments/classification/reference-v1/policy-fit-v1.json \
  -output /private/new-policy-fit-directory
```

Only train/dev can be fitted. The command rejects combinations with live,
recovery, dry-run or the separate promotion `-gate` mode. It uses a transport with
no network implementation, no credentials, and creates a new 0700 directory with
0600 JSON files; existing output is never overwritten. `inputs.json` fingerprints
the exact files. `policy-fit.json` carries the full frozen config, inference
binding, reference/data hashes, baseline and selected reports, all candidate
losses/eligibility reasons, support and split. `selected-dataset.json` is private
because it includes reference material; it exists only when a candidate qualified.

Exact loss ties prefer higher accept thresholds and lower rejection thresholds.
The exported candidate has `calibrated=false`, `promote=false` and status
`fitted_unvalidated`; small group counts remain inconclusive. If none qualifies,
the artifact says `no_eligible_candidate` and has no selected policy. No automatic
fallback or relaxation occurs.

`ReplayFittedPolicy` checks model/spec/taxonomy/batch binding and the candidate's
config-derived identity before reuse on another verified dataset. This is an
engineering compatibility check. It does not prove that an external artifact was
frozen before holdout access, validate quality, authorize holdout access, install a
production policy, or replace controlled ablations and independent evaluation.

The older `SearchThresholds` helper is topic-only engineering support. It is not
used by this command and must not be treated as a six-dimensional fitted policy.
