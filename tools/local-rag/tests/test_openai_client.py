import json
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

from rag.openai_client import OpenAIClient, load_env_file


class FakeOpenAIHandler(BaseHTTPRequestHandler):
    requests = []

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = json.loads(self.rfile.read(length).decode("utf-8"))
        self.__class__.requests.append((self.path, self.headers.get("Authorization"), body))
        if self.path == "/v1/embeddings":
            inputs = body["input"]
            data = [{"index": idx, "embedding": [float(idx), 1.0, 0.5]} for idx, _ in enumerate(inputs)]
            self.write_json({"data": data})
            return
        if self.path == "/v1/responses":
            self.write_json({"output_text": "直接答案\nOpenAI response"})
            return
        self.send_response(404)
        self.end_headers()

    def write_json(self, payload):
        encoded = json.dumps(payload).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def log_message(self, *args):
        pass


class OpenAIClientTest(unittest.TestCase):
    def test_load_env_file_does_not_override_existing_environment(self):
        with tempfile.TemporaryDirectory() as tmp:
            env_file = Path(tmp) / ".env"
            env_file.write_text("OPENAI_API_KEY=from-file\nCUSTOM_KEY=value\n", encoding="utf-8")
            old = __import__("os").environ.get("OPENAI_API_KEY")
            __import__("os").environ["OPENAI_API_KEY"] = "existing"
            try:
                load_env_file(env_file)
                self.assertEqual(__import__("os").environ["OPENAI_API_KEY"], "existing")
                self.assertEqual(__import__("os").environ["CUSTOM_KEY"], "value")
            finally:
                if old is None:
                    __import__("os").environ.pop("OPENAI_API_KEY", None)
                else:
                    __import__("os").environ["OPENAI_API_KEY"] = old
                __import__("os").environ.pop("CUSTOM_KEY", None)

    def test_embeddings_and_responses_use_openai_api_shape(self):
        FakeOpenAIHandler.requests = []
        server = ThreadingHTTPServer(("127.0.0.1", 0), FakeOpenAIHandler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            client = OpenAIClient(
                base_url=f"http://127.0.0.1:{server.server_address[1]}/v1",
                api_key="test-key",
                embedding_model="text-embedding-3-small",
                answer_model="gpt-4.1-mini",
            )
            embeddings = client.embed_many(["a", "b"])
            answer = client.generate("hello")
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)

        self.assertEqual(embeddings, [[0.0, 1.0, 0.5], [1.0, 1.0, 0.5]])
        self.assertEqual(answer, "直接答案\nOpenAI response")
        self.assertEqual(FakeOpenAIHandler.requests[0][0], "/v1/embeddings")
        self.assertEqual(FakeOpenAIHandler.requests[0][1], "Bearer test-key")
        self.assertEqual(FakeOpenAIHandler.requests[0][2]["model"], "text-embedding-3-small")
        self.assertEqual(FakeOpenAIHandler.requests[1][0], "/v1/responses")
        self.assertEqual(FakeOpenAIHandler.requests[1][2]["model"], "gpt-4.1-mini")
