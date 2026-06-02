# RTK Local RAG

Workspace-local RAG service for querying `rtk_cloud_workspace` and its submodules with natural language.

## Design

- Implemented in Go. The Python prototype has been removed from the runtime path.
- Uses SQLite for document metadata, chunk storage, and FTS5 retrieval when available.
- Falls back to SQLite `LIKE` retrieval when the local SQLite build does not include FTS5.
- Uses the OpenAI API for embeddings and answer generation when `OPENAI_API_KEY` is available.
- Loads `OPENAI_API_KEY` from `~/.env` automatically without writing secrets into the repository.
- Falls back to local hybrid search and extractive answers when the OpenAI API is unavailable.
- Stores path, line range, repo name, submodule path, commit, dirty state, source layer, and document classification with every chunk.

## Commands

From this directory:

```sh
go run ./cmd/rag --workspace ../.. index --full
go run ./cmd/rag --workspace ../.. status
go run ./cmd/rag --workspace ../.. query "device 怎麼取得認證"
go run ./cmd/rag-server --workspace ../.. --port 8765
```

Open `http://127.0.0.1:8765` for the web UI.

To build reusable binaries:

```sh
go build -o bin/rag ./cmd/rag
go build -o bin/rag-server ./cmd/rag-server
```

The server performs a full index before listening by default. To open the UI immediately against an existing `.rag/rag.db`:

```sh
go run ./cmd/rag-server --workspace ../.. --port 8765 --skip-initial-index
```

## OpenAI Models

Defaults:

- Embeddings: `text-embedding-3-small`
- Answers: `gpt-4.1-mini`

Override with environment variables:

```sh
OPENAI_RAG_EMBEDDING_MODEL=text-embedding-3-large \
OPENAI_RAG_ANSWER_MODEL=gpt-4.1-mini \
go run ./cmd/rag --workspace ../.. index --full
```

Disable OpenAI calls and use local SQLite FTS/extractive answers only:

```sh
RTK_RAG_ENABLE_EMBEDDINGS=0 RTK_RAG_ENABLE_ANSWERS=0 \
go run ./cmd/rag --workspace ../.. query "device 怎麼取得認證"
```

## Index Scope

Included:

- Markdown docs, README files, ADRs.
- OpenAPI/YAML/TOML/JSON config files.
- Selected Go/JS entrypoints, route files, app/config code.

Excluded:

- `.git`, `.rag`, build outputs, caches, `node_modules`, vendored directories.
- fixtures and large media-like/generated files.

## API

- `POST /api/query`
- `GET /api/status`
- `POST /api/index/full`
- `POST /api/index/changed`

Example query:

```sh
curl -s http://127.0.0.1:8765/api/query \
  -H 'content-type: application/json' \
  -d '{"query":"video server 的組成有哪些"}'
```

## Tests

```sh
go test ./...
```
