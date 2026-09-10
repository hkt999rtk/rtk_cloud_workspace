import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location(
    'openbao_service_authority',
    Path(__file__).with_name('openbao_service_authority.py'))
o = importlib.util.module_from_spec(spec)
spec.loader.exec_module(o)


class OpenBaoServiceAuthorityTests(unittest.TestCase):
    def test_v5_adds_only_openbao_client(self):
        self.assertEqual(o.V5_CLIENT_IDS,
                         sorted(o.p.V4_CLIENT_IDS + ['service:openbao']))
        self.assertEqual(o.OpenBaoServiceAuthority.dns_names,
                         o.p.V4_DNS_NAMES)
        self.assertEqual(o.OpenBaoServiceAuthority.predecessor_versions,
                         (1, 2, 3, 4))


if __name__ == '__main__':
    unittest.main()
