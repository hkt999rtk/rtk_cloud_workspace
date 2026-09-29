"""Behavior checks for initialized-catalog extraction and schema comparison."""
import json
import sqlite3
import tempfile
import unittest
from pathlib import Path

import database_schema_catalog as catalog


class CatalogTest(unittest.TestCase):
    def test_sqlite_capture_reads_keys_indexes_and_metadata_without_writing(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'schema.sqlite'
            connection = sqlite3.connect(path)
            connection.executescript('''
                CREATE TABLE parents(id TEXT PRIMARY KEY);
                CREATE TABLE children(id INTEGER PRIMARY KEY,
                                      parent_id TEXT REFERENCES parents(id) ON DELETE CASCADE);
                CREATE INDEX children_parent_idx ON children(parent_id);
                CREATE TABLE schema_metadata (
                  object_kind TEXT, schema_name TEXT, object_name TEXT,
                  meta_version INTEGER, details TEXT);
                INSERT INTO schema_metadata VALUES
                  ('group','main','Identity',1,'{"purpose":"Identity.","scenario":"Used for sign-in."}'),
                  ('table','main','parents',1,'{"group":"Identity","purpose":"Parents.","scenario":"Used for parents."}'),
                  ('table','main','children',1,'{"group":"Identity","purpose":"Children.","scenario":"Used for children."}'),
                  ('column','main','parents.id',1,'{"description":"Parent identifier."}'),
                  ('column','main','children.id',1,'{"description":"Child identifier."}'),
                  ('column','main','children.parent_id',1,'{"description":"References parents.id."}');
            ''')
            connection.close()
            before = path.read_bytes()
            store = catalog.inspect_sqlite(path, 'Example', 'main')
            self.assertEqual(before, path.read_bytes())
            self.assertEqual([], catalog.validate(catalog.snapshot([store]), strict=True))
            child = next(table for table in store['tables'] if table['name'] == 'children')
            self.assertEqual('parents', child['fks'][0]['parent'])
            self.assertEqual('CASCADE', child['fks'][0]['on_delete'])
            self.assertIn('children_parent_idx', {index['name'] for index in child['indexes']})

    def test_missing_metadata_and_changed_migration_are_reported(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'schema.sqlite'
            connection = sqlite3.connect(path)
            connection.execute('CREATE TABLE entries(id INTEGER PRIMARY KEY)')
            connection.commit()
            connection.close()
            store = catalog.inspect_sqlite(path, 'Example', 'main')
            expected = catalog.snapshot([store])
            self.assertIn('metadata unavailable', '\n'.join(catalog.validate(expected, strict=True)))
            modified = json.loads(json.dumps(store))
            modified['migrations'] = ['001_init']
            actual = catalog.snapshot([modified])
            self.assertEqual([{'object': 'Example/main/migrations', 'change': 'changed'}],
                             catalog.compare(expected, actual))
            self.assertEqual(expected['schema_hash'], actual['schema_hash'])

    def test_snapshot_hash_is_independent_of_capture_time(self):
        store = {'service': 'Example', 'store': 'main', 'dialect': 'sqlite',
                 'tables': [], 'views': [], 'metadata': [], 'migrations': []}
        first = catalog.snapshot([store])
        second = catalog.snapshot([store])
        self.assertEqual(first['schema_hash'], second['schema_hash'])
        second['captured_at'] = 'later'
        self.assertEqual([], catalog.compare(first, second))

    def test_diff_names_the_changed_column(self):
        store = {'service': 'Example', 'store': 'main', 'dialect': 'sqlite',
                 'tables': [{'schema': 'main', 'name': 'devices', 'comment': '',
                             'columns': [{'name': 'id', 'type': 'TEXT', 'not_null': True,
                                          'default': None, 'comment': ''}],
                             'pk': ['id'], 'fks': [], 'indexes': [], 'checks': [],
                             'triggers': []}],
                 'views': [], 'metadata': [], 'migrations': []}
        before = catalog.snapshot([store])
        changed = json.loads(json.dumps(store))
        changed['tables'][0]['columns'][0]['type'] = 'INTEGER'
        differences = catalog.compare(before, catalog.snapshot([changed]))
        self.assertEqual([{'object': 'Example/main/main.devices/columns/id',
                           'change': 'changed'}], differences)

    def test_diff_cli_writes_linked_report_and_returns_difference_status(self):
        store = {'service': 'Example', 'store': 'main', 'dialect': 'sqlite',
                 'tables': [], 'views': [], 'metadata': [], 'migrations': []}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            expected = root / 'expected.json'
            actual = root / 'actual.json'
            expected.write_text(json.dumps(catalog.snapshot([store])))
            changed = json.loads(json.dumps(store))
            changed['migrations'] = ['001']
            actual.write_text(json.dumps(catalog.snapshot([changed])))
            report = root / 'nested' / 'diff.json'
            page = root / 'nested' / 'diff.html'
            status = catalog.main(['diff', '--baseline', str(expected), '--actual', str(actual),
                                   '--output', str(report), '--html-output', str(page)])
            self.assertEqual(1, status)
            self.assertIn('Example/main/migrations', report.read_text())
            self.assertIn('database-er-atlas.html#overall', page.read_text())


if __name__ == '__main__':
    unittest.main()
