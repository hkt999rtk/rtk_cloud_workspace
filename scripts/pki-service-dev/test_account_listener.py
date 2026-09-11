import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location('account_listener', Path(__file__).with_name('account_listener.py'))
listener = importlib.util.module_from_spec(spec)
spec.loader.exec_module(listener)

life_spec = importlib.util.spec_from_file_location('account_listener_lifecycle', Path(__file__).with_name('account_listener_lifecycle.py'))
lifecycle = importlib.util.module_from_spec(life_spec)
life_spec.loader.exec_module(lifecycle)


class AccountListenerTests(unittest.TestCase):
    def owner(self):
        return {'spec': {'template': {'spec': {
            'containers': [{'name': 'app'}, {'name': 'pkimanagement', 'image': 'old', 'env': [], 'volumeMounts': []}],
            'volumes': []}}}}

    def test_listener_template_has_separate_state_and_namespace_local_manifests(self):
        result = listener.listener_template(self.owner(), 'image', 'a' * 64)['spec']
        sidecar = result['containers'][1]
        env = {item['name']: item['value'] for item in sidecar['env']}
        self.assertEqual(env['PKI_MANAGEMENT_ACCOUNT_HOST_IDENTITY_STATE'], listener.STATE)
        self.assertNotEqual(env['PKI_MANAGEMENT_ACCOUNT_HOST_IDENTITY_STATE'], listener.mg.STATE)
        self.assertEqual(env['PKI_MANAGEMENT_ACCOUNT_HOST_NAME'], listener.ACCOUNT_DNS)
        self.assertEqual(sidecar['ports'], [{'name': 'account-service', 'containerPort': 8443, 'protocol': 'TCP'}])
        self.assertEqual({v['name'] for v in result['volumes']}, {'account-service-bundles', 'account-service-crls'})

    def test_seed_removal_keeps_all_managed_listener_settings(self):
        seeded = listener.listener_template(self.owner(), 'image', 'a' * 64)
        env = {item['name'] for item in listener.without_seed(seeded)['spec']['containers'][1]['env']}
        self.assertNotIn('PKI_MANAGEMENT_ACCOUNT_HOST_SEED_CERT', env)
        self.assertNotIn('PKI_MANAGEMENT_ACCOUNT_HOST_SEED_KEY', env)
        self.assertIn('PKI_MANAGEMENT_ACCOUNT_HOST_IDENTITY_STATE', env)
        self.assertIn('PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_BUNDLE_MANIFEST', env)

    def test_crl_paths_are_private_to_account_manager_owner(self):
        issuer = {'issuer_id': 'id', 'trust_domain': 'service', 'environment': 'dev', 'status': 'active'}
        self.assertEqual(listener.account_crls([{'issuer': issuer}])[0]['state_path'],
                         '/var/lib/account-pki/private/account-listener-crl-id.json')
        issuer['status'] = 'ready'
        with self.assertRaises(RuntimeError):
            listener.account_crls([{'issuer': issuer}])

    def test_lifecycle_selects_the_current_service_issuer_not_a_historical_version(self):
        active = {'environment': 'dev', 'trust_domain': 'service', 'kind': 'intermediate', 'status': 'active',
                  'issuer_id': 'v5', 'service_client_ids': ['service:account-manager'],
                  'server_dns_names': [listener.ACCOUNT_DNS]}
        retiring = dict(active, issuer_id='v3', status='retiring')
        self.assertEqual(lifecycle.current_service_issuer([retiring, active]), active)
        with self.assertRaisesRegex(RuntimeError, 'one active Service intermediate'):
            lifecycle.current_service_issuer([active, dict(active, issuer_id='v6')])
        with self.assertRaisesRegex(RuntimeError, 'does not authorize Account Manager'):
            lifecycle.current_service_issuer([dict(active, service_client_ids=[])])

    def test_consumer_crl_entries_retain_all_service_issuers_with_consumer_state_paths(self):
        issuer = {'issuer_id': 'a' * 8 + '-aaaa-aaaa-aaaa-' + 'a' * 12,
                  'environment': 'dev', 'trust_domain': 'service', 'status': 'retiring'}
        result = lifecycle.consumer_crl_entries([{'issuer': issuer}], '/state/crl-{issuer_id}.json')
        self.assertEqual(result, [{'issuer': issuer, 'state_path': '/state/crl-' + issuer['issuer_id'] + '.json'}])
        with self.assertRaisesRegex(RuntimeError, 'invalid issuer'):
            lifecycle.consumer_crl_entries([{'issuer': dict(issuer, trust_domain='device')}], '/state/{issuer_id}')

    def test_lifecycle_revokes_a_predecessor_through_its_own_issuer(self):
        runner = lifecycle.AccountListenerLifecycle.__new__(lifecycle.AccountListenerLifecycle)
        calls = []

        def api(path, body=None, role='requester'):
            calls.append((path, body, role))
            if path.endswith('/crl'):
                return {'crl_sha256': 'old-crl'}
            return {'crl_sha256': 'new-crl'}

        runner.api = api
        runner.save = lambda *_: None
        runner.wait_receipts = lambda *args: calls.append(('wait', args, None))
        runner.sync_service_crl_consumers = lambda issuer_id: calls.append(('sync', issuer_id, None))
        predecessor = {'fingerprint': 'a' * 64, 'issuer_id': 'retiring-service-v3', 'revoked_at': None}
        runner.revoke_and_publish('client', [predecessor], {}, predecessor)
        paths = [call[0] for call in calls if call[0] not in ('wait', 'sync')]
        self.assertEqual(paths, [
            '/issuers/retiring-service-v3/crl',
            '/issuers/retiring-service-v3/revoke-service-client',
            '/issuers/retiring-service-v3/publish-service-client-revocation',
            '/issuers/retiring-service-v3/finalize-service-client-revocation',
        ])

    def test_lifecycle_replays_only_publication_for_an_already_revoked_predecessor(self):
        runner = lifecycle.AccountListenerLifecycle.__new__(lifecycle.AccountListenerLifecycle)
        paths = []
        runner.api = lambda path, *args, **kwargs: (paths.append(path) or {'crl_sha256': 'crl'})
        runner.save = lambda *_: None
        runner.wait_receipts = lambda *_: None
        runner.sync_service_crl_consumers = lambda *_: None
        predecessor = {'fingerprint': 'b' * 64, 'issuer_id': 'retiring-service-v3', 'revoked_at': 'already'}
        runner.revoke_and_publish('client', [predecessor], {}, predecessor)
        self.assertEqual(paths, [
            '/issuers/retiring-service-v3/crl',
            '/issuers/retiring-service-v3/publish-service-client-revocation',
            '/issuers/retiring-service-v3/finalize-service-client-revocation',
        ])


if __name__ == '__main__':
    unittest.main()
