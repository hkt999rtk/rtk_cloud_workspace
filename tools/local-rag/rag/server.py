from __future__ import annotations

import argparse
import json
import mimetypes
import os
import threading
import time
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
from urllib.parse import urlparse

from .indexer import LocalRagIndex
from .openai_client import OpenAIClient


class PollingWatcher:
    def __init__(self, index: LocalRagIndex, interval: float = 5.0) -> None:
        self.index = index
        self.interval = interval
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None

    def start(self) -> None:
        if self._thread:
            return
        self._thread = threading.Thread(target=self._run, name="local-rag-watcher", daemon=True)
        self._thread.start()

    def stop(self) -> None:
        self._stop.set()
        if self._thread:
            self._thread.join(timeout=2)

    def _run(self) -> None:
        while not self._stop.wait(self.interval):
            try:
                self.index.index_changed()
            except Exception:
                # Keep the web service available if a transient file read races with edits.
                pass


class RagRequestHandler(BaseHTTPRequestHandler):
    server_version = "LocalRag/0.1"

    @property
    def rag_index(self) -> LocalRagIndex:
        return self.server.rag_index  # type: ignore[attr-defined]

    @property
    def static_dir(self) -> Path:
        return self.server.static_dir  # type: ignore[attr-defined]

    def do_GET(self) -> None:
        parsed = urlparse(self.path)
        if parsed.path == "/api/status":
            self.write_json(self.rag_index.status())
            return
        if parsed.path.startswith("/api/"):
            self.write_json({"error": "not_found"}, HTTPStatus.NOT_FOUND)
            return
        self.serve_static(parsed.path)

    def do_POST(self) -> None:
        parsed = urlparse(self.path)
        if parsed.path == "/api/query":
            body = self.read_json()
            query = str(body.get("query", "")).strip()
            if not query:
                self.write_json({"error": "query is required"}, HTTPStatus.BAD_REQUEST)
                return
            filters = body.get("filters") if isinstance(body.get("filters"), dict) else {}
            self.write_json(self.rag_index.query(query, filters=filters))
            return
        if parsed.path == "/api/index/full":
            self.write_json(self.rag_index.index_full())
            return
        if parsed.path == "/api/index/changed":
            self.write_json(self.rag_index.index_changed())
            return
        self.write_json({"error": "not_found"}, HTTPStatus.NOT_FOUND)

    def read_json(self) -> dict[str, Any]:
        length = int(self.headers.get("Content-Length", "0") or "0")
        raw = self.rfile.read(length) if length else b"{}"
        try:
            data = json.loads(raw.decode("utf-8"))
        except json.JSONDecodeError:
            return {}
        return data if isinstance(data, dict) else {}

    def write_json(self, data: Any, status: HTTPStatus = HTTPStatus.OK) -> None:
        encoded = json.dumps(data, ensure_ascii=False, indent=2).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(encoded)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(encoded)

    def serve_static(self, path: str) -> None:
        if path in {"", "/"}:
            path = "/index.html"
        target = (self.static_dir / path.lstrip("/")).resolve()
        if not target.is_file() or self.static_dir not in target.parents:
            target = self.static_dir / "index.html"
        content = target.read_bytes()
        mime = mimetypes.guess_type(target.name)[0] or "application/octet-stream"
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Type", f"{mime}; charset=utf-8" if mime.startswith("text/") else mime)
        self.send_header("Content-Length", str(len(content)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(content)

    def log_message(self, format: str, *args: Any) -> None:
        print(f"{self.address_string()} - {format % args}")


def create_server(
    workspace: Path,
    db_path: Path | None,
    host: str,
    port: int,
    ai_client: OpenAIClient | None = None,
) -> ThreadingHTTPServer:
    index = LocalRagIndex(workspace, db_path, ai_client=ai_client)
    index.index_full()
    static_dir = Path(__file__).resolve().parents[1] / "web"
    server = ThreadingHTTPServer((host, port), RagRequestHandler)
    server.rag_index = index  # type: ignore[attr-defined]
    server.static_dir = static_dir  # type: ignore[attr-defined]
    return server


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="rag-server")
    parser.add_argument("--workspace", default=os.environ.get("RTK_RAG_WORKSPACE", Path.cwd().as_posix()))
    parser.add_argument("--db", default=os.environ.get("RTK_RAG_DB"))
    parser.add_argument("--host", default=os.environ.get("RTK_RAG_HOST", "127.0.0.1"))
    parser.add_argument("--port", type=int, default=int(os.environ.get("RTK_RAG_PORT", "8765")))
    parser.add_argument("--watch", action=argparse.BooleanOptionalAction, default=True)
    parser.add_argument("--watch-interval", type=float, default=5.0)
    args = parser.parse_args(argv)

    server = create_server(Path(args.workspace), Path(args.db) if args.db else None, args.host, args.port)
    watcher = PollingWatcher(server.rag_index, args.watch_interval)  # type: ignore[attr-defined]
    if args.watch:
        watcher.start()
    print(f"Local RAG server listening on http://{args.host}:{args.port}")
    print(f"Workspace: {Path(args.workspace).resolve()}")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        watcher.stop()
        server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
