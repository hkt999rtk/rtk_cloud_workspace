#!/usr/bin/env python3
"""Activate and extend the governed Dev App PKI hierarchy."""
import argparse
import base64
import copy
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
s = importlib.util.module_from_spec(spec)
spec.loader.exec_module(s)
m, NS = s.m, s.NS

CONSUMERS = ['pkibroker', 'pkiturn', 'video-cloud-api-app']
DEPLOYMENTS = ['video-cloud-api-app-pki', 'mqtt-pki', 'pkiturn']


def app_manifest(authorities):
    roots = [item for item in authorities if item['kind'] == 'root']
    root_ids = {item['issuer_id'] for item in roots}
    m.require(roots and len(root_ids) == len(roots),
              'App manifest requires unique Roots')
    m.require(all(item['environment'] == 'dev'
                  and item['trust_domain'] == 'app'
                  and ((item['kind'] == 'root'
                        and not item.get('parent_issuer_id'))
                       or (item['kind'] == 'intermediate'
                           and item['parent_issuer_id'] in root_ids))
                  for item in authorities), 'App manifest lineage differs')
    return {
        'roots.pem': ''.join(item['certificate_pem'].rstrip() + '\n'
                             for item in roots),
        'issuers.json': json.dumps([{
            'issuer_id': item['issuer_id'],
            'trust_bundle_version': item['trust_bundle_version']}
            for item in authorities]),
        'crls.json': json.dumps([{
            'issuer': item,
            'state_path': '/run/pki-state/app/' + item['issuer_id'] + '-crl.json'}
            for item in authorities])}


