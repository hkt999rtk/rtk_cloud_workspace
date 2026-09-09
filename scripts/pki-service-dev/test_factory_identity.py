import importlib.util
from pathlib import Path
import unittest
import tempfile
import json


spec = importlib.util.spec_from_file_location(
    'factory_identity', Path(__file__).with_name('factory_identity.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class FactoryIdentityTests(unittest.TestCase):
    def test_select_active_v2_requires_exact_transition(self):
        root = {'issuer_id': 'root'}
        common = {'environment': 'dev', 'trust_domain': 'service',
                  'kind': 'intermediate', 'parent_issuer_id': 'root'}
        old = dict(common, issuer_id='old', status='retiring',
                   service_client_ids=list(m.fa.OLD_CLIENT_IDS))
        new = dict(common, issuer_id='new', status='active',
                   service_client_ids=list(m.fa.V2_CLIENT_IDS))
        self.assertEqual(m.select_active_v2([old, new], root), (old, new))
        with self.assertRaisesRegex(RuntimeError, 'v2 required'):
            m.select_active_v2([old, dict(new, service_client_ids=['service:other'])], root)

    def test_provisioner_template_changes_only_exact_closed_policy(self):
        owner = {'spec': {'template': {'spec': {'containers': [{
            'name': 'certissuer', 'image': 'image', 'env': [
                {'name': 'OTHER', 'value': 'kept'},
                {'name': 'CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN', 'value': '^$'},
            ]}]}}}}
        result = m.provisioner_template(owner, '^$', '^factoryenroll$')
        self.assertEqual(m.env_values(result['spec']['containers'][0]), {
            'OTHER': 'kept',
            'CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN': '^factoryenroll$',
        })
        self.assertEqual(m.env_values(owner['spec']['template']['spec']['containers'][0])[
            'CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN'], '^$')

    def test_seed_pod_keeps_key_in_retained_pvc(self):
        image = 'ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:' + 'a' * 64
        pod = m.seed_pod(image, 'b' * 64, [{'name': 'existing-pull'}])
        self.assertEqual(pod['spec']['imagePullSecrets'], [{'name': 'existing-pull'}])
        self.assertEqual(pod['metadata']['namespace'], m.NS)
        self.assertFalse(pod['spec']['automountServiceAccountToken'])
        self.assertEqual(pod['spec']['restartPolicy'], 'Never')
        self.assertEqual(pod['spec']['volumes'][0]['persistentVolumeClaim']['claimName'], m.PVC)
        container = pod['spec']['containers'][0]
        self.assertTrue(container['securityContext']['readOnlyRootFilesystem'])
        self.assertNotIn('PRIVATE KEY', str(pod))
        values = m.env_values(container)
        self.assertEqual(values['PKI_CLIENT_IDENTITY_STATE'], m.STATE)
        self.assertEqual(values['PKI_CLIENT_IDENTITY_BOOTSTRAP_KEY'], '/run/bootstrap/client.key')

    def test_parse_seed_log_accepts_only_public_matching_state(self):
        fingerprint = 'c' * 64
        raw = ('{"subject":"service:factory-enroll","certificate_sha256":"' + fingerprint + '"}\n'
               '{"subject":"service:factory-enroll","fingerprint":"' + fingerprint
               + '","pending":false,"root_sha256":"' + 'd' * 64 + '"}\n'
               '600\n' + 'e' * 64 + '  ' + m.STATE + '\n')
        parsed = m.parse_seed_log(raw)
        self.assertEqual(parsed['certificate_sha256'], fingerprint)
        self.assertEqual(parsed['state_sha256'], 'e' * 64)
        with self.assertRaisesRegex(RuntimeError, 'permission'):
            m.parse_seed_log(raw.replace('\n600\n', '\n644\n'))

    def test_seed_refuses_missing_pull_credentials(self):
        with self.assertRaisesRegex(RuntimeError, 'pull references'):
            m.seed_pod('image', 'b' * 64, [])

    def test_adoption_reuses_one_key_and_preserves_unrelated_settings(self):
        name = 'factoryenroll-certissuer-client'
        owner = {'spec': {'replicas': 1, 'template': {'spec': {
            'volumes': [{'name': name, 'secret': {'secretName': name}}],
            'containers': [{'name': 'factoryenroll', 'image': 'old', 'env': [
                {'name': 'UNRELATED', 'valueFrom': {'secretKeyRef': {'name': 'runtime', 'key': 'key'}}},
                {'name': 'FACTORY_ENROLL_CERT_ISSUER_CLIENT_KEY', 'value': '/old/key'},
            ], 'volumeMounts': [{'name': name, 'mountPath': '/old'}]}]}}}}
        result = m.managed_template(owner, 'new', 'a' * 64)
        c = result['spec']['containers'][0]
        self.assertEqual(c['env'][0], owner['spec']['template']['spec']['containers'][0]['env'][0])
        self.assertNotIn('FACTORY_ENROLL_CERT_ISSUER_CLIENT_KEY', m.env_values(c))
        self.assertFalse(any('MANAGEMENT_KEY' in e['name'] for e in c['env']))
        self.assertFalse(any('secret' in v for v in result['spec']['volumes']))
        self.assertEqual(m.env_values(c)['FACTORY_ENROLL_SERVICE_IDENTITY_STATE'], m.STATE)
        self.assertEqual(m.CONSUMERS, ['certissuer', 'factory-enroll', 'pki-controller'])

    def test_recovery_refuses_replaced_pvc_or_changed_workload(self):
        factory = {'metadata': {'uid': 'factory'}, 'spec': {'template': {'old': True}}}
        pvc = {'metadata': {'uid': 'volume'}, 'spec': {'volumeName': 'retained'}}
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory)
            for name, value in [('report.json', {'status': 'failed'}),
                                ('interrupted-seed-cleanup.json', {'seed_pod_deleted': True}),
                                ('factory-deployment-baseline.json', factory),
                                ('factory-identity-pvc.json', pvc)]:
                (source / name).write_text(json.dumps(value))
            m.verify_unused_pvc_recovery(source, factory, pvc)
            with self.assertRaisesRegex(RuntimeError, 'changed since'):
                m.verify_unused_pvc_recovery(source, factory, dict(pvc, metadata={'uid': 'other'}))
            with self.assertRaisesRegex(RuntimeError, 'changed since'):
                m.verify_unused_pvc_recovery(source, dict(factory, spec={'template': {}}), pvc)


if __name__ == '__main__':
    unittest.main()
