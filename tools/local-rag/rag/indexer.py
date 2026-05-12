from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import re
import sqlite3
import time
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any

from .metadata import RepositoryInfo, classify_document, discover_repositories, repo_for_path
from .openai_client import OpenAIClient


INDEXABLE_SUFFIXES = {".md", ".markdown", ".yaml", ".yml", ".toml", ".json", ".go", ".js", ".jsx", ".mjs", ".ts", ".tsx"}
EXCLUDED_PARTS = {
    ".git",
    ".rag",
    ".claude",
    ".codex",
    ".worktrees",
    "node_modules",
    "vendor",
    "dist",
    "build",
    "target",
    ".cache",
    "__pycache__",
    "coverage",
    "fixtures",
    "3rd_party",
}
CODE_INCLUDE_HINTS = (
    "/cmd/",
    "/internal/config/",
    "/internal/app/",
    "/internal/routes",
    "/web/src/routes",
    "/web/src/http",
    "main.go",
)


@dataclass(frozen=True)
class Chunk:
    content: str
    heading: str
    workspace_path: str
    repo_name: str
    submodule_path: str
    commit_sha: str
    branch_or_detached_state: str
    file_path: str
    line_start: int
    line_end: int
    doc_classification: str
    source_layer: str
    content_hash: str


@dataclass(frozen=True)
class SearchResult:
    chunk_id: int
    score: float
    content: str
    heading: str
    repo_name: str
    file_path: str
    line_start: int
    line_end: int
    doc_classification: str
    source_layer: str
    commit_sha: str
    branch_or_detached_state: str

    def citation(self) -> dict[str, Any]:
        return {
            "repo": self.repo_name,
            "path": self.file_path,
            "lines": [self.line_start, self.line_end],
            "commit": self.commit_sha,
            "heading": self.heading,
            "classification": self.doc_classification,
            "source_layer": self.source_layer,
            "score": round(self.score, 4),
        }


