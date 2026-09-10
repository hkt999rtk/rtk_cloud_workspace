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

    def test_intermediate_policy_uses_only_live_openbao_origins(self):
        request = o.intermediate_request(self.root(
            status='active', issuer_id='root-1'))
        self.assertEqual(request['trust_domain'], 'openbao_tls')
        self.assertEqual(request['service_client_ids'], ['service:openbao'])
        self.assertEqual(request['server_dns_names'], [
            'openbao.video-cloud-dev-secrets.svc',
            'openbao.video-cloud-dev-secrets.svc.cluster.local'])
        self.assertEqual(request['parent_issuer_id'], 'root-1')

    def test_intermediate_recovery_is_separate_from_root_reconcile(self):
        self.assertTrue(callable(o.OpenBaoAuthorityRun.resume_intermediate))


if __name__ == '__main__':
    unittest.main()
