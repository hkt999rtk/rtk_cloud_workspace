#!/usr/bin/env python3
"""Restore the final Dev Service issuer signer policies for CertIssuer.

The final authority is already active.  This repair does not create, activate,
or retire an issuer; it only restores the two least-privilege provider policies
that let CertIssuer sign the approved server and Service-client profiles.
"""
import argparse
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import re
import sys
import time

spec = importlib.util.spec_from_file_location(
    'final_authority', Path(__file__).with_name('service_successor_final_authority.py'))
f = importlib.util.module_from_spec(spec)
spec.loader.exec_module(f)
m, NS = f.m, f.r.NS

FINAL_ISSUER = 'd61845ca-6b85-4f11-920b-f2f9685b0c13'
FINAL_ROOT = '697e8e86-5af6-4580-8456-7f91d17634f2'
ROLE = 'certissuer-pki-dev'


class FinalSignerRepair(f.FinalAuthority):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = 'Dev final Service authority CertIssuer signer-policy repair only'
        self.report['service_successor_final_authority_signer_repair_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def api(self, path, body=None, *args, **kwargs):
        # Controller discovery may briefly return 503 while its Service listener
        # reconnects.  Retry reads only; a mutation is never replayed here.
        if body is not None:
            return super().api(path, body, *args, **kwargs)
        for attempt in range(3):
            try:
                return super().api(path, None, *args, **kwargs)
            except RuntimeError as error:
                if 'status 503' not in str(error) or attempt == 2:
                    raise
                time.sleep(attempt + 1)

    def issuer(self):
        issuer = self.api('/issuers/' + FINAL_ISSUER)
        m.require(issuer['environment'] == 'dev' and issuer['trust_domain'] == 'service'
                  and issuer['kind'] == 'intermediate' and issuer['status'] == 'active'
                  and issuer['parent_issuer_id'] == FINAL_ROOT
                  and issuer['service_client_ids'] == f.CLIENT_IDS
                  and issuer['server_dns_names'] == f.DNS_NAMES,
                  'final Service issuer scope changed')
        m.require(re.fullmatch(r'pki-issuers/service/[0-9a-f-]{36}/v[0-9]+',
                               issuer.get('signer_reference', '')),
                  'final Service signer mount changed')
        return issuer

    def preflight_repair(self):
        m.require(self.kube(['config', 'current-context']).strip() == self.context,
                  'canonical Dev context mismatch')
        self.forward('am', 'video-cloud-dev-account-manager', 'account-manager', 80)
        self.accounts = m.read(self.foundation / 'accounts.json')
        for name in ('pki-controller', 'certissuer'):
            deployment = self.obj('deployment', name)
            m.require(deployment['spec'].get('replicas') == 1
                      and deployment.get('status', {}).get('readyReplicas') == 1
                      and deployment.get('status', {}).get('updatedReplicas') == 1
                      and deployment.get('status', {}).get('observedGeneration') == deployment['metadata']['generation'],
                      name + ' is not ready for signer-policy repair')
        issuer = self.issuer()
        self.save('final-issuer.json', issuer)
        return issuer

    def ensure_policy(self, name, text):
        names = json.loads(self.bao(['policy', 'list', '-format=json']))
        if name in names:
            m.require(self.bao(['policy', 'read', name]).strip() == text.strip(),
                      'existing final Service signer policy differs: ' + name)
            return False
        self.bao(['policy', 'write', name, '-'], text)
        m.require(self.bao(['policy', 'read', name]).strip() == text.strip(),
                  'written final Service signer policy differs: ' + name)
        return True

    def repair(self):
        issuer = self.preflight_repair()
        rendered = json.loads(self.kube(['-n', NS, 'exec', 'deployment/pki-controller', '--',
                                         '/app/pkicontroller', 'render-openbao-policy', FINAL_ISSUER]))
        m.require(rendered.get('mount') == issuer['signer_reference'] and rendered.get('environment') == 'dev'
                  and '/sign/server' in rendered.get('signer_policy', '')
                  and '/sign/service-client' in rendered.get('service_client_signer_policy', ''),
                  'rendered final Service signer policies differ')
        names = {
            'server': 'pki-service-server-dev-v2-' + FINAL_ISSUER,
            'client': 'pki-service-client-dev-v2-' + FINAL_ISSUER,
        }
        policies = {'server': rendered['signer_policy'], 'client': rendered['service_client_signer_policy']}
        created = {kind: self.ensure_policy(names[kind], policies[kind]) for kind in names}
        before = json.loads(self.bao(['read', '-format=json', 'auth/kubernetes/role/' + ROLE]))['data']
        m.require(before['audience'] == 'openbao' and before['token_no_default_policy']
                  and before['bound_service_account_names'] == ['certissuer-pki']
                  and before['bound_service_account_namespaces'] == [NS],
                  'CertIssuer OpenBao role binding changed')
        desired = dict(before)
        desired['token_policies'] = list(before['token_policies'])
        added = []
        for kind in ('server', 'client'):
            if names[kind] not in desired['token_policies']:
                desired['token_policies'].append(names[kind])
                added.append(names[kind])
        if added:
            m.require(json.loads(self.bao(['read', '-format=json', 'auth/kubernetes/role/' + ROLE]))['data'] == before,
                      'CertIssuer OpenBao role changed during repair')
            self.bao(['write', 'auth/kubernetes/role/' + ROLE, '-'], json.dumps(desired))
        after = json.loads(self.bao(['read', '-format=json', 'auth/kubernetes/role/' + ROLE]))['data']
        m.require(after == desired and all(name in after['token_policies'] for name in names.values()),
                  'final Service signer policies were not attached to CertIssuer')
        self.save('final-signer-policy-repair.json', {
            'issuer_id': FINAL_ISSUER, 'mount': rendered['mount'],
            'policy_names': names, 'policy_sha256': {kind: m.digest(text.encode()) for kind, text in policies.items()},
            'created_policy': created, 'attached_policy': added, 'private_keys_exported': False})
        self.verify_v2_signer(issuer)
        self.check('final_service_certissuer_signer_repaired', {
            'issuer_id': FINAL_ISSUER, 'policy_names': names,
            'server_and_service_client_capability': 'update_only',
            'private_keys_exported': False, 'staging_touched': False})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    args.phase = 'repair-final-service-signer-policy'
    args.authority = args.prepared = args.image = args.activation = args.intermediate = None
    args.resume = False
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = FinalSignerRepair(args)
    try:
        runner.repair()
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
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