class LocalRagIndex:
    def __init__(self, workspace: Path, db_path: Path | None = None, ai_client: OpenAIClient | None = None) -> None:
        self.workspace = workspace.resolve()
        self.db_path = (db_path or self.workspace / ".rag" / "rag.db").resolve()
        self.ai_client = ai_client or OpenAIClient()
        self._repo_cache: list[RepositoryInfo] | None = None

    def connect(self) -> sqlite3.Connection:
        self.db_path.parent.mkdir(parents=True, exist_ok=True)
        conn = sqlite3.connect(self.db_path)
        conn.row_factory = sqlite3.Row
        conn.execute("pragma journal_mode = wal")
        self.ensure_schema(conn)
        return conn

    def ensure_schema(self, conn: sqlite3.Connection) -> None:
        conn.executescript(
            """
            create table if not exists repositories (
              path text primary key,
              name text not null,
              commit_sha text not null,
              branch_or_detached_state text not null,
              dirty integer not null,
              indexed_at real not null
            );
            create table if not exists documents (
              path text primary key,
              repo_name text not null,
              submodule_path text not null,
              commit_sha text not null,
              branch_or_detached_state text not null,
              doc_classification text not null,
              source_layer text not null,
              content_hash text not null,
              active integer not null,
              indexed_at real not null
            );
            create table if not exists chunks (
              id integer primary key autoincrement,
              document_path text not null,
              repo_name text not null,
              submodule_path text not null,
              commit_sha text not null,
              branch_or_detached_state text not null,
              heading text not null,
              line_start integer not null,
              line_end integer not null,
              doc_classification text not null,
              source_layer text not null,
              content text not null,
              content_hash text not null,
              embedding_json text,
              active integer not null,
              indexed_at real not null
            );
            create virtual table if not exists chunk_fts using fts5(
              content,
              heading,
              document_path unindexed,
              content='chunks',
              content_rowid='id'
            );
            """
        )

    def index_full(self) -> dict[str, Any]:
        conn = self.connect()
        try:
            self._repo_cache = discover_repositories(self.workspace)
            self._update_repositories(conn)
            paths = list(self.iter_indexable_files())
            active_paths = {path.relative_to(self.workspace).as_posix() for path in paths}
            existing = {
                row["path"]
                for row in conn.execute("select path from documents where active = 1").fetchall()
            }
            for removed in sorted(existing - active_paths):
                self._deactivate_document(conn, removed)
            indexed = 0
            skipped = 0
            for path in paths:
                if self._index_file_if_changed(conn, path):
                    indexed += 1
                else:
                    skipped += 1
            conn.commit()
            return {"indexed": indexed, "skipped": skipped, "active_files": len(active_paths)}
        finally:
            self._repo_cache = None
            conn.close()

    def index_changed(self) -> dict[str, Any]:
        return self.index_full()

    def _update_repositories(self, conn: sqlite3.Connection) -> None:
        now = time.time()
        for repo in self._repo_cache or discover_repositories(self.workspace):
            conn.execute(
                """
                insert into repositories(path, name, commit_sha, branch_or_detached_state, dirty, indexed_at)
                values (?, ?, ?, ?, ?, ?)
                on conflict(path) do update set
                  name=excluded.name,
                  commit_sha=excluded.commit_sha,
                  branch_or_detached_state=excluded.branch_or_detached_state,
                  dirty=excluded.dirty,
                  indexed_at=excluded.indexed_at
                """,
                (repo.path, repo.name, repo.commit_sha, repo.branch_or_detached_state, int(repo.dirty), now),
            )

    def iter_indexable_files(self) -> list[Path]:
        files: list[Path] = []
        for root, dirs, names in os.walk(self.workspace):
            root_path = Path(root)
            dirs[:] = [item for item in dirs if item not in EXCLUDED_PARTS and not item.startswith(".")]
            if any(part in EXCLUDED_PARTS for part in root_path.relative_to(self.workspace).parts):
                continue
            for name in names:
                path = root_path / name
                if self.should_index(path):
                    files.append(path)
        return sorted(files)

    def should_index(self, path: Path) -> bool:
        rel = path.relative_to(self.workspace).as_posix()
        suffix = path.suffix.lower()
        if suffix not in INDEXABLE_SUFFIXES:
            return False
        if any(part in EXCLUDED_PARTS for part in Path(rel).parts):
            return False
        if path.stat().st_size > 512_000:
            return False
        if suffix in {".go", ".js", ".jsx", ".mjs", ".ts", ".tsx"}:
            return any(hint in f"/{rel}" for hint in CODE_INCLUDE_HINTS)
        return True

    def _index_file_if_changed(self, conn: sqlite3.Connection, path: Path) -> bool:
        path = path.resolve()
        rel = path.relative_to(self.workspace).as_posix()
        text = path.read_text(encoding="utf-8", errors="replace")
        content_hash = hashlib.sha256(text.encode("utf-8")).hexdigest()
        current = conn.execute("select content_hash, active from documents where path = ?", (rel,)).fetchone()
        if current and current["content_hash"] == content_hash and current["active"] == 1:
            if not self._needs_embedding_refresh(conn, rel):
                return False

        self._deactivate_document(conn, rel)
        repo = self._repo_for_path(path) if self._repo_cache is not None else repo_for_path(self.workspace, path)
        doc_classification, source_layer = classify_document(rel)
        now = time.time()
        conn.execute(
            """
            insert into documents(path, repo_name, submodule_path, commit_sha, branch_or_detached_state,
              doc_classification, source_layer, content_hash, active, indexed_at)
            values (?, ?, ?, ?, ?, ?, ?, ?, 1, ?)
            on conflict(path) do update set
              repo_name=excluded.repo_name,
              submodule_path=excluded.submodule_path,
              commit_sha=excluded.commit_sha,
              branch_or_detached_state=excluded.branch_or_detached_state,
              doc_classification=excluded.doc_classification,
              source_layer=excluded.source_layer,
              content_hash=excluded.content_hash,
              active=1,
              indexed_at=excluded.indexed_at
            """,
            (
                rel,
                repo.name,
                repo.path,
                repo.commit_sha,
                repo.branch_or_detached_state,
                doc_classification,
                source_layer,
                content_hash,
                now,
            ),
        )
        chunks = self.chunk_file(path)
        embeddings = self._embed_chunks(chunks)
        for chunk, embedding in zip(chunks, embeddings):
            cursor = conn.execute(
                """
                insert into chunks(document_path, repo_name, submodule_path, commit_sha, branch_or_detached_state,
                  heading, line_start, line_end, doc_classification, source_layer, content, content_hash,
                  embedding_json, active, indexed_at)
                values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)
                """,
                (
                    chunk.file_path,
                    chunk.repo_name,
                    chunk.submodule_path,
                    chunk.commit_sha,
                    chunk.branch_or_detached_state,
                    chunk.heading,
                    chunk.line_start,
                    chunk.line_end,
                    chunk.doc_classification,
                    chunk.source_layer,
                    chunk.content,
                    chunk.content_hash,
                    json.dumps(embedding) if embedding else None,
                    now,
                ),
            )
            conn.execute(
                "insert into chunk_fts(rowid, content, heading, document_path) values (?, ?, ?, ?)",
                (cursor.lastrowid, chunk.content, chunk.heading, chunk.file_path),
            )
        return True

    def _needs_embedding_refresh(self, conn: sqlite3.Connection, rel: str) -> bool:
        if not getattr(self.ai_client, "enable_embeddings", False):
            return False
        missing = conn.execute(
            "select count(*) from chunks where document_path = ? and active = 1 and embedding_json is null",
            (rel,),
        ).fetchone()[0]
        return bool(missing)

    def _embed_chunks(self, chunks: list[Chunk]) -> list[list[float] | None]:
        if hasattr(self.ai_client, "embed_many"):
            return self.ai_client.embed_many([chunk.content[:4000] for chunk in chunks])
        return [self.ai_client.embed(chunk.content[:4000]) for chunk in chunks]

    def _deactivate_document(self, conn: sqlite3.Connection, rel: str) -> None:
        ids = [row["id"] for row in conn.execute("select id from chunks where document_path = ? and active = 1", (rel,))]
        for chunk_id in ids:
            conn.execute("delete from chunk_fts where rowid = ?", (chunk_id,))
        conn.execute("update chunks set active = 0 where document_path = ?", (rel,))
        conn.execute("update documents set active = 0 where path = ?", (rel,))

    def chunk_file(self, path: Path) -> list[Chunk]:
        path = path.resolve()
        rel = path.relative_to(self.workspace).as_posix()
        repo = self._repo_for_path(path) if self._repo_cache is not None else repo_for_path(self.workspace, path)
        doc_classification, source_layer = classify_document(rel)
        text = path.read_text(encoding="utf-8", errors="replace")
        lines = text.splitlines()
        suffix = path.suffix.lower()
        if suffix in {".md", ".markdown"}:
            spans = self._markdown_spans(lines)
        else:
            spans = self._plain_spans(lines)
        chunks: list[Chunk] = []
        for line_start, line_end, heading, content in spans:
            normalized = content.strip()
            if not normalized:
                continue
            chunks.append(
                Chunk(
                    content=normalized,
                    heading=heading or Path(rel).name,
                    workspace_path=self.workspace.as_posix(),
                    repo_name=repo.name,
                    submodule_path=repo.path,
                    commit_sha=repo.commit_sha,
                    branch_or_detached_state=repo.branch_or_detached_state,
                    file_path=rel,
                    line_start=line_start,
                    line_end=line_end,
                    doc_classification=doc_classification,
                    source_layer=source_layer,
                    content_hash=hashlib.sha256(normalized.encode("utf-8")).hexdigest(),
                )
            )
        return chunks

    def _repo_for_path(self, file_path: Path) -> RepositoryInfo:
        candidates = self._repo_cache or discover_repositories(self.workspace)
        matches: list[tuple[int, RepositoryInfo]] = []
        for repo in candidates:
            root = self.workspace if repo.path == "." else self.workspace / repo.path
            try:
                file_path.relative_to(root)
                matches.append((len(root.as_posix()), repo))
            except ValueError:
                continue
        return sorted(matches, reverse=True)[0][1] if matches else candidates[0]

    def _markdown_spans(self, lines: list[str]) -> list[tuple[int, int, str, str]]:
        spans: list[tuple[int, int, str, str]] = []
        heading_stack: list[tuple[int, str]] = []
        start = 1
        active_heading = ""
        buffer: list[str] = []

        def flush(end_line: int) -> None:
            if buffer and "\n".join(buffer).strip():
                spans.append((start, end_line, active_heading, "\n".join(buffer)))

        for index, line in enumerate(lines, start=1):
            match = re.match(r"^(#{1,6})\s+(.+?)\s*$", line)
            if match:
                flush(index - 1)
                level = len(match.group(1))
                title = match.group(2).strip()
                heading_stack[:] = [(lvl, text) for lvl, text in heading_stack if lvl < level]
                heading_stack.append((level, title))
                active_heading = " > ".join(text for _, text in heading_stack)
                start = index
                buffer = [line]
            else:
                buffer.append(line)
        flush(len(lines))
        return self._split_large_spans(spans)

    def _plain_spans(self, lines: list[str]) -> list[tuple[int, int, str, str]]:
        spans: list[tuple[int, int, str, str]] = []
        for offset in range(0, len(lines), 80):
            chunk = lines[offset : offset + 80]
            spans.append((offset + 1, offset + len(chunk), "", "\n".join(chunk)))
        return self._split_large_spans(spans)

    def _split_large_spans(self, spans: list[tuple[int, int, str, str]]) -> list[tuple[int, int, str, str]]:
        result: list[tuple[int, int, str, str]] = []
        for start, end, heading, content in spans:
            lines = content.splitlines()
            if len(content) <= 2200:
                result.append((start, end, heading, content))
                continue
            for offset in range(0, len(lines), 45):
                piece = lines[offset : offset + 45]
                result.append((start + offset, start + offset + len(piece) - 1, heading, "\n".join(piece)))
        return result

    def search(self, query: str, limit: int = 8, filters: dict[str, Any] | None = None) -> list[SearchResult]:
        expanded = expand_query(query)
        filters = filters or {}
        conn = self.connect()
        try:
            rows = self._fts_rows(conn, expanded, limit * 4, filters)
            if not rows:
                rows = self._like_rows(conn, expanded, limit * 4, filters)
            query_embedding = self.ai_client.embed(expanded)
            results = [self._row_to_result(row, expanded, query_embedding) for row in rows]
        finally:
            conn.close()
        return sorted(results, key=lambda item: item.score, reverse=True)[:limit]

    def _fts_rows(self, conn: sqlite3.Connection, query: str, limit: int, filters: dict[str, Any]) -> list[sqlite3.Row]:
        terms = tokenize(query)
        if not terms:
            return []
        fts_query = " OR ".join(terms[:12])
        where, params = self._filter_clause(filters)
        sql = f"""
            select c.*, bm25(chunk_fts) as rank
            from chunk_fts
            join chunks c on c.id = chunk_fts.rowid
            where chunk_fts match ? and c.active = 1 {where}
            order by rank
            limit ?
        """
        try:
            return conn.execute(sql, [fts_query, *params, limit]).fetchall()
        except sqlite3.OperationalError:
            return []

    def _like_rows(self, conn: sqlite3.Connection, query: str, limit: int, filters: dict[str, Any]) -> list[sqlite3.Row]:
        terms = tokenize(query)[:8]
        where, params = self._filter_clause(filters)
        clauses = " or ".join(["lower(content) like ? or lower(heading) like ?" for _ in terms])
        like_params: list[str] = []
        for term in terms:
            like_params.extend([f"%{term.lower()}%", f"%{term.lower()}%"])
        if not clauses:
            clauses = "1=1"
        sql = f"select *, 10.0 as rank from chunks where active = 1 and ({clauses}) {where} limit ?"
        return conn.execute(sql, [*like_params, *params, limit]).fetchall()

    def _filter_clause(self, filters: dict[str, Any]) -> tuple[str, list[Any]]:
        clauses = []
        params: list[Any] = []
        for key, column in (("repo", "repo_name"), ("source_layer", "source_layer"), ("classification", "doc_classification")):
            value = filters.get(key)
            if value:
                clauses.append(f"and {column} = ?")
                params.append(value)
        return " ".join(clauses), params

    def _row_to_result(self, row: sqlite3.Row, query: str, query_embedding: list[float] | None) -> SearchResult:
        base = 1.0 / (1.0 + abs(float(row["rank"]))) if "rank" in row.keys() else 0.2
        lexical = lexical_score(query, f"{row['heading']} {row['content']}")
        authority = authority_weight(row["doc_classification"], row["source_layer"], row["document_path"])
        scope_penalty = -0.9 if "out of scope" in str(row["heading"]).lower() and "scope" not in query.lower() else 0.0
        vector = 0.0
        if query_embedding and row["embedding_json"]:
            try:
                vector = cosine(query_embedding, json.loads(row["embedding_json"]))
            except (TypeError, ValueError, json.JSONDecodeError):
                vector = 0.0
        score = base + lexical + authority + vector + scope_penalty
        return SearchResult(
            chunk_id=int(row["id"]),
            score=score,
            content=row["content"],
            heading=row["heading"],
            repo_name=row["repo_name"],
            file_path=row["document_path"],
            line_start=int(row["line_start"]),
            line_end=int(row["line_end"]),
            doc_classification=row["doc_classification"],
            source_layer=row["source_layer"],
            commit_sha=row["commit_sha"],
            branch_or_detached_state=row["branch_or_detached_state"],
        )

    def query(self, query: str, filters: dict[str, Any] | None = None, limit: int = 8) -> dict[str, Any]:
        results = self.search(query, limit=limit, filters=filters)
        citations = [result.citation() for result in results]
        conflicts = detect_conflicts(results)
        local_answer = compose_answer(query, results, conflicts)
        generated = self.ai_client.generate(answer_prompt(query, results, conflicts))
        answer = generated if generated else local_answer
        return {
            "query": query,
            "answer": answer,
            "citations": citations,
            "matched_chunks": [asdict(result) for result in results],
            "conflicts": conflicts,
            "confidence_notes": confidence_notes(results, generated is not None),
        }

    def status(self) -> dict[str, Any]:
        conn = self.connect()
        try:
            self._update_repositories(conn)
            repositories = [dict(row) for row in conn.execute("select * from repositories order by path").fetchall()]
            active_documents = conn.execute("select count(*) from documents where active = 1").fetchone()[0]
            active_chunks = conn.execute("select count(*) from chunks where active = 1").fetchone()[0]
            last_indexed = conn.execute("select max(indexed_at) from documents").fetchone()[0]
            conn.commit()
        finally:
            conn.close()
        return {
            "workspace": self.workspace.as_posix(),
            "db_path": self.db_path.as_posix(),
            "active_documents": active_documents,
            "active_chunks": active_chunks,
            "last_indexed_at": last_indexed,
            "repositories": repositories,
            "dirty_repositories": [repo for repo in repositories if repo["dirty"]],
        }


