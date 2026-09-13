import copy
import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location(
    'service_api_root_policy', Path(__file__).with_name('service_api_root_policy.py'))
r = importlib.util.module_from_spec(spec)
spec.loader.exec_module(r)


class ServiceAPIRootPolicyTests(unittest.TestCase):
    def root(self):
        return {'issuer_id': '00000000-0000-4000-8000-000000000001',
                'certificate_fingerprint_sha256': 'a' * 64}

    def owner(self):
        env = []
        for prefix in r.PREFIXES:
            env += [
                {'name': prefix + '_SERVER_PKI_ROOT_SHA256', 'value': 'a' * 64},
                {'name': prefix + '_SERVER_PKI_NAME', 'value': 'account.example'},
                {'name': prefix + '_SERVER_CRL_MANIFEST', 'value': '/crls.json'},
                {'name': prefix + '_PKI_CONTROLLER_URL', 'value': 'https://controller.example'},
                {'name': prefix + '_MANAGEMENT_CA', 'value': '/ca.crt'},
            ]
        env += [
            {'name': r.PREFIXES[0] + '_IDENTITY_STATE', 'value': '/identity.json'},
            {'name': r.PREFIXES[0] + '_IDENTITY_ROOT_SHA256', 'value': 'a' * 64},
            {'name': 'KEPT', 'value': 'true'},
        ]
        return {'spec': {'template': {'metadata': {'annotations': {'kept': 'true'}}, 'spec': {
            'containers': [{'name': r.CONTAINER, 'image': 'old', 'env': env,
                            'volumeMounts': [{'name': 'service-root', 'mountPath': '/run/pki-service-root', 'readOnly': True}]}],
            'volumes': [{'name': 'service-root', 'configMap': {'name': 'pki-service-host-root'}}],
        }}}}

    def test_template_adds_both_api_root_policy_owners(self):
        owner = self.owner()
        result = r.api_template(owner, self.root())
        values = {entry['name']: entry['value'] for entry in result['spec']['containers'][0]['env']}
        self.assertEqual(values, {**{entry['name']: entry['value'] for entry in owner['spec']['template']['spec']['containers'][0]['env']},
                                  **r.root_settings(self.root()['issuer_id'])})
        self.assertEqual(result['metadata']['annotations']['rtk.realtek.com/service-root-policy'], self.root()['issuer_id'])
        self.assertEqual(owner['spec']['template']['spec']['containers'][0]['image'], 'old')

    def test_template_rejects_partial_or_changed_policy(self):
        owner = self.owner()
        owner['spec']['template']['spec']['containers'][0]['env'].append(
            {'name': r.PREFIXES[0] + '_SERVICE_ROOT_ID', 'value': 'old'})
        with self.assertRaises(RuntimeError):
            r.api_template(owner, self.root())
        owner = self.owner()
        adopted = r.api_template(owner, self.root())
        owner['spec']['template'] = adopted
        self.assertEqual(r.api_template(owner, self.root()), adopted)
        owner['spec']['template']['spec']['containers'][0]['env'][-1]['value'] = 'wrong'
        with self.assertRaises(RuntimeError):
            r.api_template(owner, self.root())
        owner = self.owner()
        owner['spec']['template']['metadata']['annotations']['rtk.realtek.com/service-root-policy'] = 'old'
        with self.assertRaises(RuntimeError):
            r.api_template(owner, self.root())

    def test_template_does_not_mutate_input_and_validates_state(self):
        owner = self.owner()
        before = copy.deepcopy(owner)
        r.api_template(owner, self.root())
        self.assertEqual(owner, before)
        self.assertEqual(r.installed_policy_sha({'policy': {'policy_sha256': 'b' * 64}}), 'b' * 64)
        with self.assertRaises(RuntimeError):
            r.installed_policy_sha({'policy_sha256': 'b' * 64})


if __name__ == '__main__':
    unittest.main()
