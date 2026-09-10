#!/usr/bin/env python3
"""Dev-only MQTT authority and broker-host rollout, one resumable phase at a time."""
import argparse
import base64
import datetime as dt
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import sys
import uuid


spec = importlib.util.spec_from_file_location(
    'service_hierarchy', Path(__file__).with_name('run.py'))
h = importlib.util.module_from_spec(spec)
spec.loader.exec_module(h)
m, NS = h.m, h.NS

MQTT_CONSUMERS = ['video-cloud-api', 'video-cloud-logingester']
MQTT_HOST = 'mqtt-pki.' + NS + '.svc'
IMAGE_PATTERN = re.compile(
    r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}')


def intermediate_request(root):
    m.require(root['environment'] == 'dev' and root['trust_domain'] == 'mqtt'
              and root['kind'] == 'root' and root['status'] == 'active',
              'active dev MQTT Root required')
    return {'environment': 'dev', 'trust_domain': 'mqtt', 'kind': 'intermediate',
            'parent_issuer_id': root['issuer_id'],
            'server_dns_names': [MQTT_HOST]}


def mqtt_client_settings(root):
    controller = 'https://pki-controller.' + NS + '.svc:18446'
    return {
        'VIDEO_CLOUD_ENV': 'dev',
        'VIDEO_CLOUD_MQTT_TLS': 'true',
        'VIDEO_CLOUD_MQTT_ADDR': MQTT_HOST + ':8883',
        'VIDEO_CLOUD_MQTT_TLS_CA_ROOT_PATH': '/run/pki-mqtt/roots.pem',
        'VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256': root['certificate_fingerprint_sha256'],
        'VIDEO_CLOUD_MQTT_SERVER_PKI_NAME': MQTT_HOST,
        'VIDEO_CLOUD_MQTT_SERVER_PKI_SWEEP_INTERVAL': '10s',
        'VIDEO_CLOUD_MQTT_BUNDLE_MANIFEST': '/run/pki-mqtt/issuers.json',
        'VIDEO_CLOUD_MQTT_BUNDLE_CONTROLLER_URL': controller,
        'VIDEO_CLOUD_MQTT_BUNDLE_MANAGEMENT_CA': '/run/pki-service-root/root.pem',
        'VIDEO_CLOUD_MQTT_BUNDLE_MANAGEMENT_CERT': '/run/pki-mqtt-management/tls.crt',
        'VIDEO_CLOUD_MQTT_BUNDLE_MANAGEMENT_KEY': '/run/pki-mqtt-management/tls.key',
    }


def mqtt_intermediate_manifest(root, issuer):
    return [
        {'issuer_id': root['issuer_id'],
         'trust_bundle_version': root['trust_bundle_version']},
        {'issuer_id': issuer['issuer_id'],
         'trust_bundle_version': issuer['trust_bundle_version']},
    ]