def tokenize(text: str) -> list[str]:
    terms = re.findall(r"[A-Za-z0-9_./:-]+|[\u4e00-\u9fff]{2,}", text.lower())
    stop = {"the", "and", "or", "of", "for", "有哪些", "怎麼", "取得"}
    return [term.strip("./:-") for term in terms if len(term.strip("./:-")) >= 2 and term not in stop]


def expand_query(query: str) -> str:
    expansions = {
        "認證": "auth authentication credential certificate cert token activation provision provisioning",
        "凭證": "auth authentication credential certificate cert token activation provision provisioning",
        "组成": "architecture component service runtime inventory deployment",
        "組成": "architecture component service runtime inventory deployment",
        "video server": "rtk_video_cloud API media storage WebRTC MQTT NATS Postgres",
        "device": "device registry activation certificate token transport provisioning",
        "webrtc": "streaming WebRTC TURN ICE session",
        "mqtt": "MQTT broker transport EMQX topic",
    }
    expanded = [query]
    lower = query.lower()
    for term, addition in expansions.items():
        if term.lower() in lower:
            expanded.append(addition)
    return " ".join(expanded)


def lexical_score(query: str, text: str) -> float:
    terms = set(tokenize(query))
    if not terms:
        return 0.0
    haystack = text.lower()
    hits = sum(1 for term in terms if term in haystack)
    return hits / len(terms)


