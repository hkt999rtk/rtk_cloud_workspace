import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('service_egress', Path(__file__).with_name('egress.py'))
e = importlib.util.module_from_spec(spec)
spec.loader.exec_module(e)


class EgressTests(unittest.TestCase):
    def owner(self, name):
        prefix = 'CERT_ISSUER_SERVICE_CLIENT' if name == 'certissuer' else 'PKI_SERVICE_CLIENT'
        host = 'CERT_ISSUER_HOST_RENEWAL' if name == 'certissuer' else 'PKI_HOST_RENEWAL'
        return {'spec': {'template': {'spec': {'containers': [{'name': name, 'image': 'old', 'env': [
            {'name': prefix + '_ROOT_SHA256', 'value': 'root'},
            {'name': prefix + '_BUNDLE_MANIFEST', 'value': '/bundle'},
            {'name': prefix + '_PKI_CONTROLLER_URL', 'value': 'https://controller'},
            {'name': prefix + '_MANAGEMENT_CA', 'value': '/run/pki-service-consumer/ca.crt'},
            {'name': prefix + '_MANAGEMENT_CERT', 'value': '/run/pki-service-consumer/tls.crt'},
            {'name': prefix + '_MANAGEMENT_KEY', 'value': '/run/pki-service-consumer/tls.key'},
            {'name': host + '_CLIENT_CERT', 'value': '/run/pki-service-consumer/tls.crt'},
            {'name': host + '_CLIENT_KEY', 'value': '/run/pki-service-consumer/tls.key'},
            {'name': 'UNRELATED', 'value': 'retained'}],
            'volumeMounts': [{'name': 'service-consumer', 'mountPath': '/run/pki-service-consumer', 'readOnly': True},
                             {'name': 'retained', 'mountPath': '/retained'}]}],
            'volumes': [{'name': 'service-consumer', 'secret': {'secretName': e.profile(name)['secret']}},
                        {'name': 'retained', 'emptyDir': {}}]}}}}

    def test_managed_template_removes_only_private_bootstrap_transport(self):
        image = 'ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:' + 'a' * 64
        owner = self.owner('certissuer')
        template = e.managed_template(owner, 'certissuer', image)
        container = template['spec']['containers'][0]
        values = e.env_values(container)
        self.assertEqual(values['CERT_ISSUER_SERVICE_CLIENT_IDENTITY_STATE'], e.STATE)
        self.assertEqual(values['CERT_ISSUER_SERVICE_CLIENT_MANAGEMENT_CA'], e.CA_MOUNT)
        for key in ('CERT_ISSUER_SERVICE_CLIENT_MANAGEMENT_CERT', 'CERT_ISSUER_SERVICE_CLIENT_MANAGEMENT_KEY',
                    'CERT_ISSUER_HOST_RENEWAL_CLIENT_CERT', 'CERT_ISSUER_HOST_RENEWAL_CLIENT_KEY'):
            self.assertNotIn(key, values)
        self.assertEqual(values['UNRELATED'], 'retained')
        self.assertEqual(container['image'], image)
        self.assertEqual([item['name'] for item in container['volumeMounts']], ['retained', 'service-managed-egress-ca'])
        self.assertEqual(template['spec']['volumes'][0], {'name': 'retained', 'emptyDir': {}})
        self.assertEqual(template['spec']['volumes'][1]['configMap']['name'], 'pki-service-managed-egress-certissuer-ca')
        self.assertEqual(owner['spec']['template']['spec']['containers'][0]['image'], 'old')

    def test_managed_template_refuses_missing_or_preexisting_transition_state(self):
        owner = self.owner('pki-controller')
        owner['spec']['template']['spec']['containers'][0]['env'] = [item for item in owner['spec']['template']['spec']['containers'][0]['env']
                                                                       if item['name'] != 'PKI_HOST_RENEWAL_CLIENT_KEY']
        with self.assertRaisesRegex(RuntimeError, 'static egress settings incomplete'):
            e.managed_template(owner, 'pki-controller', 'image')
        owner = self.owner('pki-controller')
        owner['spec']['template']['spec']['containers'][0]['env'].append({'name': 'PKI_SERVICE_CLIENT_IDENTITY_STATE', 'value': e.STATE})
        with self.assertRaisesRegex(RuntimeError, 'already configured'):
            e.managed_template(owner, 'pki-controller', 'image')

    def test_pem_removal_is_exact_and_never_accepts_ambiguous_bundle(self):
        cert = '-----BEGIN CERTIFICATE-----\nlegacy\n-----END CERTIFICATE-----'
        other = '-----BEGIN CERTIFICATE-----\nother\n-----END CERTIFICATE-----'
        self.assertEqual(e.remove_pem(cert + '\n' + other + '\n', cert), other + '\n')
        for bundle in (other + '\n', cert + '\n' + cert + '\n', cert + '\n'):
            with self.assertRaises(RuntimeError):
                e.remove_pem(bundle, cert)

    def test_enrollment_template_changes_only_image_and_exact_policy(self):
        owner = self.owner('certissuer')
        owner['spec']['template']['spec']['containers'][0]['env'].append(
            {'name': 'CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN', 'value': '^service-provisioner$'})
        result = e.enrollment_template(owner, 'new-image', '^certissuer$')
        values = e.env_values(result['spec']['containers'][0])
        self.assertEqual(values['CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN'], '^certissuer$')
        self.assertEqual(values['UNRELATED'], 'retained')
        self.assertEqual(result['spec']['containers'][0]['image'], 'new-image')
        self.assertEqual(owner['spec']['template']['spec']['containers'][0]['image'], 'old')


if __name__ == '__main__':
    unittest.main()
