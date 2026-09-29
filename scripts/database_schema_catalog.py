#!/usr/bin/env python3
"""Capture database catalogs for ER documentation and compare schema snapshots.

Connection arguments name environment variables, so connection URLs stay out of
shell history and generated artifacts. All inspect paths use read-only access.
"""
from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import html
import json
import os
import re
import shutil
import sqlite3
import ssl
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from urllib.parse import parse_qs, unquote, urlsplit

FORMAT_VERSION = 1


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=False)


def digest(value):
    return hashlib.sha256(canonical(value).encode()).hexdigest()


def _numbers(value):
    if value is None:
        return []
    if isinstance(value, (list, tuple)):
        return [int(x) for x in value]
    return [int(x) for x in str(value).strip('{}').replace(',', ' ').split()]


def _metadata(rows):
    result = []
    for kind, schema, name, version, details in rows:
        if isinstance(details, str):
            details = json.loads(details)
        result.append({'kind': kind, 'schema': schema, 'name': name,
                       'version': version, 'details': details})
    return sorted(result, key=lambda row: (row['kind'], row['schema'], row['name']))


def inspect_postgres(dsn, service, store):
    try:
        import pg8000.dbapi
    except ImportError as exc:
        raise RuntimeError('Install scripts/requirements-database-schema.txt') from exc
    url = urlsplit(dsn)
    if url.scheme not in ('postgres', 'postgresql') or not url.hostname or not url.path.strip('/'):
        raise ValueError('Expected a PostgreSQL connection URL')
    options = parse_qs(url.query)
    mode = options.get('sslmode', ['prefer'])[-1]
    if mode not in ('disable', 'prefer', 'require', 'verify-ca', 'verify-full'):
        raise ValueError(f'Unsupported PostgreSQL sslmode: {mode}')
    ssl_context = None
    if mode == 'disable':
        ssl_context = False
    elif mode in ('require', 'verify-ca', 'verify-full'):
        ssl_context = ssl.create_default_context()
        if mode == 'require':
            ssl_context.check_hostname = False
            ssl_context.verify_mode = ssl.CERT_NONE
        elif mode == 'verify-ca':
            ssl_context.check_hostname = False
    conn = pg8000.dbapi.connect(
        host=url.hostname, port=url.port or 5432, user=unquote(url.username or ''),
        password=unquote(url.password or ''), database=unquote(url.path.lstrip('/')),
        ssl_context=ssl_context,
    )
    conn.autocommit = True
    try:
        cur = conn.cursor()
        cur.execute('BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY')
        cur.execute("""SELECT c.oid, n.nspname, c.relname, obj_description(c.oid,'pg_class')
                       FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
                       WHERE n.nspname='public' AND c.relkind IN ('r','p')
                       ORDER BY n.nspname,c.relname""")
        tables = {}
        for oid, schema, name, comment in cur.fetchall():
            tables[oid] = {'schema': schema, 'name': name, 'comment': comment or '',
                           'columns': [], 'pk': [], 'fks': [], 'indexes': [],
                           'checks': [], 'triggers': []}
        cur.execute("""SELECT c.oid,a.attnum,a.attname,format_type(a.atttypid,a.atttypmod),
                              a.attnotnull,pg_get_expr(d.adbin,d.adrelid),col_description(c.oid,a.attnum)
                       FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
                       JOIN pg_attribute a ON a.attrelid=c.oid
                       LEFT JOIN pg_attrdef d ON d.adrelid=c.oid AND d.adnum=a.attnum
                       WHERE n.nspname='public' AND c.relkind IN ('r','p')
                         AND a.attnum>0 AND NOT a.attisdropped
                       ORDER BY c.oid,a.attnum""")
        attr = {}
        for oid, number, name, dtype, required, default, comment in cur.fetchall():
            attr[(oid, number)] = name
            tables[oid]['columns'].append({'name': name, 'type': dtype,
                                           'not_null': bool(required), 'default': default,
                                           'comment': comment or ''})
        cur.execute("""SELECT conrelid,confrelid,conname,contype,conkey::text,confkey::text,
                              confupdtype,confdeltype,pg_get_constraintdef(oid,true)
                       FROM pg_constraint WHERE conrelid=ANY(%s)
                       ORDER BY conrelid,conname""", (list(tables),))
        actions = {'a': 'NO ACTION', 'r': 'RESTRICT', 'c': 'CASCADE',
                   'n': 'SET NULL', 'd': 'SET DEFAULT'}
        for child, parent, name, kind, child_nums, parent_nums, update, delete, definition in cur.fetchall():
            table = tables[child]
            if kind == 'p':
                table['pk'] = [attr[(child, n)] for n in _numbers(child_nums)]
            elif kind == 'f':
                if parent not in tables:
                    raise ValueError(f'{table["name"]}.{name} refers outside captured schema')
                table['fks'].append({
                    'name': name, 'columns': [attr[(child, n)] for n in _numbers(child_nums)],
                    'parent': tables[parent]['name'],
                    'parent_columns': [attr[(parent, n)] for n in _numbers(parent_nums)],
                    'on_update': actions[update], 'on_delete': actions[delete],
                })
            elif kind == 'c':
                table['checks'].append({'name': name, 'definition': definition})
        cur.execute("""SELECT i.indrelid,ci.relname,i.indisunique,i.indpred IS NOT NULL,
                              i.indexprs IS NOT NULL,i.indnkeyatts,i.indkey::text,
                              pg_get_indexdef(i.indexrelid)
                       FROM pg_index i JOIN pg_class ci ON ci.oid=i.indexrelid
                       WHERE i.indrelid=ANY(%s) ORDER BY i.indrelid,ci.relname""", (list(tables),))
        for oid, name, unique, partial, expression, nkeys, keynums, definition in cur.fetchall():
            nums = _numbers(keynums)[:nkeys]
            columns = [attr[(oid, n)] for n in nums] if not expression and all(n for n in nums) else []
            tables[oid]['indexes'].append({'name': name, 'unique': bool(unique),
                                            'partial': bool(partial), 'columns': columns,
                                            'definition': definition})
        cur.execute("""SELECT tgrelid,tgname,pg_get_triggerdef(oid,true)
                       FROM pg_trigger WHERE tgrelid=ANY(%s) AND NOT tgisinternal
                       ORDER BY tgrelid,tgname""", (list(tables),))
        for oid, name, definition in cur.fetchall():
            tables[oid]['triggers'].append({'name': name, 'definition': definition})
        cur.execute("""SELECT n.nspname,c.relname,pg_get_viewdef(c.oid,true)
                       FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
                       WHERE n.nspname='public' AND c.relkind IN ('v','m')
                       ORDER BY n.nspname,c.relname""")
        views = [{'schema': s, 'name': n, 'definition': d} for s, n, d in cur.fetchall()]
        cur.execute("SELECT to_regclass('public.schema_metadata') IS NOT NULL")
        metadata = []
        if cur.fetchone()[0]:
            cur.execute("""SELECT object_kind,schema_name,object_name,meta_version,details::text
                           FROM public.schema_metadata ORDER BY 1,2,3""")
            metadata = _metadata(cur.fetchall())
        cur.execute("SELECT to_regclass('public.schema_migrations') IS NOT NULL")
        migrations = []
        if cur.fetchone()[0]:
            cur.execute('SELECT version::text FROM public.schema_migrations ORDER BY version')
            migrations = [row[0] for row in cur.fetchall()]
        cur.execute('ROLLBACK')
        return {'service': service, 'store': store, 'dialect': 'postgresql',
                'tables': sorted(tables.values(), key=lambda t: (t['schema'], t['name'])),
                'views': views, 'metadata': metadata, 'migrations': migrations}
    finally:
        conn.close()


