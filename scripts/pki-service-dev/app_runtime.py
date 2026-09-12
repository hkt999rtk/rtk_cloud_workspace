#!/usr/bin/env python3
"""Install the separate R1 App API verifier and controller policy in Dev."""
import argparse
import base64
import copy
import importlib.util
import json
import hashlib
import os
from pathlib import Path
import re
import secrets
import signal
import sys
import time
from urllib.parse import quote, urlsplit, urlunsplit
import uuid


spec = importlib.util.spec_from_file_location(
    'service_hierarchy', Path(__file__).with_name('run.py'))
h = importlib.util.module_from_spec(spec)
spec.loader.exec_module(h)
m, NS = h.m, h.NS
CONSUMERS = 'video-cloud-api-app,pkibroker,pkiturn'
IMAGE = re.compile(
    r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}')


def env_replace(items, updates, remove):
    names = [item['name'] for item in items]
    m.require(len(names) == len(set(names)), 'duplicate environment settings')
    return [item for item in items
            if item['name'] not in updates and not any(
                item['name'].startswith(prefix) for prefix in remove)] + [
                    {'name': key, 'value': value}
                    for key, value in updates.items()]


def append_ca(bundle, certificate):
    certificate = certificate.strip() + '\n'
    return bundle if certificate.strip() in bundle else bundle.rstrip() + '\n' + certificate


def initial_state(root):
    policy = {'environment': 'dev', 'trust_domain': 'app', 'version': 0,
              'distrusted_roots': [], 'policy_sha256': ''}
    canonical = json.dumps(policy, separators=(',', ':'))
    policy['policy_sha256'] = hashlib.sha256(canonical.encode()).hexdigest()
    return json.dumps({'policy': policy, 'roots_pem': root['certificate_pem']},
                      separators=(',', ':'))


