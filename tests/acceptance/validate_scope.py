#!/usr/bin/env python3
"""Validate the frozen scope index, not runtime behavior or acceptance."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess


ROOT = Path(__file__).resolve().parents[2]
SNAPSHOT = ROOT / "docs/jev-v2/evidence/scope-20260923"


def require(condition, message):
    if not condition:
        raise ValueError(message)


def validate(check_git=False, share_repo=None):
    original_bytes = (SNAPSHOT / "requirements.json").read_bytes()
    original = json.loads(original_bytes)
    matrix = json.loads((SNAPSHOT / "matrix.json").read_text())
    require(hashlib.sha256(original_bytes).hexdigest() == matrix["requirements_sha256"], "requirement snapshot changed")
    counts = [10, 11, 14, 15, 14, 12, 10, 14, 14, 12]
    expected_tasks = {f"B{batch:02}-T{n:02}" for batch, count in enumerate(counts, 1) for n in range(1, count + 1)}
    expected_controls = {f"{prefix}{n:02}" for prefix, count in [("R", 39), ("SC", 30), ("F", 14), ("R2-", 14), ("R3-", 12)] for n in range(1, count + 1)}
    tasks = {t["task"]: t for t in matrix["tasks"]}
    controls = {c["id"]: c for c in matrix["controls"]}
    require(len(matrix["tasks"]) == 126 and set(tasks) == expected_tasks, "missing/duplicate task")
    require(len(matrix["controls"]) == 109 and set(controls) == expected_controls, "missing/duplicate control")
    require(len(original["tasks"]) == 126 and {t["task"] for t in original["tasks"]} == expected_tasks, "original task scope")
    require(len(original["controls"]) == 109 and {c["id"] for c in original["controls"]} == expected_controls, "original control scope")
    for t in original["tasks"]:
        require(all(tasks[t["task"]][k] == v for k, v in t.items()), f"original task changed: {t['task']}")
    for c in original["controls"]:
        require(all(controls[c["id"]][k] == v for k, v in c.items()), f"original control changed: {c['id']}")
    gaps = matrix["gaps"]
    require(set(gaps) == {f"G{n:02}" for n in range(1, 12)}, "gap inventory")
    for t in tasks.values():
        require(t["accepted"] is False and bool(t["remaining"]), "snapshot must not assert acceptance")
        require(t["assessment"] in {"implementation_gap", "verification_gap", "external_review"}, "unknown assessment")
        require(bool(t["families"]) and set(t["families"]) <= matrix["registry"].keys(), "unresolved evidence family")
        require(bool(t["gaps"]) and set(t["gaps"]) <= gaps.keys(), "unresolved gap")
        require(set(t["controls"]) == {i for i, c in controls.items() if t["task"] in c["tasks"]}, "asymmetric control mapping")
    for c in controls.values():
        require(c["accepted"] is False and c["assessment"] == "requires_clause_review", "control falsely accepted")
        require(bool(c["tasks"]) and set(c["tasks"]) <= tasks.keys(), "unresolved control task")
        require(set(c["gaps"]) == {g for t in c["tasks"] for g in tasks[t]["gaps"]}, "incorrect inherited gaps")
    for gap in gaps.values():
        require(set(gap["primary_tasks"]) <= tasks.keys(), "unresolved primary task")
    require(all("automatic_reference" in controls[i]["authorization_override"] for i in ["R34", "SC22"]), "manual-label waiver omitted")
    require(matrix["paid_calls_this_audit"] == 0 and matrix["status"] == "in_progress", "audit scope changed")
    references = [r for family in matrix["registry"].values() for refs in family.values() for r in refs]
    references += [c["source"] for c in controls.values() if "path" in c["source"]]
    unique = {(r["repo"], r["path"]): r for r in references}
    repositories = {"E": ROOT, "S": share_repo or ROOT.parent / "cairn-share"}
    for r in references:
        sha = matrix["baselines"][r["repo"]]
        require(re.fullmatch(r"[a-f0-9]{40}", sha) is not None, "invalid git SHA")
        require(re.fullmatch(r"[a-f0-9]{64}", r["sha256"]) is not None, "invalid content hash")
        expected_url = f"https://github.com/Alpenl/{matrix['repositories'][r['repo']]}/blob/{sha}/{r['path']}"
        require(r["url"] == expected_url, "unpinned/mismatched source link")
    if check_git:
        for (repo, path), r in unique.items():
            data = subprocess.check_output(["git", "-C", str(repositories[repo]), "show", matrix["baselines"][repo] + ":" + path])
            require(hashlib.sha256(data).hexdigest() == r["sha256"], f"file differs from frozen object: {repo}:{path}")
    report = (ROOT / "docs/jev-v2/evidence/B10-20260923-scope.md").read_text()
    for target in re.findall(r"\]\(([^)]+)\)", report):
        if not target.startswith("https://"):
            require((ROOT / "docs/jev-v2/evidence" / target).is_file(), f"missing report link: {target}")
    print(f"PASS: 126 tasks + 109 controls = 235 IDs; exact requirements, bidirectional mappings, {len(unique)} pinned files; git objects checked={check_git}")
    print("Index integrity only; no functional, quality or final acceptance is asserted.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check-git", action="store_true")
    parser.add_argument("--share-repo", type=Path)
    args = parser.parse_args()
    validate(args.check_git, args.share_repo)
