# Database Schema to ER Documentation

Status: active

Owner: rtk_cloud_workspace

Last reviewed: 2026-09-29

## Sources and output

The service's schema initializer and migrations define the physical database. PostgreSQL `COMMENT ON TABLE` and `COMMENT ON COLUMN` describe business purpose and key fields. Each database also has a versioned `schema_metadata` table for groups, usage scenarios, critical column classifications, and evidenced cross-service references. SQLite stores descriptions in `schema_metadata` because its schema has no native object comments.

The workflow is:

1. Run Account Manager and Billing migrations, Video Cloud schema initialization and PKI migration, Cloud Admin migrations, and all three Frontend SQLite initializers against isolated databases.
2. Read the PostgreSQL and SQLite catalogs in read-only transactions. The extractor records tables, columns, defaults, keys, indexes, checks, triggers, views, migration versions, comments, and metadata. It validates that metadata points to existing objects.
3. Save the normalized [schema snapshot](database-schema.json). Render the [ER atlas](database-er-atlas.html) and [index](database-er-diagrams.md) from that snapshot. Declared foreign keys are Crow's-foot relationships; cross-service metadata appears as logical links.
4. In CI, recreate the databases and compare their structure, metadata, atlas, and index with the published files.

The checked-in snapshot and ER files are generated design references. Edit the owning service's schema and metadata first. New tables require a purpose, usage scenario, and group. PK/FK fields and classified state, money, count, and byte fields require descriptions. The check reports metadata that points to removed tables or columns.

## Generate and check

From the workspace root, install the Python catalog dependency and use the workspace command:

```sh
python3 -m pip install -r scripts/requirements-database-schema.txt
go run ./scripts/go/rtk-cloud -- schema generate
go run ./scripts/go/rtk-cloud -- schema generate --check
```

Generation uses Docker with `postgres:16-alpine`, Go, and temporary SQLite files. It invokes production schema code through each service's `cmd/schema-init`; it does not start the application. The command removes its temporary PostgreSQL container. `--check` writes candidate files only to a temporary directory and returns an error if the committed snapshot or documents differ. The old SQL-text parser is retained solely for historical schema-simplification tests.

## Capture an environment

`schema snapshot` reads existing databases and does not run schema initialization. Provide PostgreSQL connection URLs through environment variables and paths to SQLite databases accessible from the command host. For a remote SQLite database, run the command where its file is mounted, or use a consistent SQLite online backup from the owning service; copying only a live `.sqlite` file can omit WAL transactions.

The PostgreSQL reader captures the active schema. For a non-`public` deployment, include `search_path=<schema>` in its connection URL so table and metadata identities match the deployed schema.

```sh
go run ./scripts/go/rtk-cloud -- schema snapshot \
  --environment staging \
  --postgres 'Account Manager:primary:ER_ACCOUNT_DSN' \
  --postgres 'Billing:primary:ER_BILLING_DSN' \
  --postgres 'Video Cloud:primary:ER_VIDEO_DSN' \
  --sqlite 'Cloud Admin:primary:/path/to/admin.sqlite' \
  --sqlite 'Cloud Frontend:analytics:/path/to/analytics.sqlite' \
  --sqlite 'Cloud Frontend:search:/path/to/search.sqlite' \
  --sqlite 'Cloud Frontend:leads:/path/to/leads.sqlite' \
  --revision 'Account Manager=<deployed-commit>' \
  --revision 'Billing=<deployed-commit>' \
  --revision 'Video Cloud=<deployed-commit>' \
  --revision 'Cloud Admin=<deployed-commit>' \
  --revision 'Cloud Frontend=<deployed-commit>'
```

The output is written under `cloud_env/staging/runtime/artifacts/database/<timestamp>/schema.json`; this runtime directory is ignored by Git. The snapshot includes the environment, capture time, supplied source revisions, applied migration versions, and a hash of structure and metadata. Missing metadata in an older environment is visible in the snapshot; `--strict` additionally requires full documentation coverage.

Compare the environment with the snapshot generated from **the same deployed service revisions**:

```sh
go run ./scripts/go/rtk-cloud -- schema diff \
  --baseline /path/to/deployed-version-schema.json \
  --actual cloud_env/staging/runtime/artifacts/database/<timestamp>/schema.json \
  --output cloud_env/staging/runtime/artifacts/database/<timestamp>/diff.json \
  --html-output cloud_env/staging/runtime/artifacts/database/<timestamp>/diff.html
```

The schema CLI exits with status 0 for an equal schema, 1 for differences, and 2 for an inspection or input error. When using `go run`, Go itself returns 1 for any nonzero child exit, so inspect the printed status or invoke a built `rtk-cloud` binary when exit-code distinction matters. Its `version_match` field indicates whether revision evidence supports a drift conclusion. Long-lived acceptance snapshots should be archived with the existing release evidence; the local runtime copy remains an operator artifact.
