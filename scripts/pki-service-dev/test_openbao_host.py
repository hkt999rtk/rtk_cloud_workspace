import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location(
    'openbao_host', Path(__file__).with_name('openbao_host.py'))
o = importlib.util.module_from_spec(spec)
spec.loader.exec_module(o)


class OpenBaoHostTests(unittest.TestCase):
    def test_certissuer_route_is_exact_and_preserves_source(self):
        owner = {'spec': {'template': {'metadata': {}, 'spec': {
            'containers': [{
                'name': 'certissuer', 'image': 'old',
                'env': [{'name': 'EXISTING', 'value': 'kept'}]}]}}}}
        root = {'certificate_fingerprint_sha256': 'a' * 64}
        result = o.certissuer_route_template(owner, 'pinned', root)
        container = result['spec']['containers'][0]
        values = {item['name']: item.get('value')
                  for item in container['env']}
        self.assertEqual(container['image'], 'pinned')
        self.assertEqual(values['EXISTING'], 'kept')
        self.assertEqual(
            values['CERT_ISSUER_OPENBAO_HOST_DNS_NAMES'],
            ','.join(o.OPENBAO_HOST_NAMES))
        self.assertEqual(
            values['CERT_ISSUER_OPENBAO_HOST_CLIENT_CN_PATTERN'],
            '^service:openbao$')
        self.assertEqual(owner['spec']['template']['spec']['containers'][0]
                         ['image'], 'old')

    def test_certissuer_route_rejects_existing_configuration(self):
        owner = {'spec': {'template': {'metadata': {}, 'spec': {
            'containers': [{
                'name': 'certissuer', 'image': 'old', 'env': [{
                    'name': 'CERT_ISSUER_OPENBAO_HOST_PKI_ROOT_SHA256',
                    'value': 'configured'}]}]}}}}
        with self.assertRaises(RuntimeError):
            o.certissuer_route_template(
                owner, 'pinned',
                {'certificate_fingerprint_sha256': 'a' * 64})

    def test_authority_loader_does_not_shadow_acceptance_root_state(self):
        self.assertFalse('root' in o.OpenBaoHostRun.__dict__)
        self.assertTrue(callable(o.OpenBaoHostRun.openbao_root))

    def test_intermediate_loader_accepts_requested_lifecycle_status(self):
        saved = {
            'issuer_id': 'issuer-1', 'environment': 'dev',
            'trust_domain': 'openbao_tls', 'kind': 'intermediate',
            'parent_issuer_id': 'root-1', 'issuer_version': 1,
            'signer_provider': 'openbao', 'signer_reference': 'mount',
            'certificate_fingerprint_sha256': 'a' * 64,
            'certificate_pem': 'certificate',
            'trust_bundle_version': 'bundle',
            'service_client_ids': ['service:openbao'],
            'server_dns_names': o.OPENBAO_HOST_NAMES, 'status': 'ready'}
        runner = object.__new__(o.OpenBaoHostRun)
        runner.args = type('Args', (), {'intermediate': '/evidence'})()
        runner.openbao_root = lambda status: {'issuer_id': 'root-1'}
        runner.api = lambda path: dict(saved, status='active')
        runner.save = lambda *args: None
        original = o.m.read
        try:
            o.m.read = lambda path: (saved if path.name ==
                                      'intermediate-ready.json' else
                                      {'operation_id': 'operation-1'})
            _, issuer, _ = runner.ready_intermediate('active')
        finally:
            o.m.read = original
        self.assertEqual(issuer['status'], 'active')

    def test_activation_gate_uses_operation_requester(self):
        calls = []
        runner = object.__new__(o.OpenBaoHostRun)
        runner.api = lambda *args, **kwargs: calls.append((args, kwargs))
        runner.require_activation_blocked({'operation_id': 'operation-1'})
        self.assertEqual(calls, [(('/operations/operation-1/activate', {}, 409),
                                  {})])

    def test_unconfigured_domain_denial_uses_operation_requester(self):
        calls = []
        runner = object.__new__(o.OpenBaoHostRun)
        runner.api = lambda *args, **kwargs: calls.append((args, kwargs))
        runner.require_activation_policy_absent({'operation_id': 'operation-1'})
        self.assertEqual(calls, [(('/operations/operation-1/activate', {}, 403),
                                  {})])

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
        env = {item['name']: item.get('value') for item in
               owner['spec']['template']['spec']['containers'][0]['env']}
        self.assertEqual(env['PKI_REQUIRED_CONSUMERS_OPENBAO_TLS'],
                         'certissuer,pki-controller')

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

    def test_recovery_changes_only_image_for_staged_client(self):
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
            owner, 'broken', 'combined', 'manifest', root)
        recovered = runner.recovery_client_template(
            owner, 'fixed', 'combined', 'manifest', root)
        self.assertEqual(recovered['spec']['containers'][0]['image'], 'fixed')
        self.assertEqual(recovered['spec']['volumes'],
                         owner['spec']['template']['spec']['volumes'])

    def test_intermediate_template_changes_only_manifest_and_annotation(self):
        owner = {'metadata': {'name': 'certissuer'}, 'spec': {'template': {
            'metadata': {}, 'spec': {'containers': [{
                'name': 'certissuer', 'image': 'pinned'}], 'volumes': [{
                    'name': 'openbao-server-bundles',
                    'configMap': {'name': 'root-only'}}]}}}}
        runner = object.__new__(o.OpenBaoHostRun)
        got = runner.intermediate_template(
            owner, 'pinned', 'root-and-intermediate', 'run-1')
        self.assertEqual(got['spec']['volumes'][0]['configMap']['name'],
                         'root-and-intermediate')
        self.assertEqual(got['metadata']['annotations'][
            'rtk.cloud/openbao-intermediate-staging'], 'run-1')
        self.assertEqual(owner['spec']['template']['spec']['volumes'][0]
                         ['configMap']['name'], 'root-only')


if __name__ == '__main__':
    unittest.main()