def inspect_sqlite(path, service, store):
    path = Path(path).resolve(strict=True)
    conn = sqlite3.connect(path.as_uri() + '?mode=ro', uri=True)
    try:
        conn.execute('PRAGMA query_only=ON')
        conn.execute('BEGIN')
        tables = []
        for name, ddl in conn.execute("""SELECT name,sql FROM sqlite_master
                                        WHERE type='table' AND name NOT LIKE 'sqlite_%'
                                        ORDER BY name"""):
            columns = []
            for _, cname, dtype, required, default, pk_order, hidden in conn.execute(
                    'SELECT * FROM pragma_table_xinfo(?)', (name,)):
                columns.append({'name': cname, 'type': dtype, 'not_null': bool(required),
                                'default': default, 'hidden': hidden, 'comment': '',
                                'pk_order': pk_order})
            pk = [c['name'] for c in sorted(columns, key=lambda c: c['pk_order']) if c['pk_order']]
            for column in columns:
                column.pop('pk_order')
            foreign = {}
            for fid, seq, parent, child_col, parent_col, on_update, on_delete, _ in conn.execute(
                    'SELECT * FROM pragma_foreign_key_list(?)', (name,)):
                fk = foreign.setdefault(fid, {'name': f'{name}_fk_{fid}', 'columns': [],
                                              'parent': parent, 'parent_columns': [],
                                              'on_update': on_update, 'on_delete': on_delete})
                fk['columns'].append(child_col)
                fk['parent_columns'].append(parent_col)
            indexes = []
            for _, iname, unique, origin, partial in conn.execute('SELECT * FROM pragma_index_list(?)', (name,)):
                keys = [row[2] for row in conn.execute('SELECT * FROM pragma_index_xinfo(?)', (iname,)) if row[5]]
                idef = conn.execute('SELECT sql FROM sqlite_master WHERE name=?', (iname,)).fetchone()
                indexes.append({'name': iname, 'unique': bool(unique), 'partial': bool(partial),
                                'columns': keys if all(keys) else [],
                                'definition': idef[0] if idef else None, 'origin': origin})
            triggers = [{'name': n, 'definition': d} for n, d in conn.execute(
                "SELECT name,sql FROM sqlite_master WHERE type='trigger' AND tbl_name=? ORDER BY name", (name,))]
            tables.append({'schema': 'main', 'name': name, 'comment': '', 'columns': columns,
                           'pk': pk, 'fks': list(foreign.values()), 'indexes': indexes,
                           'checks': [], 'triggers': triggers, 'definition': ddl})
        views = [{'schema': 'main', 'name': n, 'definition': d} for n, d in conn.execute(
            "SELECT name,sql FROM sqlite_master WHERE type='view' ORDER BY name")]
        metadata = []
        if any(t['name'] == 'schema_metadata' for t in tables):
            metadata = _metadata(conn.execute("""SELECT object_kind,schema_name,object_name,
                                               meta_version,details FROM schema_metadata
                                               ORDER BY 1,2,3"""))
        migrations = []
        if any(t['name'] == 'schema_migrations' for t in tables):
            migrations = [str(row[0]) for row in conn.execute('SELECT version FROM schema_migrations ORDER BY version')]
        conn.rollback()
        return {'service': service, 'store': store, 'dialect': 'sqlite',
                'tables': tables, 'views': views, 'metadata': metadata, 'migrations': migrations}
    finally:
        conn.close()