def authority_weight(classification: str, layer: str, path: str) -> float:
    weight = 0.0
    if classification == "source":
        weight += 0.5
    elif classification == "reference-only":
        weight -= 0.2
    if layer == "contracts":
        weight += 1.0
    elif layer == "workspace":
        weight += 0.5
    elif layer == "service":
        weight += 0.25
    elif layer == "generated":
        weight -= 0.25
    if "/rtk_cloud_contracts_doc/" in path and not path.startswith("repos/rtk_cloud_contracts_doc/"):
        weight -= 0.5
    return weight


def cosine(left: list[float], right: list[float]) -> float:
    if not left or not right or len(left) != len(right):
        return 0.0
    dot = sum(a * b for a, b in zip(left, right))
    left_norm = math.sqrt(sum(a * a for a in left))
    right_norm = math.sqrt(sum(b * b for b in right))
    return dot / (left_norm * right_norm) if left_norm and right_norm else 0.0


def detect_conflicts(results: list[SearchResult]) -> list[dict[str, Any]]:
    if len(results) < 2:
        return []
    layers = {result.source_layer for result in results[:5]}
    conflict_terms = ("legacy", "deprecated", "instead", "before", "only", "must", "should")
    if len(layers) > 1 and any(term in result.content.lower() for result in results[:5] for term in conflict_terms):
        return [
            {
                "type": "possible_source_mismatch",
                "message": "Multiple source layers mention this topic; verify the canonical contract before treating service-local notes as normative.",
                "paths": [result.file_path for result in results[:5]],
            }
        ]
    return []


