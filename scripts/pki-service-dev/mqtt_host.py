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
import time
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
EMQX_SERVICE_SUBJECT = 'service:emqx-pki'
EMQX_SERVICE_STATE = '/var/lib/emqx-pki/identity/service-client.json'
MQTT_RUNTIME_SECRET = 'mqtt-pki-runtime-managed'
MQTT_MANAGEMENT_SECRET = 'pki-mqtt-host-management'
MQTT_CALLBACK_CA = 'pki-mqtt-callback-server-ca'
MQTT_RUNTIME_FIELDS = {
    'api-keys.conf', 'authentication', 'cookie', 'dashboard-password'}
IMAGE_PATTERN = re.compile(
    r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}')
EMQX_IMAGE_PATTERN = re.compile(
    r'ghcr\.io/hkt999rtk/rtk_cloud_dev/emqx-pki@sha256:[0-9a-f]{64}')


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
        'EMQX_PKI_RUNTIME_DIR': '/run/emqx-pki/private',
        'EMQX_PKI_HOST_IDENTITY_STATE': MQTT_HOST_STATE,
        'EMQX_PKI_HOST_NAME': MQTT_HOST,
        'EMQX_PKI_HOST_DNS_NAMES': MQTT_HOST,
        'EMQX_PKI_HOST_ROOT_SHA256':
            mqtt_root['certificate_fingerprint_sha256'],
        # The broker server leaf and its renewal caller have separate
        # state and keys.  The callback-management Secret is intentionally
        # excluded: it authenticates EMQX callbacks only.
        'EMQX_PKI_SERVICE_CLIENT_IDENTITY_STATE': EMQX_SERVICE_STATE,
        'EMQX_PKI_SERVICE_CLIENT_ROOT_SHA256':
            service_root['certificate_fingerprint_sha256'],
        'EMQX_PKI_HOST_RENEWAL_URL': 'https://' + issuer + ':9443',
        'EMQX_PKI_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256':
            service_root['certificate_fingerprint_sha256'],
        'EMQX_PKI_HOST_RENEWAL_SERVER_PKI_NAME': issuer,
        'EMQX_PKI_HOST_RENEWAL_TLS_CA': '/run/pki-service-root/root.pem',
        'EMQX_PKI_HOST_RENEWAL_SERVER_PKI_SWEEP_INTERVAL': '10s',
    }


def emqx_bootstrap_settings(settings):
    """Add one-use bootstrap files without contaminating broker settings."""
    m.require('EMQX_PKI_HOST_RENEWAL_CLIENT_CERT' not in settings
              and 'EMQX_PKI_HOST_RENEWAL_CLIENT_KEY' not in settings,
              'managed EMQX settings retain a static renewal credential')
    return dict(settings,
                EMQX_PKI_SERVICE_CLIENT_IDENTITY_BOOTSTRAP_CERT='/run/emqx-bootstrap/client.crt',
                EMQX_PKI_SERVICE_CLIENT_IDENTITY_BOOTSTRAP_KEY='/run/emqx-bootstrap/client.key')


def emqx_bootstrap_pod(image, settings, service_root_configmap, pull_secrets):
    """Build a one-shot Pod whose ephemeral cert may enroll one Service client.

    The generated CA and client key live only in ``emptyDir``. The controller
    reads the public CA after the Pod is ready, then opens CertIssuer's exact
    temporary route. The Pod waits for that gate before it runs bootstrap.
    """
    m.require(EMQX_IMAGE_PATTERN.fullmatch(image), 'verified dev EMQX image digest required')
    m.require(service_root_configmap and all(set(item) == {'name'} and item['name']
                                             for item in pull_secrets),
              'reviewed Service root and registry pull references required')
    values = emqx_bootstrap_settings(settings)
    values.update({
        'PKI_DATABASE_URL': None,
        'EMQX_PKI_BOOTSTRAP_REQUEST_ID': 'dev-emqx-service-bootstrap-' + uuid.uuid4().hex,
    })
    command = '''set -eu; umask 077; mkdir -p /run/emqx-bootstrap;
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out /run/emqx-bootstrap/ca.key;
openssl req -x509 -new -key /run/emqx-bootstrap/ca.key -sha256 -days 1 \
  -subj /CN=dev-emqx-service-bootstrap-ca \
  -addext basicConstraints=critical,CA:TRUE \
  -addext keyUsage=critical,keyCertSign,cRLSign -out /run/emqx-bootstrap/ca.crt;
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out /run/emqx-bootstrap/client.key;
openssl req -new -key /run/emqx-bootstrap/client.key -subj /CN=service:emqx-pki \
  -out /run/emqx-bootstrap/client.csr;
printf 'basicConstraints=CA:FALSE\\nextendedKeyUsage=clientAuth\\nkeyUsage=digitalSignature\\n' \
  > /run/emqx-bootstrap/client.ext;
openssl x509 -req -in /run/emqx-bootstrap/client.csr -CA /run/emqx-bootstrap/ca.crt \
  -CAkey /run/emqx-bootstrap/ca.key -CAcreateserial -days 1 -sha256 \
  -extfile /run/emqx-bootstrap/client.ext -out /run/emqx-bootstrap/client.crt;
chmod 600 /run/emqx-bootstrap/*; touch /run/emqx-bootstrap/ready;
while test ! -f /run/emqx-bootstrap/issue; do sleep 1; done;
exec /usr/local/bin/emqxpkihost bootstrap'''
    env = []
    for name, value in values.items():
        if name == 'PKI_DATABASE_URL':
            env.append({'name': name, 'valueFrom': {'secretKeyRef': {
                'name': 'pki-controller-database', 'key': 'url'}}})
        else:
            env.append({'name': name, 'value': value})
    return {
        'apiVersion': 'v1', 'kind': 'Pod',
        'metadata': {'name': 'emqx-service-bootstrap', 'namespace': NS,
                     'labels': {'app.kubernetes.io/name': 'mqtt-pki',
                                'rtk.cloud/purpose': 'emqx-service-bootstrap'}},
        'spec': {'restartPolicy': 'Never', 'automountServiceAccountToken': False,
                 'imagePullSecrets': pull_secrets,
                 'securityContext': {'runAsUser': 1000, 'runAsGroup': 1000,
                                     'fsGroup': 1000,
                                     'fsGroupChangePolicy': 'OnRootMismatch'},
                 'containers': [{'name': 'bootstrap', 'image': image,
                                 'command': ['sh', '-c'], 'args': [command], 'env': env,
                                 'volumeMounts': [
                                     {'name': 'state', 'mountPath': '/var/lib/emqx-pki'},
                                     {'name': 'service-root', 'mountPath': '/run/pki-service-root',
                                      'readOnly': True},
                                     {'name': 'bootstrap', 'mountPath': '/run/emqx-bootstrap'}],
                                 'securityContext': {'allowPrivilegeEscalation': False,
                                                     'capabilities': {'drop': ['ALL']},
                                                     'runAsNonRoot': True,
                                                     'readOnlyRootFilesystem': True}}],
                 'volumes': [
                     {'name': 'state', 'persistentVolumeClaim': {'claimName': MQTT_HOST_PVC}},
                     {'name': 'service-root', 'configMap': {'name': service_root_configmap,
                                                            'defaultMode': 0o444}},
                     {'name': 'bootstrap', 'emptyDir': {}}]}}


def certissuer_emqx_bootstrap_template(owner, pattern, bootstrap=False):
    """Open exactly one Service client enrollment, or restore its closed policy."""
    template = json.loads(json.dumps(owner['spec']['template']))
    containers = template['spec'].get('containers', [])
    m.require(len(containers) == 1 and containers[0].get('name') == 'certissuer',
              'CertIssuer container ownership changed')
    values = {item['name']: item.get('value') for item in containers[0].get('env', [])}
    m.require(len(values) == len(containers[0].get('env', [])),
              'duplicate CertIssuer environment setting')
    expected = '^service-provisioner$' if bootstrap else '^service:emqx-pki$'
    replacement = '^service:emqx-pki$' if bootstrap else '^service-provisioner$'
    m.require(values.get('CERT_ISSUER_MQTT_SERVER_CLIENT_CN_PATTERN') in
              ('^emqx-pki$', '^service:emqx-pki$')
              and values.get('CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN') == expected,
              'CertIssuer bootstrap policy changed')
    updates = {'CERT_ISSUER_MQTT_SERVER_CLIENT_CN_PATTERN': pattern,
               'CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN': replacement}
    remove = ()
    if bootstrap:
        updates.update({'CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER': EMQX_SERVICE_SUBJECT,
                        'CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SUBJECT': EMQX_SERVICE_SUBJECT,
                        'CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CA':
                            '/etc/video-cloud/certissuer/emqx-bootstrap-ca.crt'})
    else:
        remove = ('CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER',
                  'CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SUBJECT',
                  'CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CA')
    containers[0]['env'] = [item for item in containers[0]['env']
                            if item['name'] not in remove]
    containers[0]['env'] = h.with_env(containers[0]['env'], updates)
    return template


def reconfigure_managed_mqtt_template(owner, image, settings, output_name):
    """Replace only EMQX host identity settings on the already-managed Pod."""
    template = json.loads(json.dumps(owner['spec']['template']))
    pod = template['spec']
    containers = {item['name']: item for item in pod.get('containers', [])}
    m.require(set(containers) == {'mqtt', 'pkibroker'}, 'dedicated MQTT Pod topology changed')
    mqtt = containers['mqtt']
    current = {item['name']: item.get('value') for item in mqtt.get('env', [])}
    m.require(len(current) == len(mqtt.get('env', []))
              and current.get('EMQX_PKI_HOST_IDENTITY_STATE') == MQTT_HOST_STATE
              and current.get('EMQX_PKI_HOST_NAME') == MQTT_HOST
              and current.get('EMQX_PKI_HOST_DNS_NAMES') == MQTT_HOST
              and current.get('EMQX_PKI_HOST_RENEWAL_CLIENT_CERT')
              and current.get('EMQX_PKI_HOST_RENEWAL_CLIENT_KEY'),
              'managed MQTT predecessor configuration changed')
    m.require(not ({'EMQX_PKI_SERVICE_CLIENT_IDENTITY_STATE',
                    'EMQX_PKI_SERVICE_CLIENT_ROOT_SHA256'} & set(current)),
              'managed EMQX Service client state already configured')
    mqtt['image'] = image
    mqtt['env'] = [item for item in mqtt['env']
                   if not item['name'].startswith('EMQX_PKI_')]
    mqtt['env'] = h.with_env(mqtt['env'], settings)
    volumes = {item['name']: item for item in pod.get('volumes', [])}
    m.require(volumes.get('mqtt-host-state', {}).get('persistentVolumeClaim', {}).get(
                  'claimName') == MQTT_HOST_PVC
              and volumes.get('mqtt-host-runtime', {}).get('emptyDir') == {}
              and volumes.get('mqtt-host-management', {}).get('secret', {}).get(
                  'secretName') == MQTT_MANAGEMENT_SECRET
              and volumes.get('pki-service-root', {}).get('configMap', {}).get('name'),
              'managed MQTT volume ownership changed')
    template.setdefault('metadata', {}).setdefault('annotations', {})[
        'rtk.cloud/emqx-service-client-adoption'] = output_name
    return template


