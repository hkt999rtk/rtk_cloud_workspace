import importlib.util
from pathlib import Path
import unittest
import datetime as dt
import copy
import json
import tempfile
from types import SimpleNamespace
from unittest.mock import Mock, patch


spec = importlib.util.spec_from_file_location(
    'openbao_host', Path(__file__).with_name('openbao_host.py'))
o = importlib.util.module_from_spec(spec)
spec.loader.exec_module(o)


class OpenBaoHostTests(unittest.TestCase):
    def test_recovery_discovers_before_signing_and_never_repeats_attempt(self):
        for existing, attempted in [(True, False), (False, False), (False, True)]:
            with self.subTest(existing=existing, attempted=attempted), tempfile.TemporaryDirectory() as directory:
                base = Path(directory)
                (base / 'pki').mkdir()
                (base / 'report.json').write_text(json.dumps({
                    'environment': 'dev', 'phase': 'exercise-provider-outage', 'status': 'failed'}))
                (base / 'outage-policy.json').write_text(json.dumps({'metadata': {
                    'namespace': o.NS, 'name': 'pki-openbao-certissuer-outage-test'}}))
                request = 'fe4f7d98-ad49-4893-95a4-3179157bdb7a'
                marker = base / 'pki' / ('openbao-recovery-' + request + '.json')
                if attempted:
                    marker.write_text('{}')
                now = dt.datetime.now(dt.timezone.utc)
                claim = {'issuer_id': request, 'created_at': (now - dt.timedelta(hours=2)).isoformat(),
                         'status': 'issuing', 'revoked_at': None, 'ttl_days': 30,
                         'subject': 'service:openbao', 'csr_pem': 'csr', 'request_digest': 'digest'}
                issuer = {'signer_reference': 'pki-issuers/service/' + request + '/v5',
                          'status': 'active', 'trust_domain': 'service',
                          'not_after': (now + dt.timedelta(days=365)).isoformat()}
                runner = o.OpenBaoHostRun.__new__(o.OpenBaoHostRun)
                runner.base, runner.openssl = base, 'openssl'
                runner.args = SimpleNamespace(request_id=request, failed=str(base))
                runner.save, runner.check = Mock(), Mock()
                runner.inspect_client_state = Mock(side_effect=[
                    {'pending': True, 'pending_request_id': request, 'fingerprint': 'old'},
                    {'pending': False, 'fingerprint': 'new'}])
                runner.inspect_host_state = Mock(return_value={'fingerprint': 'host'})
                runner.sql = Mock(side_effect=[json.dumps(claim)] +
                    ([] if existing else [json.dumps(claim)]) +
                    [json.dumps(dict(claim, status='succeeded'))])
                runner.api = Mock(side_effect=[issuer, {}])
                pods = {'items': [{'metadata': {'uid': 'new-pod'}, 'status': {'phase': 'Running',
                    'startTime': (now - dt.timedelta(hours=1)).isoformat()}}]}
                runner.kube = Mock(side_effect=[''] + ([] if existing else [json.dumps(pods), 'jwt']))
                responses = [json.dumps(['ab:cd'] if existing else [])]
                if existing:
                    responses += [json.dumps({'data': {'certificate': 'cert', 'revocation_time': 0}})]
                else:
                    responses += [json.dumps({'auth': {'client_token': 'token'}})]
                    if not attempted:
                        responses += [json.dumps({'data': {'certificate': 'cert', 'serial_number': 'ab:cd'}})]
                    responses += ['{}']
                runner.bao = Mock(side_effect=responses)
                with patch.object(o.m, 'command', return_value='same-public-key'):
                    if attempted:
                        with self.assertRaises(FileExistsError):
                            runner.recover_provider_outage()
                    else:
                        runner.recover_provider_outage()
                signs = [c for c in runner.bao.call_args_list if '/sign/service-client' in str(c)]
                self.assertEqual(len(signs), int(not existing and not attempted))
                self.assertEqual(runner.check.call_count, int(not attempted))

    def test_recovery_retains_original_ttl_ceiling(self):
        now = dt.datetime(2026, 9, 12, tzinfo=dt.timezone.utc)
        claim = {'created_at': (now - dt.timedelta(hours=2)).isoformat(),
                 'status': 'issuing', 'revoked_at': None,
                 'subject': 'service:openbao', 'ttl_days': 30}
        issuer = {'status': 'active', 'trust_domain': 'service',
                  'not_after': (now + dt.timedelta(days=365)).isoformat()}
        pods = [{'metadata': {}, 'status': {'phase': 'Running',
                 'startTime': (now - dt.timedelta(hours=1)).isoformat()}}]
        self.assertEqual(o.recovery_ttl(claim, issuer, pods, now),
                         30 * 86400 - 2 * 3600 - 60)
        issuer['not_after'] = (now + dt.timedelta(days=31)).isoformat()
        self.assertEqual(o.recovery_ttl(claim, issuer, pods, now), 86400 - 60)
        for invalid in ([], [dict(pods[0], metadata={'deletionTimestamp': now.isoformat()})],
                        [{'metadata': {}, 'status': {'phase': 'Running',
                          'startTime': claim['created_at']}}],
                        [{'metadata': {}, 'status': {'phase': 'Running',
                          'startTime': (now - dt.timedelta(seconds=10)).isoformat()}}]):
            with self.subTest(pods=invalid), self.assertRaises(RuntimeError):
                o.recovery_ttl(claim, issuer, invalid, now)
        for field, value in [('status', 'succeeded'), ('revoked_at', now.isoformat()),
                             ('subject', 'service:other'), ('ttl_days', 0)]:
            invalid = copy.deepcopy(claim)
            invalid[field] = value
            with self.subTest(field=field), self.assertRaises(RuntimeError):
                o.recovery_ttl(invalid, issuer, pods, now)

    def test_managed_openbao_config_replaces_only_listener_keys(self):
        source = {'extraconfig-from-values.hcl': (
            'listener "tcp" {\n'
            ' tls_cert_file = "/openbao/tls/tls.crt"\n'
            ' tls_key_file = "/openbao/tls/tls.key"\n}\n')}
        result = o.managed_openbao_config(source)
        self.assertIn('/run/openbao-pki/private/current/chain.pem',
                      result['extraconfig-from-values.hcl'])
        self.assertIn('/run/openbao-pki/private/current/key.pem',
                      result['extraconfig-from-values.hcl'])
        self.assertNotIn('/openbao/tls/',
                         result['extraconfig-from-values.hcl'])

    def test_managed_openbao_template_has_no_seed_or_static_tls(self):
        start = ('prepare\n/usr/local/bin/docker-entrypoint.sh bao server '
                 '-config=/tmp/storageconfig.hcl \n')
        owner = {'spec': {
            'replicas': 1, 'updateStrategy': {'type': 'OnDelete'},
            'template': {'metadata': {}, 'spec': {
                'containers': [{
                    'name': 'openbao',
                    'image': 'quay.io/openbao/openbao:2.5.4',
                    'command': ['/bin/sh', '-ec'], 'args': [start],
                    'readinessProbe': {'exec': {'command': [
                        '/bin/sh', '-ec', 'bao status -tls-skip-verify']}},
                    'volumeMounts': [
                        {'name': 'data', 'mountPath': '/openbao/data'},
                        {'name': 'openbao-tls',
                         'mountPath': '/openbao/tls'}]}],
                'volumes': [
                    {'name': 'openbao-tls', 'secret': {
                        'secretName': 'openbao-tls'}},
                    {'name': 'home', 'emptyDir': {}}]}}}}
        host_root = {'certificate_fingerprint_sha256': 'a' * 64}
        service_root = {'certificate_fingerprint_sha256': 'b' * 64}
        result = o.managed_openbao_template(
            owner, 'openbao@sha256:' + 'c' * 64,
            host_root, service_root)
        pod = result['spec']
        self.assertTrue(pod['shareProcessNamespace'])
        self.assertEqual(pod['imagePullSecrets'], [{'name': 'ghcr-pull'}])
        self.assertEqual([item['name'] for item in pod['containers']],
                         ['openbao', 'openbao-pki'])
        self.assertEqual([item['name'] for item in pod['initContainers']],
                         ['openbao-pki-install'])
        self.assertIn('chmod -R go-rwx',
                      pod['initContainers'][0]['args'][0])
        self.assertEqual(pod['securityContext']['fsGroupChangePolicy'],
                         'OnRootMismatch')
        self.assertNotIn('openbao-tls', str(pod))
        self.assertIn('openbao_pid=$!', pod['containers'][0]['args'][0])
        self.assertIn('/ready', pod['containers'][0]
                      ['readinessProbe']['exec']['command'][-1])
        env = {item['name']: item for item in pod['containers'][1]['env']}
        self.assertNotIn(
            'OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_BOOTSTRAP_CERT', env)
        self.assertNotIn('OPENBAO_PKI_SEED_CERT', env)
        self.assertEqual(env['OPENBAO_PKI_HOST_ROOT_SHA256']['value'],
                         'a' * 64)
        self.assertEqual(env['OPENBAO_PKI_SERVICE_CLIENT_ROOT_SHA256']['value'],
                         'b' * 64)

    def test_registry_network_policy_allows_only_openbao_from_secrets(self):
        policy = o.registry_network_policy()
        self.assertEqual(policy['metadata'], {
            'name': 'allow-openbao-pki-registry',
            'namespace': 'video-cloud-dev-platform'})
        self.assertEqual(policy['spec']['podSelector']['matchLabels'], {
            'app.kubernetes.io/name': 'postgresql'})
        source = policy['spec']['ingress'][0]['from']
        self.assertEqual(len(source), 1)
        self.assertEqual(source[0]['namespaceSelector']['matchLabels'], {
            'kubernetes.io/metadata.name': 'video-cloud-dev-secrets'})
        self.assertEqual(source[0]['podSelector']['matchLabels'], {
            'app.kubernetes.io/name': 'openbao'})
        self.assertEqual(policy['spec']['ingress'][0]['ports'], [
            {'port': 5432, 'protocol': 'TCP'}])

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

    def test_provider_verification_uses_exact_origin_and_managed_identity(self):
        owner = {'metadata': {'name': 'certissuer'}, 'spec': {'template': {
            'metadata': {}, 'spec': {'volumes': [{
                'name': 'host-state', 'persistentVolumeClaim': {
                    'claimName': 'certissuer-state'}}], 'containers': [{
                'name': 'certissuer', 'env': [
                    {'name': 'OPENBAO_ADDR', 'value':
                     'https://' + o.OPENBAO_HOST_NAMES[1] + ':8200'},
                    {'name': 'OPENBAO_CACERT', 'value':
                     '/run/openbao-ca/ca.crt'},
                    {'name': 'OPENBAO_SERVER_BUNDLE_MANIFEST', 'value':
                     '/run/openbao-server-bundles/issuers.json'}],
                'volumeMounts': [{'name': 'host-state',
                                  'mountPath': '/var/lib/pki-host'}]}]}}}}
        result = o.provider_verification_template(
            owner, {'certificate_fingerprint_sha256': 'a' * 64},
            'crl-manifest',
            'ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:' +
            'b' * 64, 'run-1')
        env = {item['name']: item.get('value')
               for item in result['spec']['containers'][0]['env']}
        self.assertEqual(env['OPENBAO_SERVER_PKI_NAME'],
                         o.OPENBAO_HOST_NAMES[1])
        self.assertEqual(env['OPENBAO_SERVER_PKI_ROOT_SHA256'], 'a' * 64)
        self.assertEqual(env['OPENBAO_SERVER_PKI_SWEEP_INTERVAL'], '10s')
        self.assertEqual(env['OPENBAO_SERVER_CRL_MANIFEST'],
                         '/run/openbao-server-crls/crls.json')
        self.assertNotIn('OPENBAO_MANAGEMENT_CERT', env)
        self.assertNotIn('OPENBAO_MANAGEMENT_KEY', env)
        self.assertNotIn('OPENBAO_SERVER_PKI_NAME', {
            item['name'] for item in owner['spec']['template']['spec'][
                'containers'][0]['env']})

    def test_provider_crl_manifest_uses_private_retained_state(self):
        issuers = [{'issuer_id': 'root'}, {'issuer_id': 'v1'},
                   {'issuer_id': 'v2'}]
        manifest = o.provider_crl_manifest(issuers)
        self.assertEqual([item['issuer'] for item in manifest], issuers)
        self.assertEqual([item['state_path'] for item in manifest], [
            '/var/lib/pki-host/identity/openbao-tls-crl-root.json',
            '/var/lib/pki-host/identity/openbao-tls-crl-v1.json',
            '/var/lib/pki-host/identity/openbao-tls-crl-v2.json'])

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

    def test_server_only_v2_keeps_v1_during_trust_overlap(self):
        common = {
            'environment': 'dev', 'trust_domain': 'openbao_tls',
            'kind': 'intermediate', 'parent_issuer_id': 'root-1',
            'signer_provider': 'openbao', 'signer_reference': 'mount',
            'server_dns_names': o.OPENBAO_HOST_NAMES}
        v1 = dict(common, issuer_id='issuer-v1', issuer_version=1,
                  certificate_fingerprint_sha256='1' * 64,
                  certificate_pem='v1', trust_bundle_version='bundle-v1',
                  service_client_ids=['service:openbao'], status='active')
        v2 = dict(common, issuer_id='issuer-v2', issuer_version=2,
                  certificate_fingerprint_sha256='2' * 64,
                  certificate_pem='v2', trust_bundle_version='bundle-v2',
                  service_client_ids=[], status='ready')
        runner = object.__new__(o.OpenBaoHostRun)
        runner.args = type('Args', (), {
            'intermediate': '/evidence', 'server_only': True})()
        runner.openbao_root = lambda status: {
            'issuer_id': 'root-1', 'trust_bundle_version': 'bundle-root'}
        runner.api = lambda path: v2 if path.endswith('issuer-v2') else v1
        original = o.m.read
        try:
            o.m.read = lambda path: (
                v2 if path.name == 'intermediate-ready.json' else
                v1 if path.name == 'intermediate-v1.json' else
                {'operation_id': 'operation-v2'})
            root, issuer, _ = runner.ready_intermediate()
            overlap = runner.intermediate_bundle_issuers(root, issuer)
        finally:
            o.m.read = original
        self.assertEqual([item['issuer_id'] for item in overlap],
                         ['root-1', 'issuer-v1', 'issuer-v2'])

    def test_openbao_server_rows_keep_only_exact_host_names(self):
        runner = object.__new__(o.OpenBaoHostRun)
        exact = {'request_id': 'one', 'dns_names': o.OPENBAO_HOST_NAMES}
        other = {'request_id': 'two', 'dns_names': ['other.example']}
        runner.sql = lambda query: '\n'.join(
            o.json.dumps(item) for item in (exact, other))
        self.assertEqual(runner.server_rows(), [exact])

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
