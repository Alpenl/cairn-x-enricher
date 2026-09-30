"""Verify the recorded source-role intervention and score paired training subsets.

Run from the Enricher repository at the report's tested SHA:
python3 compare-source-roles.py BASELINE_LIVE_DIR VARIANT_LIVE_DIR NEW_PRIVATE_DIR
This script never requests inference. It invokes the existing offline scorer.
"""
import copy
import json
import os
from pathlib import Path
import subprocess
import sys

baseline_root, variant_root, output = map(Path, sys.argv[1:])

def read(path):
    return json.loads(path.read_text())

def lines(path):
    return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]

def write(path, value):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as stream:
        json.dump(value, stream, ensure_ascii=False, indent=2)
        stream.write('\n')

old_live = read(baseline_root / 'result.json')
baseline = old_live['dataset']
intervention = read(Path(__file__).with_name('source-roles-intervention.json'))
assert old_live['completed'] and old_live['calls'] == 42 and old_live['usage_missing_calls'] == 0
assert old_live['plan']['spec_id'] == intervention['baseline_spec_id']
assert old_live['plan']['spec_hash'] == intervention['baseline_spec_hash']
live = read(variant_root / 'result.json')
variant = live['dataset']
assert live['plan']['spec_id'] == intervention['variant_spec_id']
assert live['plan']['spec_hash'] == intervention['variant_spec_hash']
assert live['completed'] and live['calls'] == 42 and live['usage_missing_calls'] == 0
assert baseline['split'] == variant['split'] == 'train'
assert baseline['samples'] == variant['samples']
assert len(baseline['samples']) == len(baseline['predictions']) == len(variant['predictions']) == 42
assert old_live['plan']['reference_hash'] == live['plan']['reference_hash']
assert {p['policy_version'] for p in baseline['predictions']} == {p['policy_version'] for p in variant['predictions']} == {'jev-policy-v2'}
assert {p['model'] for p in baseline['predictions']} == {p['model'] for p in variant['predictions']} == {'jev-1.13.0'}

old_requests = {}
for entry in lines(baseline_root / 'calls.ndjson'):
    if entry['phase'] == 'finished':
        assert entry['sample_id'] not in old_requests
        assert entry['attempt'] == 1 and not entry['retry'] and not entry['cached']
        assert not entry.get('error') and entry['usage_known'] and entry['resolved_model'] == 'jev-1.13.0'
        old_requests[entry['sample_id']] = entry['request']
new_calls = [entry for entry in lines(variant_root / 'calls.ndjson') if entry['phase'] == 'finished']
assert len(new_calls) == len({entry['sample_id'] for entry in new_calls}) == 42
assert set(old_requests) == {entry['sample_id'] for entry in new_calls}
question_changes = 0
for entry in new_calls:
    old, new = old_requests[entry['sample_id']], entry['request']
    assert entry['attempt'] == 1 and not entry['retry'] and not entry['cached']
    assert not entry.get('error') and entry['usage_known'] and entry['resolved_model'] == 'jev-1.13.0'
    assert old['model'] == new['model'] == 'jev-1.13.0'
    assert old['state'] == new['state']
    assert old['questions'].keys() == new['questions'].keys()
    assert len(old['questions']) == entry['questions'] == 29
    restored = copy.deepcopy(new)
    for name, question in restored['questions'].items():
        assert question['instructions'].startswith(intervention['new_rule'])
        question['instructions'] = intervention['old_rule'] + question['instructions'][len(intervention['new_rule']):]
        assert question == old['questions'][name]
        question_changes += 1
    assert restored == old

output.mkdir(mode=0o700)
subsets = {'all': baseline['samples']}
for label in sorted({s['language'] for s in baseline['samples']}):
    subsets['language-' + label] = [s for s in baseline['samples'] if s['language'] == label]
