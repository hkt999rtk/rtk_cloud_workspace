# Database Entity–Relationship Model

Open the [Crow’s-foot ER atlas](database-er-atlas.html).

## Notation and navigation

The single-file atlas opens with an **Overall** view of five schema owners and only evidence-backed cross-service mappings. Select a service to reach reviewed topical groups of at most eight entities. Every group explains the data domain and operation context; every entity has its own data-purpose and usage-scenario description. Group membership is editorial, **not** an extra FK or a claim that every member is directly connected. All 224 entities, including isolated tables, occur in exactly one group. The description inventory is maintained in `scripts/database_er_narratives_en.py`; regeneration fails when a new DDL table lacks a group or explanation.

Search indexes all 224 entities and 235 FK relationships. Entity nodes open complete columns, descriptions, and outgoing/incoming FK lists. Each FK detail explicitly links both the referencing (child) and referenced (parent) entity; the parent catalog links back to every child that references it. Relationship lines, R labels, and group links open a stable relationship anchor. Direct `file://...#entity-...` or `#relation-...` links expand the containing details; browser Back/Forward follows those anchors. Return links lead to Overall.

Each Crow’s-foot detail diagram places a referencing entity beside the entities it references. Both endpoints of every extracted foreign key appear together. Related entities may recur in several diagrams; the complete entity catalog links to every occurrence. Diagrams have at most four entity boxes and three relationships. Native expandable sections keep the document navigable; fixed minimum drawing width preserves readable text on smaller screens.

- Circle: minimum zero. Bar: one. Crow’s foot: maximum many.
- `1`: exactly one; `0..1`: optional one; `0..*`: zero or more.
- `PK` and `FK` denote declared primary and foreign keys, including compound keys. An `_id` suffix alone does not make an attribute an FK.
- Parent optionality follows FK nullability; a primary/unique key contained in the child FK implies at most one child. Partial unique indexes do not imply unconditional one-to-one cardinality.
- No parent-to-child mandatory minimum is inferred from a foreign key. Additional trigger/application rules may impose stricter business requirements.
- Self-references repeat the same table in distinct roles. Association entities preserve the two one-to-many relationships implementing many-to-many associations.

## Coverage

| Schema owner | Tables | Declared relationships | Models |
| --- | ---: | ---: | ---: |
| Account Manager | 88 | 124 | 77 |
| Billing | 55 | 71 | 47 |
| Video Cloud | 64 | 37 | 28 |
| Cloud Admin | 12 | 2 | 2 |
| Cloud Frontend | 5 | 1 | 1 |

These are service schema collections, not a claim that there are exactly five physical database instances. Video Cloud includes the PKI registry; Frontend includes its SQLite analytics/search/leads schemas. Runtime stores without relational DDL, external provider storage, and Redis keyspaces have no SQL ER model here. Schema migration metadata tables are included where defined by these sources.

## Sources and reproducibility

The generator reads Account Manager and Billing migrations in filename order, Video Cloud runtime PostgreSQL schema and PKI DDL, Cloud Admin SQLite migrations, and Frontend SQLite repository initializers. It applies literal migration table/index deletion, column addition/removal/rename, table rename, nullability changes, FK additions/removals, and unconditional unique indexes in source order. Test reset helpers are excluded; unsupported persistent table/index DDL fails explicitly. Table attributes omitted from a drawing are available in its database’s full entity catalog. Each entity explanation links to its checked-in schema source. The descriptions express the schema's data responsibility and intended operation context, informed by representative runtime call sites; they are not proof of activity in any deployed database.

The revision below is each checkout's base commit. The generated model also
includes any uncommitted changes to the listed source files in the current
workspace; it is not necessarily a pure snapshot of those commits.

| Source checkout | Base commit |
| --- | --- |
| Account Manager | `cf8d10b1012f` |
| Billing | `026016a9e797` |
| Video Cloud | `cd3f50f395f4` |
| Cloud Admin | `1bbeb8e18494` |
| Cloud Frontend | `6a7f3fb15cbc` |

Refresh from the workspace root with `python3 scripts/generate_database_er_atlas.py`. The process reads source files only; it never opens a deployed database. Exact source paths appear in the entity catalog.

## Interpretation boundary

This is a static model of literal checked-in DDL, not a migration execution engine or live database introspection. Conditional historical repair branches and dynamically constructed SQL require review if their behavior changes. Account Manager, Video Cloud, and Admin extraction is also compared with independently initialized PostgreSQL/SQLite catalogs using `scripts/check_database_er_catalog.py`; the stored local catalog fixtures and upgrade tests are under `tests/fixtures/database-simplification` and the service repositories. This does not assert that any deployed environment has already upgraded.

Dashed orange Overall links are **logical references, not enforced database foreign keys**. They use no Crow’s-foot cardinality because the cited source does not establish one. Each link names both endpoint columns and links to its checked-in code or contract evidence. Currently evidenced mappings are Account Manager `organizations.id` to Billing `commercial_accounts.organization_id`, Account Manager `organizations.id` to Video Cloud `devices.org_id`, and Account Manager `devices.id` to Video Cloud `devices.account_device_id`. The Account Manager device UUID and Video Cloud `devices.id` are deliberately not equated. No Cloud Admin or Cloud Frontend cross-service link is inferred from similar column names alone. The only Crow’s-foot lines represent declared FKs in the extracted DDL.

Tables with no declared FK remain explicitly listed and fully documented in the catalog. Attributes displayed on a relationship model prioritize its PK and the relationship’s actual FK columns; other relationships of repeated entities are linked in the catalog. The HTML embeds its styles and navigation script, requires no external drawing library, and can be opened directly via `file://`.
