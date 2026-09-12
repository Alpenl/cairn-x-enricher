#!/usr/bin/env python3
"""Ablate one runtime capability at a time in a disposable Go checkout."""

import argparse
import json
import shutil
import subprocess
import tempfile
import time
from collections import Counter
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CASES = [
    ("search-evidence", "internal/enrich/validate.go", "if !candidate.SearchVerified {", "if false && !candidate.SearchVerified {", 1),
    ("title-bounds", "internal/enrich/validate.go", "if titleRunes < minAITitleRunes || titleRunes > maxAITitleRunes || !containsHan(result.AITitle) {", "if false && (titleRunes < minAITitleRunes || titleRunes > maxAITitleRunes || !containsHan(result.AITitle)) {", 1),
    ("strict-schema", "internal/enrich/responses.go", "Strict: true,", "Strict: false,", 2),
    ("classification-normalization", "internal/enrich/workflow.go", "result.Classification = w.catalog.Normalize(result.Classification)", "// normalization ablated", 1),
    ("provider-error-context", "internal/enrich/workflow.go", "return Result{}, err\n\t}\n\tif err := ctx.Err();", "return Result{}, context.Canceled\n\t}\n\tif err := ctx.Err();", 1),
    ("http-retries", "internal/enrich/responses.go", "if !shouldRetry {", "if true || !shouldRetry {", 1),
    ("recovered-source", "internal/processor/processor.go", 'if sourceText == "" && job.Attempt > 1 {', 'if false && sourceText == "" && job.Attempt > 1 {', 1),
    ("source-image-retention", "internal/processor/processor.go", "if useExisting && len(existing.Images) > 0 {", "if false && useExisting && len(existing.Images) > 0 {", 1),
    ("scheduled-drain", "internal/processor/processor.go", "workCtx := context.WithoutCancel(ctx)", "workCtx := ctx", 1),
    ("manual-drain", "internal/dashboard/dashboard.go", "context.WithCancel(context.WithoutCancel(ctx))", "context.WithCancel(ctx)", 1),
    ("queue-capacity", "internal/dashboard/dashboard.go", "if s.queued.Load()+int64(len(ids)) > int64(cap(s.jobs)) {", "if false && s.queued.Load()+int64(len(ids)) > int64(cap(s.jobs)) {", 1),
    ("worker-auth", "internal/cairn/client.go", 'request.Header.Set("Authorization", "Bearer "+c.token)', '// authorization ablated', 1),
    ("readiness", "internal/health/health.go", 't.snapshot.Ready = reason == ""', 't.snapshot.Ready = true', 1),
    ("concurrency-bound", "internal/config/config.go", 'intValue("MAX_CONCURRENCY", 2, 1, 16)', 'intValue("MAX_CONCURRENCY", 2, 0, 16)', 1),
    ("lazy-original", "internal/dashboard/reader.js", 'if (!original.hidden) paragraphs(original, item.original_text || "");', 'paragraphs(original, item.original_text || "");', 1),
    ("full-export", "internal/dashboard/common.js", 'summary.content_loaded === false', 'false', 1),
]


def verify(root, logs, name):
    started = time.monotonic()
    try:
        result = subprocess.run(["go", "test", "-json", "-timeout=45s", "./..."], cwd=root, capture_output=True, text=True, timeout=90)
    except subprocess.TimeoutExpired:
        return {"case": name, "status": "TIMEOUT", "seconds": round(time.monotonic() - started, 3)}
    (logs / f"{name}.log").write_text(result.stdout + result.stderr)
    failed, passed = [], 0
    for line in result.stdout.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("Test") and event.get("Action") == "fail":
            failed.append(event["Test"])
        if event.get("Test") and event.get("Action") == "pass":
            passed += 1
    status = "SURVIVED" if result.returncode == 0 else "KILLED_TEST" if failed else "KILLED_BUILD"
    return {"case": name, "status": status, "passed_tests": passed, "failed_tests": failed,
            "seconds": round(time.monotonic() - started, 3), "returncode": result.returncode}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--validate", action="store_true")
    parser.add_argument("--out", type=Path, default=ROOT / "experiments/results/architecture.json")
    args = parser.parse_args()
    for name, file, old, _, count in CASES:
        actual = (ROOT / file).read_text().count(old)
        if actual != count:
            raise SystemExit(f"{name}: expected {count} anchors, got {actual}")
    print(f"Validated {len(CASES)} capability ablations.", flush=True)
    if args.validate:
        return
    args.out.parent.mkdir(parents=True, exist_ok=True)
    logs = args.out.parent / "architecture-logs"
    logs.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="cairn-go-ablation-") as directory:
        root = Path(directory)
        files = subprocess.check_output(["git", "ls-files", "-co", "--exclude-standard", "-z"], cwd=ROOT).decode().split("\0")
        for relative in set(files):
            source = ROOT / relative
            if not relative or not source.is_file():
                continue
            target = root / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, target)
        baseline = verify(root, logs, "baseline")
        if baseline["status"] != "SURVIVED":
            raise SystemExit(f"Baseline failed: {baseline}")
        print(f"Baseline: {baseline['passed_tests']} test outcomes passed", flush=True)
        rows = []
        for name, file, old, new, count in CASES:
            target = root / file
            original = target.read_text()
            try:
                target.write_text(original.replace(old, new, count))
                result = verify(root, logs, name)
                result["file"] = file
                rows.append(result)
                print(f"{name}: {result['status']}", flush=True)
            finally:
                target.write_text(original)
            args.out.write_text(json.dumps({"baseline": baseline, "results": rows}, indent=2))
        print(json.dumps(Counter(row["status"] for row in rows)), flush=True)


if __name__ == "__main__":
    main()
