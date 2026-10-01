---
name: rtk-knowledge-search
description: Find authoritative RTK Cloud documentation, contracts, environment DNS rules, service design, and implementation evidence. Use when locating a specification, identifying the source of a rule, or resolving current versus draft or historical documentation across the workspace.
---

# RTK Knowledge Search

Find the applicable source in the current RTK Cloud checkout and answer with
verified file references. Use the local RAG index to locate candidate passages.

## Select the source

Locate the workspace containing `docs/README.md`, `repos/`, and `tools/local-rag/`.
When working inside a service checkout, use its containing workspace if available;
retain the user's selected checkout. Paths in this workflow are workspace-relative.

Read `<workspace>/docs/README.md`, especially **Start Here** and **Repository
Documentation**, for the maintained topic-to-source mapping. Consult
`<workspace>/docs/documentation-governance.md` when authority, status, or ownership
is unclear. Resolve these paths against the selected workspace, even if the skill
is symlinked or installed elsewhere. Maintain topic routing in that index rather
than copying the document catalog into this skill.

Distinguish the question's scope before searching:

- Environment DNS/hostname rules belong to workspace policy. HTTP paths, shared
  payloads, and wire behavior belong to canonical contracts.
- Service implementation belongs to the owning repository; compare it with the
  applicable contract when the question concerns shared behavior.
- Current deployment claims require the selected environment and dated deployment
  evidence. A resolved plan describes intent. An active or normative design may
  still describe a target awaiting implementation or deployment.

## Find the relevant passage

Start with the routed entry point, `rg --files`, and focused `rg -n` searches in
the applicable documentation or owning source directory. Preserve exact symbols
and config keys; supplement Chinese questions with English component/feature terms.

Use RAG when terminology is uncertain or evidence spans repositories. Read
`<workspace>/tools/local-rag/README.md` before using its commands. If the existing
`.rag/rag.db` is present, run from `tools/local-rag/`:

```sh
RTK_RAG_ENABLE_EMBEDDINGS=0 RTK_RAG_ENABLE_ANSWERS=0 \
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local \
go run ./cmd/rag --workspace ../.. --db ../../.rag/rag.db \
  query 'DNS environment naming'
```

Replace the query with focused terms for the selected topic. These flags disable
external model calls and Go downloads. Use `matched_chunks` and `citations` to
identify files and headings, then read the relevant current source passages.

The current CLI has `query`, `status`, and `index`; it has no `search` subcommand
or CLI filters. If the database, local dependencies, or toolchain are unavailable,
continue with direct file search. Querying can initialize SQLite schema/WAL state;
use direct file search for a strict no-filesystem-writes request.

## Confirm authority and freshness

- Read each candidate's current header, scope, replacement links, and relevant
  surrounding section. Apply `Classification`, `Status`, `Owner`, and `Applies to`
  when present, along with the documentation index's classification. Missing
  metadata stays unknown.
- Select authority within the question's scope. Indexed ranking and path-based
  classification do not override the source document's declared status or scope.
  Service summaries refer back to `repos/rtk_cloud_contracts_doc` for shared contracts.
- Re-find the cited heading or text in the current file and obtain current line
  numbers. Old indexed line ranges, commit IDs, or snippets may be stale. Index
  counts, populated embeddings, and repository status timestamps do not establish
  that a particular document matches the current checkout.
- When the index misses a new document or disagrees with the file, use the current
  file and disclose any material uncertainty. The LIKE fallback limits candidates
  before reranking; an empty or weak result does not establish absent documentation.
- Keep ordinary retrieval scoped to documentation and relevant source/config.
  Skip indexed secrets, keys, raw runtime state, and generated dumps. Inspect only
  the needed sanitized deployment evidence for an actual-state question.

Ordinary lookup uses the existing checkout and index. Git updates, index rebuilds,
embedding calls, and server startup require the corresponding requested task.
Continue finding sources directly when a refresh would help but was not requested.

## Index scan timing

The current RAG scans files when `index --changed` or `index --full` is run. Both
scan the eligible file set and hash-check content; unchanged files with embeddings
are skipped. A server startup also indexes before listening by default, unless
`--skip-initial-index` is used. Ordinary queries do not scan the document tree,
and this skill does not install a background schedule.

For requested index maintenance, batch an incremental update after documentation
edits, repository updates, renames, or deletions are complete. Follow the current
Go commands and credential source in the local RAG README. Missing embeddings can
also be regenerated; changed indexing is not limited to Git's changed-file list.
If maintenance is outside the current task, finish retrieval from current files
and identify the pending refresh when it affects the answer.

## Answer and maintain

Give the direct answer with clickable source paths and verified line references.
State the applicable environment/version/status when it affects the conclusion.
Distinguish documented requirements, observed implementation, dated deployment
evidence, and inference. Explain unresolved disagreements using their source scopes.
Keep the response proportionate to the user's question.

For an authorized documentation edit, update the owning source and its existing
index entry or links as needed. Report a missing source, stale reference, or RAG
metadata gap precisely; implement retrieval-tool changes when they are requested.
