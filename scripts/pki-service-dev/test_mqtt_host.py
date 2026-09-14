import importlib.util
import base64
import json
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
        prior_image = dict(admitted, image='prior-image')
        self.assertFalse(m.valid_crl_state_initializer(
            prior_image, 'pinned-image'))
        self.assertTrue(m.compatible_crl_state_initializer(prior_image))
        nonnumeric = dict(prior_image,
                          securityContext=dict(init['securityContext']))
        nonnumeric['securityContext'].pop('runAsUser')
        nonnumeric['securityContext'].pop('runAsGroup')
        self.assertTrue(m.compatible_crl_state_initializer(
            nonnumeric, numeric_identity=False))
        legacy = dict(prior_image, args=[
            'set -eu; umask 077; mkdir -p /var/lib/mqtt-pki/crls '
            '/var/lib/mqtt-pki/identity/service-crls; chmod 700 '
            '/var/lib/mqtt-pki/crls /var/lib/mqtt-pki/identity '
            '/var/lib/mqtt-pki/identity/service-crls'])
        self.assertTrue(m.compatible_crl_state_initializer(legacy))
        self.assertFalse(m.compatible_crl_state_initializer(
            dict(prior_image, args=['mkdir -p /tmp'])))

    def test_existing_crl_source_must_be_an_immutable_dev_mqtt_manifest(self):
        valid = {'immutable': True, 'data': {'crls.json': json.dumps([{
            'issuer': {'issuer_id': '00000000-0000-4000-8000-000000000001',
                       'environment': 'dev', 'trust_domain': 'mqtt'},
            'state_path': '/var/lib/mqtt-pki/crls/current.json'}])}}
        self.assertTrue(m.valid_mqtt_crl_manifest(valid))
        self.assertFalse(m.valid_mqtt_crl_manifest(
            dict(valid, immutable=False)))
        invalid = json.loads(json.dumps(valid))
        invalid['data']['crls.json'] = json.dumps([{
            'issuer': {'issuer_id': 'not-an-issuer', 'environment': 'dev',
                       'trust_domain': 'mqtt'},
            'state_path': '/tmp/current.json'}])
        self.assertFalse(m.valid_mqtt_crl_manifest(invalid))

    def test_successor_root_id_replaces_only_the_policy_authority(self):
        env = [{'name': 'VIDEO_CLOUD_MQTT_ROOT_ID', 'value': 'predecessor'},
               {'name': 'VIDEO_CLOUD_MQTT_ROOT_STATE', 'value': '/private/state'},
               {'name': 'VIDEO_CLOUD_MQTT_ROOTS',
                'value': '/run/pki-mqtt/roots.pem'},
               {'name': 'RETAINED', 'value': 'yes'}]
        updated = m.h.with_env(env, {'VIDEO_CLOUD_MQTT_ROOT_ID': 'successor'})
        self.assertEqual({item['name']: item['value'] for item in updated}, {
            'VIDEO_CLOUD_MQTT_ROOT_ID': 'successor',
            'VIDEO_CLOUD_MQTT_ROOT_STATE': '/private/state',
            'VIDEO_CLOUD_MQTT_ROOTS': '/run/pki-mqtt/roots.pem',
            'RETAINED': 'yes'})

    def test_managed_host_settings_use_the_selected_mqtt_root_only(self):
        root = dict(self.root(), certificate_fingerprint_sha256='b' * 64)
        service = {'certificate_fingerprint_sha256': 'c' * 64}
        settings = m.mqtt_host_settings(root, service)
        self.assertEqual(settings['EMQX_PKI_HOST_ROOT_SHA256'], 'b' * 64)
        self.assertEqual(settings['EMQX_PKI_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256'],
                         'c' * 64)
        self.assertEqual(settings['EMQX_PKI_SERVICE_CLIENT_ROOT_SHA256'],
                         'c' * 64)
        self.assertEqual(settings['EMQX_PKI_SERVICE_CLIENT_IDENTITY_STATE'],
                         m.EMQX_SERVICE_STATE)
        self.assertEqual(settings['EMQX_PKI_HOST_IDENTITY_STATE'],
                         m.MQTT_HOST_STATE)
        self.assertNotIn('EMQX_PKI_HOST_RENEWAL_CLIENT_CERT', settings)
        self.assertNotIn('EMQX_PKI_HOST_RENEWAL_CLIENT_KEY', settings)

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

    def test_host_settings_separate_mqtt_and_service_roots(self):
        service = {'certificate_fingerprint_sha256': 'd' * 64}
        settings = m.mqtt_host_settings(self.root(), service)
        self.assertEqual(settings['EMQX_PKI_HOST_ROOT_SHA256'], 'a' * 64)
        self.assertEqual(
            settings['EMQX_PKI_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256'],
            'd' * 64)
        self.assertEqual(settings['EMQX_PKI_SERVICE_CLIENT_ROOT_SHA256'],
                         'd' * 64)
        self.assertEqual(settings['EMQX_PKI_SERVICE_CLIENT_IDENTITY_STATE'],
                         m.EMQX_SERVICE_STATE)
        self.assertEqual(settings['EMQX_PKI_HOST_DNS_NAMES'], m.MQTT_HOST)
        self.assertEqual(settings['EMQX_PKI_HOST_IDENTITY_STATE'],
                         m.MQTT_HOST_STATE)
        self.assertNotIn('EMQX_PKI_HOST_RENEWAL_CLIENT_KEY', settings)

    def test_emqx_bootstrap_is_one_use_and_normal_settings_stay_keyless(self):
        normal = m.mqtt_host_settings(self.root(), {
            'certificate_fingerprint_sha256': 'd' * 64})
        seeded = m.emqx_bootstrap_settings(normal)
        self.assertNotIn('EMQX_PKI_HOST_RENEWAL_CLIENT_KEY', normal)
        self.assertEqual(seeded['EMQX_PKI_SERVICE_CLIENT_IDENTITY_BOOTSTRAP_KEY'],
                         '/run/emqx-bootstrap/client.key')
        self.assertEqual(normal['EMQX_PKI_SERVICE_CLIENT_IDENTITY_STATE'],
                         m.EMQX_SERVICE_STATE)
        self.assertNotIn('EMQX_PKI_SERVICE_CLIENT_IDENTITY_BOOTSTRAP_KEY', normal)

    def test_emqx_bootstrap_pod_has_ephemeral_ca_and_no_exported_key(self):
        settings = m.mqtt_host_settings(self.root(), {
            'certificate_fingerprint_sha256': 'd' * 64})
        pod = m.emqx_bootstrap_pod('ghcr.io/hkt999rtk/rtk_cloud_dev/emqx-pki@sha256:' +
                                   'c' * 64, settings, 'pki-service-host-root',
                                   [{'name': 'registry'}])
        container = pod['spec']['containers'][0]
        env = {item['name']: item for item in container['env']}
        self.assertEqual(env['EMQX_PKI_SERVICE_CLIENT_IDENTITY_STATE']['value'],
                         m.EMQX_SERVICE_STATE)
        self.assertIn('EMQX_PKI_SERVICE_CLIENT_IDENTITY_BOOTSTRAP_KEY', env)
        self.assertNotIn('EMQX_PKI_HOST_RENEWAL_CLIENT_KEY', env)
        self.assertEqual(pod['spec']['volumes'][-1], {'name': 'bootstrap', 'emptyDir': {}})
        self.assertNotIn('PRIVATE KEY', str(pod))
        self.assertIn('service:emqx-pki', container['args'][0])

    def test_certissuer_bootstrap_policy_is_exact_and_closes_cleanly(self):
        owner = {'spec': {'template': {'spec': {'containers': [{
            'name': 'certissuer', 'env': [
                {'name': 'CERT_ISSUER_MQTT_SERVER_CLIENT_CN_PATTERN',
                 'value': '^emqx-pki$'},
                {'name': 'CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN',
                 'value': '^service-provisioner$'}]}]}}}}
        opened = m.certissuer_emqx_bootstrap_template(owner, '^service:emqx-pki$', True)
        values = {item['name']: item['value'] for item in
                  opened['spec']['containers'][0]['env']}
        self.assertEqual(values['CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN'],
                         '^service:emqx-pki$')
        self.assertEqual(values['CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER'],
                         m.EMQX_SERVICE_SUBJECT)
        closed = m.certissuer_emqx_bootstrap_template(
            {'spec': {'template': opened}}, '^service:emqx-pki$', False)
        values = {item['name']: item['value'] for item in
                  closed['spec']['containers'][0]['env']}
        self.assertEqual(values['CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN'],
                         '^service-provisioner$')
        self.assertNotIn('CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER', values)

    def test_reconfigure_managed_broker_removes_static_renewal_credentials(self):
        owner = {'spec': {'template': {'metadata': {}, 'spec': {
            'containers': [{'name': 'mqtt', 'image': 'old', 'env': [
                {'name': 'EMQX_PKI_HOST_IDENTITY_STATE', 'value': m.MQTT_HOST_STATE},
                {'name': 'EMQX_PKI_HOST_NAME', 'value': m.MQTT_HOST},
                {'name': 'EMQX_PKI_HOST_DNS_NAMES', 'value': m.MQTT_HOST},
                {'name': 'EMQX_PKI_HOST_RENEWAL_CLIENT_CERT', 'value': '/old/cert'},
                {'name': 'EMQX_PKI_HOST_RENEWAL_CLIENT_KEY', 'value': '/old/key'},
                {'name': 'PKI_DATABASE_URL', 'valueFrom': {'secretKeyRef': {
                    'name': 'pki-controller-database', 'key': 'url'}}}],
                'volumeMounts': []}, {'name': 'pkibroker', 'image': 'worker',
                'env': [], 'volumeMounts': []}],
            'volumes': [
                {'name': 'mqtt-host-state', 'persistentVolumeClaim': {'claimName': m.MQTT_HOST_PVC}},
                {'name': 'mqtt-host-runtime', 'emptyDir': {}},
                {'name': 'mqtt-host-management', 'secret': {'secretName': m.MQTT_MANAGEMENT_SECRET}},
                {'name': 'pki-service-root', 'configMap': {'name': 'pki-service-host-root'}}]}}}}
        settings = m.mqtt_host_settings(self.root(), {
            'certificate_fingerprint_sha256': 'd' * 64})
        result = m.reconfigure_managed_mqtt_template(owner, 'new-image', settings, 'phase')
        values = {item['name']: item.get('value') for item in
                  result['spec']['containers'][0]['env']}
        self.assertEqual(values['EMQX_PKI_SERVICE_CLIENT_IDENTITY_STATE'],
                         m.EMQX_SERVICE_STATE)
        self.assertNotIn('EMQX_PKI_HOST_RENEWAL_CLIENT_KEY', values)
        self.assertNotIn('EMQX_PKI_SEED_KEY', values)
        self.assertIn('PKI_DATABASE_URL', values)

    def test_managed_runtime_drops_all_tls_private_material(self):
        source = {name: name + '-value' for name in
                  m.MQTT_RUNTIME_FIELDS | {
                      'tls.key', 'tls.crt', 'server-ca.crt',
                      'callback.key', 'callback.crt', 'callback-ca.crt'}}
        source['authentication'] = base64.b64encode(json.dumps([{
            'backend': 'http', 'ssl': {'enable': True,
                                       'keyfile': '/legacy/callback.key',
                                       'certfile': '/legacy/callback.crt',
                                       'cacertfile': '/legacy/ca.crt'}}]).encode()).decode()
        result = m.managed_runtime_data(source)
        self.assertEqual(set(result), m.MQTT_RUNTIME_FIELDS)
        self.assertFalse(set(result) & {
            'tls.key', 'tls.crt', 'server-ca.crt',
            'callback.key', 'callback.crt', 'callback-ca.crt'})
        ssl = json.loads(base64.b64decode(
            result['authentication']).decode())[0]['ssl']
        self.assertEqual(ssl, {
            'enable': True,
            'keyfile': '/run/emqx-pki-management/tls.key',
            'certfile': '/run/emqx-pki-management/tls.crt',
            'cacertfile': '/run/mqtt-callback-ca/ca.crt'})
        with self.assertRaisesRegex(RuntimeError,
                                    'existing MQTT runtime fields incomplete'):
            m.managed_runtime_data({'cookie': 'only'})

    def test_managed_broker_template_preserves_worker_and_owns_host_state(self):
        owner = {'spec': {'template': {'metadata': {}, 'spec': {
            'securityContext': {'fsGroup': 1000},
            'containers': [{
                'name': 'mqtt', 'image': 'static',
                'env': [{'name': 'EMQX_NODE__COOKIE', 'valueFrom': {
                    'secretKeyRef': {'name': 'mqtt-pki-runtime',
                                     'key': 'cookie'}}}],
                'volumeMounts': [{'name': 'runtime',
                                  'mountPath': '/run/mqtt-pki'}]}, {
                'name': 'pkibroker', 'image': 'worker',
                'env': [{'name': 'KEEP', 'value': 'yes'}],
                'volumeMounts': []}],
            'volumes': [
                {'name': 'runtime', 'secret': {
                    'secretName': 'mqtt-pki-runtime'}},
                {'name': 'pki-service-root', 'configMap': {
                    'name': 'pki-service-host-root'}}]}}}}
        settings = {'EMQX_PKI_HOST_NAME': m.MQTT_HOST}
        result = m.managed_mqtt_template(
            owner, 'managed-image', settings, 'phase')
        pod = result['spec']
        containers = {item['name']: item for item in pod['containers']}
        self.assertEqual(containers['mqtt']['image'], 'managed-image')
        self.assertEqual(containers['pkibroker'],
                         owner['spec']['template']['spec']['containers'][1])
        refs = [item.get('valueFrom', {}).get('secretKeyRef', {}).get('name')
                for item in containers['mqtt']['env']]
        self.assertIn(m.MQTT_RUNTIME_SECRET, refs)
        volumes = {item['name']: item for item in pod['volumes']}
        self.assertEqual(volumes['mqtt-host-state']['persistentVolumeClaim']
                         ['claimName'], m.MQTT_HOST_PVC)
        self.assertEqual(volumes['runtime']['secret']['secretName'],
                         m.MQTT_RUNTIME_SECRET)
        self.assertEqual(volumes['mqtt-host-runtime'], {
            'name': 'mqtt-host-runtime', 'emptyDir': {}})
        self.assertEqual(volumes['mqtt-callback-ca']['configMap']['name'],
                         m.MQTT_CALLBACK_CA)
        initializer = next(item for item in pod['initContainers']
                           if item['name'] == 'prepare-mqtt-host-state')
        self.assertEqual(initializer['securityContext']['runAsUser'], 1000)
        self.assertEqual(initializer['securityContext']['runAsGroup'], 1000)
        self.assertIn('chmod 700 /var/lib/emqx-pki/identity',
                      initializer['args'][0])
        self.assertIn('chmod 700 /run/emqx-pki/private',
                      initializer['args'][0])
        self.assertEqual({item['name'] for item in
                          initializer['volumeMounts']},
                         {'mqtt-host-state', 'mqtt-host-runtime'})
        self.assertEqual(owner['spec']['template']['spec']['containers'][0]
                         ['image'], 'static')

    def test_current_host_requires_one_unrevoked_registered_served_leaf(self):
        runner = object.__new__(m.MQTTHostRun)
        runner.inspect_host_state = Mock(return_value={
            'pending': False, 'subject': m.MQTT_HOST,
            'fingerprint': 'f' * 64})
        row = {'fingerprint': 'f' * 64, 'issuer_id': 'issuer',
               'caller': m.EMQX_SERVICE_SUBJECT, 'status': 'succeeded',
               'revoked_at': None}
        runner.server_rows = Mock(return_value=[row])
        runner.served_fingerprint = Mock(return_value='f' * 64)
        runner.host_state_digest = Mock(return_value='s' * 64)
        runner.obj = Mock(return_value={'metadata': {'uid': 'pvc'}})
        owner = {'metadata': {'uid': 'deployment'}, 'spec': {'template': {
            'spec': {'containers': [
                {'name': 'mqtt', 'image': 'mqtt-image'},
                {'name': 'pkibroker', 'image': 'worker-image'}]}}}}
        current = runner.current_host({'issuer_id': 'issuer'}, owner)
        self.assertEqual(current['row'], row)
        self.assertEqual(current['pvc_uid'], 'pvc')
        self.assertEqual(current['images'], {
            'mqtt': 'mqtt-image', 'pkibroker': 'worker-image'})
        runner.server_rows = Mock(return_value=[dict(row, revoked_at='now')])
        with self.assertRaisesRegex(RuntimeError,
                                    'current MQTT host is not admitted'):
            runner.current_host({'issuer_id': 'issuer'}, owner)

    def test_managed_host_requires_the_service_identity_caller(self):
        runner = object.__new__(m.MQTTHostRun)
        runner.inspect_host_state = Mock(return_value={
            'pending': False, 'subject': m.MQTT_HOST,
            'fingerprint': 'f' * 64})
        row = {'fingerprint': 'f' * 64, 'issuer_id': 'issuer',
               'caller': m.EMQX_SERVICE_SUBJECT, 'status': 'succeeded',
               'revoked_at': None}
        runner.server_rows = Mock(return_value=[row])
        runner.served_fingerprint = Mock(return_value='f' * 64)
        runner.host_state_digest = Mock(return_value='s' * 64)
        runner.obj = Mock(return_value={'metadata': {'uid': 'pvc'}})
        owner = {'metadata': {'uid': 'deployment'}, 'spec': {'template': {
            'spec': {'containers': [{'name': 'mqtt', 'image': 'mqtt-image'}]}}}}
        self.assertEqual(runner.current_host_with_caller(
            {'issuer_id': 'issuer'}, m.EMQX_SERVICE_SUBJECT, owner)['row'], row)
        with self.assertRaisesRegex(RuntimeError,
                                    'current MQTT host is not admitted'):
            runner.current_host_with_caller({'issuer_id': 'issuer'},
                                            'emqx-pki', owner)

    def test_root_fingerprints_are_certificate_der_digests(self):
        roots = ('-----BEGIN CERTIFICATE-----\n'
                 'YQ==\n-----END CERTIFICATE-----\n'
                 '-----BEGIN CERTIFICATE-----\nYg==\n'
                 '-----END CERTIFICATE-----\n')
        self.assertEqual(m.MQTTHostRun.root_fingerprints(roots), {
            __import__('hashlib').sha256(b'a').hexdigest(),
            __import__('hashlib').sha256(b'b').hexdigest()})

    def test_root_policy_advance_preserves_cumulative_history(self):
        old = {'issuer_id': 'old', 'operation_id': 'old-op',
               'certificate_sha256': 'a' * 64,
               'public_key_sha256': 'b' * 64}
        target = {'issuer_id': 'target',
                  'certificate_fingerprint_sha256': 'c' * 64}
        before = {'environment': 'dev', 'trust_domain': 'mqtt', 'version': 4,
                  'distrusted_roots': [old]}
        added = {'issuer_id': 'target', 'operation_id': 'new-op',
                 'certificate_sha256': 'c' * 64,
                 'public_key_sha256': 'd' * 64}
        after = dict(before, version=7, distrusted_roots=[old, added])
        m.validate_root_policy_advance(
            before, after, target, {'operation_id': 'new-op'})
        with self.assertRaisesRegex(RuntimeError, 'policy differs'):
            m.validate_root_policy_advance(
                before, dict(after, distrusted_roots=[added]), target,
                {'operation_id': 'new-op'})
        with self.assertRaisesRegex(RuntimeError, 'policy differs'):
            changed = dict(old, certificate_sha256='e' * 64)
            m.validate_root_policy_advance(
                before, dict(after, distrusted_roots=[changed, added]), target,
                {'operation_id': 'new-op'})

    def test_tls_denial_requires_a_certificate_validation_failure(self):
        result = type('Result', (), {
            'returncode': 1,
            'stderr': ('verified TLS peer unavailable: tls: failed to verify '
                       'certificate: x509: certificate signed by unknown authority')})()
        self.assertTrue(m.tls_verification_denied(result))
        self.assertFalse(m.tls_verification_denied(
            type('Result', (), {'returncode': 1,
                                'stderr': 'connection refused'})()))
        self.assertFalse(m.tls_verification_denied(
            type('Result', (), {'returncode': 0,
                                'stderr': 'certificate signed by unknown authority'})()))

    def test_consumer_restart_requires_new_pod_uids(self):
        runner = object.__new__(m.MQTTHostRun)
        runner.mqtt_consumer_state = Mock(
            side_effect=lambda name: {'deployment': name})
        runner.deployment_pod = Mock(side_effect=[
            {'metadata': {'uid': 'api-old'}},
            {'metadata': {'uid': 'api-new'}},
            {'metadata': {'uid': 'logs-old'}},
            {'metadata': {'uid': 'logs-new'}}])
        owner = {'spec': {'template': {'metadata': {'annotations': {}}}}}
        runner.obj = Mock(return_value=owner)
        runner.scoped_patch = Mock()
        runner.wait_available = Mock()
        evidence = runner.restart_mqtt_consumers('withdraw-policy')
        self.assertEqual(evidence['video-cloud-api']['previous_pod_uid'],
                         'api-old')
        self.assertEqual(evidence['video-cloud-logingester'][
                         'replacement_pod_uid'], 'logs-new')
        annotations = [call.args[2][0]['value']['metadata']['annotations'][
                       'rtk.cloud/mqtt-root-distrust-restart']
                       for call in runner.scoped_patch.call_args_list]
        self.assertEqual(len(set(annotations)), 2)
        self.assertTrue(all(value.startswith('withdraw-policy-')
                            for value in annotations))


if __name__ == '__main__':
    unittest.main()
