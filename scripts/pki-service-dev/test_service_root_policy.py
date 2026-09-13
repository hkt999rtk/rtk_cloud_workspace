import copy
import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location(
    'service_root_policy', Path(__file__).with_name('service_root_policy.py'))
r = importlib.util.module_from_spec(spec)
spec.loader.exec_module(r)


class ServiceRootPolicyTemplateTests(unittest.TestCase):
    def root(self):
        return {'issuer_id': '00000000-0000-4000-8000-000000000001',
                'certificate_fingerprint_sha256': 'a' * 64}

    def owner(self, name):
        prefix = r.prefix(name)
        return {'spec': {'template': {'metadata': {'annotations': {'kept': 'true'}}, 'spec': {
            'containers': [{'name': name, 'image': 'old', 'env': [
                {'name': prefix + '_ROOT_SHA256', 'value': 'a' * 64},
                {'name': prefix + '_SERVER_CRL_MANIFEST', 'value': '/crls.json'},
                {'name': prefix + '_PKI_CONTROLLER_URL', 'value': 'https://controller.example'},
                {'name': prefix + '_MANAGEMENT_CA', 'value': '/ca.crt'},
                {'name': 'KEPT', 'value': 'true'}], 'volumeMounts': []}],
            'volumes': []}}}}

    def test_template_adds_only_durable_root_policy_settings(self):
        for name in r.NAMES:
            with self.subTest(name=name):
                before = self.owner(name)
                result = r.root_template(before, name, self.root())
                container = result['spec']['containers'][0]
                env = {entry['name']: entry['value'] for entry in container['env']}
                selected = r.prefix(name)
                self.assertEqual(env[selected + '_SERVICE_ROOT_ID'], self.root()['issuer_id'])
                self.assertEqual(env[selected + '_SERVICE_ROOT_STATE'], r.ROOT_STATE)
                self.assertEqual(env[selected + '_SERVICE_ROOTS'], r.ROOT_MOUNT + '/roots.pem')
                self.assertEqual(env['KEPT'], 'true')
                self.assertEqual(container['volumeMounts'], [{'name': 'service-root-policy', 'mountPath': r.ROOT_MOUNT, 'readOnly': True}])
                self.assertEqual(result['spec']['volumes'], [{'name': 'service-root-policy', 'configMap': {'name': r.ROOT_CONFIG, 'defaultMode': 292}}])
                self.assertEqual(before['spec']['template']['spec']['containers'][0]['image'], 'old')

    def test_template_rejects_partial_or_conflicting_adoption(self):
        owner = self.owner('pki-controller')
        owner['spec']['template']['spec']['containers'][0]['env'].append(
            {'name': 'PKI_SERVICE_CLIENT_SERVICE_ROOT_ID', 'value': 'old'})
        with self.assertRaises(RuntimeError):
            r.root_template(owner, 'pki-controller', self.root())

        owner = self.owner('certissuer')
        owner['spec']['template']['spec']['containers'][0]['env'] = [
            entry for entry in owner['spec']['template']['spec']['containers'][0]['env']
            if entry['name'] != 'CERT_ISSUER_SERVICE_CLIENT_MANAGEMENT_CA']
        with self.assertRaises(RuntimeError):
            r.root_template(owner, 'certissuer', self.root())

        owner = self.owner('pki-controller')
        owner['spec']['template'] = r.root_template(owner, 'pki-controller', self.root())
        owner['spec']['template']['spec']['containers'][0]['env'][-1]['value'] = '/wrong/roots.pem'
        with self.assertRaises(RuntimeError):
            r.root_template(owner, 'pki-controller', self.root())

    def test_template_reconciles_an_exact_existing_adoption(self):
        owner = self.owner('pki-controller')
        adopted = r.root_template(owner, 'pki-controller', self.root())
        owner['spec']['template'] = adopted
        self.assertEqual(r.root_template(owner, 'pki-controller', self.root()), adopted)

    def test_template_does_not_modify_input(self):
        owner = self.owner('pki-controller')
        original = copy.deepcopy(owner)
        r.root_template(owner, 'pki-controller', self.root())
        self.assertEqual(owner, original)

    def test_installed_policy_sha_uses_pkitrust_state_envelope(self):
        state = {'policy': {'policy_sha256': 'b' * 64}, 'roots_pem': 'public'}
        self.assertEqual(r.installed_policy_sha(state), 'b' * 64)
        for invalid in ({}, {'policy': {}}, {'policy_sha256': 'b' * 64},
                        {'policy': {'policy_sha256': 'not-a-digest'}}):
            with self.subTest(invalid=invalid):
                with self.assertRaises(RuntimeError):
                    r.installed_policy_sha(invalid)


if __name__ == '__main__':
    unittest.main()
