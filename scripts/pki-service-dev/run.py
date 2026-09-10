#!/usr/bin/env python3
"""Dev-only Service hierarchy rollout. Each phase uses a new private evidence directory."""
import argparse
import base64
import datetime as dt
import importlib.util
import fcntl
import json
import os
from pathlib import Path
import re
import secrets
import signal
import sys
import time
import uuid

spec = importlib.util.spec_from_file_location('device_acceptance', Path(__file__).resolve().parents[1] / 'pki-dev-acceptance/run.py')
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
NS = m.NS
SERVICE_CONSUMERS = ['certissuer', 'pki-controller']
SERVICE_CLIENT_IDS = ['service:account-manager', 'service:certissuer', 'service:pki-controller']
SERVICE_DNS_NAMES = [name + '.' + NS + '.svc' for name in SERVICE_CONSUMERS]


def intermediate_request(root):
    m.require(root['environment'] == 'dev' and root['trust_domain'] == 'service'
              and root['kind'] == 'root' and root['status'] == 'active', 'active dev Service Root required')
    return {'environment': 'dev', 'trust_domain': 'service', 'kind': 'intermediate',
            'parent_issuer_id': root['issuer_id'], 'service_client_ids': list(SERVICE_CLIENT_IDS),
            'server_dns_names': list(SERVICE_DNS_NAMES)}


def listener_bundle_template(owner, configmap):
    template = json.loads(json.dumps(owner['spec']['template']))
    volumes = template['spec']['volumes']
    selected = [v for v in volumes if v['name'] == 'service-bundles']
    m.require(len(selected) == 1 and selected[0].get('configMap', {}).get('name') == 'pki-service-bundles',
              'listener bundle source changed; reconcile')
    selected[0]['configMap']['name'] = configmap
    return template


def verify_provider_role(role, client):
    m.require(role['allowed_domains'] == (SERVICE_CLIENT_IDS if client else SERVICE_DNS_NAMES)
              and role['key_type'] == 'ec' and role['key_bits'] == 256
              and role['client_flag'] is client and role['server_flag'] is not client
              and role['max_ttl'] == (90 if client else 365) * 86400
              and role['key_usage'] == ['DigitalSignature'] and role['require_cn'] and role['allow_bare_domains']
              and role['enforce_hostnames'] is not client,
              'provider role differs from approved identity/profile')
    for field in ('allow_any_name', 'allow_subdomains', 'allow_glob_domains', 'allow_wildcard_certificates',
                  'allow_ip_sans', 'use_csr_common_name', 'use_csr_sans', 'code_signing_flag', 'email_protection_flag', 'no_store'):
        m.require(role[field] is False, 'provider role broadens profile: ' + field)
    m.require(not role['allowed_uri_sans'] and not role['allowed_other_sans'], 'provider role permits other SANs')


def with_env(existing, updates, remove=()):
    names = [e['name'] for e in existing]
    m.require(len(names) == len(set(names)), 'duplicate environment settings')
    return [e for e in existing if e['name'] not in updates and e['name'] not in remove] + [
        {'name': key, 'value': value} for key, value in updates.items()]


def append_pem(existing, extra):
    m.require(extra.startswith('-----BEGIN CERTIFICATE-----'), 'public certificate required')
    return existing if extra.strip() in existing else existing.rstrip() + '\n' + extra