def validate(snapshot, *, strict=False):
    problems = []
    objects = {(s['service'], s['store'], t['schema'], t['name']): t
               for s in snapshot['stores'] for t in s['tables']}
    if len(objects) != sum(len(s['tables']) for s in snapshot['stores']):
        problems.append('Duplicate service/store/schema/table identity')
    for store in snapshot['stores']:
        meta = {(m['kind'], m['schema'], m['name']): m['details'] for m in store['metadata']}
        if len(meta) != len(store['metadata']):
            problems.append(f'{store["service"]}/{store["store"]}: duplicate metadata identity')
        for row in store['metadata']:
            kind, schema, name = row['kind'], row['schema'], row['name']
            if row['version'] != FORMAT_VERSION:
                problems.append(f'{store["service"]}/{store["store"]}: unsupported metadata version {row["version"]}')
            if kind == 'table' and (store['service'], store['store'], schema, name) not in objects:
                problems.append(f'{store["service"]}/{store["store"]}: metadata refers to missing table {name}')
            if kind == 'column':
                table_name, _, column = name.partition('.')
                table = objects.get((store['service'], store['store'], schema, table_name))
                if not table or column not in {c['name'] for c in table['columns']}:
                    problems.append(f'{store["service"]}/{store["store"]}: metadata refers to missing column {name}')
            if kind == 'logical_ref':
                detail = row['details']
                for role in ('source', 'target'):
                    service_name = detail.get(role + '_service')
                    table_name = detail.get(role + '_table')
                    column_name = detail.get(role + '_column')
                    candidates = [table for (owner, _, _, name), table in objects.items()
                                  if owner == service_name and name == table_name]
                    if len(candidates) != 1 or column_name not in {
                            column['name'] for table in candidates for column in table['columns']}:
                        problems.append(f'{store["service"]}/{store["store"]}: '
                                        f'logical reference {name} has missing {role} endpoint')
        if strict and not store['metadata']:
            problems.append(f'{store["service"]}/{store["store"]}: metadata unavailable')
        for table in store['tables']:
            schema, name = table['schema'], table['name']
            if name == 'schema_metadata':
                continue
            note = meta.get(('table', schema, name))
            if strict and (not note or not note.get('group') or not note.get('scenario') or
                           not (table['comment'] or note.get('purpose'))):
                problems.append(f'{store["service"]}/{store["store"]}: missing table description {name}')
            if strict and note and ('group', schema, note['group']) not in meta:
                problems.append(f'{store["service"]}/{store["store"]}: missing group {note["group"]}')
            critical = set(table['pk']) | {c for fk in table['fks'] for c in fk['columns']}
            for column in table['columns']:
                semantic = bool(re.search(r'(?:^|_)(?:status|state|count|bytes)$', column['name']) or
                                column['name'].endswith('_minor'))
                if (column['name'] in critical or semantic) and strict:
                    detail = meta.get(('column', schema, name + '.' + column['name']))
                    if not column['comment'] and not (detail and detail.get('description')):
                        problems.append(f'{store["service"]}/{store["store"]}: missing critical column description {name}.{column["name"]}')
                    if semantic and (not detail or not detail.get('semantic_kind')):
                        problems.append(f'{store["service"]}/{store["store"]}: missing semantic kind {name}.{column["name"]}')
    return problems


