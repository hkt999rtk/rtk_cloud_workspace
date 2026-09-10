#!/usr/bin/env python3
"""Prepare the exact dev Service v4 authority adding the PKI broker identity."""
import argparse
import base64
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import sys
import uuid


spec = importlib.util.spec_from_file_location(
    'factory_adoption', Path(__file__).with_name('factory_adoption.py'))
fa = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fa)
r, m = fa.r, fa.m

V3_CLIENT_IDS = fa.V2_CLIENT_IDS + ['service:video-cloud-api']
V4_CLIENT_IDS = sorted(V3_CLIENT_IDS + ['service:pkibroker'])
V3_DNS_NAMES = sorted(list(r.SERVICE_DNS_NAMES) + [
    'account-manager-internal.video-cloud-dev-account-manager.svc'])
V4_DNS_NAMES = list(V3_DNS_NAMES)


def v4_request(root):
    m.require(root['environment'] == 'dev' and root['trust_domain'] == 'service'
              and root['kind'] == 'root' and root['status'] == 'active',
              'active dev Service Root required')
    return {'environment': 'dev', 'trust_domain': 'service', 'kind': 'intermediate',
            'parent_issuer_id': root['issuer_id'],
            'service_client_ids': list(V4_CLIENT_IDS),
            'server_dns_names': list(V4_DNS_NAMES)}


def select_predecessors(items, root, transition_id=None):
    live = sorted((i for i in items if i['environment'] == 'dev'
                   and i['trust_domain'] == 'service' and i['kind'] == 'intermediate'
                   and i['status'] in ('active', 'retiring')),
                  key=lambda i: i['issuer_version'])
    m.require(len(live) == 3 and [i['issuer_version'] for i in live] == [1, 2, 3],
              'exact Service v1/v2/v3 predecessors required')
    v1, v2, v3 = live
    m.require(v1['parent_issuer_id'] == root['issuer_id']
              and v1['status'] == 'retiring'
              and v1['service_client_ids'] == fa.OLD_CLIENT_IDS
              and v1['server_dns_names'] == list(r.SERVICE_DNS_NAMES),
              'retiring Service v1 policy differs')
    m.require(v2['parent_issuer_id'] == root['issuer_id']
              and v2['status'] == 'retiring'
              and v2['service_client_ids'] == fa.V2_CLIENT_IDS
              and v2['server_dns_names'] == list(r.SERVICE_DNS_NAMES),
              'retiring Service v2 policy differs')
    m.require(v3['parent_issuer_id'] == root['issuer_id']
              and v3['status'] == 'active'
              and v3['service_client_ids'] == V3_CLIENT_IDS
              and v3['server_dns_names'] == V3_DNS_NAMES,
              'active Service v3 policy differs')
    unfinished = [i for i in items if i['environment'] == 'dev'
                  and i['trust_domain'] == 'service' and i['kind'] == 'intermediate'
                  and i['issuer_id'] not in {v1['issuer_id'], v2['issuer_id'], v3['issuer_id'], transition_id}
                  and i['status'] not in ('retired', 'cancelled', 'revoked',
                                          'compromised', 'failed')]
    m.require(not unfinished, 'another Service successor already exists; reconcile')
    return live

def bundle_references(root, predecessors, successor):
    issuers = [root] + list(predecessors)
    if successor is not None:
        issuers.append(successor)
    return [{'issuer_id': i['issuer_id'],
             'trust_bundle_version': i['trust_bundle_version']}
            for i in issuers]



