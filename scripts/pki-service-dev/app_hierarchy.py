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
IMAGE = re.compile(
    r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}')


def with_env(entries, updates, remove=()):
    names = [entry['name'] for entry in entries]
    m.require(len(names) == len(set(names)), 'duplicate environment setting')
    return [entry for entry in entries
            if entry['name'] not in updates and entry['name'] not in remove] + [
                {'name': name, 'value': value}
                for name, value in updates.items()]


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


def consumer_template(owner, name, image, marker):
    template = copy.deepcopy(owner['spec']['template'])
    annotations = template['metadata'].setdefault('annotations', {})
    annotations['rtk.realtek.com/app-trust-revision'] = marker
    if not image:
        return template
    m.require(IMAGE.fullmatch(image), 'immutable Dev Video Cloud image required')
    selected = {
        'video-cloud-api-app-pki': 'app',
        'mqtt-pki': 'pkibroker',
        'pkiturn': 'pkiturn'}[name]
    containers = [item for item in template['spec']['containers']
                  if item['name'] == selected]
    m.require(len(containers) == 1,
              'App consumer container ownership changed: ' + name)
    containers[0]['image'] = image
    if name == 'video-cloud-api-app-pki':
        containers[0]['env'] = with_env(containers[0].get('env', []), {
            'VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_ROOTS':
                '/run/pki-app/roots.pem'})
    return template


def controller_template(owner, image, marker):
    m.require(IMAGE.fullmatch(image or ''),
              'immutable Dev Video Cloud image required')
    template = copy.deepcopy(owner['spec']['template'])
    containers = template['spec']['containers']
    m.require(len(containers) == 1
              and containers[0]['name'] == 'pki-controller',
              'PKI controller container ownership changed')
    containers[0]['image'] = image
    template['metadata'].setdefault('annotations', {})[
        'rtk.realtek.com/app-hierarchy-controller'] = marker
    return template


