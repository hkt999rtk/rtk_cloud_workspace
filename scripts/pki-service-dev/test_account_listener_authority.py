import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location(
    'account_listener_authority', Path(__file__).with_name('account_listener_authority.py'))
a = importlib.util.module_from_spec(spec)
spec.loader.exec_module(a)


class AccountListenerAuthorityTest(unittest.TestCase):
    def root(self):
        return {'issuer_id': 'root', 'environment': 'dev', 'trust_domain': 'service',
                'kind': 'root', 'status': 'active', 'trust_bundle_version': 'r'}

    def issuers(self):
        common = {'environment': 'dev', 'trust_domain': 'service',
                  'kind': 'intermediate', 'parent_issuer_id': 'root'}
        return [dict(common, issuer_id='v1', issuer_version=1, status='retiring',
                     service_client_ids=list(a.V1_CLIENT_IDS),
                     server_dns_names=list(a.V2_DNS_NAMES), trust_bundle_version='1'),
                dict(common, issuer_id='v2', issuer_version=2, status='active',
                     service_client_ids=list(a.V2_CLIENT_IDS),
                     server_dns_names=list(a.V2_DNS_NAMES), trust_bundle_version='2')]

    def test_v3_adds_only_api_caller_and_account_dns(self):
        request = a.v3_request(self.root())
        self.assertEqual(request['service_client_ids'],
                         a.V2_CLIENT_IDS + ['service:video-cloud-api'])
        self.assertEqual(set(request['server_dns_names']) - set(a.V2_DNS_NAMES),
                         {a.ACCOUNT_DNS})
        request['service_client_ids'].append('service:other')
        self.assertNotIn('service:other', a.v3_request(self.root())['service_client_ids'])

    def test_predecessors_require_exact_v1_v2_transition(self):
        issuers = self.issuers()
        self.assertEqual(a.select_predecessors(issuers, self.root()), issuers)
        with self.assertRaisesRegex(RuntimeError, 'v2 policy differs'):
            a.select_predecessors([issuers[0], dict(issuers[1],
                                                   service_client_ids=['service:other'])],
                                  self.root())
        with self.assertRaisesRegex(RuntimeError, 'successor already exists'):
            a.select_predecessors(issuers + [dict(issuers[1], issuer_id='v3',
                                                  issuer_version=3, status='ready')],
                                  self.root())
        self.assertEqual(a.select_predecessors(
            issuers + [dict(issuers[1], issuer_id='v3', issuer_version=3, status='ready')],
            self.root(), 'v3'), issuers)

    def test_bundle_retains_root_and_both_predecessors(self):
        successor = dict(self.issuers()[1], issuer_id='v3', issuer_version=3,
                         trust_bundle_version='3')
        refs = a.bundle_references(self.root(), self.issuers(), successor)
        self.assertEqual([ref['issuer_id'] for ref in refs], ['root', 'v1', 'v2', 'v3'])


if __name__ == '__main__':
    unittest.main()
