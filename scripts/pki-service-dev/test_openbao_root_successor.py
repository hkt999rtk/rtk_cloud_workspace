import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location(
    'openbao_root_successor', Path(__file__).with_name('openbao_root_successor.py'))
o = importlib.util.module_from_spec(spec)
spec.loader.exec_module(o)


class OpenBaoRootSuccessorTests(unittest.TestCase):
    def root(self, **changes):
        value = {'environment': 'dev', 'trust_domain': 'openbao_tls',
                 'kind': 'root', 'status': 'active'}
        value.update(changes)
        return value

    def test_root_scope_is_dev_openbao_tls_only(self):
        o.valid_root(self.root(), 'active')
        for field, value in [('environment', 'staging'),
                             ('trust_domain', 'service'),
                             ('kind', 'intermediate'),
                             ('status', 'ready')]:
            with self.assertRaises(RuntimeError):
                o.valid_root(self.root(**{field: value}), 'active')

    def test_predecessor_requires_exactly_one_active_root(self):
        runner = object.__new__(o.OpenBaoRootSuccessor)
        runner.roots = lambda: [self.root(issuer_id='old')]
        self.assertEqual(runner.predecessor()['issuer_id'], 'old')
        for roots in ([], [self.root(), self.root(issuer_id='other')]):
            runner.roots = lambda roots=roots: roots
            with self.assertRaises(RuntimeError):
                runner.predecessor()


if __name__ == '__main__':
    unittest.main()
