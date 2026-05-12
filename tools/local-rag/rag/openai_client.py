from __future__ import annotations

import json
import os
import ssl
import urllib.error
import urllib.request
from pathlib import Path


def load_env_file(path: Path | None = None) -> None:
    env_path = path or Path.home() / ".env"
    if not env_path.exists():
        return
    for raw_line in env_path.read_text(encoding="utf-8", errors="ignore").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        value = value.strip().strip('"').strip("'")
        if key and key not in os.environ:
            os.environ[key] = value


class OpenAIClient:
    def __init__(
        self,
        base_url: str | None = None,
        api_key: str | None = None,
        embedding_model: str | None = None,
        answer_model: str | None = None,
        timeout: float = 20.0,
        enable_embeddings: bool | None = None,
        enable_answers: bool | None = None,
        env_file: Path | None = None,
    ) -> None:
        load_env_file(env_file)
        self.base_url = (base_url or os.environ.get("OPENAI_BASE_URL") or "https://api.openai.com/v1").rstrip("/")
        self.api_key = api_key or os.environ.get("OPENAI_API_KEY", "")
        self.embedding_model = embedding_model or os.environ.get("OPENAI_RAG_EMBEDDING_MODEL", "text-embedding-3-small")
        self.answer_model = answer_model or os.environ.get("OPENAI_RAG_ANSWER_MODEL", "gpt-4.1-mini")
        self.timeout = timeout
        self.enable_embeddings = (
            os.environ.get("RTK_RAG_ENABLE_EMBEDDINGS", "1") != "0" if enable_embeddings is None else enable_embeddings
        )
        self.enable_answers = os.environ.get("RTK_RAG_ENABLE_ANSWERS", "1") != "0" if enable_answers is None else enable_answers
        self._embedding_available: bool | None = None
        self._answer_available: bool | None = None
        self._ssl_context = default_ssl_context()

    def embed(self, text: str) -> list[float] | None:
        embeddings = self.embed_many([text])
        return embeddings[0] if embeddings else None

    def embed_many(self, texts: list[str]) -> list[list[float] | None]:
        if not self.enable_embeddings or not self.api_key:
            return [None for _ in texts]
        if self._embedding_available is False:
            return [None for _ in texts]
        payload = {"model": self.embedding_model, "input": texts}
        data = self._post_json("/embeddings", payload, timeout=self.timeout)
        if not data:
            self._embedding_available = False
            return [None for _ in texts]
        try:
            rows = sorted(data["data"], key=lambda item: item.get("index", 0))
            embeddings = [row.get("embedding") if isinstance(row, dict) else None for row in rows]
        except (KeyError, TypeError):
            self._embedding_available = False
            return [None for _ in texts]
        if len(embeddings) == len(texts) and all(isinstance(item, list) for item in embeddings):
            self._embedding_available = True
            return embeddings
        self._embedding_available = False
        return [None for _ in texts]

    def generate(self, prompt: str) -> str | None:
        if not self.enable_answers or not self.api_key:
            return None
        if self._answer_available is False:
            return None
        payload = {
            "model": self.answer_model,
            "input": prompt,
            "temperature": 0.2,
        }
        data = self._post_json("/responses", payload, timeout=max(self.timeout, 45.0))
        if not data:
            self._answer_available = False
            return None
        text = extract_response_text(data)
        if text:
            self._answer_available = True
            return text
        self._answer_available = False
        return None

    def _post_json(self, path: str, payload: dict, timeout: float) -> dict | None:
        encoded = json.dumps(payload).encode("utf-8")
        request = urllib.request.Request(
            f"{self.base_url}{path}",
            data=encoded,
            headers={
                "Authorization": f"Bearer {self.api_key}",
                "Content-Type": "application/json",
            },
            method="POST",
        )
        try:
            with urllib.request.urlopen(request, timeout=timeout, context=self._ssl_context) as response:
                return json.loads(response.read().decode("utf-8"))
        except (OSError, urllib.error.URLError, TimeoutError, json.JSONDecodeError):
            return None


def extract_response_text(data: dict) -> str | None:
    output_text = data.get("output_text")
    if isinstance(output_text, str) and output_text.strip():
        return output_text.strip()
    parts: list[str] = []
    for item in data.get("output", []) if isinstance(data.get("output"), list) else []:
        if not isinstance(item, dict):
            continue
        for content in item.get("content", []) if isinstance(item.get("content"), list) else []:
            if not isinstance(content, dict):
                continue
            text = content.get("text")
            if isinstance(text, str) and text.strip():
                parts.append(text.strip())
    return "\n".join(parts).strip() or None


def default_ssl_context() -> ssl.SSLContext:
    cafile = os.environ.get("SSL_CERT_FILE")
    if cafile:
        return ssl.create_default_context(cafile=cafile)
    try:
        import certifi  # type: ignore

        return ssl.create_default_context(cafile=certifi.where())
    except Exception:
        return ssl.create_default_context()