def managed_runtime_data(source):
    m.require(set(source) >= MQTT_RUNTIME_FIELDS,
              'existing MQTT runtime fields incomplete')
    result = {name: source[name] for name in sorted(MQTT_RUNTIME_FIELDS)}
    authentication = json.loads(base64.b64decode(
        result['authentication']).decode())
    m.require(isinstance(authentication, list) and len(authentication) == 1
              and authentication[0].get('backend') == 'http'
              and authentication[0].get('ssl', {}).get('enable') is True,
              'existing MQTT callback authentication changed')
    ssl = authentication[0]['ssl']
    ssl['cacertfile'] = '/run/mqtt-callback-ca/ca.crt'
    ssl['certfile'] = '/run/emqx-pki-management/tls.crt'
    ssl['keyfile'] = '/run/emqx-pki-management/tls.key'
    result['authentication'] = base64.b64encode(
        json.dumps(authentication, separators=(',', ':')).encode()).decode()
    return result


def mqtt_host_state_initializer(image):
    return {
        'name': 'prepare-mqtt-host-state', 'image': image,
        'command': ['sh', '-c'],
        'args': ['set -eu; umask 077; mkdir -p /run/emqx-pki/private; '
                 'chmod 700 /run/emqx-pki/private; '
                 'if [ -d /var/lib/emqx-pki/identity ]; then '
                 'chmod 700 /var/lib/emqx-pki/identity; '
                 'find /var/lib/emqx-pki/identity -type f '
                 '-exec chmod 600 {} +; fi'],
        'volumeMounts': [
            {'name': 'mqtt-host-state', 'mountPath': '/var/lib/emqx-pki'},
            {'name': 'mqtt-host-runtime', 'mountPath': '/run/emqx-pki'}],
        'securityContext': {'allowPrivilegeEscalation': False,
                            'capabilities': {'drop': ['ALL']},
                            'runAsUser': 1000, 'runAsGroup': 1000,
                            'runAsNonRoot': True}}


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
        {'name': 'mqtt-host-runtime', 'mountPath': '/run/emqx-pki'},
        {'name': 'mqtt-host-management',
         'mountPath': '/run/emqx-pki-management', 'readOnly': True},
        {'name': 'mqtt-callback-ca', 'mountPath': '/run/mqtt-callback-ca',
         'readOnly': True},
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
        {'name': 'mqtt-host-runtime', 'emptyDir': {}},
        {'name': 'mqtt-host-management', 'secret': {
            'secretName': MQTT_MANAGEMENT_SECRET, 'defaultMode': 288}},
        {'name': 'mqtt-callback-ca', 'configMap': {
            'name': MQTT_CALLBACK_CA}},
    ])
    pod.setdefault('initContainers', []).append(
        mqtt_host_state_initializer(image))
    template.setdefault('metadata', {}).setdefault('annotations', {})[
        'rtk.cloud/mqtt-host-rollout'] = output_name
    return template


