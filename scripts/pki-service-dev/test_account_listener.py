import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location('account_listener', Path(__file__).with_name('account_listener.py'))
listener = importlib.util.module_from_spec(spec)
spec.loader.exec_module(listener)


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


if __name__ == '__main__':
    unittest.main()
