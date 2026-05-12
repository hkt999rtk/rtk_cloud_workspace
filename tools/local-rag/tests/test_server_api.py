import json
import sys
import tempfile
import threading
import unittest
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

from rag.server import create_server
from test_rag_core import NoopAI, init_git_repo


class ServerApiTest(unittest.TestCase):
    def test_status_query_and_reindex_endpoints(self):
        with tempfile.TemporaryDirectory() as tmp:
            workspace = Path(tmp) / "workspace"
            workspace.mkdir()
            init_git_repo(workspace)
            docs = workspace / "docs"
            docs.mkdir()
            (docs / "auth.md").write_text(
                "# Auth\n\nDevice activation issues a certificate and token.\n",
                encoding="utf-8",
            )
            server = create_server(workspace, workspace / ".rag" / "rag.db", "127.0.0.1", 0, ai_client=NoopAI())
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            base = f"http://127.0.0.1:{server.server_address[1]}"
            try:
                status = get_json(f"{base}/api/status")
                self.assertGreaterEqual(status["active_documents"], 1)

                query = post_json(f"{base}/api/query", {"query": "device 認證"})
                self.assertIn("直接答案", query["answer"])
                self.assertTrue(query["citations"])

                changed = post_json(f"{base}/api/index/changed", {})
                self.assertIn("active_files", changed)
            finally:
                server.shutdown()
                server.server_close()
                thread.join(timeout=2)


def get_json(url: str):
    with urllib.request.urlopen(url, timeout=5) as response:
        return json.loads(response.read().decode("utf-8"))


def post_json(url: str, payload: dict):
    request = urllib.request.Request(
        url,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=5) as response:
        return json.loads(response.read().decode("utf-8"))
