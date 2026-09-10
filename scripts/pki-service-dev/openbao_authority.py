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
OPENBAO_NAMESPACE = 'video-cloud-dev-secrets'
OPENBAO_CLIENT_IDS = ['service:openbao']
OPENBAO_DNS_NAMES = [
    'openbao.' + OPENBAO_NAMESPACE + '.svc',
    'openbao.' + OPENBAO_NAMESPACE + '.svc.cluster.local']


def validate_root(issuer, status):
    m.require(issuer['environment'] == 'dev'
              and issuer['trust_domain'] == DOMAIN
              and issuer['kind'] == 'root'
              and issuer['status'] == status,
              'independent dev OpenBao TLS Root differs')


def intermediate_request(root):
    validate_root(root, 'active')
    return {'environment': 'dev', 'trust_domain': DOMAIN,
            'kind': 'intermediate', 'parent_issuer_id': root['issuer_id'],
            'service_client_ids': list(OPENBAO_CLIENT_IDS),
            'server_dns_names': list(OPENBAO_DNS_NAMES)}


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

    def active_root(self):
        source = Path(self.args.root)
        saved = m.read(source / 'root-ready.json')
        root = self.api('/issuers/' + saved['issuer_id'])
        validate_root(root, 'active')
        m.require(root['certificate_fingerprint_sha256'] ==
                  saved['certificate_fingerprint_sha256']
                  and root['trust_bundle_version'] ==
                  saved['trust_bundle_version'],
                  'active OpenBao TLS Root changed')
        crl = self.api('/issuers/' + root['issuer_id'] + '/crl')
        self.save('openbao-tls-root.json', root)
        self.save('openbao-tls-root-crl.json', crl)
        return root

    def verify_intermediate_custody(self, issuer):
        keys = json.loads(self.bao([
            'list', '-format=json', issuer['signer_reference'] + '/keys']))
        m.require(len(keys) == 1,
                  'OpenBao TLS intermediate must own exactly one internal key')
        jwt = self.kube([
            '-n', s.NS, 'create', 'token', 'pki-controller',
            '--audience=openbao', '--duration=10m']).strip()
        login = json.loads(self.bao([
            'write', '-format=json', 'auth/kubernetes/login', '-'],
            json.dumps({'role': 'pki-controller-dev', 'jwt': jwt})))
        token = login['auth']['client_token']
        try:
            for suffix in ('keys', 'key/' + keys[0], 'sign/server',
                           'sign/service-client', 'sign/default',
                           'sign-verbatim/default',
                           'intermediate/generate/exported'):
                result = json.loads(self.bao([
                    'write', '-format=json', 'sys/capabilities', '-'],
                    json.dumps({'token': token, 'paths': [
                        issuer['signer_reference'] + '/' + suffix]})))
                expected = ['list'] if suffix == 'keys' else ['deny']
                m.require(result['data']['capabilities'] == expected,
                          'controller intermediate capability broadened')
        finally:
            self.bao(['write', 'auth/token/revoke', '-'],
                     json.dumps({'token': token}))

    def prepare_intermediate(self):
        root = self.active_root()
        for name in ('openbao', 'openbao-internal'):
            service = self.obj('service', name, OPENBAO_NAMESPACE)
            m.require(service['metadata']['namespace'] == OPENBAO_NAMESPACE
                      and service['spec']['selector'].get(
                          'app.kubernetes.io/name') == 'openbao',
                      'OpenBao Service ownership changed')
        cursor = ''
        while True:
            page = self.api('/issuers/search', {
                'limit': 100, 'before': cursor})
            m.require(not any(item['trust_domain'] == DOMAIN
                              and item['kind'] == 'intermediate'
                              for item in page['items']),
                      'OpenBao TLS intermediate already exists; reconcile it')
            cursor = page.get('next', '')
            if not cursor:
                break
        request = intermediate_request(root)
        self.save('intermediate-request.json', request)
        operation = self.api('/operations', request,
                             key='dev-openbao-tls-intermediate-' +
                             uuid.uuid4().hex)
        self.save('intermediate-operation.json', operation)
        self.approval(operation)
        issuer = self.api('/issuers/' + operation['issuer_id'])
        m.require(issuer['status'] == 'approved'
                  and issuer['signer_provider'] == 'openbao'
                  and issuer['service_client_ids'] == OPENBAO_CLIENT_IDS
                  and issuer['server_dns_names'] == OPENBAO_DNS_NAMES,
                  'OpenBao TLS intermediate reservation differs')
        policies = json.loads(self.kube([
            '-n', s.NS, 'exec', 'deployment/pki-controller', '--',
            '/app/pkicontroller', 'render-openbao-policy',
            issuer['issuer_id']]))
        m.require(policies['mount'] == issuer['signer_reference']
                  and policies['environment'] == 'dev',
                  'OpenBao TLS provider policy scope differs')
        self.save('provider-policies.json', policies)
        self.role_policy('pki-controller-dev',
                         'pki-controller-dev-' + issuer['issuer_id'],
                         policies['controller_policy'])
        self.api('/operations/' + operation['operation_id'] + '/provision', {})
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['status'] == 'provisioning' and issuer['csr_pem'],
                  'OpenBao TLS intermediate CSR was not registered')
        self.save('intermediate-provisioning.json', issuer)
        self.verify_intermediate_custody(issuer)
        csr_hash = m.digest(base64.b64decode(
            ''.join(issuer['csr_pem'].splitlines()[1:-1])))
        source = Path(self.args.root)
        self.ceremony_call([
            'sign', '--issuer', self.output / 'intermediate-provisioning.json',
            '--expected-request-sha256', operation['request_sha256'],
            '--expected-csr-sha256', csr_hash,
            '--parent', self.output / 'openbao-tls-root.json',
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
        m.require(issuer['status'] == 'ready',
                  'OpenBao TLS intermediate did not become ready')
        self.save('intermediate-ready.json', issuer)
        self.api('/operations/' + operation['operation_id'] + '/activate', {},
                 409)
        self.check('openbao_tls_intermediate_ready_gate_closed', {
            'issuer_id': issuer['issuer_id'],
            'service_client_ids': issuer['service_client_ids'],
            'server_dns_names': issuer['server_dns_names'],
            'internal_key_count': 1,
            'activation_without_consumer_receipts_denied': True})

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
    parser.add_argument('--phase', choices=['prepare-root',
                                           'prepare-intermediate'],
                        default='prepare-root')
    parser.add_argument('--root')
    parser.add_argument('--reconcile')
    args = parser.parse_args()
    lock = (Path(args.config_root).expanduser() /
            'dev/pki/openbao-host-rollout.lock')
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    m.require(args.phase != 'prepare-intermediate' or args.root,
              'active OpenBao TLS Root evidence required')
    args.phase = ('prepare-openbao-tls-root' if args.phase == 'prepare-root'
                  else 'prepare-openbao-tls-intermediate')
    runner = OpenBaoAuthorityRun(args)
    try:
        runner.preflight()
        if args.reconcile:
            runner.reconcile(args.reconcile)
        elif args.phase == 'prepare-openbao-tls-intermediate':
            runner.prepare_intermediate()
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