class PKIBrokerAuthority(fa.FactoryAdoption):
    def __init__(self, args):
        m.Acceptance.__init__(self, args.config_root, 'lke649805-ctx', args.output,
                              resume='ready' if args.resume else False)
        self.args = args
        self.report['foundation_scope'] = 'Active dev Service v3; PKI broker Service authority'
        self.report['checks'] = {}
        self.report['phase'] = args.phase
        self.report['pkibroker_authority_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def inventory(self, transition_id=None):
        root = self.active_root()
        items = self.service_intermediates()
        predecessors = select_predecessors(items, root, transition_id)
        return root, predecessors

    def prepare_v4(self):
        root, predecessors = self.inventory()
        for issuer in predecessors:
            self.save('intermediate-v%d.json' % issuer['issuer_version'], issuer)
            self.save('intermediate-v%d-crl.json' % issuer['issuer_version'],
                      self.api('/issuers/' + issuer['issuer_id'] + '/crl'))
        request = v4_request(root)
        self.save('intermediate-request.json', request)
        operation = self.api('/operations', request,
                             key='dev-service-intermediate-v4-' + uuid.uuid4().hex)
        self.save('intermediate-operation.json', operation)
        self.approve_intermediate(operation)
        issuer = self.api('/issuers/' + operation['issuer_id'])
        m.require(issuer['status'] == 'approved' and issuer['signer_provider'] == 'openbao'
                  and issuer['issuer_version'] == 4,
                  'Service intermediate v4 reservation differs')
        m.require(issuer['signer_reference'] + '/' not in
                  json.loads(self.bao(['secrets', 'list', '-format=json'])),
                  'Service intermediate v4 provider mount already exists; reconcile')
        self.provision_v4(root, predecessors, operation, issuer)

    def resume_v4(self):
        request = m.read(self.output / 'intermediate-request.json')
        operation = m.read(self.output / 'intermediate-operation.json')
        root, predecessors = self.inventory(operation['issuer_id'])
        m.require(request == v4_request(root), 'saved Service v4 request changed')
        current = self.api('/operations/' + operation['operation_id'])
        m.require(current['issuer_id'] == operation['issuer_id']
                  and current['request_sha256'] == operation['request_sha256']
                  and current['status'] == 'approved', 'saved approved v4 operation differs')
        issuer = self.api('/issuers/' + operation['issuer_id'])
        m.require(issuer['status'] == 'approved'
                  and issuer['service_client_ids'] == V4_CLIENT_IDS
                  and issuer['server_dns_names'] == V4_DNS_NAMES,
                  'approved Service v4 differs during recovery')
        m.require(not (self.output / 'provider-policies.json').exists()
                  and issuer['signer_reference'] + '/' not in
                  json.loads(self.bao(['secrets', 'list', '-format=json'])),
                  'recovery is limited to a pre-provision approval failure')
        self.provision_v4(root, predecessors, current, issuer)

    def provision_v4(self, root, predecessors, operation, issuer):
        policies = json.loads(self.kube([
            '-n', r.NS, 'exec', 'deployment/pki-controller', '--',
            '/app/pkicontroller', 'render-openbao-policy', issuer['issuer_id']]))
        m.require(policies['mount'] == issuer['signer_reference']
                  and policies['environment'] == 'dev'
                  and '/sign/server' in policies['signer_policy']
                  and '/sign/service-client' in policies['service_client_signer_policy'],
                  'Service v4 provider policy differs')
        self.save('provider-policies.json', policies)
        before = json.loads(self.bao([
            'read', '-format=json', 'auth/kubernetes/role/pki-controller-dev']))['data']
        self.save('controller-role-before.json', before)
        self.role_policy('pki-controller-dev', 'pki-controller-dev-' + issuer['issuer_id'],
                         policies['controller_policy'])
        self.api('/operations/' + operation['operation_id'] + '/provision', {})
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['status'] == 'provisioning' and issuer['csr_pem'],
                  'Service v4 CSR not durably registered')
        self.save('intermediate-provisioning.json', issuer)
        self.verify_intermediate_custody(issuer)
        source = Path(self.args.authority)
        csr_digest = m.digest(base64.b64decode(''.join(issuer['csr_pem'].splitlines()[1:-1])))
        self.ceremony_call([
            'sign', '--issuer', self.output / 'intermediate-provisioning.json',
            '--expected-request-sha256', operation['request_sha256'],
            '--expected-csr-sha256', csr_digest,
            '--parent', self.output / 'service-root.json',
            '--expected-parent-sha256', root['certificate_fingerprint_sha256'],
            '--key', source / 'root-offline-simulation/ca-key.encrypted.pem',
            '--passphrase-file', m.read(source / 'passphrase-reference.json')['path'],
            '--out', self.output / 'intermediate-signed'])
        self.api('/operations/' + operation['operation_id'] + '/import', {
            'certificate_pem': (self.output / 'intermediate-signed/certificate.pem').read_text()}, 204)
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['status'] == 'ready' and issuer['issuer_version'] == 4
                  and issuer['service_client_ids'] == V4_CLIENT_IDS
                  and issuer['server_dns_names'] == V4_DNS_NAMES,
                  'ready Service v4 differs from approved policy')
        m.require([self.api('/issuers/' + old['issuer_id']) for old in predecessors]
                  == predecessors, 'Service predecessors changed before trust installation')
        self.save('intermediate-ready.json', issuer)
        self.api('/operations/' + operation['operation_id'] + '/activate', {}, 409)
        self.check('intermediate_v4_ready_gate_closed', {
            'issuer_id': issuer['issuer_id'], 'issuer_version': 4,
            'activation_without_receipts': 409,
            'service_client_ids': issuer['service_client_ids'],
            'server_dns_names': issuer['server_dns_names'],
            'private_key_exported': False})

    def ready_v4(self):
        root = self.active_root()
        source = Path(self.args.prepared)
        m.require(m.read(source / 'report.json')['status'] == 'passed',
                  'successful Service v4 preparation required')
        predecessors = [m.read(source / ('intermediate-v%d.json' % version))
                        for version in (1, 2, 3)]
        successor = m.read(source / 'intermediate-ready.json')
        current = select_predecessors(self.service_intermediates(), root,
                                      successor['issuer_id'])
        m.require(current == predecessors, 'Service predecessors changed')
        operation = m.read(source / 'intermediate-operation.json')
        m.require(self.api('/issuers/' + successor['issuer_id']) == successor
                  and successor['status'] == 'ready'
                  and successor['service_client_ids'] == V4_CLIENT_IDS
                  and successor['server_dns_names'] == V4_DNS_NAMES
                  and operation['issuer_id'] == successor['issuer_id'],
                  'ready Service v4 evidence changed')
        return root, predecessors, successor, operation

    def current_bundle(self, root, predecessors):
        names = set()
        for listener in r.SERVICE_CONSUMERS:
            owner = self.obj('deployment', listener)
            volumes = [v for v in owner['spec']['template']['spec']['volumes']
                       if v['name'] == 'service-bundles']
            m.require(len(volumes) == 1, 'listener Service bundle missing: ' + listener)
            names.add(volumes[0]['configMap']['name'])
        m.require(len(names) == 1, 'listeners do not share current Service bundle')
        name = names.pop()
        manifest = self.obj('configmap', name)
        expected = bundle_references(root, predecessors, None)
        m.require(manifest.get('immutable')
                  and json.loads(manifest['data']['issuers.json']) == expected,
                  'current Service v3 bundle differs')
        return name

    def v4_bundle_name(self, successor):
        return 'pki-service-bundles-v4-' + successor['issuer_id'][:8]

    def persist_listener(self, name):
        path = self.base / 'pki/controller-bootstrap/rollout' / (name + '-deployment.json')
        desired = m.read(path)
        live = self.obj('deployment', name)
        m.require(desired['metadata']['name'] == name
                  and desired['metadata']['namespace'] == r.NS,
                  'persisted listener scope changed')
        saved = [v for v in desired['spec']['template']['spec']['volumes']
                 if v['name'] == 'service-bundles']
        installed = [v for v in live['spec']['template']['spec']['volumes']
                     if v['name'] == 'service-bundles']
        m.require(len(saved) == len(installed) == 1,
                  'persisted Service bundle volume changed')
        saved[0]['configMap']['name'] = installed[0]['configMap']['name']
        m.write(path, desired)

    def install_controller(self):
        root, predecessors, successor, operation = self.ready_v4()
        old_name = self.current_bundle(root, predecessors)
        new_name = self.v4_bundle_name(successor)
        refs = bundle_references(root, predecessors, successor)
        self.create({'apiVersion': 'v1', 'kind': 'ConfigMap',
                     'metadata': {'name': new_name, 'namespace': r.NS},
                     'immutable': True, 'data': {'issuers.json': json.dumps(refs)}})
        self.install_listener_bundle('pki-controller', old_name, new_name)
        self.persist_listener('pki-controller')
        receipts = self.wait_receipts(successor['issuer_id'], successor['trust_bundle_version'],
                                      ['pki-controller'], absent='certissuer')
        self.api('/operations/' + operation['operation_id'] + '/activate', {}, 409)
        self.device_baseline()
        self.check('intermediate_v4_controller_receipt', {
            'bundle': new_name, 'references': refs, 'consumers': receipts,
            'certissuer_absent': True, 'activation_denied': 409})

    def recover_controller(self):
        failed = Path(self.args.failed)
        report = m.read(failed / 'report.json')
        m.require(report['status'] == 'failed' and report['phase'] == 'controller',
                  'failed controller phase evidence required')
        root, predecessors, successor, operation = self.ready_v4()
        bundle, refs = self.require_v4_bundle(root, predecessors, successor)
        m.require(self.current_bundle_for('pki-controller') == bundle,
                  'controller did not retain Service v4')
        m.require(self.current_bundle_for('certissuer') != bundle,
                  'certissuer advanced during controller recovery')
        receipts = self.wait_receipts(successor['issuer_id'],
                                      successor['trust_bundle_version'],
                                      ['pki-controller'], absent='certissuer')
        self.api('/operations/' + operation['operation_id'] + '/activate', {}, 409)
        self.persist_listener('pki-controller')
        self.device_baseline()
        self.check('intermediate_v4_controller_receipt_recovered', {
            'bundle': bundle, 'references': refs, 'consumers': receipts,
            'certissuer_absent': True, 'activation_denied': 409,
            'authority_mutation_replayed': False})

    def require_v4_bundle(self, root, predecessors, successor):
        name = self.v4_bundle_name(successor)
        refs = bundle_references(root, predecessors, successor)
        manifest = self.obj('configmap', name)
        m.require(manifest.get('immutable')
                  and json.loads(manifest['data']['issuers.json']) == refs,
                  'Service v4 bundle differs')
        return name, refs

    def install_certissuer(self):
        root, predecessors, successor, operation = self.ready_v4()
        new_name, refs = self.require_v4_bundle(root, predecessors, successor)
        controller = self.obj('deployment', 'pki-controller')
        selected = [v for v in controller['spec']['template']['spec']['volumes']
                    if v['name'] == 'service-bundles']
        m.require(len(selected) == 1 and selected[0]['configMap']['name'] == new_name,
                  'controller has not retained Service v4')
        self.wait_receipts(successor['issuer_id'], successor['trust_bundle_version'],
                           ['pki-controller'], absent='certissuer')
        old_name = self.current_bundle_for('certissuer')
        self.install_listener_bundle('certissuer', old_name, new_name)
        self.persist_listener('certissuer')
        receipts = self.wait_receipts(successor['issuer_id'], successor['trust_bundle_version'],
                                      r.SERVICE_CONSUMERS)
        policies = m.read(Path(self.args.prepared) / 'provider-policies.json')
        role = 'certissuer-pki-dev'
        self.save('certissuer-role-before.json', json.loads(self.bao([
            'read', '-format=json', 'auth/kubernetes/role/' + role]))['data'])
        self.role_policy(role, 'pki-service-server-dev-v4-' + successor['issuer_id'],
                         policies['signer_policy'])
        self.role_policy(role, 'pki-service-client-dev-v4-' + successor['issuer_id'],
                         policies['service_client_signer_policy'])
        self.verify_v4_signer(successor)
        m.require(self.api('/operations/' + operation['operation_id'])['status'] == 'ready',
                  'Service v4 operation changed before activation')
        self.device_baseline()
        self.check('intermediate_v4_listener_receipts_and_signer', {
            'bundle': new_name, 'references': refs, 'consumers': receipts,
            'server_and_service_client_signing_only': True})

    def current_bundle_for(self, listener):
        owner = self.obj('deployment', listener)
        selected = [v for v in owner['spec']['template']['spec']['volumes']
                    if v['name'] == 'service-bundles']
        m.require(len(selected) == 1, 'listener Service bundle missing: ' + listener)
        return selected[0]['configMap']['name']

    def verify_v4_signer(self, issuer):
        keys = json.loads(self.bao(['list', '-format=json', issuer['signer_reference'] + '/keys']))
        m.require(len(keys) == 1, 'Service v4 key count changed')
        account = self.obj('deployment', 'certissuer')['spec']['template']['spec']['serviceAccountName']
        jwt = self.kube(['-n', r.NS, 'create', 'token', account,
                         '--audience=openbao', '--duration=10m']).strip()
        login = json.loads(self.bao(['write', '-format=json', 'auth/kubernetes/login', '-'],
                                   json.dumps({'role': 'certissuer-pki-dev', 'jwt': jwt})))
        token = login['auth']['client_token']
        try:
            for path in ('sign/server', 'sign/service-client', 'key/' + keys[0],
                         'roles/server', 'roles/service-client', 'intermediate/generate/internal'):
                result = json.loads(self.bao(['write', '-format=json', 'sys/capabilities', '-'],
                                             json.dumps({'token': token, 'paths': [
                                                 issuer['signer_reference'] + '/' + path]})))
                expected = ['update'] if path in ('sign/server', 'sign/service-client') else ['deny']
                m.require(result['data']['capabilities'] == expected,
                          'certissuer v4 capability differs: ' + path)
        finally:
            self.bao(['write', 'auth/token/revoke', '-'], json.dumps({'token': token}))
        for role_name, expected_names in (('server', V4_DNS_NAMES),
                                          ('service-client', V4_CLIENT_IDS)):
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
                      'Service v4 provider role differs: ' + role_name)
            self.save('provider-role-' + role_name + '.json', data)

    def activate_v4(self):
        root, predecessors, successor, operation = self.ready_v4()
        bundle, refs = self.require_v4_bundle(root, predecessors, successor)
        for listener in r.SERVICE_CONSUMERS:
            m.require(self.current_bundle_for(listener) == bundle,
                      'listener does not use Service v4: ' + listener)
        bundle_receipts = self.wait_receipts(successor['issuer_id'],
                                            successor['trust_bundle_version'],
                                            r.SERVICE_CONSUMERS)
        self.verify_v4_signer(successor)
        self.api('/operations/' + operation['operation_id'] + '/activate', {}, 204)
        current = self.api('/issuers/' + successor['issuer_id'])
        old = [self.api('/issuers/' + i['issuer_id']) for i in predecessors]
        m.require(current['status'] == 'active'
                  and [i['status'] for i in old] == ['retiring', 'retiring', 'retiring']
                  and current['certificate_fingerprint_sha256'] == successor['certificate_fingerprint_sha256'],
                  'Service v4 activation transition differs')
        provider = json.loads(self.bao(['read', '-format=json',
                                        current['signer_reference'] + '/cert/crl']))
        record = self.api('/issuers/' + current['issuer_id'] + '/crl',
                          {'crl_pem': provider['data']['certificate']})
        manifest = self.obj('configmap', 'pki-service-client-crls')
        entries = json.loads(manifest['data']['crls.json'])
        expected_ids = [i['issuer_id'] for i in [root] + predecessors]
        m.require([entry['issuer']['issuer_id'] for entry in entries] == expected_ids,
                  'existing Service CRL manifest differs before v4 activation')
        updated = fa.crl_entries(root, *old, current)
        self.observed_patch('configmap', 'pki-service-client-crls', manifest, [
            {'op': 'test', 'path': '/data/crls.json', 'value': manifest['data']['crls.json']},
            {'op': 'replace', 'path': '/data/crls.json', 'value': json.dumps(updated)}])
        path = self.base / 'pki/controller-bootstrap/rollout/pki-service-client-crls-configmap.json'
        desired = m.read(path)
        m.require(desired['metadata']['name'] == 'pki-service-client-crls'
                  and desired['metadata']['namespace'] == r.NS,
                  'persisted Service CRL manifest scope changed')
        desired['data']['crls.json'] = json.dumps(updated)
        m.write(path, desired)
        for listener in r.SERVICE_CONSUMERS:
            self.rollout_listener_restart(listener)
        crl_receipts = self.wait_receipts(current['issuer_id'], record['crl_sha256'],
                                         r.SERVICE_CONSUMERS, kind='crl')
        self.device_baseline()
        self.save('intermediate-v4-active.json', current)
        self.save('intermediate-v1-retiring.json', old[0])
        self.save('intermediate-v2-retiring.json', old[1])
        self.save('intermediate-v3-retiring.json', old[2])
        self.save('intermediate-v4-crl.json', record)
        self.check('intermediate_v4_active_with_crl', {
            'bundle': bundle, 'references': refs,
            'bundle_consumers': bundle_receipts,
            'crl_sha256': record['crl_sha256'], 'crl_consumers': crl_receipts,
            'v1_status': 'retiring', 'v2_status': 'retiring', 'v3_status': 'retiring',
            'v4_status': 'active',
            'device_baseline': 'passed'})

    def install_listener_image(self, name, image):
        m.require(name in r.SERVICE_CONSUMERS and re.fullmatch(
            r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}',
            image or ''), 'verified dev listener image required')
        owner = self.obj('deployment', name)
        containers = owner['spec']['template']['spec']['containers']
        m.require(len(containers) == 1 and containers[0]['name'] == name,
                  'listener container changed: ' + name)
        if containers[0]['image'] != image:
            self.observed_patch('deployment', name, owner, [
                {'op': 'test', 'path': '/spec/template/spec/containers/0/image',
                 'value': containers[0]['image']},
                {'op': 'replace', 'path': '/spec/template/spec/containers/0/image',
                 'value': image}])
        self.kube(['-n', r.NS, 'rollout', 'status', 'deployment/' + name,
                   '--timeout=300s'], timeout=310)
        live = self.obj('deployment', name)
        m.require(live['spec']['template']['spec']['containers'][0]['image'] == image,
                  'listener image rollout differs: ' + name)
        path = self.base / 'pki/controller-bootstrap/rollout' / (name + '-deployment.json')
        desired = m.read(path)
        desired_containers = desired['spec']['template']['spec']['containers']
        m.require(desired['metadata']['name'] == name
                  and desired['metadata']['namespace'] == r.NS
                  and len(desired_containers) == 1
                  and desired_containers[0]['name'] == name,
                  'persisted listener scope changed: ' + name)
        desired_containers[0]['image'] = image
        m.write(path, desired)
        key = 'PKI_CONTROLLER_IMAGE' if name == 'pki-controller' else 'PKI_CERTISSUER_IMAGE'
        m.write(self.base / 'operator/env' / key, image + '\n')

    def recover_activation(self):
        failed = m.read(Path(self.args.failed) / 'report.json')
        m.require(failed['status'] == 'failed' and failed['phase'] == 'activate',
                  'failed v4 activation evidence required')
        root = self.active_root()
        source = Path(self.args.prepared)
        m.require(m.read(source / 'report.json')['status'] == 'passed',
                  'successful Service v4 preparation required')
        saved_old = [m.read(source / ('intermediate-v%d.json' % version))
                     for version in (1, 2, 3)]
        saved_new = m.read(source / 'intermediate-ready.json')
        old = [self.api('/issuers/' + issuer['issuer_id']) for issuer in saved_old]
        new = self.api('/issuers/' + saved_new['issuer_id'])
        operation = m.read(source / 'intermediate-operation.json')
        current_operation = self.api('/operations/' + operation['operation_id'])
        m.require([issuer['status'] for issuer in old] == ['retiring', 'retiring', 'retiring']
                  and new['status'] == 'active' and current_operation['status'] == 'active'
                  and [issuer['certificate_fingerprint_sha256'] for issuer in old]
                  == [issuer['certificate_fingerprint_sha256'] for issuer in saved_old]
                  and new['certificate_fingerprint_sha256']
                  == saved_new['certificate_fingerprint_sha256']
                  and new['service_client_ids'] == V4_CLIENT_IDS
                  and new['server_dns_names'] == V4_DNS_NAMES,
                  'exact activated Service v4 transition required')
        bundle, refs = self.require_v4_bundle(root, old, new)
        for listener in r.SERVICE_CONSUMERS:
            m.require(self.current_bundle_for(listener) == bundle,
                      'listener does not use Service v4: ' + listener)
        bundle_receipts = self.wait_receipts(new['issuer_id'],
                                            new['trust_bundle_version'],
                                            r.SERVICE_CONSUMERS)
        self.verify_v4_signer(new)
        provider_crl = json.loads(self.bao([
            'read', '-format=json', new['signer_reference'] + '/cert/crl']))['data']['certificate']
        record = self.api('/issuers/' + new['issuer_id'] + '/crl', expected=503)
        imported_crl = record is None
        if imported_crl:
            record = self.api('/issuers/' + new['issuer_id'] + '/crl',
                              {'crl_pem': provider_crl})
        m.require(record['crl_pem'] == provider_crl,
                  'recovered v4 CRL differs from OpenBao')

        manifest = self.obj('configmap', 'pki-service-client-crls')
        entries = json.loads(manifest['data']['crls.json'])
        updated = fa.crl_entries(root, *old, new)
        ids = [entry['issuer']['issuer_id'] for entry in entries]
        previous_ids = [issuer['issuer_id'] for issuer in [root] + old]
        m.require(ids == previous_ids or entries == updated,
                  'Service CRL manifest differs during v4 recovery')
        if ids == previous_ids:
            self.observed_patch('configmap', 'pki-service-client-crls', manifest, [
                {'op': 'test', 'path': '/data/crls.json',
                 'value': manifest['data']['crls.json']},
                {'op': 'replace', 'path': '/data/crls.json',
                 'value': json.dumps(updated)}])
        desired_path = self.base / 'pki/controller-bootstrap/rollout/pki-service-client-crls-configmap.json'
        desired = m.read(desired_path)
        m.require(desired['metadata']['name'] == 'pki-service-client-crls'
                  and desired['metadata']['namespace'] == r.NS,
                  'persisted Service CRL manifest scope changed')
        desired['data']['crls.json'] = json.dumps(updated)
        m.write(desired_path, desired)
        for listener in r.SERVICE_CONSUMERS:
            self.install_listener_image(listener, self.args.image)
            self.rollout_listener_restart(listener)
        crl_receipts = self.wait_receipts(new['issuer_id'], record['crl_sha256'],
                                         r.SERVICE_CONSUMERS, kind='crl')
        self.device_baseline()
        self.save('intermediate-v4-active.json', new)
        self.save('intermediate-v1-retiring.json', old[0])
        self.save('intermediate-v2-retiring.json', old[1])
        self.save('intermediate-v3-retiring.json', old[2])
        self.save('intermediate-v4-crl.json', record)
        self.check('intermediate_v4_activation_recovered', {
            'bundle': bundle, 'references': refs,
            'bundle_consumers': bundle_receipts,
            'crl_sha256': record['crl_sha256'], 'crl_consumers': crl_receipts,
            'v1_status': 'retiring', 'v2_status': 'retiring', 'v3_status': 'retiring',
            'v4_status': 'active',
            'listener_images': {name: self.args.image for name in r.SERVICE_CONSUMERS},
            'device_baseline': 'passed', 'activation_replayed': False,
            'provider_crl_imported_during_recovery': imported_crl})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', choices=['prepare-intermediate-v4', 'controller',
                                           'recover-controller', 'certissuer',
                                           'activate', 'recover-activation'],
                        default='prepare-intermediate-v4')
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--authority', required=True)
    parser.add_argument('--prepared')
    parser.add_argument('--failed')
    parser.add_argument('--image')
    parser.add_argument('--resume', action='store_true')
    args = parser.parse_args()
    args.activation, args.intermediate = None, None
    m.require(args.phase == 'prepare-intermediate-v4' or args.prepared,
              'successful v4 preparation evidence required')
    m.require(not args.resume or args.phase == 'prepare-intermediate-v4',
              'resume is limited to v4 preparation')
    m.require(args.phase != 'recover-controller' or args.failed,
              'failed controller evidence required for recovery')
    m.require(args.phase != 'recover-activation' or (args.failed and args.image),
              'failed activation evidence and verified image required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = PKIBrokerAuthority(args)
    try:
        runner.preflight()
        {'prepare-intermediate-v4': runner.resume_v4 if args.resume else runner.prepare_v4,
         'controller': runner.install_controller,
         'recover-controller': runner.recover_controller,
         'certissuer': runner.install_certissuer,
         'activate': runner.activate_v4,
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
        raise RuntimeError('interrupted; reconcile saved v4 authority evidence before retry')
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