def snapshot(stores, *, environment=None, revisions=None, source_dirty=None):
    stores = sorted(stores, key=lambda s: (s['service'], s['store']))
    structural = [{key: value for key, value in store.items() if key != 'migrations'} for store in stores]
    return {'format_version': FORMAT_VERSION, 'environment': environment,
            'captured_at': dt.datetime.now(dt.timezone.utc).isoformat(),
            'revisions': revisions or {}, 'source_dirty': source_dirty or {},
            'schema_hash': digest(structural), 'stores': stores}


def compare(expected, actual):
    if expected['format_version'] != actual['format_version']:
        raise ValueError('Snapshot format versions differ')
    def flatten(data):
        result = {}
        for store in data['stores']:
            prefix = f'{store["service"]}/{store["store"]}'
            for table in store['tables']:
                key = f'{prefix}/{table["schema"]}.{table["name"]}'
                result[key] = {'comment': table['comment'], 'definition': table.get('definition')}
                result[key + '/pk'] = table['pk']
                for column in table['columns']:
                    result[key + '/columns/' + column['name']] = column
                for fk in table['fks']:
                    result[key + '/fks/' + fk['name']] = fk
                for index in table['indexes']:
                    result[key + '/indexes/' + index['name']] = index
                for check in table['checks']:
                    result[key + '/checks/' + check['name']] = check
                for trigger in table['triggers']:
                    result[key + '/triggers/' + trigger['name']] = trigger
            for row in store['metadata']:
                result[f'{prefix}/metadata/{row["kind"]}/{row["schema"]}.{row["name"]}'] = row
            for view in store['views']:
                result[f'{prefix}/view/{view["schema"]}.{view["name"]}'] = view
            result[f'{prefix}/migrations'] = store['migrations']
        return result
    before, after = flatten(expected), flatten(actual)
    return [{'object': name, 'change': 'added' if name not in before else
             'removed' if name not in after else 'changed'}
            for name in sorted(set(before) | set(after)) if before.get(name) != after.get(name)]


ROOT = Path(__file__).resolve().parents[1]
SERVICE_REPOS = {
    'Account Manager': 'rtk_account_manager', 'Billing': 'rtk_billing',
    'Video Cloud': 'rtk_video_cloud', 'Cloud Admin': 'rtk_cloud_admin',
    'Cloud Frontend': 'rtk_cloud_frontend',
}


def _run(args, *, cwd=ROOT, env=None):
    subprocess.run(args, cwd=cwd, env=env, check=True)


