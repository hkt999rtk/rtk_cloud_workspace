#!/usr/bin/env python3
"""Build a source-derived, static Crow's-foot ER model. No live DB access.

The parser intentionally handles the repository's literal DDL subset, including
ALTERs and compound keys. This is documentation extraction, not a SQL executor.
"""
from __future__ import annotations

import html
import re
import subprocess
import textwrap
from collections import defaultdict
from pathlib import Path

from database_er_narratives_en import ENTITY_NOTES, GROUPS

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / 'docs/design/database-er-atlas.html'
INDEX = ROOT / 'docs/design/database-er-diagrams.md'
SOURCES = {
    'Account Manager': ['repos/rtk_account_manager/internal/database/database.go', 'repos/rtk_account_manager/migrations'],
    'Billing': ['repos/rtk_billing/internal/database/database.go', 'repos/rtk_billing/migrations'],
    'Video Cloud': ['repos/rtk_video_cloud/internal/postgres/postgres.go', 'repos/rtk_video_cloud/internal/postgres/brand_webhook_schema.go', 'repos/rtk_video_cloud/internal/postgres/device_presence_outbox_schema.go', 'repos/rtk_video_cloud/internal/postgres/factory_cloud_handoff_schema.go', 'repos/rtk_video_cloud/internal/postgres/resource_cloud_handoff_schema.go', 'repos/rtk_video_cloud/internal/postgres/resource_cloud_deletion_schema.go', 'repos/rtk_video_cloud/internal/postgres/usage_evidence_schema.go', 'repos/rtk_video_cloud/internal/pki/schema.sql'],
    'Cloud Admin': ['repos/rtk_cloud_admin/internal/store/store.go'],
    'Cloud Frontend': ['repos/rtk_cloud_frontend/internal/analytics/repository.go', 'repos/rtk_cloud_frontend/internal/search/repository.go', 'repos/rtk_cloud_frontend/internal/leads/repository.go'],
}
ESC = html.escape
IDENT = r'[a-z_][a-z0-9_]*'
CREATE = re.compile(rf'CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?({IDENT})\s*\(', re.I)
ALTER = re.compile(rf'ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?({IDENT})\s+([^;]+)', re.I)
UNIQUE = re.compile(rf'CREATE\s+UNIQUE\s+INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?({IDENT})\s+ON\s+({IDENT})\s*\(([^;]+)', re.I)
DROP_TABLE = re.compile(rf'DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?({IDENT})(?:\s+(RESTRICT|CASCADE))?\s*;', re.I)
DROP_INDEX = re.compile(rf'DROP\s+INDEX\s+(?:IF\s+EXISTS\s+)?({IDENT})\s*;', re.I)
REF = re.compile(rf'REFERENCES\s+({IDENT})\s*\(([^)]+)\)', re.I)


def split_sql(text, delimiter=','):
    """Split outside parentheses and SQL string literals."""
    parts, start, depth, quoted = [], 0, 0, False
    i = 0
    while i < len(text):
        c = text[i]
        if c == "'":
            if quoted and i + 1 < len(text) and text[i + 1] == "'":
                i += 2
                continue
            quoted = not quoted
        elif not quoted:
            if c == '(': depth += 1
            elif c == ')': depth -= 1
            elif c == delimiter and depth == 0:
                parts.append(text[start:i].strip())
                start = i + 1
        i += 1
    parts.append(text[start:].strip())
    return [p for p in parts if p]


def body_at(text, opening):
    depth, quoted = 0, False
    for i in range(opening, len(text)):
        c = text[i]
        if c == "'": quoted = not quoted
        if not quoted:
            if c == '(': depth += 1
            if c == ')':
                depth -= 1
                if depth == 0: return text[opening + 1:i]
    raise ValueError('Unbalanced CREATE TABLE')


def keys(text):
    return tuple(c.strip().lower() for c in text.split(','))


def source_files(paths):
    for name in paths:
        path = ROOT / name
        yield from sorted(path.glob('*.sql')) if path.is_dir() else [path]


def add_definition(table, definition, source):
    definition = re.sub(r'\s+', ' ', definition).strip()
    named = re.match(rf'CONSTRAINT\s+({IDENT})\s+', definition, re.I)
    constraint = named.group(1).lower() if named else None
    if named: definition = definition[named.end():]
    composite = re.match(r'(PRIMARY KEY|UNIQUE|FOREIGN KEY)\s*\(([^)]+)\)', definition, re.I)
    if composite:
        kind = composite.group(1).upper()
        cols = keys(composite.group(2))
        if kind in ('PRIMARY KEY', 'UNIQUE'):
            cname = constraint or (table['name'] + ('_pkey' if kind == 'PRIMARY KEY' else '_' + '_'.join(cols) + '_key'))
            table['unique'][cname] = cols
            if kind == 'PRIMARY KEY': table['pk'] = cols
            return
    else:
        if re.match(r'(CHECK|EXCLUDE)\b', definition, re.I): return
        match = re.match(rf'({IDENT})\s+(.+)', definition, re.I)
        if not match: return
        column, rest = match.groups()
        column = column.lower()
        dtype = re.split(r'\s+(?:NOT\s+NULL|NULL|PRIMARY|UNIQUE|REFERENCES|DEFAULT|CHECK|COLLATE|CONSTRAINT|GENERATED)\b', rest, maxsplit=1, flags=re.I)[0]
        table['columns'][column] = {'type': dtype.lower(), 'nn': bool(re.search(r'NOT NULL', rest, re.I) or (re.search(r'PRIMARY KEY', rest, re.I) and (table.get('_dialect', 'postgres') != 'sqlite' or dtype.lower() == 'integer')))}
        cols = (column,)
        if re.search(r'PRIMARY KEY', rest, re.I):
            table['pk'] = cols
            table['unique'][table['name'] + '_pkey'] = cols
        elif re.search(r'\bUNIQUE\b', rest, re.I):
            table['unique'][table['name'] + '_' + column + '_key'] = cols
    reference = REF.search(definition)
    if reference:
        target, target_cols = reference.groups()
        cname = constraint or table['name'] + '_' + '_'.join(cols) + '_fkey'
        table['fks'][cname] = {'child': table['name'], 'cols': cols, 'parent': target.lower(), 'target': keys(target_cols), 'source': source, 'constraint': cname}


def rename_column(tables, table, old, new):
    if old not in table['columns']:
        if new in table['columns']: return  # historical conditional repair
        raise ValueError(f"Unknown rename column: {table['name']}.{old}")
    table['columns'] = {new if c == old else c: v for c, v in table['columns'].items()}
    replace = lambda cols: tuple(new if c == old else c for c in cols)
    table['pk'] = replace(table['pk'])
    table['unique'] = {k: replace(v) for k, v in table['unique'].items()}
    for t in tables.values():
        for fk in t['fks'].values():
            if fk['child'] == table['name']: fk['cols'] = replace(fk['cols'])
            if fk['parent'] == table['name']: fk['target'] = replace(fk['target'])


