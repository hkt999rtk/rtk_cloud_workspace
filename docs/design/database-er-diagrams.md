# Database Entity–Relationship Model

Open the [Crow’s-foot ER atlas](database-er-atlas.html).

## Notation and navigation

The single-file atlas opens with an **Overall** view of five schema owners and only evidence-backed cross-service mappings. Select a service to reach topical groups of at most eight entities. Every group explains the data domain and operation context; every entity has its own data-purpose and usage-scenario description. Group membership is editorial, **not** an extra FK or a claim that every member is directly connected. All 259 entities, including isolated tables, occur in exactly one group. Descriptions and group assignments come from the databases' `COMMENT` and `schema_metadata` entries; regeneration fails when a new table lacks an explanation.

Search indexes all 259 entities and 256 FK relationships. Entity nodes open complete columns, descriptions, and outgoing/incoming FK lists. Each FK detail explicitly links both the referencing (child) and referenced (parent) entity; the parent catalog links back to every child that references it. Relationship lines, R labels, and group links open a stable relationship anchor. Direct `file://...#entity-...` or `#relation-...` links expand the containing details; browser Back/Forward follows those anchors. Return links lead to Overall.

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
| Account Manager | 93 | 134 | 83 |
| Billing | 65 | 77 | 51 |
| Video Cloud | 79 | 42 | 33 |
| Cloud Admin | 14 | 2 | 2 |
| Cloud Frontend | 8 | 1 | 1 |

These are service schema collections, not a claim that there are exactly five physical database instances. Video Cloud includes the PKI registry; Frontend includes its SQLite analytics/search/leads schemas. Runtime stores without relational DDL, external provider storage, and Redis keyspaces have no SQL ER model here. Schema migration metadata tables are included where defined by these sources.

## Sources and reproducibility

The generator applies each service's production schema initializer to isolated PostgreSQL or SQLite databases, then reads their catalogs and metadata into [the schema snapshot](database-schema.json). Types, keys, constraints, defaults, indexes, views, and triggers come from the initialized catalogs. Table purpose and column descriptions come from PostgreSQL `COMMENT` or SQLite metadata; group and logical-reference records come from `schema_metadata`. Table attributes omitted from a drawing are available in its complete catalog. The descriptions express intended responsibility and usage; they are not proof of activity in any deployed database.

The revision below is each checkout's base commit. The generated model also
includes any uncommitted changes to the listed source files in the current
workspace; it is not necessarily a pure snapshot of those commits.

| Source checkout | Base commit |
| --- | --- |
| Account Manager | `faf424e57c73` |
| Billing | `a16e12a8e445` |
| Video Cloud | `ae38cddfdc49` |
| Cloud Admin | `4035e920404d` |
| Cloud Frontend | `475b9fe80d6c` |

Refresh from the workspace root with `go run ./scripts/go/rtk-cloud -- schema generate`. Run `go run ./scripts/go/rtk-cloud -- schema generate --check` in CI or before review. Docker, Go, Python, and `scripts/requirements-database-schema.txt` are required. See [the design and usage guide](database-er-process.md) for the complete schema-to-ER process and environment comparison commands.

## Interpretation boundary

This is a snapshot of newly initialized databases from the current workspace sources. It does not assert that any deployed environment has upgraded. Environment-specific snapshots must be captured separately and compared with the expected snapshot for their deployed version. The older SQL parser remains available for historical schema-simplification fixtures; the published ER model uses the initialized catalogs.

Dashed orange Overall links are **logical references, not enforced database foreign keys**. They use no Crow’s-foot cardinality because the cited source does not establish one. Each link names both endpoint columns and links to its checked-in code or contract evidence. Currently evidenced mappings are Account Manager `organizations.id` to Billing `commercial_accounts.organization_id`, Account Manager `organizations.id` to Video Cloud `devices.org_id`, and Account Manager `devices.id` to Video Cloud `devices.account_device_id`. The Account Manager device UUID and Video Cloud `devices.id` are deliberately not equated. No Cloud Admin or Cloud Frontend cross-service link is inferred from similar column names alone. The only Crow’s-foot lines represent declared FKs in the extracted DDL.

Tables with no declared FK remain explicitly listed and fully documented in the catalog. Attributes displayed on a relationship model prioritize its PK and the relationship’s actual FK columns; other relationships of repeated entities are linked in the catalog. The HTML embeds its styles and navigation script, requires no external drawing library, and can be opened directly via `file://`.
