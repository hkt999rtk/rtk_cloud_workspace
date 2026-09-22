# Local database catalog evidence

All databases used for these captures were isolated local fixtures. No file here
contains deployed data, private keys, passwords, or a database backup.

| Catalog | Predecessor source | Before | Current | Initializer |
| --- | --- | ---: | ---: | --- |
| Account Manager | `e82d50e` | 92 | 88 | `internal/database.Migrate` |
| Video Cloud, including PKI | `5041964` | 71 | 64 | `postgres.EnsureSchema` and `pki.Store.Migrate` |
| Admin | `8063ab2` | 16 | 12 | `store.Migrate` |

`schema-before.json` is the original source-derived ER baseline, including the
unchanged Billing and Frontend collections. It is deliberately not overwritten.
Its Admin text-primary-key nullability was incorrect: SQLite permits NULL for a
TEXT PRIMARY KEY unless NOT NULL is explicit. The independent old Admin catalog
revealed this in eleven tables. The corrected parser matches the actual old and
new catalogs; this is a documentation correction, not a new database alteration.

To reproduce PostgreSQL catalog captures, initialize a new disposable database
using the listed source revision and initializer, then execute `catalog.sql`.
Parse its single JSON result and write sorted, indented JSON. For Admin, initialize
a new SQLite file and read sqlite_master plus PRAGMA table_info, index_list,
index_info and foreign_key_list. Count a single INTEGER PRIMARY KEY as non-null
because it aliases rowid; do not apply that rule to text keys.

Compare current source with each current capture using:

```sh
python3 scripts/check_database_er_catalog.py --database 'Account Manager' --catalog tests/fixtures/database-simplification/account-catalog-after.json
python3 scripts/check_database_er_catalog.py --database 'Video Cloud' --catalog tests/fixtures/database-simplification/video-catalog-after.json
python3 scripts/check_database_er_catalog.py --database 'Cloud Admin' --catalog tests/fixtures/database-simplification/admin-catalog-after.json
```

The populated, repeatable upgrade/rollback fixtures live beside service tests:

- Account Manager: `internal/database/simplification_integration_test.go`, using
  ordered historical migrations through 082 before the new 083/084 migrations.
- Video Cloud: `internal/postgres/testdata/ota_schema_v0.sql` and
  `internal/postgres/ota_consistency_integration_test.go`.
- Admin: `internal/store/schema_maintenance_test.go`, using the unchanged
  historical migrations through version 10 before offline version 11.

Separately, the complete predecessor Video Cloud initializer was exercised with
three populated OTA rows. A PostgreSQL custom-format backup was taken before
maintenance; check/apply/verify succeeded, including authoritative canceled state
and revision 1. Restoring the backup and restarting the predecessor initializer
reproduced all 71 catalog tables exactly and preserved the original JSON/column
state discrepancy. This is a synthetic local rollback drill, not a qualified
shared-dev platform restore.
