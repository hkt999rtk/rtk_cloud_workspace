#!/usr/bin/env python3
"""Prepare the dev Service Intermediate v2 required by factory enrollment."""
import argparse
import base64
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import signal
import sys
import uuid


spec = importlib.util.spec_from_file_location('service_rollout', Path(__file__).with_name('run.py'))
r = importlib.util.module_from_spec(spec)
spec.loader.exec_module(r)
m = r.m

OLD_CLIENT_IDS = ['service:account-manager', 'service:certissuer', 'service:pki-controller']
V2_CLIENT_IDS = ['service:account-manager', 'service:certissuer',
                 'service:factory-enroll', 'service:pki-controller']


def v2_request(root):
    m.require(root['environment'] == 'dev' and root['trust_domain'] == 'service'
              and root['kind'] == 'root' and root['status'] == 'active',
              'active dev Service Root required')
    return {'environment': 'dev', 'trust_domain': 'service', 'kind': 'intermediate',
            'parent_issuer_id': root['issuer_id'], 'service_client_ids': list(V2_CLIENT_IDS),
            'server_dns_names': list(r.SERVICE_DNS_NAMES)}


def select_v1(items, root):
    candidates = [i for i in items if i['environment'] == 'dev' and i['trust_domain'] == 'service'
                  and i['kind'] == 'intermediate']
    active = [i for i in candidates if i['status'] == 'active']
    m.require(len(active) == 1, 'exactly one active Service intermediate required')
    issuer = active[0]
    m.require(issuer['parent_issuer_id'] == root['issuer_id']
              and issuer['service_client_ids'] == OLD_CLIENT_IDS
              and issuer['server_dns_names'] == r.SERVICE_DNS_NAMES,
              'active Service intermediate v1 policy differs')
    unfinished = [i for i in candidates if i['issuer_id'] != issuer['issuer_id']
                  and i['status'] not in ('retired', 'cancelled', 'revoked', 'compromised', 'failed')]
    m.require(not unfinished, 'another Service intermediate transition already exists; reconcile')
    return issuer


class FactoryAdoption(r.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['phase'] = 'prepare-intermediate-v2'
        self.report['factory_adoption_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def service_intermediates(self):
        items = []
        cursor = ''
        while True:
            page = self.api('/issuers/search', {'limit': 100, 'before': cursor})
            items.extend(page['items'])
            cursor = page.get('next', '')
            if not cursor:
                return items

    def prepare_intermediate_v2(self):
        root = self.active_root()
        old = select_v1(self.service_intermediates(), root)
        current_old = self.api('/issuers/' + old['issuer_id'])
        m.require(current_old == old, 'active Service intermediate changed during inventory')
        old_crl = self.api('/issuers/' + old['issuer_id'] + '/crl')
        self.save('intermediate-v1.json', old)
        self.save('intermediate-v1-crl.json', old_crl)
        for name in r.SERVICE_CONSUMERS:
            service = self.obj('service', name)
            m.require(service['metadata']['namespace'] == r.NS
                      and service['spec']['selector'].get('app.kubernetes.io/name') == name,
                      'Service host endpoint ownership changed')

        request = v2_request(root)
        self.save('intermediate-request.json', request)
        operation = self.api('/operations', request,
                             key='dev-service-intermediate-v2-' + uuid.uuid4().hex)
        self.save('intermediate-operation.json', operation)
        self.approval(operation)
        issuer = self.api('/issuers/' + operation['issuer_id'])
        m.require(issuer['status'] == 'approved' and issuer['signer_provider'] == 'openbao'
                  and issuer['issuer_version'] == old['issuer_version'] + 1,
                  'Service intermediate v2 reservation differs')
        m.require(issuer['signer_reference'] + '/' not in
                  json.loads(self.bao(['secrets', 'list', '-format=json'])),
                  'Service intermediate v2 provider mount already exists; reconcile')

        policies = json.loads(self.kube(['-n', r.NS, 'exec', 'deployment/pki-controller', '--',
                                         '/app/pkicontroller', 'render-openbao-policy', issuer['issuer_id']]))
        m.require(policies['mount'] == issuer['signer_reference'] and policies['environment'] == 'dev',
                  'provider policy scope differs')
        m.require('/sign/server' in policies['signer_policy']
                  and '/sign/service-client' in policies['service_client_signer_policy'],
                  'provider leaf signer policies missing')
        self.save('provider-policies.json', policies)
        before = json.loads(self.bao(['read', '-format=json',
                                      'auth/kubernetes/role/pki-controller-dev']))['data']
        self.save('controller-role-before.json', before)
        self.role_policy('pki-controller-dev',
                         'pki-controller-dev-' + issuer['issuer_id'],
                         policies['controller_policy'])
        self.api('/operations/' + operation['operation_id'] + '/provision', {})
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['status'] == 'provisioning' and issuer['csr_pem'],
                  'Service intermediate v2 CSR not durably registered')
        self.save('intermediate-provisioning.json', issuer)
        self.verify_intermediate_custody(issuer)

        source = Path(self.args.authority)
        csr_digest = m.digest(base64.b64decode(''.join(issuer['csr_pem'].splitlines()[1:-1])))
        self.ceremony_call(['sign', '--issuer', self.output / 'intermediate-provisioning.json',
                            '--expected-request-sha256', operation['request_sha256'],
                            '--expected-csr-sha256', csr_digest,
                            '--parent', self.output / 'service-root.json',
                            '--expected-parent-sha256', root['certificate_fingerprint_sha256'],
                            '--key', source / 'root-offline-simulation/ca-key.encrypted.pem',
                            '--passphrase-file', m.read(source / 'passphrase-reference.json')['path'],
                            '--out', self.output / 'intermediate-signed'])
        self.api('/operations/' + operation['operation_id'] + '/import',
                 {'certificate_pem': (self.output / 'intermediate-signed/certificate.pem').read_text()}, 204)
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['status'] == 'ready' and issuer['parent_issuer_id'] == root['issuer_id']
                  and issuer['service_client_ids'] == V2_CLIENT_IDS
                  and issuer['server_dns_names'] == r.SERVICE_DNS_NAMES,
                  'ready Service intermediate v2 differs from approved policy')
        m.require(self.api('/issuers/' + old['issuer_id']) == old,
                  'Service intermediate v1 changed before v2 trust installation')
        self.save('intermediate-ready.json', issuer)
        self.api('/operations/' + operation['operation_id'] + '/activate', {}, 409)
        self.check('intermediate_v2_ready_gate_closed', {
            'issuer_id': issuer['issuer_id'], 'issuer_version': issuer['issuer_version'],
            'activation_without_receipts': 409,
            'service_client_ids': issuer['service_client_ids'],
            'server_dns_names': issuer['server_dns_names'],
            'v1_unchanged_and_active': True,
            'private_key_exported': False,
        })


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--authority', required=True)
    args = parser.parse_args()
    args.phase, args.image, args.activation, args.intermediate = (
        'prepare-intermediate-v2', None, None, None)
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = FactoryAdoption(args)
    try:
        runner.preflight()
        runner.prepare_intermediate_v2()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'
        runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        os.close(owner)
        print(json.dumps({'phase': runner.report['phase'], 'status': runner.report['status'],
                          'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    def interrupted(*_):
        raise RuntimeError('interrupted; reconcile saved v2 authority evidence before retry')
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
