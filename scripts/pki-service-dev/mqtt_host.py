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
MQTT_HOST_PVC = 'mqtt-pki-host-identity'
MQTT_HOST_STATE = '/var/lib/emqx-pki/identity/state.json'
MQTT_RUNTIME_SECRET = 'mqtt-pki-runtime-managed'
MQTT_MANAGEMENT_SECRET = 'pki-mqtt-host-management'
MQTT_RUNTIME_FIELDS = {
    'api-keys.conf', 'authentication', 'cookie', 'dashboard-password'}
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


def mqtt_host_settings(mqtt_root, service_root):
    issuer = 'certissuer.' + NS + '.svc'
    return {
        'PKI_ENVIRONMENT': 'dev',
        'EMQX_PKI_EXECUTABLE': '/usr/local/bin/emqx',
        'EMQX_PKI_RUNTIME_DIR': '/run/emqx-pki',
        'EMQX_PKI_HOST_IDENTITY_STATE': MQTT_HOST_STATE,
        'EMQX_PKI_HOST_NAME': MQTT_HOST,
        'EMQX_PKI_HOST_DNS_NAMES': MQTT_HOST,
        'EMQX_PKI_HOST_ROOT_SHA256':
            mqtt_root['certificate_fingerprint_sha256'],
        'EMQX_PKI_HOST_RENEWAL_CLIENT_CERT':
            '/run/emqx-pki-management/tls.crt',
        'EMQX_PKI_HOST_RENEWAL_CLIENT_KEY':
            '/run/emqx-pki-management/tls.key',
        'EMQX_PKI_HOST_RENEWAL_URL': 'https://' + issuer + ':9443',
        'EMQX_PKI_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256':
            service_root['certificate_fingerprint_sha256'],
        'EMQX_PKI_HOST_RENEWAL_SERVER_PKI_NAME': issuer,
        'EMQX_PKI_HOST_RENEWAL_TLS_CA': '/run/pki-service-root/root.pem',
        'EMQX_PKI_HOST_RENEWAL_SERVER_PKI_SWEEP_INTERVAL': '10s',
        'EMQX_PKI_SEED_CERT': '/var/lib/emqx-pki/seed/chain.pem',
        'EMQX_PKI_SEED_KEY': '/var/lib/emqx-pki/seed/key.pem',
    }


def managed_runtime_data(source):
    m.require(set(source) >= MQTT_RUNTIME_FIELDS,
              'existing MQTT runtime fields incomplete')
    return {name: source[name] for name in sorted(MQTT_RUNTIME_FIELDS)}


def managed_mqtt_template(owner, image, settings, output_name):
    template = json.loads(json.dumps(owner['spec']['template']))
    pod = template['spec']
    containers = {item['name']: item for item in pod['containers']}
    m.require(set(containers) == {'mqtt', 'pkibroker'},
              'dedicated MQTT Pod topology changed')
    mqtt = containers['mqtt']
    m.require(not any(item['name'].startswith('EMQX_PKI_')
                      for item in mqtt.get('env', [])),
              'MQTT host is already managed; reconcile')
    m.require(pod.get('securityContext', {}).get('fsGroup') == 1000,
              'EMQX filesystem ownership changed')
    mqtt['image'] = image
    mqtt['env'] = h.with_env(mqtt.get('env', []), settings)
    mqtt['env'].append({'name': 'PKI_DATABASE_URL', 'valueFrom': {
        'secretKeyRef': {'name': 'pki-controller-database', 'key': 'url'}}})
    for item in mqtt['env']:
        reference = item.get('valueFrom', {}).get('secretKeyRef', {})
        if reference.get('name') == 'mqtt-pki-runtime':
            reference['name'] = MQTT_RUNTIME_SECRET
    mqtt.setdefault('volumeMounts', []).extend([
        {'name': 'mqtt-host-state', 'mountPath': '/var/lib/emqx-pki'},
        {'name': 'mqtt-host-management',
         'mountPath': '/run/emqx-pki-management', 'readOnly': True},
        {'name': 'pki-service-root', 'mountPath': '/run/pki-service-root',
         'readOnly': True},
    ])
    volumes = {item['name']: item for item in pod.get('volumes', [])}
    m.require(volumes.get('runtime', {}).get('secret', {}).get(
                  'secretName') == 'mqtt-pki-runtime'
              and 'pki-service-root' in volumes,
              'MQTT runtime or Service trust source changed')
    volumes['runtime']['secret']['secretName'] = MQTT_RUNTIME_SECRET
    pod['volumes'].extend([
        {'name': 'mqtt-host-state', 'persistentVolumeClaim': {
            'claimName': MQTT_HOST_PVC}},
        {'name': 'mqtt-host-management', 'secret': {
            'secretName': MQTT_MANAGEMENT_SECRET, 'defaultMode': 288}},
    ])
    template.setdefault('metadata', {}).setdefault('annotations', {})[
        'rtk.cloud/mqtt-host-rollout'] = output_name
    return template


