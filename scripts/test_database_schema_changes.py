"""Exercise schema retirement against SQLite's catalog, not just parser expectations."""
import sqlite3
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import generate_database_er_atlas as atlas


class SchemaChangesTest(unittest.TestCase):
    def parse(self, sql, **kwargs):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'migration.sql').write_text(sql)
            with patch.object(atlas, 'ROOT', root):
                return atlas.parse_database(['migration.sql'], **kwargs)

    def test_retirement_and_renames_match_executed_sql(self):
        sql = '''CREATE TABLE parent (id INTEGER PRIMARY KEY, old TEXT NOT NULL UNIQUE, obsolete TEXT);
CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parent(id));
CREATE TABLE unused (id INTEGER PRIMARY KEY);
ALTER TABLE parent RENAME COLUMN old TO label;
ALTER TABLE parent DROP COLUMN obsolete;
ALTER TABLE parent RENAME TO owner;
DROP TABLE unused;'''
        parsed = self.parse(sql)
        db = sqlite3.connect(':memory:')
        self.addCleanup(db.close)
        db.executescript(sql)
        names = {row[0] for row in db.execute("SELECT name FROM sqlite_master WHERE type='table'")}
        self.assertEqual(names, set(parsed))
        for name in names:
            columns = db.execute(f'PRAGMA table_info({name})').fetchall()
            self.assertEqual([row[1] for row in columns], list(parsed[name]['columns']))
            self.assertEqual(tuple(row[1] for row in columns if row[5]), parsed[name]['pk'])
        self.assertEqual('owner', next(iter(parsed['child']['fks'].values()))['parent'])
        self.assertEqual('owner', db.execute('PRAGMA foreign_key_list(child)').fetchone()[2])

    def test_sqlite_text_primary_key_does_not_imply_not_null(self):
        sql = 'CREATE TABLE t (id TEXT PRIMARY KEY, serial INTEGER NOT NULL UNIQUE);'
        parsed = self.parse(sql, dialect='sqlite')['t']
        db = sqlite3.connect(':memory:')
        self.addCleanup(db.close)
        db.executescript(sql)
        db.execute('INSERT INTO t VALUES(NULL, 1)')
        self.assertFalse(parsed['columns']['id']['nn'])
        self.assertTrue(parsed['columns']['serial']['nn'])

    def test_unknown_create_and_drop_fail_explicitly(self):
        for sql in ['CREATE TABLE copied AS SELECT 1 AS id;', 'CREATE UNLOGGED TABLE t (id INTEGER);', 'DROP TABLE first, second;']:
            with self.subTest(sql=sql), self.assertRaisesRegex(ValueError, 'Unsupported DDL'):
                self.parse(sql)

    def test_unknown_alter_fails_with_source(self):
        with self.assertRaisesRegex(ValueError, 'Unsupported ALTER TABLE in migration.sql'):
            self.parse('CREATE TABLE t (id INTEGER PRIMARY KEY); ALTER TABLE t FROBULATE id;')

    def test_if_not_exists_does_not_overwrite_nullability(self):
        table = self.parse('CREATE TABLE t (id INTEGER NOT NULL); ALTER TABLE t ADD COLUMN IF NOT EXISTS id TEXT;')['t']
        self.assertEqual({'type': 'integer', 'nn': True}, table['columns']['id'])

    def test_unique_index_removal_changes_cardinality(self):
        tables = self.parse('''CREATE TABLE p (id INTEGER PRIMARY KEY);
CREATE TABLE c (id INTEGER PRIMARY KEY, pid INTEGER NOT NULL REFERENCES p(id));
CREATE UNIQUE INDEX c_pid ON c(pid);
DROP INDEX c_pid;''')
        self.assertFalse(next(iter(tables['c']['fks'].values()))['one'])

    def test_reset_helper_is_not_a_schema_migration(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            file = root / 'internal/postgres/postgres.go'
            file.parent.mkdir(parents=True)
            file.write_text('''package postgres
func EnsureSchema() { exec(`CREATE TABLE live (id INTEGER PRIMARY KEY);`) }
func Reset() { exec(`DROP TABLE live;`) }
''')
            with patch.object(atlas, 'ROOT', root):
                self.assertEqual({'live'}, set(atlas.parse_database(['internal/postgres/postgres.go'])))


if __name__ == '__main__':
    unittest.main()
