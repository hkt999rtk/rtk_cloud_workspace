---
name: rtk-knowledge-search
description: Find authoritative RTK Cloud documentation, contracts, environment DNS rules, service design, and implementation evidence. Use when locating a specification, identifying the source of a rule, or resolving current versus draft or historical documentation across the workspace.
---

# RTK Knowledge Search

Find the applicable source in the current RTK Cloud checkout and answer with
verified file references. Use semantic retrieval to find candidate passages;
always confirm the answer against the current source file.

## Select the source

Locate the workspace containing `docs/README.md`, `repos/`, and
`tools/local-rag/`. When working inside a service checkout, use its containing
workspace if available; retain the user's selected checkout. Paths in this
workflow are workspace-relative.

Read `<workspace>/docs/README.md`, especially **Start Here** and **Repository
Documentation**, for the maintained topic-to-source mapping. Consult
`<workspace>/docs/documentation-governance.md` when authority, status, or
ownership is unclear. Resolve these paths against the selected workspace, even
if this skill is symlinked or installed elsewhere. Maintain topic routing in
that index rather than copying the document catalog into this skill.

Distinguish the question's scope before searching:

- Environment DNS and hostname rules belong to workspace policy. HTTP paths,
  shared payloads, and wire behavior belong to canonical contracts.
- Service implementation belongs to the owning repository; compare it with the
  applicable contract when the question concerns shared behavior.
- Current deployment claims require the selected environment and dated
  deployment evidence. A resolved plan describes intent. An active or
  normative design may still describe a target awaiting implementation or
  deployment.

## Find the relevant passage

Use `mcp-local-rag`'s `query_documents` for natural-language semantic and
keyword retrieval when the tool is available. Start with the user's question
as a complete query; include the named service, behavior, and relevant source
or destination when that context is clear. Do not reduce conceptual questions
to a list of literal keywords. For exact identifiers, routes, errors, and
configuration keys, include the exact string in the query or follow with a
focused exact-name search.

Use the workspace topic map to set a narrow RAG `scope` when the relevant
repository or document directory is known. Omit the scope when the topic is
unclear or spans repositories. Review topical relevance as well as the result
score. If the results are weak, try one or two differently phrased semantic
queries; do not treat a weak or empty result as proof that documentation is
absent.

If `mcp-local-rag` is unavailable, use the workspace Go RAG only as a candidate
finder, then search the routed source files directly. From
`<workspace>/tools/local-rag/`, the local command is:

```sh
RTK_RAG_ENABLE_EMBEDDINGS=0 RTK_RAG_ENABLE_ANSWERS=0 \
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local \
go run ./cmd/rag --workspace ../.. --db ../../.rag/rag.db \
  query 'Explain how a device receives its credentials and which service owns them'
```

This offline fallback is lexical only. The Go RAG's current query path first
limits candidates with FTS/LIKE and only then reranks them with embeddings, so
embeddings cannot recover a relevant chunk that was never selected. Its query
and answer APIs can also send indexed content to the configured OpenAI endpoint;
do not enable those calls for ordinary lookup unless the task authorizes that
external processing. The command disables external model calls and Go downloads.
If semantic MCP retrieval is unavailable, combine the candidate results with
direct source search and do not claim the index search was comprehensive.

The local CLI has `query`, `status`, and `index`; it has no `search` subcommand
or CLI filters. Querying can initialize SQLite schema/WAL state; use direct file
search for a strict no-filesystem-writes request.

Use `rg --files` and focused `rg -n` searches in the applicable documentation
or owning source directory to locate the current file and exact symbol. Preserve
exact symbols and config keys. Supplement Chinese questions with English
component or feature terms only when useful for locating implementation names.

## Confirm authority and freshness

- Read each candidate's current header, scope, replacement links, and relevant
  surrounding section. Apply `Classification`, `Status`, `Owner`, and `Applies
  to` when present, along with the documentation index's classification.
  Missing metadata stays unknown.
- Select authority within the question's scope. Retrieval ranking and path-based
  classification do not override the source document's declared status or
  scope. Service summaries refer back to `repos/rtk_cloud_contracts_doc` for
  shared contracts.
- Re-find the cited heading or text in the current file and obtain current line
  numbers. Indexed line ranges, commit IDs, or snippets can be stale. Index
  counts, populated embeddings, and repository status timestamps do not
  establish that a particular document matches the current checkout.
- When the index misses a new document or disagrees with the file, use the
  current file and disclose any material uncertainty. Continue with direct
  source search when retrieval is weak.
- Keep ordinary retrieval scoped to documentation and relevant source/config.
  Skip indexed secrets, keys, raw runtime state, and generated dumps. Inspect
  only the needed sanitized deployment evidence for an actual-state question.

Ordinary lookup uses the existing checkout and index. Git updates, index
rebuilds, embedding calls, and server startup require the corresponding
requested task. Do not refresh the index as a side effect of an ordinary
question.

## Index scan timing

The current Go RAG scans files when `index --changed` or `index --full` is run.
Both scan the eligible file set and hash-check content; unchanged files with
embeddings are skipped. A server startup also indexes before listening by
default, unless `--skip-initial-index` is used. Ordinary queries do not scan the
document tree, and this skill does not install a background schedule.

For requested index maintenance, batch an incremental update after documentation
edits, repository updates, renames, or deletions are complete. Follow the
current RAG README for the exact command and credential source. Missing
embeddings can also be regenerated; changed indexing is not limited to Git's
changed-file list. If maintenance is outside the current task, finish retrieval
from current files and identify a stale index when it affects the answer.

## Answer and maintain

Give the direct answer with clickable source paths and verified line references.
State the applicable environment, version, or status when it affects the
conclusion. Distinguish documented requirements, observed implementation, dated
deployment evidence, and inference. Explain unresolved disagreements using
their source scopes. Keep the response proportionate to the user's question.

For an authorized documentation edit, update the owning source and its existing
index entry or links as needed. Report a missing source, stale reference, or RAG
coverage or metadata gap precisely; implement retrieval-tool changes when they
are requested.
