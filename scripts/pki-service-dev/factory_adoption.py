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


def bundle_references(root, old, new):
    return [{'issuer_id': i['issuer_id'], 'trust_bundle_version': i['trust_bundle_version']}
            for i in (root, old, new)]


def replacement_bundle_template(owner, old_name, new_name):
    template = json.loads(json.dumps(owner['spec']['template']))
    volumes = template['spec']['volumes']
    selected = [v for v in volumes if v['name'] == 'service-bundles']
    m.require(len(selected) == 1 and selected[0].get('configMap', {}).get('name') == old_name,
              'listener is not using the reviewed Service v1 bundle')
    selected[0]['configMap']['name'] = new_name
    return template


def crl_entries(*issuers):
    return [{'issuer': issuer,
             'state_path': '/var/lib/pki-host/identity/crls/' + issuer['issuer_id'] + '.json'}
            for issuer in issuers]


class FactoryAdoption(r.ServiceRun):
    def __init__(self, args):
        m.Acceptance.__init__(self, args.config_root, 'lke649805-ctx', args.output,
                              resume='ready' if args.resume else False)
        self.args = args
        self.report['foundation_scope'] = 'Existing dev Service Root/v1; factory Service client adoption'
        self.report['checks'] = {}
        self.report['phase'] = args.phase
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
        self.approve_intermediate(operation)
        issuer = self.api('/issuers/' + operation['issuer_id'])
        m.require(issuer['status'] == 'approved' and issuer['signer_provider'] == 'openbao'
                  and issuer['issuer_version'] == old['issuer_version'] + 1,
                  'Service intermediate v2 reservation differs')
        m.require(issuer['signer_reference'] + '/' not in
                  json.loads(self.bao(['secrets', 'list', '-format=json'])),
                  'Service intermediate v2 provider mount already exists; reconcile')

        self.provision_and_sign(root, old, operation, issuer)

    def approve_intermediate(self, operation):
        path = '/operations/' + operation['operation_id']
        self.api(path + '/approvals', {
            'request_sha256': operation['request_sha256'], 'role': 'pki_admin'},
            204, 'approver')
        approved = self.api(path)
        m.require(approved['status'] == 'approved'
                  and approved['operation_id'] == operation['operation_id']
                  and approved['request_sha256'] == operation['request_sha256'],
                  'independent Intermediate approval not recorded')
        return approved

    def resume_approved_intermediate(self):
        root = self.active_root()
        request = m.read(self.output / 'intermediate-request.json')
        operation = m.read(self.output / 'intermediate-operation.json')
        old = m.read(self.output / 'intermediate-v1.json')
        m.require(request == v2_request(root), 'saved Service intermediate v2 request changed')
        m.require(self.api('/issuers/' + old['issuer_id']) == old and old['status'] == 'active',
                  'Service intermediate v1 changed during recovery')
        current_operation = self.api('/operations/' + operation['operation_id'])
        m.require(current_operation['operation_id'] == operation['operation_id']
                  and current_operation['issuer_id'] == operation['issuer_id']
                  and current_operation['request_sha256'] == operation['request_sha256']
                  and current_operation['status'] == 'approved',
                  'saved approved v2 operation differs')
        issuer = self.api('/issuers/' + operation['issuer_id'])
        m.require(issuer['status'] == 'approved' and issuer['parent_issuer_id'] == root['issuer_id']
                  and issuer['service_client_ids'] == V2_CLIENT_IDS
                  and issuer['server_dns_names'] == r.SERVICE_DNS_NAMES,
                  'approved Service intermediate v2 differs during recovery')
        m.require(not (self.output / 'provider-policies.json').exists()
                  and issuer['signer_reference'] + '/' not in
                  json.loads(self.bao(['secrets', 'list', '-format=json'])),
                  'recovery is limited to the pre-provision approval failure')
        self.report['reconciled_from'] = str(self.output)
        self.provision_and_sign(root, old, current_operation, issuer)

    def provision_and_sign(self, root, old, operation, issuer):

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

    def ready_v2(self):
        root = self.active_root()
        source = Path(self.args.prepared)
        m.require(m.read(source / 'report.json')['status'] == 'passed',
                  'successful Service intermediate v2 preparation required')
        old = m.read(source / 'intermediate-v1.json')
        new = m.read(source / 'intermediate-ready.json')
        operation = m.read(source / 'intermediate-operation.json')
        m.require(self.api('/issuers/' + old['issuer_id']) == old and old['status'] == 'active',
                  'Service intermediate v1 changed before v2 activation')
        current = self.api('/issuers/' + new['issuer_id'])
        m.require(current == new and new['status'] == 'ready'
                  and new['parent_issuer_id'] == root['issuer_id']
                  and new['issuer_version'] == old['issuer_version'] + 1
                  and new['service_client_ids'] == V2_CLIENT_IDS
                  and new['server_dns_names'] == r.SERVICE_DNS_NAMES
                  and operation['issuer_id'] == new['issuer_id'],
                  'ready Service intermediate v2 evidence changed')
        return root, old, new, operation

    def old_bundle_name(self, old):
        name = 'pki-service-bundles-' + old['issuer_id'][:8]
        manifest = self.obj('configmap', name)
        expected = [{'issuer_id': i['issuer_id'], 'trust_bundle_version': i['trust_bundle_version']}
                    for i in (self.api('/issuers/' + old['parent_issuer_id']), old)]
        m.require(manifest.get('immutable') and json.loads(manifest['data']['issuers.json']) == expected,
                  'existing Service v1 bundle differs')
        return name

    def v2_bundle_name(self, new):
        return 'pki-service-bundles-v2-' + new['issuer_id'][:8]

    def install_listener_bundle(self, name, old_name, new_name):
        owner = self.obj('deployment', name)
        m.require(owner['spec']['replicas'] == 1, 'expected one dev listener replica')
        template = replacement_bundle_template(owner, old_name, new_name)
        self.observed_patch('deployment', name, owner, [
            {'op': 'test', 'path': '/spec/template/spec/volumes',
             'value': owner['spec']['template']['spec']['volumes']},
            {'op': 'replace', 'path': '/spec/template', 'value': template},
        ])
        self.kube(['-n', r.NS, 'rollout', 'status', 'deployment/' + name,
                   '--timeout=300s'], timeout=310)

    def install_controller(self):
        root, old, new, operation = self.ready_v2()
        old_name, new_name = self.old_bundle_name(old), self.v2_bundle_name(new)
        refs = bundle_references(root, old, new)
        self.create({'apiVersion': 'v1', 'kind': 'ConfigMap',
                     'metadata': {'name': new_name, 'namespace': r.NS},
                     'immutable': True, 'data': {'issuers.json': json.dumps(refs)}})
        self.install_listener_bundle('pki-controller', old_name, new_name)
        receipts = self.wait_receipts(new['issuer_id'], new['trust_bundle_version'],
                                     ['pki-controller'], absent='certissuer')
        self.api('/operations/' + operation['operation_id'] + '/activate', {}, 409)
        m.require(self.api('/issuers/' + old['issuer_id']) == old
                  and self.api('/issuers/' + new['issuer_id']) == new,
                  'authority changed during controller-only installation')
        self.device_baseline()
        self.check('intermediate_v2_controller_receipt', {
            'bundle': new_name, 'references': refs, 'consumers': receipts,
            'certissuer_absent': True, 'activation_denied': 409,
            'v1_still_active': True,
        })

    def require_v2_bundle(self, root, old, new):
        name = self.v2_bundle_name(new)
        manifest = self.obj('configmap', name)
        refs = bundle_references(root, old, new)
        m.require(manifest.get('immutable') and json.loads(manifest['data']['issuers.json']) == refs,
                  'Service v2 bundle differs')
        return name, refs

    def install_certissuer(self):
        root, old, new, operation = self.ready_v2()
        old_name = self.old_bundle_name(old)
        new_name, refs = self.require_v2_bundle(root, old, new)
        controller = self.obj('deployment', 'pki-controller')
        selected = [v for v in controller['spec']['template']['spec']['volumes']
                    if v['name'] == 'service-bundles']
        m.require(len(selected) == 1 and selected[0]['configMap']['name'] == new_name,
                  'controller has not retained the Service v2 bundle')
        self.wait_receipts(new['issuer_id'], new['trust_bundle_version'],
                           ['pki-controller'], absent='certissuer')
        self.install_listener_bundle('certissuer', old_name, new_name)
        receipts = self.wait_receipts(new['issuer_id'], new['trust_bundle_version'],
                                     r.SERVICE_CONSUMERS)

        policies = m.read(Path(self.args.prepared) / 'provider-policies.json')
        role = 'certissuer-pki-dev'
        self.save('certissuer-role-before.json',
                  json.loads(self.bao(['read', '-format=json', 'auth/kubernetes/role/' + role]))['data'])
        self.role_policy(role, 'pki-service-server-dev-v2-' + new['issuer_id'],
                         policies['signer_policy'])
        self.role_policy(role, 'pki-service-client-dev-v2-' + new['issuer_id'],
                         policies['service_client_signer_policy'])
        self.verify_v2_signer(new)
        m.require(self.api('/issuers/' + old['issuer_id']) == old
                  and self.api('/issuers/' + new['issuer_id']) == new,
                  'authority changed before reviewed activation')
        m.require(self.api('/operations/' + operation['operation_id'])['status'] == 'ready',
                  'Service intermediate v2 operation changed before activation')
        self.device_baseline()
        self.check('intermediate_v2_listener_receipts_and_signer', {
            'bundle': new_name, 'references': refs, 'consumers': receipts,
            'v2_server_and_service_client_signing_only': True,
            'activation_still_separate': True, 'v1_still_active': True,
        })

    def verify_v2_signer(self, issuer):
        keys = json.loads(self.bao(['list', '-format=json', issuer['signer_reference'] + '/keys']))
        m.require(len(keys) == 1, 'Service intermediate v2 key count changed')
        account = self.obj('deployment', 'certissuer')['spec']['template']['spec']['serviceAccountName']
        m.require(account, 'certissuer ServiceAccount missing')
        jwt = self.kube(['-n', r.NS, 'create', 'token', account,
                         '--audience=openbao', '--duration=10m']).strip()
        login = json.loads(self.bao(['write', '-format=json', 'auth/kubernetes/login', '-'],
                                   json.dumps({'role': 'certissuer-pki-dev', 'jwt': jwt})))
        token = login['auth']['client_token']
        try:
            for path in ('sign/server', 'sign/service-client', 'key/' + keys[0],
                         'roles/server', 'roles/service-client',
                         'intermediate/generate/internal'):
                result = json.loads(self.bao(['write', '-format=json', 'sys/capabilities', '-'],
                                             json.dumps({'token': token, 'paths': [
                                                 issuer['signer_reference'] + '/' + path]})))
                expected = ['update'] if path in ('sign/server', 'sign/service-client') else ['deny']
                m.require(result['data']['capabilities'] == expected,
                          'certissuer v2 provider capability differs: ' + path)
        finally:
            self.bao(['write', 'auth/token/revoke', '-'], json.dumps({'token': token}))
        for role_name, expected_names in (
                ('server', r.SERVICE_DNS_NAMES), ('service-client', V2_CLIENT_IDS)):
            data = json.loads(self.bao(['read', '-format=json',
                                        issuer['signer_reference'] + '/roles/' + role_name]))['data']
            m.require(data['allowed_domains'] == expected_names
                      and data['key_type'] == 'ec' and data['key_bits'] == 256
                      and data['client_flag'] is (role_name == 'service-client')
                      and data['server_flag'] is (role_name == 'server')
                      and data['key_usage'] == ['DigitalSignature']
                      and data['require_cn'] and data['allow_bare_domains']
                      and not data['allow_any_name'] and not data['allow_subdomains']
                      and not data['allow_glob_domains'] and not data['allow_ip_sans']
                      and not data['use_csr_common_name'] and not data['use_csr_sans'],
                      'Service intermediate v2 provider role differs: ' + role_name)
            self.save('provider-role-' + role_name + '.json', data)

    def activate_v2(self):
        root, old, new, operation = self.ready_v2()
        bundle, refs = self.require_v2_bundle(root, old, new)
        for name in r.SERVICE_CONSUMERS:
            owner = self.obj('deployment', name)
            selected = [v for v in owner['spec']['template']['spec']['volumes']
                        if v['name'] == 'service-bundles']
            m.require(len(selected) == 1 and selected[0]['configMap']['name'] == bundle,
                      'listener does not use Service v2 bundle: ' + name)
        receipts = self.wait_receipts(new['issuer_id'], new['trust_bundle_version'],
                                     r.SERVICE_CONSUMERS)
        self.verify_v2_signer(new)
        self.api('/operations/' + operation['operation_id'] + '/activate', {}, 204)
        current_new = self.api('/issuers/' + new['issuer_id'])
        current_old = self.api('/issuers/' + old['issuer_id'])
        m.require(current_new['status'] == 'active' and current_old['status'] == 'retiring'
                  and current_new['certificate_fingerprint_sha256'] == new['certificate_fingerprint_sha256']
                  and current_old['certificate_fingerprint_sha256'] == old['certificate_fingerprint_sha256'],
                  'Service intermediate activation transition differs')
        provider = json.loads(self.bao(['read', '-format=json',
                                        new['signer_reference'] + '/cert/crl']))
        record = self.api('/issuers/' + new['issuer_id'] + '/crl',
                          {'crl_pem': provider['data']['certificate']})
        self.install_v2_crl_manifest(root, current_old, current_new)
        for name in r.SERVICE_CONSUMERS:
            self.rollout_listener_restart(name)
        crl_receipts = self.wait_receipts(new['issuer_id'], record['crl_sha256'],
                                         r.SERVICE_CONSUMERS, kind='crl')
        self.device_baseline()
        self.save('intermediate-v2-active.json', current_new)
        self.save('intermediate-v1-retiring.json', current_old)
        self.save('intermediate-v2-crl.json', record)
        self.check('intermediate_v2_active_with_crl', {
            'bundle': bundle, 'references': refs, 'bundle_consumers': receipts,
            'crl_sha256': record['crl_sha256'], 'crl_consumers': crl_receipts,
            'v1_status': 'retiring', 'v2_status': 'active',
            'listener_restarts': r.SERVICE_CONSUMERS, 'device_baseline': 'passed',
        })

    def install_v2_crl_manifest(self, root, old, new):
        name = 'pki-service-client-crls'
        manifest = self.obj('configmap', name)
        current = json.loads(manifest['data']['crls.json'])
        m.require([entry['issuer']['issuer_id'] for entry in current]
                  == [root['issuer_id'], old['issuer_id']],
                  'existing Service CRL manifest differs before v2 activation')
        updated = crl_entries(root, old, new)
        self.observed_patch('configmap', name, manifest, [
            {'op': 'test', 'path': '/data/crls.json', 'value': manifest['data']['crls.json']},
            {'op': 'replace', 'path': '/data/crls.json', 'value': json.dumps(updated)},
        ])
        self.save('service-crl-manifest-issuer-ids.json',
                  [entry['issuer']['issuer_id'] for entry in updated])

    def recover_activation(self):
        root = self.active_root()
        source = Path(self.args.prepared)
        m.require(m.read(source / 'report.json')['status'] == 'passed',
                  'successful Service intermediate v2 preparation required')
        old_saved = m.read(source / 'intermediate-v1.json')
        new_saved = m.read(source / 'intermediate-ready.json')
        old = self.api('/issuers/' + old_saved['issuer_id'])
        new = self.api('/issuers/' + new_saved['issuer_id'])
        m.require(old['status'] == 'retiring' and new['status'] == 'active'
                  and old['certificate_fingerprint_sha256'] == old_saved['certificate_fingerprint_sha256']
                  and new['certificate_fingerprint_sha256'] == new_saved['certificate_fingerprint_sha256']
                  and new['service_client_ids'] == V2_CLIENT_IDS,
                  'exact activated Service intermediate transition required')
        bundle, refs = self.require_v2_bundle(root, old, new)
        manifest = self.obj('configmap', 'pki-service-client-crls')
        entries = json.loads(manifest['data']['crls.json'])
        expected = crl_entries(root, old, new)
        m.require(entries == expected, 'recovered Service CRL manifest differs')
        bundle_receipts = self.wait_receipts(new['issuer_id'], new['trust_bundle_version'],
                                            r.SERVICE_CONSUMERS)
        record = self.api('/issuers/' + new['issuer_id'] + '/crl')
        crl_receipts = self.wait_receipts(new['issuer_id'], record['crl_sha256'],
                                         r.SERVICE_CONSUMERS, kind='crl')
        self.verify_v2_signer(new)
        for name in r.SERVICE_CONSUMERS:
            owner = self.obj('deployment', name)
            selected = [v for v in owner['spec']['template']['spec']['volumes']
                        if v['name'] == 'service-bundles']
            m.require(len(selected) == 1 and selected[0]['configMap']['name'] == bundle,
                      'listener does not use recovered Service v2 bundle: ' + name)
            self.rollout_listener_restart(name)
        self.device_baseline()
        self.save('intermediate-v2-active.json', new)
        self.save('intermediate-v1-retiring.json', old)
        self.save('intermediate-v2-crl.json', record)
        self.check('intermediate_v2_activation_recovered', {
            'bundle': bundle, 'references': refs, 'bundle_consumers': bundle_receipts,
            'crl_sha256': record['crl_sha256'], 'crl_consumers': crl_receipts,
            'crl_manifest_issuer_ids': [i['issuer_id'] for i in (root, old, new)],
            'v1_status': 'retiring', 'v2_status': 'active',
            'listener_restarts': r.SERVICE_CONSUMERS, 'device_baseline': 'passed',
            'activation_replayed': False,
        })

    def rollout_listener_restart(self, name):
        owner = self.obj('deployment', name)
        annotations = dict(owner['spec']['template']['metadata'].get('annotations', {}))
        annotations['rtk.cloud/pki-service-v2'] = uuid.uuid4().hex
        self.observed_patch('deployment', name, owner, [
            {'op': 'add', 'path': '/spec/template/metadata/annotations', 'value': annotations},
        ])
        self.kube(['-n', r.NS, 'rollout', 'status', 'deployment/' + name,
                   '--timeout=300s'], timeout=310)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', choices=['prepare-intermediate-v2', 'controller',
                                           'certissuer', 'activate', 'recover-activation'],
                        default='prepare-intermediate-v2')
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--authority', required=True)
    parser.add_argument('--prepared')
    parser.add_argument('--resume', action='store_true')
    args = parser.parse_args()
    args.image, args.activation, args.intermediate = None, None, None
    m.require(args.phase == 'prepare-intermediate-v2' or args.prepared,
              'successful v2 preparation evidence required')
    m.require(not args.resume or args.phase == 'prepare-intermediate-v2',
              'resume is limited to v2 preparation')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = FactoryAdoption(args)
    try:
        runner.preflight()
        {'prepare-intermediate-v2': (runner.resume_approved_intermediate
                                     if args.resume else runner.prepare_intermediate_v2),
         'controller': runner.install_controller,
         'certissuer': runner.install_certissuer,
         'activate': runner.activate_v2,
         'recover-activation': runner.recover_activation}[args.phase]()
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
