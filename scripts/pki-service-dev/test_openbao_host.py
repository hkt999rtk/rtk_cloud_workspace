import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location(
    'openbao_host', Path(__file__).with_name('openbao_host.py'))
o = importlib.util.module_from_spec(spec)
spec.loader.exec_module(o)


class OpenBaoHostTests(unittest.TestCase):
    def test_staged_template_preserves_listener_and_uses_managed_receipts(self):
        owner = {'metadata': {'name': 'certissuer'}, 'spec': {'template': {
            'metadata': {}, 'spec': {'containers': [{
                'name': 'certissuer', 'image': 'old', 'env': [],
                'volumeMounts': [
                    {'name': 'openbao-ca', 'mountPath': '/run/openbao-ca',
                     'readOnly': True},
                    {'name': 'host-root', 'mountPath': '/run/pki-host-root',
                     'readOnly': True}]}],
                'volumes': [
                    {'name': 'openbao-ca', 'configMap': {
                        'name': 'pki-openbao-transport-ca'}},
                    {'name': 'host-root', 'configMap': {'name': 'root'}}]}}}}
        runner = object.__new__(o.OpenBaoHostRun)
        runner.output = Path('/private/evidence')
        root = {'certificate_fingerprint_sha256': 'a' * 64}
        result = runner.staged_client_template(
            owner, 'new', 'combined', 'manifest', root)
        pod = result['spec']
        env = {item['name']: item.get('value')
               for item in pod['containers'][0]['env']}
        self.assertEqual(env['OPENBAO_SERVER_BUNDLE_ROOT_SHA256'], 'a' * 64)
        self.assertNotIn('OPENBAO_SERVER_PKI_ROOT_SHA256', env)
        self.assertNotIn('OPENBAO_MANAGEMENT_KEY', env)
        volumes = {item['name']: item for item in pod['volumes']}
        self.assertEqual(volumes['openbao-ca']['configMap']['name'], 'combined')
        self.assertEqual(owner['spec']['template']['spec']['volumes'][0]
                         ['configMap']['name'], 'pki-openbao-transport-ca')

    def test_staged_template_is_accepted_by_recovery_verifier(self):
        owner = {'metadata': {'name': 'pki-controller'},
                 'spec': {'template': {'metadata': {}, 'spec': {
                     'containers': [{
                         'name': 'pki-controller', 'image': 'old', 'env': [],
                         'volumeMounts': [
                             {'name': 'openbao-ca',
                              'mountPath': '/run/openbao-ca'},
                             {'name': 'host-root',
                              'mountPath': '/run/pki-host-root'}]}],
                     'volumes': [
                         {'name': 'openbao-ca', 'configMap': {
                             'name': 'pki-openbao-transport-ca'}},
                         {'name': 'host-root', 'configMap': {
                             'name': 'root'}}]}}}}
        runner = object.__new__(o.OpenBaoHostRun)
        runner.output = Path('/private/evidence')
        root = {'certificate_fingerprint_sha256': 'a' * 64}
        owner['spec']['template'] = runner.staged_client_template(
            owner, 'new', 'combined', 'manifest', root)
        runner.verify_staged_client(
            owner, 'new', 'combined', 'manifest', root)

    def test_recovery_verifier_rejects_different_image(self):
        owner = {'metadata': {'name': 'certissuer'},
                 'spec': {'template': {'metadata': {}, 'spec': {
                     'containers': [{
                         'name': 'certissuer', 'image': 'old', 'env': [],
                         'volumeMounts': [
                             {'name': 'openbao-ca',
                              'mountPath': '/run/openbao-ca'},
                             {'name': 'host-root',
                              'mountPath': '/run/pki-host-root'}]}],
                     'volumes': [
                         {'name': 'openbao-ca', 'configMap': {
                             'name': 'pki-openbao-transport-ca'}},
                         {'name': 'host-root', 'configMap': {
                             'name': 'root'}}]}}}}
        runner = object.__new__(o.OpenBaoHostRun)
        runner.output = Path('/private/evidence')
        root = {'certificate_fingerprint_sha256': 'a' * 64}
        owner['spec']['template'] = runner.staged_client_template(
            owner, 'new', 'combined', 'manifest', root)
        with self.assertRaises(RuntimeError):
            runner.verify_staged_client(
                owner, 'other', 'combined', 'manifest', root)


if __name__ == '__main__':
    unittest.main()
