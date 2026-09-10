#!/usr/bin/env python3
"""Adopt governed OpenBao TLS in dev, one resumable phase at a time."""
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


spec = importlib.util.spec_from_file_location(
    'service_hierarchy', Path(__file__).with_name('run.py'))
h = importlib.util.module_from_spec(spec)
spec.loader.exec_module(h)
m, NS = h.m, h.NS

CONSUMERS = ['certissuer', 'pki-controller']
IMAGE_PATTERN = re.compile(
    r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}')


class OpenBaoHostRun(h.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = (
            'Dev-only OpenBao TLS host and actual provider clients')
        self.report['openbao_host_runner_sha256'] = m.digest(
            Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def openbao_root(self, status):
        source = Path(self.args.authority)
        saved = m.read(source / 'root-ready.json')
        root = self.api('/issuers/' + saved['issuer_id'])
        m.require(root['environment'] == 'dev'
                  and root['trust_domain'] == 'openbao_tls'
                  and root['kind'] == 'root'
                  and root['status'] == status
                  and root['certificate_fingerprint_sha256'] ==
                  saved['certificate_fingerprint_sha256']
                  and root['trust_bundle_version'] ==
                  saved['trust_bundle_version'],
                  'saved OpenBao TLS Root changed')
        self.save('openbao-tls-root.json', root)
        return root

    def scoped_patch(self, name, before, template):
        self.save('before-' + name + '-deployment.json', before)
        patches = [
            {'op': 'test', 'path': '/metadata/resourceVersion',
             'value': before['metadata']['resourceVersion']},
            {'op': 'replace', 'path': '/spec/template', 'value': template}]
        changed = json.loads(self.kube([
            '-n', NS, 'patch', 'deployment', name, '--type=json',
            '--patch-file=/dev/stdin', '-o', 'json'], json.dumps(patches)))
        desired = {'apiVersion': changed['apiVersion'], 'kind': changed['kind'],
                   'metadata': {key: changed['metadata'][key]
                                for key in ('name', 'namespace', 'labels',
                                            'annotations')
                                if key in changed['metadata']},
                   'spec': changed['spec']}
        self.save('after-' + name + '-deployment.json', desired)
        m.write(self.base / 'pki/controller-bootstrap/rollout' /
                (name + '-deployment.json'), desired)

    def staged_client_template(self, owner, image, ca_configmap,
                               manifest_configmap, root):
        template = json.loads(json.dumps(owner['spec']['template']))
        pod = template['spec']
        m.require(len(pod['containers']) == 1,
                  'provider Deployment topology changed')
        container = pod['containers'][0]
        m.require(container['name'] == owner['metadata']['name'],
                  'provider container name changed')
        volumes = {item['name']: item for item in pod.get('volumes', [])}
        m.require(volumes.get('openbao-ca', {}).get('configMap', {}).get(
                  'name') == 'pki-openbao-transport-ca'
                  and 'openbao-server-bundles' not in volumes,
                  'OpenBao staged trust source changed; reconcile')
        mounts = {item['name']: item
                  for item in container.get('volumeMounts', [])}
        m.require(mounts.get('openbao-ca', {}).get('mountPath') ==
                  '/run/openbao-ca'
                  and any(item['name'] == 'host-root'
                          and item['mountPath'] == '/run/pki-host-root'
                          for item in container.get('volumeMounts', [])),
                  'provider trust mounts changed')
        volumes['openbao-ca']['configMap']['name'] = ca_configmap
        pod['volumes'].append({
            'name': 'openbao-server-bundles',
            'configMap': {'name': manifest_configmap}})
        container.setdefault('volumeMounts', []).append({
            'name': 'openbao-server-bundles',
            'mountPath': '/run/openbao-server-bundles', 'readOnly': True})
        container['image'] = image
        container['env'] = h.with_env(container.get('env', []), {
            'OPENBAO_SERVER_BUNDLE_MANIFEST':
                '/run/openbao-server-bundles/issuers.json',
            'OPENBAO_SERVER_BUNDLE_ROOT_SHA256':
                root['certificate_fingerprint_sha256'],
            'OPENBAO_SERVER_BUNDLE_PKI_CONTROLLER_URL':
                'https://pki-controller.' + NS + '.svc:18446',
            'OPENBAO_SERVER_BUNDLE_MANAGEMENT_CA':
                '/run/pki-host-root/root.pem'})
        if owner['metadata']['name'] == 'pki-controller':
            container['env'] = h.with_env(container['env'], {
                'PKI_REQUIRED_BUNDLE_CONSUMERS_OPENBAO_TLS':
                    ','.join(CONSUMERS)})
        template.setdefault('metadata', {}).setdefault('annotations', {})[
            'rtk.cloud/openbao-root-staging'] = self.output.name
        return template

    def recovery_client_template(self, owner, image, ca_configmap,
                                 manifest_configmap, root):
        pod = owner['spec']['template']['spec']
        volumes = {item['name']: item for item in pod.get('volumes', [])}
        source = volumes.get('openbao-ca', {}).get('configMap', {}).get('name')
        if source == 'pki-openbao-transport-ca':
            return self.staged_client_template(
                owner, image, ca_configmap, manifest_configmap, root)
        m.require(source == ca_configmap,
                  'OpenBao recovery trust source changed')
        template = json.loads(json.dumps(owner['spec']['template']))
        template['spec']['containers'][0]['image'] = image
        candidate = {'metadata': owner['metadata'],
                     'spec': {'template': template}}
        self.verify_staged_client(candidate, image, ca_configmap,
                                  manifest_configmap, root)
        template.setdefault('metadata', {}).setdefault('annotations', {})[
            'rtk.cloud/openbao-root-staging'] = self.output.name
        return template

    def verify_staged_client(self, owner, image, ca_configmap,
                             manifest_configmap, root):
        pod = owner['spec']['template']['spec']
        m.require(len(pod['containers']) == 1,
                  'provider Deployment topology changed')
        container = pod['containers'][0]
        m.require(container['name'] == owner['metadata']['name']
                  and container['image'] == image,
                  'installed provider image changed')
        volumes = {item['name']: item for item in pod.get('volumes', [])}
        m.require(volumes.get('openbao-ca', {}).get('configMap', {}).get(
                  'name') == ca_configmap
                  and volumes.get('openbao-server-bundles', {}).get(
                      'configMap', {}).get('name') == manifest_configmap,
                  'installed OpenBao trust sources changed')
        mounts = {item['name']: item
                  for item in container.get('volumeMounts', [])}
        m.require(mounts.get('openbao-ca', {}).get('mountPath') ==
                  '/run/openbao-ca'
                  and mounts.get('openbao-server-bundles', {}).get(
                      'mountPath') == '/run/openbao-server-bundles'
                  and mounts.get('host-root', {}).get('mountPath') ==
                  '/run/pki-host-root',
                  'installed OpenBao trust mounts changed')
        env = {item['name']: item.get('value')
               for item in container.get('env', [])}
        expected = {
            'OPENBAO_SERVER_BUNDLE_MANIFEST':
                '/run/openbao-server-bundles/issuers.json',
            'OPENBAO_SERVER_BUNDLE_ROOT_SHA256':
                root['certificate_fingerprint_sha256'],
            'OPENBAO_SERVER_BUNDLE_PKI_CONTROLLER_URL':
                'https://pki-controller.' + NS + '.svc:18446',
            'OPENBAO_SERVER_BUNDLE_MANAGEMENT_CA':
                '/run/pki-host-root/root.pem'}
        m.require(all(env.get(key) == value for key, value in expected.items()),
                  'installed OpenBao bundle settings changed')
        if owner['metadata']['name'] == 'pki-controller':
            m.require(env.get(
                'PKI_REQUIRED_BUNDLE_CONSUMERS_OPENBAO_TLS') ==
                ','.join(CONSUMERS), 'OpenBao activation gate changed')

    def root_configmaps(self, root):
        legacy = self.obj('configmap', 'pki-openbao-transport-ca')
        m.require(set(legacy.get('data', {})) == {'ca.crt'},
                  'legacy OpenBao public CA source changed')
        suffix = root['issuer_id'][:8]
        ca_name = 'pki-openbao-transport-ca-' + suffix
        manifest_name = 'pki-openbao-tls-bundles-' + suffix
        refs = [{'issuer_id': root['issuer_id'],
                 'trust_bundle_version': root['trust_bundle_version']}]
        return ca_name, manifest_name, {
            ca_name: {'ca.crt': h.append_pem(
                legacy['data']['ca.crt'], root['certificate_pem'])},
            manifest_name: {'issuers.json': json.dumps(refs)}}

    def ensure_root_configmaps(self, root):
        ca_name, manifest_name, expected = self.root_configmaps(root)
        for name in (ca_name, manifest_name):
            raw = self.kube(['-n', NS, 'get', 'configmap', name,
                             '--ignore-not-found', '-o', 'json'])
            if not raw.strip():
                self.create({'apiVersion': 'v1', 'kind': 'ConfigMap',
                             'metadata': {'name': name, 'namespace': NS},
                             'immutable': True, 'data': expected[name]})
                continue
            current = json.loads(raw)
            m.require(current.get('immutable') is True
                      and current.get('data') == expected[name],
                      'installed OpenBao Root ConfigMap changed: ' + name)
        return ca_name, manifest_name

    def require_activation_blocked(self, operation):
        # The operation requester owns activation. Approval accounts only supply
        # the two distinct approvals recorded during authority preparation.
        self.api('/operations/' + operation['operation_id'] + '/activate', {},
                 409)

    def require_activation_policy_absent(self, operation):
        self.api('/operations/' + operation['operation_id'] + '/activate', {},
                 403)

    def install_root_consumers(self):
        root = self.openbao_root('ready')
        m.require(IMAGE_PATTERN.fullmatch(self.args.image or ''),
                  'verified dev application image digest required')
        operation = m.read(Path(self.args.authority) / 'root-operation.json')
        self.require_activation_policy_absent(operation)
        ca_configmap, manifest_configmap = self.ensure_root_configmaps(root)
        for name in reversed(CONSUMERS):
            owner = self.obj('deployment', name)
            template = self.staged_client_template(
                owner, self.args.image, ca_configmap,
                manifest_configmap, root)
            self.scoped_patch(name, owner, template)
            self.kube(['-n', NS, 'rollout', 'status',
                       'deployment/' + name, '--timeout=300s'], timeout=310)
            if name == 'pki-controller':
                self.require_activation_blocked(operation)
        receipts = self.wait_receipts(root['issuer_id'],
                                     root['trust_bundle_version'], CONSUMERS)
        self.check('openbao_tls_root_installed_by_actual_clients', {
            'issuer_id': root['issuer_id'], 'consumers': receipts,
            'activation_without_policy_denied': True,
            'activation_without_receipts_denied': True,
            'ca_configmap': ca_configmap,
            'manifest_configmap': manifest_configmap,
            'existing_listener_preserved': True,
            'image': self.args.image})

    def finish_root_consumers(self):
        failed = m.read(Path(self.args.failed) / 'report.json')
        m.require(failed['status'] == 'failed'
                  and failed['phase'] == 'install-root-consumers',
                  'failed OpenBao Root consumer phase required')
        root = m.read(Path(self.args.authority) / 'root-ready.json')
        m.require(root['environment'] == 'dev'
                  and root['trust_domain'] == 'openbao_tls'
                  and root['kind'] == 'root'
                  and root['status'] == 'ready',
                  'saved OpenBao TLS Root is not ready')
        m.require(IMAGE_PATTERN.fullmatch(self.args.image or ''),
                  'verified dev application image digest required')
        operation = m.read(Path(self.args.authority) / 'root-operation.json')
        ca_configmap, manifest_configmap = self.ensure_root_configmaps(root)
        for name in reversed(CONSUMERS):
            owner = self.obj('deployment', name)
            template = self.recovery_client_template(
                owner, self.args.image, ca_configmap,
                manifest_configmap, root)
            if template != owner['spec']['template']:
                self.scoped_patch(name, owner, template)
            self.kube(['-n', NS, 'rollout', 'status',
                       'deployment/' + name, '--timeout=300s'], timeout=310)
            self.verify_staged_client(self.obj('deployment', name),
                                      self.args.image, ca_configmap,
                                      manifest_configmap, root)
            if name == 'pki-controller':
                root = self.openbao_root('ready')
                self.require_activation_blocked(operation)
        receipts = self.wait_receipts(root['issuer_id'],
                                     root['trust_bundle_version'], CONSUMERS)
        self.check('openbao_tls_root_consumer_recovery', {
            'issuer_id': root['issuer_id'], 'consumers': receipts,
            'reconciled_from': str(Path(self.args.failed)),
            'activation_without_receipts_denied': True,
            'existing_listener_preserved': True,
            'image': self.args.image})

    def recovery_preflight(self):
        m.require(self.kube(['config', 'current-context']).strip() ==
                  self.context, 'canonical dev context mismatch')
        namespace = self.obj('namespace', NS)
        m.require(namespace['metadata']['name'] == NS, 'wrong namespace')
        account = self.obj('deployment', 'account-manager',
                           'video-cloud-dev-account-manager')
        m.require(account.get('status', {}).get('readyReplicas', 0) ==
                  account['spec']['replicas'] > 0,
                  'Account Manager unavailable during recovery')
        self.forward('am', 'video-cloud-dev-account-manager',
                     'account-manager', 80)
        self.accounts = m.read(self.foundation / 'accounts.json')
        identities = {name: self.login(credentials)[1]
                      for name, credentials in self.accounts.items()}
        m.require(len(set(identities.values())) == 3,
                  'approval accounts are not distinct')
        self.check('recovery_preflight', {
            'context': self.context, 'namespace': NS,
            'account_manager_ready': True,
            'controller_may_be_unavailable': True})

    def activate_root(self):
        root = self.openbao_root('ready')
        operation = m.read(Path(self.args.authority) / 'root-operation.json')
        receipts = self.wait_receipts(root['issuer_id'],
                                     root['trust_bundle_version'], CONSUMERS)
        self.api('/operations/' + operation['operation_id'] + '/activate', {},
                 204)
        root = self.api('/issuers/' + root['issuer_id'])
        m.require(root['status'] == 'active',
                  'OpenBao TLS Root did not activate')
        self.save('openbao-tls-root-active.json', root)
        now = dt.datetime.now(dt.timezone.utc).replace(microsecond=0)
        request = {
            'issuer_id': root['issuer_id'],
            'issuer_fingerprint_sha256':
                root['certificate_fingerprint_sha256'],
            'crl_number': '1', 'this_update': m.stamp(now),
            'next_update': m.stamp(now + dt.timedelta(days=1)),
            'revocations': []}
        self.save('root-crl-request.json', request)
        expected = self.ceremony_call([
            'crl-digest', self.output / 'root-crl-request.json']).strip()
        authority = Path(self.args.authority)
        self.ceremony_call([
            'crl', '--issuer', self.output / 'openbao-tls-root-active.json',
            '--crl-request', self.output / 'root-crl-request.json',
            '--expected-request-sha256', expected,
            '--key', authority / 'root-offline-simulation/ca-key.encrypted.pem',
            '--passphrase-file', m.read(
                authority / 'passphrase-reference.json')['path'],
            '--out', self.output / 'root-crl'])
        raw = (self.output / 'root-crl/revocations.pem').read_text()
        record = self.api('/issuers/' + root['issuer_id'] + '/crl', {
            'crl_pem': raw}, role='approver')
        self.save('openbao-tls-root-crl.json', record)
        self.check('openbao_tls_root_active_with_crl', {
            'issuer_id': root['issuer_id'], 'consumers': receipts,
            'crl_sha256': record['crl_sha256'],
            'existing_listener_preserved': True})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--phase', required=True, choices=[
        'install-root-consumers', 'finish-root-consumers', 'activate-root'])
    parser.add_argument('--authority', required=True)
    parser.add_argument('--image')
    parser.add_argument('--failed')
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    m.require(args.phase != 'finish-root-consumers' or args.failed,
              'failed Root consumer evidence required')
    lock = (Path(args.config_root).expanduser() /
            'dev/pki/openbao-host-rollout.lock')
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = OpenBaoHostRun(args)
    try:
        if args.phase == 'finish-root-consumers':
            runner.recovery_preflight()
        else:
            runner.preflight()
        {'install-root-consumers': runner.install_root_consumers,
         'finish-root-consumers': runner.finish_root_consumers,
         'activate-root': runner.activate_root}[args.phase]()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'
        runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        os.close(owner)
        print(json.dumps({'status': runner.report['status'],
                          'report': str(runner.output / 'report.json')}),
              flush=True)


if __name__ == '__main__':
    def interrupted(*_):
        raise RuntimeError('interrupted; reconcile saved OpenBao host evidence')
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