def compose_answer(query: str, results: list[SearchResult], conflicts: list[dict[str, Any]]) -> str:
    if not results:
        return "直接答案\n找不到足夠的本地索引內容回答這個問題。\n\n依據來源\n無。\n\n相關文件\n無。\n\n不確定或衝突\n請先執行 full index 或放寬 filter。"
    top = results[:4]
    direct_lines = [f"- {summarize_chunk(result.content)}" for result in top]
    source_lines = [
        f"- [{idx}] {result.file_path}:{result.line_start}-{result.line_end} ({result.source_layer}, {result.doc_classification}, {result.commit_sha[:12]})"
        for idx, result in enumerate(top, start=1)
    ]
    related = sorted({result.file_path for result in results})
    conflict_text = (
        "\n".join(f"- {item['message']}" for item in conflicts)
        if conflicts
        else "- 未偵測到明確衝突；仍應以引用來源中的 canonical/source 文件為準。"
    )
    return (
        "直接答案\n"
        + "\n".join(direct_lines)
        + "\n\n依據來源\n"
        + "\n".join(source_lines)
        + "\n\n相關文件\n"
        + "\n".join(f"- {path}" for path in related[:8])
        + "\n\n不確定或衝突\n"
        + conflict_text
    )


def summarize_chunk(content: str) -> str:
    text = re.sub(r"\s+", " ", content).strip()
    text = re.sub(r"^#+\s*", "", text)
    return text[:260] + ("..." if len(text) > 260 else "")


