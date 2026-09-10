import importlib.util
from pathlib import Path
import unittest
from unittest.mock import Mock


spec = importlib.util.spec_from_file_location(
    'mqtt_host', Path(__file__).with_name('mqtt_host.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class MQTTHostTests(unittest.TestCase):
    def root(self, status='active'):
        return {
            'environment': 'dev', 'trust_domain': 'mqtt', 'kind': 'root',
            'status': status, 'issuer_id': 'root',
            'certificate_fingerprint_sha256': 'a' * 64,
            'trust_bundle_version': 'b' * 64,
        }

    def test_intermediate_is_limited_to_one_dev_mqtt_host(self):
        request = m.intermediate_request(self.root())
        self.assertEqual(request, {
            'environment': 'dev', 'trust_domain': 'mqtt',
            'kind': 'intermediate', 'parent_issuer_id': 'root',
            'server_dns_names': [m.MQTT_HOST]})
        for field, value in [('environment', 'staging'),
                             ('trust_domain', 'service'),
                             ('status', 'ready'), ('kind', 'intermediate')]:
            with self.assertRaisesRegex(RuntimeError,
                                        'active dev MQTT Root required'):
                m.intermediate_request(dict(self.root(), **{field: value}))

    def test_actual_clients_use_pinned_tls_and_separate_receipt_transport(self):
        settings = m.mqtt_client_settings(self.root())
        self.assertEqual(settings['VIDEO_CLOUD_ENV'], 'dev')
        self.assertEqual(settings['VIDEO_CLOUD_MQTT_ADDR'],
                         m.MQTT_HOST + ':8883')
        self.assertEqual(settings['VIDEO_CLOUD_MQTT_SERVER_PKI_NAME'],
                         m.MQTT_HOST)
        self.assertEqual(settings['VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256'],
                         'a' * 64)
        self.assertEqual(settings['VIDEO_CLOUD_MQTT_BUNDLE_MANAGEMENT_CA'],
                         '/run/pki-service-root/root.pem')
        self.assertNotIn('MFA', ''.join(settings))

    def test_client_template_preserves_existing_private_settings(self):
        owner = {'spec': {'template': {'metadata': {}, 'spec': {
            'containers': [{'name': 'app', 'image': 'old', 'env': [{
                'name': 'POSTGRES_PASSWORD', 'valueFrom': {
                    'secretKeyRef': {'name': 'runtime', 'key': 'password'}}}],
                'volumeMounts': [{'name': 'service-root',
                                  'mountPath': '/run/pki-service-root',
                                  'readOnly': True}]}],
            'volumes': [{'name': 'service-root', 'configMap': {
                'name': 'pki-service-host-root'}}]}}}}
        runner = object.__new__(m.MQTTHostRun)
        runner.args = type('Args', (), {'image':
            'ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:' +
            'c' * 64})()
        runner.output = Path('/private/evidence/root')
        result = runner.client_template(owner, self.root(), 'consumer', 'bundle')
        container = result['spec']['containers'][0]
        self.assertEqual(container['env'][0],
                         owner['spec']['template']['spec']['containers'][0]['env'][0])
        self.assertEqual(container['image'], runner.args.image)
        self.assertEqual([v['name'] for v in result['spec']['volumes']],
                         ['service-root', 'mqtt-pki-bundles',
                          'mqtt-pki-management'])
        self.assertEqual(result['spec']['securityContext'], {
            'fsGroup': 10001, 'fsGroupChangePolicy': 'OnRootMismatch'})
        self.assertEqual(len([v for v in container['volumeMounts']
                              if v['name'] == 'service-root']), 1)
        self.assertEqual(owner['spec']['template']['spec']['containers'][0]
                         ['image'], 'old')

    def test_intermediate_manifest_is_exact_and_fresh(self):
        root = self.root()
        issuer = {'issuer_id': 'intermediate',
                  'trust_bundle_version': 'c' * 64}
        first = m.mqtt_intermediate_manifest(root, issuer)
        first.append({'issuer_id': 'unreviewed'})
        self.assertEqual(len(m.mqtt_intermediate_manifest(root, issuer)), 2)

    def test_crl_initializer_only_creates_private_state_directory(self):
        init = m.crl_state_initializer('pinned-image')
        self.assertEqual(init['image'], 'pinned-image')
        self.assertEqual(init['volumeMounts'], [{
            'name': 'mqtt-pki-state', 'mountPath': '/var/lib/mqtt-pki'}])
        self.assertIn('mkdir -p /var/lib/mqtt-pki/crls', init['args'][0])
        self.assertTrue(init['securityContext']['readOnlyRootFilesystem'])
        self.assertEqual(init['securityContext']['runAsUser'], 10001)
        self.assertEqual(init['securityContext']['runAsGroup'], 10001)
        self.assertEqual(init['securityContext']['capabilities']['drop'], ['ALL'])
        admitted = dict(init, imagePullPolicy='IfNotPresent', resources={})
        self.assertTrue(m.valid_crl_state_initializer(admitted, 'pinned-image'))
        legacy = dict(admitted, securityContext=dict(init['securityContext']))
        legacy['securityContext'].pop('runAsUser')
        legacy['securityContext'].pop('runAsGroup')
        self.assertTrue(m.valid_crl_state_initializer(
            legacy, 'pinned-image', numeric_identity=False))
        self.assertFalse(m.valid_crl_state_initializer(
            dict(admitted, image='other'), 'pinned-image'))

    def test_intermediate_switch_changes_only_selected_bundle_source(self):
        owner = {'spec': {'template': {'metadata': {'annotations': {
            'retained': 'yes'}}, 'spec': {'volumes': [
                {'name': 'mqtt-pki-bundles', 'configMap': {
                    'name': 'pki-mqtt-bundles'}},
                {'name': 'private', 'secret': {'secretName': 'private'}}]}}}}
        runner = object.__new__(m.MQTTHostRun)
        runner.output = Path('/private/evidence/phase')
        runner.obj = Mock(return_value=owner)
        runner.scoped_patch, runner.kube = Mock(), Mock()
        runner.switch_client_bundle('video-cloud-api', 'reviewed-successor')
        changed = runner.scoped_patch.call_args.args[2][0]['value']
        self.assertEqual(changed['spec']['volumes'][0]['configMap']['name'],
                         'reviewed-successor')
        self.assertEqual(changed['spec']['volumes'][1],
                         owner['spec']['template']['spec']['volumes'][1])
        self.assertEqual(owner['spec']['template']['spec']['volumes'][0]
                         ['configMap']['name'], 'pki-mqtt-bundles')
        owner['spec']['template'] = changed
        runner.obj = Mock(return_value=owner)
        with self.assertRaisesRegex(RuntimeError,
                                    'MQTT client bundle source changed'):
            runner.switch_client_bundle('video-cloud-api', 'another')


if __name__ == '__main__':
    unittest.main()
