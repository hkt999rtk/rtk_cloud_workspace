import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location(
    'openbao_authority', Path(__file__).with_name('openbao_authority.py'))
o = importlib.util.module_from_spec(spec)
spec.loader.exec_module(o)


class OpenBaoAuthorityTests(unittest.TestCase):
    def root(self, **changes):
        value = {'environment': 'dev', 'trust_domain': 'openbao_tls',
                 'kind': 'root', 'status': 'ready'}
        value.update(changes)
        return value

    def test_root_scope_is_independent_and_dev_only(self):
        o.validate_root(self.root(), 'ready')
        for field, value in [('environment', 'staging'),
                             ('trust_domain', 'service'),
                             ('kind', 'intermediate'),
                             ('status', 'active')]:
            with self.assertRaisesRegex(
                    RuntimeError, 'independent dev OpenBao TLS Root differs'):
                o.validate_root(self.root(**{field: value}), 'ready')


if __name__ == '__main__':
    unittest.main()
