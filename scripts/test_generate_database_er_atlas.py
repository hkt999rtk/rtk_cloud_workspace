"""Navigation and coverage checks for the static ER atlas."""
import unittest
from collections import Counter
from html import escape
from html.parser import HTMLParser
import re
import tempfile
from pathlib import Path
from unittest.mock import patch

import generate_database_er_atlas as atlas
from database_er_narratives_en import ENTITY_NOTES, GROUPS


class AtlasHTML(HTMLParser):
    def __init__(self):
        super().__init__()
        self.ids = []
        self.fragments = []
        self.search_items = []

    def handle_starttag(self, tag, attributes):
        attrs = dict(attributes)
        if 'id' in attrs:
            self.ids.append(attrs['id'])
        if attrs.get('href', '').startswith('#'):
            self.fragments.append(attrs['href'][1:])
        if 'search-item' in attrs.get('class', '').split():
            self.search_items.append(attrs.get('href'))


class AtlasGenerationTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.databases = {db: atlas.parse_database(paths) for db, paths in atlas.SOURCES.items()}
        cls.page = atlas.OUT.read_text()
        cls.html = AtlasHTML()
        cls.html.feed(cls.page)

    def test_every_entity_and_fk_has_unique_reachable_anchor(self):
        ids = set(self.html.ids)
        self.assertEqual(len(ids), len(self.html.ids), [name for name, n in Counter(self.html.ids).items() if n > 1])
        self.assertFalse(set(self.html.fragments) - ids)
        entities = [atlas.entity_id(db, table) for db, tables in self.databases.items() for table in tables]
        fks = [atlas.relation_id(db, fk) for db, tables in self.databases.items()
               for table in tables.values() for fk in table['fks'].values()]
        self.assertEqual(sum(len(tables) for tables in self.databases.values()), len(entities))
        self.assertTrue(set(entities + fks) <= ids)
        self.assertEqual(set('#' + name for name in entities + fks), set(self.html.search_items))
        self.assertTrue(set(entities + fks) <= set(self.html.fragments))

    def test_logical_mappings_are_sourced_and_not_fk(self):
        for key, left_db, left_table, left_col, right_db, right_table, right_col, source, _ in atlas.LOGICAL_LINKS:
            self.assertIn('logical-' + key, self.html.ids)
            self.assertTrue((atlas.ROOT / source).is_file())
            self.assertIn(left_col, self.databases[left_db][left_table]['columns'])
            self.assertIn(right_col, self.databases[right_db][right_table]['columns'])
            self.assertIn('../../' + source, self.page)
        self.assertIn('Logical reference, not a database FK.', self.page)

    def test_bounded_groups_cover_every_declared_fk(self):
        atlas.validate_narratives(self.databases)
        for db, tables in self.databases.items():
            groups = atlas.relationship_groups(db, tables)
            group_targets = re.findall(r'href="#(relation-[^"]+)"', groups)
            expected = [atlas.relation_id(db, fk) for table in tables.values() for fk in table['fks'].values()]
            self.assertEqual(Counter(expected), Counter(group_targets), db)
            members = [name for _, _, _, names in GROUPS[db] for name in names.split()]
            self.assertEqual(set(tables), set(members), db)
            self.assertEqual(len(members), len(set(members)), db)
            self.assertEqual(set(tables), set(ENTITY_NOTES[db]), db)
            for number, (title, purpose, scenario, _) in enumerate(GROUPS[db], 1):
                group_id = f'group-{atlas.slug(db)}-{number}'
                self.assertIn(group_id, self.html.ids)
                self.assertIn(title, self.page)
                self.assertIn(purpose, self.page)
                self.assertIn(scenario, self.page)
            for name, (purpose, scenario) in ENTITY_NOTES[db].items():
                self.assertIn(escape(purpose), self.page, name)
                self.assertIn(escape(scenario), self.page, name)

    def test_every_fk_names_both_entities_and_has_reverse_link(self):
        for db, tables in self.databases.items():
            page = atlas.catalog(db, tables, {name: [] for name in tables})
            for table in tables.values():
                for fk in table['fks'].values():
                    child = atlas.entity_id(db, fk['child'])
                    parent = atlas.entity_id(db, fk['parent'])
                    relation = atlas.relation_id(db, fk)
                    self.assertIn(f'Referenced by <a href="#{child}">{fk["child"]}</a> via <a href="#{relation}">{fk["constraint"]}</a>', page)
                    self.assertIn(f'<a href="#{relation}">{fk["constraint"]}</a> references <a href="#{parent}">{fk["parent"]}</a>', page)
                    detail = self.page.split(f'id="{relation}" tabindex="-1">', 1)[1].split('</li>', 1)[0]
                    self.assertIn('Referencing entity:', detail)
                    self.assertIn('referenced entity:', detail)
                    self.assertIn(f'href="#{child}"', detail)
                    self.assertIn(f'href="#{parent}"', detail)

    def test_regeneration_is_stable(self):
        # Never rewrite published documentation as a test side effect.
        with tempfile.TemporaryDirectory() as directory:
            out, index = Path(directory) / 'atlas.html', Path(directory) / 'index.md'
            by_paths = {tuple(paths): self.databases[name] for name, paths in atlas.SOURCES.items()}
            with patch.object(atlas, 'OUT', out), patch.object(atlas, 'INDEX', index), patch.object(atlas, 'parse_database', side_effect=lambda paths: by_paths[tuple(paths)]):
                atlas.main()
                first = out.read_bytes(), index.read_bytes()
                atlas.main()
                self.assertEqual(first, (out.read_bytes(), index.read_bytes()))

    def test_readable_content_is_english(self):
        for path in (atlas.OUT, atlas.INDEX, atlas.ROOT / 'scripts/database_er_narratives_en.py'):
            self.assertNotRegex(path.read_text(), r'[\u3400-\u9fff]', path.as_posix())


if __name__ == '__main__':
    unittest.main()
