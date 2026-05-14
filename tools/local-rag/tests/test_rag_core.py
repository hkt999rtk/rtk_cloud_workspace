import json
import os
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

from rag.indexer import LocalRagIndex
from rag.metadata import classify_document, discover_repositories


class NoopAI:
    enable_embeddings = False

    def embed(self, text):
        return None

    def embed_many(self, texts):
        return [None for _ in texts]

    def generate(self, prompt):
        return None


def make_index(workspace: Path) -> LocalRagIndex:
    return LocalRagIndex(workspace, workspace / ".rag" / "rag.db", ai_client=NoopAI())


def init_git_repo(path: Path) -> str:
    subprocess.run(["git", "init", "-q"], cwd=path, check=True)
    subprocess.run(["git", "config", "user.email", "test@example.com"], cwd=path, check=True)
    subprocess.run(["git", "config", "user.name", "Test User"], cwd=path, check=True)
    (path / "README.md").write_text("# repo\n", encoding="utf-8")
    subprocess.run(["git", "add", "README.md"], cwd=path, check=True)
    subprocess.run(["git", "commit", "-q", "-m", "init"], cwd=path, check=True)
    return subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=path, text=True).strip()


def make_workspace(tmp_path: Path) -> Path:
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    init_git_repo(workspace)
    (workspace / "repos").mkdir()
    contracts = workspace / "repos" / "rtk_cloud_contracts_doc"
    service = workspace / "repos" / "rtk_video_cloud"
    copied = service / "docs" / "rtk_cloud_contracts_doc"
    contracts.mkdir(parents=True)
    service.mkdir(parents=True)
    copied.mkdir(parents=True)
    init_git_repo(contracts)
    init_git_repo(service)
    return workspace


class LocalRagCoreTest(unittest.TestCase):
    def test_markdown_chunking_preserves_heading_and_line_metadata(self):
        with tempfile.TemporaryDirectory() as tmp:
            workspace = make_workspace(Path(tmp))
            doc = workspace / "repos" / "rtk_video_cloud" / "docs" / "auth.md"
            doc.write_text(
                "# Auth\n\nIntro.\n\n## Device Certificate\n\nDevice obtains a certificate during activation.\n",
                encoding="utf-8",
            )

            index = make_index(workspace)
            chunks = index.chunk_file(doc)

            cert_chunk = next(chunk for chunk in chunks if "Device obtains" in chunk.content)
            self.assertEqual(cert_chunk.heading, "Auth > Device Certificate")
            self.assertEqual(cert_chunk.line_start, 5)
            self.assertEqual(cert_chunk.line_end, 7)
            self.assertEqual(cert_chunk.file_path, "repos/rtk_video_cloud/docs/auth.md")
            self.assertEqual(cert_chunk.repo_name, "rtk_video_cloud")
            self.assertEqual(cert_chunk.source_layer, "service")

    def test_yaml_openapi_ingestion_and_classification(self):
        with tempfile.TemporaryDirectory() as tmp:
            workspace = make_workspace(Path(tmp))
            api = workspace / "repos" / "rtk_video_cloud" / "docs" / "openapi.yaml"
            api.write_text(
                "openapi: 3.0.0\npaths:\n  /devices/{id}/activate:\n    post:\n      summary: Device activation\n",
                encoding="utf-8",
            )

            index = make_index(workspace)
            index.index_full()
            results = index.search("device activation", limit=5)

            self.assertTrue(any("Device activation" in result.content for result in results))
            self.assertEqual(classify_document(api.relative_to(workspace)), ("source", "service"))

    def test_contracts_source_ranks_above_copied_contract_docs(self):
        with tempfile.TemporaryDirectory() as tmp:
            workspace = make_workspace(Path(tmp))
            canonical = workspace / "repos" / "rtk_cloud_contracts_doc" / "AUTH.md"
            copy = workspace / "repos" / "rtk_video_cloud" / "docs" / "rtk_cloud_contracts_doc" / "AUTH.md"
            canonical.write_text("# Auth\n\nDevice token is issued by canonical contract.\n", encoding="utf-8")
            copy.write_text("# Auth\n\nDevice token is copied service documentation.\n", encoding="utf-8")

            index = make_index(workspace)
            index.index_full()
            results = index.search("device token auth contract", limit=2)

            self.assertEqual(results[0].file_path, "repos/rtk_cloud_contracts_doc/AUTH.md")
            self.assertEqual(results[0].doc_classification, "source")
            self.assertEqual(results[0].source_layer, "contracts")

    def test_changed_index_marks_deleted_chunks_inactive(self):
        with tempfile.TemporaryDirectory() as tmp:
            workspace = make_workspace(Path(tmp))
            doc = workspace / "docs" / "runtime.md"
            doc.parent.mkdir(exist_ok=True)
            doc.write_text("# Runtime\n\nVideo server uses API, storage, MQTT, and WebRTC.\n", encoding="utf-8")

            index = make_index(workspace)
            index.index_full()
            self.assertTrue(index.search("WebRTC storage MQTT", limit=1))

            doc.unlink()
            index.index_changed()

            with sqlite3.connect(index.db_path) as conn:
                inactive = conn.execute("select active from documents where path = ?", ("docs/runtime.md",)).fetchone()[0]
            self.assertEqual(inactive, 0)
            self.assertEqual(index.search("WebRTC storage MQTT", limit=1), [])

    def test_repository_status_reports_dirty_submodule_like_repo(self):
        with tempfile.TemporaryDirectory() as tmp:
            workspace = make_workspace(Path(tmp))
            repo = workspace / "repos" / "rtk_video_cloud"
            (repo / "dirty.md").write_text("dirty\n", encoding="utf-8")

            repos = discover_repositories(workspace)
            video = next(item for item in repos if item.name == "rtk_video_cloud")

            self.assertTrue(video.dirty)
            self.assertEqual(video.path, "repos/rtk_video_cloud")
            self.assertEqual(len(video.commit_sha), 40)

    def test_query_returns_answer_citations_and_conflict_notes(self):
        with tempfile.TemporaryDirectory() as tmp:
            workspace = make_workspace(Path(tmp))
            (workspace / "repos" / "rtk_cloud_contracts_doc" / "AUTH.md").write_text(
                "# Auth\n\nDevices obtain credentials during activation using a signed device certificate.\n",
                encoding="utf-8",
            )
            (workspace / "repos" / "rtk_video_cloud" / "docs" / "auth.md").write_text(
                "# Auth\n\nLegacy notes say devices use a bootstrap token before certificates.\n",
                encoding="utf-8",
            )

            index = make_index(workspace)
            index.index_full()
            response = index.query("device 怎麼取得認證")

            self.assertIn("直接答案", response["answer"])
            self.assertTrue(response["citations"])
            self.assertTrue(response["matched_chunks"])
            self.assertTrue(response["confidence_notes"])
            self.assertTrue(response["conflicts"])
            self.assertEqual(response["citations"][0]["path"], "repos/rtk_cloud_contracts_doc/AUTH.md")