roles = lambda sample: '+'.join(sorted({b['role'] for b in (sample['material'].get('context') or [])})) or 'primary-only'
for role in sorted({roles(s) for s in baseline['samples']}):
    subsets['context-' + role] = [s for s in baseline['samples'] if roles(s) == role]

reports = {}
for name, samples in subsets.items():
    assert '/' not in name and '..' not in name
    ids = {s['sample_id'] for s in samples}
    reports[name] = {}
    for condition, dataset in [('baseline', baseline), ('variant', variant)]:
        subset = dict(dataset, samples=samples, predictions=[p for p in dataset['predictions'] if p['sample_id'] in ids])
        path = output / (name + '-' + condition + '-dataset.json')
        write(path, subset)
        completed = subprocess.run(['go', 'run', './experiments/classification/main', '-dataset', str(path)], check=True, capture_output=True, text=True)
        record = json.loads(completed.stdout)
        assert not record['decision']['promote']
        write(output / (name + '-' + condition + '-report.json'), record)
        reports[name][condition] = record
    assert reports[name]['baseline']['report']['reference_hash'] == reports[name]['variant']['report']['reference_hash']

metrics = ['micro_precision','micro_recall','macro_precision','macro_recall','accepted_error_rate','coverage','review_fields_per_sample','brier','ece']
summary = {
    'intervention': 'only shared material instructions changed from quotes/comments to explicit source-role descriptions',
    'verified_pairs': 42,
    'verified_question_replacements': question_changes,
    'all_actual_request_states_identical': True,
    'reference_hash': old_live['plan']['reference_hash'],
    'model': 'jev-1.13.0',
    'policy': 'jev-policy-v2',
    'new_calls': live['calls'],
    'historical_calls_including_smoke': 85,
    'cumulative_calls': 85 + live['calls'],
    'baseline_input_tokens': old_live['input_tokens'],
    'variant_input_tokens': live['input_tokens'],
    'variant_reserved_input_tokens': live['reserved_input_tokens'],
    'variant_p50_latency_ms': live['p50_latency_ms'],
    'variant_p95_latency_ms': live['p95_latency_ms'],
    'variant_input_price_estimate_usd': live['input_tokens'] * 0.042 / 1_000_000,
    'price_source': 'https://docs.typesafe.ai/models',
    'price_checked_at': '2026-09-23',
    'price_is_estimate_not_bill': True,
    'subsets': {},
    'limitations': ['7 independent authored training groups; translations/variants are not independent',
                    'no repeated run to estimate provider/temporal variation',
                    'reference-scope bias is not included in bootstrap intervals',
                    'reported intervals are within-condition group bootstrap, not a paired delta confidence interval',
                    'no dev/holdout model calls, fitting or quality scoring',
                    'no policy calibration or production promotion'],
}
for name, records in reports.items():
    a,b=records['baseline']['report'],records['variant']['report']
    before={x['dimension']:x for x in a['dimensions']}
    after={x['dimension']:x for x in b['dimensions']}
    assert before.keys()==after.keys()
    for dimension in before:
        assert before[dimension]['support']==after[dimension]['support'] and before[dimension]['multi_label']==after[dimension]['multi_label']
    summary['subsets'][name]={'samples':a['samples_total'],'groups':a['independent_groups'],
        'baseline':{k:a.get(k,0) for k in metrics},'variant':{k:b.get(k,0) for k in metrics},
        'delta':{k:b.get(k,0)-a.get(k,0) for k in metrics},
        'dimensions':{d:{'baseline_f1':before[d]['f1'],'variant_f1':after[d]['f1'],'delta':after[d]['f1']-before[d]['f1'],'support':before[d]['support'], 'comparable':before[d]['support']>0} for d in before}}
write(output / 'comparison.json',summary)
write(output / 'reports.json',reports)
print(json.dumps({k:v for k,v in summary.items() if k!='subsets'},ensure_ascii=False,indent=2))
print(json.dumps(summary['subsets']['all'],ensure_ascii=False,indent=2))