class MQTTHostRun(h.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = (
            'Dev-only dedicated MQTT hierarchy and actual MQTT clients')
        self.report['mqtt_host_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def mqtt_root(self, status=None):
        saved = m.read(Path(self.args.authority) / 'root-ready.json')
        root = self.api('/issuers/' + saved['issuer_id'])
        m.require(root['environment'] == 'dev' and root['trust_domain'] == 'mqtt'
                  and root['kind'] == 'root'
                  and root['certificate_fingerprint_sha256'] ==
                  saved['certificate_fingerprint_sha256']
                  and root['trust_bundle_version'] == saved['trust_bundle_version'],
                  'saved MQTT Root changed')
        if status:
            m.require(root['status'] == status, 'MQTT Root status changed')
        self.save('mqtt-root.json', root)
        return root

    def scoped_patch(self, kind, before, changes):
        name = before['metadata']['name']
        self.save('before-' + name + '-' + kind + '.json', before)
        patches = [
            {'op': 'test', 'path': '/metadata/resourceVersion',
             'value': before['metadata']['resourceVersion']}, *changes]
        changed = json.loads(self.kube([
            '-n', NS, 'patch', kind, name, '--type=json',
            '--patch-file=/dev/stdin', '-o', 'json'], json.dumps(patches)))
        desired = {key: changed[key] for key in
                   ('apiVersion', 'kind', 'spec', 'data', 'type', 'immutable')
                   if key in changed}
        desired['metadata'] = {key: changed['metadata'][key] for key in
                               ('name', 'namespace', 'labels', 'annotations')
                               if key in changed['metadata']}
        self.save('after-' + name + '-' + kind + '.json', desired)
        m.write(self.base / 'pki/controller-bootstrap/rollout' /
                (name + '-' + kind.lower() + '.json'), desired)
        return changed

    def patch_ca(self, secret, field, certificates):
        before = self.obj('secret', secret)
        current = base64.b64decode(before['data'][field]).decode()
        updated = current
        for certificate in certificates:
            updated = h.append_pem(updated, certificate)
        if updated != current:
            self.scoped_patch('secret', before, [{
                'op': 'test', 'path': '/data/' + field,
                'value': before['data'][field]}, {
                'op': 'replace', 'path': '/data/' + field,
                'value': base64.b64encode(updated.encode()).decode()}])

    def create_private_consumer_secret(self, name):
        source = self.base / 'pki/consumers' / name
        m.require(all((source / item).is_file()
                      for item in ('ca.crt', 'tls.crt', 'tls.key')),
                  'prepared dev consumer identity required: ' + name)
        root_pem = self.obj('configmap', 'pki-service-host-root')['data']['root.pem']
        secret = 'pki-mqtt-consumer-' + name
        data = {'tls.crt': (source / 'tls.crt').read_text(),
                'tls.key': (source / 'tls.key').read_text(),
                'ca.crt': root_pem}
        self.create({'apiVersion': 'v1', 'kind': 'Secret',
                     'metadata': {'name': secret, 'namespace': NS},
                     'type': 'Opaque', 'data': {
                         key: base64.b64encode(value.encode()).decode()
                         for key, value in data.items()}})
        return secret

    def client_template(self, owner, root, secret, configmap):
        template = json.loads(json.dumps(owner['spec']['template']))
        pod = template['spec']
        m.require(len(pod['containers']) == 1
                  and not any(v['name'] == 'mqtt-pki-bundles'
                              for v in pod.get('volumes', [])),
                  'MQTT client already configured; reconcile')
        container = pod['containers'][0]
        container['image'] = self.args.image
        container['env'] = h.with_env(
            container.get('env', []), mqtt_client_settings(root))
        mounts = container.setdefault('volumeMounts', [])
        mounts.extend([
            {'name': 'mqtt-pki-bundles', 'mountPath': '/run/pki-mqtt',
             'readOnly': True},
            {'name': 'mqtt-pki-management',
             'mountPath': '/run/pki-mqtt-management', 'readOnly': True},
        ])
        existing = {v['name'] for v in pod.get('volumes', [])}
        additions = [
            {'name': 'mqtt-pki-bundles',
             'configMap': {'name': configmap}},
            {'name': 'mqtt-pki-management',
             'secret': {'secretName': secret, 'defaultMode': 288}},
        ]
        if 'service-root' not in existing:
            additions.append({'name': 'service-root',
                              'configMap': {'name': 'pki-service-host-root'}})
            mounts.append({'name': 'service-root',
                           'mountPath': '/run/pki-service-root',
                           'readOnly': True})
        pod.setdefault('volumes', []).extend(additions)
        template.setdefault('metadata', {}).setdefault('annotations', {})[
            'rtk.cloud/mqtt-client-rollout'] = self.output.name
        return template

    def install_root_consumers(self):
        root = self.mqtt_root('ready')
        m.require(IMAGE_PATTERN.fullmatch(self.args.image or ''),
                  'verified dev application image digest required')
        operation = m.read(Path(self.args.authority) / 'root-operation.json')
        self.api('/operations/' + operation['operation_id'] + '/activate', {}, 409)
        refs = [{'issuer_id': root['issuer_id'],
                 'trust_bundle_version': root['trust_bundle_version']}]
        configmap = 'pki-mqtt-bundles'
        self.create({'apiVersion': 'v1', 'kind': 'ConfigMap',
                     'metadata': {'name': configmap, 'namespace': NS},
                     'immutable': True,
                     'data': {'roots.pem': root['certificate_pem'],
                              'issuers.json': json.dumps(refs)}})
        cas = [(self.base / 'pki/consumers' / name / 'ca.crt').read_text()
               for name in MQTT_CONSUMERS]
        self.patch_ca('pki-controller-tls', 'ca.crt', cas)
        controller = self.obj('deployment', 'pki-controller')
        template = json.loads(json.dumps(controller['spec']['template']))
        c = template['spec']['containers'][0]
        c['env'] = h.with_env(c.get('env', []), {
            'PKI_REQUIRED_CONSUMERS_MQTT': ','.join(MQTT_CONSUMERS),
            'PKI_REQUIRED_BUNDLE_CONSUMERS_MQTT': ','.join(MQTT_CONSUMERS)})
        self.scoped_patch('deployment', controller, [{
            'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', NS, 'rollout', 'status',
                   'deployment/pki-controller', '--timeout=240s'], timeout=250)
        self.create({'apiVersion': 'networking.k8s.io/v1',
                     'kind': 'NetworkPolicy',
                     'metadata': {'name': 'allow-mqtt-trust-consumers',
                                  'namespace': NS},
                     'spec': {
                         'podSelector': {'matchLabels': {
                             'app.kubernetes.io/name': 'pki-controller'}},
                         'policyTypes': ['Ingress'],
                         'ingress': [{'from': [{'podSelector': {
                             'matchExpressions': [{
                                 'key': 'app.kubernetes.io/name',
                                 'operator': 'In',
                                 'values': MQTT_CONSUMERS}]}}],
                                      'ports': [{'port': 18446,
                                                 'protocol': 'TCP'}]}]}})
        for name in MQTT_CONSUMERS:
            secret = self.create_private_consumer_secret(name)
            owner = self.obj('deployment', name)
            template = self.client_template(owner, root, secret, configmap)
            self.scoped_patch('deployment', owner, [{
                'op': 'replace', 'path': '/spec/template', 'value': template}])
            self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + name,
                       '--timeout=240s'], timeout=250)
        receipts = self.wait_receipts(root['issuer_id'],
                                     root['trust_bundle_version'],
                                     MQTT_CONSUMERS)
        self.check('mqtt_root_installed_by_actual_clients', {
            'issuer_id': root['issuer_id'], 'consumers': receipts,
            'activation_without_receipts_denied': True,
            'client_image': self.args.image})

    def activate_root(self):
        root = self.mqtt_root('ready')
        operation = m.read(Path(self.args.authority) / 'root-operation.json')
        receipts = self.wait_receipts(root['issuer_id'],
                                     root['trust_bundle_version'],
                                     MQTT_CONSUMERS)
        self.api('/operations/' + operation['operation_id'] + '/activate', {}, 204)
        root = self.api('/issuers/' + root['issuer_id'])
        m.require(root['status'] == 'active', 'MQTT Root did not activate')
        self.save('mqtt-root-active.json', root)
        now = dt.datetime.now(dt.timezone.utc).replace(microsecond=0)
        request = {
            'issuer_id': root['issuer_id'],
            'issuer_fingerprint_sha256': root['certificate_fingerprint_sha256'],
            'crl_number': '1', 'this_update': m.stamp(now),
            'next_update': m.stamp(now + dt.timedelta(days=1)),
            'revocations': []}
        self.save('root-crl-request.json', request)
        expected = self.ceremony_call([
            'crl-digest', self.output / 'root-crl-request.json']).strip()
        authority = Path(self.args.authority)
        self.ceremony_call([
            'crl', '--issuer', self.output / 'mqtt-root-active.json',
            '--crl-request', self.output / 'root-crl-request.json',
            '--expected-request-sha256', expected,
            '--key', authority / 'root-offline-simulation/ca-key.encrypted.pem',
            '--passphrase-file', m.read(
                authority / 'passphrase-reference.json')['path'],
            '--out', self.output / 'root-crl'])
        raw = (self.output / 'root-crl/revocations.pem').read_text()
        record = self.api('/issuers/' + root['issuer_id'] + '/crl',
                          {'crl_pem': raw})
        self.save('mqtt-root-crl.json', record)
        self.check('mqtt_root_active_with_crl', {
            'issuer_id': root['issuer_id'], 'consumers': receipts,
            'crl_sha256': record['crl_sha256']})

    def prepare_intermediate(self):
        root = self.mqtt_root('active')
        self.api('/issuers/' + root['issuer_id'] + '/crl')
        cursor = ''
        while True:
            page = self.api('/issuers/search', {'limit': 100, 'before': cursor})
            m.require(not any(i['environment'] == 'dev'
                              and i['trust_domain'] == 'mqtt'
                              and i['kind'] == 'intermediate'
                              and i['status'] not in ('retired', 'cancelled',
                                                     'revoked', 'compromised',
                                                     'failed')
                              for i in page['items']),
                      'MQTT intermediate already exists; reconcile')
            cursor = page.get('next', '')
            if not cursor:
                break
        request = intermediate_request(root)
        operation = self.api('/operations', request,
                             key='dev-mqtt-intermediate-' + uuid.uuid4().hex)
        self.save('intermediate-request.json', request)
        self.save('intermediate-operation.json', operation)
        self.approval(operation)
        issuer = self.api('/issuers/' + operation['issuer_id'])
        m.require(issuer['status'] == 'approved'
                  and issuer['signer_provider'] == 'openbao',
                  'approved MQTT intermediate differs')
        policies = json.loads(self.kube([
            '-n', NS, 'exec', 'deployment/pki-controller', '--',
            '/app/pkicontroller', 'render-openbao-policy', issuer['issuer_id']]))
        m.require(policies['mount'] == issuer['signer_reference']
                  and '/sign/server' in policies['signer_policy'],
                  'MQTT provider policy differs')
        self.save('provider-policies.json', policies)
        self.role_policy('pki-controller-dev',
                         'pki-controller-dev-' + issuer['issuer_id'],
                         policies['controller_policy'])
        self.api('/operations/' + operation['operation_id'] + '/provision', {})
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['status'] == 'provisioning' and issuer['csr_pem'],
                  'MQTT intermediate CSR not registered')
        self.save('intermediate-provisioning.json', issuer)
        csr_hash = m.digest(base64.b64decode(
            ''.join(issuer['csr_pem'].splitlines()[1:-1])))
        authority = Path(self.args.authority)
        self.ceremony_call([
            'sign', '--issuer', self.output / 'intermediate-provisioning.json',
            '--expected-request-sha256', operation['request_sha256'],
            '--expected-csr-sha256', csr_hash,
            '--parent', self.output / 'mqtt-root.json',
            '--expected-parent-sha256', root['certificate_fingerprint_sha256'],
            '--key', authority / 'root-offline-simulation/ca-key.encrypted.pem',
            '--passphrase-file', m.read(
                authority / 'passphrase-reference.json')['path'],
            '--out', self.output / 'intermediate-signed'])
        self.api('/operations/' + operation['operation_id'] + '/import', {
            'certificate_pem': (self.output /
                                'intermediate-signed/certificate.pem').read_text()}, 204)
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['status'] == 'ready'
                  and issuer['server_dns_names'] == [MQTT_HOST],
                  'ready MQTT intermediate differs')
        self.save('intermediate-ready.json', issuer)
        self.api('/operations/' + operation['operation_id'] + '/activate', {}, 409)
        self.check('mqtt_intermediate_ready_gate_closed', {
            'issuer_id': issuer['issuer_id'],
            'server_dns_names': issuer['server_dns_names']})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', choices=[
        'install-root-consumers', 'activate-root', 'prepare-intermediate'],
        required=True)
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--authority', required=True)
    parser.add_argument('--image')
    args = parser.parse_args()
    lock = Path(args.config_root).expanduser() / 'dev/pki/mqtt-host-rollout.lock'
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = MQTTHostRun(args)
    try:
        runner.preflight()
        {'install-root-consumers': runner.install_root_consumers,
         'activate-root': runner.activate_root,
         'prepare-intermediate': runner.prepare_intermediate}[args.phase]()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'
        runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        os.close(owner)
        print(json.dumps({'phase': args.phase, 'status': runner.report['status'],
                          'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    def interrupted(*_):
        raise RuntimeError('interrupted; reconcile the saved MQTT phase')
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