def crl_state_initializer(image):
    return {
        'name': 'mqtt-pki-state-init', 'image': image,
        'command': ['sh', '-c'],
        'args': ['set -eu; umask 077; mkdir -p /var/lib/mqtt-pki/crls '
                 '/var/lib/mqtt-pki/identity; chmod 700 '
                 '/var/lib/mqtt-pki/crls /var/lib/mqtt-pki/identity'],
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


def compatible_crl_state_initializer(value, numeric_identity=True):
    """Accept only the two hardened initializer forms emitted before recovery.

    A later Dev application-image rollout can leave this init container at an
    older image digest.  Its image is intentionally replaced during recovery,
    but the mount, command and non-root security contract must still be one of
    the reviewed forms below.
    """
    image = value.get('image')
    if not isinstance(image, str) or not image:
        return False
    if valid_crl_state_initializer(value, image, numeric_identity):
        return True
    legacy = crl_state_initializer(image)
    legacy['args'] = [
        'set -eu; umask 077; mkdir -p /var/lib/mqtt-pki/crls '
        '/var/lib/mqtt-pki/identity/service-crls; chmod 700 '
        '/var/lib/mqtt-pki/crls /var/lib/mqtt-pki/identity '
        '/var/lib/mqtt-pki/identity/service-crls']
    for field in ('name', 'image', 'command', 'args', 'volumeMounts'):
        if value.get(field) != legacy[field]:
            return False
    security = value.get('securityContext', {})
    required = legacy['securityContext']
    for field in ('allowPrivilegeEscalation', 'capabilities', 'runAsNonRoot',
                  'readOnlyRootFilesystem'):
        if security.get(field) != required[field]:
            return False
    ids = (security.get('runAsUser'), security.get('runAsGroup'))
    return ids == ((10001, 10001) if numeric_identity else (None, None))


def valid_mqtt_crl_manifest(configmap):
    """Return whether an existing CRL source is a reviewed Dev MQTT manifest."""
    try:
        entries = json.loads(configmap['data']['crls.json'])
    except (KeyError, TypeError, json.JSONDecodeError):
        return False
    return (configmap.get('immutable') is True and bool(entries)
            and all(isinstance(entry, dict)
                    and isinstance(entry.get('state_path'), str)
                    and entry['state_path'].startswith('/var/lib/mqtt-pki/crls/')
                    and entry.get('issuer', {}).get('environment') == 'dev'
                    and entry['issuer'].get('trust_domain') == 'mqtt'
                    and re.fullmatch(r'[0-9a-f-]{36}',
                                     entry['issuer'].get('issuer_id', ''))
                    for entry in entries))


class MQTTHostRun(h.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.remote_probe = '/run/emqx-pki/private/.pki-dev-probe-' + uuid.uuid4().hex
        self.probe_pods = {}
        self.report['foundation_scope'] = (
            'Dev-only dedicated MQTT hierarchy and actual MQTT clients')
        self.report['mqtt_host_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def close(self):
        for uid, pod in self.probe_pods.items():
            try:
                if self.obj('pod', pod)['metadata']['uid'] == uid:
                    self.kube(['-n', NS, 'exec', pod, '-c', 'mqtt', '--',
                               'rm', '-f', self.remote_probe])
            except RuntimeError:
                pass
        super().close()

    def sql(self, query):
        return self.kube([
            '-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0',
            '--', 'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres',
            '-d', 'video_cloud', '-At'], query).strip()

    def mqtt_pod(self, ready=True):
        owner = self.obj('deployment', 'mqtt-pki')
        selector = ','.join(key + '=' + value for key, value in
                            owner['spec']['selector']['matchLabels'].items())
        pods = json.loads(self.kube([
            '-n', NS, 'get', 'pods', '-l', selector, '-o', 'json']))['items']
        pods = [pod for pod in pods
                if not pod['metadata'].get('deletionTimestamp')
                and pod.get('status', {}).get('phase') not in ('Succeeded', 'Failed')]
        m.require(len(pods) == 1, 'expected one MQTT identity owner')
        if ready:
            statuses = {item['name']: item for item in
                        pods[0]['status'].get('containerStatuses', [])}
            m.require(set(statuses) == {'mqtt', 'pkibroker'}
                      and all(item.get('ready') for item in statuses.values()),
                      'MQTT identity owner is not ready')
        return pods[0]

    def install_host_probe(self, pod=None):
        pod = pod or self.mqtt_pod()
        uid, name = pod['metadata']['uid'], pod['metadata']['name']
        if uid not in self.probe_pods:
            binary = self.output / 'pki-dev-probe-linux'
            if not binary.exists():
                result = m.subprocess.run([
                    'go', 'build', '-trimpath', '-ldflags=-s -w', '-o',
                    str(binary), './pki-dev-probe'], cwd=m.WORKSPACE / 'scripts/go',
                    env=dict(os.environ, GOOS='linux', GOARCH='amd64',
                             CGO_ENABLED='0', GOWORK='off'),
                    capture_output=True, timeout=180)
                m.require(result.returncode == 0,
                          'MQTT public state probe build failed')
            self.kube([
                '-n', NS, 'exec', '-i', name, '-c', 'mqtt', '--', 'sh', '-c',
                'umask 077; base64 -d > ' + self.remote_probe
                + ' && chmod 700 ' + self.remote_probe],
                base64.b64encode(binary.read_bytes()).decode())
            self.probe_pods[uid] = name
        return name

    def inspect_host_state(self, pod=None):
        name = self.install_host_probe(pod)
        return json.loads(self.kube([
            '-n', NS, 'exec', name, '-c', 'mqtt', '--', self.remote_probe,
            'service-state', MQTT_HOST_STATE]))

    def server_rows(self):
        raw = self.sql(
            "SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,caller,"
            "status,fingerprint,dns_names,certificate_pem,issued_at,revoked_at "
            "FROM pki_server_issuances WHERE environment='dev' "
            "AND domain='mqtt' ORDER BY issued_at,request_id) t;")
        return [row for row in (json.loads(line) for line in raw.splitlines()
                                if line)
                if row['dns_names'] == [MQTT_HOST]]

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
            changed = False
            env = {item['name']: item.get('value')
                   for item in container.get('env', [])}
            m.require(env.get('VIDEO_CLOUD_MQTT_ROOT_ID')
                      and env.get('VIDEO_CLOUD_MQTT_ROOT_STATE')
                      and env.get('VIDEO_CLOUD_MQTT_ROOTS') ==
                      '/run/pki-mqtt/roots.pem',
                      'MQTT dynamic Root policy settings changed: ' + name)
            if env['VIDEO_CLOUD_MQTT_ROOT_ID'] != root['issuer_id']:
                # The existing private state remains in place: it contains the
                # reviewed overlap roots.  Changing only its governing Root ID
                # makes the active successor's policy authoritative before the
                # predecessor CRL manifest is withdrawn.
                container['env'] = h.with_env(container['env'], {
                    'VIDEO_CLOUD_MQTT_ROOT_ID': root['issuer_id']})
                changed = True
            volumes = [item for item in pod['volumes']
                       if item['name'] == 'mqtt-pki-crls']
            m.require(len(volumes) == 1 and volumes[0].get('configMap', {}).get('name'),
                      'MQTT CRL volume changed: ' + name)
            current_manifest = volumes[0]['configMap']['name']
            if current_manifest != configmap:
                m.require(valid_mqtt_crl_manifest(
                              self.obj('configmap', current_manifest)),
                          'MQTT CRL source is not a reviewed Dev manifest: ' + name)
                volumes[0]['configMap']['name'] = configmap
                changed = True
            existing = [item for item in pod.get('initContainers', [])
                        if item['name'] == 'mqtt-pki-state-init']
            if not existing:
                pod.setdefault('initContainers', []).append(
                    crl_state_initializer(container['image']))
                changed = True
            else:
                expected = crl_state_initializer(container['image'])
                if not valid_crl_state_initializer(
                        existing[0], container['image']):
                    m.require(name in MQTT_CONSUMERS
                              and (compatible_crl_state_initializer(existing[0])
                                   or compatible_crl_state_initializer(
                                       existing[0], False)),
                              'MQTT CRL initializer changed: ' + name)
                    pod['initContainers'] = [
                        expected if item['name'] == 'mqtt-pki-state-init'
                        else item for item in pod['initContainers']]
                    changed = True
            if changed:
                annotations = template.setdefault('metadata', {}).setdefault(
                    'annotations', {})
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
            'CERT_ISSUER_MQTT_SERVER_CLIENT_CN_PATTERN': '^service:emqx-pki$'})
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
            'caller_pattern': '^service:emqx-pki$', 'image': self.args.image})

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
               'metadata': {'name': podname, 'namespace': NS,
                            'labels': {'app.kubernetes.io/name': 'mqtt-pki'}},
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

    def bootstrap_emqx_service_client(self):
        """Seed the one managed Service client and MQTT leaf on the broker PVC."""
        m.require(EMQX_IMAGE_PATTERN.fullmatch(self.args.image or '')
                  and IMAGE_PATTERN.fullmatch(self.args.certissuer_image or ''),
                  'verified managed EMQX and CertIssuer image digests required')
        source = Path(self.args.service_authority)
        evidence = m.read(source / 'report.json')
        service = m.read(source / 'intermediate-active.json')
        m.require(evidence.get('status') == 'passed' and service.get('status') == 'active'
                  and service.get('environment') == 'dev'
                  and service.get('trust_domain') == 'service'
                  and EMQX_SERVICE_SUBJECT in service.get('service_client_ids', []),
                  'active Service v9 evidence for EMQX is required')
        m.require(self.api('/issuers/' + service['issuer_id']) == service,
                  'active Service v9 authority changed')
        mqtt_root, mqtt_issuer, _ = self.ready_intermediate(status='active')
        root = self.api('/issuers/' + service['parent_issuer_id'])
        m.require(root.get('status') == 'active' and root.get('kind') == 'root'
                  and root.get('trust_domain') == 'service',
                  'active Service Root changed')
        owner = self.obj('deployment', 'mqtt-pki')
        m.require(owner['spec'].get('replicas') == 0
                  and owner['spec'].get('strategy', {}).get('type') == 'Recreate',
                  'MQTT broker must remain stopped for Service bootstrap')
        pvc = self.obj('persistentvolumeclaim', MQTT_HOST_PVC)
        m.require(pvc.get('status', {}).get('phase') == 'Bound',
                  'fresh MQTT host state PVC is required')
        volumes = {item['name']: item for item in owner['spec']['template']['spec'].get('volumes', [])}
        root_volume = volumes.get('pki-service-root', {}).get('configMap', {})
        root_map = self.obj('configmap', root_volume.get('name', ''))
        m.require(root_map.get('immutable') is True
                  and root_map.get('data', {}).get('root.pem') == root['certificate_pem'],
                  'broker Service Root projection changed')
        settings = mqtt_host_settings(mqtt_root, root)
        requested = emqx_bootstrap_pod(
            self.args.image, settings, root_volume['name'],
            owner['spec']['template']['spec'].get('imagePullSecrets', []))
        m.require(not self.kube(['-n', NS, 'get', 'pod', 'emqx-service-bootstrap',
                                 '--ignore-not-found', '-o', 'name']).strip(),
                  'EMQX Service bootstrap Pod already exists; reconcile')
        self.save('emqx-service-bootstrap-pod.json', requested)
        self.create(requested)
        self.kube(['-n', NS, 'wait', '--for=condition=Ready',
                   'pod/emqx-service-bootstrap', '--timeout=180s'], timeout=190)
        self.kube(['-n', NS, 'exec', 'emqx-service-bootstrap', '--', 'test', '-f',
                   '/run/emqx-bootstrap/ready'])
        bootstrap_ca = self.kube(['-n', NS, 'exec', 'emqx-service-bootstrap', '--',
                                  'cat', '/run/emqx-bootstrap/ca.crt'])
        # Public bootstrap CA is recorded solely for exact removal after this
        # enrollment. The private CA/client keys never leave emptyDir.
        self.save('emqx-bootstrap-ca.pem', bootstrap_ca)
        bootstrap_key = 'emqx-bootstrap-ca.crt'
        initial = self.obj('secret', 'certissuer-runtime')
        m.require(bootstrap_key not in initial['data'],
                  'CertIssuer bootstrap CA field already exists')
        opened, ca_added = False, False
        try:
            self.scoped_patch('secret', initial, [{
                'op': 'add', 'path': '/data/' + bootstrap_key,
                'value': base64.b64encode(bootstrap_ca.encode()).decode()}])
            ca_added = True
            certissuer = self.obj('deployment', 'certissuer')
            template = certissuer_emqx_bootstrap_template(
                certissuer, '^service:emqx-pki$', bootstrap=True)
            template['spec']['containers'][0]['image'] = self.args.certissuer_image
            template.setdefault('metadata', {}).setdefault('annotations', {})[
                'rtk.cloud/emqx-service-bootstrap'] = self.output.name
            self.scoped_patch('deployment', certissuer, [{
                'op': 'replace', 'path': '/spec/template', 'value': template}])
            opened = True
            self.kube(['-n', NS, 'rollout', 'status', 'deployment/certissuer',
                       '--timeout=300s'], timeout=310)
            self.kube(['-n', NS, 'exec', 'emqx-service-bootstrap', '--', 'touch',
                       '/run/emqx-bootstrap/issue'])
            self.kube(['-n', NS, 'wait', '--for=jsonpath={.status.phase}=Succeeded',
                       'pod/emqx-service-bootstrap', '--timeout=300s'], timeout=310)
        finally:
            # Close both the listener exception and its CA even if enrollment
            # fails. A failed Pod can be examined without retaining authority.
            if opened:
                certissuer = self.obj('deployment', 'certissuer')
                template = certissuer_emqx_bootstrap_template(
                    certissuer, '^service:emqx-pki$', bootstrap=False)
                template.setdefault('metadata', {}).setdefault('annotations', {})[
                    'rtk.cloud/emqx-service-bootstrap-closed'] = self.output.name
                self.scoped_patch('deployment', certissuer, [{
                    'op': 'replace', 'path': '/spec/template', 'value': template}])
                self.kube(['-n', NS, 'rollout', 'status', 'deployment/certissuer',
                           '--timeout=300s'], timeout=310)
            secret = self.obj('secret', 'certissuer-runtime')
            m.require(secret['data'].get(bootstrap_key) ==
                      base64.b64encode(bootstrap_ca.encode()).decode(),
                      'CertIssuer bootstrap CA changed during enrollment')
            self.scoped_patch('secret', secret, [{
                'op': 'test', 'path': '/data/' + bootstrap_key,
                'value': secret['data'][bootstrap_key]},
                {'op': 'remove', 'path': '/data/' + bootstrap_key}])
            if ca_added:
                certissuer = self.obj('deployment', 'certissuer')
                template = json.loads(json.dumps(certissuer['spec']['template']))
                template.setdefault('metadata', {}).setdefault('annotations', {})[
                    'rtk.cloud/emqx-bootstrap-ca-removed'] = self.output.name
                self.scoped_patch('deployment', certissuer, [{
                    'op': 'replace', 'path': '/spec/template', 'value': template}])
                self.kube(['-n', NS, 'rollout', 'status', 'deployment/certissuer',
                           '--timeout=300s'], timeout=310)
        rows = self.kube(['-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--',
                          'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d',
                          'video_cloud', '-At'],
                         "SELECT row_to_json(t) FROM (SELECT issuer_id,subject,caller,status,fingerprint,revoked_at "
                         "FROM pki_service_client_issuances WHERE environment='dev' AND subject='service:emqx-pki' "
                         "ORDER BY issued_at DESC LIMIT 1) t;").strip()
        issued = json.loads(rows)
        m.require(issued['issuer_id'] == service['issuer_id']
                  and issued['subject'] == EMQX_SERVICE_SUBJECT
                  and issued['caller'] == EMQX_SERVICE_SUBJECT
                  and issued['status'] == 'succeeded' and issued['revoked_at'] is None,
                  'EMQX managed Service client issuance differs')
        self.save('emqx-service-client-issuance.json', issued)
        current = self.obj('deployment', 'mqtt-pki')
        template = reconfigure_managed_mqtt_template(current, self.args.image, settings,
                                                      self.output.name)
        self.scoped_patch('deployment', current, [{
            'op': 'replace', 'path': '/spec/template', 'value': template},
            {'op': 'replace', 'path': '/spec/replicas', 'value': 1}])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/mqtt-pki',
                   '--timeout=300s'], timeout=310)
        self.save('mqtt-root.pem', mqtt_root['certificate_pem'])
        state = self.inspect_host_state()
        rows = [row for row in self.server_rows() if row['fingerprint'] == state['fingerprint']]
        m.require(not state['pending'] and len(rows) == 1
                  and rows[0]['issuer_id'] == mqtt_issuer['issuer_id']
                  and rows[0]['caller'] == EMQX_SERVICE_SUBJECT
                  and rows[0]['status'] == 'succeeded' and rows[0]['revoked_at'] is None
                  and self.served_fingerprint() == state['fingerprint'],
                  'managed EMQX server leaf was not issued and served')
        self.kube(['-n', NS, 'delete', 'pod', 'emqx-service-bootstrap', '--wait=true',
                   '--timeout=90s'])
        self.check('emqx_service_client_and_server_bootstrapped', {
            'service_issuer_id': service['issuer_id'], 'service_client_fingerprint': issued['fingerprint'],
            'mqtt_issuer_id': mqtt_issuer['issuer_id'], 'mqtt_server_fingerprint': state['fingerprint'],
            'bootstrap_ca_removed': True, 'bootstrap_policy_closed': True,
            'private_key_exported': False, 'staging_touched': False})

    def transition_host_root(self):
        """Move an already managed broker to the reviewed successor authority."""
        root, issuer, _ = self.ready_intermediate(status='active')
        m.require(IMAGE_PATTERN.fullmatch(self.args.image or ''),
                  'verified dev EMQX host image digest required')
        owner = self.obj('deployment', 'mqtt-pki')
        m.require(owner['spec'].get('replicas') == 1
                  and owner['spec'].get('strategy', {}).get('type') == 'Recreate'
                  and self.obj('persistentvolumeclaim', MQTT_HOST_PVC),
                  'managed MQTT host ownership changed')
        template = json.loads(json.dumps(owner['spec']['template']))
        mqtt = next((item for item in template['spec']['containers']
                     if item['name'] == 'mqtt'), None)
        m.require(mqtt is not None, 'managed MQTT host container missing')
        env = {item['name']: item.get('value') for item in mqtt.get('env', [])}
        prior_root = env.get('EMQX_PKI_HOST_ROOT_SHA256', '')
        m.require(re.fullmatch(r'[0-9a-f]{64}', prior_root)
                  and prior_root != root['certificate_fingerprint_sha256']
                  and env.get('EMQX_PKI_HOST_IDENTITY_STATE') == MQTT_HOST_STATE
                  and env.get('EMQX_PKI_HOST_NAME') == MQTT_HOST
                  and env.get('EMQX_PKI_HOST_DNS_NAMES') == MQTT_HOST,
                  'managed MQTT predecessor configuration changed')
        self.save('mqtt-root.pem', root['certificate_pem'])
        before = self.inspect_host_state()
        rows = self.server_rows()
        old = [row for row in rows if row['fingerprint'] == before['fingerprint']]
        m.require(not before['pending'] and len(old) == 1
                  and old[0]['issuer_id'] != issuer['issuer_id']
                  and old[0]['status'] == 'succeeded'
                  and old[0]['revoked_at'] is None,
                  'managed MQTT predecessor state differs')
        self.save('predecessor.json', {'state': before, 'row': old[0]})
        mqtt['image'] = self.args.image
        mqtt['env'] = h.with_env(mqtt['env'], {
            'EMQX_PKI_HOST_ROOT_SHA256':
                root['certificate_fingerprint_sha256']})
        template.setdefault('metadata', {}).setdefault('annotations', {})[
            'rtk.cloud/mqtt-host-root-transition'] = self.output.name
        self.scoped_patch('deployment', owner, [{
            'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.wait_available('mqtt-pki')
        after = self.inspect_host_state()
        rows = self.server_rows()
        successor = [row for row in rows if row['fingerprint'] == after['fingerprint']]
        m.require(not after['pending']
                  and after['fingerprint'] != before['fingerprint']
                  and len(successor) == 1
                  and successor[0]['issuer_id'] == issuer['issuer_id']
                  and successor[0]['caller'] == 'emqx-pki'
                  and successor[0]['status'] == 'succeeded'
                  and successor[0]['revoked_at'] is None
                  and self.served_fingerprint() == after['fingerprint'],
                  'MQTT successor host was not issued and served')
        clients = self.wait_actual_clients()
        self.forward('api', NS, 'video-cloud-api-pki', 8443)
        self.forward('mqtt', NS, 'mqtt-pki', 8883)
        self.device_baseline()
        self.save('successor.json', {'state': after, 'row': successor[0]})
        self.check('mqtt_managed_host_transitioned_to_successor', {
            'issuer_id': issuer['issuer_id'],
            'predecessor_fingerprint': before['fingerprint'],
            'successor_fingerprint': after['fingerprint'],
            'private_key_exported': False,
            'actual_clients_reconnected': clients,
            'device_mqtt_acl_qos1': 'passed'})

    def recover_host_transition(self):
        """Dev-only recovery for a predecessor-bound managed host state."""
        failed = m.read(Path(self.args.failed) / 'report.json')
        m.require(failed['status'] == 'failed'
                  and failed['phase'] in ('transition-host-root',
                                          'recover-host-transition'),
                  'failed managed-host transition evidence required')
        root, issuer, _ = self.ready_intermediate(status='active')
        m.require(IMAGE_PATTERN.fullmatch(self.args.image or ''),
                  'verified dev EMQX host image digest required')
        owner = self.obj('deployment', 'mqtt-pki')
        m.require(owner['spec'].get('replicas') in (0, 1)
                  and owner['spec'].get('strategy', {}).get('type') == 'Recreate',
                  'managed MQTT recovery ownership changed')
        mqtt = next((item for item in owner['spec']['template']['spec']['containers']
                     if item['name'] == 'mqtt'), None)
        env = {item['name']: item.get('value') for item in mqtt.get('env', [])}
        m.require(mqtt is not None
                  and env.get('EMQX_PKI_HOST_ROOT_SHA256') ==
                  root['certificate_fingerprint_sha256']
                  and env.get('EMQX_PKI_HOST_IDENTITY_STATE') == MQTT_HOST_STATE,
                  'successor MQTT host transition is not installed')
        previous_pvc = self.obj('persistentvolumeclaim', MQTT_HOST_PVC)
        resuming = (failed['phase'] == 'recover-host-transition'
                    and previous_pvc.get('status', {}).get('phase') == 'Bound')
        self.save('replaced-host-pvc.json', {
            'uid': previous_pvc['metadata']['uid'], 'spec': previous_pvc['spec']})
        if not resuming and owner['spec']['replicas'] == 1:
            self.scoped_patch('deployment', owner, [{
                'op': 'replace', 'path': '/spec/replicas', 'value': 0}])
            deadline = time.monotonic() + 120
            while self.obj('deployment', 'mqtt-pki').get('status', {}).get('replicas', 0):
                m.require(time.monotonic() < deadline,
                          'MQTT host did not stop before Dev state reset')
                time.sleep(2)
        # Retain volumes keep their old claim UID.  A reset must request a new
        # volume, never force a replacement claim onto predecessor storage.
        if not resuming and (previous_pvc.get('status', {}).get('phase') != 'Bound' or \
                previous_pvc['spec'].get('volumeName')):
            self.kube(['-n', NS, 'delete', 'persistentvolumeclaim', MQTT_HOST_PVC,
                       '--wait=true', '--timeout=180s'], timeout=190)
        if not resuming:
            spec = json.loads(json.dumps(previous_pvc['spec']))
            spec.pop('volumeName', None)
            self.create({'apiVersion': 'v1', 'kind': 'PersistentVolumeClaim',
                         'metadata': {'name': MQTT_HOST_PVC, 'namespace': NS},
                         'spec': spec})
            self.kube(['-n', NS, 'wait', '--for=jsonpath={.status.phase}=Bound',
                       'persistentvolumeclaim/' + MQTT_HOST_PVC,
                       '--timeout=180s'], timeout=190)
        volumes = {item['name']: item for item in
                   owner['spec']['template']['spec']['volumes']}
        service_volume = volumes.get('pki-service-root', {}).get('configMap', {})
        m.require(service_volume.get('name'),
                  'managed MQTT Service root source changed')
        service_root = self.obj('configmap', service_volume['name'])['data']['root.pem']
        self.save('service-root.pem', service_root)
        podname = 'pki-host-seed-mqtt-recovery-' + uuid.uuid4().hex[:8]
        script = (
            'set -eu; umask 077; mkdir -p /state/seed; '
            'openssl genpkey -algorithm EC -pkeyopt '
            'ec_paramgen_curve:P-256 -out /state/seed/key.pem; '
            'openssl req -new -key /state/seed/key.pem '
            '-subj /CN=' + MQTT_HOST + ' -addext subjectAltName=DNS:' +
            MQTT_HOST + ' -out /state/seed/csr.pem; exec sleep 3600')
        pod = {'apiVersion': 'v1', 'kind': 'Pod',
               'metadata': {'name': podname, 'namespace': NS,
                            'labels': {'app.kubernetes.io/name': 'mqtt-pki'}},
               'spec': {'restartPolicy': 'Never',
                        'automountServiceAccountToken': False,
                        'imagePullSecrets': owner['spec']['template']['spec'].get(
                            'imagePullSecrets', []),
                        'securityContext': {'runAsUser': 1000,
                                            'runAsGroup': 1000,
                                            'fsGroup': 1000,
                                            'fsGroupChangePolicy': 'OnRootMismatch'},
                        'containers': [{'name': 'seed', 'image': self.args.image,
                                        'command': ['sh', '-c', script],
                                        'volumeMounts': [
                                            {'name': 'state', 'mountPath': '/state'},
                                            {'name': 'management', 'mountPath': '/run/mqtt-management', 'readOnly': True},
                                            {'name': 'service-root', 'mountPath': '/run/service-root', 'readOnly': True}]}],
                        'volumes': [{'name': 'state', 'persistentVolumeClaim': {
                            'claimName': MQTT_HOST_PVC}},
                                    {'name': 'management', 'secret': {
                                        'secretName': MQTT_MANAGEMENT_SECRET,
                                        'defaultMode': 288}},
                                    {'name': 'service-root', 'configMap': {
                                        'name': service_volume['name']}}]}}
        if resuming:
            items = json.loads(self.kube([
                '-n', NS, 'get', 'pods', '-o', 'json']))['items']
            matches = [item['metadata']['name'] for item in items
                       if item['metadata']['name'].startswith(
                           'pki-host-seed-mqtt-recovery-')
                       and item['status'].get('phase') == 'Running']
            m.require(len(matches) == 1, 'recoverable MQTT seed Pod changed')
            self.kube(['-n', NS, 'delete', 'pod', matches[0], '--wait=true',
                       '--timeout=90s'])
        self.create(pod)
        self.kube(['-n', NS, 'wait', '--for=condition=Ready',
                   'pod/' + podname, '--timeout=240s'], timeout=250)
        csr = self.kube(['-n', NS, 'exec', podname, '--',
                         'cat', '/state/seed/csr.pem'])
        request = {'request_id': 'dev-mqtt-server-recovery-' + uuid.uuid4().hex,
                   'csr_pem': csr, 'ttl_days': 30, 'purpose': 'server'}
        issued = json.loads(self.kube([
            '-n', NS, 'exec', '-i', podname, '--', 'sh', '-c',
            'curl --fail-with-body --silent --show-error '
            '--cacert /run/service-root/root.pem '
            '--cert /run/mqtt-management/tls.crt '
            '--key /run/mqtt-management/tls.key '
            '-H Content-Type:application/json --data-binary @- '
            'https://certissuer.' + NS + '.svc:9443/v1/certificates/mqtt/issue'],
            json.dumps(request)))
        self.save('issued.json', issued)
        self.save('mqtt-root.pem', root['certificate_pem'])
        self.save('mqtt-intermediate.pem', issuer['certificate_pem'])
        self.save('leaf.pem', issued['certificate_pem'])
        m.require(issued['dns_names'] == [MQTT_HOST]
                  and issued['caller_identity'] == 'emqx-pki',
                  'recovery MQTT server issuance identity differs')
        m.command([self.openssl, 'verify', '-CAfile', self.output / 'mqtt-root.pem',
                   '-untrusted', self.output / 'mqtt-intermediate.pem',
                   '-purpose', 'sslserver', '-verify_hostname', MQTT_HOST,
                   self.output / 'leaf.pem'])
        self.kube(['-n', NS, 'exec', '-i', podname, '--', 'sh', '-c',
                   'set -eu; umask 077; cat > /state/seed/chain.pem'],
                  issued['certificate_chain_pem'])
        self.kube(['-n', NS, 'delete', 'pod', podname, '--wait=true',
                   '--timeout=90s'])
        current = self.obj('deployment', 'mqtt-pki')
        self.scoped_patch('deployment', current, [{
            'op': 'replace', 'path': '/spec/replicas', 'value': 1}])
        self.wait_available('mqtt-pki')
        state = self.inspect_host_state()
        rows = [row for row in self.server_rows()
                if row['fingerprint'] == state['fingerprint']]
        m.require(not state['pending'] and len(rows) == 1
                  and rows[0]['issuer_id'] == issuer['issuer_id']
                  and rows[0]['caller'] == 'emqx-pki'
                  and self.served_fingerprint() == state['fingerprint'],
                  'recovered MQTT successor is not served')
        digest = self.host_state_digest()
        self.kube(['-n', NS, 'exec', 'deployment/mqtt-pki', '-c', 'mqtt', '--',
                   'sh', '-c', 'set -eu; rm /var/lib/emqx-pki/seed/key.pem '
                   '/var/lib/emqx-pki/seed/csr.pem /var/lib/emqx-pki/seed/chain.pem'])
        current = self.obj('deployment', 'mqtt-pki')
        template = json.loads(json.dumps(current['spec']['template']))
        template.setdefault('metadata', {}).setdefault('annotations', {})[
            'rtk.cloud/mqtt-host-reset-seedless-restart'] = self.output.name
        self.scoped_patch('deployment', current, [{
            'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.wait_available('mqtt-pki')
        m.require(self.host_state_digest() == digest
                  and self.served_fingerprint() == state['fingerprint'],
                  'recovered MQTT successor changed across seed-free restart')
        clients = self.wait_actual_clients()
        self.forward('api', NS, 'video-cloud-api-pki', 8443)
        self.forward('mqtt', NS, 'mqtt-pki', 8883)
        self.device_baseline()
        self.check('mqtt_host_transition_recovered_from_dev_reset', {
            'issuer_id': issuer['issuer_id'], 'successor_fingerprint': state['fingerprint'],
            'predecessor_pvc_replaced': True, 'private_key_exported': False,
            'seed_removed': True, 'restart_without_seed': True,
            'actual_clients_reconnected': clients, 'device_mqtt_acl_qos1': 'passed'})

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
        deadline = time.monotonic() + 60
        while True:
            try:
                self.forward('mqtt-host', NS, 'mqtt-pki', 8883)
                result = json.loads(m.command([
                    self.probe, 'tls-peer', self.output / 'mqtt-root.pem',
                    MQTT_HOST, self.ports['mqtt-host'][0]]))
                m.require(result['peer_sha256'],
                          'MQTT listener certificate missing')
                return result['peer_sha256']
            except RuntimeError:
                m.require(time.monotonic() < deadline,
                          'MQTT listener certificate verification deadline')
                time.sleep(2)

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
        self.create({'apiVersion': 'v1', 'kind': 'ConfigMap',
                     'metadata': {'name': MQTT_CALLBACK_CA, 'namespace': NS},
                     'immutable': True, 'data': {
                         'ca.crt': (self.base / 'pki/servers/'
                                    'video-cloud-api-pki/ca.crt').read_text()}})
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
        self.finish_host_adoption(issuer, issued)

    def finish_host_adoption(self, issuer, issued):
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

    def finish_host_adoption_recovery(self):
        failed = m.read(Path(self.args.failed) / 'report.json')
        m.require(failed['status'] == 'failed'
                  and failed['phase'] in ('adopt-host',
                                          'finish-host-adoption'),
                  'failed MQTT host adoption evidence required')
        _, issuer, _ = self.ready_intermediate(status='active')
        source = Path(self.args.prepared)
        m.require(m.read(source / 'report.json')['status'] == 'passed',
                  'successful MQTT host preparation required')
        reference = m.read(source / 'pod-reference.json')
        m.require(self.obj('persistentvolumeclaim', MQTT_HOST_PVC)[
                      'metadata']['uid'] == reference['pvc_uid'],
                  'prepared MQTT host PVC changed')
        self.obj('secret', MQTT_MANAGEMENT_SECRET)
        runtime = self.obj('secret', MQTT_RUNTIME_SECRET)
        m.require(set(runtime['data']) == MQTT_RUNTIME_FIELDS,
                  'managed MQTT runtime Secret changed')
        owner = self.obj('deployment', 'mqtt-pki')
        template = json.loads(json.dumps(owner['spec']['template']))
        pod = template['spec']
        mqtt = next(item for item in pod['containers']
                    if item['name'] == 'mqtt')
        env = {item['name']: item.get('value')
               for item in mqtt.get('env', [])}
        m.require(env.get('EMQX_PKI_HOST_IDENTITY_STATE') == MQTT_HOST_STATE
                  and mqtt['image'] == self.args.image,
                  'managed MQTT host deployment changed')
        mqtt['env'] = h.with_env(mqtt['env'], {
            'EMQX_PKI_RUNTIME_DIR': '/run/emqx-pki/private'})
        volumes = {item['name']: item for item in pod.get('volumes', [])}
        if 'mqtt-host-runtime' not in volumes:
            pod['volumes'].append({'name': 'mqtt-host-runtime',
                                   'emptyDir': {}})
        else:
            m.require(volumes['mqtt-host-runtime'].get('emptyDir') == {},
                      'MQTT host runtime volume changed')
        mounts = {item['name']: item for item in mqtt.get('volumeMounts', [])}
        if 'mqtt-host-runtime' not in mounts:
            mqtt['volumeMounts'].append({
                'name': 'mqtt-host-runtime', 'mountPath': '/run/emqx-pki'})
        else:
            m.require(mounts['mqtt-host-runtime'].get('mountPath') ==
                      '/run/emqx-pki', 'MQTT host runtime mount changed')
        initializers = [item for item in pod.get('initContainers', [])
                        if item['name'] == 'prepare-mqtt-host-state']
        m.require(len(initializers) <= 1,
                  'duplicate MQTT host state initializer')
        if not initializers:
            pod.setdefault('initContainers', []).append(
                mqtt_host_state_initializer(self.args.image))
        elif initializers[0] != mqtt_host_state_initializer(self.args.image):
            pod['initContainers'] = [
                mqtt_host_state_initializer(self.args.image)
                if item['name'] == 'prepare-mqtt-host-state' else item
                for item in pod['initContainers']]
        template.setdefault('metadata', {}).setdefault('annotations', {})[
            'rtk.cloud/mqtt-host-runtime-recovery'] = self.output.name
        self.scoped_patch('deployment', owner, [{
            'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', NS, 'rollout', 'status',
                   'deployment/mqtt-pki', '--timeout=300s'], timeout=310)
        self.save('mqtt-root.pem', self.mqtt_root('active')['certificate_pem'])
        self.report['reconciled_from'] = str(Path(self.args.failed))
        self.finish_host_adoption(issuer, m.read(source / 'issued.json'))

    def recovery_preflight(self):
        m.require(self.kube(['config', 'current-context']).strip() ==
                  self.context, 'canonical dev context mismatch')
        m.require(self.obj('namespace', NS)['metadata']['name'] == NS,
                  'wrong namespace')
        self.forward('am', 'video-cloud-dev-account-manager',
                     'account-manager', 80)
        self.accounts = m.read(self.foundation / 'accounts.json')
        for role in ('requester', 'approver', 'custodian'):
            self.api('/issuers/search', {'limit': 1}, role=role)
        result = m.subprocess.run(
            ['go', 'build', '-o', str(self.probe), './pki-dev-probe'],
            cwd=m.WORKSPACE / 'scripts/go',
            env=dict(os.environ, GOWORK='off'), capture_output=True,
            timeout=180)
        m.require(result.returncode == 0,
                  'recovery wire probe build failed')
        for name in ('pki-controller', 'certissuer', 'video-cloud-api',
                     'video-cloud-logingester'):
            owner = self.obj('deployment', name)
            m.require(owner.get('status', {}).get('readyReplicas', 0) ==
                      owner['spec']['replicas'] > 0,
                      'dependency not ready: ' + name)
        mqtt = self.obj('deployment', 'mqtt-pki')
        replicas = mqtt['spec']['replicas']
        observed = mqtt['status'].get('observedGeneration')
        m.require((replicas == 1 and observed == mqtt['metadata']['generation'])
                  or (replicas == 0
                      and mqtt['status'].get('replicas', 0) == 0),
                  'failed MQTT deployment state is not observed')
        self.check('recovery_preflight', {
            'environment': 'dev', 'mqtt_fail_closed': True,
            'healthy_dependencies': [
                'pki-controller', 'certissuer', 'video-cloud-api',
                'video-cloud-logingester']})

    def repair_host_callback(self):
        adoption = m.read(Path(self.args.adoption) / 'report.json')
        m.require(adoption['status'] == 'passed'
                  and adoption['phase'] == 'finish-host-adoption',
                  'successful MQTT host adoption required')
        self.rollout_callback_api()
        root, _, _ = self.ready_intermediate(status='active')
        self.save('mqtt-root.pem', root['certificate_pem'])
        self.create({'apiVersion': 'v1', 'kind': 'ConfigMap',
                     'metadata': {'name': MQTT_CALLBACK_CA, 'namespace': NS},
                     'immutable': True, 'data': {
                         'ca.crt': (self.base / 'pki/servers/'
                                    'video-cloud-api-pki/ca.crt').read_text()}})
        secret = self.obj('secret', MQTT_RUNTIME_SECRET)
        m.require(set(secret['data']) == MQTT_RUNTIME_FIELDS,
                  'managed MQTT runtime Secret changed')
        updated = managed_runtime_data(secret['data'])
        self.scoped_patch('secret', secret, [
            {'op': 'test', 'path': '/data/authentication',
             'value': secret['data']['authentication']},
            {'op': 'replace', 'path': '/data/authentication',
             'value': updated['authentication']}])
        owner = self.obj('deployment', 'mqtt-pki')
        template = json.loads(json.dumps(owner['spec']['template']))
        pod = template['spec']
        mqtt = next(item for item in pod['containers']
                    if item['name'] == 'mqtt')
        env = {item['name']: item.get('value')
               for item in mqtt.get('env', [])}
        m.require(env.get('EMQX_PKI_HOST_IDENTITY_STATE') == MQTT_HOST_STATE,
                  'managed MQTT host deployment changed')
        volumes = {item['name']: item for item in pod.get('volumes', [])}
        mounts = {item['name']: item for item in mqtt.get('volumeMounts', [])}
        m.require('mqtt-callback-ca' not in volumes
                  and 'mqtt-callback-ca' not in mounts,
                  'MQTT callback CA repair already applied')
        pod['volumes'].append({'name': 'mqtt-callback-ca', 'configMap': {
            'name': MQTT_CALLBACK_CA}})
        mqtt['volumeMounts'].append({
            'name': 'mqtt-callback-ca',
            'mountPath': '/run/mqtt-callback-ca', 'readOnly': True})
        template.setdefault('metadata', {}).setdefault('annotations', {})[
            'rtk.cloud/mqtt-callback-repair'] = self.output.name
        self.scoped_patch('deployment', owner, [{
            'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.wait_available('mqtt-pki')
        self.verify_callback_clients()

    def verify_callback_clients(self):
        clients = self.wait_actual_clients()
        m.write(self.base / 'pki/servers/mqtt-pki/ca.crt',
                (self.output / 'mqtt-root.pem').read_text())
        self.forward('api', NS, 'video-cloud-api-pki', 8443)
        self.forward('mqtt', NS, 'mqtt-pki', 8883)
        self.device_baseline()
        self.check('mqtt_callback_and_actual_clients_restored', {
            'callback_client_key_source': MQTT_MANAGEMENT_SECRET,
            'callback_ca_source': MQTT_CALLBACK_CA,
            'runtime_secret_has_server_key': False,
            'actual_clients': clients,
            'device_mqtt_acl_qos1': 'passed'})

    def wait_actual_clients(self):
        deadline = time.monotonic() + 60
        clients = ''
        while time.monotonic() < deadline:
            try:
                clients = self.kube([
                    '-n', NS, 'exec', 'deployment/mqtt-pki', '-c', 'mqtt',
                    '--', 'sh', '-c',
                    '/usr/local/bin/emqx ctl clients list || true'])
            except RuntimeError as error:
                m.require(str(error) == 'command failed: kubectl',
                          'unexpected MQTT client inspection failure')
                time.sleep(2)
                continue
            if ('video-cloud-api-' in clients
                    and 'video-cloud-logingester-log-sub' in clients):
                break
            time.sleep(2)
        m.require('video-cloud-api-' in clients
                  and 'video-cloud-logingester-log-sub' in clients,
                  'actual MQTT clients did not authenticate')
        return list(MQTT_CONSUMERS)

    def wait_available(self, name):
        self.kube(['-n', NS, 'wait', '--for=condition=Available',
                   'deployment/' + name, '--timeout=300s'], timeout=310)
        deadline = time.monotonic() + 300
        while time.monotonic() < deadline:
            owner = self.obj('deployment', name)
            if (owner['status'].get('observedGeneration') ==
                    owner['metadata']['generation']
                    and owner['status'].get('updatedReplicas') ==
                    owner['spec']['replicas']
                    and owner['status'].get('readyReplicas') ==
                    owner['spec']['replicas']):
                return
            time.sleep(2)
        raise RuntimeError('available deployment update timed out: ' + name)

    def lifecycle_prerequisites(self, evidence, phase):
        prior = m.read(Path(evidence) / 'report.json')
        phases = (phase,) if isinstance(phase, str) else tuple(phase)
        m.require(prior['status'] == 'passed' and prior['phase'] in phases,
                  'successful MQTT ' + '/'.join(phases) +
                  ' evidence required')
        root, issuer, _ = self.ready_intermediate(status='active')
        self.save('mqtt-root.pem', root['certificate_pem'])
        owner = self.obj('deployment', 'mqtt-pki')
        mqtt = next(item for item in owner['spec']['template']['spec'][
                    'containers'] if item['name'] == 'mqtt')
        env = {item['name']: item for item in mqtt.get('env', [])}
        m.require(env.get('EMQX_PKI_HOST_IDENTITY_STATE', {}).get('value') ==
                  MQTT_HOST_STATE
                  and env.get('PKI_DATABASE_URL', {}).get(
                      'valueFrom', {}).get('secretKeyRef', {}).get('name') ==
                  'pki-controller-database'
                  and owner['spec']['strategy']['type'] == 'Recreate'
                  and owner['spec']['replicas'] == 1,
                  'managed MQTT host ownership changed')
        return root, issuer, owner

    def current_host(self, issuer, owner=None):
        return self.current_host_with_caller(issuer, 'emqx-pki', owner)

    def current_host_with_caller(self, issuer, caller, owner=None):
        owner = owner or self.obj('deployment', 'mqtt-pki')
        state = self.inspect_host_state()
        rows = self.server_rows()
        selected = [row for row in rows
                    if row['fingerprint'] == state['fingerprint']]
        m.require(not state['pending'] and state['subject'] == MQTT_HOST
                  and len(selected) == 1
                  and selected[0]['issuer_id'] == issuer['issuer_id']
                  and selected[0]['caller'] == caller
                  and selected[0]['status'] == 'succeeded'
                  and selected[0]['revoked_at'] is None
                  and self.served_fingerprint() == state['fingerprint'],
                  'current MQTT host is not admitted and served')
        return {
            'deployment_uid': owner['metadata']['uid'],
            'pvc_uid': self.obj('persistentvolumeclaim', MQTT_HOST_PVC)[
                'metadata']['uid'],
            'images': {item['name']: item['image'] for item in
                       owner['spec']['template']['spec']['containers']},
            'state': state, 'state_file_sha256': self.host_state_digest(),
            'row': selected[0], 'rows': rows}

    def verify_managed_emqx_bootstrap(self):
        """Record that the live broker has only the managed Service path."""
        service_source = Path(self.args.service_authority)
        service_report = m.read(service_source / 'report.json')
        m.require(service_report.get('status') == 'passed'
                  and service_report.get('phase') in (
                      'activate-emqx-service-v9', 'recover-emqx-service-v9'),
                  'passed EMQX Service authority evidence required')
        service = m.read(service_source / 'intermediate-active.json')
        m.require(service.get('status') == 'active'
                  and service.get('trust_domain') == 'service'
                  and EMQX_SERVICE_SUBJECT in service.get('service_client_ids', [])
                  and self.api('/issuers/' + service['issuer_id']) == service,
                  'active EMQX Service issuer changed')
        root, issuer, _ = self.ready_intermediate(status='active')
        owner = self.obj('deployment', 'mqtt-pki')
        mqtt = next(item for item in owner['spec']['template']['spec']['containers']
                    if item['name'] == 'mqtt')
        env = {item['name']: item.get('value') for item in mqtt.get('env', [])}
        forbidden = {name for name in env if name.startswith('EMQX_PKI_') and
                     ('BOOTSTRAP' in name or 'RENEWAL_CLIENT_' in name)}
        m.require(not forbidden
                  and env.get('EMQX_PKI_SERVICE_CLIENT_IDENTITY_STATE') ==
                  EMQX_SERVICE_STATE
                  and env.get('EMQX_PKI_HOST_ROOT_SHA256') ==
                  root['certificate_fingerprint_sha256'],
                  'broker retains a bootstrap or static renewal credential')
        m.require('emqx-bootstrap-ca.crt' not in
                  self.obj('secret', 'certissuer-runtime').get('data', {}),
                  'CertIssuer retains the EMQX bootstrap CA')
        certissuer = self.obj('deployment', 'certissuer')
        certenv = {item['name']: item.get('value') for item in
                   certissuer['spec']['template']['spec']['containers'][0].get('env', [])}
        m.require(certenv.get('CERT_ISSUER_MQTT_SERVER_CLIENT_CN_PATTERN') ==
                  '^service:emqx-pki$'
                  and certenv.get('CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN') ==
                  '^service-provisioner$'
                  and 'CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER' not in certenv,
                  'CertIssuer retains the EMQX bootstrap policy')
        service_row = self.sql(
            "SELECT row_to_json(t) FROM (SELECT issuer_id,subject,caller,status,"
            "fingerprint,revoked_at FROM pki_service_client_issuances WHERE "
            "environment='dev' AND subject='service:emqx-pki' ORDER BY issued_at "
            "DESC LIMIT 1) t;")
        service_row = json.loads(service_row)
        m.require(service_row['issuer_id'] == service['issuer_id']
                  and service_row['subject'] == EMQX_SERVICE_SUBJECT
                  and service_row['caller'] == EMQX_SERVICE_SUBJECT
                  and service_row['status'] == 'succeeded'
                  and service_row['revoked_at'] is None,
                  'current EMQX Service client issuance differs')
        self.save('emqx-service-client-issuance.json', service_row)
        self.save('mqtt-root.pem', root['certificate_pem'])
        healthy = self.current_host_with_caller(issuer, EMQX_SERVICE_SUBJECT,
                                                owner)
        clients = self.wait_actual_clients()
        self.check('managed_emqx_bootstrap_verified', {
            'service_issuer_id': service['issuer_id'],
            'service_client_fingerprint': service_row['fingerprint'],
            'mqtt_issuer_id': issuer['issuer_id'],
            'mqtt_server_fingerprint': healthy['state']['fingerprint'],
            'bootstrap_ca_removed': True,
            'bootstrap_policy_closed': True,
            'static_renewal_credential_present': False,
            'actual_clients_authenticated': clients,
            'staging_touched': False})

    def renew_host(self):
        traffic = m.read(Path(self.args.traffic) / 'report.json')
        m.require(traffic['status'] == 'passed'
                  and traffic['phase'] in ('repair-host-callback',
                                            'finish-host-callback')
                  and traffic['checks'][
                      'mqtt_callback_and_actual_clients_restored'][
                          'status'] == 'passed',
                  'successful actual MQTT client evidence required')
        root, issuer, owner = self.lifecycle_prerequisites(
            self.args.traffic, traffic['phase'])
        before = self.current_host(issuer, owner)
        self.save('baseline.json', before)
        pod = self.mqtt_pod()
        remote = self.install_host_probe(pod)
        command = self.kube(['-n', NS, 'exec', remote, '-c', 'mqtt', '--',
                             'cat', '/proc/1/comm']).strip()
        m.require(command == 'emqxpkihost',
                  'PID 1 is not the MQTT host identity owner')
        device = m.read(self.foundation / 'device-2/enroll-request.json')['devid']
        auth = self.auth(self.foundation / 'device-2/v4', device)
        held, opened = self.mqtt(auth, device, 'hold')
        try:
            held.stable(3)
            signaled = dt.datetime.now(dt.timezone.utc)
            intent = {'pod_uid': pod['metadata']['uid'],
                      'previous_fingerprint': before['state']['fingerprint'],
                      'at': m.stamp(signaled)}
            self.save('renewal-intent.json', intent)
            self.kube(['-n', NS, 'exec', remote, '-c', 'mqtt', '--',
                       'kill', '-HUP', '1'])
            closed = held.event(80)
            m.require(closed.get('event') == 'closed',
                      'pre-renewal MQTT session remained open')
            elapsed = (m.parse_time(closed['at']) - signaled).total_seconds()
            age = (m.parse_time(closed['at']) -
                   m.parse_time(opened['at'])).total_seconds()
            m.require(0 <= elapsed < 80 and age < 90,
                      'MQTT interruption cannot be attributed to renewal')
            m.require(held.child.wait(timeout=5) == 0,
                      'held MQTT session probe failed')
            self.save('session-closed.json', {
                'event': closed, 'elapsed_seconds': elapsed,
                'session_age_seconds': age})
        finally:
            held.close()
        deadline = time.monotonic() + 180
        while True:
            try:
                state = self.inspect_host_state(pod)
            except RuntimeError:
                state = before['state']
            if (not state['pending']
                    and state['fingerprint'] != before['state']['fingerprint']):
                break
            m.require(time.monotonic() < deadline,
                      'MQTT host renewal deadline; do not signal again')
            time.sleep(2)
        rows = self.server_rows()
        old_ids = {row['request_id'] for row in before['rows']}
        added = [row for row in rows if row['request_id'] not in old_ids]
        m.require(len(rows) == len(before['rows']) + 1 and len(added) == 1
                  and added[0]['fingerprint'] == state['fingerprint']
                  and added[0]['issuer_id'] == issuer['issuer_id']
                  and added[0]['caller'] == 'emqx-pki'
                  and added[0]['status'] == 'succeeded'
                  and added[0]['revoked_at'] is None
                  and state['public_key_sha256'] !=
                  before['state']['public_key_sha256']
                  and state['state_sha256'] !=
                  before['state']['state_sha256'],
                  'MQTT host replacement registry evidence differs')
        m.require(self.served_fingerprint() == state['fingerprint'],
                  'MQTT listener did not install the successor')
        self.wait_actual_clients()
        renewed = self.current_host(issuer)
        self.save('renewed.json', renewed)
        current = self.obj('deployment', 'mqtt-pki')
        template = json.loads(json.dumps(current['spec']['template']))
        template.setdefault('metadata', {}).setdefault('annotations', {})[
            'rtk.cloud/mqtt-host-renewal-restart'] = self.output.name
        self.scoped_patch('deployment', current, [{
            'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.wait_available('mqtt-pki')
        restarted = self.current_host(issuer)
        m.require(restarted['state'] == renewed['state']
                  and restarted['state_file_sha256'] ==
                  renewed['state_file_sha256']
                  and restarted['rows'] == renewed['rows'],
                  'MQTT successor changed across owner restart')
        self.wait_actual_clients()
        self.forward('api', NS, 'video-cloud-api-pki', 8443)
        self.forward('mqtt', NS, 'mqtt-pki', 8883)
        self.device_baseline()
        self.check('mqtt_host_renewed_and_reconnected', {
            'issuer_id': issuer['issuer_id'],
            'predecessor_fingerprint': before['state']['fingerprint'],
            'successor_fingerprint': state['fingerprint'],
            'predecessor_session_closed_seconds': elapsed,
            'predecessor_session_age_seconds': age,
            'new_owner_key': True, 'private_key_exported': False,
            'restart_from_retained_successor': True,
            'actual_clients_reconnected': list(MQTT_CONSUMERS),
            'device_mqtt_acl_qos1': 'passed'})

    def finish_host_renewal(self):
        failed = Path(self.args.failed)
        report = m.read(failed / 'report.json')
        m.require(report['status'] == 'failed'
                  and report['phase'] == 'renew-host'
                  and report['failure'] == 'command failed: pki-dev-probe'
                  and (failed / 'renewal-intent.json').is_file()
                  and not (failed / 'renewed.json').exists(),
                  'matching failed MQTT renewal evidence required')
        traffic = m.read(Path(self.args.traffic) / 'report.json')
        m.require(traffic['status'] == 'passed'
                  and traffic['phase'] in ('repair-host-callback',
                                            'finish-host-callback'),
                  'successful actual MQTT client evidence required')
        _, issuer, owner = self.lifecycle_prerequisites(
            self.args.traffic, traffic['phase'])
        before = m.read(failed / 'baseline.json')
        intent = m.read(failed / 'renewal-intent.json')
        m.require(intent['previous_fingerprint'] ==
                  before['state']['fingerprint']
                  and owner['metadata']['uid'] == before['deployment_uid']
                  and self.obj('persistentvolumeclaim', MQTT_HOST_PVC)[
                      'metadata']['uid'] == before['pvc_uid'],
                  'failed MQTT renewal owner changed')
        current = self.current_host(issuer, owner)
        old_ids = {row['request_id'] for row in before['rows']}
        added = [row for row in current['rows']
                 if row['request_id'] not in old_ids]
        m.require(len(current['rows']) == len(before['rows']) + 1
                  and len(added) == 1
                  and added[0]['fingerprint'] ==
                  current['state']['fingerprint']
                  and current['state']['fingerprint'] !=
                  before['state']['fingerprint']
                  and current['state']['public_key_sha256'] !=
                  before['state']['public_key_sha256'],
                  'failed MQTT renewal did not install one successor')
        self.save('baseline.json', before)
        self.save('renewal-intent.json', intent)
        self.save('renewed.json', current)
        device = m.read(self.foundation / 'device-2/enroll-request.json')['devid']
        auth = self.auth(self.foundation / 'device-2/v4', device)
        held, opened = self.mqtt(auth, device, 'hold')
        try:
            held.stable(3)
            started = dt.datetime.now(dt.timezone.utc)
            live = self.obj('deployment', 'mqtt-pki')
            template = json.loads(json.dumps(live['spec']['template']))
            template.setdefault('metadata', {}).setdefault('annotations', {})[
                'rtk.cloud/mqtt-host-renewal-recovery'] = self.output.name
            self.scoped_patch('deployment', live, [{
                'op': 'replace', 'path': '/spec/template', 'value': template}])
            closed = held.event(80)
            m.require(closed.get('event') == 'closed',
                      'MQTT session remained open across successor restart')
            elapsed = (m.parse_time(closed['at']) - started).total_seconds()
            age = (m.parse_time(closed['at']) -
                   m.parse_time(opened['at'])).total_seconds()
            m.require(0 <= elapsed < 80 and age < 90,
                      'MQTT recovery interruption was not bounded')
            m.require(held.child.wait(timeout=5) == 0,
                      'held MQTT recovery probe failed')
            self.save('session-closed.json', {
                'event': closed, 'elapsed_seconds': elapsed,
                'session_age_seconds': age})
        finally:
            held.close()
        self.wait_available('mqtt-pki')
        restarted = self.current_host(issuer)
        m.require(restarted['state'] == current['state']
                  and restarted['state_file_sha256'] ==
                  current['state_file_sha256']
                  and restarted['rows'] == current['rows'],
                  'reconciled MQTT successor changed across restart')
        self.wait_actual_clients()
        self.forward('api', NS, 'video-cloud-api-pki', 8443)
        self.forward('mqtt', NS, 'mqtt-pki', 8883)
        self.device_baseline()
        self.report['reconciled_from'] = str(failed)
        self.check('mqtt_host_renewed_and_reconnected', {
            'issuer_id': issuer['issuer_id'],
            'predecessor_fingerprint': before['state']['fingerprint'],
            'successor_fingerprint': current['state']['fingerprint'],
            'renewal_interruption_bound_seconds': 80,
            'successor_restart_session_closed_seconds': elapsed,
            'new_owner_key': True, 'private_key_exported': False,
            'restart_from_retained_successor': True,
            'duplicate_renewal_avoided': True,
            'actual_clients_reconnected': list(MQTT_CONSUMERS),
            'device_mqtt_acl_qos1': 'passed'})

    def revoke_host(self):
        _, issuer, _ = self.lifecycle_prerequisites(
            self.args.renewal, ('renew-host', 'finish-host-renewal'))
        source = Path(self.args.renewal)
        before = m.read(source / 'baseline.json')
        renewed = m.read(source / 'renewed.json')
        current = self.current_host(issuer)
        m.require(current['state'] == renewed['state'],
                  'installed MQTT successor changed')
        rows = self.server_rows()
        old = [row for row in rows if row['fingerprint'] ==
               before['state']['fingerprint']]
        successor = [row for row in rows if row['fingerprint'] ==
                     renewed['state']['fingerprint']]
        m.require(len(old) == len(successor) == 1
                  and old[0]['revoked_at'] is None
                  and successor[0]['revoked_at'] is None,
                  'MQTT retirement targets changed')
        previous = self.api('/issuers/' + issuer['issuer_id'] + '/crl')
        self.save('target.json', old[0])
        self.save('previous-crl.json', previous)
        body = {
            'certificate_sha256': old[0]['fingerprint'],
            'reason': 'Replaced by managed MQTT host renewal '
                      + successor[0]['request_id']}
        self.save('revocation-request.json', body)
        record = self.api('/issuers/' + issuer['issuer_id'] +
                          '/revoke-server', body)
        m.require(self.api('/issuers/' + issuer['issuer_id'] +
                           '/revoke-server', body) == record,
                  'MQTT server revocation replay changed record')
        self.save('revocation.json', record)
        old = [row for row in self.server_rows()
               if row['fingerprint'] == before['state']['fingerprint']]
        m.require(len(old) == 1 and old[0]['revoked_at'] is not None
                  and self.api('/issuers/' + issuer['issuer_id'] + '/crl') ==
                  previous,
                  'MQTT predecessor registry denial differs')
        self.current_host(issuer)
        self.device_baseline()
        self.check('mqtt_predecessor_denied_in_registry', {
            'predecessor_fingerprint': old[0]['fingerprint'],
            'successor_fingerprint': successor[0]['fingerprint'],
            'revocation_replay_identical': True,
            'provider_publication_pending': True,
            'successor_and_device_paths_healthy': True})

    def publish_host_revocation(self):
        _, issuer, _ = self.lifecycle_prerequisites(
            self.args.revocation, 'revoke-host')
        source = Path(self.args.revocation)
        target = m.read(source / 'target.json')
        rows = [row for row in self.server_rows()
                if row['fingerprint'] == target['fingerprint']]
        m.require(len(rows) == 1 and rows[0]['revoked_at'] is not None,
                  'revoked MQTT predecessor changed')
        before = self.api('/issuers/' + issuer['issuer_id'] + '/crl')
        body = {'certificate_sha256': target['fingerprint']}
        published = self.api('/issuers/' + issuer['issuer_id'] +
                             '/publish-server-revocation', body)
        m.require(published['crl_sha256'] != before['crl_sha256']
                  and self.api('/issuers/' + issuer['issuer_id'] +
                               '/publish-server-revocation', body) == published,
                  'MQTT predecessor CRL publication differs')
        self.save('published-crl.json', published)
        receipts = self.wait_receipts(
            issuer['issuer_id'], published['crl_sha256'], MQTT_CONSUMERS,
            kind='crl')
        entries = json.loads(m.command([self.probe, 'crl'],
                                       published['crl_pem'])) or []
        self.save('target.pem', target['certificate_pem'])
        serial = m.command([
            self.openssl, 'x509', '-in', self.output / 'target.pem',
            '-noout', '-serial']).strip().split('=')[1]
        m.require(any(int(item['serial_hex'], 16) == int(serial, 16)
                      for item in entries),
                  'published MQTT CRL omits the predecessor')
        finalized = self.api('/issuers/' + issuer['issuer_id'] +
                             '/finalize-server-revocation', body)
        m.require(finalized['crl_sha256'] == published['crl_sha256']
                  and self.api('/issuers/' + issuer['issuer_id'] +
                               '/finalize-server-revocation', body) == finalized,
                  'MQTT predecessor finalization differs')
        self.save('finalized.json', finalized)
        self.current_host(issuer)
        self.wait_actual_clients()
        self.device_baseline()
        self.check('mqtt_predecessor_revocation_published', {
            'predecessor_fingerprint': target['fingerprint'],
            'crl_sha256': published['crl_sha256'],
            'consumers': receipts, 'crl_contains_predecessor': True,
            'finalization_replay_identical': True,
            'successor_and_device_paths_healthy': True})

    def trust_receipts(self, root, issuer):
        ids = "'" + "','".join([root['issuer_id'], issuer['issuer_id']]) + "'"
        raw = self.sql(
            "SELECT row_to_json(t) FROM ("
            "SELECT 'bundle' AS kind,issuer_id,consumer_id,"
            "bundle_version AS value,acknowledged_at "
            "FROM pki_bundle_acknowledgments WHERE issuer_id IN (" + ids + ") "
            "UNION ALL SELECT 'crl',issuer_id,consumer_id,digest,"
            "acknowledged_at FROM pki_crl_acknowledgments "
            "WHERE issuer_id IN (" + ids + ") "
            "ORDER BY kind,issuer_id,consumer_id,value) t;")
        return [json.loads(line) for line in raw.splitlines() if line]

    def verify_host_lifecycle(self):
        root, issuer, owner = self.lifecycle_prerequisites(
            self.args.publication, 'publish-host-revocation')
        source = Path(self.args.publication)
        published = m.read(source / 'published-crl.json')
        m.require(self.api('/issuers/' + issuer['issuer_id'] + '/crl') ==
                  published,
                  'published MQTT retirement CRL changed')
        self.wait_receipts(issuer['issuer_id'], published['crl_sha256'],
                           MQTT_CONSUMERS, kind='crl')
        healthy = self.current_host(issuer, owner)
        receipts = self.trust_receipts(root, issuer)
        template = json.loads(json.dumps(owner['spec']['template']))
        self.save('healthy-template.json', template)
        mqtt = next(item for item in template['spec']['containers']
                    if item['name'] == 'mqtt')
        database = [item for item in mqtt['env']
                    if item['name'] == 'PKI_DATABASE_URL']
        m.require(len(database) == 1 and 'valueFrom' in database[0],
                  'MQTT registry dependency changed')
        failed_template = json.loads(json.dumps(template))
        failed_mqtt = next(item for item in failed_template['spec']['containers']
                           if item['name'] == 'mqtt')
        failed_mqtt['env'] = h.with_env(
            failed_mqtt['env'], {'PKI_DATABASE_URL':
                                 'postgres://127.0.0.1:1/unavailable?connect_timeout=1'})
        failed_template.setdefault('metadata', {}).setdefault(
            'annotations', {})['rtk.cloud/mqtt-host-trust-failure'] = (
                self.output.name)
        old_pod = self.mqtt_pod()['metadata']
        restored = False
        try:
            self.scoped_patch('deployment', owner, [{
                'op': 'replace', 'path': '/spec/template',
                'value': failed_template}])
            self.kube(['-n', NS, 'wait', '--for=delete',
                       'pod/' + old_pod['name'], '--timeout=180s'], timeout=190)
            deadline = time.monotonic() + 90
            failure = None
            while time.monotonic() < deadline:
                try:
                    pod = self.mqtt_pod(ready=False)
                except RuntimeError:
                    time.sleep(2)
                    continue
                statuses = {item['name']: item for item in
                            pod['status'].get('containerStatuses', [])}
                mqtt_status = statuses.get('mqtt', {})
                endpoints = self.obj('endpoints', 'mqtt-pki')
                ready_endpoints = sum(
                    len(item.get('addresses', []))
                    for item in endpoints.get('subsets', []))
                if (pod['metadata']['uid'] != old_pod['uid']
                        and not mqtt_status.get('ready')
                        and ready_endpoints == 0):
                    failure = {
                        'old_pod_uid': old_pod['uid'],
                        'failed_pod_uid': pod['metadata']['uid'],
                        'mqtt_ready': False,
                        'service_has_ready_endpoint': False,
                        'restart_count': mqtt_status.get('restartCount', 0)}
                    break
                time.sleep(2)
            m.require(failure is not None,
                      'MQTT host did not fail closed without registry trust')
            self.save('trust-failure.json', failure)
            m.require(self.server_rows() == healthy['rows']
                      and self.trust_receipts(root, issuer) == receipts,
                      'trust failure created issuance or acknowledgment state')
        finally:
            current = self.obj('deployment', 'mqtt-pki')
            self.scoped_patch('deployment', current, [{
                'op': 'replace', 'path': '/spec/template',
                'value': template}])
            self.wait_available('mqtt-pki')
            restored = True
        m.require(restored, 'MQTT host trust restoration failed')
        after = self.current_host(issuer)
        m.require(after['state'] == healthy['state']
                  and after['state_file_sha256'] ==
                  healthy['state_file_sha256']
                  and after['rows'] == healthy['rows']
                  and self.trust_receipts(root, issuer) == receipts,
                  'MQTT trust recovery changed durable state')
        self.wait_actual_clients()
        self.forward('api', NS, 'video-cloud-api-pki', 8443)
        self.forward('mqtt', NS, 'mqtt-pki', 8883)
        self.device_baseline()
        wrong_name = m.subprocess.run([
            str(self.probe), 'tls-peer', str(self.output / 'mqtt-root.pem'),
            'wrong.' + MQTT_HOST, self.ports['mqtt'][0]],
            capture_output=True, text=True, timeout=15)
        m.require(wrong_name.returncode != 0,
                  'wrong MQTT DNS name was accepted')
        service_root = self.obj('configmap', 'pki-service-host-root')[
            'data']['root.pem']
        self.save('wrong-root.pem', service_root)
        wrong_root = m.subprocess.run([
            str(self.probe), 'tls-peer', str(self.output / 'wrong-root.pem'),
            MQTT_HOST, self.ports['mqtt'][0]],
            capture_output=True, text=True, timeout=15)
        m.require(wrong_root.returncode != 0,
                  'wrong MQTT Root was accepted')
        self.check('mqtt_host_trust_failure_recovered', {
            'registry_unavailable_broker_ready': False,
            'registry_unavailable_service_endpoint': False,
            'issuance_and_acknowledgment_rows_unchanged': True,
            'durable_successor_unchanged': True,
            'actual_clients_reconnected': list(MQTT_CONSUMERS),
            'wrong_name_denied_before_credentials': True,
            'wrong_root_denied_before_credentials': True,
            'device_mqtt_acl_qos1': 'passed'})

    def finish_host_callback_recovery(self):
        failed = m.read(Path(self.args.failed) / 'report.json')
        m.require(failed['status'] == 'failed'
                  and failed['phase'] in ('repair-host-callback',
                                          'finish-host-callback'),
                  'failed MQTT callback repair evidence required')
        self.rollout_callback_api()
        root, _, _ = self.ready_intermediate(status='active')
        self.save('mqtt-root.pem', root['certificate_pem'])
        expected_ca = (self.base / 'pki/servers/'
                       'video-cloud-api-pki/ca.crt').read_text()
        callback_ca = self.obj('configmap', MQTT_CALLBACK_CA)
        m.require(callback_ca.get('immutable') is True
                  and callback_ca.get('data', {}).get('ca.crt') == expected_ca,
                  'MQTT callback server CA changed')
        owner = self.obj('deployment', 'mqtt-pki')
        template = json.loads(json.dumps(owner['spec']['template']))
        volumes = {item['name']: item
                   for item in template['spec'].get('volumes', [])}
        callback = volumes.get('mqtt-callback-ca', {}).get('configMap', {})
        m.require(callback.get('name') in (
                      'pki-mqtt-callback-ca', MQTT_CALLBACK_CA),
                  'failed callback CA source changed')
        callback_changed = callback.get('name') != MQTT_CALLBACK_CA
        callback['name'] = MQTT_CALLBACK_CA
        if callback_changed:
            template.setdefault('metadata', {}).setdefault('annotations', {})[
                'rtk.cloud/mqtt-callback-ca-recovery'] = self.output.name
            self.scoped_patch('deployment', owner, [{
                'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.wait_available('mqtt-pki')
        self.report['reconciled_from'] = str(Path(self.args.failed))
        self.verify_callback_clients()

    def rollout_callback_api(self):
        m.require(IMAGE_PATTERN.fullmatch(self.args.image or ''),
                  'verified dev callback API image digest required')
        owner = self.obj('deployment', 'video-cloud-api-pki')
        template = json.loads(json.dumps(owner['spec']['template']))
        containers = template['spec']['containers']
        m.require(len(containers) == 1 and containers[0]['name'] == 'app',
                  'MQTT callback API topology changed')
        if containers[0]['image'] == self.args.image:
            self.wait_available('video-cloud-api-pki')
            return
        containers[0]['image'] = self.args.image
        template.setdefault('metadata', {}).setdefault('annotations', {})[
            'rtk.cloud/mqtt-tls-server-clients'] = self.output.name
        self.scoped_patch('deployment', owner, [{
            'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.wait_available('video-cloud-api-pki')


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', choices=[
        'install-root-consumers', 'finish-root-consumers', 'activate-root',
        'prepare-intermediate', 'install-intermediate',
        'activate-intermediate', 'finish-intermediate-activation',
        'configure-certissuer', 'prepare-host', 'transition-host-root',
        'recover-host-transition', 'bootstrap-emqx-service-client',
        'verify-managed-emqx-bootstrap',
        'adopt-host',
        'finish-host-adoption', 'repair-host-callback',
        'finish-host-callback', 'renew-host', 'finish-host-renewal', 'revoke-host',
        'publish-host-revocation', 'verify-host-lifecycle'],
        required=True)
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--authority', required=True)
    parser.add_argument('--image')
    parser.add_argument('--failed')
    parser.add_argument('--intermediate')
    parser.add_argument('--prepared')
    parser.add_argument('--adoption')
    parser.add_argument('--traffic')
    parser.add_argument('--renewal')
    parser.add_argument('--revocation')
    parser.add_argument('--publication')
    parser.add_argument('--service-authority')
    parser.add_argument('--certissuer-image')
    args = parser.parse_args()
    m.require(args.phase != 'finish-root-consumers' or args.failed,
              'failed Root consumer evidence required')
    m.require(args.phase != 'finish-intermediate-activation' or args.failed,
              'failed intermediate activation evidence required')
    m.require(args.phase != 'recover-host-transition' or args.failed,
              'failed managed MQTT host transition evidence required')
    m.require(args.phase not in ('install-intermediate',
                                 'activate-intermediate',
                                 'finish-intermediate-activation',
                                 'configure-certissuer', 'prepare-host',
                                 'transition-host-root',
                                 'recover-host-transition', 'bootstrap-emqx-service-client',
                                 'verify-managed-emqx-bootstrap',
                                 'adopt-host', 'finish-host-adoption',
                                 'repair-host-callback',
                                 'finish-host-callback', 'renew-host',
                                 'finish-host-renewal', 'revoke-host',
                                 'publish-host-revocation',
                                 'verify-host-lifecycle')
              or args.intermediate,
              'prepared MQTT intermediate evidence required')
    m.require(args.phase != 'bootstrap-emqx-service-client'
              or (args.service_authority and args.image and args.certissuer_image),
              'active Service v9 evidence, managed EMQX image and CertIssuer image required')
    m.require(args.phase != 'verify-managed-emqx-bootstrap'
              or args.service_authority,
              'active EMQX Service authority evidence required')
    m.require(args.phase != 'adopt-host' or args.prepared,
              'prepared MQTT host evidence required')
    m.require(args.phase != 'finish-host-adoption'
              or (args.prepared and args.failed and args.image),
              'failed adoption, prepared host and image required')
    m.require(args.phase != 'repair-host-callback'
              or (args.adoption and args.image),
              'successful MQTT host adoption and callback image required')
    m.require(args.phase != 'finish-host-callback'
              or (args.failed and args.image),
              'failed MQTT callback repair and callback image required')
    m.require(args.phase != 'renew-host' or args.traffic,
              'successful actual MQTT client evidence required')
    m.require(args.phase != 'finish-host-renewal'
              or (args.traffic and args.failed),
              'failed renewal and actual MQTT client evidence required')
    m.require(args.phase != 'revoke-host' or args.renewal,
              'successful MQTT host renewal evidence required')
    m.require(args.phase != 'publish-host-revocation' or args.revocation,
              'successful MQTT host revocation evidence required')
    m.require(args.phase != 'verify-host-lifecycle' or args.publication,
              'successful MQTT host CRL publication evidence required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/mqtt-host-rollout.lock'
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = MQTTHostRun(args)
    try:
        if args.phase in ('finish-host-adoption', 'repair-host-callback',
                          'finish-host-callback', 'recover-host-transition',
                          'bootstrap-emqx-service-client',
                          'verify-managed-emqx-bootstrap'):
            runner.recovery_preflight()
        else:
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
         'transition-host-root': runner.transition_host_root,
         'recover-host-transition': runner.recover_host_transition,
         'bootstrap-emqx-service-client': runner.bootstrap_emqx_service_client,
         'verify-managed-emqx-bootstrap': runner.verify_managed_emqx_bootstrap,
         'adopt-host': runner.adopt_host,
         'finish-host-adoption':
             runner.finish_host_adoption_recovery,
         'repair-host-callback': runner.repair_host_callback,
         'finish-host-callback':
             runner.finish_host_callback_recovery,
         'renew-host': runner.renew_host,
         'finish-host-renewal': runner.finish_host_renewal,
         'revoke-host': runner.revoke_host,
         'publish-host-revocation': runner.publish_host_revocation,
         'verify-host-lifecycle': runner.verify_host_lifecycle}[args.phase]()
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
