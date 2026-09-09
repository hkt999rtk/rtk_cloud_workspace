import importlib.util
from pathlib import Path
import unittest
from unittest.mock import Mock


spec = importlib.util.spec_from_file_location('factory_adoption', Path(__file__).with_name('factory_adoption.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class FactoryAdoptionTests(unittest.TestCase):
    def test_v2_request_adds_only_factory_identity(self):
        root = {'environment': 'dev', 'trust_domain': 'service', 'kind': 'root',
                'status': 'active', 'issuer_id': 'root'}
        request = m.v2_request(root)
        self.assertEqual(request['service_client_ids'], m.V2_CLIENT_IDS)
        self.assertEqual(request['server_dns_names'], m.r.SERVICE_DNS_NAMES)
        request['service_client_ids'].append('service:other')
        self.assertNotIn('service:other', m.v2_request(root)['service_client_ids'])

    def test_select_v1_requires_exact_active_policy_and_no_transition(self):
        root = {'issuer_id': 'root'}
        issuer = {'environment': 'dev', 'trust_domain': 'service', 'kind': 'intermediate',
                  'status': 'active', 'issuer_id': 'v1', 'parent_issuer_id': 'root',
                  'service_client_ids': list(m.OLD_CLIENT_IDS),
                  'server_dns_names': list(m.r.SERVICE_DNS_NAMES)}
        self.assertEqual(m.select_v1([issuer], root), issuer)
        changed = dict(issuer, service_client_ids=m.V2_CLIENT_IDS)
        with self.assertRaisesRegex(RuntimeError, 'v1 policy differs'):
            m.select_v1([changed], root)
        pending = dict(issuer, status='ready', issuer_id='v2')
        with self.assertRaisesRegex(RuntimeError, 'transition already exists'):
            m.select_v1([issuer, pending], root)

    def test_intermediate_approval_uses_one_independent_admin(self):
        runner = object.__new__(m.FactoryAdoption)
        operation = {'operation_id': 'operation', 'request_sha256': 'a' * 64}
        approved = dict(operation, issuer_id='issuer', status='approved')
        runner.api = Mock(side_effect=[None, approved])
        self.assertEqual(runner.approve_intermediate(operation), approved)
        self.assertEqual(runner.api.call_args_list[0].args,
                         ('/operations/operation/approvals',
                          {'request_sha256': 'a' * 64, 'role': 'pki_admin'},
                          204, 'approver'))
        self.assertEqual(runner.api.call_count, 2)


if __name__ == '__main__':
    unittest.main()
