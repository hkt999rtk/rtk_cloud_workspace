#!/usr/bin/env python3
"""Prepare or reconcile the Dev Service Root successor without activating it."""
import argparse
import base64
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

spec = importlib.util.spec_from_file_location('service_hierarchy', Path(__file__).with_name('run.py'))
s = importlib.util.module_from_spec(spec)
spec.loader.exec_module(s)
m, NS = s.m, s.NS
AM_NS = 'video-cloud-dev-account-manager'


def current_root(items):
    roots = [item for item in items if item.get('environment') == 'dev' and
             item.get('trust_domain') == 'service' and item.get('kind') == 'root' and
             item.get('status') in ('active', 'retiring')]
    m.require(len(roots) == 1 and roots[0]['status'] == 'active',
              'Service successor requires exactly one active predecessor Root')
    return roots[0]


class ServiceRootSuccessor(s.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = 'Dev Service Root successor preparation; no activation or withdrawal'
        self.report['service_root_successor_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def preflight_successor(self):
        m.require(self.kube(['config', 'current-context']).strip() == self.context,
                  'canonical dev context mismatch')
        for name, namespace in (('pki-controller', NS), ('certissuer', NS),
                                ('factoryenroll', NS), ('video-cloud-api', NS),
                                ('account-manager', AM_NS)):
            deployment = self.obj('deployment', name, namespace)
            m.require(deployment['spec'].get('replicas') == 1 and
                      deployment.get('status', {}).get('readyReplicas') == 1 and
                      deployment.get('status', {}).get('updatedReplicas') == 1 and
                      deployment.get('status', {}).get('observedGeneration') == deployment['metadata'].get('generation'),
                      'required Dev deployment is not ready: ' + name)
        self.forward('am', AM_NS, 'account-manager', 80)
        self.accounts = m.read(self.foundation / 'accounts.json')
        result = subprocess.run(['go', 'build', '-o', str(self.ceremony), './cmd/pkiceremony'],
                                cwd=str(m.WORKSPACE / 'repos/rtk_video_cloud'),
                                env=dict(os.environ, GOWORK='off'), capture_output=True, timeout=180)
        m.require(result.returncode == 0, 'Service successor ceremony build failed')
        self.check('service_successor_preflight', {
            'deployments': ['pki-controller', 'certissuer', 'factoryenroll', 'video-cloud-api', 'account-manager'],
            'staging_touched': False})

    def service_issuers(self):
        items, cursor = [], ''
        while True:
            page = self.api('/issuers/search', {'limit': 100, 'before': cursor})
            items.extend(item for item in page['items'] if item.get('trust_domain') == 'service')
            cursor = page.get('next', '')
            if not cursor:
                return items

    def prepare(self):
        predecessor = current_root(self.service_issuers())
        self.save('predecessor-root.json', predecessor)
        operation = self.api('/operations', {
            'environment': 'dev', 'trust_domain': 'service', 'kind': 'root'},
            key='dev-service-root-successor-' + uuid.uuid4().hex)
        self.save('root-operation.json', operation)
        self.approval(operation)
        issuer = self.api('/issuers/' + operation['issuer_id'])
        m.require(issuer['status'] == 'approved' and issuer['kind'] == 'root' and
                  issuer['trust_domain'] == 'service' and issuer['signer_provider'] == 'offline',
                  'Service successor Root reservation differs')
        self.save('root-approved.json', issuer)
        passfile = self.base / 'pki/rehearsal-passphrases' / ('service-root-' + issuer['issuer_id'])
        m.require(not passfile.exists(), 'Service successor passphrase already exists')
        m.write(passfile, secrets.token_urlsafe(48))
        self.save('passphrase-reference.json', {'path': str(passfile)})
        keydir = self.output / 'root-offline-simulation'
        self.ceremony_call(['generate', '--issuer', self.output / 'root-approved.json',
                            '--passphrase-file', passfile,
                            '--expected-request-sha256', operation['request_sha256'], '--out', keydir])
        csr = (keydir / 'csr.pem').read_text()
        self.api('/operations/' + operation['operation_id'] + '/provision', {'csr_pem': csr})
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['status'] == 'provisioning' and issuer['csr_pem'] == csr,
                  'Service successor Root CSR differs')
        self.save('root-provisioning.json', issuer)
        csr_sha = m.digest(base64.b64decode(''.join(csr.splitlines()[1:-1])))
        self.ceremony_call(['sign', '--issuer', self.output / 'root-provisioning.json',
                            '--passphrase-file', passfile, '--key', keydir / 'ca-key.encrypted.pem',
                            '--expected-request-sha256', operation['request_sha256'],
                            '--expected-csr-sha256', csr_sha, '--out', self.output / 'root-signed'])
        self.api('/operations/' + operation['operation_id'] + '/import', {
            'certificate_pem': (self.output / 'root-signed/certificate.pem').read_text()}, 204)
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['status'] == 'ready' and issuer['trust_domain'] == 'service',
                  'Service successor Root import differs')
        self.save('root-ready.json', issuer)
        self.api('/operations/' + operation['operation_id'] + '/activate', {}, 409)
        self.check('service_successor_ready_gate_closed', {
            'predecessor_root_id': predecessor['issuer_id'], 'successor_root_id': issuer['issuer_id'],
            'activation_without_receipts': 409,
            'key_custody': 'encrypted offline Dev simulation; distinct software approval accounts'})

    def reconcile(self, source):
        source = Path(source).resolve()
        report, operation = m.read(source / 'report.json'), m.read(source / 'root-operation.json')
        m.require(report.get('status') == 'failed' and report.get('phase') == 'prepare-service-root-successor',
                  'failed Service successor preparation evidence required')
        predecessor = m.read(source / 'predecessor-root.json')
        current = self.api('/issuers/' + operation['issuer_id'])
        m.require(current['trust_domain'] == 'service' and current['kind'] == 'root' and
                  current['issuer_id'] == operation['issuer_id'], 'saved Service successor changed')
        if current['status'] == 'ready':
            self.api('/operations/' + operation['operation_id'] + '/activate', {}, 409)
            self.save('root-ready.json', current)
            self.save('root-operation.json', operation)
            self.check('service_successor_ready_gate_closed', {
                'successor_root_id': current['issuer_id'], 'activation_without_receipts': 409,
                'reused_saved_root': True})
            return
        m.require(current['status'] == 'approved' and not current.get('csr_pem'),
                  'only a pre-key approved Service successor may resume')
        self.save('predecessor-root.json', predecessor)
        self.save('root-operation.json', operation)
        self.save('root-approved.json', current)
        passfile = self.base / 'pki/rehearsal-passphrases' / ('service-root-' + current['issuer_id'])
        if passfile.exists():
            m.require(passfile.is_file() and not passfile.is_symlink() and
                      passfile.stat().st_mode & 0o777 == 0o600,
                      'saved Service successor passphrase differs')
        else:
            m.write(passfile, secrets.token_urlsafe(48))
        self.save('passphrase-reference.json', {'path': str(passfile)})
        keydir = self.output / 'root-offline-simulation'
        self.ceremony_call(['generate', '--issuer', self.output / 'root-approved.json',
                            '--passphrase-file', passfile,
                            '--expected-request-sha256', operation['request_sha256'], '--out', keydir])
        csr = (keydir / 'csr.pem').read_text()
        self.api('/operations/' + operation['operation_id'] + '/provision', {'csr_pem': csr})
        current = self.api('/issuers/' + current['issuer_id'])
        m.require(current['status'] == 'provisioning' and current['csr_pem'] == csr,
                  'resumed Service successor Root CSR differs')
        self.save('root-provisioning.json', current)
        csr_sha = m.digest(base64.b64decode(''.join(csr.splitlines()[1:-1])))
        self.ceremony_call(['sign', '--issuer', self.output / 'root-provisioning.json',
                            '--passphrase-file', passfile, '--key', keydir / 'ca-key.encrypted.pem',
                            '--expected-request-sha256', operation['request_sha256'],
                            '--expected-csr-sha256', csr_sha, '--out', self.output / 'root-signed'])
        self.api('/operations/' + operation['operation_id'] + '/import', {
            'certificate_pem': (self.output / 'root-signed/certificate.pem').read_text()}, 204)
        current = self.api('/issuers/' + current['issuer_id'])
        m.require(current['status'] == 'ready', 'resumed Service successor Root import differs')
        self.save('root-ready.json', current)
        self.api('/operations/' + operation['operation_id'] + '/activate', {}, 409)
        self.check('service_successor_ready_gate_closed', {
            'predecessor_root_id': predecessor['issuer_id'], 'successor_root_id': current['issuer_id'],
            'activation_without_receipts': 409, 'reused_approved_operation': True})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--reconcile')
    args = parser.parse_args()
    args.phase = 'prepare-service-root-successor'
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = ServiceRootSuccessor(args)
    try:
        runner.preflight_successor()
        runner.reconcile(args.reconcile) if args.reconcile else runner.prepare()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        os.close(fd)
        print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(RuntimeError('interrupted; reconcile saved successor evidence')))
    signal.signal(signal.SIGINT, lambda *_: (_ for _ in ()).throw(RuntimeError('interrupted; reconcile saved successor evidence')))
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
