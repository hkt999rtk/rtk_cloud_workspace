#!/usr/bin/env python3
"""Prepare the Dev App Root; consumer-gated activation is a later R1 phase."""
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


class AppAuthorityRun(s.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = (
            'Dev-only App Root preparation; no consumer rollout or activation')
        self.report['app_authority_runner_sha256'] = m.digest(
            Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def app_issuers(self):
        cursor = ''
        items = []
        while True:
            page = self.api('/issuers/search', {'limit': 100, 'before': cursor})
            items += [item for item in page['items']
                      if item['trust_domain'] == 'app']
            cursor = page.get('next', '')
            if not cursor:
                return items

    def prepare_root(self):
        existing = self.app_issuers()
        if self.args.successor:
            roots = [item for item in existing if item['kind'] == 'root'
                     and item['status'] in ('active', 'retiring')]
            m.require(len(roots) == 1,
                      'successor App Root requires exactly one current Root')
            self.save('predecessor-root.json', roots[0])
        else:
            m.require(not existing,
                      'App authority already exists; use --successor or reconcile')
        operation = self.api(
            '/operations',
            {'environment': 'dev', 'trust_domain': 'app', 'kind': 'root'},
            key='dev-app-root-' + uuid.uuid4().hex)
        self.save('root-operation.json', operation)
        self.approval(operation)
        issuer = self.api('/issuers/' + operation['issuer_id'])
        m.require(issuer['status'] == 'approved'
                  and issuer['signer_provider'] == 'offline'
                  and issuer['trust_domain'] == 'app',
                  'App Root reservation differs')
        self.save('root-approved.json', issuer)

        passfile = (self.base / 'pki/rehearsal-passphrases'
                    / ('app-root-' + issuer['issuer_id']))
        m.require(not passfile.exists(), 'App Root passphrase already exists')
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
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['csr_pem'] == csr
                  and issuer['status'] == 'provisioning',
                  'App Root CSR differs')
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
            'certificate_pem':
                (self.output / 'root-signed/certificate.pem').read_text()}, 204)
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['status'] == 'ready'
                  and issuer['trust_domain'] == 'app',
                  'App Root import differs')
        self.save('root-ready.json', issuer)
        gate_status = 409 if self.args.successor else 403
        self.api('/operations/' + operation['operation_id'] + '/activate', {},
                 gate_status)
        self.check('app_root_ready_gate_closed', {
            'issuer_id': issuer['issuer_id'],
            'activation_without_bundle_receipts': gate_status,
            'successor': self.args.successor,
            'key_custody': ('encrypted offline Dev simulation; distinct software '
                            'approval accounts')})

    def reconcile(self, failed):
        source = Path(failed).resolve()
        report = m.read(source / 'report.json')
        m.require(report['status'] == 'failed'
                  and report.get('phase') in (
                      'prepare-app-root', 'prepare-app-root-successor'),
                  'failed App Root preparation evidence required')
        saved = m.read(source / 'root-ready.json')
        operation = m.read(source / 'root-operation.json')
        current = self.api('/issuers/' + saved['issuer_id'])
        m.require(current == saved and current['status'] == 'ready'
                  and current['trust_domain'] == 'app'
                  and operation['issuer_id'] == current['issuer_id'],
                  'saved ready App Root changed; do not recreate it')
        successor = report.get('phase') == 'prepare-app-root-successor'
        gate_status = 409 if successor else 403
        self.api('/operations/' + operation['operation_id'] + '/activate', {},
                 gate_status)
        self.save('root-ready.json', current)
        self.save('root-operation.json', operation)
        self.save('reconciled-from.json', {'report': str(source / 'report.json')})
        self.check('app_root_ready_gate_closed', {
            'issuer_id': current['issuer_id'],
            'activation_without_bundle_receipts': gate_status,
            'successor': successor,
            'reused_saved_root': True})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--reconcile')
    parser.add_argument('--successor', action='store_true')
    args = parser.parse_args()
    m.require(not args.reconcile or not args.successor,
              '--successor cannot be combined with --reconcile')
    args.phase = ('prepare-app-root-successor' if args.successor
                  else 'prepare-app-root')
    lock = (Path(args.config_root).expanduser()
            / 'dev/pki/app-root-rollout.lock')
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = AppAuthorityRun(args)
    try:
        runner.preflight()
        if args.reconcile:
            runner.reconcile(args.reconcile)
        else:
            runner.prepare_root()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'
        runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        os.close(owner)
        print(json.dumps({
            'status': runner.report['status'],
            'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    def interrupted(*_):
        raise RuntimeError(
            'interrupted; reconcile saved App Root evidence before retry')
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