def resumable_approved_intermediate(saved, current, issuer, parent_id):
    stable = ('action', 'operation_id', 'issuer_id', 'request_sha256',
              'created_by')
    return (all(current.get(key) == saved.get(key) for key in stable)
            and saved.get('status') in ('requested', 'approved')
            and current.get('status') == 'approved'
            and issuer.get('issuer_id') == current.get('issuer_id')
            and issuer.get('status') == 'approved'
            and issuer.get('parent_issuer_id') == parent_id
            and issuer.get('trust_domain') == 'app'
            and issuer.get('kind') == 'intermediate'
            and not issuer.get('csr_pem'))


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
        repairing_missing_root_crl = (
            self.args.phase == 'activate-root' and bool(self.args.resume))
        readiness = {}
        for name in DEPLOYMENTS:
            owner = self.obj('deployment', name)
            ready = owner.get('status', {}).get('readyReplicas', 0)
            m.require(owner['spec']['replicas'] == 1
                      and (ready == 1 or repairing_missing_root_crl),
                      'App consumer is not ready: ' + name)
            readiness[name] = ready
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
            'ready_replicas': readiness,
            'missing_root_crl_repair': repairing_missing_root_crl,
            'required_consumers': CONSUMERS,
            'staging_touched': False})

    def app_root(self, *statuses):
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

    def manifest_authorities(self, *extra):
        cursor, items = '', []
        while True:
            page = self.api('/issuers/search', {
                'limit': 100, 'before': cursor})
            items += [item for item in page['items']
                      if item['environment'] == 'dev'
                      and item['trust_domain'] == 'app'
                      and item['kind'] in ('root', 'intermediate')
                      and item['status'] in ('ready', 'active', 'retiring')]
            cursor = page.get('next', '')
            if not cursor:
                break
        by_id = {item['issuer_id']: item for item in items}
        by_id.update({item['issuer_id']: item for item in extra})
        authorities = list(by_id.values())
        roots = {item['issuer_id'] for item in authorities
                 if item['kind'] == 'root'}
        m.require(roots and all(item['kind'] == 'root'
                               or item['parent_issuer_id'] in roots
                               for item in authorities),
                  'App manifest contains an incomplete lineage')
        return authorities

    def root_operation(self):
        operation = m.read(Path(self.args.authority) / 'root-operation.json')
        m.require(operation['issuer_id'] ==
                  self.app_root('ready', 'active')['issuer_id'],
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
            template = consumer_template(owner, name, self.args.image, marker)
            self.observed_patch('deployment', name, owner, [{
                'op': 'replace', 'path': '/spec/template',
                'value': template}])
            self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + name,
                       '--timeout=300s'], timeout=310)
            current = self.obj('deployment', name)
            m.require(current.get('status', {}).get('readyReplicas') == 1,
                      'App consumer did not restart: ' + name)
            if name == 'mqtt-pki':
                self.forward('mqtt', NS, 'mqtt-pki', 8883)
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

    def rollout_controller(self, marker):
        owner = self.obj('deployment', 'pki-controller')
        template = controller_template(owner, self.args.image, marker)
        self.observed_patch('deployment', 'pki-controller', owner, [{
            'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', NS, 'rollout', 'status',
                   'deployment/pki-controller', '--timeout=300s'], timeout=310)
        current = self.obj('deployment', 'pki-controller')
        containers = current['spec']['template']['spec']['containers']
        m.require(current.get('status', {}).get('readyReplicas') == 1
                  and len(containers) == 1
                  and containers[0]['name'] == 'pki-controller'
                  and containers[0]['image'] == self.args.image,
                  'PKI controller did not roll to reviewed App image')
        return current['metadata']['generation']

    def sign_initial_root_crl(self, root):
        if self.crl_count(root['issuer_id']) == '1':
            return self.api('/issuers/' + root['issuer_id'] + '/crl',
                            role='approver')
        m.require(self.crl_count(root['issuer_id']) == '0',
                  'unexpected App Root CRL history')
        if self.args.resume:
            return self.import_saved_root_crl(root, Path(self.args.resume))
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
        return self.import_saved_root_crl(root, self.output)

    def import_saved_root_crl(self, root, source):
        report = m.read(source / 'report.json')
        m.require(report['environment'] == 'dev'
                  and report['phase'] == 'activate-root'
                  and report['status'] == (
                      'running' if source.resolve() == self.output.resolve()
                      else 'failed'),
                  'current or failed Dev App Root evidence required')
        saved_root = m.read(source / 'root-active.json')
        request = m.read(source / 'root-crl-request.json')
        manifest = m.read(source / 'root-crl/public-manifest.json')
        raw = (source / 'root-crl/revocations.pem').read_text()
        der = base64.b64decode(''.join(raw.splitlines()[1:-1]), validate=True)
        expected = self.ceremony_call([
            'crl-digest', source / 'root-crl-request.json']).strip()
        m.require(saved_root == root
                  and request['issuer_id'] == root['issuer_id']
                  and request['issuer_fingerprint_sha256'] ==
                  root['certificate_fingerprint_sha256']
                  and request['crl_number'] == '1'
                  and not request['revocations']
                  and manifest['request'] == request
                  and manifest['request_sha256'] == expected
                  and manifest['crl_sha256'] == m.digest(der),
                  'saved App Root CRL evidence changed')
        self.save('root-crl-source.json', {
            'source': str(source), 'request_sha256': expected,
            'crl_sha256': manifest['crl_sha256']})
        record = self.api('/issuers/' + root['issuer_id'] + '/crl', {
            'crl_pem': raw}, role='approver')
        m.require(record['crl_sha256'] == manifest['crl_sha256'],
                  'imported App Root CRL differs')
        return record

    def activate_root(self):
        root = self.app_root('ready', 'active')
        operation = self.root_operation()
        pre_restarted = self.install_manifest(
            self.manifest_authorities(root),
            'root-ready-' + root['trust_bundle_version'][:16])
        receipts = self.wait_receipts(
            root['issuer_id'], root['trust_bundle_version'], CONSUMERS)
        if root['status'] == 'ready':
            self.api('/operations/' + operation['operation_id'] + '/activate',
                     {}, 204)
            root = self.app_root('active')
        self.save('root-active.json', root)
        crl = self.sign_initial_root_crl(root)
        self.save('root-crl.json', crl)
        post_restarted = self.install_manifest(
            self.manifest_authorities(root),
            'root-' + root['trust_bundle_version'][:16])
        crl_receipts = self.wait_receipts(
            root['issuer_id'], crl['crl_sha256'], CONSUMERS, kind='crl')
        self.check('app_root_active_with_crl', {
            'issuer_id': root['issuer_id'], 'bundle_receipts': receipts,
            'crl_sha256': crl['crl_sha256'],
            'crl_receipts': crl_receipts,
            'restarted_consumers': pre_restarted + post_restarted})
        self.device_baseline()

    def device_baseline(self):
        device = m.read(
            self.foundation / 'device-2/enroll-request.json')['devid']
        auth, auth_attempts = self.wait_positive_auth(
            self.foundation / 'device-2/v4', device)
        deadline = time.monotonic() + 45
        mqtt_attempts = 0
        while True:
            mqtt_attempts += 1
            try:
                self.mqtt(auth, device, 'roundtrip')
                break
            except RuntimeError as error:
                temporary = (str(error).startswith('probe failed:') or
                             str(error) ==
                             'MQTT authorization result differs: 5')
                if not temporary or time.monotonic() >= deadline:
                    raise
                time.sleep(2)
        self.check('device_baseline_after_app_hierarchy', {
            'direct_mtls': 'passed', 'mqtt_acl_qos1': 'passed',
            'auth_attempts': auth_attempts,
            'mqtt_attempts': mqtt_attempts})

    def active_root(self):
        root = self.app_root('active')
        crl = self.api('/issuers/' + root['issuer_id'] + '/crl',
                       role='approver')
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

    def intermediate(self, *statuses):
        source = Path(self.args.intermediate)
        saved = m.read(source / 'intermediate-ready.json')
        operation = m.read(source / 'intermediate-operation.json')
        issuer = self.api('/issuers/' + saved['issuer_id'])
        root = self.active_root()
        m.require(operation['issuer_id'] == issuer['issuer_id']
                  and issuer['environment'] == 'dev'
                  and issuer['trust_domain'] == 'app'
                  and issuer['kind'] == 'intermediate'
                  and issuer['parent_issuer_id'] == root['issuer_id']
                  and issuer['status'] in statuses
                  and issuer['certificate_fingerprint_sha256'] ==
                  saved['certificate_fingerprint_sha256']
                  and issuer['trust_bundle_version'] ==
                  saved['trust_bundle_version']
                  and issuer['signer_reference'] ==
                  saved['signer_reference'],
                  'reviewed App intermediate changed')
        return root, issuer, operation

    def install_intermediate(self):
        root, issuer, _ = self.intermediate('ready')
        restarted = self.install_manifest(
            self.manifest_authorities(root, issuer),
            'bundle-' + issuer['trust_bundle_version'][:16])
        receipts = self.wait_receipts(
            issuer['issuer_id'], issuer['trust_bundle_version'], CONSUMERS)
        self.check('app_intermediate_installed', {
            'issuer_id': issuer['issuer_id'],
            'bundle_sha256': issuer['trust_bundle_version'],
            'bundle_receipts': receipts,
            'restarted_consumers': restarted,
            'accepted_before_activation': False})
        self.device_baseline()

    def activate_intermediate(self):
        root, issuer, operation = self.intermediate('ready', 'active')
        receipts = self.wait_receipts(
            issuer['issuer_id'], issuer['trust_bundle_version'], CONSUMERS)
        if issuer['status'] == 'ready':
            self.api('/operations/' + operation['operation_id'] + '/activate',
                     {}, 204)
            _, issuer, _ = self.intermediate('active')
        self.save('intermediate-active.json', issuer)
        if self.crl_count(issuer['issuer_id']) == '0':
            provider = json.loads(self.bao([
                'read', '-format=json',
                issuer['signer_reference'] + '/cert/crl']))
            raw = provider['data']['certificate']
            record = self.api('/issuers/' + issuer['issuer_id'] + '/crl', {
                'crl_pem': raw}, role='approver')
        else:
            m.require(self.crl_count(issuer['issuer_id']) == '1',
                      'unexpected App intermediate CRL history')
            record = self.api('/issuers/' + issuer['issuer_id'] + '/crl',
                              role='approver')
        self.save('intermediate-crl.json', record)
        restarted = self.install_manifest(
            self.manifest_authorities(root, issuer),
            'active-' + record['crl_sha256'][:16])
        root_crl = self.api('/issuers/' + root['issuer_id'] + '/crl',
                            role='approver')
        root_receipts = self.wait_receipts(
            root['issuer_id'], root_crl['crl_sha256'], CONSUMERS, kind='crl')
        crl_receipts = self.wait_receipts(
            issuer['issuer_id'], record['crl_sha256'], CONSUMERS, kind='crl')
        self.verify_intermediate_custody(issuer)
        self.check('app_intermediate_active_with_crl', {
            'issuer_id': issuer['issuer_id'],
            'bundle_receipts': receipts,
            'root_crl_receipts': root_receipts,
            'crl_sha256': record['crl_sha256'],
            'crl_receipts': crl_receipts,
            'restarted_consumers': restarted})
        self.device_baseline()

    def ensure_role_policy(self, role_name, policy_name, policy):
        role_path = 'auth/kubernetes/role/' + role_name
        before = json.loads(self.bao([
            'read', '-format=json', role_path]))['data']
        m.require(before['token_no_default_policy']
                  and before['audience'] == 'openbao'
                  and before['bound_service_account_namespaces'] == [NS],
                  'unexpected App signer workload binding')
        available = json.loads(self.bao(['policy', 'list', '-format=json']))
        if policy_name in available:
            m.require(self.bao(['policy', 'read', policy_name]).strip() ==
                      policy.strip(), 'existing App signer policy differs')
        else:
            self.bao(['policy', 'write', policy_name, '-'], policy)
        desired = copy.deepcopy(before)
        if policy_name not in desired['token_policies']:
            desired['token_policies'].append(policy_name)
            m.require(json.loads(self.bao([
                'read', '-format=json', role_path]))['data'] == before,
                'App signer workload binding changed concurrently')
            self.bao(['write', role_path, '-'], json.dumps(desired))
        m.require(json.loads(self.bao([
            'read', '-format=json', role_path]))['data'] == desired,
            'App signer workload policy was not installed')
        self.save('role-' + role_name + '.json', desired)

    def verify_signer_capability(self, issuer):
        jwt = self.kube([
            '-n', NS, 'create', 'token', 'certissuer-pki',
            '--audience=openbao', '--duration=10m']).strip()
        login = json.loads(self.bao([
            'write', '-format=json', 'auth/kubernetes/login', '-'],
            json.dumps({'role': 'certissuer-pki-dev', 'jwt': jwt})))
        token = login['auth']['client_token']
        try:
            result = json.loads(self.bao([
                'write', '-format=json', 'sys/capabilities', '-'],
                json.dumps({'token': token, 'paths': [
                    issuer['signer_reference'] + '/sign/app']})))
            m.require(result['data']['capabilities'] == ['update'],
                      'certissuer App signing capability differs')
        finally:
            self.bao(['write', 'auth/token/revoke', '-'],
                     json.dumps({'token': token}))

    def enable_issuance(self):
        m.require(IMAGE.fullmatch(self.args.image or ''),
                  'immutable Dev Video Cloud image is required')
        _, issuer, _ = self.intermediate('active')
        policy = m.read(Path(self.args.intermediate) / 'provider-policies.json')
        m.require(policy['issuer_id'] == issuer['issuer_id']
                  and policy['mount'] == issuer['signer_reference']
                  and '/sign/app' in policy['signer_policy'],
                  'saved App signer policy differs')
        policy_name = 'certissuer-app-' + issuer['issuer_id']
        self.ensure_role_policy('certissuer-pki-dev', policy_name,
                                policy['signer_policy'])
        owner = self.obj('deployment', 'certissuer')
        template = copy.deepcopy(owner['spec']['template'])
        containers = template['spec']['containers']
        m.require(len(containers) == 1
                  and containers[0]['name'] == 'certissuer',
                  'certissuer ownership changed')
        container = containers[0]
        static = {
            'CERT_ISSUER_APP_CA_CERT_PATH',
            'CERT_ISSUER_APP_CA_KEY_PATH',
            'CERT_ISSUER_APP_CA_KEY_PASSPHRASE',
            'CERT_ISSUER_APP_SIGNER_PROVIDER',
            'CERT_ISSUER_APP_OPENBAO_PKI_MOUNT',
            'CERT_ISSUER_APP_OPENBAO_PKI_ROLE',
            'CERT_ISSUER_APP_PKCS11_MODULE_PATH',
            'CERT_ISSUER_APP_PKCS11_TOKEN_LABEL',
            'CERT_ISSUER_APP_PKCS11_SLOT_ID',
            'CERT_ISSUER_APP_PKCS11_PIN',
            'CERT_ISSUER_APP_PKCS11_KEY_LABEL',
            'CERT_ISSUER_APP_PKCS11_EXPECTED_ALGORITHM'}
        container['image'] = self.args.image
        container['env'] = with_env(container.get('env', []), {
            'CERT_ISSUER_APP_PKI_ENABLED': 'true'}, static)
        self.observed_patch('deployment', 'certissuer', owner, [{
            'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', NS, 'rollout', 'status',
                   'deployment/certissuer', '--timeout=300s'], timeout=310)
        current = self.obj('deployment', 'certissuer')
        current_container = current['spec']['template']['spec']['containers'][0]
        current_env = {entry['name']: entry.get('value', '')
                       for entry in current_container.get('env', [])}
        m.require(current.get('status', {}).get('readyReplicas') == 1
                  and current_container['image'] == self.args.image
                  and current_env.get('CERT_ISSUER_APP_PKI_ENABLED') == 'true'
                  and not static.intersection(current_env),
                  'registry-backed App issuance rollout differs')
        self.verify_signer_capability(issuer)
        self.check('registry_app_issuance_enabled', {
            'issuer_id': issuer['issuer_id'], 'image': self.args.image,
            'openbao_policy': policy_name,
            'static_app_signer_settings_present': False,
            'account_manager_transport_changed': False})
        self.device_baseline()

    def prepare_intermediate(self):
        root = self.active_root()
        request = {'environment': 'dev', 'trust_domain': 'app',
                   'kind': 'intermediate',
                   'parent_issuer_id': root['issuer_id']}
        if self.args.resume:
            source = Path(self.args.resume)
            report = m.read(source / 'report.json')
            saved_request = m.read(source / 'intermediate-request.json')
            saved_operation = m.read(source / 'intermediate-operation.json')
            m.require(report['environment'] == 'dev'
                      and report['phase'] == 'prepare-intermediate'
                      and report['status'] == 'failed'
                      and saved_request == request,
                      'failed Dev App intermediate evidence required')
            operation = self.api('/operations/' +
                                 saved_operation['operation_id'])
            issuer = self.api('/issuers/' + operation['issuer_id'])
            m.require(resumable_approved_intermediate(
                saved_operation, operation, issuer, root['issuer_id']),
                      'failed App intermediate operation was not untouched')
            self.save('intermediate-resume-source.json', {
                'source': str(source),
                'operation_id': operation['operation_id'],
                'request_sha256': operation['request_sha256']})
        else:
            m.require(not any(item['parent_issuer_id'] == root['issuer_id']
                              for item in self.app_intermediates()),
                      'App intermediate already exists for this Root; reconcile')
            operation = self.api('/operations', request,
                                 key='dev-app-intermediate-' + uuid.uuid4().hex)
            self.approval(operation)
        self.save('intermediate-request.json', request)
        self.save('intermediate-operation.json', operation)
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
        self.ensure_role_policy('pki-controller-dev',
                                'pki-controller-dev-' + issuer['issuer_id'],
                                policies['controller_policy'])
        controller_generation = self.rollout_controller(
            'app-intermediate-' + issuer['issuer_id'])
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
            'private_key_exported': False,
            'controller_generation': controller_generation,
            'resumed_approved_operation': bool(self.args.resume)})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--phase', required=True,
                        choices=('activate-root', 'prepare-intermediate',
                                 'install-intermediate',
                                 'activate-intermediate', 'enable-issuance'))
    parser.add_argument('--authority', required=True)
    parser.add_argument('--intermediate')
    parser.add_argument('--image')
    parser.add_argument('--output', required=True)
    parser.add_argument('--resume', help='Reuse signed evidence from a failed phase')
    args = parser.parse_args()
    m.require(not args.resume or args.phase in (
        'activate-root', 'prepare-intermediate'),
        '--resume applies only to Root activation or intermediate preparation')
    m.require(args.phase not in ('install-intermediate',
                                 'activate-intermediate',
                                 'enable-issuance') or args.intermediate,
              'App intermediate evidence is required')
    m.require(args.phase not in ('prepare-intermediate', 'enable-issuance')
              or args.image,
              'Dev image digest is required for controller or issuer rollout')
    lock = (Path(args.config_root).expanduser() /
            'dev/pki/app-hierarchy-rollout.lock')
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = AppHierarchy(args)
    try:
        runner.preflight_app()
        {'activate-root': runner.activate_root,
         'prepare-intermediate': runner.prepare_intermediate,
         'install-intermediate': runner.install_intermediate,
         'activate-intermediate': runner.activate_intermediate,
         'enable-issuance': runner.enable_issuance}[args.phase]()
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