def answer_prompt(query: str, results: list[SearchResult], conflicts: list[dict[str, Any]]) -> str:
    context = "\n\n".join(
        f"[{idx}] {result.file_path}:{result.line_start}-{result.line_end} ({result.source_layer}/{result.doc_classification})\n{result.content}"
        for idx, result in enumerate(results[:8], start=1)
    )
    return (
        "你是本機 RTK Cloud workspace RAG 助手。只能根據 CONTEXT 回答，必須用繁體中文，並保留四段標題："
        "直接答案、依據來源、相關文件、不確定或衝突。不要編造未出現在 CONTEXT 的事實。\n\n"
        f"QUESTION:\n{query}\n\nCONTEXT:\n{context}\n\nPOSSIBLE_CONFLICTS:\n{json.dumps(conflicts, ensure_ascii=False)}"
    )


def confidence_notes(results: list[SearchResult], used_llm: bool) -> list[str]:
    notes = ["answer_llm=openai" if used_llm else "answer_llm=unavailable; used extractive local summary"]
    if not results:
        notes.append("no_results")
    elif results[0].source_layer == "contracts":
        notes.append("top_result_is_canonical_contract")
    else:
        notes.append("top_result_is_not_contract; verify against contracts when API behavior matters")
    if any(result.source_layer == "generated" for result in results):
        notes.append("some_results_are_reference_only_or_copied_docs")
    return notes


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="rag")
    parser.add_argument("--workspace", default=os.environ.get("RTK_RAG_WORKSPACE", Path.cwd().as_posix()))
    parser.add_argument("--db", default=os.environ.get("RTK_RAG_DB"))
    sub = parser.add_subparsers(dest="command", required=True)
    index_parser = sub.add_parser("index")
    index_parser.add_argument("--full", action="store_true")
    index_parser.add_argument("--changed", action="store_true")
    sub.add_parser("status")
    query_parser = sub.add_parser("query")
    query_parser.add_argument("query")
    args = parser.parse_args(argv)
    workspace = Path(args.workspace)
    db_path = Path(args.db) if args.db else None
    index = LocalRagIndex(workspace, db_path)
    if args.command == "index":
        result = index.index_changed() if args.changed and not args.full else index.index_full()
        print(json.dumps(result, ensure_ascii=False, indent=2))
    elif args.command == "status":
        print(json.dumps(index.status(), ensure_ascii=False, indent=2))
    elif args.command == "query":
        print(json.dumps(index.query(args.query), ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
