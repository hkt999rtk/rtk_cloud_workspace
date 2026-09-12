import importlib.util
import copy
import json
from pathlib import Path
import unittest
from unittest.mock import Mock, patch


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

    def test_consumer_sync_reconciles_rollouts_after_configmap_only_interruption(self):
        runner = lifecycle.AccountListenerLifecycle.__new__(lifecycle.AccountListenerLifecycle)
        runner.output = Path('/tmp/t11-consumer-sync')
        issuer = {'issuer_id': 'a' * 8 + '-aaaa-aaaa-aaaa-' + 'a' * 12,
                  'environment': 'dev', 'trust_domain': 'service', 'status': 'active'}
        objects = {
            ('configmap', 'pki-service-client-crls'): {'metadata': {'name': 'pki-service-client-crls'},
                'data': {'crls.json': json.dumps([{'issuer': issuer}])}},
            ('configmap', 'factoryenroll-service-crls'): {'metadata': {'name': 'factoryenroll-service-crls'}, 'data': {'crls.json': '[]'}},
            ('configmap', 'video-cloud-api-pki-trust'): {'metadata': {'name': 'video-cloud-api-pki-trust'}, 'data': {'service-crls.json': '[]'}},
            ('configmap', 'account-manager-service-crls-old'): {'metadata': {'name': 'account-manager-service-crls-old'},
                'immutable': True, 'data': {'crls.json': '[]'}},
        }
        for name in ('factoryenroll', 'video-cloud-api-pki'):
            objects[('deployment', name)] = {'metadata': {'name': name}, 'spec': {'replicas': 1,
                'template': {'metadata': {'annotations': {}}, 'spec': {}}}, 'status': {'readyReplicas': 1}}
        objects[('deployment', 'account-manager')] = {
            'metadata': {'name': 'account-manager'},
            'spec': {'replicas': 1, 'template': {
                'metadata': {'annotations': {}},
                'spec': {'volumes': [
                    {'name': 'account-service-crls', 'configMap': {'name': 'account-manager-service-crls-old'}},
                ]},
            }},
            'status': {'readyReplicas': 1},
        }
        interrupted = [True]
        rollouts = []

        def obj(kind, name, namespace=None):
            if kind == 'deployment' and interrupted[0]:
                interrupted[0] = False
                raise RuntimeError('interrupted after ConfigMap updates')
            return copy.deepcopy(objects[(kind, name)])

        def patch(kind, before, changes):
            current = objects[(kind, before['metadata']['name'])]
            for change in changes:
                if change['op'] != 'replace':
                    continue
                if kind == 'configmap':
                    current['data'][change['path'].split('/')[-1]] = change['value']
                else:
                    current['spec']['template'] = change['value']

        def create(value):
            objects[('configmap', value['metadata']['name'])] = copy.deepcopy(value)

        runner.obj = obj
        runner.scoped_patch = patch
        runner.create = create
        runner.save = lambda *_: None
        def kube(args, **_):
            if args[:3] == ['-n', listener.AM_NS, 'get']:
                name = args[4]
                value = objects.get(('configmap', name))
                return '' if value is None else json.dumps(value)
            rollouts.append(args)
            return ''
        runner.kube = kube
        with self.assertRaisesRegex(RuntimeError, 'interrupted after ConfigMap updates'):
            runner.sync_service_crl_consumers(issuer['issuer_id'])
        runner.sync_service_crl_consumers(issuer['issuer_id'])
        self.assertEqual(len(rollouts), 3)
        for name in ('factoryenroll', 'video-cloud-api-pki', 'account-manager'):
            annotations = objects[('deployment', name)]['spec']['template']['metadata']['annotations']
            self.assertIn(lifecycle.CONSUMER_MANIFEST_ANNOTATION, annotations)
        selected = objects[('deployment', 'account-manager')]['spec']['template']['spec']['volumes'][0]
        self.assertNotEqual(selected['configMap']['name'], 'account-manager-service-crls-old')

    def test_consumer_sync_leaves_manifests_unchanged_when_a_target_is_unready(self):
        runner = lifecycle.AccountListenerLifecycle.__new__(lifecycle.AccountListenerLifecycle)
        issuer = {'issuer_id': 'b' * 8 + '-bbbb-bbbb-bbbb-' + 'b' * 12,
                  'environment': 'dev', 'trust_domain': 'service', 'status': 'active'}
        objects = {
            ('configmap', 'pki-service-client-crls'): {'metadata': {'name': 'pki-service-client-crls'},
                'data': {'crls.json': json.dumps([{'issuer': issuer}])}},
            ('configmap', 'factoryenroll-service-crls'): {'metadata': {'name': 'factoryenroll-service-crls'}, 'data': {'crls.json': 'old'}},
            ('configmap', 'video-cloud-api-pki-trust'): {'metadata': {'name': 'video-cloud-api-pki-trust'}, 'data': {'service-crls.json': 'old'}},
        }
        for name, ready in (('factoryenroll', 1), ('video-cloud-api-pki', 0)):
            objects[('deployment', name)] = {'metadata': {'name': name, 'generation': 1}, 'spec': {'replicas': 1,
                'template': {'metadata': {'annotations': {}}, 'spec': {}}},
                'status': {'readyReplicas': ready, 'observedGeneration': 1}}
        objects[('deployment', 'account-manager')] = {'metadata': {'name': 'account-manager', 'generation': 1},
            'spec': {'replicas': 1, 'template': {'metadata': {'annotations': {}}, 'spec': {'volumes': [
                {'name': 'account-service-crls', 'configMap': {'name': 'account-manager-service-crls-old'}},
            ]}}}, 'status': {'readyReplicas': 1, 'observedGeneration': 1}}
        runner.obj = lambda kind, name, namespace=None: copy.deepcopy(objects[(kind, name)])
        patches = []
        runner.scoped_patch = lambda *args: patches.append(args)
        runner.create = lambda *_: self.fail('unready preflight must not create a ConfigMap')
        with self.assertRaisesRegex(RuntimeError, 'video-cloud-api-pki'):
            runner.sync_service_crl_consumers()
        self.assertEqual(patches, [])
        self.assertEqual(objects[('configmap', 'factoryenroll-service-crls')]['data']['crls.json'], 'old')

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
        runner.require_service_crl_consumers_current = lambda issuer_id: calls.append(('current', issuer_id, None))
        predecessor = {'fingerprint': 'a' * 64, 'issuer_id': 'retiring-service-v3', 'revoked_at': None}
        runner.revoke_and_publish('client', [predecessor], {}, predecessor)
        paths = [call[0] for call in calls if call[0] not in ('wait', 'current')]
        self.assertEqual(paths, [
            '/issuers/retiring-service-v3/crl',
            '/issuers/retiring-service-v3/revoke-service-client',
            '/issuers/retiring-service-v3/publish-service-client-revocation',
            '/issuers/retiring-service-v3/finalize-service-client-revocation',
        ])
        self.assertIn(('current', 'retiring-service-v3', None), calls)

    def test_lifecycle_reconciles_consumers_before_opening_held_sessions(self):
        runner = lifecycle.AccountListenerLifecycle.__new__(lifecycle.AccountListenerLifecycle)
        calls = []
        runner.preflight_lifecycle = lambda: calls.append('preflight')
        runner.active_root = lambda: {'certificate_pem': 'root'}
        runner.save = lambda *_: None
        runner.v3 = lambda: {'issuer_id': 'active'}

        def reconciled():
            calls.append('reconcile')
            raise RuntimeError('stop after reconciliation')

        runner.sync_service_crl_consumers = reconciled
        runner.forward = lambda *_: self.fail('held-session setup began before consumer reconciliation')
        runner.install_probe = lambda: self.fail('probe installed before consumer reconciliation')
        with self.assertRaisesRegex(RuntimeError, 'stop after reconciliation'):
            runner.lifecycle()
        self.assertEqual(calls, ['preflight', 'reconcile'])

    def test_lifecycle_replays_only_publication_for_an_already_revoked_predecessor(self):
        runner = lifecycle.AccountListenerLifecycle.__new__(lifecycle.AccountListenerLifecycle)
        paths = []
        runner.api = lambda path, *args, **kwargs: (paths.append(path) or {'crl_sha256': 'crl'})
        runner.save = lambda *_: None
        runner.wait_receipts = lambda *_: None
        runner.require_service_crl_consumers_current = lambda *_: None
        predecessor = {'fingerprint': 'b' * 64, 'issuer_id': 'retiring-service-v3', 'revoked_at': 'already'}
        runner.revoke_and_publish('client', [predecessor], {}, predecessor)
        self.assertEqual(paths, [
            '/issuers/retiring-service-v3/crl',
            '/issuers/retiring-service-v3/publish-service-client-revocation',
            '/issuers/retiring-service-v3/finalize-service-client-revocation',
        ])

    def test_pending_recovery_matches_existing_provider_certificate_before_reconcile(self):
        runner = lifecycle.AccountListenerLifecycle.__new__(lifecycle.AccountListenerLifecycle)
        issuer_id = 'a' * 8 + '-aaaa-aaaa-aaaa-' + 'a' * 12
        request_id = 'b' * 8 + '-bbbb-bbbb-bbbb-' + 'b' * 12
        claim = {'issuer_id': issuer_id, 'request_id': request_id,
                 'subject': lifecycle.SUBJECT, 'caller': lifecycle.SUBJECT,
                 'status': 'issuing', 'csr_pem': 'csr'}
        runner.openssl = 'openssl'
        runner.api = Mock(return_value={'signer_reference': 'pki-issuers/service/' + issuer_id + '/v5'})
        runner.bao = Mock(side_effect=[json.dumps(['ab:cd']), json.dumps({
            'data': {'certificate': 'certificate', 'revocation_time': 0}})])
        saved = {}
        runner.save = lambda name, value: saved.setdefault(name, value)
        with patch.object(lifecycle.m, 'command', side_effect=['public-key', 'public-key']):
            self.assertEqual(runner.provider_serial_for_pending_claim(claim), 'ab:cd')
        self.assertEqual(saved['pending-provider-inventory.json']['matching_serials'], ['ab:cd'])
        self.assertNotIn('csr_pem', saved['pending-provider-inventory.json'])


if __name__ == '__main__':
    unittest.main()
