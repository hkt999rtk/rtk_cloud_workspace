import importlib.util
from pathlib import Path
import unittest
import tempfile
from unittest.mock import Mock
import json

spec = importlib.util.spec_from_file_location('managed_hosts', Path(__file__).with_name('hosts.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class ManagedHostTests(unittest.TestCase):
    def test_private_state_allows_setgid_inheritance_without_group_access(self):
        for directory in ('700', '2700'):
            self.assertEqual(m.private_state_digest(directory + '\n600\n' + 'a' * 64 + '  state.json\n'), 'a' * 64)
        for directory, file_mode in [('770', '600'), ('2770', '600'), ('700', '640'), ('4700', '600')]:
            with self.assertRaises(RuntimeError):
                m.private_state_digest(directory + '\n' + file_mode + '\n' + 'a' * 64 + '  state.json\n')

    def test_preparation_recovery_stops_if_signing_claim_exists(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory)
            m.m.write(source / 'report.json', {'status': 'failed', 'phase': 'prepare'})
            issuer = {'issuer_id': 'fixture'}
            m.m.write(source / 'service-intermediate.json', issuer)
            m.m.write(source / 'certissuer/request.json', {'request_id': 'dev-server-' + 'a' * 32})
            m.m.write(source / 'certissuer/pod-reference.json', {})
            runner = object.__new__(m.HostRun)
            runner.args = Mock(prepared=str(source))
            runner.hierarchy = Mock(return_value=({}, issuer))
            runner.kube = Mock(return_value='1')
            runner.finish_host = Mock()
            with self.assertRaisesRegex(RuntimeError, 'signing claim exists'):
                runner.resume_prepare()
            runner.finish_host.assert_not_called()

    def test_controller_recovery_rejects_changed_private_state_before_restart(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory)
            m.m.write(source / 'report.json', {'status': 'failed', 'phase': 'controller'})
            owner = {'spec': {'template': {'spec': {}}, 'strategy': {'type': 'Recreate'}}}
            m.m.write(source / ('after-' + m.NS + '-pki-controller-deployment.json'), owner)
            m.m.write(source / 'pki-controller-served.json', {'state_sha256': 'original'})
            runner = object.__new__(m.HostRun)
            runner.args = Mock(adoption=str(source))
            runner.hierarchy = Mock()
            runner.prepared = Mock(return_value=(source, {'pvc': 'owned', 'pvc_uid': 'uid'}, {}))
            runner.obj = Mock(side_effect=[owner, {'metadata': {'uid': 'uid'}}])
            runner.state_digest = Mock(return_value='changed')
            runner.rollout = Mock()
            runner.kube = Mock()
            with self.assertRaisesRegex(RuntimeError, 'state changed'):
                runner.resume_controller()
            runner.rollout.assert_not_called()
            runner.kube.assert_not_called()

    def test_signer_audit_uses_actual_workload_service_account_and_revokes_token(self):
        runner = object.__new__(m.HostRun)
        runner.obj = Mock(return_value={'spec': {'template': {'spec': {'serviceAccountName': 'certissuer-pki'}}}})
        runner.kube = Mock(return_value='workload-jwt')
        runner.bao = Mock(side_effect=['["key-id"]', '{"auth":{"client_token":"audit-token"}}',
                                     '{"data":{"capabilities":["deny"]}}', ''])
        with self.assertRaisesRegex(RuntimeError, 'capability differs'):
            runner.verify_server_signer({'signer_reference': 'mount'})
        self.assertEqual(runner.kube.call_args.args[0][4], 'certissuer-pki')
        self.assertEqual(runner.bao.call_args.args[0], ['write', 'auth/token/revoke', '-'])

    def test_client_signer_requires_explicit_audit_policy(self):
        for enabled in (False, True):
            runner = object.__new__(m.HostRun)
            runner.obj = Mock(return_value={'spec': {'template': {'spec': {'serviceAccountName': 'certissuer-pki'}}}})
            runner.kube = Mock(return_value='workload-jwt')
            runner.check = Mock()
            responses = ['["key-id"]', '{"auth":{"client_token":"audit-token"}}']
            responses += [json.dumps({'data': {'capabilities': c}}) for c in
                          [['update'], ['update'] if enabled else ['deny'], ['deny'], ['deny'], ['deny']]]
            if enabled:
                responses.append(json.dumps({'data': {'capabilities': ['deny']}}))
            runner.bao = Mock(side_effect=responses + [''])
            runner.verify_server_signer({'signer_reference': 'mount'}, service_client=enabled)
            self.assertEqual(runner.check.call_args.args[1]['service_client_signing_allowed'], enabled)
            self.assertEqual(runner.bao.call_args.args[0], ['write', 'auth/token/revoke', '-'])

    def test_host_settings_keep_server_key_and_client_identity_separate(self):
        for name, prefix in [('pki-controller', 'PKI'), ('certissuer', 'CERT_ISSUER')]:
            settings = m.managed_settings(name, 'a' * 64)
            self.assertNotEqual(settings[prefix + '_SERVER_KEY'], settings[prefix + '_HOST_RENEWAL_CLIENT_KEY'])
            self.assertEqual(settings[prefix + '_HOST_NAME'], name + '.' + m.NS + '.svc')
            self.assertEqual(settings[prefix + '_HOST_RENEWAL_URL'], 'https://certissuer.' + m.NS + '.svc:9443')
        with self.assertRaises(RuntimeError):
            m.managed_settings('unreviewed-host', 'a' * 64)

    def test_api_controller_identity_verification_rejects_static_key_mount(self):
        with tempfile.TemporaryDirectory() as directory:
            runner = object.__new__(m.HostRun)
            runner.foundation = Path(directory)
            m.m.write(runner.foundation / 'device-root-active.json', {'issuer_id': 'device-root'})
            required = {
                'VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE': '/var/lib/identity.json',
                'VIDEO_CLOUD_CONTROLLER_ROOT_SHA256': 'a' * 64,
                'VIDEO_CLOUD_CONTROLLER_IDENTITY_SERVER_PKI_ROOT_SHA256': 'a' * 64,
                'VIDEO_CLOUD_CONTROLLER_IDENTITY_SERVER_PKI_NAME': 'controller.example.test',
                'VIDEO_CLOUD_CONTROLLER_IDENTITY_TLS_CA': '/run/controller/ca.pem',
                'VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_URL': 'https://issuer.example.test',
                'VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_SERVER_PKI_ROOT_SHA256': 'a' * 64,
                'VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_SERVER_PKI_NAME': 'issuer.example.test',
                'VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_TLS_CA': '/run/controller/ca.pem',
            }
            app = {'name': 'app', 'env': [{'name': name, 'value': value} for name, value in required.items()], 'volumeMounts': []}
            deployment = {'spec': {'template': {'spec': {'containers': [app], 'volumes': []}}}}
            runner.obj = Mock(side_effect=[deployment])
            runner.api = Mock(return_value={'crl_sha256': 'digest'})
            runner.kube = Mock(return_value='HTTP/1.1 200 OK\r\n\r\ndigest')
            runner.check = Mock()
            runner.verify_controller_callers({'issuer_id': 'root'})
            app['volumeMounts'].append({'name': 'pki-management', 'mountPath': '/run/pki-management'})
            runner.obj = Mock(side_effect=[deployment])
            with self.assertRaisesRegex(RuntimeError, 'static controller credential mount remains'):
                runner.verify_controller_callers({'issuer_id': 'root'})

    def test_private_volume_transition_preserves_existing_settings_and_rejects_replay(self):
        owner = {'spec': {'replicas': 1, 'template': {'spec': {
            'containers': [{'env': [{'name': 'DATABASE_URL', 'valueFrom': {'secretKeyRef': {'name': 'private', 'key': 'dsn'}}}],
                            'volumeMounts': [{'name': 'retained', 'mountPath': '/retained'}]}],
            'volumes': [{'name': 'retained', 'secret': {'secretName': 'private'}}], 'securityContext': {'runAsNonRoot': True}}}}}
        result = m.managed_template(owner, m.managed_settings('certissuer', 'a' * 64), 'certissuer-service-identity')
        self.assertEqual(result['spec']['containers'][0]['env'][0], owner['spec']['template']['spec']['containers'][0]['env'][0])
        self.assertEqual(result['spec']['volumes'][0], owner['spec']['template']['spec']['volumes'][0])
        self.assertTrue(result['spec']['securityContext']['runAsNonRoot'])
        self.assertEqual(result['spec']['securityContext']['fsGroupChangePolicy'], 'OnRootMismatch')
        self.assertEqual(len(owner['spec']['template']['spec']['volumes']), 1)
        owner['spec']['template'] = result
        with self.assertRaises(RuntimeError):
            m.managed_template(owner, {}, 'other')
        owner['spec']['replicas'] = 2
        with self.assertRaises(RuntimeError):
            m.managed_template(owner, {}, 'other')


if __name__ == '__main__':
    unittest.main()
