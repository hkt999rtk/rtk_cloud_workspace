import copy
import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location(
    'service_caller_root_policy', Path(__file__).with_name('service_caller_root_policy.py'))
r = importlib.util.module_from_spec(spec)
spec.loader.exec_module(r)


class ServiceCallerRootPolicyTemplateTests(unittest.TestCase):
    def root(self):
        return {'issuer_id': '00000000-0000-4000-8000-000000000001',
                'certificate_fingerprint_sha256': 'a' * 64}

    def owner(self):
        prefix = r.PREFIX
        return {'spec': {'replicas': 1, 'template': {'metadata': {'annotations': {'kept': 'true'}}, 'spec': {
            'containers': [{'name': r.CONTAINER, 'image': 'old', 'env': [
                {'name': prefix + '_URL', 'value': 'https://issuer.example'},
                {'name': prefix + '_SERVER_PKI_ROOT_SHA256', 'value': 'a' * 64},
                {'name': prefix + '_SERVER_PKI_NAME', 'value': 'issuer.example'},
                {'name': prefix + '_TLS_CA', 'value': '/run/service-root/root.pem'},
                {'name': prefix + '_SERVER_CRL_MANIFEST', 'value': '/run/service-crls/crls.json'},
                {'name': prefix + '_PKI_CONTROLLER_URL', 'value': 'https://controller.example'},
                {'name': prefix + '_MANAGEMENT_CA', 'value': '/run/service-root/root.pem'},
                {'name': 'FACTORY_ENROLL_SERVICE_IDENTITY_STATE', 'value': '/state/identity/client.json'},
                {'name': 'FACTORY_ENROLL_SERVICE_IDENTITY_ROOT_SHA256', 'value': 'a' * 64},
                {'name': 'KEPT', 'value': 'true'}],
                            'volumeMounts': [{'name': 'identity', 'mountPath': '/state'},
                                             {'name': 'service-root', 'mountPath': '/run/service-root'}]}],
            'volumes': [{'name': 'identity', 'persistentVolumeClaim': {'claimName': 'factoryenroll-service-identity'}},
                        {'name': 'service-root', 'configMap': {'name': 'pki-service-host-root'}}]}}}}

    def test_template_adds_only_durable_root_settings(self):
        before = self.owner()
        result = r.caller_template(before, self.root())
        values = {item['name']: item['value'] for item in result['spec']['containers'][0]['env']}
        self.assertEqual(values[r.PREFIX + '_SERVICE_ROOT_ID'], self.root()['issuer_id'])
        self.assertEqual(values[r.PREFIX + '_SERVICE_ROOT_STATE'], r.STATE)
        self.assertEqual(values[r.PREFIX + '_SERVICE_ROOTS'], r.ROOTS)
        self.assertEqual(values[r.ADMISSION_PREFIX + '_SERVICE_ROOT_ID'], self.root()['issuer_id'])
        self.assertEqual(values[r.ADMISSION_PREFIX + '_SERVICE_ROOT_STATE'], r.ADMISSION_STATE)
        self.assertEqual(values[r.ADMISSION_PREFIX + '_SERVICE_ROOTS'], r.ROOTS)
        self.assertEqual(values['KEPT'], 'true')
        self.assertEqual(before['spec']['template']['spec']['containers'][0]['image'], 'old')

    def test_template_rejects_partial_or_conflicting_adoption(self):
        owner = self.owner()
        owner['spec']['template']['spec']['containers'][0]['env'].append(
            {'name': r.PREFIX + '_SERVICE_ROOT_ID', 'value': 'old'})
        with self.assertRaises(RuntimeError):
            r.caller_template(owner, self.root())
        owner = self.owner()
        adopted = r.caller_template(owner, self.root())
        owner['spec']['template'] = adopted
        self.assertEqual(r.caller_template(owner, self.root()), adopted)
        owner['spec']['template']['spec']['containers'][0]['env'][-1]['value'] = '/wrong'
        with self.assertRaises(RuntimeError):
            r.caller_template(owner, self.root())

    def test_template_does_not_modify_input(self):
        owner = self.owner()
        original = copy.deepcopy(owner)
        r.caller_template(owner, self.root())
        self.assertEqual(owner, original)

    def test_policy_state_requires_envelope_digest(self):
        self.assertEqual(r.installed_policy_sha({'policy': {'policy_sha256': 'b' * 64}}), 'b' * 64)
        with self.assertRaises(RuntimeError):
            r.installed_policy_sha({'policy_sha256': 'b' * 64})


if __name__ == '__main__':
    unittest.main()
