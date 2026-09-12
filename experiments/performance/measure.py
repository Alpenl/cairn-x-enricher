#!/usr/bin/env python3
"""Measure real dashboard DOM and response sizes using agent-browser and preview.py."""

import argparse
import json
import subprocess
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
from pathlib import Path

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--before", default="http://127.0.0.1:8765")
parser.add_argument("--after", default="http://127.0.0.1:8766")
parser.add_argument("--out", type=Path, default=Path(__file__).with_name("browser-results.json"))
args = parser.parse_args()


def browser(session, *command):
    output = subprocess.run(["agent-browser", "--session", session, *command, "--json"], check=True, capture_output=True, text=True)
    result = json.loads(output.stdout)
    if not result["success"]:
        raise RuntimeError(result)
    return result["data"]


def evaluate(session, script):
    return browser(session, "eval", script)["result"]


def measure(name, base):
    session = "cairn-perf-" + name
    try:
        browser(session, "open", base)
        browser(session, "wait", "--fn", 'document.querySelectorAll(".fcard, .item").length === 40')
        home = evaluate(session, """(async () => {
          const entries = performance.getEntriesByType('resource');
          const list = entries.find(e => new URL(e.name).pathname === '/api/bookmarks');
          const first = document.querySelector('.fcard');
          let mutations = 0;
          const observer = new MutationObserver(records => { mutations += records.length; });
          observer.observe(document.querySelector('#stream'), {childList:true, subtree:true});
          await new Promise(resolve => setTimeout(resolve, 11000));
          observer.disconnect();
          return {cards:document.querySelectorAll('.fcard, .item').length, domNodes:document.querySelectorAll('*').length,
            listResponseBytes:list.decodedBodySize, listDurationMs:list.duration,
            pollChildMutations:mutations, firstCardPreserved:first === document.querySelector('.fcard')};
        })()""")
        browser(session, "open", base + "/bookmarks/80")
        browser(session, "wait", "--fn", 'document.querySelectorAll("#read-body p").length === 240')
        reader = evaluate(session, """(async () => {
          const first = document.querySelector('#read-body p');
          const picture = document.querySelector('#read-figures img');
          const initial = {domNodes:document.querySelectorAll('*').length,
            originalParagraphs:document.querySelectorAll('#read-original p').length,
            translatedParagraphs:document.querySelectorAll('#read-body p').length};
          await new Promise(resolve => setTimeout(resolve, 9000));
          return {...initial, textNodePreserved:first === document.querySelector('#read-body p'),
            imageNodePreserved:picture === document.querySelector('#read-figures img')};
        })()""")
        browser(session, "click", "#read-toggle")
        reader["expandedOriginalParagraphs"] = evaluate(session, 'document.querySelectorAll("#read-original p").length')
        browser(session, "set", "viewport", "390", "844")
        reader["mobileHorizontalOverflow"] = evaluate(session, 'document.documentElement.scrollWidth > innerWidth')
        return {"home": home, "reader": reader}
    finally:
        browser(session, "close")


with ThreadPoolExecutor(max_workers=2) as pool:
    futures = {name: pool.submit(measure, name, base) for name, base in [("before", args.before), ("after", args.after)]}
    results = {name: future.result() for name, future in futures.items()}
results["measured_at"] = datetime.now(timezone.utc).isoformat()
results["fixture"] = {"bookmarks": 80, "page_size": 40, "paragraphs_per_language": 240, "production_data": False}
args.out.write_text(json.dumps(results, ensure_ascii=False, indent=2) + "\n")
print(json.dumps(results, ensure_ascii=False, indent=2))