def crl_state_initializer(image):
    return {
        'name': 'mqtt-pki-state-init', 'image': image,
        'command': ['sh', '-c'],
        'args': ['set -eu; umask 077; mkdir -p /var/lib/mqtt-pki/crls; '
                 'chmod 700 /var/lib/mqtt-pki/crls'],
        'volumeMounts': [{'name': 'mqtt-pki-state',
                          'mountPath': '/var/lib/mqtt-pki'}],
        'securityContext': {'allowPrivilegeEscalation': False,
                            'capabilities': {'drop': ['ALL']},
                            'runAsUser': 10001, 'runAsGroup': 10001,
                            'runAsNonRoot': True,
                            'readOnlyRootFilesystem': True}}


def valid_crl_state_initializer(value, image, numeric_identity=True):
    expected = crl_state_initializer(image)
    for field in ('name', 'image', 'command', 'args', 'volumeMounts'):
        if value.get(field) != expected[field]:
            return False
    security = value.get('securityContext', {})
    required = expected['securityContext']
    for field in ('allowPrivilegeEscalation', 'capabilities', 'runAsNonRoot',
                  'readOnlyRootFilesystem'):
        if security.get(field) != required[field]:
            return False
    ids = (security.get('runAsUser'), security.get('runAsGroup'))
    return ids == ((10001, 10001) if numeric_identity else (None, None))


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
        security = pod.setdefault('securityContext', {})
        m.require(security.get('fsGroup') in (None, 10001),
                  'MQTT client filesystem group changed')
        security['fsGroup'] = 10001
        security['fsGroupChangePolicy'] = 'OnRootMismatch'
        template.setdefault('metadata', {}).setdefault('annotations', {})[
            'rtk.cloud/mqtt-client-rollout'] = self.output.name
        return template

    def verify_installed_client(self, name, root, configmap):
        owner = self.obj('deployment', name)
        pod = owner['spec']['template']['spec']
        m.require(len(pod['containers']) == 1 and
                  pod['securityContext'].get('fsGroup') == 10001,
                  'installed MQTT client security differs: ' + name)
        container = pod['containers'][0]
        current = {item['name']: item.get('value')
                   for item in container.get('env', [])}
        expected = mqtt_client_settings(root)
        m.require(all(current.get(key) == value
                      for key, value in expected.items()),
                  'installed MQTT client settings differ: ' + name)
        volumes = {item['name']: item for item in pod.get('volumes', [])}
        m.require(volumes.get('mqtt-pki-bundles', {}).get(
                      'configMap', {}).get('name') == configmap
                  and volumes.get('mqtt-pki-management', {}).get(
                      'secret', {}).get('secretName') ==
                  'pki-mqtt-consumer-' + name,
                  'installed MQTT client volumes differ: ' + name)
        return owner

    def install_root_consumers(self):
        root = self.mqtt_root('ready')
        m.require(IMAGE_PATTERN.fullmatch(self.args.image or ''),
                  'verified dev application image digest required')
        operation = m.read(Path(self.args.authority) / 'root-operation.json')
        refs = [{'issuer_id': root['issuer_id'],
                 'trust_bundle_version': root['trust_bundle_version']}]
        configmap = 'pki-mqtt-bundles'
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
        self.api('/operations/' + operation['operation_id'] + '/activate', {},
                 409, role='approver')
        self.create({'apiVersion': 'v1', 'kind': 'ConfigMap',
                     'metadata': {'name': configmap, 'namespace': NS},
                     'immutable': True,
                     'data': {'roots.pem': root['certificate_pem'],
                              'issuers.json': json.dumps(refs)}})
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

    def finish_root_consumers(self):
        failed = m.read(Path(self.args.failed) / 'report.json')
        m.require(failed['status'] == 'failed'
                  and failed['phase'] == 'install-root-consumers',
                  'failed Root consumer phase required')
        root = self.mqtt_root('ready')
        configmap = self.obj('configmap', 'pki-mqtt-bundles')
        expected = [{'issuer_id': root['issuer_id'],
                     'trust_bundle_version': root['trust_bundle_version']}]
        m.require(configmap.get('immutable')
                  and configmap['data']['roots.pem'] == root['certificate_pem']
                  and json.loads(configmap['data']['issuers.json']) == expected,
                  'installed MQTT Root ConfigMap changed')
        controller = self.obj('deployment', 'pki-controller')
        values = {item['name']: item.get('value') for item in
                  controller['spec']['template']['spec']['containers'][0]['env']}
        m.require(values.get('PKI_REQUIRED_CONSUMERS_MQTT') ==
                  ','.join(MQTT_CONSUMERS)
                  and values.get('PKI_REQUIRED_BUNDLE_CONSUMERS_MQTT') ==
                  ','.join(MQTT_CONSUMERS), 'MQTT activation gates changed')
        for name in MQTT_CONSUMERS:
            owner = self.obj('deployment', name)
            pod = owner['spec']['template']['spec']
            if pod.get('securityContext', {}).get('fsGroup') != 10001:
                m.require(name == 'video-cloud-logingester'
                          and pod.get('securityContext', {}).get('fsGroup') is None,
                          'unexpected MQTT client permission recovery')
                template = json.loads(json.dumps(owner['spec']['template']))
                security = template['spec'].setdefault('securityContext', {})
                security['fsGroup'] = 10001
                security['fsGroupChangePolicy'] = 'OnRootMismatch'
                self.scoped_patch('deployment', owner, [{
                    'op': 'replace', 'path': '/spec/template',
                    'value': template}])
            self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + name,
                       '--timeout=240s'], timeout=250)
            self.verify_installed_client(name, root, 'pki-mqtt-bundles')
        receipts = self.wait_receipts(root['issuer_id'],
                                     root['trust_bundle_version'],
                                     MQTT_CONSUMERS)
        self.check('mqtt_root_consumer_recovery', {
            'issuer_id': root['issuer_id'], 'consumers': receipts,
            'reconciled_from': str(Path(self.args.failed)),
            'permission_change': 'pod fsGroup only'})

    def activate_root(self):
        root = self.mqtt_root('ready')
        operation = m.read(Path(self.args.authority) / 'root-operation.json')
        receipts = self.wait_receipts(root['issuer_id'],
                                     root['trust_bundle_version'],
                                     MQTT_CONSUMERS)
        self.api('/operations/' + operation['operation_id'] + '/activate', {},
                 204, role='approver')
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
                          {'crl_pem': raw}, role='approver')
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
                                'intermediate-signed/certificate.pem').read_text()},
                 204, role='approver')
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['status'] == 'ready'
                  and issuer['server_dns_names'] == [MQTT_HOST],
                  'ready MQTT intermediate differs')
        self.save('intermediate-ready.json', issuer)
        self.api('/operations/' + operation['operation_id'] + '/activate', {},
                 409, role='approver')
        self.check('mqtt_intermediate_ready_gate_closed', {
            'issuer_id': issuer['issuer_id'],
            'server_dns_names': issuer['server_dns_names']})

    def ready_intermediate(self, status='ready'):
        root = self.mqtt_root('active')
        source = Path(self.args.intermediate)
        saved = m.read(source / 'intermediate-ready.json')
        issuer = self.api('/issuers/' + saved['issuer_id'])
        m.require(issuer['status'] == status
                  and issuer['environment'] == 'dev'
                  and issuer['trust_domain'] == 'mqtt'
                  and issuer['kind'] == 'intermediate'
                  and issuer['parent_issuer_id'] == root['issuer_id']
                  and issuer['server_dns_names'] == [MQTT_HOST]
                  and issuer['certificate_fingerprint_sha256'] ==
                  saved['certificate_fingerprint_sha256'],
                  'saved MQTT intermediate changed')
        operation = m.read(source / 'intermediate-operation.json')
        m.require(operation['issuer_id'] == issuer['issuer_id'],
                  'saved MQTT intermediate operation changed')
        self.save('mqtt-intermediate.json', issuer)
        return root, issuer, operation

    def switch_client_bundle(self, name, configmap):
        owner = self.obj('deployment', name)
        template = json.loads(json.dumps(owner['spec']['template']))
        volumes = [v for v in template['spec']['volumes']
                   if v['name'] == 'mqtt-pki-bundles']
        m.require(len(volumes) == 1
                  and volumes[0].get('configMap', {}).get('name') ==
                  'pki-mqtt-bundles',
                  'MQTT client bundle source changed: ' + name)
        volumes[0]['configMap']['name'] = configmap
        template.setdefault('metadata', {}).setdefault('annotations', {})[
            'rtk.cloud/mqtt-intermediate-rollout'] = self.output.name
        self.scoped_patch('deployment', owner, [{
            'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + name,
                   '--timeout=240s'], timeout=250)

    def install_intermediate(self):
        root, issuer, operation = self.ready_intermediate()
        configmap = 'pki-mqtt-bundles-' + issuer['issuer_id'][:8]
        refs = mqtt_intermediate_manifest(root, issuer)
        self.create({'apiVersion': 'v1', 'kind': 'ConfigMap',
                     'metadata': {'name': configmap, 'namespace': NS},
                     'immutable': True,
                     'data': {'roots.pem': root['certificate_pem'],
                              'issuers.json': json.dumps(refs)}})
        self.switch_client_bundle('video-cloud-api', configmap)
        first = self.wait_receipts(issuer['issuer_id'],
                                   issuer['trust_bundle_version'],
                                   ['video-cloud-api'],
                                   absent='video-cloud-logingester')
        self.api('/operations/' + operation['operation_id'] + '/activate', {},
                 409, role='approver')
        self.switch_client_bundle('video-cloud-logingester', configmap)
        receipts = self.wait_receipts(issuer['issuer_id'],
                                     issuer['trust_bundle_version'],
                                     MQTT_CONSUMERS)
        self.check('mqtt_intermediate_installed_by_actual_clients', {
            'issuer_id': issuer['issuer_id'], 'first_consumer': first,
            'consumers': receipts,
            'single_consumer_activation_denied': True,
            'configmap': configmap})

    def add_client_crl_state(self, name, root, issuer, configmap):
        pvc = name + '-mqtt-pki-state'
        self.create({'apiVersion': 'v1', 'kind': 'PersistentVolumeClaim',
                     'metadata': {'name': pvc, 'namespace': NS},
                     'spec': {'accessModes': ['ReadWriteOnce'],
                              'storageClassName': 'linode-block-storage-retain',
                              'resources': {'requests': {'storage': '10Gi'}}}})
        self.kube(['-n', NS, 'wait', '--for=jsonpath={.status.phase}=Bound',
                   'persistentvolumeclaim/' + pvc, '--timeout=180s'], timeout=190)
        owner = self.obj('deployment', name)
        template = json.loads(json.dumps(owner['spec']['template']))
        pod, container = template['spec'], template['spec']['containers'][0]
        m.require(not any(v['name'] in ('mqtt-pki-crls', 'mqtt-pki-state')
                          for v in pod['volumes']),
                  'MQTT CRL state already configured: ' + name)
        container['env'] = h.with_env(container['env'], {
            'VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST':
                '/run/pki-mqtt-crls/crls.json',
            'VIDEO_CLOUD_MQTT_PKI_CONTROLLER_URL':
                'https://pki-controller.' + NS + '.svc:18446',
            'VIDEO_CLOUD_MQTT_MANAGEMENT_CA':
                '/run/pki-service-root/root.pem',
            'VIDEO_CLOUD_MQTT_MANAGEMENT_CERT':
                '/run/pki-mqtt-management/tls.crt',
            'VIDEO_CLOUD_MQTT_MANAGEMENT_KEY':
                '/run/pki-mqtt-management/tls.key'})
        container['volumeMounts'].extend([
            {'name': 'mqtt-pki-crls', 'mountPath': '/run/pki-mqtt-crls',
             'readOnly': True},
            {'name': 'mqtt-pki-state', 'mountPath': '/var/lib/mqtt-pki'}])
        pod['volumes'].extend([
            {'name': 'mqtt-pki-crls', 'configMap': {'name': configmap}},
            {'name': 'mqtt-pki-state', 'persistentVolumeClaim': {
                'claimName': pvc}}])
        pod.setdefault('initContainers', []).append(
            crl_state_initializer(container['image']))
        template.setdefault('metadata', {}).setdefault('annotations', {})[
            'rtk.cloud/mqtt-crl-rollout'] = self.output.name
        changes = [{'op': 'replace', 'path': '/spec/template',
                    'value': template}]
        if name == 'video-cloud-logingester':
            changes.insert(0, {'op': 'replace', 'path': '/spec/strategy',
                               'value': {'type': 'Recreate'}})
        self.scoped_patch('deployment', owner, changes)
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + name,
                   '--timeout=300s'], timeout=310)

    def activate_intermediate(self):
        root, issuer, operation = self.ready_intermediate()
        receipts = self.wait_receipts(issuer['issuer_id'],
                                     issuer['trust_bundle_version'],
                                     MQTT_CONSUMERS)
        self.api('/operations/' + operation['operation_id'] + '/activate', {},
                 204, role='approver')
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['status'] == 'active',
                  'MQTT intermediate did not activate')
        provider = json.loads(self.bao([
            'read', '-format=json', issuer['signer_reference'] + '/cert/crl']))
        self.save('intermediate-provider-crl.json', provider)
        record = self.api('/issuers/' + issuer['issuer_id'] + '/crl', {
            'crl_pem': provider['data']['certificate']}, role='approver')
        self.save('mqtt-intermediate-crl.json', record)
        root_crl = self.api('/issuers/' + root['issuer_id'] + '/crl')
        crl_configmap = 'pki-mqtt-crls-' + issuer['issuer_id'][:8]
        manifest = [{
            'issuer': authority,
            'state_path': '/var/lib/mqtt-pki/crls/' +
                          authority['issuer_id'] + '.json'}
            for authority in (root, issuer)]
        self.create({'apiVersion': 'v1', 'kind': 'ConfigMap',
                     'metadata': {'name': crl_configmap, 'namespace': NS},
                     'immutable': True,
                     'data': {'crls.json': json.dumps(manifest)}})
        for name in MQTT_CONSUMERS:
            self.add_client_crl_state(name, root, issuer, crl_configmap)
        crl_receipts = {}
        for authority, crl in ((root, root_crl), (issuer, record)):
            crl_receipts[authority['issuer_id']] = self.wait_receipts(
                authority['issuer_id'], crl['crl_sha256'], MQTT_CONSUMERS,
                kind='crl')
        self.check('mqtt_intermediate_active_with_crl_consumers', {
            'issuer_id': issuer['issuer_id'], 'bundle_consumers': receipts,
            'crl_sha256': record['crl_sha256'],
            'root_crl_sha256': root_crl['crl_sha256'],
            'crl_consumers': crl_receipts,
            'private_state': 'one retained PVC per actual client'})

    def finish_intermediate_activation(self):
        failed = m.read(Path(self.args.failed) / 'report.json')
        m.require(failed['status'] == 'failed'
                  and failed['phase'] == 'activate-intermediate',
                  'failed intermediate activation phase required')
        root, issuer, _ = self.ready_intermediate(status='active')
        root_crl = self.api('/issuers/' + root['issuer_id'] + '/crl')
        issuer_crl = self.api('/issuers/' + issuer['issuer_id'] + '/crl')
        configmap = 'pki-mqtt-crls-' + issuer['issuer_id'][:8]
        self.obj('configmap', configmap)
        for name in MQTT_CONSUMERS:
            owner = self.obj('deployment', name)
            template = json.loads(json.dumps(owner['spec']['template']))
            pod, container = template['spec'], template['spec']['containers'][0]
            existing = [item for item in pod.get('initContainers', [])
                        if item['name'] == 'mqtt-pki-state-init']
            if not existing:
                pod.setdefault('initContainers', []).append(
                    crl_state_initializer(container['image']))
                template.setdefault('metadata', {}).setdefault('annotations', {})[
                    'rtk.cloud/mqtt-crl-recovery'] = self.output.name
                self.scoped_patch('deployment', owner, [{
                    'op': 'replace', 'path': '/spec/template',
                    'value': template}])
            else:
                expected = crl_state_initializer(container['image'])
                if not valid_crl_state_initializer(
                        existing[0], container['image']):
                    m.require(name in MQTT_CONSUMERS
                              and valid_crl_state_initializer(
                                  existing[0], container['image'], False),
                              'MQTT CRL initializer changed: ' + name)
                    pod['initContainers'] = [
                        expected if item['name'] == 'mqtt-pki-state-init'
                        else item for item in pod['initContainers']]
                    annotations = template.setdefault(
                        'metadata', {}).setdefault('annotations', {})
                    annotations['rtk.cloud/mqtt-crl-recovery'] = self.output.name
                    self.scoped_patch('deployment', owner, [{
                        'op': 'replace', 'path': '/spec/template',
                        'value': template}])
            self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + name,
                       '--timeout=300s'], timeout=310)
        receipts = {}
        for authority, crl in ((root, root_crl), (issuer, issuer_crl)):
            receipts[authority['issuer_id']] = self.wait_receipts(
                authority['issuer_id'], crl['crl_sha256'], MQTT_CONSUMERS,
                kind='crl')
        self.check('mqtt_intermediate_activation_recovery', {
            'issuer_id': issuer['issuer_id'], 'crl_consumers': receipts,
            'reconciled_from': str(Path(self.args.failed)),
            'permission_change': 'private CRL state directory initializer'})

    def configure_certissuer(self):
        root, issuer, _ = self.ready_intermediate(status='active')
        m.require(IMAGE_PATTERN.fullmatch(self.args.image or ''),
                  'verified dev application image digest required')
        policies = m.read(Path(self.args.intermediate) / 'provider-policies.json')
        m.require(policies['issuer_id'] == issuer['issuer_id']
                  and '/sign/server' in policies['signer_policy'],
                  'saved MQTT signer policy changed')
        self.role_policy('certissuer-pki-dev',
                         'pki-mqtt-server-dev-' + issuer['issuer_id'],
                         policies['signer_policy'])
        self.patch_ca('certissuer-runtime', 'client-ca.crt', [
            (self.base / 'pki/consumers/emqx-pki/ca.crt').read_text()])
        owner = self.obj('deployment', 'certissuer')
        template = json.loads(json.dumps(owner['spec']['template']))
        container = template['spec']['containers'][0]
        container['image'] = self.args.image
        container['env'] = h.with_env(container['env'], {
            'CERT_ISSUER_MQTT_SERVER_PKI_ROOT_SHA256':
                root['certificate_fingerprint_sha256'],
            'CERT_ISSUER_MQTT_SERVER_DNS_NAMES': MQTT_HOST,
            'CERT_ISSUER_MQTT_SERVER_CLIENT_CN_PATTERN': '^emqx-pki$'})
        template.setdefault('metadata', {}).setdefault('annotations', {})[
            'rtk.cloud/mqtt-issuer-rollout'] = self.output.name
        self.scoped_patch('deployment', owner, [{
            'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.create({'apiVersion': 'networking.k8s.io/v1',
                     'kind': 'NetworkPolicy',
                     'metadata': {'name': 'allow-mqtt-host-renewal',
                                  'namespace': NS},
                     'spec': {
                         'podSelector': {'matchLabels': {
                             'app.kubernetes.io/name': 'certissuer'}},
                         'policyTypes': ['Ingress'],
                         'ingress': [{'from': [{'podSelector': {
                             'matchLabels': {
                                 'app.kubernetes.io/name': 'mqtt-pki'}}}],
                                      'ports': [{'port': 9443,
                                                 'protocol': 'TCP'}]}]}})
        self.kube(['-n', NS, 'rollout', 'status',
                   'deployment/certissuer', '--timeout=240s'], timeout=250)
        self.check('mqtt_named_issuance_route_enabled', {
            'issuer_id': issuer['issuer_id'], 'dns_names': [MQTT_HOST],
            'caller_pattern': '^emqx-pki$', 'image': self.args.image})

    def mqtt_issuer_http(self, body, ca):
        identity = self.base / 'pki/consumers/emqx-pki'
        response = json.loads(m.command([
            self.probe, 'tls', ca, identity / 'tls.crt', identity / 'tls.key',
            'certissuer.' + NS + '.svc', self.ports['issuer'][0],
            '/v1/certificates/mqtt/issue'], json.dumps(body)))
        self.save('http-emqx-pki.json', response)
        m.require(response['status'] == 200,
                  'MQTT server issuance failed: ' + str(response['status']))
        return json.loads(response['body'])

    def prepare_host(self):
        root, issuer, _ = self.ready_intermediate(status='active')
        m.require(IMAGE_PATTERN.fullmatch(self.args.image or ''),
                  'verified dev EMQX host image digest required')
        self.save('mqtt-root.pem', root['certificate_pem'])
        self.save('mqtt-intermediate.pem', issuer['certificate_pem'])
        owner = self.obj('deployment', 'mqtt-pki')
        podspec = owner['spec']['template']['spec']
        mqtt = next((item for item in podspec['containers']
                     if item['name'] == 'mqtt'), None)
        m.require(mqtt is not None
                  and not any(item['name'].startswith('EMQX_PKI_')
                              for item in mqtt.get('env', [])),
                  'unmanaged dedicated MQTT broker required')
        self.create({'apiVersion': 'v1', 'kind': 'PersistentVolumeClaim',
                     'metadata': {'name': MQTT_HOST_PVC, 'namespace': NS},
                     'spec': {'accessModes': ['ReadWriteOnce'],
                              'storageClassName': 'linode-block-storage-retain',
                              'resources': {'requests': {'storage': '10Gi'}}}})
        self.kube(['-n', NS, 'wait',
                   '--for=jsonpath={.status.phase}=Bound',
                   'persistentvolumeclaim/' + MQTT_HOST_PVC,
                   '--timeout=180s'], timeout=190)
        podname = 'pki-host-seed-mqtt-' + uuid.uuid4().hex[:8]
        script = (
            'set -eu; umask 077; mkdir -p /state/seed; '
            'test ! -e /state/seed/key.pem; '
            'openssl genpkey -algorithm EC -pkeyopt '
            'ec_paramgen_curve:P-256 -out /state/seed/key.pem; '
            'openssl req -new -key /state/seed/key.pem '
            '-subj /CN=' + MQTT_HOST + ' -addext subjectAltName=DNS:' +
            MQTT_HOST + ' -out /state/seed/csr.pem; exec sleep 3600')
        pod = {'apiVersion': 'v1', 'kind': 'Pod',
               'metadata': {'name': podname, 'namespace': NS},
               'spec': {'restartPolicy': 'Never',
                        'automountServiceAccountToken': False,
                        'imagePullSecrets': podspec.get('imagePullSecrets', []),
                        'securityContext': {
                            'runAsUser': 1000, 'runAsGroup': 1000,
                            'fsGroup': 1000,
                            'fsGroupChangePolicy': 'OnRootMismatch'},
                        'containers': [{
                            'name': 'seed', 'image': self.args.image,
                            'command': ['sh', '-c', script],
                            'volumeMounts': [{
                                'name': 'state', 'mountPath': '/state'}]}],
                        'volumes': [{'name': 'state',
                                     'persistentVolumeClaim': {
                                         'claimName': MQTT_HOST_PVC}}]}}
        self.save('seed-pod.json', pod)
        self.kube(['-n', NS, 'create', '-f', '-'], json.dumps(pod))
        self.kube(['-n', NS, 'wait', '--for=condition=Ready',
                   'pod/' + podname, '--timeout=240s'], timeout=250)
        csr = self.kube(['-n', NS, 'exec', podname, '--',
                         'cat', '/state/seed/csr.pem'])
        request = {'request_id': 'dev-mqtt-server-' + uuid.uuid4().hex,
                   'csr_pem': csr, 'ttl_days': 30, 'purpose': 'server'}
        self.save('request.json', request)
        service_root = self.obj('configmap', 'pki-service-host-root')[
            'data']['root.pem']
        self.save('service-root.pem', service_root)
        self.forward('issuer', NS, 'certissuer', 9443)
        issued = self.mqtt_issuer_http(request, self.output / 'service-root.pem')
        self.save('issued.json', issued)
        self.save('leaf.pem', issued['certificate_pem'])
        m.require(issued['dns_names'] == [MQTT_HOST]
                  and issued['caller_identity'] == 'emqx-pki',
                  'MQTT server issuance identity differs')
        m.command([self.openssl, 'verify', '-CAfile',
                   self.output / 'mqtt-root.pem', '-untrusted',
                   self.output / 'mqtt-intermediate.pem', '-purpose',
                   'sslserver', '-verify_hostname', MQTT_HOST,
                   self.output / 'leaf.pem'])
        public = m.command([self.openssl, 'x509', '-in',
                            self.output / 'leaf.pem', '-pubkey', '-noout'])
        csr_public = self.kube(['-n', NS, 'exec', podname, '--',
                                'openssl', 'req', '-in',
                                '/state/seed/csr.pem', '-pubkey', '-noout'])
        m.require(public == csr_public,
                  'MQTT certificate does not match broker-owned key')
        self.kube(['-n', NS, 'exec', '-i', podname, '--', 'sh', '-c',
                   'set -eu; umask 077; test ! -e /state/seed/chain.pem; '
                   'cat > /state/seed/chain.pem'],
                  issued['certificate_chain_pem'])
        reference = {
            'name': podname,
            'uid': self.obj('pod', podname)['metadata']['uid'],
            'pvc': MQTT_HOST_PVC,
            'pvc_uid': self.obj('persistentvolumeclaim',
                                MQTT_HOST_PVC)['metadata']['uid']}
        self.save('pod-reference.json', reference)
        self.check('mqtt_host_seed_issued', {
            'issuer_id': issuer['issuer_id'], 'dns_names': [MQTT_HOST],
            'key_exported': False, 'pvc_uid': reference['pvc_uid']})

    def host_state_digest(self):
        value = self.kube([
            '-n', NS, 'exec', 'deployment/mqtt-pki', '-c', 'mqtt', '--',
            'sh', '-c',
            'set -eu; stat -c %a /var/lib/emqx-pki/identity; '
            'stat -c %a ' + MQTT_HOST_STATE + '; sha256sum ' + MQTT_HOST_STATE])
        lines = value.splitlines()
        m.require(len(lines) == 3 and lines[0] in ('700', '2700')
                  and lines[1] == '600'
                  and re.fullmatch('[0-9a-f]{64}', lines[2].split()[0]),
                  'MQTT managed state permissions differ')
        return lines[2].split()[0]

    def served_fingerprint(self):
        self.forward('mqtt-host', NS, 'mqtt-pki', 8883)
        result = json.loads(m.command([
            self.probe, 'tls-peer', self.output / 'mqtt-root.pem', MQTT_HOST,
            self.ports['mqtt-host'][0]]))
        m.require(result['peer_sha256'], 'MQTT listener certificate missing')
        return result['peer_sha256']

    def adopt_host(self):
        root, issuer, _ = self.ready_intermediate(status='active')
        m.require(IMAGE_PATTERN.fullmatch(self.args.image or ''),
                  'verified dev EMQX host image digest required')
        source = Path(self.args.prepared)
        prior = m.read(source / 'report.json')
        m.require(prior['status'] == 'passed'
                  and prior['phase'] == 'prepare-host',
                  'successful MQTT host preparation required')
        issued = m.read(source / 'issued.json')
        reference = m.read(source / 'pod-reference.json')
        m.require(reference['pvc'] == MQTT_HOST_PVC
                  and self.obj('persistentvolumeclaim', MQTT_HOST_PVC)[
                      'metadata']['uid'] == reference['pvc_uid'],
                  'prepared MQTT host PVC changed')
        pod = self.obj('pod', reference['name'])
        m.require(pod['metadata']['uid'] == reference['uid'],
                  'prepared MQTT seed Pod changed')
        self.save('mqtt-root.pem', root['certificate_pem'])
        self.save('mqtt-intermediate.pem', issuer['certificate_pem'])
        service_root = self.obj('configmap', 'pki-service-host-root')[
            'data']['root.pem']
        service_root_cert = self.output / 'service-root.pem'
        self.save('service-root.pem', service_root)
        service_root_fingerprint = m.command([
            self.openssl, 'x509', '-in', service_root_cert, '-noout',
            '-fingerprint', '-sha256']).strip().split('=')[1].replace(
                ':', '').lower()
        identity = self.base / 'pki/consumers/emqx-pki'
        self.create({'apiVersion': 'v1', 'kind': 'Secret',
                     'metadata': {'name': MQTT_MANAGEMENT_SECRET,
                                  'namespace': NS},
                     'type': 'Opaque', 'data': {
                         name: base64.b64encode((identity / name).read_bytes()
                                               ).decode()
                         for name in ('tls.crt', 'tls.key')}})
        legacy = self.obj('secret', 'mqtt-pki-runtime')
        self.create({'apiVersion': 'v1', 'kind': 'Secret',
                     'metadata': {'name': MQTT_RUNTIME_SECRET,
                                  'namespace': NS},
                     'type': legacy.get('type', 'Opaque'),
                     'data': managed_runtime_data(legacy['data'])})
        owner = self.obj('deployment', 'mqtt-pki')
        settings = mqtt_host_settings(root, {
            'certificate_fingerprint_sha256': service_root_fingerprint})
        template = managed_mqtt_template(
            owner, self.args.image, settings, self.output.name)
        policy = self.obj('networkpolicy', 'allow-mqtt-pki-clients')
        policy_spec = json.loads(json.dumps(policy['spec']))
        expressions = policy_spec['ingress'][0]['from'][0][
            'podSelector']['matchExpressions']
        selected = next(item for item in expressions
                        if item['key'] == 'app.kubernetes.io/name'
                        and item['operator'] == 'In')
        selected['values'] = sorted(set(selected['values']) |
                                    set(MQTT_CONSUMERS))
        self.scoped_patch('networkpolicy', policy, [{
            'op': 'replace', 'path': '/spec', 'value': policy_spec}])
        self.kube(['-n', NS, 'delete', 'pod', reference['name'],
                   '--wait=true', '--timeout=90s'])
        self.scoped_patch('deployment', owner, [
            {'op': 'replace', 'path': '/spec/strategy',
             'value': {'type': 'Recreate'}},
            {'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', NS, 'rollout', 'status',
                   'deployment/mqtt-pki', '--timeout=300s'], timeout=310)
        expected = m.digest(base64.b64decode(''.join(
            issued['certificate_pem'].strip().splitlines()[1:-1])))
        m.require(self.served_fingerprint() == expected,
                  'MQTT listener serves another certificate')
        state = self.host_state_digest()
        self.kube(['-n', NS, 'exec', 'deployment/mqtt-pki', '-c', 'mqtt',
                   '--', 'sh', '-c',
                   'set -eu; test -s ' + MQTT_HOST_STATE + '; '
                   'rm /var/lib/emqx-pki/seed/key.pem '
                   '/var/lib/emqx-pki/seed/csr.pem '
                   '/var/lib/emqx-pki/seed/chain.pem'])
        current = self.obj('deployment', 'mqtt-pki')
        restarted = json.loads(json.dumps(current['spec']['template']))
        restarted.setdefault('metadata', {}).setdefault('annotations', {})[
            'rtk.cloud/mqtt-host-seedless-restart'] = self.output.name
        self.scoped_patch('deployment', current, [{
            'op': 'replace', 'path': '/spec/template', 'value': restarted}])
        self.kube(['-n', NS, 'rollout', 'status',
                   'deployment/mqtt-pki', '--timeout=300s'], timeout=310)
        m.require(self.served_fingerprint() == expected
                  and self.host_state_digest() == state,
                  'MQTT host changed across seed-free restart')
        self.kube(['-n', NS, 'delete', 'secret', 'mqtt-pki-runtime'])
        legacy_path = (self.base / 'pki/controller-bootstrap/rollout' /
                       'mqtt-pki-runtime-secret.json')
        legacy_path.unlink(missing_ok=True)
        m.write(self.base / 'operator/env/PKI_MQTT_IMAGE', self.args.image + '\n')
        self.check('mqtt_host_adopted', {
            'issuer_id': issuer['issuer_id'], 'certificate_sha256': expected,
            'state_sha256': state, 'seed_removed': True,
            'restart_without_seed': True,
            'legacy_server_key_secret_removed': True,
            'actual_client_network_access': MQTT_CONSUMERS})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', choices=[
        'install-root-consumers', 'finish-root-consumers', 'activate-root',
        'prepare-intermediate', 'install-intermediate',
        'activate-intermediate', 'finish-intermediate-activation',
        'configure-certissuer', 'prepare-host', 'adopt-host'],
        required=True)
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--authority', required=True)
    parser.add_argument('--image')
    parser.add_argument('--failed')
    parser.add_argument('--intermediate')
    parser.add_argument('--prepared')
    args = parser.parse_args()
    m.require(args.phase != 'finish-root-consumers' or args.failed,
              'failed Root consumer evidence required')
    m.require(args.phase != 'finish-intermediate-activation' or args.failed,
              'failed intermediate activation evidence required')
    m.require(args.phase not in ('install-intermediate',
                                 'activate-intermediate',
                                 'finish-intermediate-activation',
                                 'configure-certissuer', 'prepare-host',
                                 'adopt-host') or args.intermediate,
              'prepared MQTT intermediate evidence required')
    m.require(args.phase != 'adopt-host' or args.prepared,
              'prepared MQTT host evidence required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/mqtt-host-rollout.lock'
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = MQTTHostRun(args)
    try:
        runner.preflight()
        {'install-root-consumers': runner.install_root_consumers,
         'finish-root-consumers': runner.finish_root_consumers,
         'activate-root': runner.activate_root,
         'prepare-intermediate': runner.prepare_intermediate,
         'install-intermediate': runner.install_intermediate,
         'activate-intermediate': runner.activate_intermediate,
         'finish-intermediate-activation':
             runner.finish_intermediate_activation,
         'configure-certissuer': runner.configure_certissuer,
         'prepare-host': runner.prepare_host,
         'adopt-host': runner.adopt_host}[args.phase]()
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