def apply_alter(table, text, source, tables=None):
    tables = tables if tables is not None else {table['name']: table}
    for action in split_sql(text):
        action = re.sub(r'\s+', ' ', action).strip()
        add = re.fullmatch(r'ADD\s+(?:COLUMN\s+)?(?:IF NOT EXISTS\s+)?(.+)', action, re.I)
        if add:
            definition = add.group(1)
            col = definition.split()[0].lower()
            if 'IF NOT EXISTS' not in action.upper() or col not in table['columns']:
                add_definition(table, definition, source)
            continue
        drop = re.fullmatch(rf'DROP CONSTRAINT\s+(?:IF EXISTS\s+)?({IDENT})(?: RESTRICT)?', action, re.I)
        if drop:
            name = drop.group(1).lower()
            if name == table['name'] + '_pkey': table['pk'] = ()
            table['fks'].pop(name, None)
            table['unique'].pop(name, None)
            continue
        drop = re.fullmatch(rf'DROP COLUMN\s+(?:IF EXISTS\s+)?({IDENT})(?: RESTRICT)?', action, re.I)
        if drop:
            col = drop.group(1).lower()
            table['columns'].pop(col, None)
            table['pk'] = tuple(c for c in table['pk'] if c != col)
            table['unique'] = {k: v for k, v in table['unique'].items() if col not in v}
            table['fks'] = {k: v for k, v in table['fks'].items() if col not in v['cols']}
            continue
        rename = re.fullmatch(rf'RENAME COLUMN ({IDENT}) TO ({IDENT})', action, re.I)
        if rename:
            rename_column(tables, table, *[x.lower() for x in rename.groups()])
            continue
        rename = re.fullmatch(rf'RENAME TO ({IDENT})', action, re.I)
        if rename:
            old, new = table['name'], rename.group(1).lower()
            if new in tables: raise ValueError(f'Duplicate table rename: {new}')
            tables[new] = tables.pop(old)
            table['name'] = new
            for t in tables.values():
                for fk in t['fks'].values():
                    if fk['child'] == old: fk['child'] = new
                    if fk['parent'] == old: fk['parent'] = new
            continue
        nullable = re.fullmatch(rf'ALTER COLUMN\s+({IDENT})\s+(SET|DROP) NOT NULL', action, re.I)
        if nullable:
            table['columns'][nullable.group(1).lower()]['nn'] = nullable.group(2).upper() == 'SET'
            continue
        dtype = re.fullmatch(rf'ALTER COLUMN ({IDENT}) (?:SET DATA )?TYPE (.+?)(?: USING .+)?', action, re.I)
        if dtype:
            table['columns'][dtype.group(1).lower()]['type'] = dtype.group(2).lower()
            continue
        # These operations do not change the ER attributes represented here.
        if re.fullmatch(rf'ALTER COLUMN {IDENT} (?:SET DEFAULT .+|DROP DEFAULT)', action, re.I): continue
        if re.fullmatch(rf'(?:ENABLE|DISABLE) TRIGGER {IDENT}|VALIDATE CONSTRAINT {IDENT}', action, re.I): continue
        raise ValueError(f'Unsupported ALTER TABLE in {source}: {action}')


def sql_blocks(file):
    raw = file.read_text()
    if file.suffix != '.go': return [raw]
    # Only the initializer owns runtime DDL; Reset is a test helper, not a migration.
    if file.as_posix().endswith('/internal/postgres/postgres.go'):
        raw = raw[raw.index('func EnsureSchema('):]
        raw = re.split(r'\nfunc ', raw, maxsplit=1)[0]
    return re.findall(r'`([^`]*)`', raw, re.S)


def parse_database(paths, *, dialect=None):
    if dialect is None:
        dialect = 'sqlite' if any('repos/rtk_cloud_admin/' in p or 'repos/rtk_cloud_frontend/' in p for p in paths) else 'postgres'
    tables = {}
    for file in source_files(paths):
        blocks = sql_blocks(file)
        for block in blocks:
            text = re.sub(r'--[^\n]*', '', block)
            events = [(m.start(), 'create', m) for m in CREATE.finditer(text)]
            events += [(m.start(), 'alter', m) for m in ALTER.finditer(text)]
            events += [(m.start(), 'unique', m) for m in UNIQUE.finditer(text)]
            events += [(m.start(), 'drop_table', m) for m in DROP_TABLE.finditer(text)]
            events += [(m.start(), 'drop_index', m) for m in DROP_INDEX.finditer(text)]
            source = file.relative_to(ROOT).as_posix()
            recognized = {position for position, _, _ in events}
            # A schema-changing statement outside our supported grammar must
            # fail extraction rather than disappear from the diagram silently.
            for ddl in re.finditer(r'\b(?:CREATE\s+(?:(?:TEMP|TEMPORARY|UNLOGGED)\s+)?TABLE|ALTER\s+TABLE|DROP\s+TABLE|CREATE\s+UNIQUE\s+INDEX|DROP\s+INDEX)\b', text, re.I):
                # Migration-local temporary tables are deliberately excluded
                # from the persistent business catalog.
                if re.match(r'CREATE\s+(?:TEMP|TEMPORARY)\s+TABLE', ddl.group(), re.I):
                    continue
                if ddl.start() not in recognized:
                    snippet = text[ddl.start():].split(';', 1)[0][:180]
                    raise ValueError(f'Unsupported DDL in {source}: {snippet}')
            for _, kind, match in sorted(events, key=lambda event: event[0]):
                if kind == 'create':
                    name = match.group(1).lower()
                    if name in tables: continue
                    table = tables[name] = {'_dialect': dialect, 'name': name, 'columns': {}, 'pk': (), 'unique': {}, 'fks': {}, 'source': source}
                    for item in split_sql(body_at(text, match.end() - 1)):
                        add_definition(table, item, source)
                elif kind == 'alter' and match.group(1).lower() in tables:
                    apply_alter(tables[match.group(1).lower()], match.group(2), source, tables)
                elif kind == 'drop_table':
                    if match.group(2) and match.group(2).upper() == 'CASCADE':
                        raise ValueError(f'Unsupported cascading table removal in {source}')
                    tables.pop(match.group(1).lower(), None)
                elif kind == 'drop_index':
                    for table in tables.values():
                        table['unique'].pop(match.group(1).lower(), None)
                elif kind == 'unique':
                    index, name, tail = match.groups()
                    if name.lower() in tables and not re.search(r'\bWHERE\b', tail, re.I):
                        cols = keys(tail.split(')')[0])
                        if all(c in tables[name.lower()]['columns'] for c in cols):
                            tables[name.lower()]['unique'][index.lower()] = cols
    for table in tables.values():
        for column in table['pk']:
            if dialect != 'sqlite' or table['columns'][column]['type'] == 'integer':
                table['columns'][column]['nn'] = True
        for fk in table['fks'].values():
            assert fk['parent'] in tables, f"Unresolved table {fk}"
            parent = tables[fk['parent']]
            assert all(c in table['columns'] for c in fk['cols']), fk
            assert all(c in parent['columns'] for c in fk['target']), fk
            fk['optional'] = not all(table['columns'][c]['nn'] for c in fk['cols'])
            fk['one'] = any(set(u).issubset(fk['cols']) for u in table['unique'].values())
    return tables


