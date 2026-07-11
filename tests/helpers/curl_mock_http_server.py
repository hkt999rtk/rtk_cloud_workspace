#!/usr/bin/env python3
"""Expose an existing curl fixture script as a local HTTP test server."""

import http.server
import os
import subprocess
import sys
import tempfile
from pathlib import Path


PORT = int(sys.argv[1])
CURL_FIXTURE = sys.argv[2]
CONTROL_FILE = Path(sys.argv[3]) if len(sys.argv) > 3 else None


def fixture_env():
    env = os.environ.copy()
    if CONTROL_FILE and CONTROL_FILE.exists():
        for raw in CONTROL_FILE.read_text().splitlines():
            raw = raw.strip()
            if not raw or raw.startswith("#") or "=" not in raw:
                continue
            key, value = raw.split("=", 1)
            env[key] = value
    return env


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def handle_request(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length) if length else b""
        with tempfile.TemporaryDirectory() as tmp:
            payload = Path(tmp) / "payload.json"
            output = Path(tmp) / "output.json"
            payload.write_bytes(body)
            url = f"http://127.0.0.1:{PORT}{self.path}"
            command = [
                CURL_FIXTURE,
                "-sS",
                "-X",
                self.command,
                "-o",
                str(output),
                "-w",
                "%{http_code}",
                "--data-binary",
                "@" + str(payload),
                url,
            ]
            completed = subprocess.run(command, env=fixture_env(), capture_output=True, text=True)
            if completed.returncode != 0:
                response = (completed.stderr or completed.stdout or "fixture failed").encode()
                status = 500
            else:
                try:
                    status = int(completed.stdout.strip())
                except ValueError:
                    status = 500
                response = output.read_bytes() if output.exists() else b""
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(response)))
        self.end_headers()
        self.wfile.write(response)

    do_GET = handle_request
    do_POST = handle_request
    do_DELETE = handle_request
    do_PATCH = handle_request
    do_PUT = handle_request


http.server.ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