class AppHierarchy(s.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = (
            'Dev-only governed App Root and intermediate hierarchy')
        self.report['app_hierarchy_runner_sha256'] = m.digest(
            Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def preflight_app(self):
        self.preflight()
        for name in DEPLOYMENTS:
            owner = self.obj('deployment', name)
            m.require(owner.get('status', {}).get('readyReplicas') ==
                      owner['spec']['replicas'] == 1,
                      'App consumer is not ready: ' + name)
        controller = self.obj('deployment', 'pki-controller')
        env = {entry['name']: entry.get('value', '')
               for container in controller['spec']['template']['spec']['containers']
               for entry in container.get('env', [])}
        required = ','.join(sorted(filter(None, env.get(
            'PKI_REQUIRED_CONSUMERS_APP', '').split(','))))
        bundles = ','.join(sorted(filter(None, env.get(
            'PKI_REQUIRED_BUNDLE_CONSUMERS_APP', '').split(','))))
        expected = ','.join(CONSUMERS)
        m.require(required == expected and bundles == expected,
                  'App consumer policy differs')
        self.check('app_preflight', {
            'consumer_deployments': DEPLOYMENTS,
            'required_consumers': CONSUMERS,
            'staging_touched': False})

    def root(self, *statuses):
        source = Path(self.args.authority)
        saved = m.read(source / 'root-ready.json')
        root = self.api('/issuers/' + saved['issuer_id'])
        m.require(root['environment'] == 'dev'
                  and root['trust_domain'] == 'app'
                  and root['kind'] == 'root'
                  and root['status'] in statuses
                  and root['certificate_fingerprint_sha256'] ==
                  saved['certificate_fingerprint_sha256']
                  and root['trust_bundle_version'] ==
                  saved['trust_bundle_version'],
                  'reviewed App Root changed')
        return root

    def root_operation(self):
        operation = m.read(Path(self.args.authority) / 'root-operation.json')
        m.require(operation['issuer_id'] == self.root('ready', 'active')['issuer_id'],
                  'App Root operation differs')
        return operation

    def crl_count(self, issuer_id):
        m.require(str(uuid.UUID(issuer_id)) == issuer_id,
                  'invalid App issuer identifier')
        return self.kube([
            '-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0',
            '--', 'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres',
            '-d', 'video_cloud', '-At'],
            "SELECT count(*) FROM pki_crls WHERE issuer_id='" +
            issuer_id + "';").strip()

    def restart_consumers(self, marker):
        restarted = []
        for name in DEPLOYMENTS:
            owner = self.obj('deployment', name)
            annotations = copy.deepcopy(owner['spec']['template']['metadata'].get(
                'annotations', {}))
            annotations['rtk.realtek.com/app-trust-revision'] = marker
            path = '/spec/template/metadata/annotations'
            op = 'replace' if 'annotations' in owner['spec']['template']['metadata'] else 'add'
            self.observed_patch('deployment', name, owner, [{
                'op': op, 'path': path, 'value': annotations}])
            self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + name,
                       '--timeout=300s'], timeout=310)
            current = self.obj('deployment', name)
            m.require(current.get('status', {}).get('readyReplicas') == 1,
                      'App consumer did not restart: ' + name)
            restarted.append(name)
        return restarted

    def install_manifest(self, authorities, marker):
        desired = app_manifest(authorities)
        current = self.obj('configmap', 'pki-app-trust')
        root_trust = current.get('data', {}).get('root-trust.json', '')
        m.require(root_trust, 'persisted App Root policy seed is missing')
        desired['root-trust.json'] = root_trust
        if current['data'] != desired:
            self.observed_patch('configmap', 'pki-app-trust', current, [{
                'op': 'replace', 'path': '/data', 'value': desired}])
            m.write(self.base / 'pki/controller-bootstrap/rollout' /
                    'pki-app-trust-configmap.json', {
                        'apiVersion': 'v1', 'kind': 'ConfigMap',
                        'metadata': {'name': 'pki-app-trust', 'namespace': NS},
                        'data': desired})
        return self.restart_consumers(marker)

    def sign_initial_root_crl(self, root):
        if self.crl_count(root['issuer_id']) == '1':
            return self.api('/issuers/' + root['issuer_id'] + '/crl')
        m.require(self.crl_count(root['issuer_id']) == '0',
                  'unexpected App Root CRL history')
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
        source = Path(self.args.authority)
        self.ceremony_call([
            'crl', '--issuer', self.output / 'root-active.json',
            '--crl-request', self.output / 'root-crl-request.json',
            '--expected-request-sha256', expected,
            '--key', source / 'root-offline-simulation/ca-key.encrypted.pem',
            '--passphrase-file', m.read(
                source / 'passphrase-reference.json')['path'],
            '--out', self.output / 'root-crl'])
        raw = (self.output / 'root-crl/revocations.pem').read_text()
        record = self.api('/issuers/' + root['issuer_id'] + '/crl', {
            'crl_pem': raw})
        manifest = m.read(self.output / 'root-crl/public-manifest.json')
        m.require(record['crl_sha256'] == manifest['crl_sha256'],
                  'imported App Root CRL differs')
        return record

    def activate_root(self):
        root = self.root('ready', 'active')
        operation = self.root_operation()
        receipts = self.wait_receipts(
            root['issuer_id'], root['trust_bundle_version'], CONSUMERS)
        if root['status'] == 'ready':
            self.api('/operations/' + operation['operation_id'] + '/activate',
                     {}, 204)
            root = self.root('active')
        self.save('root-active.json', root)
        crl = self.sign_initial_root_crl(root)
        self.save('root-crl.json', crl)
        restarted = self.install_manifest(
            [root], 'root-' + root['trust_bundle_version'][:16])
        crl_receipts = self.wait_receipts(
            root['issuer_id'], crl['crl_sha256'], CONSUMERS, kind='crl')
        self.check('app_root_active_with_crl', {
            'issuer_id': root['issuer_id'], 'bundle_receipts': receipts,
            'crl_sha256': crl['crl_sha256'],
            'crl_receipts': crl_receipts,
            'restarted_consumers': restarted})
        self.device_baseline()

    def active_root(self):
        root = self.root('active')
        crl = self.api('/issuers/' + root['issuer_id'] + '/crl')
        m.require(dt.datetime.fromisoformat(
            crl['next_update'].replace('Z', '+00:00')) >
            dt.datetime.now(dt.timezone.utc) + dt.timedelta(hours=1),
            'App Root CRL needs refresh')
        self.save('root-active.json', root)
        self.save('root-crl.json', crl)
        return root

    def verify_intermediate_custody(self, issuer):
        keys = json.loads(self.bao([
            'list', '-format=json', issuer['signer_reference'] + '/keys']))
        m.require(len(keys) == 1,
                  'App intermediate must own exactly one internal key')
        jwt = self.kube([
            '-n', NS, 'create', 'token', 'pki-controller',
            '--audience=openbao', '--duration=10m']).strip()
        login = json.loads(self.bao([
            'write', '-format=json', 'auth/kubernetes/login', '-'],
            json.dumps({'role': 'pki-controller-dev', 'jwt': jwt})))
        token = login['auth']['client_token']
        try:
            for suffix in ('keys', 'key/' + keys[0], 'sign/app',
                           'sign/default', 'sign-verbatim/default',
                           'intermediate/generate/exported'):
                result = json.loads(self.bao([
                    'write', '-format=json', 'sys/capabilities', '-'],
                    json.dumps({'token': token, 'paths': [
                        issuer['signer_reference'] + '/' + suffix]})))
                expected = ['list'] if suffix == 'keys' else ['deny']
                m.require(result['data']['capabilities'] == expected,
                          'controller App signing/key capability broadened')
        finally:
            self.bao(['write', 'auth/token/revoke', '-'],
                     json.dumps({'token': token}))

    def app_intermediates(self):
        cursor, items = '', []
        while True:
            page = self.api('/issuers/search', {
                'limit': 100, 'before': cursor})
            items += [item for item in page['items']
                      if item['trust_domain'] == 'app'
                      and item['kind'] == 'intermediate']
            cursor = page.get('next', '')
            if not cursor:
                return items

    def prepare_intermediate(self):
        root = self.active_root()
        m.require(not self.app_intermediates(),
                  'App intermediate already exists; reconcile saved evidence')
        request = {'environment': 'dev', 'trust_domain': 'app',
                   'kind': 'intermediate',
                   'parent_issuer_id': root['issuer_id']}
        self.save('intermediate-request.json', request)
        operation = self.api('/operations', request,
                             key='dev-app-intermediate-' + uuid.uuid4().hex)
        self.save('intermediate-operation.json', operation)
        self.approval(operation)
        issuer = self.api('/issuers/' + operation['issuer_id'])
        m.require(issuer['status'] == 'approved'
                  and issuer['signer_provider'] == 'openbao'
                  and issuer['parent_issuer_id'] == root['issuer_id'],
                  'App intermediate reservation differs')
        policies = json.loads(self.kube([
            '-n', NS, 'exec', 'deployment/pki-controller', '--',
            '/app/pkicontroller', 'render-openbao-policy',
            issuer['issuer_id']]))
        m.require(policies['mount'] == issuer['signer_reference']
                  and policies['environment'] == 'dev'
                  and '/sign/app' in policies['signer_policy'],
                  'App provider policy differs')
        self.save('provider-policies.json', policies)
        self.role_policy('pki-controller-dev',
                         'pki-controller-dev-' + issuer['issuer_id'],
                         policies['controller_policy'])
        self.api('/operations/' + operation['operation_id'] + '/provision', {})
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['status'] == 'provisioning' and issuer['csr_pem'],
                  'App intermediate CSR not registered')
        self.save('intermediate-provisioning.json', issuer)
        self.verify_intermediate_custody(issuer)
        csr_hash = m.digest(base64.b64decode(
            ''.join(issuer['csr_pem'].splitlines()[1:-1])))
        source = Path(self.args.authority)
        self.ceremony_call([
            'sign', '--issuer', self.output / 'intermediate-provisioning.json',
            '--expected-request-sha256', operation['request_sha256'],
            '--expected-csr-sha256', csr_hash,
            '--parent', self.output / 'root-active.json',
            '--expected-parent-sha256',
            root['certificate_fingerprint_sha256'],
            '--key', source / 'root-offline-simulation/ca-key.encrypted.pem',
            '--passphrase-file', m.read(
                source / 'passphrase-reference.json')['path'],
            '--out', self.output / 'intermediate-signed'])
        self.api('/operations/' + operation['operation_id'] + '/import', {
            'certificate_pem': (self.output /
                                'intermediate-signed/certificate.pem').read_text()},
                 204)
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['status'] == 'ready'
                  and issuer['parent_issuer_id'] == root['issuer_id'],
                  'App intermediate did not become ready')
        self.save('intermediate-ready.json', issuer)
        self.api('/operations/' + operation['operation_id'] + '/activate', {},
                 409)
        self.check('app_intermediate_ready_gate_closed', {
            'issuer_id': issuer['issuer_id'],
            'activation_without_bundle_receipts_denied': True,
            'private_key_exported': False})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--phase', required=True,
                        choices=('activate-root', 'prepare-intermediate'))
    parser.add_argument('--authority', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    lock = (Path(args.config_root).expanduser() /
            'dev/pki/app-hierarchy-rollout.lock')
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = AppHierarchy(args)
    try:
        runner.preflight_app()
        {'activate-root': runner.activate_root,
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
        print(json.dumps({'status': runner.report['status'],
                          'report': str(runner.output / 'report.json')}),
              flush=True)


if __name__ == '__main__':
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(
        RuntimeError('interrupted; reconcile App hierarchy before retry')))
    signal.signal(signal.SIGINT, lambda *_: (_ for _ in ()).throw(
        RuntimeError('interrupted; reconcile App hierarchy before retry')))
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
