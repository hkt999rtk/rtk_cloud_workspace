#!/usr/bin/env python3
"""Prepare the independent dev OpenBao TLS Root without changing the listener."""
import argparse
import base64
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import secrets
import signal
import sys
import uuid


spec = importlib.util.spec_from_file_location(
    'service_hierarchy', Path(__file__).with_name('run.py'))
s = importlib.util.module_from_spec(spec)
spec.loader.exec_module(s)
m = s.m

DOMAIN = 'openbao_tls'


def validate_root(issuer, status):
    m.require(issuer['environment'] == 'dev'
              and issuer['trust_domain'] == DOMAIN
              and issuer['kind'] == 'root'
              and issuer['status'] == status,
              'independent dev OpenBao TLS Root differs')


class OpenBaoAuthorityRun(s.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = (
            'Dev-only independent OpenBao TLS Root preparation; '
            'no listener, provider-auth or seal change')
        self.report['openbao_authority_runner_sha256'] = m.digest(
            Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def prepare_root(self):
        cursor = ''
        while True:
            page = self.api('/issuers/search', {
                'limit': 100, 'before': cursor})
            m.require(not any(item['trust_domain'] == DOMAIN
                              for item in page['items']),
                      'OpenBao TLS authority already exists; reconcile it')
            cursor = page.get('next', '')
            if not cursor:
                break
        operation = self.api('/operations', {
            'environment': 'dev', 'trust_domain': DOMAIN, 'kind': 'root'},
            key='dev-openbao-tls-root-' + uuid.uuid4().hex)
        self.save('root-operation.json', operation)
        self.approval(operation)
        issuer = self.api('/issuers/' + operation['issuer_id'])
        validate_root(issuer, 'approved')
        m.require(issuer['signer_provider'] == 'offline',
                  'OpenBao TLS Root must remain offline')
        self.save('root-approved.json', issuer)

        passfile = (self.base / 'pki/rehearsal-passphrases' /
                    ('openbao-tls-root-' + issuer['issuer_id']))
        m.require(not passfile.exists(),
                  'OpenBao TLS Root passphrase already exists')
        m.write(passfile, secrets.token_urlsafe(48))
        self.save('passphrase-reference.json', {'path': str(passfile)})
        keydir = self.output / 'root-offline-simulation'
        self.ceremony_call([
            'generate', '--issuer', self.output / 'root-approved.json',
            '--passphrase-file', passfile,
            '--expected-request-sha256', operation['request_sha256'],
            '--out', keydir])
        csr = (keydir / 'csr.pem').read_text()
        self.api('/operations/' + operation['operation_id'] + '/provision', {
            'csr_pem': csr})
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        validate_root(issuer, 'provisioning')
        m.require(issuer['csr_pem'] == csr,
                  'OpenBao TLS Root CSR differs')
        self.save('root-provisioning.json', issuer)
        csr_hash = m.digest(base64.b64decode(
            ''.join(csr.splitlines()[1:-1])))
        self.ceremony_call([
            'sign', '--issuer', self.output / 'root-provisioning.json',
            '--passphrase-file', passfile,
            '--key', keydir / 'ca-key.encrypted.pem',
            '--expected-request-sha256', operation['request_sha256'],
            '--expected-csr-sha256', csr_hash,
            '--out', self.output / 'root-signed'])
        self.api('/operations/' + operation['operation_id'] + '/import', {
            'certificate_pem': (self.output /
                                'root-signed/certificate.pem').read_text()}, 204)
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        validate_root(issuer, 'ready')
        self.save('root-ready.json', issuer)
        self.check('openbao_tls_root_ready_gate_closed', {
            'issuer_id': issuer['issuer_id'],
            'activation': 'deferred until actual provider clients install it',
            'key_custody': ('encrypted offline dev simulation; distinct '
                            'software approval accounts')})

    def reconcile(self, failed):
        failed = Path(failed)
        report = m.read(failed / 'report.json')
        m.require(report['status'] == 'failed'
                  and report.get('phase') == 'prepare-openbao-tls-root',
                  'failed OpenBao TLS Root preparation evidence required')
        operation = m.read(failed / 'root-operation.json')
        saved = m.read(failed / 'root-ready.json')
        current = self.api('/issuers/' + operation['issuer_id'])
        validate_root(current, 'ready')
        m.require(saved['issuer_id'] == operation['issuer_id']
                  and current == saved,
                  'saved OpenBao TLS Root changed; do not recreate it')
        self.save('reconciled-from.json', {
            'report': str(failed / 'report.json'),
            'issuer_id': current['issuer_id']})
        self.save('root-ready.json', current)
        self.check('openbao_tls_root_ready_gate_closed', {
            'issuer_id': current['issuer_id'],
            'root_status': current['status'],
            'key_custody': 'retained encrypted offline dev simulation'})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--reconcile')
    args = parser.parse_args()
    lock = (Path(args.config_root).expanduser() /
            'dev/pki/openbao-host-rollout.lock')
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    args.phase = 'prepare-openbao-tls-root'
    runner = OpenBaoAuthorityRun(args)
    try:
        runner.preflight()
        runner.reconcile(args.reconcile) if args.reconcile else runner.prepare_root()
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
        raise RuntimeError(
            'interrupted; reconcile saved OpenBao TLS Root evidence before retry')
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
