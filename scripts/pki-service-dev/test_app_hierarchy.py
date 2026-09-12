import importlib.util
from pathlib import Path
import json
import tempfile
from types import SimpleNamespace
import unittest


spec = importlib.util.spec_from_file_location(
    'app_hierarchy', Path(__file__).with_name('app_hierarchy.py'))
a = importlib.util.module_from_spec(spec)
spec.loader.exec_module(a)


class AppManifestTests(unittest.TestCase):
    def issuer(self, issuer_id, kind, parent=''):
        return {
            'issuer_id': issuer_id, 'kind': kind,
            'parent_issuer_id': parent, 'environment': 'dev',
            'trust_domain': 'app', 'certificate_pem': issuer_id + '\n',
            'trust_bundle_version': issuer_id * 64}

    def test_manifest_keeps_two_independent_lineages(self):
        first = self.issuer('a', 'root')
        old = self.issuer('b', 'intermediate', 'a')
        second = self.issuer('c', 'root')
        successor = self.issuer('d', 'intermediate', 'c')

        result = a.app_manifest([first, old, second, successor])

        self.assertEqual(result['roots.pem'], 'a\nc\n')
        self.assertIn('/run/pki-state/app/b-crl.json', result['crls.json'])
        self.assertIn('/run/pki-state/app/d-crl.json', result['crls.json'])

    def test_manifest_rejects_cross_lineage_intermediate(self):
        root = self.issuer('a', 'root')
        foreign = self.issuer('b', 'intermediate', 'missing')
        with self.assertRaises(RuntimeError):
            a.app_manifest([root, foreign])

    def test_app_root_lookup_does_not_collide_with_device_preflight_root(self):
        saved = {
            'issuer_id': '00000000-0000-4000-8000-000000000001',
            'environment': 'dev', 'trust_domain': 'app', 'kind': 'root',
            'status': 'ready', 'certificate_fingerprint_sha256': 'a' * 64,
            'trust_bundle_version': 'b' * 64}
        with tempfile.TemporaryDirectory() as directory:
            Path(directory, 'root-ready.json').write_text(json.dumps(saved))
            runner = object.__new__(a.AppHierarchy)
            runner.args = SimpleNamespace(authority=directory)
            runner.root = {'trust_domain': 'device'}
            runner.api = lambda path: saved
            self.assertEqual(runner.app_root('ready'), saved)

    def test_registry_issuance_env_removes_static_app_signer(self):
        result = a.with_env([
            {'name': 'CERT_ISSUER_APP_CA_CERT_PATH', 'value': '/old'},
            {'name': 'CERT_ISSUER_APP_CLIENT_CN_PATTERN', 'value': '^caller$'},
        ], {'CERT_ISSUER_APP_PKI_ENABLED': 'true'}, {
            'CERT_ISSUER_APP_CA_CERT_PATH'})
        self.assertEqual(result, [
            {'name': 'CERT_ISSUER_APP_CLIENT_CN_PATTERN', 'value': '^caller$'},
            {'name': 'CERT_ISSUER_APP_PKI_ENABLED', 'value': 'true'}])

    def test_consumer_rollout_selects_only_owned_binary_and_app_roots(self):
        image = ('ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:'
                 + 'a' * 64)
        owner = lambda names: {'spec': {'template': {
            'metadata': {'annotations': {'kept': 'true'}},
            'spec': {'containers': [
                {'name': name, 'image': 'old-' + name, 'env': [
                    {'name': 'KEPT', 'value': name}]}
                for name in names]}}}}
        cases = {
            'video-cloud-api-app-pki': (['app'], 'app'),
            'mqtt-pki': (['mqtt', 'pkibroker'], 'pkibroker'),
            'pkiturn': (['pkiturn', 'coturn-cli-tunnel'], 'pkiturn')}
        for deployment, (names, selected) in cases.items():
            with self.subTest(deployment=deployment):
                before = owner(names)
                result = a.consumer_template(
                    before, deployment, image, 'successor')
                containers = {item['name']: item
                              for item in result['spec']['containers']}
                self.assertEqual(containers[selected]['image'], image)
                for name in set(names) - {selected}:
                    self.assertEqual(containers[name]['image'], 'old-' + name)
                self.assertEqual(result['metadata']['annotations'], {
                    'kept': 'true',
                    'rtk.realtek.com/app-trust-revision': 'successor'})
                roots = [item for item in containers[selected]['env']
                         if item['name'] ==
                         'VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_ROOTS']
                self.assertEqual(len(roots),
                                 1 if deployment ==
                                 'video-cloud-api-app-pki' else 0)
                self.assertEqual(before['spec']['template']['spec']
                                 ['containers'][0]['image'], 'old-' + names[0])


if __name__ == '__main__':
    unittest.main()