def slug(value):
    return re.sub(r'[^a-z0-9]+', '-', value.lower()).strip('-')


def entity_id(db, name):
    return f'entity-{slug(db)}-{slug(name)}'


def relation_id(db, fk):
    return f'relation-{slug(db)}-{slug(fk["child"])}-{slug(fk["constraint"])}'


# These are documented, persisted identifiers, not SQL foreign keys. Keep the
# evidence beside each edge so an apparent naming match cannot become a claim.
LOGICAL_LINKS = (
    ('account-billing-organization', 'Account Manager', 'organizations', 'id',
     'Billing', 'commercial_accounts', 'organization_id',
     'repos/rtk_billing/migrations/039_payment_commercial_settlement.sql',
     'Billing explicitly stores the Account Manager organization UUID as an external identifier.'),
    ('account-video-organization', 'Account Manager', 'organizations', 'id',
     'Video Cloud', 'devices', 'org_id',
     'repos/rtk_cloud_contracts_doc/cross_service_channel.md',
     'The cross-service device contract carries org_id alongside the Video Cloud device identity.'),
    ('account-video-device', 'Account Manager', 'devices', 'id',
     'Video Cloud', 'devices', 'account_device_id',
     'repos/rtk_cloud_contracts_doc/product_readiness.md',
     'The account device UUID is persisted separately from the Video Cloud device ID.'),
)


def text_lines(value, x, y, css, limit=42, step=20):
    # Underscores are preserved; textwrap supplies deterministic wrapping.
    lines = textwrap.wrap(value, limit, break_long_words=True, break_on_hyphens=False) or ['']
    return ''.join(f'<text x="{x}" y="{y+i*step}" class="{css}">{ESC(line)}</text>' for i, line in enumerate(lines))


def shown_fields(table, required=()):
    chosen = list(dict.fromkeys([*table['pk'], *required]))
    for name in table['columns']:
        if len(chosen) >= max(6, len(required) + len(table['pk'])): break
        if name not in chosen: chosen.append(name)
    return chosen