def render_persisted_listener(base, name):
    m.require(name in SERVICE_CONSUMERS, 'known Service listener required')
    directory = base / 'pki/controller-bootstrap/rollout'
    desired = m.read(directory / (name + '-deployment.json'))
    settings = m.read(directory / (name + '-service-settings.json'))
    key = 'PKI_CONTROLLER_IMAGE' if name == 'pki-controller' else 'PKI_CERTISSUER_IMAGE'
    image = (base / 'operator/env' / key).read_text().strip()
    m.require(re.fullmatch(r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}', image), 'persisted dev image digest required')
    m.require(desired['metadata']['name'] == name and desired['metadata']['namespace'] == NS, 'persisted listener scope changed')
    containers = desired['spec']['template']['spec']['containers']
    m.require(len(containers) == 1 and containers[0]['name'] == name, 'persisted listener container changed')
    containers[0]['image'] = image
    containers[0]['env'] = with_env(containers[0]['env'], settings)
    if name == 'pki-controller':
        env = {e['name']: e.get('value') for e in containers[0]['env']}
        m.require(m.device_consumers(env) == m.CONSUMERS and env.get('PKI_REQUIRED_CONSUMERS_SERVICE') in (
            ','.join(SERVICE_CONSUMERS), 'certissuer,factory-enroll,pki-controller'), 'persisted domain gates changed')
    return desired


class ServiceRun(m.Acceptance):
    def __init__(self, args):
        super().__init__(args.config_root, 'lke649805-ctx', args.output)
        self.args = args
        self.report['foundation_scope'] = 'Existing dev Device baseline; independent Service hierarchy rollout'
        self.report['checks'] = {}
        self.report['phase'] = args.phase
        self.report['service_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def ready(self):
        saved = m.read(Path(self.args.authority) / 'root-ready.json')
        current = self.api('/issuers/' + saved['issuer_id'])
        m.require(current == saved and current['status'] == 'ready' and current['trust_domain'] == 'service'
                  and current['environment'] == 'dev', 'ready Service Root changed; reconcile')
        self.root_service = current
        self.root_op = m.read(Path(self.args.authority) / 'root-operation.json')
        return current

    def ceremony_call(self, args):
        prior = os.environ.get('PATH', '')
        try:
            os.environ['PATH'] = str(Path(self.openssl).parent) + os.pathsep + prior
            return m.command([self.ceremony] + args)
        finally:
            os.environ['PATH'] = prior

    def prepare_root(self):
        cursor = ''
        while True:
            page = self.api('/issuers/search', {'limit': 100, 'before': cursor})
            m.require(not any(i['trust_domain'] == 'service' for i in page['items']),
                      'Service authority already exists; reconcile instead of creating another')
            cursor = page.get('next', '')
            if not cursor:
                break
        operation = self.api('/operations', {'environment': 'dev', 'trust_domain': 'service', 'kind': 'root'},
                             key='dev-service-root-' + uuid.uuid4().hex)
        self.save('root-operation.json', operation)
        self.approval(operation)
        issuer = self.api('/issuers/' + operation['issuer_id'])
        m.require(issuer['status'] == 'approved' and issuer['signer_provider'] == 'offline', 'Root reservation differs')
        self.save('root-approved.json', issuer)
        passfile = self.base / 'pki/rehearsal-passphrases' / ('service-root-' + issuer['issuer_id'])
        m.require(not passfile.exists(), 'Root passphrase already exists')
        m.write(passfile, secrets.token_urlsafe(48))
        self.save('passphrase-reference.json', {'path': str(passfile)})
        keydir = self.output / 'root-offline-simulation'
        self.ceremony_call(['generate', '--issuer', self.output / 'root-approved.json', '--passphrase-file', passfile,
                            '--expected-request-sha256', operation['request_sha256'], '--out', keydir])
        csr = (keydir / 'csr.pem').read_text()
        self.api('/operations/' + operation['operation_id'] + '/provision', {'csr_pem': csr})
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['csr_pem'] == csr and issuer['status'] == 'provisioning', 'registered CSR differs')
        self.save('root-provisioning.json', issuer)
        csr_hash = m.digest(base64.b64decode(''.join(csr.splitlines()[1:-1])))
        self.ceremony_call(['sign', '--issuer', self.output / 'root-provisioning.json', '--passphrase-file', passfile,
                            '--key', keydir / 'ca-key.encrypted.pem', '--expected-request-sha256', operation['request_sha256'],
                            '--expected-csr-sha256', csr_hash, '--out', self.output / 'root-signed'])
        self.api('/operations/' + operation['operation_id'] + '/import',
                 {'certificate_pem': (self.output / 'root-signed/certificate.pem').read_text()}, 204)
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['status'] == 'ready', 'Root import not ready')
        self.save('root-ready.json', issuer)
        self.api('/operations/' + operation['operation_id'] + '/activate', {}, 409)
        self.check('root_ready_gate_closed', {'issuer_id': issuer['issuer_id'], 'activation_without_receipts': 409,
                                             'key_custody': 'encrypted offline dev simulation; distinct software approval accounts'})

    def active_root(self):
        saved = m.read(Path(self.args.authority) / 'root-ready.json')
        root = self.api('/issuers/' + saved['issuer_id'])
        intermediate_request(root)
        m.require(root['certificate_fingerprint_sha256'] == saved['certificate_fingerprint_sha256']
                  and root['trust_bundle_version'] == saved['trust_bundle_version'], 'Service Root changed')
        crl = self.api('/issuers/' + root['issuer_id'] + '/crl')
        m.require(dt.datetime.fromisoformat(crl['next_update'].replace('Z', '+00:00')) >
                  dt.datetime.now(dt.timezone.utc) + dt.timedelta(hours=1), 'Service Root CRL needs reviewed refresh')
        self.save('service-root.json', root)
        self.save('service-root-crl.json', crl)
        return root

    def prepare_intermediate(self):
        root = self.active_root()
        cursor = ''
        while True:
            page = self.api('/issuers/search', {'limit': 100, 'before': cursor})
            m.require(not any(i['trust_domain'] == 'service' and i['kind'] == 'intermediate' for i in page['items']),
                      'Service intermediate already exists; reconcile')
            cursor = page.get('next', '')
            if not cursor:
                break
        for name in SERVICE_CONSUMERS:
            service = self.obj('service', name)
            m.require(service['metadata']['namespace'] == NS and service['spec']['selector'].get('app.kubernetes.io/name') == name,
                      'Service host endpoint ownership changed')
        request = intermediate_request(root)
        self.save('intermediate-request.json', request)
        operation = self.api('/operations', request, key='dev-service-intermediate-' + uuid.uuid4().hex)
        self.save('intermediate-operation.json', operation)
        self.approval(operation)
        issuer = self.api('/issuers/' + operation['issuer_id'])
        m.require(issuer['status'] == 'approved' and issuer['signer_provider'] == 'openbao', 'intermediate reservation differs')
        m.require(issuer['signer_reference'] + '/' not in json.loads(self.bao(['secrets', 'list', '-format=json'])),
                  'intermediate provider mount already exists; reconcile')
        policies = json.loads(self.kube(['-n', NS, 'exec', 'deployment/pki-controller', '--',
                                        '/app/pkicontroller', 'render-openbao-policy', issuer['issuer_id']]))
        m.require(policies['mount'] == issuer['signer_reference'] and policies['environment'] == 'dev', 'provider policy scope differs')
        self.save('provider-policies.json', policies)
        before = json.loads(self.bao(['read', '-format=json', 'auth/kubernetes/role/pki-controller-dev']))['data']
        self.save('controller-role-before.json', before)
        self.role_policy('pki-controller-dev', 'pki-controller-dev-' + issuer['issuer_id'], policies['controller_policy'])
        self.api('/operations/' + operation['operation_id'] + '/provision', {})
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['status'] == 'provisioning' and issuer['csr_pem'], 'Service CSR not durably registered')
        self.save('intermediate-provisioning.json', issuer)
        self.verify_intermediate_custody(issuer)
        source = Path(self.args.authority)
        csr_digest = m.digest(base64.b64decode(''.join(issuer['csr_pem'].splitlines()[1:-1])))
        self.ceremony_call(['sign', '--issuer', self.output / 'intermediate-provisioning.json',
                            '--expected-request-sha256', operation['request_sha256'], '--expected-csr-sha256', csr_digest,
                            '--parent', self.output / 'service-root.json', '--expected-parent-sha256', root['certificate_fingerprint_sha256'],
                            '--key', source / 'root-offline-simulation/ca-key.encrypted.pem',
                            '--passphrase-file', m.read(source / 'passphrase-reference.json')['path'], '--out', self.output / 'intermediate-signed'])
        self.api('/operations/' + operation['operation_id'] + '/import',
                 {'certificate_pem': (self.output / 'intermediate-signed/certificate.pem').read_text()}, 204)
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['status'] == 'ready', 'Service intermediate import not ready')
        self.save('intermediate-ready.json', issuer)
        self.api('/operations/' + operation['operation_id'] + '/activate', {}, 409)
        self.check('intermediate_ready_gate_closed', {'issuer_id': issuer['issuer_id'], 'activation_without_receipts': 409,
                                                    'service_client_ids': issuer['service_client_ids'], 'server_dns_names': issuer['server_dns_names']})

    def verify_intermediate_custody(self, issuer):
        keys = json.loads(self.bao(['list', '-format=json', issuer['signer_reference'] + '/keys']))
        m.require(len(keys) == 1, 'intermediate must own exactly one internal key')
        jwt = self.kube(['-n', NS, 'create', 'token', 'pki-controller', '--audience=openbao', '--duration=10m']).strip()
        login = json.loads(self.bao(['write', '-format=json', 'auth/kubernetes/login', '-'],
                                   json.dumps({'role': 'pki-controller-dev', 'jwt': jwt})))
        token = login['auth']['client_token']
        try:
            for suffix in ('keys', 'key/' + keys[0], 'sign/server', 'sign/service-client', 'sign/default',
                           'sign-verbatim/default', 'intermediate/generate/exported'):
                result = json.loads(self.bao(['write', '-format=json', 'sys/capabilities', '-'],
                                            json.dumps({'token': token, 'paths': [issuer['signer_reference'] + '/' + suffix]})))
                m.require(result['data']['capabilities'] == (['list'] if suffix == 'keys' else ['deny']),
                          'controller key/signing capability differs from approved boundary')
        finally:
            self.bao(['write', 'auth/token/revoke', '-'], json.dumps({'token': token}))
        self.check('service_intermediate_key_custody', {'issuer_id': issuer['issuer_id'], 'internal_key_count': 1,
                  'controller_private_key_read_export_and_leaf_signing_denied': True})

    def ready_intermediate(self):
        root = self.active_root()
        source = Path(self.args.intermediate)
        saved = m.read(source / 'intermediate-ready.json')
        issuer = self.api('/issuers/' + saved['issuer_id'])
        m.require(issuer == saved and issuer['status'] == 'ready' and issuer['parent_issuer_id'] == root['issuer_id']
                  and issuer['service_client_ids'] == SERVICE_CLIENT_IDS and issuer['server_dns_names'] == SERVICE_DNS_NAMES,
                  'ready intermediate or approved policy changed; reconcile')
        operation = m.read(source / 'intermediate-operation.json')
        m.require(operation['issuer_id'] == issuer['issuer_id'], 'saved intermediate operation differs')
        return root, issuer, operation

    def install_intermediate(self, controller):
        root, issuer, operation = self.ready_intermediate()
        name = 'pki-controller' if controller else 'certissuer'
        configmap = 'pki-service-bundles-' + issuer['issuer_id'][:8]
        refs = [{'issuer_id': i['issuer_id'], 'trust_bundle_version': i['trust_bundle_version']} for i in (root, issuer)]
        if controller:
            self.create({'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': configmap, 'namespace': NS},
                         'immutable': True, 'data': {'issuers.json': json.dumps(refs)}})
        else:
            current = self.obj('configmap', configmap)
            m.require(current.get('immutable') and json.loads(current['data']['issuers.json']) == refs, 'reviewed Service manifest changed')
            self.wait_receipts(issuer['issuer_id'], issuer['trust_bundle_version'], ['pki-controller'], absent='certissuer')
        owner = self.obj('deployment', name)
        template = listener_bundle_template(owner, configmap)
        m.require(owner['spec']['replicas'] == 1, 'expected one dev listener replica')
        self.observed_patch('deployment', name, owner, [
            {'op': 'test', 'path': '/spec/template/spec/containers/0/image', 'value': template['spec']['containers'][0]['image']},
            {'op': 'test', 'path': '/spec/template/spec/volumes', 'value': owner['spec']['template']['spec']['volumes']},
            {'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + name, '--timeout=180s'], timeout=190)
        receipts = self.wait_receipts(issuer['issuer_id'], issuer['trust_bundle_version'],
                                     ['pki-controller'] if controller else SERVICE_CONSUMERS, absent='certissuer' if controller else None)
        if controller:
            self.api('/operations/' + operation['operation_id'] + '/activate', {}, 409)
        self.check(name + '_intermediate_receipt', {'issuer_id': issuer['issuer_id'], 'consumers': receipts,
                                                   'missing_certissuer_activation_denied': controller})

    def activate_intermediate(self):
        _, issuer, operation = self.ready_intermediate()
        receipts = self.wait_receipts(issuer['issuer_id'], issuer['trust_bundle_version'], SERVICE_CONSUMERS)
        self.api('/operations/' + operation['operation_id'] + '/activate', {}, 204)
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['status'] == 'active', 'Service intermediate not active')
        self.save('intermediate-active.json', issuer)
        provider = json.loads(self.bao(['read', '-format=json', issuer['signer_reference'] + '/cert/crl']))
        self.save('intermediate-provider-crl.json', provider)
        record = self.api('/issuers/' + issuer['issuer_id'] + '/crl', {'crl_pem': provider['data']['certificate']})
        self.save('intermediate-crl.json', record)
        self.verify_intermediate_custody(issuer)
        self.check('intermediate_active_with_crl', {'issuer_id': issuer['issuer_id'], 'consumers': receipts,
                  'crl_sha256': record['crl_sha256'], 'next_update': record['next_update'], 'leaf_signer_policies_granted': False})
        self.device_baseline()

    def device_baseline(self):
        device = m.read(self.foundation / 'device-2/enroll-request.json')['devid']
        auth, auth_attempts = self.wait_positive_auth(
            self.foundation / 'device-2/v4', device)
        attempts = self.wait_positive_mqtt(auth, device)
        self.mqtt(auth, device, 'roundtrip')
        self.check('device_baseline_after_service_hierarchy', {'direct_mtls': 'passed', 'mqtt_acl_qos1': 'passed',
                   'auth_attempts': auth_attempts, 'attempts': attempts,
                   'full_device_lifecycle_rerun': False})

    def wait_positive_auth(self, identity, device):
        deadline = time.monotonic() + 45
        attempts = 0
        while True:
            attempts += 1
            try:
                return self.auth(identity, device), attempts
            except RuntimeError as error:
                message = str(error)
                temporary = message == 'command failed: pki-dev-probe' or any(
                    message == 'TLS /request_token: status %d, expected 200' % status
                    for status in (401, 403))
                if not temporary or time.monotonic() >= deadline:
                    raise
                time.sleep(2)

    def create(self, obj):
        self.save('create-' + obj['metadata']['name'] + '.json', obj)
        self.kube(['-n', NS, 'create', '-f', '-'], json.dumps(obj))
        m.write(self.base / 'pki/controller-bootstrap/rollout' / (obj['metadata']['name'] + '-' + obj['kind'].lower() + '.json'), obj)

    def observed_patch(self, kind, name, before, changes):
        self.save('before-' + name + '-' + kind + '.json', before)
        return self.patch(kind, name, before, changes)

    def rollout_listener(self, controller):
        root = self.ready()
        image = self.args.image
        m.require(re.fullmatch(r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}', image or ''), 'verified dev image digest required')
        name = 'pki-controller' if controller else 'certissuer'
        owner = self.obj('deployment', name)
        template = json.loads(json.dumps(owner['spec']['template']))
        pod = template['spec']
        containers = pod['containers']
        m.require(len(containers) == 1 and containers[0]['name'] == name, 'listener container changed')
        c = containers[0]
        env = {e['name']: e.get('value') for e in c.get('env', [])}
        prefix = 'PKI_SERVICE_CLIENT' if controller else 'CERT_ISSUER_SERVICE_CLIENT'
        m.require(not any(k.startswith(prefix + '_') for k in env), 'Service client listener already configured; reconcile')
        consumer = self.base / 'pki/consumers' / name
        m.require(all((consumer / n).is_file() for n in ('ca.crt', 'tls.crt', 'tls.key')), 'prepare selected dev consumer first')
        secret_name = 'pki-service-consumer-' + name
        data = {'tls.crt': (consumer / 'tls.crt').read_text(), 'tls.key': (consumer / 'tls.key').read_text(),
                'ca.crt': (self.base / 'pki/controller-bootstrap/management-ca.crt').read_text()}
        self.create({'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': secret_name, 'namespace': NS},
                     'type': 'Opaque', 'data': {k: base64.b64encode(v.encode()).decode() for k, v in data.items()}})
        if controller:
            m.require(m.device_consumers(env) == m.CONSUMERS, 'Device consumers changed')
            self.create({'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': 'pki-service-bundles', 'namespace': NS},
                         'data': {'issuers.json': json.dumps([{'issuer_id': root['issuer_id'], 'trust_bundle_version': root['trust_bundle_version']}])}})
            tls = self.obj('secret', 'pki-controller-tls')
            ca = base64.b64decode(tls['data']['ca.crt']).decode()
            for identity in ('pki-controller', 'certissuer'):
                ca = append_pem(ca, (self.base / 'pki/consumers' / identity / 'ca.crt').read_text())
            ca = append_pem(ca, root['certificate_pem'])
            self.observed_patch('secret', 'pki-controller-tls', tls, [{'op': 'replace', 'path': '/data/ca.crt', 'value': base64.b64encode(ca.encode()).decode()}])
            self.create({'apiVersion': 'networking.k8s.io/v1', 'kind': 'NetworkPolicy',
                         'metadata': {'name': 'allow-service-listener-controller', 'namespace': NS},
                         'spec': {'podSelector': {'matchLabels': {'app.kubernetes.io/name': 'pki-controller'}}, 'policyTypes': ['Ingress'],
                                  'ingress': [{'from': [{'podSelector': {'matchExpressions': [{'key': 'app.kubernetes.io/name', 'operator': 'In', 'values': SERVICE_CONSUMERS}]}}],
                                               'ports': [{'port': 18446, 'protocol': 'TCP'}]}]}})
        else:
            current = self.obj('deployment', 'pki-controller')['spec']['template']['spec']['containers'][0]
            required = {e['name']: e.get('value') for e in current['env']}
            m.require(required.get('PKI_REQUIRED_CONSUMERS_SERVICE') == ','.join(SERVICE_CONSUMERS), 'Service consumer gate changed')
            tls = self.obj('secret', 'certissuer-runtime')
            ca = append_pem(base64.b64decode(tls['data']['client-ca.crt']).decode(), root['certificate_pem'])
            self.observed_patch('secret', 'certissuer-runtime', tls, [{'op': 'replace', 'path': '/data/client-ca.crt', 'value': base64.b64encode(ca.encode()).decode()}])
        updates = {prefix + '_ROOT_SHA256': root['certificate_fingerprint_sha256'],
                   prefix + '_BUNDLE_MANIFEST': '/run/pki-service-bundles/issuers.json',
                   prefix + '_PKI_CONTROLLER_URL': 'https://pki-controller.' + NS + '.svc:18446',
                   prefix + '_MANAGEMENT_CA': '/run/pki-service-consumer/ca.crt',
                   prefix + '_MANAGEMENT_CERT': '/run/pki-service-consumer/tls.crt',
                   prefix + '_MANAGEMENT_KEY': '/run/pki-service-consumer/tls.key'}
        if controller:
            updates.update(PKI_REQUIRED_CONSUMERS_DEVICE='video-cloud-api,pkibroker', PKI_REQUIRED_CONSUMERS_SERVICE=','.join(SERVICE_CONSUMERS))
        else:
            updates[prefix + '_PKI_ENABLED'] = 'true'
        c['env'] = with_env(c.get('env', []), updates, ('PKI_REQUIRED_CONSUMERS',) if controller else ())
        c['image'] = image
        security = pod.setdefault('securityContext', {})
        m.require(security.get('fsGroup') in (None, 10001), 'listener filesystem group changed')
        security['fsGroup'] = 10001
        c['volumeMounts'] += [{'name': 'service-bundles', 'mountPath': '/run/pki-service-bundles', 'readOnly': True},
                              {'name': 'service-consumer', 'mountPath': '/run/pki-service-consumer', 'readOnly': True}]
        pod['volumes'] += [{'name': 'service-bundles', 'configMap': {'name': 'pki-service-bundles'}},
                           {'name': 'service-consumer', 'secret': {'secretName': secret_name, 'defaultMode': 288}}]
        self.observed_patch('deployment', name, owner, [{'op': 'test', 'path': '/spec/template/spec/containers/0/image', 'value': owner['spec']['template']['spec']['containers'][0]['image']},
                                                      {'op': 'replace', 'path': '/spec/template', 'value': template}])
        # Same persistence used by dev PKI's scoped deployment/acceptance tooling.
        self.save('intended-' + name + '.json', {'image': image, 'settings': updates})
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + name, '--timeout=180s'], timeout=190)
        key = 'PKI_CONTROLLER_IMAGE' if controller else 'PKI_CERTISSUER_IMAGE'
        prior = self.base / 'operator/env' / key
        self.save('operator-before.json', {key: prior.read_text() if prior.exists() else None})
        m.write(prior, image + '\n')
        m.write(self.base / 'pki/controller-bootstrap/rollout' / (name + '-service-settings.json'), updates)
        m.require(prior.read_text().strip() == image, 'persisted image differs')
        expected = ['pki-controller'] if controller else SERVICE_CONSUMERS
        receipts = self.wait_receipts(root['issuer_id'], root['trust_bundle_version'], expected, absent='certissuer' if controller else None)
        if controller:
            self.api('/operations/' + self.root_op['operation_id'] + '/activate', {}, 409)
        self.check(name + '_root_receipt', {'issuer_id': root['issuer_id'], 'image': image, 'consumers': receipts,
                                           'missing_certissuer_activation_denied': controller})

    def activate_root(self):
        root = self.ready()
        receipts = self.wait_receipts(root['issuer_id'], root['trust_bundle_version'], SERVICE_CONSUMERS)
        self.api('/operations/' + self.root_op['operation_id'] + '/activate', {}, 204)
        root = self.api('/issuers/' + root['issuer_id'])
        m.require(root['status'] == 'active', 'Service Root not active')
        self.save('root-active.json', root)
        now = dt.datetime.now(dt.timezone.utc).replace(microsecond=0)
        request = {'issuer_id': root['issuer_id'], 'issuer_fingerprint_sha256': root['certificate_fingerprint_sha256'],
                   'crl_number': '1', 'this_update': m.stamp(now), 'next_update': m.stamp(now + dt.timedelta(days=1)), 'revocations': []}
        self.save('root-crl-request.json', request)
        expected = self.ceremony_call(['crl-digest', self.output / 'root-crl-request.json']).strip()
        source = Path(self.args.authority)
        self.ceremony_call(['crl', '--issuer', self.output / 'root-active.json', '--crl-request', self.output / 'root-crl-request.json',
                            '--expected-request-sha256', expected, '--key', source / 'root-offline-simulation/ca-key.encrypted.pem',
                            '--passphrase-file', m.read(source / 'passphrase-reference.json')['path'], '--out', self.output / 'root-crl'])
        self.import_saved_root_crl(self.output, root, receipts)

    def import_saved_root_crl(self, source, root, receipts):
        manifest = m.read(source / 'root-crl/public-manifest.json')
        request = m.read(source / 'root-crl-request.json')
        raw = (source / 'root-crl/revocations.pem').read_text()
        crl_der = base64.b64decode(''.join(raw.splitlines()[1:-1]), validate=True)
        expected = self.ceremony_call(['crl-digest', source / 'root-crl-request.json']).strip()
        m.require(manifest['request'] == request and manifest['request_sha256'] == expected
                  and manifest['crl_sha256'] == m.digest(crl_der), 'saved CRL evidence changed')
        m.require(request['issuer_id'] == root['issuer_id'] and request['issuer_fingerprint_sha256'] == root['certificate_fingerprint_sha256']
                  and request['crl_number'] == '1' and not request['revocations'], 'initial Root CRL scope changed')
        issuer_id = root['issuer_id']
        m.require(str(uuid.UUID(issuer_id)) == issuer_id, 'invalid Root identifier')
        count = self.kube(['-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--',
                          'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'video_cloud', '-At'],
                         "SELECT count(*) FROM pki_crls WHERE issuer_id='" + issuer_id + "';").strip()
        if count == '0':
            record = self.api('/issuers/' + issuer_id + '/crl', {'crl_pem': raw})
        else:
            record = self.api('/issuers/' + issuer_id + '/crl')
        m.require(record['crl_sha256'] == manifest['crl_sha256'], 'live CRL differs; do not replay signing or import')
        self.save('root-crl.json', record)
        self.check('root_active_with_crl', {'issuer_id': issuer_id, 'consumers': receipts, 'crl_sha256': record['crl_sha256'],
                                          'signed_evidence': str(source), 'resigned': False})
        self.device_baseline()

    def finish_root_crl(self):
        source = Path(self.args.activation)
        prior = m.read(source / 'report.json')
        m.require(prior['status'] == 'failed' and prior['phase'] == 'activate-root', 'failed activation evidence required')
        root = m.read(source / 'root-active.json')
        saved = m.read(Path(self.args.authority) / 'root-ready.json')
        m.require(root['issuer_id'] == saved['issuer_id'] and root['status'] == 'active'
                  and root == self.api('/issuers/' + root['issuer_id']), 'active Root differs from saved operation')
        receipts = self.wait_receipts(root['issuer_id'], root['trust_bundle_version'], SERVICE_CONSUMERS)
        self.save('root-active.json', root)
        self.report['reconciled_from'] = str(source)
        self.import_saved_root_crl(source, root, receipts)


    def verify_runtime(self):
        saved = m.read(Path(self.args.authority) / 'root-ready.json')
        root = self.api('/issuers/' + saved['issuer_id'])
        m.require(root['status'] == 'active' and root['certificate_fingerprint_sha256'] == saved['certificate_fingerprint_sha256']
                  and root['trust_domain'] == 'service' and root['environment'] == 'dev', 'Service Root changed')
        record = self.api('/issuers/' + root['issuer_id'] + '/crl')
        m.require(dt.datetime.fromisoformat(record['next_update'].replace('Z', '+00:00')) > dt.datetime.now(dt.timezone.utc),
                  'Service Root CRL expired')
        receipts = self.wait_receipts(root['issuer_id'], root['trust_bundle_version'], SERVICE_CONSUMERS)
        for name in SERVICE_CONSUMERS:
            desired = render_persisted_listener(self.base, name)
            current = self.obj('deployment', name)
            wanted = desired['spec']['template']['spec']['containers'][0]
            actual = current['spec']['template']['spec']['containers'][0]
            m.require(actual['image'] == wanted['image'] and {e['name']: e for e in actual['env']} == {e['name']: e for e in wanted['env']}
                      and actual['volumeMounts'] == wanted['volumeMounts']
                      and current['spec']['template']['spec']['volumes'] == desired['spec']['template']['spec']['volumes'],
                      'live/persisted listener configuration differs')
            self.save('rendered-' + name + '.json', desired)
            self.kube(['-n', NS, 'exec', 'deployment/' + name, '--', 'sh', '-c', 'test -x /app/pkimanagement'])
        source = Path(self.args.authority)
        passfile = m.read(source / 'passphrase-reference.json')['path']
        keyfile = source / 'root-offline-simulation/ca-key.encrypted.pem'
        m.require(keyfile.stat().st_mode & 0o777 == 0o600 and Path(passfile).stat().st_mode & 0o777 == 0o600, 'offline material permissions changed')
        self.save('root-public.pem', root['certificate_pem'])
        public = m.command([self.openssl, 'x509', '-in', self.output / 'root-public.pem', '-pubkey', '-noout'])
        key_public = m.command([self.openssl, 'pkey', '-in', keyfile, '-passin', 'file:' + passfile, '-pubout'])
        m.require(public == key_public, 'offline Root key correspondence failed')
        self.save('root-active.json', root)
        self.save('root-crl.json', record)
        self.check('service_root_runtime_audit', {'issuer_id': root['issuer_id'], 'consumers': receipts,
                  'crl_sha256': record['crl_sha256'], 'crl_next_update': record['next_update'],
                  'offline_key_matches': True, 'persisted_render_matches_live': True, 'managed_owner_binary_present': True,
                  'managed_callers': 'outside hierarchy audit scope'})
        if self.args.intermediate:
            saved_intermediate = m.read(Path(self.args.intermediate) / 'intermediate-ready.json')
            issuer = self.api('/issuers/' + saved_intermediate['issuer_id'])
            m.require(issuer['status'] == 'active' and issuer['parent_issuer_id'] == root['issuer_id']
                      and issuer['certificate_fingerprint_sha256'] == saved_intermediate['certificate_fingerprint_sha256']
                      and issuer['service_client_ids'] == SERVICE_CLIENT_IDS and issuer['server_dns_names'] == SERVICE_DNS_NAMES,
                      'active intermediate policy or certificate differs')
            crl = self.api('/issuers/' + issuer['issuer_id'] + '/crl')
            m.require(dt.datetime.fromisoformat(crl['next_update'].replace('Z', '+00:00')) > dt.datetime.now(dt.timezone.utc),
                      'intermediate CRL expired')
            acks = self.wait_receipts(issuer['issuer_id'], issuer['trust_bundle_version'], SERVICE_CONSUMERS)
            configmap = 'pki-service-bundles-' + issuer['issuer_id'][:8]
            manifest = self.obj('configmap', configmap)
            refs = [{'issuer_id': i['issuer_id'], 'trust_bundle_version': i['trust_bundle_version']} for i in (root, issuer)]
            m.require(manifest.get('immutable') and json.loads(manifest['data']['issuers.json']) == refs, 'intermediate bundle changed')
            for name in SERVICE_CONSUMERS:
                pod = self.obj('deployment', name)['spec']['template']['spec']
                selected = [v for v in pod['volumes'] if v['name'] == 'service-bundles']
                m.require(len(selected) == 1 and selected[0]['configMap']['name'] == configmap, 'listener uses another bundle')
            self.verify_intermediate_custody(issuer)
            for role_name in ('server', 'service-client'):
                role = json.loads(self.bao(['read', '-format=json', issuer['signer_reference'] + '/roles/' + role_name]))['data']
                verify_provider_role(role, role_name == 'service-client')
                selected = role['issuer_ref']
                m.require(selected == 'default' or re.fullmatch('[0-9a-f-]{36}', selected), 'invalid provider selected issuer')
                provider_issuer = json.loads(self.bao(['read', '-format=json', issuer['signer_reference'] + '/issuer/' + selected]))['data']
                cert = provider_issuer['certificate']
                fingerprint = m.digest(base64.b64decode(''.join(cert.strip().splitlines()[1:-1]), validate=True))
                m.require(fingerprint == issuer['certificate_fingerprint_sha256'], 'provider role selects another issuer')
                self.save('provider-role-' + role_name + '.json', role)
                self.save('provider-issuer-' + role_name + '.json', provider_issuer)
            self.save('intermediate-active.json', issuer)
            self.save('intermediate-crl.json', crl)
            self.check('service_intermediate_runtime_audit', {'issuer_id': issuer['issuer_id'], 'consumers': acks,
                      'crl_sha256': crl['crl_sha256'], 'crl_next_update': crl['next_update'], 'persisted_manifest_matches_live': True,
                      'provider_server_and_client_profiles_match': True, 'managed_leaves': 'outside hierarchy audit scope'})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', choices=['prepare-root', 'controller', 'certissuer', 'activate-root', 'finish-root-crl', 'verify',
                                          'prepare-intermediate', 'intermediate-controller', 'intermediate-certissuer', 'activate-intermediate'], required=True)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--authority')
    parser.add_argument('--image')
    parser.add_argument('--activation')
    parser.add_argument('--intermediate')
    args = parser.parse_args()
    m.require(args.phase == 'prepare-root' or args.authority, 'prior prepared Root evidence required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    m.require(args.phase != 'finish-root-crl' or args.activation, 'saved signed activation evidence required')
    m.require(args.phase not in ('intermediate-controller', 'intermediate-certissuer', 'activate-intermediate') or args.intermediate,
              'saved ready intermediate evidence required')
    runner = ServiceRun(args)
    try:
        runner.preflight()
        {'prepare-root': runner.prepare_root, 'controller': lambda: runner.rollout_listener(True),
         'certissuer': lambda: runner.rollout_listener(False), 'activate-root': runner.activate_root, 'finish-root-crl': runner.finish_root_crl, 'verify': runner.verify_runtime,
         'prepare-intermediate': runner.prepare_intermediate, 'intermediate-controller': lambda: runner.install_intermediate(True),
         'intermediate-certissuer': lambda: runner.install_intermediate(False), 'activate-intermediate': runner.activate_intermediate}[args.phase]()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'
        runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        os.close(owner)
        print(json.dumps({'phase': args.phase, 'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    def interrupted(*_):
        raise RuntimeError('interrupted; reconcile the saved phase before retry')
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