def generate(*, check=False):
    """Run production schema initializers against isolated databases."""
    with tempfile.TemporaryDirectory(prefix='rtk-er-schema-') as directory:
        scratch = Path(directory)
        container = 'rtk-er-schema-' + digest(str(scratch))[:12]
        _run(['docker', 'run', '--detach', '--rm', '--name', container,
              '--label', 'rtk.local-test=er-schema', '-e', 'POSTGRES_USER=postgres',
              '-e', 'POSTGRES_PASSWORD=local_test_only', '-e', 'POSTGRES_DB=postgres',
              '-p', '127.0.0.1::5432', 'postgres:16-alpine'])
        try:
            for _ in range(40):
                ready = subprocess.run(['docker', 'exec', container, 'pg_isready',
                                        '-U', 'postgres', '-d', 'postgres'],
                                       capture_output=True).returncode == 0
                if ready:
                    break
                time.sleep(.5)
            else:
                raise RuntimeError('Temporary PostgreSQL did not become ready')
            port_line = subprocess.check_output(['docker', 'port', container, '5432/tcp'], text=True)
            port = int(port_line.strip().rsplit(':', 1)[1])
            stores = []
            for service, dbname in [('Account Manager', 'er_account_manager'),
                                    ('Billing', 'er_billing'), ('Video Cloud', 'er_video_cloud')]:
                _run(['docker', 'exec', container, 'createdb', '-U', 'postgres', dbname])
                dsn = f'postgres://postgres:local_test_only@127.0.0.1:{port}/{dbname}?sslmode=disable'
                env = dict(os.environ, GOWORK='off', DATABASE_URL=dsn)
                _run(['go', 'run', './cmd/schema-init'], cwd=ROOT / 'repos' / SERVICE_REPOS[service], env=env)
                stores.append(inspect_postgres(dsn, service, 'primary'))
            admin = scratch / 'admin.sqlite'
            _run(['go', 'run', './cmd/schema-init', '--database', str(admin)],
                 cwd=ROOT / 'repos/rtk_cloud_admin', env=dict(os.environ, GOWORK='off'))
            stores.append(inspect_sqlite(admin, 'Cloud Admin', 'primary'))
            frontend = {name: scratch / (name + '.sqlite') for name in ('analytics', 'search', 'leads')}
            _run(['go', 'run', './cmd/schema-init',
                  *[arg for name, path in frontend.items() for arg in ('--' + name, str(path))]],
                 cwd=ROOT / 'repos/rtk_cloud_frontend', env=dict(os.environ, GOWORK='off'))
            stores.extend(inspect_sqlite(path, 'Cloud Frontend', name)
                          for name, path in frontend.items())
            revisions = {service: subprocess.check_output(
                ['git', '-C', str(ROOT / 'repos' / repo), 'rev-parse', '--short=12', 'HEAD'],
                text=True).strip() for service, repo in SERVICE_REPOS.items()}
            source_dirty = {service: bool(subprocess.check_output(
                ['git', '-C', str(ROOT / 'repos' / repo), 'status', '--porcelain'],
                text=True).strip()) for service, repo in SERVICE_REPOS.items()}
            result = snapshot(stores, revisions=revisions, source_dirty=source_dirty)
            problems = validate(result, strict=True)
            if problems:
                raise ValueError('\n'.join(problems))
            import generate_database_er_atlas as atlas
            published = ROOT / 'docs/design/database-schema.json'
            if check:
                baseline = json.loads(published.read_text())
                changes = compare(baseline, result)
                if (changes or baseline['schema_hash'] != result['schema_hash'] or
                        baseline.get('revisions') != result['revisions'] or
                        baseline.get('source_dirty', {}) != result['source_dirty']):
                    raise ValueError('Published schema snapshot differs: ' +
                                     ', '.join(change['object'] for change in changes[:12]))
            candidate = scratch / 'schema.json'
            candidate.write_text(json.dumps(result, indent=2, sort_keys=True) + '\n')
            atlas.SNAPSHOT = candidate
            atlas.OUT, atlas.INDEX = scratch / 'atlas.html', scratch / 'index.md'
            atlas.main()
            if check:
                if (atlas.OUT.read_bytes() != (ROOT / 'docs/design/database-er-atlas.html').read_bytes() or
                        atlas.INDEX.read_bytes() != (ROOT / 'docs/design/database-er-diagrams.md').read_bytes()):
                    raise ValueError('Published ER atlas or index needs regeneration')
                print('Database schema and ER documentation are current')
            else:
                shutil.copyfile(candidate, published)
                shutil.copyfile(atlas.OUT, ROOT / 'docs/design/database-er-atlas.html')
                shutil.copyfile(atlas.INDEX, ROOT / 'docs/design/database-er-diagrams.md')
                print('Published database snapshot and ER documentation')
            return 0
        finally:
            subprocess.run(['docker', 'rm', '--force', container],
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def _html_report(report, atlas_href):
    rows = []
    for change in report['changes']:
        object_name = change['object']
        parts = object_name.split('/')
        service = parts[0]
        if len(parts) >= 5 and parts[2] == 'metadata' and parts[3] in ('table', 'column'):
            object_part = parts[4].split('.', 1)[1] if '.' in parts[4] else ''
            table = object_part.split('.', 1)[0]
        elif len(parts) >= 3 and '.' in parts[2]:
            table = parts[2].split('.', 1)[1]
        else:
            table = ''
        if table == 'schema_metadata' and service == 'Cloud Frontend':
            table = parts[1] + '.' + table
        anchor = ('entity-' + '-'.join(''.join(c if c.isalnum() else '-' for c in part.lower()).strip('-')
                                        for part in (service, table))) if table else 'overall'
        rows.append(f'<tr><td>{html.escape(change["change"])}</td><td><a href="{html.escape(atlas_href)}#{anchor}">{html.escape(object_name)}</a></td></tr>')
    return ('<!doctype html><html lang="en"><meta charset="utf-8"><title>Database schema differences</title>'
            '<h1>Database schema differences</h1><p>Compare each environment with its deployed version.</p>'
            '<table><thead><tr><th>Change</th><th>Object</th></tr></thead><tbody>' +
            ''.join(rows) + '</tbody></table></html>\n')


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    capture = commands.add_parser('snapshot')
    capture.add_argument('--environment')
    capture.add_argument('--postgres', action='append', default=[], metavar='SERVICE:STORE:ENV_VAR')
    capture.add_argument('--sqlite', action='append', default=[], metavar='SERVICE:STORE:PATH')
    capture.add_argument('--revision', action='append', default=[], metavar='SERVICE=GIT_SHA')
    capture.add_argument('--output')
    capture.add_argument('--strict', action='store_true')
    diff = commands.add_parser('diff')
    diff.add_argument('--baseline', required=True)
    diff.add_argument('--actual', required=True)
    diff.add_argument('--output')
    diff.add_argument('--html-output')
    generate_parser = commands.add_parser('generate')
    generate_parser.add_argument('--check', action='store_true')
    args = parser.parse_args(argv)
    if args.command == 'generate':
        return generate(check=args.check)
    if args.command == 'snapshot':
        stores = []
        for item in args.postgres:
            service, name, env = item.split(':', 2)
            dsn = os.environ.get(env)
            if not dsn:
                parser.error(f'Connection environment variable {env} is empty')
            stores.append(inspect_postgres(dsn, service, name))
        for item in args.sqlite:
            service, name, path = item.split(':', 2)
            stores.append(inspect_sqlite(path, service, name))
        if not stores:
            parser.error('At least one --postgres or --sqlite target is required')
        revisions = dict(item.split('=', 1) for item in args.revision)
        result = snapshot(stores, environment=args.environment, revisions=revisions)
        problems = validate(result, strict=args.strict)
        if problems:
            raise ValueError('\n'.join(problems))
        output = args.output
        if not output:
            if not args.environment:
                parser.error('--output or --environment is required')
            if not re.fullmatch(r'[a-z0-9][a-z0-9-]*', args.environment):
                parser.error('--environment must contain only lowercase letters, digits, and hyphens')
            stamp = dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
            output = ROOT / 'cloud_env' / args.environment / 'runtime/artifacts/database' / stamp / 'schema.json'
        Path(output).parent.mkdir(parents=True, exist_ok=True)
        Path(output).write_text(json.dumps(result, indent=2, sort_keys=True) + '\n')
        print(f'{len(stores)} stores captured; schema hash {result["schema_hash"]}')
        return 0
    expected = json.loads(Path(args.baseline).read_text())
    actual = json.loads(Path(args.actual).read_text())
    changes = compare(expected, actual)
    report = {'baseline_hash': expected['schema_hash'], 'actual_hash': actual['schema_hash'],
              'baseline_revisions': expected.get('revisions', {}),
              'actual_revisions': actual.get('revisions', {}),
              'baseline_source_dirty': expected.get('source_dirty', {}),
              'changes': changes}
    report['version_match'] = (bool(actual.get('revisions')) and
                               expected.get('revisions') == actual.get('revisions') and
                               not any(expected.get('source_dirty', {}).values()))
    rendered = json.dumps(report, indent=2, sort_keys=True) + '\n'
    if args.output:
        Path(args.output).parent.mkdir(parents=True, exist_ok=True)
        Path(args.output).write_text(rendered)
    else:
        print(rendered, end='')
    if args.html_output:
        destination = Path(args.html_output)
        destination.parent.mkdir(parents=True, exist_ok=True)
        atlas_href = os.path.relpath(ROOT / 'docs/design/database-er-atlas.html', destination.parent)
        destination.write_text(_html_report(report, atlas_href))
    return 1 if changes else 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, ValueError, RuntimeError) as exc:
        print(f'database schema: {exc}', file=sys.stderr)
        sys.exit(2)
