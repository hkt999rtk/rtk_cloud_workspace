import importlib.util
from pathlib import Path
import unittest
import base64
import tempfile
from unittest.mock import Mock

spec = importlib.util.spec_from_file_location('service_rollout', Path(__file__).with_name('run.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class RolloutTests(unittest.TestCase):
    def test_domain_change_preserves_unrelated_secret_references(self):
        secret = {'name': 'DATABASE_URL', 'valueFrom': {'secretKeyRef': {'name': 'database', 'key': 'dsn'}}}
        env = [secret, {'name': 'PKI_REQUIRED_CONSUMERS', 'value': 'video-cloud-api,pkibroker'}]
        result = m.with_env(env, {'PKI_REQUIRED_CONSUMERS_DEVICE': 'video-cloud-api,pkibroker',
                                  'PKI_REQUIRED_CONSUMERS_SERVICE': 'certissuer,pki-controller'},
                            ('PKI_REQUIRED_CONSUMERS',))
        self.assertEqual(result[0], secret)
        self.assertEqual(env[1]['name'], 'PKI_REQUIRED_CONSUMERS')
        self.assertNotIn('PKI_REQUIRED_CONSUMERS', [e['name'] for e in result])
        self.assertEqual(m.m.device_consumers({e['name']: e.get('value') for e in result}), m.m.CONSUMERS)

    def test_duplicate_settings_fail_before_changes(self):
        with self.assertRaisesRegex(RuntimeError, 'duplicate environment'):
            m.with_env([{'name': 'X', 'value': 'one'}, {'name': 'X', 'value': 'two'}], {'X': 'new'})

    def test_persisted_renderer_consumes_image_and_settings(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            path = base / 'pki/controller-bootstrap/rollout'
            desired = {'metadata': {'name': 'pki-controller', 'namespace': m.NS},
                       'spec': {'template': {'spec': {'containers': [{'name': 'pki-controller', 'image': 'old', 'env': []}]}}}}
            settings = {'PKI_REQUIRED_CONSUMERS_DEVICE': 'video-cloud-api,pkibroker',
                        'PKI_REQUIRED_CONSUMERS_SERVICE': 'certissuer,pki-controller'}
            m.m.write(path / 'pki-controller-deployment.json', desired)
            m.m.write(path / 'pki-controller-service-settings.json', settings)
            image = 'ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:' + 'a' * 64
            m.m.write(base / 'operator/env/PKI_CONTROLLER_IMAGE', image)
            rendered = m.render_persisted_listener(base, 'pki-controller')['spec']['template']['spec']['containers'][0]
            self.assertEqual(rendered['image'], image)
            self.assertEqual({e['name']: e['value'] for e in rendered['env']}, settings)
            settings['PKI_REQUIRED_CONSUMERS_DEVICE'] = 'video-cloud-api'
            m.m.write(path / 'pki-controller-service-settings.json', settings)
            with self.assertRaisesRegex(RuntimeError, 'persisted domain gates changed'):
                m.render_persisted_listener(base, 'pki-controller')

    def test_saved_crl_import_uses_ceremony_filename_and_never_resigns(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory)
            root = {'issuer_id': '123e4567-e89b-12d3-a456-426614174000', 'certificate_fingerprint_sha256': 'b' * 64}
            request = {'issuer_id': root['issuer_id'], 'issuer_fingerprint_sha256': root['certificate_fingerprint_sha256'],
                       'crl_number': '1', 'revocations': []}
            raw = '-----BEGIN X509 CRL-----\n' + base64.b64encode(b'fixture').decode() + '\n-----END X509 CRL-----\n'
            digest = m.m.digest(b'fixture')
            m.m.write(source / 'root-crl/revocations.pem', raw)
            m.m.write(source / 'root-crl-request.json', request)
            manifest = {'request': request, 'request_sha256': 'approved', 'crl_sha256': digest}
            m.m.write(source / 'root-crl/public-manifest.json', manifest)
            m.m.write(source / 'device-2/enroll-request.json', {'devid': 'baseline'})
            runner = object.__new__(m.ServiceRun)
            runner.foundation = source
            runner.ceremony_call = Mock(return_value='approved')
            runner.api = Mock(return_value={'crl_sha256': digest})
            runner.kube = Mock(return_value='0')
            runner.save, runner.check = Mock(), Mock()
            runner.auth, runner.wait_positive_mqtt = Mock(return_value={}), Mock(return_value=1)
            runner.import_saved_root_crl(source, root, m.SERVICE_CONSUMERS)
            self.assertEqual(runner.ceremony_call.call_args.args[0][0], 'crl-digest')
            self.assertEqual(runner.api.call_args.args[1], {'crl_pem': raw})
            runner.api.reset_mock()
            runner.kube.return_value = '1'
            runner.import_saved_root_crl(source, root, m.SERVICE_CONSUMERS)
            self.assertEqual(len(runner.api.call_args.args), 1)  # Reconcile an existing CRL by GET.
            runner.api.reset_mock()
            manifest['crl_sha256'] = 'c' * 64
            m.m.write(source / 'root-crl/public-manifest.json', manifest)
            with self.assertRaisesRegex(RuntimeError, 'saved CRL evidence changed'):
                runner.import_saved_root_crl(source, root, m.SERVICE_CONSUMERS)
            runner.api.assert_not_called()

    def test_append_preserves_existing_public_trust(self):
        public = '-----BEGIN CERTIFICATE-----\nfixture\n-----END CERTIFICATE-----\n'
        self.assertEqual(m.append_pem('prior\n', public), 'prior\n' + public)
        self.assertEqual(m.append_pem('prior\n' + public, public), 'prior\n' + public)
        with self.assertRaisesRegex(RuntimeError, 'public certificate required'):
            m.append_pem('prior\n', '-----BEGIN PRIVATE KEY-----')


if __name__ == '__main__':
    unittest.main()
