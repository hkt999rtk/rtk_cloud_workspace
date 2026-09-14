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

    def test_server_only_successor_has_no_service_client_identity(self):
        request = o.intermediate_request(
            self.root(status='active', issuer_id='root-1'), [])
        self.assertEqual(request['service_client_ids'], [])
        self.assertEqual(request['server_dns_names'], o.OPENBAO_DNS_NAMES)

    def test_server_only_successor_requires_active_server_predecessor(self):
        root = self.root(status='active', issuer_id='root-2')
        v2 = {'environment': 'dev', 'trust_domain': 'openbao_tls',
              'kind': 'intermediate', 'parent_issuer_id': 'root-1',
              'service_client_ids': [], 'server_dns_names': o.OPENBAO_DNS_NAMES,
              'issuer_id': 'v2', 'issuer_version': 2, 'status': 'active'}
        self.assertEqual(o.select_server_predecessor([v2], root), v2)
        for changed in (
                dict(v2, status='retiring'),
                dict(v2, issuer_version=1),
                dict(v2, service_client_ids=o.OPENBAO_CLIENT_IDS),
                dict(v2, parent_issuer_id='root-2')):
            with self.assertRaises(RuntimeError):
                o.select_server_predecessor([changed], root)
        v3 = dict(o.intermediate_request(root, []), issuer_id='v3',
                  issuer_version=3, status='ready')
        with self.assertRaisesRegex(RuntimeError, 'server successor already exists'):
            o.select_server_predecessor([v2, v3], root)

    def test_intermediate_inventory_hydrates_search_summaries(self):
        runner = object.__new__(o.OpenBaoAuthorityRun)
        calls = []

        def api(path, query=None):
            calls.append(path)
            if path == '/issuers/search':
                return {'items': [{
                    'issuer_id': 'issuer-v1', 'environment': 'dev',
                    'trust_domain': 'openbao_tls', 'kind': 'intermediate'}]}
            return {'issuer_id': 'issuer-v1',
                    'service_client_ids': ['service:openbao']}

        runner.api = api
        self.assertEqual(runner.intermediates()[0]['service_client_ids'],
                         ['service:openbao'])
        self.assertEqual(calls, ['/issuers/search', '/issuers/issuer-v1'])

    def test_intermediate_recovery_is_separate_from_root_reconcile(self):
        self.assertTrue(callable(o.OpenBaoAuthorityRun.resume_intermediate))


if __name__ == '__main__':
    unittest.main()