def box_height(table, required=()):
    return 116 + sum(28 * max(1, (len(f) + 39)//40) for f in shown_fields(table, required))


def entity(db, table, x, y, required=(), role=''):
    fields = shown_fields(table, required)
    height = box_height(table, required)
    name = table['name']
    fkcols = {c for fk in table['fks'].values() for c in fk['cols']}
    output = [f'<a class="entity-link" href="#{entity_id(db, name)}" aria-label="Open complete entity {ESC(db)} {ESC(name)}"><g class="entity" data-table="{name}"><rect x="{x}" y="{y}" width="440" height="{height}" rx="4" fill="#fff" stroke="#4f5d75"/>', f'<path d="M{x} {y+80} h440" stroke="#bfc0c0"/>']
    output.append(text_lines(name, x+16, y+30, 'entity-name', 46, 20))
    output.append(f'<text x="{x+16}" y="{y+68}" class="role">{ESC(role)}</text>')
    yy = y + 106
    for field in fields:
        tags = '/'.join(t for t, enabled in [('PK', field in table['pk']), ('FK', field in fkcols)] if enabled)
        output.append(f'<text x="{x+16}" y="{yy}" class="key">{tags}</text>' + text_lines(field, x+84, yy, 'attribute', 40, 28))
        yy += 28 * max(1, (len(field) + 39)//40)
    count = len(table['columns']) - len(fields)
    output.append(f'<text x="{x+16}" y="{y+height-14}" class="note">{count} additional attributes in the entity catalog</text></g></a>')
    return ''.join(output)


def crow(x, y, direction, maximum, minimum):
    """Endpoint-local Crow's-foot glyph. direction points into connector."""
    def line(a, b, c, d): return f'<path d="M{x+direction*a} {y+b} L{x+direction*c} {y+d}"/>'
    parts = ['<g class="cardinality">']
    if maximum == 'many':
        parts += [line(0, -9, 16, 0), line(0, 9, 16, 0), line(0, 0, 16, 0)]
    else: parts.append(line(10, -9, 10, 9))
    if minimum == 0:
        parts.append(f'<circle cx="{x+direction*26}" cy="{y}" r="5" fill="#f5f5f5"/>')
    else: parts.append(line(22, -9, 22, 9))
    return ''.join(parts) + '</g>'


def diagram(db, child, fks, tables, number):
    ident = f'{slug(db)}-{child["name"]}-{number}'
    required = tuple(dict.fromkeys(c for fk in fks for c in fk['cols']))
    ch = box_height(child, required)
    heights = [box_height(tables[fk['parent']], fk['target']) for fk in fks]
    total = sum(heights) + max(0, len(heights)-1)*56
    height = max(total, ch) + 120
    sy = 32 + (max(total, ch)-ch)//2
    sy -= sy % 4
    endpoints, nodes, relations = [], [], []
    ty = 32
    for i, (fk, ph) in enumerate(zip(fks, heights)):
        rid = relation_id(db, fk)
        ey = ty + ph//2
        ey -= ey % 4
        start = sy+80+(ch-100)*(i+1)//(len(fks)+1)
        start -= start % 4
        lane = 580 + 24*i if ey < start else 676 - 24*i
        d = 1 if ey > start else -1
        path = (f'M480 {start} H{lane-8} Q{lane} {start} {lane} {start+d*8} '
                f'V{ey-d*8} Q{lane} {ey} {lane+8} {ey} H880') if abs(start-ey) >= 16 else f'M480 {ey} H880'
        if abs(start-ey) < 16: start = ey
        endpoints.append(f'<a class="relation-link" href="#{rid}" aria-label="Open relationship {ESC(fk["constraint"])}"><path d="{path}" class="relationship"/><path d="{path}" class="relation-hit"/></a>')
        endpoints.append(crow(480, start, 1, 'one' if fk['one'] else 'many', 0))
        endpoints.append(crow(880, ey, -1, 'one', 0 if fk['optional'] else 1))
        # Numeric labels supplement, never replace, traditional endpoint symbols.
        endpoints.append(f'<text x="516" y="{start-16}" class="range">0..{"1" if fk["one"] else "*"}</text><text x="822" y="{ey-16}" class="range">{"0..1" if fk["optional"] else "1"}</text>')
        endpoints.append(f'<a href="#{rid}" aria-label="Open relationship R{i+1}"><rect x="712" y="{ey-32}" width="48" height="24" rx="2" fill="#f5f5f5"/><text x="720" y="{ey-16}" class="range">R{i+1}</text></a>')
        nodes.append(entity(db, tables[fk['parent']], 880, ty, fk['target'], 'REFERENCED ENTITY' + (' · same table, parent role' if child['name'] == fk['parent'] else '')))
        relations.append(f'<li id="{rid}" tabindex="-1"><strong>R{i+1} · {ESC(fk["constraint"])}.</strong> Referencing entity: <a href="#{entity_id(db, child["name"])}"><code>{ESC(child["name"])} ({ESC(", ".join(fk["cols"]))})</code></a>; referenced entity: <a href="#{entity_id(db, fk["parent"])}"><code>{ESC(fk["parent"])} ({ESC(", ".join(fk["target"]))})</code></a>. Each child has {"zero or one" if fk["optional"] else "exactly one"} parent; each parent has {"zero or one child" if fk["one"] else "zero or more children"}. <span class="source">Declared in <code>{ESC(fk["source"])}</code>.</span> <a href="#overall">Back to overall</a></li>')
        ty += ph + 56
    nodes.insert(0, entity(db, child, 40, sy, required, 'REFERENCING ENTITY'))
    svg = f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1360 {height}" role="group" aria-labelledby="{ident}-title {ident}-desc"><title id="{ident}-title">{ESC(child["name"])} relationships</title><desc id="{ident}-desc">Crow\'s-foot relationships from {ESC(child["name"])} to its referenced entities, with optionality and primary and foreign keys.</desc><rect width="1360" height="{height}" fill="#f5f5f5"/>{"".join(endpoints)}{"".join(nodes)}<path d="M40 {height-56} H1320" stroke="#bfc0c0"/><text x="40" y="{height-28}" class="note">PK primary key · FK declared foreign key · circle = zero · bar = one · crow’s foot = many</text></svg>'
    return f'<details class="model" id="{ident}" {"open" if number == 1 else ""}><summary>{ESC(child["name"])} <span>{len(fks)} relationship{"s" if len(fks)>1 else ""}</span></summary><div class="canvas">{svg}</div><ul class="relations">{"".join(relations)}</ul><p class="model-note">Repeated entities provide context. All FK relationships of every entity are available through the entity catalog below.</p></details>', ident


def catalog(db, tables, links):
    items = []
    incoming = defaultdict(list)
    for table in tables.values():
        for fk in table['fks'].values():
            incoming[fk['parent']].append(fk)
    for name, table in sorted(tables.items()):
        purpose, scenario = ENTITY_NOTES[db][name]
        fkcols = {c for fk in table['fks'].values() for c in fk['cols']}
        rows = []
        for col, definition in table['columns'].items():
            tags = ', '.join(t for t, yes in [('PK', col in table['pk']), ('FK', col in fkcols)] if yes)
            rows.append(f'<tr><td><code>{ESC(col)}</code></td><td>{ESC(definition["type"])}</td><td>{tags}</td><td>{"No" if definition["nn"] else "Yes"}</td></tr>')
        refs = ' · '.join(f'<a href="#{link}">Model {i+1}</a>' for i, link in enumerate(links[name])) or 'No declared incoming or outgoing foreign keys in the extracted DDL.'
        outgoing = ''.join(f'<li><a href="#{relation_id(db, fk)}">{ESC(fk["constraint"])}</a> references <a href="#{entity_id(db, fk["parent"])}">{ESC(fk["parent"])}</a> ({ESC(", ".join(fk["cols"]))} → {ESC(", ".join(fk["target"]))})</li>' for fk in table['fks'].values())
        inbound = ''.join(f'<li>Referenced by <a href="#{entity_id(db, fk["child"])}">{ESC(fk["child"])}</a> via <a href="#{relation_id(db, fk)}">{ESC(fk["constraint"])}</a> ({ESC(", ".join(fk["cols"]))} → {ESC(", ".join(fk["target"]))})</li>' for fk in sorted(incoming[name], key=lambda f: (f['child'], f['constraint'])))
        panels = []
        if outgoing:
            panels.append(f'<div><h4>Outgoing declared FK</h4><ul>{outgoing}</ul></div>')
        if inbound:
            panels.append(f'<div><h4>Referenced by (incoming FK)</h4><ul>{inbound}</ul></div>')
        if not panels:
            panels.append('<p>No declared incoming or outgoing FK.</p>')
        items.append(f'<details class="catalog-entity" id="{entity_id(db, name)}"><summary>{ESC(name)} <span>{len(table["columns"])} attributes · {len(table["fks"])} outgoing FK · {len(incoming[name])} incoming FK</span></summary><div class="entity-explanation"><p><strong>Data managed:</strong> {ESC(purpose)}</p><p><strong>When used:</strong> {ESC(scenario)}</p></div><p class="entity-models">Diagrams: {refs}</p><table><thead><tr><th>Attribute</th><th>SQL type</th><th>Key</th><th>Nullable</th></tr></thead><tbody>{"".join(rows)}</tbody></table><div class="entity-relations {'single' if len(panels) == 1 else ''}">{"".join(panels)}</div><p class="source">Schema source: <a href="../../{ESC(table["source"])}"><code>{ESC(table["source"])}</code></a>. <a href="#overall">Back to overall</a></p></details>')
    return f'<details class="catalog" id="catalog-{slug(db)}"><summary>Complete entity catalog — {len(tables)} tables</summary>{"".join(items)}</details>'


def independent_entities(db, names, tables, number):
    """Draw genuine unconnected entities without inventing associations."""
    ident = f'{slug(db)}-independent-{number}'
    nodes, y = [], 32
    for offset in range(0, len(names), 2):
        pair = names[offset:offset+2]
        for i, name in enumerate(pair):
            nodes.append(entity(db, tables[name], 120+i*640, y, role='NO DECLARED FOREIGN-KEY RELATIONSHIP'))
        y += max(box_height(tables[name]) for name in pair)+48
    height = y+40
    svg = f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1360 {height}" role="img" aria-labelledby="{ident}-title {ident}-desc"><title id="{ident}-title">Independent entities — {ESC(db)}</title><desc id="{ident}-desc">Entities with no declared incoming or outgoing foreign keys in the source schema.</desc><rect width="1360" height="{height}" fill="#f5f5f5"/>{"".join(nodes)}<text x="120" y="{height-28}" class="note">No relationship is implied by proximity. Full attributes appear in the entity catalog.</text></svg>'
    return f'<details class="model" id="{ident}"><summary>Independent entities {number}<span>{ESC(", ".join(names))}</span></summary><div class="canvas">{svg}</div></details>', ident


def relationship_groups(db, tables):
    """Reviewed topical groups; every table is assigned to exactly one card."""
    cards = []
    for number, (title, purpose, scenario, names) in enumerate(GROUPS[db], 1):
        members = names.split()
        fks = sorted((fk for name in members for fk in tables[name]['fks'].values()), key=lambda f: (f['child'], f['constraint']))
        names_html = ''.join(f'<div class="group-entity"><a href="#{entity_id(db, name)}"><code>{ESC(name)}</code></a><p><strong>Data:</strong> {ESC(ENTITY_NOTES[db][name][0])}</p><p><strong>When used:</strong> {ESC(ENTITY_NOTES[db][name][1])}</p></div>' for name in members)
        relations_html = ''.join(f'<li><a href="#{relation_id(db, fk)}">{ESC(fk["constraint"])}</a>: <a href="#{entity_id(db, fk["child"])}">{ESC(fk["child"])}.{ESC(", ".join(fk["cols"]))}</a> references <a href="#{entity_id(db, fk["parent"])}">{ESC(fk["parent"])}.{ESC(", ".join(fk["target"]))}</a></li>' for fk in fks)
        ident = f'group-{slug(db)}-{number}'
        cards.append(f'<article class="group-card" id="{ident}"><h4>Group {number} · {ESC(title)} <span>{len(members)} {"entity" if len(members) == 1 else "entities"} · {len(fks)} outgoing FK</span></h4><div class="group-explanation"><p><strong>Data scope:</strong> {ESC(purpose)}</p><p><strong>When used:</strong> {ESC(scenario)}</p></div><div class="group-entities">{names_html}</div><details class="group-relations"><summary>Declared FK in this group · {len(fks)}</summary><ul>{relations_html or "<li>No outgoing FK in this group; see linked entity for incoming relationships.</li>"}</ul></details></article>')
    toc = ''.join(f'<a href="#group-{slug(db)}-{number}">{ESC(title)}</a>' for number, (title, _, _, _) in enumerate(GROUPS[db], 1))
    return f'<nav class="group-nav" aria-label="{ESC(db)} relationship groups">{toc}</nav><div class="group-grid">{"".join(cards)}</div>'


def overall(databases):
    counts = {db: (len(tables), sum(len(t['fks']) for t in tables.values())) for db, tables in databases.items()}
    for _, left_db, left_table, left_col, right_db, right_table, right_col, _, _ in LOGICAL_LINKS:
        assert left_col in databases[left_db][left_table]['columns']
        assert right_col in databases[right_db][right_table]['columns']
    positions = {'Account Manager': (40, 160), 'Billing': (780, 80),
                 'Video Cloud': (780, 310), 'Cloud Admin': (40, 490),
                 'Cloud Frontend': (780, 490)}
    boxes = []
    for db, (x, y) in positions.items():
        tables, fks = counts[db]
        boxes.append(f'<a class="overview-service" href="#{slug(db)}" aria-label="Open {ESC(db)} service"><g><rect x="{x}" y="{y}" width="460" height="{180 if db == "Account Manager" else 110}" rx="4"/><text x="{x+20}" y="{y+42}" class="overview-title">{ESC(db)}</text><text x="{x+20}" y="{y+76}" class="overview-count">{tables} entities · {fks} declared FK</text></g></a>')
    # Route distinct evidence-backed mappings through the open gutter, never
    # through a service box. No cardinality glyph is used on logical edges.
    routes = ((270, 135, 610, 730, 'org → commercial account'),
              (300, 350, 700, 750, 'org → video device'),
              (330, 390, 650, 730, 'account device → video device'))
    edges, details = [], []
    for number, (link, (start_y, end_y, lane, badge_x, label)) in enumerate(zip(LOGICAL_LINKS, routes), 1):
        key, left_db, left_table, left_col, right_db, right_table, right_col, source, reason = link
        ident = f'logical-{key}'
        path = f'M500 {start_y} H{lane} V{end_y} H780'
        edges.append(f'<a class="logical-line" href="#{ident}" aria-label="Open logical mapping {ESC(label)}"><path d="{path}"/><path d="{path}" class="logical-hit"/><circle cx="{badge_x}" cy="{end_y}" r="15"/><text x="{badge_x}" y="{end_y+5}" text-anchor="middle">{number}</text><title>{ESC(label)} — logical reference, not a database FK</title></a>')
        details.append(f'<li id="{ident}" tabindex="-1"><strong><a href="#{ident}">{ESC(label)}</a></strong>: <a href="#{entity_id(left_db, left_table)}">{ESC(left_db)}.{ESC(left_table)}.{ESC(left_col)}</a> → <a href="#{entity_id(right_db, right_table)}">{ESC(right_db)}.{ESC(right_table)}.{ESC(right_col)}</a>. {ESC(reason)} <strong>Logical reference, not a database FK.</strong> <a href="../../{ESC(source)}">Source: <code>{ESC(source)}</code></a>.</li>')
    svg = f'<svg class="overall-svg" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1280 630" role="group" aria-labelledby="overall-title overall-desc"><title id="overall-title">RTK Cloud database ownership overview</title><desc id="overall-desc">Five service schema collections. Dashed connectors are documented logical references, not foreign keys. Select a service or connector for details.</desc><rect width="1280" height="630" fill="#f5f5f5"/>{"".join(edges)}{"".join(boxes)}</svg>'
    index = []
    for db, tables in databases.items():
        for name, table in sorted(tables.items()):
            index.append(f'<a class="search-item" href="#{entity_id(db, name)}" data-search="{ESC((db+" "+name).lower())}">{ESC(db)} · {ESC(name)} <small>entity</small></a>')
            for fk in table['fks'].values():
                label = f'{db} {fk["child"]} {", ".join(fk["cols"])} {fk["parent"]} {", ".join(fk["target"])} {fk["constraint"]}'
                index.append(f'<a class="search-item" href="#{relation_id(db, fk)}" data-search="{ESC(label.lower())}">{ESC(db)} · {ESC(fk["child"])} → {ESC(fk["parent"])} <small>declared FK</small></a>')
    return f'<section id="overall"><h2>Overall</h2><p>Choose a service to inspect bounded relationship groups, then an entity or declared FK for its complete detail. Dashed lines are evidence-backed cross-service identifier mappings; they are not SQL foreign keys and imply no cardinality.</p><p class="mobile-scroll">Swipe the overview horizontally to see every service and mapping.</p><div class="overview-canvas">{svg}</div><h3>Cross-service logical references</h3><ol class="logical-details">{"".join(details)}</ol><label class="search-label" for="atlas-search">Find any entity or declared FK</label><input id="atlas-search" type="search" placeholder="Search service, table, column or constraint" autocomplete="off"><p id="search-status" class="source">Type to search all {sum(n for n, _ in counts.values())} entities and {sum(n for _, n in counts.values())} declared FK relationships.</p><div id="search-results" class="search-results" hidden>{"".join(index)}</div></section>'


CSS = '''
*{box-sizing:border-box}html{scroll-behavior:smooth}body{margin:0;background:#f5f5f5;color:#2d3142;font:16px/1.6 Geist,Arial,sans-serif}main{max-width:1480px;margin:auto;padding:48px 40px}h1,h2{font-family:'Instrument Serif',Georgia,serif;font-weight:400;line-height:1.15}h1{font-size:52px;margin:12px 0 24px}h2{font-size:38px;margin:48px 0 16px}h3{font-size:21px}a{color:#2e5aa8}code{font-family:'Geist Mono',monospace;font-size:.88em;overflow-wrap:anywhere}.eyebrow{font-size:12px;letter-spacing:.14em;color:#4f5d75}.intro{max-width:880px}nav{display:flex;gap:12px;flex-wrap:wrap;margin:28px 0}nav a{border:1px solid #bfc0c0;border-radius:4px;padding:8px 16px;text-decoration:none}.legend{border-left:4px solid #eb6c36;padding:8px 24px;margin:32px 0;background:white}.legend p{margin:6px 0}.model,.catalog{margin:16px 0;border:1px solid #bfc0c0;border-radius:4px}.model summary,.catalog>summary{padding:16px 20px;font-size:18px;font-weight:600;cursor:pointer;overflow-wrap:anywhere}summary span{font-size:13px;color:#4f5d75;margin-left:16px;font-weight:400}.canvas{overflow:auto;padding:0}svg{display:block;width:100%;min-width:1360px}.entity-name{font:600 16px Geist,Arial,sans-serif;fill:#2d3142}.attribute{font:13px 'Geist Mono',monospace;fill:#2d3142}.key{font:600 12px Geist,Arial,sans-serif;fill:#2e5aa8}.role{font:11px Geist,Arial,sans-serif;letter-spacing:.06em;fill:#4f5d75}.note{font:12px Geist,Arial,sans-serif;fill:#4f5d75}.range{font:12px 'Geist Mono',monospace;fill:#4f5d75}.relationship,.cardinality{fill:none;stroke:#2d3142;stroke-width:1.6}.relations{margin:16px 28px 16px 48px;padding:0;font-size:14px}.relations li{margin:8px 0}.model-note,.source{margin:16px 24px;color:#4f5d75;font-size:13px}.catalog-entity{margin:12px 20px;border-top:1px solid #bfc0c0;padding:12px 0}.catalog-entity summary{cursor:pointer;font-weight:600}table{width:100%;border-collapse:collapse;margin:16px 0;font-size:14px}th,td{text-align:left;padding:10px 12px;border-bottom:1px solid #ddd}th{background:#ececec}section{scroll-margin-top:20px}.standalone{line-height:2.1}footer{border-top:1px solid #bfc0c0;padding-top:24px;margin-top:48px;font-size:13px;color:#4f5d75}@media(max-width:700px){main{padding:24px 16px}h1{font-size:38px}summary span{display:block;margin-left:20px}}@media print{main{max-width:none;padding:0}nav{display:none}.canvas{overflow:visible}svg{min-width:0}.model{break-inside:avoid}details:not([open])>*:not(summary){display:block}}
'''

CSS += '''
#overall{margin-top:40px}.overview-canvas{overflow:auto;border:1px solid #bfc0c0;border-radius:4px;background:#f5f5f5}.overall-svg{min-width:900px}.overview-service rect{fill:#fff;stroke:#4f5d75;stroke-width:1.6}.overview-service:hover rect,.overview-service:focus-visible rect{stroke:#eb6c36;stroke-width:3}.overview-title{font:600 23px Geist,Arial,sans-serif;fill:#2d3142}.overview-count{font:15px Geist,Arial,sans-serif;fill:#4f5d75}.logical-line>path:first-child{fill:none;stroke:#eb6c36;stroke-width:3;stroke-dasharray:9 7}.logical-hit,.relation-hit{fill:none;stroke:transparent;stroke-width:20;pointer-events:stroke}.logical-line:hover>path:first-child,.logical-line:focus-visible>path:first-child{stroke:#2e5aa8;stroke-width:5}.relation-link:hover .relationship,.relation-link:focus-visible .relationship{stroke:#eb6c36;stroke-width:4}.entity-link:hover rect,.entity-link:focus-visible rect{stroke:#eb6c36;stroke-width:3}.logical-details{background:#fff;border-left:4px solid #eb6c36;padding:18px 32px}.logical-details li{padding:8px 12px;margin:4px 0;scroll-margin-top:24px}.group-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,410px),1fr));gap:16px;margin:20px 0 36px}.group-card{background:#fff;border:1px solid #bfc0c0;border-radius:4px;padding:18px;min-width:0}.group-card h4{font-size:17px;margin:0 0 12px}.group-card h4 span{display:block;font-size:12px;color:#4f5d75;font-weight:400}.chip-list{display:flex;flex-wrap:wrap;gap:8px}.entity-chip{border:1px solid #bfc0c0;border-radius:4px;padding:3px 8px;text-decoration:none;font-size:13px;overflow-wrap:anywhere}.group-card ul{padding-left:20px;font-size:13px;max-height:230px;overflow:auto}.entity-relations{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,300px),1fr));gap:16px}.entity-relations h4{margin:8px 0 0}.entity-relations ul{margin:6px 0 18px;padding-left:20px}.entity-models{font-size:13px}.catalog-entity:target,.relations li:target,.logical-details li:target,.target-highlight{background:#fff1e9;outline:2px solid #eb6c36;outline-offset:2px}.catalog-entity,.relations li{scroll-margin-top:24px}.search-label{display:block;font-weight:600;margin:24px 0 8px}#atlas-search{width:100%;padding:12px;border:1px solid #4f5d75;border-radius:4px;font:inherit;background:#fff}.search-results{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,330px),1fr));gap:4px 16px;max-height:380px;overflow:auto;background:#fff;border:1px solid #bfc0c0;padding:12px}.search-results[hidden],.search-item[hidden]{display:none}.search-item{padding:5px 8px;overflow-wrap:anywhere}.search-item small{color:#4f5d75;margin-left:6px}.jump-back{font-size:13px;margin-top:20px}@media(max-width:700px){.overall-svg{min-width:900px}.logical-details{padding:12px 20px}.group-card ul{max-height:none}}@media print{.overview-canvas{overflow:visible}.overall-svg{min-width:0}.group-card ul,.search-results{max-height:none}}
'''

CSS += '.logical-line circle{fill:#fff;stroke:#eb6c36;stroke-width:2}.logical-line text{font:600 13px Geist,Arial,sans-serif;fill:#2d3142}.logical-line:hover circle,.logical-line:focus-visible circle{stroke:#2e5aa8;stroke-width:3}'
CSS += '.mobile-scroll{display:none;color:#4f5d75;font-size:13px}@media(max-width:700px){.mobile-scroll{display:block}}'
CSS += '.group-nav{margin:14px 0 22px;gap:8px}.group-nav a{font-size:13px;padding:5px 10px}.group-card{scroll-margin-top:24px}.group-card:target{outline:2px solid #eb6c36}.group-explanation{border-left:3px solid #eb6c36;padding-left:12px;margin:14px 0 18px}.group-explanation p,.entity-explanation p{margin:4px 0}.group-entities{display:grid;gap:10px}.group-entity{border-top:1px solid #ddd;padding-top:8px;font-size:13px}.group-entity p{margin:3px 0;line-height:1.5}.group-entity>a{font-weight:600;font-size:14px}.group-relations{border-top:1px solid #bfc0c0;margin-top:16px;padding-top:10px}.group-relations summary{cursor:pointer;font-weight:600;font-size:13px}.group-relations ul{max-height:260px;overflow:auto;padding-left:20px;font-size:13px}.entity-explanation{border-left:3px solid #eb6c36;margin:12px 0;padding:6px 12px;background:#fff}'
CSS += '.group-grid{align-items:start}.group-card{align-self:start}'
CSS += '.entity-relations.single{grid-template-columns:1fr}'
CSS += '.entity-relations a,.group-relations a,.relations a,.relations strong,.group-card a,.catalog-entity summary{overflow-wrap:anywhere;word-break:break-word}'

JS = '''
(() => {
  const search = document.getElementById('atlas-search');
  const results = document.getElementById('search-results');
  const status = document.getElementById('search-status');
  const items = Array.from(results.querySelectorAll('.search-item'));
  search.addEventListener('input', () => {
    const terms = search.value.toLowerCase().trim().split(/\\s+/).filter(Boolean);
    let count = 0;
    for (const item of items) {
      const match = terms.length > 0 && terms.every(term => item.dataset.search.includes(term));
      item.hidden = !match;
      if (match) count++;
    }
    results.hidden = terms.length === 0;
    status.textContent = terms.length ? `${count} matching entities or declared FK relationships.` : `Search all ${items.length} entities and declared FK relationships.`;
  });
  let highlighted;
  function openHashTarget() {
    if (highlighted) highlighted.classList.remove('target-highlight');
    const id = decodeURIComponent(location.hash.slice(1));
    const target = id && document.getElementById(id);
    if (!target) return;
    if (target.tagName === 'DETAILS') target.open = true;
    for (let parent = target.parentElement; parent; parent = parent.parentElement) {
      if (parent.tagName === 'DETAILS') parent.open = true;
    }
    target.classList.add('target-highlight');
    highlighted = target;
    requestAnimationFrame(() => target.scrollIntoView({block:'start'}));
  }
  window.addEventListener('hashchange', openHashTarget);
  document.addEventListener('click', event => {
    const link = event.target.closest('a[href^="#"]');
    if (link && link.hash === location.hash) requestAnimationFrame(openHashTarget);
  });
  openHashTarget();
})();
'''


def validate_narratives(databases):
    assert set(GROUPS) == set(databases) == set(ENTITY_NOTES)
    for db, tables in databases.items():
        assigned = [name for _, purpose, scenario, names in GROUPS[db]
                    for name in names.split() if purpose and scenario]
        assert all(1 <= len(names.split()) <= 8 for _, _, _, names in GROUPS[db]), db
        assert len(assigned) == len(set(assigned)), f'Duplicate group entity in {db}'
        assert set(assigned) == set(tables), f'Unassigned group entity in {db}: {set(tables) - set(assigned)}'
        assert set(ENTITY_NOTES[db]) == set(tables), f'Missing entity explanation in {db}: {set(tables) - set(ENTITY_NOTES[db])}'
        assert all(all(part for part in note) for note in ENTITY_NOTES[db].values()), db


def main():
    databases = {db: parse_database(paths) for db, paths in SOURCES.items()}
    validate_narratives(databases)
    sections, stats, revisions = [], [], []
    for db, paths in SOURCES.items():
        tables = databases[db]
        links, models, count = defaultdict(list), [], 0
        order = sorted(tables, key=lambda n: (n != 'organization_members', n))
        for name in order:
            table = tables[name]
            fks = list(table['fks'].values())
            for offset in range(0, len(fks), 3):
                batch = fks[offset:offset+3]
                count += 1
                rendered, ident = diagram(db, table, batch, tables, count)
                models.append(rendered)
                for referenced in {name, *(fk['parent'] for fk in batch)}: links[referenced].append(ident)
        isolated = [n for n in tables if not links[n]]
        independent = []
        for offset in range(0, len(isolated), 4):
            batch = sorted(isolated)[offset:offset+4]
            rendered, ident = independent_entities(db, batch, tables, offset//4+1)
            independent.append(rendered)
            for name in batch: links[name].append(ident)
        relations = sum(len(t['fks']) for t in tables.values())
        stats.append((db, len(tables), relations, count))
        repo = ROOT / Path(paths[0]).parts[0] / Path(paths[0]).parts[1]
        revision = subprocess.check_output(['git', '-C', str(repo), 'rev-parse', '--short=12', 'HEAD'], text=True).strip()
        revisions.append(f'| {db} | `{revision}` |')
        assert all(links[n] for n in tables), 'Every entity must have a diagram'
        sections.append(f'<section id="{slug(db)}"><h2>{db}</h2><p>{len(tables)} entities · {relations} declared {"relationship" if relations == 1 else "relationships"} · {count} relationship {"model" if count == 1 else "models"}. Topical groups contain at most eight entities each and explain their data and operation context. Group membership is editorial, not a database relationship; an FK may point to an entity in another group.</p><h3>Relationship groups</h3>{relationship_groups(db, tables)}<h3>Declared FK detail diagrams</h3><p>Open a model to read it at full size. On narrow screens, scroll horizontally instead of shrinking the text.</p>{"".join(models)}<h3>Entities without declared FK relationships</h3><p>Application references are not automatically promoted to database relationships.</p>{"".join(independent)}{catalog(db, tables, links)}<p class="jump-back"><a href="#overall">↑ Back to overall</a></p></section>')
    total = sum(s[1] for s in stats)
    edge_count = sum(s[2] for s in stats)
    nav = '<a href="#overall">Overall</a>' + ''.join(f'<a href="#{slug(db)}">{db}</a>' for db in SOURCES)
    OUT.write_text(f'''<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Database ER Model — RTK Cloud</title><style>{CSS}</style></head><body><main><p class="eyebrow">RTK CLOUD / DATABASE DESIGN</p><h1>Database Entity–Relationship Model</h1><p class="intro">Traditional Crow’s-foot notation, organized around connected entities. The model covers {total} tables and {edge_count} declared foreign-key relationships across five service-owned schema collections. Each relationship appears with both endpoints; repeated entities keep larger models readable.</p><div class="legend"><h3>How to read the model</h3><p><strong>Circle: zero · Bar: one · Crow’s foot: many.</strong> Each endpoint describes how many records at that end can relate to one record at the other end.</p><p><code>1</code> exactly one · <code>0..1</code> optional one · <code>0..*</code> zero or more. PK marks primary-key attributes; FK marks actual foreign-key attributes. A PK/FK entity commonly resolves a many-to-many relationship.</p><p>Parent optionality comes from FK nullability. A unique child FK changes the child maximum from many to one. SQL foreign keys alone do not require a parent to have children. <strong>Dashed orange lines in Overall are documented logical mappings, not SQL FK.</strong></p></div><nav aria-label="Database navigation">{nav}</nav>{overall(databases)}<div id="details"><h2>Details</h2>{"".join(sections)}</div><footer>Source-derived design model; not an inspection of deployed databases. Exact relationship columns and the full entity catalog accompany every database. See database-er-diagrams.md for scope and refresh instructions.</footer></main><script>{JS}</script></body></html>''')
    rows = '\n'.join(f'| {db} | {tables} | {rels} | {models} |' for db, tables, rels, models in stats)
    INDEX.write_text(f'''# Database Entity–Relationship Model

Open the [Crow’s-foot ER atlas](database-er-atlas.html).

## Notation and navigation

The single-file atlas opens with an **Overall** view of five schema owners and only evidence-backed cross-service mappings. Select a service to reach reviewed topical groups of at most eight entities. Every group explains the data domain and operation context; every entity has its own data-purpose and usage-scenario description. Group membership is editorial, **not** an extra FK or a claim that every member is directly connected. All {total} entities, including isolated tables, occur in exactly one group. The description inventory is maintained in `scripts/database_er_narratives_en.py`; regeneration fails when a new DDL table lacks a group or explanation.

Search indexes all {total} entities and {edge_count} FK relationships. Entity nodes open complete columns, descriptions, and outgoing/incoming FK lists. Each FK detail explicitly links both the referencing (child) and referenced (parent) entity; the parent catalog links back to every child that references it. Relationship lines, R labels, and group links open a stable relationship anchor. Direct `file://...#entity-...` or `#relation-...` links expand the containing details; browser Back/Forward follows those anchors. Return links lead to Overall.

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
{rows}

These are service schema collections, not a claim that there are exactly five physical database instances. Video Cloud includes the PKI registry; Frontend includes its SQLite analytics/search/leads schemas. Runtime stores without relational DDL, external provider storage, and Redis keyspaces have no SQL ER model here. Schema migration metadata tables are included where defined by these sources.

## Sources and reproducibility

The generator reads Account Manager and Billing migrations in filename order, Video Cloud runtime PostgreSQL schema and PKI DDL, Cloud Admin SQLite migrations, and Frontend SQLite repository initializers. It applies literal migration table/index deletion, column addition/removal/rename, table rename, nullability changes, FK additions/removals, and unconditional unique indexes in source order. Test reset helpers are excluded; unsupported persistent table/index DDL fails explicitly. Table attributes omitted from a drawing are available in its database’s full entity catalog. Each entity explanation links to its checked-in schema source. The descriptions express the schema's data responsibility and intended operation context, informed by representative runtime call sites; they are not proof of activity in any deployed database.

The revision below is each checkout's base commit. The generated model also
includes any uncommitted changes to the listed source files in the current
workspace; it is not necessarily a pure snapshot of those commits.

| Source checkout | Base commit |
| --- | --- |
{chr(10).join(revisions)}

Refresh from the workspace root with `python3 scripts/generate_database_er_atlas.py`. The process reads source files only; it never opens a deployed database. Exact source paths appear in the entity catalog.

## Interpretation boundary

This is a static model of literal checked-in DDL, not a migration execution engine or live database introspection. Conditional historical repair branches and dynamically constructed SQL require review if their behavior changes. Account Manager, Video Cloud, and Admin extraction is also compared with independently initialized PostgreSQL/SQLite catalogs using `scripts/check_database_er_catalog.py`; the stored local catalog fixtures and upgrade tests are under `tests/fixtures/database-simplification` and the service repositories. This does not assert that any deployed environment has already upgraded.

Dashed orange Overall links are **logical references, not enforced database foreign keys**. They use no Crow’s-foot cardinality because the cited source does not establish one. Each link names both endpoint columns and links to its checked-in code or contract evidence. Currently evidenced mappings are Account Manager `organizations.id` to Billing `commercial_accounts.organization_id`, Account Manager `organizations.id` to Video Cloud `devices.org_id`, and Account Manager `devices.id` to Video Cloud `devices.account_device_id`. The Account Manager device UUID and Video Cloud `devices.id` are deliberately not equated. No Cloud Admin or Cloud Frontend cross-service link is inferred from similar column names alone. The only Crow’s-foot lines represent declared FKs in the extracted DDL.

Tables with no declared FK remain explicitly listed and fully documented in the catalog. Attributes displayed on a relationship model prioritize its PK and the relationship’s actual FK columns; other relationships of repeated entities are linked in the catalog. The HTML embeds its styles and navigation script, requires no external drawing library, and can be opened directly via `file://`.
''')
    print(f'Generated {total} entities, {edge_count} relationships, {sum(s[3] for s in stats)} readable models')


if __name__ == '__main__':
    main()
