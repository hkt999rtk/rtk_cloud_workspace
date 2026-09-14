#!/usr/bin/env python3
"""Prepare or reconcile an inactive Dev OpenBao TLS Root successor."""
import argparse
import base64
import datetime as dt
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import secrets
import signal
import subprocess
import sys
import uuid


spec = importlib.util.spec_from_file_location(
    'service_hierarchy', Path(__file__).with_name('run.py'))
s = importlib.util.module_from_spec(spec)
spec.loader.exec_module(s)
m, NS = s.m, s.NS
DOMAIN = 'openbao_tls'
CONSUMERS = ('certissuer', 'pki-controller')


def valid_root(root, status):
    m.require(root.get('environment') == 'dev'
              and root.get('trust_domain') == DOMAIN
              and root.get('kind') == 'root'
              and root.get('status') == status,
              'OpenBao TLS Root scope or status differs')


class OpenBaoRootSuccessor(s.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = (
            'Dev-only OpenBao TLS Root successor preparation; no activation, '
            'provider credential, seal, or predecessor withdrawal')
        self.report['openbao_root_successor_runner_sha256'] = m.digest(
            Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def preflight_successor(self):
        m.require(self.kube(['config', 'current-context']).strip() == self.context,
                  'canonical dev context mismatch')
        m.require(self.obj('namespace', NS)['metadata']['name'] == NS,
                  'wrong provider consumer namespace')
        workloads = {}
        for name in CONSUMERS:
            deployment = self.obj('deployment', name)
            m.require(deployment['spec'].get('replicas') == 1
                      and deployment.get('status', {}).get('readyReplicas') == 1
                      and deployment.get('status', {}).get('updatedReplicas') == 1
                      and deployment.get('status', {}).get('observedGeneration') ==
                      deployment['metadata'].get('generation'),
                      'provider consumer is not ready: ' + name)
            containers = deployment['spec']['template']['spec'].get('containers', [])
            m.require(len(containers) == 1 and containers[0]['name'] == name
                      and '@sha256:' in containers[0].get('image', ''),
                      'provider consumer image ownership changed: ' + name)
            workloads[name] = containers[0]['image']
        self.forward('am', 'video-cloud-dev-account-manager', 'account-manager', 80)
        self.accounts = m.read(self.foundation / 'accounts.json')
        self.api('/issuers/search', {'limit': 1}, role='requester')
        result = subprocess.run(
            ['go', 'build', '-o', str(self.ceremony), './cmd/pkiceremony'],
            cwd=str(m.WORKSPACE / 'repos/rtk_video_cloud'),
            env=dict(os.environ, GOWORK='off'), capture_output=True, timeout=180)
        m.require(result.returncode == 0,
                  'OpenBao TLS successor ceremony build failed')
        self.check('openbao_tls_successor_preflight', {
            'consumers': workloads, 'staging_touched': False})

    def roots(self):
        items, cursor = [], ''
        while True:
            page = self.api('/issuers/search', {'limit': 100, 'before': cursor})
            for item in page['items']:
                if item.get('trust_domain') == DOMAIN and item.get('kind') == 'root':
                    items.append(self.api('/issuers/' + item['issuer_id']))
            cursor = page.get('next', '')
            if not cursor:
                return items

    def predecessor(self):
        active = [root for root in self.roots() if root.get('status') == 'active']
        m.require(len(active) == 1, 'exact active OpenBao TLS predecessor required')
        valid_root(active[0], 'active')
        return active[0]

    def prepare(self):
        predecessor = self.predecessor()
        m.require(not any(root.get('status') == 'ready' for root in self.roots()),
                  'ready OpenBao TLS successor already exists; reconcile it')
        self.save('predecessor-root.json', predecessor)
        operation = self.api('/operations', {
            'environment': 'dev', 'trust_domain': DOMAIN, 'kind': 'root'},
            key='dev-openbao-tls-root-successor-' + uuid.uuid4().hex)
        self.save('root-operation.json', operation)
        self.approval(operation)
        successor = self.api('/issuers/' + operation['issuer_id'])
        valid_root(successor, 'approved')
        m.require(successor.get('signer_provider') == 'offline',
                  'OpenBao TLS successor Root must remain offline')
        self.save('root-approved.json', successor)
        passfile = self.base / 'pki/rehearsal-passphrases' / (
            'openbao-tls-root-' + successor['issuer_id'])
        m.require(not passfile.exists(),
                  'OpenBao TLS successor passphrase already exists')
        m.write(passfile, secrets.token_urlsafe(48))
        self.save('passphrase-reference.json', {'path': str(passfile)})
        keydir = self.output / 'root-offline-simulation'
        self.ceremony_call([
            'generate', '--issuer', self.output / 'root-approved.json',
            '--passphrase-file', passfile,
            '--expected-request-sha256', operation['request_sha256'],
            '--out', keydir])
        csr = (keydir / 'csr.pem').read_text()
        self.api('/operations/' + operation['operation_id'] + '/provision',
                 {'csr_pem': csr})
        successor = self.api('/issuers/' + successor['issuer_id'])
        valid_root(successor, 'provisioning')
        m.require(successor.get('csr_pem') == csr,
                  'OpenBao TLS successor Root CSR differs')
        self.save('root-provisioning.json', successor)
        csr_hash = m.digest(base64.b64decode(''.join(csr.splitlines()[1:-1])))
        self.ceremony_call([
            'sign', '--issuer', self.output / 'root-provisioning.json',
            '--passphrase-file', passfile,
            '--key', keydir / 'ca-key.encrypted.pem',
            '--expected-request-sha256', operation['request_sha256'],
            '--expected-csr-sha256', csr_hash, '--out', self.output / 'root-signed'])
        self.api('/operations/' + operation['operation_id'] + '/import', {
            'certificate_pem': (self.output / 'root-signed/certificate.pem').read_text()}, 204)
        successor = self.api('/issuers/' + successor['issuer_id'])
        valid_root(successor, 'ready')
        self.save('root-ready.json', successor)
        self.api('/operations/' + operation['operation_id'] + '/activate', {}, 409)
        self.check('openbao_tls_successor_ready_gate_closed', {
            'predecessor_root_id': predecessor['issuer_id'],
            'successor_root_id': successor['issuer_id'],
            'activation_without_transport_receipts_denied': True,
            'key_custody': 'encrypted offline Dev simulation'})

    def reconcile(self, source):
        source = Path(source).resolve()
        report = m.read(source / 'report.json')
        m.require(report.get('status') == 'failed'
                  and report.get('phase') == 'prepare-openbao-tls-root-successor',
                  'failed OpenBao TLS successor evidence required')
        operation = m.read(source / 'root-operation.json')
        predecessor = m.read(source / 'predecessor-root.json')
        valid_root(self.api('/issuers/' + predecessor['issuer_id']), 'active')
        successor = self.api('/issuers/' + operation['issuer_id'])
        valid_root(successor, 'ready')
        self.save('predecessor-root.json', predecessor)
        self.save('root-operation.json', operation)
        self.save('root-ready.json', successor)
        self.api('/operations/' + operation['operation_id'] + '/activate', {}, 409)
        self.check('openbao_tls_successor_ready_gate_recovered', {
            'predecessor_root_id': predecessor['issuer_id'],
            'successor_root_id': successor['issuer_id'],
            'reconciled_from': str(source)})

    def activate(self):
        source = Path(self.args.source)
        prior = m.read(source / 'report.json')
        bundles = m.read(Path(self.args.bundles) / 'report.json')
        m.require(prior.get('status') == 'passed'
                  and prior.get('phase') == 'prepare-openbao-tls-root-successor'
                  and bundles.get('status') == 'passed'
                  and bundles.get('checks', {}).get(
                      'openbao_successor_bundle_receipts_installed', {}).get(
                      'status') == 'passed',
                  'passed successor preparation and bundle receipts required')
        predecessor = m.read(source / 'predecessor-root.json')
        operation = m.read(source / 'root-operation.json')
        successor = m.read(source / 'root-ready.json')
        evidence = bundles['checks']['openbao_successor_bundle_receipts_installed']['evidence']
        m.require(evidence.get('predecessor_root_id') == predecessor['issuer_id']
                  and evidence.get('successor_root_id') == successor['issuer_id']
                  and set(evidence.get('receipts', [])) == set(CONSUMERS),
                  'successor bundle receipt lineage changed')
        valid_root(self.api('/issuers/' + predecessor['issuer_id']), 'active')
        current = self.api('/issuers/' + successor['issuer_id'])
        valid_root(current, 'ready')
        m.require(current['certificate_fingerprint_sha256'] ==
                  successor['certificate_fingerprint_sha256'],
                  'successor Root certificate changed')
        self.api('/operations/' + operation['operation_id'] + '/activate', {},
                 204, role='approver')
        activated = self.api('/issuers/' + successor['issuer_id'])
        retiring = self.api('/issuers/' + predecessor['issuer_id'])
        valid_root(activated, 'active')
        valid_root(retiring, 'retiring')
        self.save('predecessor-root.json', retiring)
        self.save('root-active.json', activated)
        self.check('openbao_tls_successor_activated', {
            'predecessor_root_id': predecessor['issuer_id'],
            'successor_root_id': successor['issuer_id'],
            'bundle_receipts': evidence['receipts'],
            'old_root_status': 'retiring', 'withdrawal': 'not_attempted',
            'staging_touched': False})

    def publish_initial_crl(self):
        source = Path(self.args.source)
        prior = m.read(source / 'report.json')
        m.require(prior.get('status') == 'passed'
                  and prior.get('phase') == 'prepare-openbao-tls-root-successor',
                  'passed successor preparation evidence required')
        root = m.read(source / 'root-ready.json')
        current = self.api('/issuers/' + root['issuer_id'])
        valid_root(current, 'active')
        m.require(current['certificate_fingerprint_sha256'] ==
                  root['certificate_fingerprint_sha256'],
                  'successor Root certificate changed')
        now = dt.datetime.now(dt.timezone.utc).replace(microsecond=0)
        request = {'issuer_id': current['issuer_id'],
                   'issuer_fingerprint_sha256':
                   current['certificate_fingerprint_sha256'],
                   'crl_number': '1', 'this_update': m.stamp(now),
                   'next_update': m.stamp(now + dt.timedelta(days=1)),
                   'revocations': []}
        self.save('root-crl-request.json', request)
        expected = self.ceremony_call([
            'crl-digest', self.output / 'root-crl-request.json']).strip()
        passfile = m.read(source / 'passphrase-reference.json')['path']
        self.ceremony_call([
            'crl', '--issuer', source / 'root-ready.json',
            '--crl-request', self.output / 'root-crl-request.json',
            '--expected-request-sha256', expected,
            '--key', source / 'root-offline-simulation/ca-key.encrypted.pem',
            '--passphrase-file', passfile, '--out', self.output / 'root-crl'])
        record = self.api('/issuers/' + current['issuer_id'] + '/crl', {
            'crl_pem': (self.output / 'root-crl/revocations.pem').read_text()},
            role='approver')
        m.require(record.get('issuer_id') == current['issuer_id']
                  and record.get('crl_number') == '1',
                  'initial successor Root CRL differs')
        self.save('root-crl.json', record)
        self.check('openbao_tls_successor_initial_crl_published', {
            'issuer_id': current['issuer_id'], 'crl_sha256': record['crl_sha256'],
            'revocations': 0, 'staging_touched': False})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--phase', choices=('prepare', 'activate', 'publish-root-crl'), default='prepare')
    parser.add_argument('--source')
    parser.add_argument('--bundles')
    parser.add_argument('--reconcile')
    args = parser.parse_args()
    args.phase = {'prepare': 'prepare-openbao-tls-root-successor',
                  'activate': 'activate-openbao-tls-root-successor',
                  'publish-root-crl': 'publish-openbao-tls-root-crl'}[args.phase]
    m.require(args.phase != 'activate-openbao-tls-root-successor' or
              (args.source and args.bundles),
              'successor preparation and bundle receipt evidence required')
    m.require(args.phase != 'publish-openbao-tls-root-crl' or args.source,
              'successor preparation evidence required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/openbao-host-rollout.lock'
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = OpenBaoRootSuccessor(args)
    try:
        runner.preflight_successor()
        if args.phase == 'activate-openbao-tls-root-successor':
            runner.activate()
        elif args.phase == 'publish-openbao-tls-root-crl':
            runner.publish_initial_crl()
        else:
            runner.reconcile(args.reconcile) if args.reconcile else runner.prepare()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        os.close(owner)
        print(json.dumps({'status': runner.report['status'],
                          'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(
        RuntimeError('interrupted; reconcile saved OpenBao TLS successor evidence')))
    signal.signal(signal.SIGINT, lambda *_: (_ for _ in ()).throw(
        RuntimeError('interrupted; reconcile saved OpenBao TLS successor evidence')))
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
