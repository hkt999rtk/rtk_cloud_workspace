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
import signal
import sys


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
            m.require(current.get('type') == desired.get('type')
                      and current.get('data') == desired.get('data'),
                      'existing R1 Secret differs')
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
        self.secret('pki-app-consumer-video-cloud-api-app', identity)
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
        self.check('app_api_consumer_installed', {
            'root_id': root['issuer_id'], 'image': image,
            'device_listener_changed': False,
            'dynamic_app_state': '/run/pki-state/app/root-policy.json'})

    def install(self):
        m.require(IMAGE.fullmatch(self.args.image or ''),
                  'immutable Dev Video Cloud image required')
        root = self.app_root()
        self.install_controller(self.args.image)
        self.install_api(self.args.image, root)


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
