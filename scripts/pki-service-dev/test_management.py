import importlib.util
from pathlib import Path
import unittest
import tempfile
import json
from unittest.mock import Mock

spec = importlib.util.spec_from_file_location('managed_account', Path(__file__).with_name('management.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class ManagementTests(unittest.TestCase):
    def owner(self):
        return {'spec': {'replicas': 1, 'template': {'spec': {
            'containers': [{'name': 'app', 'image': 'old', 'env': [
                {'name': 'PKI_CONTROLLER_CLIENT_KEY', 'value': '/old.key'},
                {'name': 'DATABASE_URL', 'valueFrom': {'secretKeyRef': {'name': 'retained', 'key': 'dsn'}}}],
                'volumeMounts': [{'name': 'pki-auth', 'mountPath': '/run/pki-auth'}]}],
            'volumes': [{'name': 'pki-auth', 'secret': {'secretName': 'account-manager-pki-auth', 'defaultMode': 0o440}}],
            'securityContext': {'runAsNonRoot': True}}}}}

    def test_opt_in_removes_api_key_access_but_preserves_human_keys_and_settings(self):
        source = self.owner()
        pod = m.managed_template(source, 'app-image', 'owner-image', 'a' * 64)['spec']
        api, owner = pod['containers']
        self.assertEqual(next(e for e in api['env'] if e['name'] == 'DATABASE_URL'),
                         source['spec']['template']['spec']['containers'][0]['env'][1])
        self.assertEqual({e['name']: e.get('value') for e in api['env']}['PKI_CONTROLLER_CLIENT_KEY'], '')
        self.assertEqual([e['key'] for e in pod['volumes'][0]['secret']['items']], m.JWT_FILES)
        self.assertFalse({'management-state', 'management-bootstrap'} & {v['name'] for v in api['volumeMounts']})
        self.assertTrue({'management-state', 'management-bootstrap'} <= {v['name'] for v in owner['volumeMounts']})
        self.assertNotIn('pki-auth', {v['name'] for v in owner['volumeMounts']})
        self.assertTrue(pod['securityContext']['runAsNonRoot'])
        self.assertEqual(len(source['spec']['template']['spec']['containers']), 1)
        self.assertEqual(pod['initContainers'][0]['volumeMounts'], [{'name': 'management-socket', 'mountPath': '/socket'}])

    def test_owner_has_independent_verified_origins_and_no_tcp_listener(self):
        env = m.owner_settings('b' * 64)
        self.assertTrue(env['PKI_MANAGEMENT_SOCKET'].startswith('/run/account-pki/private/'))
        for remote, name in [('ISSUER', 'certissuer'), ('CONTROLLER', 'pki-controller')]:
            self.assertEqual(env['PKI_MANAGEMENT_' + remote + '_SERVER_PKI_NAME'], name + '.' + m.NS + '.svc')
            self.assertEqual(env['PKI_MANAGEMENT_' + remote + '_SERVER_PKI_ROOT_SHA256'], 'b' * 64)
        self.assertNotIn('PKI_MANAGEMENT_LISTEN', env)

    def test_scoped_render_only_applies_api_pin_and_rejects_namespace_drift(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            desired = self.owner()
            desired['metadata'] = {'namespace': m.AM_NS, 'name': 'account-manager'}
            desired['spec']['strategy'] = {'type': 'Recreate'}
            desired['spec']['template'] = m.managed_template(desired, 'old-api', 'retained-owner', 'a' * 64)
            path = base / 'pki/controller-bootstrap/rollout/account-manager-deployment.json'
            m.m.write(path, desired)
            image = 'ghcr.io/hkt999rtk/rtk_cloud_dev/account-manager@sha256:' + 'b' * 64
            m.m.write(base / 'operator/env/PKI_ACCOUNT_MANAGER_API_IMAGE', image)
            rendered = m.render_management(base)
            self.assertEqual(rendered['spec']['template']['spec']['containers'][0]['image'], image)
            self.assertEqual(rendered['spec']['template']['spec']['containers'][1]['image'], 'retained-owner')
            owner_image = 'ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:' + 'c' * 64
            m.m.write(base / 'operator/env/PKI_ACCOUNT_MANAGER_OWNER_IMAGE', owner_image)
            pinned = m.render_management(base)['spec']['template']['spec']['containers']
            self.assertEqual([c['image'] for c in pinned], [image, owner_image])
            desired['metadata']['namespace'] = 'video-cloud-staging-account-manager'
            m.m.write(path, desired)
            with self.assertRaises(RuntimeError): m.render_management(base)

    def test_registry_issuance_requires_succeeded_status_and_no_revocation(self):
        runner = object.__new__(m.ManagementRun)
        row = {'request_id': 'stable', 'status': 'succeeded', 'revoked_at': None}
        runner.sql = Mock(return_value=json.dumps(row))
        self.assertEqual(runner.issuance(), row)
        for raw in ('', json.dumps(dict(row, status='issuing')), json.dumps(dict(row, revoked_at='now')),
                    json.dumps(row) + '\n' + json.dumps(row)):
            runner.sql.return_value = raw
            with self.assertRaises(RuntimeError): runner.issuance()

    def test_duplicate_or_multiple_identity_owner_is_rejected(self):
        owner = self.owner()
        owner['spec']['replicas'] = 2
        with self.assertRaises(RuntimeError): m.managed_template(owner, 'app', 'owner', 'a' * 64)
        owner['spec']['replicas'] = 1
        owner['spec']['template'] = m.managed_template(owner, 'app', 'owner', 'a' * 64)
        with self.assertRaises(RuntimeError): m.managed_template(owner, 'app', 'owner', 'a' * 64)


if __name__ == '__main__': unittest.main()
