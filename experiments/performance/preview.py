#!/usr/bin/env python3
"""Serve real dashboard assets over deterministic, synthetic bookmark data.

No credentials, production database or model requests are used. Pass --assets
to point at a before/after checkout. The API honours the same compact list
contract covered by the Worker integration tests.
"""

import argparse
import base64
import json
import mimetypes
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlparse

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--assets", type=Path, default=Path(__file__).resolve().parents[2] / "internal/dashboard")
parser.add_argument("--port", type=int, default=8766)
args = parser.parse_args()

catalog = {"version": "fixture-v1", "topics": [{"id": "eng", "label": "工程", "active": True}],
           "forms": [{"id": "method", "label": "方法", "active": True}], "uses": [{"id": "quote", "label": "引用", "active": True}]}
image = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
date = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
items = [{"id": i, "url": f"https://x.com/example/status/{i}", "note": "用于性能验证的合成收藏", "created_at": date,
          "status": "processing" if i == 80 else "completed", "processable": True,
          "ai_title": f"第 {i} 条收藏：按需加载与阅读体验", "summary": "固定摘要。评估列表传输体积、原文展开和轮询渲染。",
          "original_language": "en", "original_text": "\n".join(f"Original paragraph {j}. " + "Repeatable content for the browser performance experiment. " * 4 for j in range(240)),
          "translated_text": "\n".join(f"译文第 {j} 段。" + "这是用于浏览器性能对照的固定内容。" * 4 for j in range(240)),
          "related_links": ["https://example.com/reference"], "images": [{"key": f"enrichment/{i}/{'a' * 64}.png", "content_type": "image/png"}],
          "source": "x", "why": "比较界面性能", "curation_status": "kept", "classification_reviewed": True,
          "classification": {"topics": ["eng"], "form": "method", "use": "quote", "entities": [], "uncertainty": False},
          "attempts": 1} for i in range(80, 0, -1)]


class Handler(BaseHTTPRequestHandler):
    def send(self, data, content_type="application/json"):
        if not isinstance(data, bytes):
            data = json.dumps(data, ensure_ascii=False).encode()
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        url = urlparse(self.path)
        query = parse_qs(url.query)
        if url.path == "/api/taxonomy":
            return self.send(catalog)
        if url.path == "/api/bookmarks":
            page = items
            if "before_id" in query:
                page = [item for item in page if item["id"] < int(query["before_id"][0])]
            if "q" in query:
                page = [item for item in page if query["q"][0] in json.dumps(item, ensure_ascii=False)]
            limit = int(query.get("limit", ["40"])[0])
            next_id = page[limit - 1]["id"] if len(page) > limit else None
            page = [dict(item) for item in page[:limit]]
            if query.get("view") == ["summary"]:
                for item in page:
                    item.update(original_text=None, translated_text=None, related_links=[], content_loaded=False)
            return self.send({"items": page, "next_before_id": next_id, "counts": {"total": 80, "processing": 1, "completed": 79}})
        if url.path.startswith("/api/bookmarks/"):
            return self.send(next(item for item in items if item["id"] == int(url.path.rsplit("/", 1)[1])))
        if url.path.startswith("/api/images/"):
            return self.send(image, "image/png")
        if url.path == "/api/backstage":
            return self.send({"counts": {"total": 80}, "items": []})
        name = "index.html" if url.path == "/" else "reader.html" if url.path.startswith("/bookmarks/") else url.path.removeprefix("/assets/")
        if name not in {"index.html", "reader.html", "backstage.html", "common.js", "home.js", "reader.js", "backstage.js", "dashboard.css", "download.svg"}:
            self.send_error(404)
            return
        content_type = mimetypes.guess_type(name)[0] or "application/octet-stream"
        return self.send((args.assets / name).read_bytes(), content_type + "; charset=utf-8")

    def do_PATCH(self):
        update = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        item = next(item for item in items if item["id"] == int(self.path.split("/")[3]))
        item.update(update)
        self.send(item)

    def log_message(self, *_):
        pass


print(f"Synthetic dashboard at http://127.0.0.1:{args.port}", flush=True)
ThreadingHTTPServer(("127.0.0.1", args.port), Handler).serve_forever()
