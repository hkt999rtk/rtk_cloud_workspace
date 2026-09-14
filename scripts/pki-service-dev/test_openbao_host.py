import importlib.util
from pathlib import Path
import unittest
import datetime as dt
import copy
import hashlib
import json
import tempfile
from types import SimpleNamespace
from unittest.mock import Mock, patch


spec = importlib.util.spec_from_file_location(
    'openbao_host', Path(__file__).with_name('openbao_host.py'))
o = importlib.util.module_from_spec(spec)
spec.loader.exec_module(o)


class OpenBaoHostTests(unittest.TestCase):
    def test_root_bundle_is_canonicalized_by_der_fingerprint(self):
        first = ('-----BEGIN CERTIFICATE-----\nYg==\n'
                 '-----END CERTIFICATE-----\n')
        second = ('-----BEGIN CERTIFICATE-----\nYQ==\n'
                  '-----END CERTIFICATE-----\n')
        expected = ''.join(pem for _, pem in sorted([
            (hashlib.sha256(b'b').hexdigest(), first),
            (hashlib.sha256(b'a').hexdigest(), second)]))
        self.assertEqual(o.canonical_roots_pem(first + second), expected)
        with self.assertRaises(RuntimeError):
            o.canonical_roots_pem(first + first)

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

    def test_server_recovery_ttl_remains_inside_original_claim_lifetime(self):
        now = dt.datetime(2026, 9, 14, 4, 0, tzinfo=dt.timezone.utc)
        claim = {'created_at': (now - dt.timedelta(minutes=20)).isoformat(),
                 'ttl_days': 30}
        issuer = {'not_after': (now + dt.timedelta(days=365)).isoformat()}
        self.assertEqual(o.recovery_server_ttl(claim, issuer, now),
                         30 * 86400 - 20 * 60 - 60)
        issuer['not_after'] = (now + dt.timedelta(days=30, minutes=10)).isoformat()
        self.assertEqual(o.recovery_server_ttl(claim, issuer, now), 9 * 60)
        with self.assertRaises(RuntimeError):
            o.recovery_server_ttl(claim, issuer, now + dt.timedelta(minutes=9))

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

    def test_provider_root_policy_requires_existing_crl_and_private_state(self):
        owner = {'metadata': {'name': 'certissuer'}, 'spec': {'template': {
            'metadata': {}, 'spec': {'volumes': [{
                'name': 'host-state', 'persistentVolumeClaim': {
                    'claimName': 'certissuer-state'}}], 'containers': [{
                'name': 'certissuer', 'env': [
                    {'name': 'OPENBAO_SERVER_CRL_MANIFEST', 'value': '/crls'},
                    {'name': 'OPENBAO_PKI_CONTROLLER_URL', 'value': 'https://pki.example'},
                    {'name': 'OPENBAO_MANAGEMENT_CA', 'value': '/ca'},
                    {'name': 'OPENBAO_SERVER_PKI_ROOT_SHA256', 'value': 'a' * 64},
                    {'name': 'OPENBAO_SERVER_PKI_NAME', 'value': o.OPENBAO_HOST_NAMES[0]}],
                'volumeMounts': [{'name': 'host-state',
                                  'mountPath': '/var/lib/pki-host'}]}]}}}}
        image = 'ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:' + 'b' * 64
        result = o.provider_root_policy_template(
            owner, {'issuer_id': 'root-1'}, 'root-policy', image, 'run-1')
        container = result['spec']['containers'][0]
        env = {item['name']: item.get('value') for item in container['env']}
        self.assertEqual(env['OPENBAO_SERVER_ROOT_ID'], 'root-1')
        self.assertEqual(env['OPENBAO_SERVER_ROOT_STATE'],
                         o.OPENBAO_ROOT_POLICY_STATE)
        self.assertEqual(env['OPENBAO_SERVER_ROOTS'],
                         o.OPENBAO_ROOT_POLICY_MOUNT + '/roots.pem')
        self.assertEqual(next(item for item in result['spec']['volumes']
                              if item['name'] == 'openbao-server-root-policy')
                         ['configMap']['defaultMode'], 292)
        self.assertNotIn('OPENBAO_SERVER_ROOT_ID', {
            item['name'] for item in owner['spec']['template']['spec'][
                  'containers'][0]['env']})

    def test_provider_root_overlap_keeps_predecessor_policy_owner(self):
        owner = {'metadata': {'name': 'certissuer'}, 'spec': {'template': {
            'metadata': {}, 'spec': {'volumes': [
                {'name': 'host-state', 'persistentVolumeClaim': {
                    'claimName': 'certissuer-state'}},
                {'name': 'openbao-server-root-policy', 'configMap': {
                    'name': 'pki-openbao-transport-root-policy-old',
                    'defaultMode': 292}}], 'containers': [{
                'name': 'certissuer', 'env': [
                    {'name': 'OPENBAO_SERVER_ROOT_ID', 'value': 'old-root'},
                    {'name': 'OPENBAO_SERVER_ROOT_STATE',
                     'value': o.OPENBAO_ROOT_POLICY_STATE},
                    {'name': 'OPENBAO_SERVER_ROOTS',
                     'value': o.OPENBAO_ROOT_POLICY_MOUNT + '/roots.pem'}],
                'volumeMounts': [
                    {'name': 'host-state', 'mountPath': '/var/lib/pki-host'},
                    {'name': 'openbao-server-root-policy',
                     'mountPath': o.OPENBAO_ROOT_POLICY_MOUNT,
                     'readOnly': True}]}]}}}}
        image = ('ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:' +
                 'b' * 64)
        result, state = o.provider_root_overlap_template(
            owner, {'issuer_id': 'old-root'},
            {'issuer_id': '12345678-abcd-efab-cdef-123456789012'},
            'pki-openbao-transport-root-policy-12345678', image, 'run-2')
        container = result['spec']['containers'][0]
        env = {item['name']: item.get('value') for item in container['env']}
        self.assertEqual(env['OPENBAO_SERVER_ROOT_ID'], 'old-root')
        self.assertEqual(env['OPENBAO_SERVER_ROOT_STATE'], state)
        self.assertIn('12345678-ab', state)
        volume = next(item for item in result['spec']['volumes']
                      if item['name'] == 'openbao-server-root-policy')
        self.assertEqual(volume['configMap']['name'],
                         'pki-openbao-transport-root-policy-12345678')
        self.assertEqual(owner['spec']['template']['spec']['containers'][0]
                         ['env'][0]['value'], 'old-root')

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

    def test_intermediate_template_updates_pinned_image_manifest_and_annotation(self):
        owner = {'metadata': {'name': 'certissuer'}, 'spec': {'template': {
            'metadata': {}, 'spec': {'containers': [{
                'name': 'certissuer', 'image': 'old-image'}], 'volumes': [{
                    'name': 'openbao-server-bundles',
                    'configMap': {'name': 'root-only'}}]}}}}
        runner = object.__new__(o.OpenBaoHostRun)
        image = ('ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:' +
                 'a' * 64)
        got = runner.intermediate_template(
            owner, image, 'root-and-intermediate', 'run-1')
        self.assertEqual(got['spec']['volumes'][0]['configMap']['name'],
                         'root-and-intermediate')
        self.assertEqual(got['metadata']['annotations'][
            'rtk.cloud/openbao-intermediate-staging'], 'run-1')
        self.assertEqual(got['spec']['containers'][0]['image'], image)
        self.assertEqual(owner['spec']['template']['spec']['containers'][0]
                         ['image'], 'old-image')
        self.assertEqual(owner['spec']['template']['spec']['volumes'][0]
                         ['configMap']['name'], 'root-only')

    def test_provider_recovery_requires_the_exact_receipt_failure(self):
        runner = object.__new__(o.OpenBaoHostRun)
        runner.args = type('Args', (), {
            'phase': 'install-intermediate-consumers', 'server_only': True,
            'failed': '/private/failed'})()
        runner.context = 'dev-context'
        runner.kube = Mock(return_value='dev-context')
        runner.obj = Mock(return_value={'metadata': {'name': o.NS}})
        with patch.object(o.m, 'read', return_value={
                'status': 'failed', 'phase': 'install-intermediate-consumers',
                'failure': 'wrong failure'}):
            with self.assertRaisesRegex(RuntimeError,
                                        'failed OpenBao intermediate receipt'):
                runner.provider_root_policy_preflight()

    def test_intermediate_bundle_keeps_retiring_parent_root(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory)
            predecessor = {'issuer_id': 'old-intermediate',
                           'parent_issuer_id': 'old-root',
                           'trust_bundle_version': 'old-intermediate-bundle'}
            (source / 'intermediate-predecessor.json').write_text(
                json.dumps(predecessor))
            runner = object.__new__(o.OpenBaoHostRun)
            runner.args = SimpleNamespace(server_only=True,
                                          intermediate=str(source))
            runner.api = Mock(return_value={
                'issuer_id': 'old-root', 'environment': 'dev',
                'trust_domain': 'openbao_tls', 'kind': 'root',
                'status': 'retiring', 'trust_bundle_version': 'old-root-bundle'})
            root = {'issuer_id': 'new-root',
                    'trust_bundle_version': 'new-root-bundle'}
            issuer = {'issuer_id': 'new-intermediate',
                      'trust_bundle_version': 'new-intermediate-bundle'}
            self.assertEqual([item['issuer_id'] for item in
                              runner.intermediate_bundle_issuers(root, issuer)],
                             ['new-root', 'old-root', 'old-intermediate',
                              'new-intermediate'])

    def test_current_host_uses_the_shared_provider_root_overlap(self):
        runner = object.__new__(o.OpenBaoHostRun)
        roots = ('-----BEGIN CERTIFICATE-----\nYg==\n-----END CERTIFICATE-----\n'
                 '-----BEGIN CERTIFICATE-----\nYQ==\n-----END CERTIFICATE-----\n')
        state = {'policy': {'environment': 'dev', 'trust_domain': 'openbao_tls'},
                 'roots_pem': roots}
        owner = {'spec': {'template': {'spec': {'containers': [{
            'name': 'certissuer', 'env': [{'name': 'OPENBAO_SERVER_ROOT_STATE',
            'value': '/var/lib/pki-host/identity/openbao-tls-root-policy-next.json'}]}]}}}}
        controller = copy.deepcopy(owner)
        controller['spec']['template']['spec']['containers'][0]['name'] = 'pki-controller'
        runner.obj = Mock(side_effect=[owner, controller])
        runner.kube = Mock(side_effect=[json.dumps(state), json.dumps(state)])
        runner.openbao_root = Mock(return_value={'certificate_pem': (
            '-----BEGIN CERTIFICATE-----\nYQ==\n-----END CERTIFICATE-----\n')})
        self.assertEqual(runner.provider_transport_roots(),
                         o.canonical_roots_pem(roots))

    def test_server_only_signer_advances_the_certissuer_root_lineage(self):
        root = {'certificate_fingerprint_sha256': 'b' * 64}
        owner = {'spec': {'template': {'metadata': {}, 'spec': {'containers': [{
            'name': 'certissuer', 'env': [{
                'name': 'CERT_ISSUER_OPENBAO_HOST_PKI_ROOT_SHA256',
                'value': 'a' * 64}]}]}}}}
        result = o.certissuer_server_root_template(
            owner, root, 'a' * 64, 'pinned-image', 'v3-rollout')
        env = {item['name']: item.get('value')
               for item in result['spec']['containers'][0]['env']}
        self.assertEqual(env['CERT_ISSUER_OPENBAO_HOST_PKI_ROOT_SHA256'],
                         'b' * 64)
        self.assertEqual(env['CERT_ISSUER_OPENBAO_HOST_PKI_VERIFY_ROOT_SHA256'],
                         'a' * 64)
        self.assertEqual(result['spec']['containers'][0]['image'],
                         'pinned-image')
        self.assertEqual(result['metadata']['annotations'][
            'rtk.cloud/openbao-server-issuer-root'], 'v3-rollout')
        self.assertEqual(owner['spec']['template']['spec']['containers'][0][
            'env'][0]['value'], 'a' * 64)

    def test_host_state_uses_the_configured_successor_path(self):
        runner = o.OpenBaoHostRun.__new__(o.OpenBaoHostRun)
        runner.install_host_probe = Mock(return_value=('openbao-0', 'probe'))
        runner.obj = Mock(return_value={'spec': {'template': {'spec': {
            'containers': [{'name': 'openbao-pki', 'env': [{
                'name': 'OPENBAO_PKI_HOST_IDENTITY_STATE',
                'value': '/var/lib/openbao-pki/host/server-v3.json'}]}]}}}})
        runner.kube = Mock(return_value=json.dumps({'fingerprint': 'host'}))
        self.assertEqual(runner.inspect_host_state(), {'fingerprint': 'host'})
        self.assertIn('/var/lib/openbao-pki/host/server-v3.json',
                      runner.kube.call_args.args[0])

    def test_server_only_transition_reuses_verified_signer_policy(self):
        runner = o.OpenBaoHostRun.__new__(o.OpenBaoHostRun)
        runner.args = SimpleNamespace(
            image='ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:' +
                  'a' * 64,
            policy_evidence='/evidence', server_only=True,
            reuse_signer_policy=True)
        runner.ready_intermediate = Mock(return_value=(
            {'certificate_fingerprint_sha256': 'b' * 64},
            {'issuer_id': 'issuer', 'signer_reference': 'mount',
             'server_dns_names': o.OPENBAO_HOST_NAMES,
             'service_client_ids': []}, None))
        runner.provider_root_policy_preflight = Mock()
        runner.role_policy = Mock()
        runner.obj = Mock(side_effect=[
            {'spec': {'template': {'spec': {'containers': [{'name': 'certissuer', 'env': []}]}}}},
            {'spec': {'template': {'spec': {'containers': [{'name': 'openbao-pki', 'env': [{
                'name': 'OPENBAO_PKI_HOST_ROOT_SHA256', 'value': 'c' * 64}]}]}}}}])
        runner.scoped_patch = Mock(); runner.kube = Mock(); runner.check = Mock()
        with tempfile.TemporaryDirectory() as directory:
            runner.base = Path(directory)
            runner.output = runner.base / 'transition'
            (runner.base / 'pki/controller-bootstrap/rollout').mkdir(parents=True)
            (runner.base / 'pki/controller-bootstrap/rollout/certissuer-service-settings.json').write_text('{}')
            with patch.object(o.m, 'read', return_value={'issuer_id': 'issuer', 'mount': 'mount', 'signer_policy': 'path "x/sign/server" {}'}):
                with patch.object(o.m, 'write'):
                    runner.configure_certissuer()
        runner.role_policy.assert_not_called()


if __name__ == '__main__':
    unittest.main()
