import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location(
    'app_hierarchy', Path(__file__).with_name('app_hierarchy.py'))
a = importlib.util.module_from_spec(spec)
spec.loader.exec_module(a)


class AppManifestTests(unittest.TestCase):
    def issuer(self, issuer_id, kind, parent=''):
        return {
            'issuer_id': issuer_id, 'kind': kind,
            'parent_issuer_id': parent, 'environment': 'dev',
            'trust_domain': 'app', 'certificate_pem': issuer_id + '\n',
            'trust_bundle_version': issuer_id * 64}

    def test_manifest_keeps_two_independent_lineages(self):
        first = self.issuer('a', 'root')
        old = self.issuer('b', 'intermediate', 'a')
        second = self.issuer('c', 'root')
        successor = self.issuer('d', 'intermediate', 'c')

        result = a.app_manifest([first, old, second, successor])

        self.assertEqual(result['roots.pem'], 'a\nc\n')
        self.assertIn('/run/pki-state/app/b-crl.json', result['crls.json'])
        self.assertIn('/run/pki-state/app/d-crl.json', result['crls.json'])

    def test_manifest_rejects_cross_lineage_intermediate(self):
        root = self.issuer('a', 'root')
        foreign = self.issuer('b', 'intermediate', 'missing')
        with self.assertRaises(RuntimeError):
            a.app_manifest([root, foreign])


if __name__ == '__main__':
    unittest.main()