class AppRuntime(h.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = (
            'Dev-only R1 App API/controller installation; Device verifier unchanged')
        self.report['app_runtime_runner_sha256'] = m.digest(
            Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def app_root(self):
        saved = m.read(Path(self.args.authority) / 'root-ready.json')
        current = self.api('/issuers/' + saved['issuer_id'])
        m.require(current == saved and current['environment'] == 'dev'
                  and current['trust_domain'] == 'app'
                  and current['kind'] == 'root'
                  and current['status'] == 'ready',
                  'ready App Root changed; reconcile')
        return current

    def private_material(self, category, name):
        path = self.base / 'pki' / category / name
        files = {}
        for filename in ('ca.crt', 'tls.crt', 'tls.key'):
            item = path / filename
            m.require(item.is_file() and item.stat().st_mode & 0o777 == 0o600,
                      'private Dev transport material missing or unsafe')
            files[filename] = item.read_text()
        return files

    def secret(self, name, values):
        self.ensure({'apiVersion': 'v1', 'kind': 'Secret',
                     'metadata': {'name': name, 'namespace': NS},
                     'type': 'Opaque',
                     'data': {key: base64.b64encode(value.encode()).decode()
                              for key, value in values.items()}})

    def receipts(self, root, kind):
        issuer = root['issuer_id']
        m.require(str(uuid.UUID(issuer)) == issuer, 'invalid App Root identifier')
        if kind == 'bundle':
            value = root['trust_bundle_version']
            table, field = 'pki_bundle_acknowledgments', 'bundle_version'
            scope = "issuer_id='" + issuer + "'"
        else:
            value = initial_state(root)
            value = json.loads(value)['policy']['policy_sha256']
            table, field = 'pki_root_distrust_acknowledgments', 'policy_sha256'
            scope = "environment='dev' AND domain='app'"
        m.require(re.fullmatch(r'[0-9a-f]{64}', value),
                  'invalid App receipt version')
        query = ("SELECT consumer_id FROM " + table + " WHERE " + scope
                 + " AND " + field + "='" + value
                 + "' ORDER BY consumer_id;")
        return self.kube([
            '-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0',
            '--', 'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres',
            '-d', 'video_cloud', '-At'], query).split()

    def wait_receipts(self, root, expected):
        deadline = time.monotonic() + 90
        while True:
            bundle = self.receipts(root, 'bundle')
            policy = self.receipts(root, 'root_policy')
            if all(name in bundle and name in policy for name in expected):
                return bundle, policy
            m.require(time.monotonic() < deadline,
                      'App consumer receipt deadline')
            time.sleep(3)

    def sql(self, query):
        return self.kube([
            '-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0',
            '--', 'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres',
            '-d', 'video_cloud', '-At'], query).strip()

    def turn_database(self):
        role = 'rtk_pki_turn_dev'
        raw = self.kube(['-n', NS, 'get', 'secret', 'pkiturn-database',
                         '--ignore-not-found', '-o', 'json'])
        exists = self.sql("SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='"
                          + role + "');") == 't'
        if raw.strip():
            secret = json.loads(raw)
            dsn = base64.b64decode(secret['data']['dsn']).decode()
            parsed = urlsplit(dsn)
            password = parsed.password or ''
            m.require(parsed.username == role and password,
                      'existing pkiturn verifier Secret differs')
        else:
            m.require(not exists,
                      'pkiturn database role exists without its Secret')
            source = self.obj('secret', 'pki-controller-database')
            parsed = urlsplit(base64.b64decode(
                source['data']['url']).decode())
            m.require(parsed.scheme in ('postgres', 'postgresql')
                      and parsed.path == '/video_cloud'
                      and parsed.hostname in (
                          'postgresql.video-cloud-dev-platform.svc',
                          'postgresql.video-cloud-dev-platform.svc.cluster.local'),
                      'unexpected Dev PKI database endpoint')
            password = secrets.token_hex(32)
            netloc = (role + ':' + quote(password, safe='') + '@'
                      + parsed.hostname + ':' + str(parsed.port or 5432))
            dsn = urlunsplit((parsed.scheme, netloc, parsed.path,
                              parsed.query, ''))
            self.secret('pkiturn-database', {'dsn': dsn})
        if not exists:
            m.require(re.fullmatch(r'[0-9a-f]{64}', password),
                      'unsafe generated pkiturn database password')
            self.sql('BEGIN; CREATE ROLE ' + role
                     + ' LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE '
                     'NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 4 PASSWORD \''
                     + password + '\' IN ROLE rtk_pki_verifier; COMMIT;')
        safe = self.sql(
            "SELECT rolcanlogin AND NOT(rolsuper OR rolcreatedb OR rolcreaterole "
            "OR rolreplication OR rolbypassrls) AND rolconnlimit=4 "
            "FROM pg_roles WHERE rolname='" + role + "';")
        memberships = self.sql(
            "SELECT COALESCE(string_agg(parent.rolname,',' ORDER BY "
            "parent.rolname),'') FROM pg_auth_members membership "
            "JOIN pg_roles parent ON parent.oid=membership.roleid "
            "JOIN pg_roles child ON child.oid=membership.member "
            "WHERE child.rolname='" + role + "';")
        m.require(safe == 't' and memberships == 'rtk_pki_verifier',
                  'pkiturn database role is broader than verifier-only')
        return role

    def ensure(self, desired):
        kind = desired['kind'].lower()
        name = desired['metadata']['name']
        raw = self.kube(['-n', NS, 'get', kind, name,
                         '--ignore-not-found', '-o', 'json'])
        if not raw.strip():
            self.create(desired)
            return
        current = json.loads(raw)
        if desired['kind'] == 'Secret':
            m.require(current.get('type') == desired.get('type'),
                      'existing R1 Secret type differs')
            if current.get('data') != desired.get('data'):
                m.require(name == 'pki-app-consumer-video-cloud-api-app',
                          'existing R1 Secret differs')
                self.observed_patch('secret', name, current, [{
                    'op': 'replace', 'path': '/data',
                    'value': desired['data']}])
        elif desired['kind'] == 'ConfigMap':
            if current.get('data') != desired.get('data'):
                m.require(name == 'pki-app-trust',
                          'existing R1 ConfigMap differs')
                self.observed_patch('configmap', name, current, [{
                    'op': 'replace', 'path': '/data',
                    'value': desired['data']}])
        elif desired['kind'] == 'PersistentVolumeClaim':
            m.require(current['spec']['accessModes'] == ['ReadWriteOnce']
                      and current['spec']['storageClassName'] ==
                      'linode-block-storage-retain'
                      and current['spec']['resources']['requests']['storage'] ==
                      '10Gi', 'existing R1 PVC differs')
        elif desired['kind'] == 'Deployment':
            m.require(current['spec']['selector'] == desired['spec']['selector'],
                      'existing R1 Deployment selector differs')
            if current['spec']['template'] != desired['spec']['template']:
                self.observed_patch('deployment', name, current, [{
                    'op': 'replace', 'path': '/spec/template',
                    'value': desired['spec']['template']}])
        elif desired['kind'] == 'Service':
            m.require(current['spec']['selector'] == desired['spec']['selector']
                      and current['spec']['ports'][0]['port'] == 8443,
                      'existing R1 Service differs')
        elif desired['kind'] == 'NetworkPolicy':
            m.require(current['spec'] == desired['spec'],
                      'existing R1 NetworkPolicy differs')
        else:
            m.require(False, 'existing R1 workload requires explicit reconciliation')
        self.save('reused-' + name + '.json', {
            'kind': desired['kind'], 'uid': current['metadata']['uid'],
            'resourceVersion': current['metadata']['resourceVersion']})

    def install_controller(self, image):
        app_identity = self.private_material('consumers', 'video-cloud-api-app')
        turn_identity = self.private_material('consumers', 'pkiturn')
        tls = self.obj('secret', 'pki-controller-tls')
        ca = base64.b64decode(tls['data']['ca.crt']).decode()
        ca = append_ca(append_ca(ca, app_identity['ca.crt']),
                       turn_identity['ca.crt'])
        self.observed_patch('secret', 'pki-controller-tls', tls, [{
            'op': 'replace', 'path': '/data/ca.crt',
            'value': base64.b64encode(ca.encode()).decode()}])
        owner = self.obj('deployment', 'pki-controller')
        template = copy.deepcopy(owner['spec']['template'])
        container = template['spec']['containers'][0]
        container['image'] = image
        container['env'] = env_replace(container['env'], {
            'PKI_REQUIRED_CONSUMERS_APP': CONSUMERS,
            'PKI_REQUIRED_BUNDLE_CONSUMERS_APP': CONSUMERS}, ())
        self.observed_patch('deployment', 'pki-controller', owner, [{
            'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', NS, 'rollout', 'status',
                   'deployment/pki-controller', '--timeout=240s'], timeout=250)
        self.check('app_controller_policy_installed', {
            'consumers': CONSUMERS.split(','), 'image': image,
            'separate_management_authorities': True})

    def install_api(self, image, root):
        identity = self.private_material('consumers', 'video-cloud-api-app')
        server = self.private_material('servers', 'video-cloud-api-app-pki')
        service_root = self.obj('configmap', 'pki-service-host-root')
        management_ca = service_root.get('data', {}).get('root.pem', '')
        m.require(management_ca.startswith('-----BEGIN CERTIFICATE-----'),
                  'Dev controller serving root is missing')
        management = dict(identity)
        management['ca.crt'] = management_ca
        self.secret('pki-app-consumer-video-cloud-api-app', management)
        self.secret('video-cloud-api-app-pki-tls', {
            'tls.crt': server['tls.crt'], 'tls.key': server['tls.key']})
        entry = {'issuer': root,
                 'state_path': '/run/pki-state/app/' + root['issuer_id'] + '-crl.json'}
        self.ensure({'apiVersion': 'v1', 'kind': 'ConfigMap',
                     'metadata': {'name': 'pki-app-trust', 'namespace': NS},
                     'data': {
                         'roots.pem': root['certificate_pem'],
                         'root-trust.json': initial_state(root),
                         'crls.json': json.dumps([entry]),
                         'issuers.json': json.dumps([{
                             'issuer_id': root['issuer_id'],
                             'trust_bundle_version': root['trust_bundle_version']}])}})
        self.ensure({'apiVersion': 'v1', 'kind': 'PersistentVolumeClaim',
                     'metadata': {'name': 'video-cloud-api-app-pki-trust',
                                  'namespace': NS},
                     'spec': {'accessModes': ['ReadWriteOnce'],
                              'storageClassName': 'linode-block-storage-retain',
                              'resources': {'requests': {'storage': '10Gi'}}}})
        base = self.obj('deployment', 'video-cloud-api-pki')
        deployment = {'apiVersion': 'apps/v1', 'kind': 'Deployment',
                      'metadata': {'name': 'video-cloud-api-app-pki',
                                   'namespace': NS},
                      'spec': copy.deepcopy(base['spec'])}
        deployment['spec']['selector']['matchLabels']['app.kubernetes.io/name'] = \
            'video-cloud-api-app-pki'
        template = deployment['spec']['template']
        template['metadata']['labels']['app.kubernetes.io/name'] = \
            'video-cloud-api-app-pki'
        template['metadata'].setdefault('annotations', {})[
            'rtk.realtek.com/app-management-identity-sha256'] = m.digest(
                ''.join(management[key] for key in sorted(management)).encode())
        pod = template['spec']
        container = pod['containers'][0]
        container['image'] = image
        container['ports'] = [port for port in container.get('ports', [])
                              if port['name'] == 'https']
        remove = ('VIDEO_CLOUD_AUTH_DEVICE_', 'VIDEO_CLOUD_CONTROLLER_IDENTITY_',
                  'VIDEO_CLOUD_MQTT_AUTH_')
        container['env'] = env_replace(container['env'], {
            'VIDEO_CLOUD_API_BASE_URL':
                'https://video-cloud-api-app-pki.' + NS + '.svc:8443',
            'VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED': 'false',
            'VIDEO_CLOUD_AUTH_PRODUCT_PKI_REQUIRE_CRLS': 'false',
            'VIDEO_CLOUD_AUTH_APP_PKI_ENABLED': 'true',
            'VIDEO_CLOUD_AUTH_MTLS_REQUIRED': 'true',
            'VIDEO_CLOUD_AUTH_DISABLE_ACL': 'false',
            'VIDEO_CLOUD_AUTH_TRUSTED_CLIENT_CERT_HEADERS': 'false',
            'VIDEO_CLOUD_AUTH_ENABLE_LEGACY_CERT': 'false',
            'VIDEO_CLOUD_AUTH_APP_CA_CERT': '',
            'VIDEO_CLOUD_AUTH_CRL_MANIFEST': '/run/pki-app/crls.json',
            'VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST': '/run/pki-app/issuers.json',
            'VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_STATE':
                '/run/pki-state/app/root-policy.json',
            'VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_ROOT_ID': root['issuer_id'],
            'VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_CONTROLLER_URL':
                'https://pki-controller.' + NS + '.svc:18446',
            'VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_MANAGEMENT_CA':
                '/run/pki-management/ca.crt',
            'VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_MANAGEMENT_CERT':
                '/run/pki-management/tls.crt',
            'VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_MANAGEMENT_KEY':
                '/run/pki-management/tls.key',
            'VIDEO_CLOUD_API_TLS_CERT_PATH': '/run/pki-server/tls.crt',
            'VIDEO_CLOUD_API_TLS_KEY_PATH': '/run/pki-server/tls.key',
            'VIDEO_CLOUD_MQTT_ENABLED': 'false'}, remove)
        mounts = [mount for mount in container.get('volumeMounts', [])
                  if mount['name'] not in ('pki-device', 'pki-state',
                                           'pki-initial-state', 'mqtt-callback',
                                           'controller-identity')]
        mounts += [
            {'name': 'pki-app', 'mountPath': '/run/pki-app', 'readOnly': True},
            {'name': 'pki-state', 'mountPath': '/run/pki-state'},
            {'name': 'pki-management', 'mountPath': '/run/pki-management',
             'readOnly': True}]
        container['volumeMounts'] = mounts
        volumes = [volume for volume in pod.get('volumes', [])
                   if volume['name'] not in ('pki-device', 'pki-state',
                                             'pki-initial-state', 'mqtt-callback',
                                             'controller-identity')]
        for volume in volumes:
            if volume['name'] == 'pki-server':
                volume['secret']['secretName'] = 'video-cloud-api-app-pki-tls'
        volumes += [
            {'name': 'pki-app', 'configMap': {'name': 'pki-app-trust'}},
            {'name': 'pki-state', 'persistentVolumeClaim': {
                'claimName': 'video-cloud-api-app-pki-trust'}},
            {'name': 'pki-management', 'secret': {
                'secretName': 'pki-app-consumer-video-cloud-api-app',
                'defaultMode': 288}}]
        pod['volumes'] = volumes
        pod['initContainers'] = [{
            'name': 'initialize-app-trust-state', 'image': image,
            'command': ['/bin/sh', '-ec',
                        'umask 077; mkdir -p /run/pki-state/app; '
                        'chmod 700 /run/pki-state/app; '
                        'if [ ! -e /run/pki-state/app/root-policy.json ]; then '
                        'cp /run/pki-app/root-trust.json '
                        '/run/pki-state/app/root-policy.json.new; '
                        'chmod 600 /run/pki-state/app/root-policy.json.new; '
                        'mv /run/pki-state/app/root-policy.json.new '
                        '/run/pki-state/app/root-policy.json; fi'],
            'volumeMounts': [
                {'name': 'pki-state', 'mountPath': '/run/pki-state'},
                {'name': 'pki-app', 'mountPath': '/run/pki-app',
                 'readOnly': True}]}]
        self.ensure(deployment)
        self.ensure({'apiVersion': 'v1', 'kind': 'Service',
                     'metadata': {'name': 'video-cloud-api-app-pki',
                                  'namespace': NS},
                     'spec': {'selector': {'app.kubernetes.io/name':
                                           'video-cloud-api-app-pki'},
                              'ports': [{'name': 'https', 'port': 8443,
                                         'targetPort': 'https'}]}})
        self.kube(['-n', NS, 'rollout', 'status',
                   'deployment/video-cloud-api-app-pki', '--timeout=240s'],
                  timeout=250)
        bundle, policy = self.wait_receipts(root, ['video-cloud-api-app'])
        self.check('app_api_consumer_installed', {
            'root_id': root['issuer_id'], 'image': image,
            'device_listener_changed': False,
            'dynamic_app_state': '/run/pki-state/app/root-policy.json',
            'bundle_receipts': bundle, 'root_policy_receipts': policy})

    def install_broker(self, image, root):
        owner = self.obj('deployment', 'mqtt-pki')
        template = copy.deepcopy(owner['spec']['template'])
        containers = {item['name']: item
                      for item in template['spec']['containers']}
        m.require(set(containers) == {'mqtt', 'pkibroker'},
                  'mqtt-pki container ownership changed')
        mqtt_before = copy.deepcopy(containers['mqtt'])
        broker = containers['pkibroker']
        broker['image'] = image
        broker['env'] = env_replace(broker.get('env', []), {
            'PKI_BROKER_APP_PKI_ENABLED': 'true',
            'PKI_BROKER_APP_CRL_MANIFEST': '/run/pki-app/crls.json',
            'PKI_BROKER_APP_ROOT_ID': root['issuer_id'],
            'PKI_BROKER_APP_ROOT_STATE':
                '/run/pki-state/app/root-policy.json',
            'PKI_BROKER_APP_ROOTS': '/run/pki-app/roots.pem',
            'PKI_BROKER_APP_BUNDLE_ACK_ENABLED': 'true'}, ())
        broker['volumeMounts'] = [
            item for item in broker.get('volumeMounts', [])
            if item['name'] != 'pki-app'] + [{
                'name': 'pki-app', 'mountPath': '/run/pki-app',
                'readOnly': True}]
        pod = template['spec']
        pod['volumes'] = [item for item in pod.get('volumes', [])
                          if item['name'] != 'pki-app'] + [{
                              'name': 'pki-app',
                              'configMap': {'name': 'pki-app-trust'}}]
        initializers = {item['name']: item
                        for item in pod.get('initContainers', [])}
        m.require('prepare-pki-state' in initializers,
                  'mqtt-pki state initializer changed')
        state_init = initializers['prepare-pki-state']
        state_init['image'] = image
        state_init['command'] = [
            '/bin/sh', '-ec',
            'umask 077; mkdir -p /run/pki-state/device '
            '/run/pki-state/app /run/pki-state/broker-identity/private; '
            'chmod 700 /run/pki-state/device /run/pki-state/app '
            '/run/pki-state/broker-identity '
            '/run/pki-state/broker-identity/private; '
            'find /run/pki-state/device /run/pki-state/app '
            '/run/pki-state/broker-identity/private -type f '
            '-exec chmod 600 {} +']
        template['metadata'].setdefault('annotations', {})[
            'rtk.realtek.com/app-trust-sha256'] = m.digest(
                (root['issuer_id'] + root['trust_bundle_version']
                 + image).encode())
        m.require(containers['mqtt'] == mqtt_before,
                  'App rollout changed the EMQX container')
        if template != owner['spec']['template']:
            self.observed_patch('deployment', 'mqtt-pki', owner, [{
                'op': 'replace', 'path': '/spec/template',
                'value': template}])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/mqtt-pki',
                   '--timeout=300s'], timeout=310)
        current = self.obj('deployment', 'mqtt-pki')
        m.require(current.get('status', {}).get('readyReplicas') ==
                  current['spec']['replicas'], 'mqtt-pki is not ready')
        bundle, policy = self.wait_receipts(
            root, ['video-cloud-api-app', 'pkibroker'])
        self.check('app_broker_consumer_installed', {
            'root_id': root['issuer_id'], 'image': image,
            'emqx_container_unchanged': True,
            'state': '/run/pki-state/app/root-policy.json',
            'bundle_receipts': bundle, 'root_policy_receipts': policy})
        device = m.read(self.foundation / 'device-2/enroll-request.json')['devid']
        auth, auth_attempts = self.wait_positive_auth(
            self.foundation / 'device-2/v4', device)
        mqtt_attempts = self.wait_positive_mqtt(auth, device)
        self.mqtt(auth, device, 'roundtrip')
        self.check('device_baseline_after_app_broker', {
            'direct_mtls': 'passed', 'mqtt_acl_qos1': 'passed',
            'auth_attempts': auth_attempts, 'mqtt_attempts': mqtt_attempts})

    def install_turn(self, image, root):
        role = self.turn_database()
        cli = self.base / 'pki/pkiturn/cli-password'
        ssh_dir = self.base / 'pki/pkiturn-ssh'
        key, known = ssh_dir / 'id_ed25519', ssh_dir / 'known_hosts'
        for path in (cli, key, known):
            m.require(path.is_file() and path.stat().st_mode & 0o777 == 0o600,
                      'private pkiturn credential missing or unsafe')
        m.require(re.fullmatch(r'[0-9a-f]{64}\n?', cli.read_text()),
                  'unexpected pkiturn CLI credential')
        host_keys = [line for line in known.read_text().splitlines()
                     if line and not line.startswith('#')]
        m.require('PRIVATE KEY' in key.read_text() and host_keys
                  and all((line.split()[0] == '172.232.182.134'
                           or line.startswith('|1|'))
                          and len(line.split()) >= 3 for line in host_keys),
                  'pkiturn SSH material differs from pinned hashed host keys')
        self.secret('pkiturn-cli', {'password': cli.read_text()})
        self.secret('pkiturn-ssh', {
            'id_ed25519': key.read_text(), 'known_hosts': known.read_text()})
        identity = self.private_material('consumers', 'pkiturn')
        service_root = self.obj('configmap', 'pki-service-host-root')
        management = dict(identity)
        management['ca.crt'] = service_root['data']['root.pem']
        self.secret('pki-app-consumer-pkiturn', management)
        self.ensure({'apiVersion': 'v1', 'kind': 'PersistentVolumeClaim',
                     'metadata': {'name': 'pkiturn-app-trust',
                                  'namespace': NS},
                     'spec': {'accessModes': ['ReadWriteOnce'],
                              'storageClassName': 'linode-block-storage-retain',
                              'resources': {'requests': {'storage': '10Gi'}}}})
        labels = {'app.kubernetes.io/name': 'pkiturn',
                  'app.kubernetes.io/part-of': 'rtk-cloud',
                  'rtk.realtek.com/stack': 'video-cloud-dev'}
        restricted = {
            'runAsNonRoot': True, 'runAsUser': 10001, 'runAsGroup': 10001,
            'allowPrivilegeEscalation': False,
            'capabilities': {'drop': ['ALL']}}
        env = [
            {'name': 'PKI_ENVIRONMENT', 'value': 'dev'},
            {'name': 'PKI_TURN_DEDICATED_RELAY', 'value': 'true'},
            {'name': 'PKI_DATABASE_URL', 'valueFrom': {'secretKeyRef': {
                'name': 'pkiturn-database', 'key': 'dsn'}}},
            {'name': 'PKI_TURN_REDIS_ADDR', 'value':
                'redis.video-cloud-dev-platform.svc.cluster.local:6379'},
            {'name': 'PKI_TURN_REDIS_PREFIX', 'value': 'video_cloud:webrtc'},
            {'name': 'PKI_TURN_CLI_ADDR', 'value': '127.0.0.1:5766'},
            {'name': 'PKI_TURN_CLI_PASSWORD_FILE', 'value':
                '/run/pkiturn-cli/password'},
            {'name': 'PKI_TURN_HEALTH_FILE', 'value':
                '/run/pkiturn-health/health.json'},
            {'name': 'PKI_TURN_APP_CRL_MANIFEST', 'value':
                '/run/pki-app/crls.json'},
            {'name': 'PKI_TURN_APP_ROOT_ID', 'value': root['issuer_id']},
            {'name': 'PKI_TURN_APP_ROOT_STATE', 'value':
                '/run/pki-state/app/root-policy.json'},
            {'name': 'PKI_TURN_APP_ROOTS', 'value': '/run/pki-app/roots.pem'},
            {'name': 'PKI_TURN_APP_BUNDLE_ACK_ENABLED', 'value': 'true'},
            {'name': 'PKI_TURN_PKI_CONTROLLER_URL', 'value':
                'https://pki-controller.' + NS + '.svc:18446'},
            {'name': 'PKI_TURN_MANAGEMENT_CA', 'value':
                '/run/pki-management/ca.crt'},
            {'name': 'PKI_TURN_MANAGEMENT_CERT', 'value':
                '/run/pki-management/tls.crt'},
            {'name': 'PKI_TURN_MANAGEMENT_KEY', 'value':
                '/run/pki-management/tls.key'}]
        private_init = {
            'name': 'prepare-pkiturn-private', 'image': image,
            'command': ['/bin/sh', '-ec'],
            'args': [
                'umask 077; mkdir -p /private/cli /private/ssh '
                '/run/pki-state/app; '
                'cp /source/cli/password /private/cli/password; '
                'cp /source/ssh/id_ed25519 /private/ssh/id_ed25519; '
                'cp /source/ssh/known_hosts /private/ssh/known_hosts; '
                'chmod 400 /private/cli/password /private/ssh/id_ed25519; '
                'chmod 444 /private/ssh/known_hosts; '
                'chmod 700 /run/pki-state/app; '
                'find /run/pki-state/app -type f -exec chmod 600 {} +'],
            'securityContext': restricted,
            'volumeMounts': [
                {'name': 'cli-source', 'mountPath': '/source/cli',
                 'readOnly': True},
                {'name': 'ssh-source', 'mountPath': '/source/ssh',
                 'readOnly': True},
                {'name': 'cli-private', 'mountPath': '/private/cli'},
                {'name': 'ssh-private', 'mountPath': '/private/ssh'},
                {'name': 'pki-state', 'mountPath': '/run/pki-state'}]}
        operator = {
            'name': 'pkiturn', 'image': image,
            'command': ['/app/pkiturn', 'watch'], 'env': env,
            'securityContext': restricted,
            'resources': {'requests': {'cpu': '50m', 'memory': '64Mi'},
                          'limits': {'memory': '256Mi'}},
            'startupProbe': {'exec': {'command': ['/app/pkiturn', 'health']},
                             'periodSeconds': 5, 'timeoutSeconds': 3,
                             'failureThreshold': 36},
            'readinessProbe': {'exec': {'command': ['/app/pkiturn', 'health']},
                               'periodSeconds': 10, 'timeoutSeconds': 3,
                               'failureThreshold': 2},
            'livenessProbe': {'exec': {'command': ['/app/pkiturn', 'health']},
                              'periodSeconds': 10, 'timeoutSeconds': 3,
                              'failureThreshold': 3},
            'volumeMounts': [
                {'name': 'cli-private', 'mountPath': '/run/pkiturn-cli',
                 'readOnly': True},
                {'name': 'health', 'mountPath': '/run/pkiturn-health'},
                {'name': 'pki-app', 'mountPath': '/run/pki-app',
                 'readOnly': True},
                {'name': 'pki-state', 'mountPath': '/run/pki-state'},
                {'name': 'pki-management', 'mountPath': '/run/pki-management',
                 'readOnly': True}]}
        tunnel = {
            'name': 'coturn-cli-tunnel', 'image': image,
            'command': ['/usr/bin/ssh', '-NT', '-o', 'BatchMode=yes', '-o',
                        'ExitOnForwardFailure=yes', '-o',
                        'ServerAliveInterval=15', '-o',
                        'ServerAliveCountMax=3', '-o',
                        'StrictHostKeyChecking=yes', '-o',
                        'UserKnownHostsFile=/run/pkiturn-ssh/known_hosts',
                        '-o', 'GlobalKnownHostsFile=/dev/null', '-i',
                        '/run/pkiturn-ssh/id_ed25519', '-L',
                        '127.0.0.1:5766:127.0.0.1:5766',
                        'root@172.232.182.134'],
            'securityContext': restricted,
            'resources': {'requests': {'cpu': '10m', 'memory': '16Mi'},
                          'limits': {'memory': '64Mi'}},
            'volumeMounts': [{'name': 'ssh-private',
                              'mountPath': '/run/pkiturn-ssh',
                              'readOnly': True}]}
        deployment = {
            'apiVersion': 'apps/v1', 'kind': 'Deployment',
            'metadata': {'name': 'pkiturn', 'namespace': NS},
            'spec': {'replicas': 1, 'strategy': {'type': 'Recreate'},
                     'selector': {'matchLabels': {
                         'app.kubernetes.io/name': 'pkiturn'}},
                     'template': {
                         'metadata': {'labels': labels, 'annotations': {
                             'rtk.realtek.com/app-trust-sha256': m.digest(
                                 (root['issuer_id']
                                  + root['trust_bundle_version']
                                  + image).encode())}},
                         'spec': {
                             'securityContext': {
                                 'fsGroup': 10001,
                                 'fsGroupChangePolicy': 'OnRootMismatch'},
                             'imagePullSecrets': [{'name': 'ghcr-pull'}],
                             'terminationGracePeriodSeconds': 20,
                             'initContainers': [private_init],
                             'containers': [operator, tunnel],
                             'volumes': [
                                 {'name': 'cli-source', 'secret': {
                                     'secretName': 'pkiturn-cli',
                                     'defaultMode': 288}},
                                 {'name': 'ssh-source', 'secret': {
                                     'secretName': 'pkiturn-ssh',
                                     'defaultMode': 288}},
                                 {'name': 'cli-private', 'emptyDir': {}},
                                 {'name': 'ssh-private', 'emptyDir': {}},
                                 {'name': 'health', 'emptyDir': {}},
                                 {'name': 'pki-app', 'configMap': {
                                     'name': 'pki-app-trust'}},
                                 {'name': 'pki-state',
                                  'persistentVolumeClaim': {
                                      'claimName': 'pkiturn-app-trust'}},
                                 {'name': 'pki-management', 'secret': {
                                     'secretName':
                                         'pki-app-consumer-pkiturn',
                                     'defaultMode': 288}}]}}}}
        self.ensure(deployment)
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/pkiturn',
                   '--timeout=300s'], timeout=310)
        bundle, policy = self.wait_receipts(
            root, ['video-cloud-api-app', 'pkibroker', 'pkiturn'])
        current = self.obj('deployment', 'pkiturn')
        m.require(current.get('status', {}).get('readyReplicas') == 1,
                  'pkiturn is not ready')
        self.check('app_turn_consumer_installed', {
            'root_id': root['issuer_id'], 'image': image,
            'database_role': role, 'database_scope': 'verifier-only',
            'cli': 'SSH forwarded loopback; no Kubernetes Service',
            'state': '/run/pki-state/app/root-policy.json',
            'bundle_receipts': bundle, 'root_policy_receipts': policy})

    def install(self):
        m.require(IMAGE.fullmatch(self.args.image or ''),
                  'immutable Dev Video Cloud image required')
        root = self.app_root()
        self.install_controller(self.args.image)
        self.ensure({
            'apiVersion': 'networking.k8s.io/v1',
            'kind': 'NetworkPolicy',
            'metadata': {'name': 'allow-app-trust-consumers', 'namespace': NS},
            'spec': {
                'podSelector': {'matchLabels': {
                    'app.kubernetes.io/name': 'pki-controller'}},
                'policyTypes': ['Ingress'],
                'ingress': [{
                    'from': [{'podSelector': {'matchExpressions': [{
                        'key': 'app.kubernetes.io/name',
                        'operator': 'In',
                        'values': ['video-cloud-api-app-pki', 'mqtt-pki',
                                   'pkiturn']}]}}],
                    'ports': [{'protocol': 'TCP', 'port': 18446}]}]}})
        self.install_api(self.args.image, root)
        self.install_broker(self.args.image, root)
        self.install_turn(self.args.image, root)


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--authority', required=True)
    parser.add_argument('--image', required=True)
    args = parser.parse_args()
    args.phase = 'install-app-api'
    runner = AppRuntime(args)
    try:
        runner.preflight()
        runner.install()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'
        runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        print(json.dumps({'status': runner.report['status'],
                          'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(
        RuntimeError('interrupted; reconcile R1 App runtime before retry')))
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
