#!/usr/bin/env python3
"""Install the dev-only Account Manager Service mTLS listener without exporting keys."""
import argparse
import base64
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import uuid


spec = importlib.util.spec_from_file_location('managed_account', Path(__file__).with_name('management.py'))
mg = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mg)
m, h, NS, AM_NS = mg.m, mg.h, mg.NS, mg.AM_NS

ACCOUNT_DNS = 'account-manager-internal.video-cloud-dev-account-manager.svc'
SERVICE_NAME = 'account-manager-internal'
BUNDLE_NAME = 'account-manager-service-bundles-v3-e0617745'
OLD_CRL_NAME = 'account-manager-service-crls-v3-e0617745'
CRL_NAME = OLD_CRL_NAME + '-r2'
SEED_DIR = '/var/lib/account-pki/private/account-listener-seed'
SEED_CERT, SEED_KEY = SEED_DIR + '/chain.pem', SEED_DIR + '/key.pem'
STATE = '/var/lib/account-pki/private/account-listener-state.json'


def listener_template(owner, image, root):
    template = json.loads(json.dumps(owner['spec']['template']))
    pod = template['spec']
    containers = pod['containers']
    m.require([c['name'] for c in containers] == ['app', 'pkimanagement'], 'Account Manager topology changed')
    sidecar = containers[1]
    m.require(not any(e['name'].startswith('PKI_MANAGEMENT_ACCOUNT_') for e in sidecar.get('env', [])),
              'Account Manager listener is already configured')
    m.require(not any(v['name'] in ('account-service-bundles', 'account-service-crls') for v in pod['volumes']),
              'Account Manager listener volumes already exist')
    sidecar['image'] = image
    sidecar['ports'] = [{'name': 'account-service', 'containerPort': 8443, 'protocol': 'TCP'}]
    settings = {
        'PKI_MANAGEMENT_ACCOUNT_LISTEN_ADDR': ':8443',
        'PKI_MANAGEMENT_ACCOUNT_UPSTREAM': 'http://127.0.0.1:8080',
        'PKI_MANAGEMENT_ACCOUNT_CLIENT_CA': '/run/pki-root/root.pem',
        'PKI_MANAGEMENT_ACCOUNT_CLIENT_ROOT_SHA256': root,
        'PKI_MANAGEMENT_ACCOUNT_HOST_SEED_CERT': SEED_CERT,
        'PKI_MANAGEMENT_ACCOUNT_HOST_SEED_KEY': SEED_KEY,
        'PKI_MANAGEMENT_ACCOUNT_HOST_IDENTITY_STATE': STATE,
        'PKI_MANAGEMENT_ACCOUNT_HOST_NAME': ACCOUNT_DNS,
        'PKI_MANAGEMENT_ACCOUNT_HOST_DNS_NAMES': ACCOUNT_DNS,
        'PKI_MANAGEMENT_ACCOUNT_HOST_ROOT_SHA256': root,
        'PKI_MANAGEMENT_ACCOUNT_HOST_RENEWAL_URL': 'https://certissuer.' + NS + '.svc:9443',
        'PKI_MANAGEMENT_ACCOUNT_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256': root,
        'PKI_MANAGEMENT_ACCOUNT_HOST_RENEWAL_SERVER_PKI_NAME': 'certissuer.' + NS + '.svc',
        'PKI_MANAGEMENT_ACCOUNT_HOST_RENEWAL_TLS_CA': '/run/pki-root/root.pem',
        'PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_BUNDLE_MANIFEST': '/run/pki-service-bundles/issuers.json',
        'PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST': '/run/pki-service-crls/crls.json',
    }
    sidecar['env'] = h.h.with_env(sidecar.get('env', []), settings)
    sidecar['volumeMounts'] += [
        {'name': 'account-service-bundles', 'mountPath': '/run/pki-service-bundles', 'readOnly': True},
        {'name': 'account-service-crls', 'mountPath': '/run/pki-service-crls', 'readOnly': True},
    ]
    pod['volumes'] += [
        {'name': 'account-service-bundles', 'configMap': {'name': BUNDLE_NAME}},
        {'name': 'account-service-crls', 'configMap': {'name': CRL_NAME}},
    ]
    return template


def without_seed(template):
    result = json.loads(json.dumps(template))
    sidecar = next(c for c in result['spec']['containers'] if c['name'] == 'pkimanagement')
    sidecar['env'] = [e for e in sidecar['env'] if e['name'] not in (
        'PKI_MANAGEMENT_ACCOUNT_HOST_SEED_CERT', 'PKI_MANAGEMENT_ACCOUNT_HOST_SEED_KEY')]
    return result


def account_crls(entries):
    result = []
    for entry in entries:
        issuer = entry['issuer']
        m.require(issuer['trust_domain'] == 'service' and issuer['environment'] == 'dev'
                  and issuer['status'] in ('active', 'retiring'), 'Service CRL manifest changed')
        result.append({'issuer': issuer,
                       'state_path': '/var/lib/account-pki/private/account-listener-crl-' + issuer['issuer_id'] + '.json'})
    return result


class ListenerRun(mg.ManagementRun):
    def __init__(self, args):
        m.Acceptance.__init__(self, args.config_root, 'lke649805-ctx', args.output,
                              resume=True if (args.resume or args.resume_after_deploy or args.resume_after_listener) else False)
        self.args = args
        self.report['foundation_scope'] = 'Active dev Service v3; Account Manager internal mTLS listener'
        self.report['checks'] = {}
        self.report['phase'] = 'install'
        self.report['account_listener_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def scoped_patch(self, kind, before, changes):
        name, ns = before['metadata']['name'], before['metadata']['namespace']
        m.require(ns in (NS, AM_NS), 'unexpected mutation namespace')
        self.save('before-' + ns + '-' + name + '-' + kind + '.json', before)
        patch = [{'op': 'test', 'path': '/metadata/resourceVersion', 'value': before['metadata']['resourceVersion']}] + changes
        updated = json.loads(self.kube(['-n', ns, 'patch', kind, name, '--type=json', '--patch-file=/dev/stdin', '-o', 'json'], json.dumps(patch)))
        desired = {key: updated[key] for key in ('apiVersion', 'kind', 'spec', 'data', 'type', 'immutable') if key in updated}
        desired['metadata'] = {key: updated['metadata'][key] for key in ('name', 'namespace', 'labels', 'annotations') if key in updated['metadata']}
        self.save('after-' + ns + '-' + name + '-' + kind + '.json', desired)
        m.write(self.base / 'pki/controller-bootstrap/rollout' / (name + '-' + kind.lower() + '.json'), desired)
        return updated

    def create(self, obj):
        ns, name = obj['metadata']['namespace'], obj['metadata']['name']
        m.require(ns == AM_NS, 'listener objects must stay in Account Manager dev namespace')
        self.save('create-' + name + '.json', obj)
        created = json.loads(self.kube(['-n', ns, 'create', '-f', '-', '-o', 'json'], json.dumps(obj)))
        self.save('created-' + name + '.json', created)
        m.write(self.base / 'pki/controller-bootstrap/rollout' / (name + '-' + obj['kind'].lower() + '.json'), obj)
        return created

    def v3(self):
        source = Path(self.args.activation)
        report = m.read(source / 'report.json')
        m.require(report['status'] == 'passed', 'successful Service v3 activation evidence required')
        issuer = m.read(source / 'intermediate-v3-active.json')
        current = self.api('/issuers/' + issuer['issuer_id'])
        m.require(current == issuer and current['status'] == 'active' and current['issuer_version'] == 3,
                  'active Service v3 differs from saved activation')
        m.require('service:video-cloud-api' in current['service_client_ids'] and ACCOUNT_DNS in current['server_dns_names'],
                  'Service v3 does not authorize Account Manager listener')
        return current

    def preflight_listener(self):
        m.require(self.kube(['config', 'current-context']).strip() == self.context, 'canonical dev context mismatch')
        for name, ns in [('account-manager', AM_NS), ('certissuer', NS), ('pki-controller', NS)]:
            deployment = self.obj('deployment', name, ns)
            m.require(deployment['spec']['replicas'] == 1 and deployment.get('status', {}).get('readyReplicas') == 1
                      and deployment['status'].get('observedGeneration') == deployment['metadata']['generation'],
                      'deployment is not ready: ' + name)
        account = self.obj('deployment', 'account-manager', AM_NS)
        sidecar = next(c for c in account['spec']['template']['spec']['containers'] if c['name'] == 'pkimanagement')
        env = {e['name']: e.get('value') for e in sidecar['env']}
        m.require(env.get('PKI_MANAGEMENT_IDENTITY_STATE') == mg.STATE and not any(k.startswith('PKI_MANAGEMENT_ACCOUNT_') for k in env),
                  'unexpected Account Manager managed identity state')
        self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--', 'openssl', 'version'])
        self.forward('issuer', NS, 'certissuer', 9443)
        workspace = Path(__file__).resolve().parents[2]
        self.forward('am', AM_NS, 'account-manager', 80)
        self.accounts = m.read(self.foundation / 'accounts.json')
        result = subprocess.run(['go', 'build', '-o', str(self.probe), './pki-dev-probe'], cwd=workspace / 'scripts/go',
                                  env=dict(os.environ, GOWORK='off'), capture_output=True, timeout=180)
        m.require(result.returncode == 0, 'listener probe build failed')
        self.forward('api', NS, 'video-cloud-api-pki', 8443)
        self.forward('mqtt', NS, 'mqtt-pki', 8883)
        self.check('preflight', {'context': self.context, 'account_listener_preexisting': False,
                                 'active_service_v3': self.v3()['issuer_id'], 'staging_touched': False})

    def issuer_template(self, issuer, bootstrap=False):
        template = json.loads(json.dumps(issuer['spec']['template']))
        container = template['spec']['containers'][0]
        env = {e['name']: e.get('value') for e in container['env']}
        baseline_dns = 'certissuer.' + NS + '.svc,pki-controller.' + NS + '.svc'
        expected_dns = baseline_dns if bootstrap else baseline_dns + ',' + ACCOUNT_DNS
        expected_pattern = ('^service:(certissuer|pki-controller)$' if bootstrap else
                            '^(?:service:(?:certissuer|pki-controller|account-manager)|dev-account-manager-listener-provisioner)$')
        m.require(env.get('CERT_ISSUER_GATEWAY_DNS_NAMES') == expected_dns
                  and env.get('CERT_ISSUER_GATEWAY_CLIENT_CN_PATTERN') == expected_pattern,
                  'certissuer gateway policy changed')
        pattern = '^(?:service:(?:certissuer|pki-controller|account-manager)' + ('|dev-account-manager-listener-provisioner' if bootstrap else '') + ')$'
        container['env'] = h.h.with_env(container['env'], {
            'CERT_ISSUER_GATEWAY_DNS_NAMES': baseline_dns + ',' + ACCOUNT_DNS,
            'CERT_ISSUER_GATEWAY_CLIENT_CN_PATTERN': pattern})
        return template

    def install_temporary_issuer_access(self):
        self.save('bootstrap.ext', 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=clientAuth\n')
        for name in ('listener-bootstrap-ca', 'listener-bootstrap'):
            m.command([self.openssl, 'genpkey', '-algorithm', 'EC', '-pkeyopt', 'ec_paramgen_curve:P-256', '-out', self.output / (name + '.key')])
        m.command([self.openssl, 'req', '-new', '-x509', '-key', self.output / 'listener-bootstrap-ca.key', '-days', '1',
                   '-subj', '/CN=dev-account-manager-listener-bootstrap-ca', '-addext', 'basicConstraints=critical,CA:TRUE,pathlen:0',
                   '-addext', 'keyUsage=critical,keyCertSign,cRLSign', '-out', self.output / 'listener-bootstrap-ca.crt'])
        m.command([self.openssl, 'req', '-new', '-key', self.output / 'listener-bootstrap.key',
                   '-subj', '/CN=dev-account-manager-listener-provisioner', '-out', self.output / 'listener-bootstrap.csr'])
        m.command([self.openssl, 'x509', '-req', '-in', self.output / 'listener-bootstrap.csr', '-CA', self.output / 'listener-bootstrap-ca.crt',
                   '-CAkey', self.output / 'listener-bootstrap-ca.key', '-set_serial', '1', '-days', '1', '-extfile', self.output / 'bootstrap.ext',
                   '-out', self.output / 'listener-bootstrap.crt'])
        runtime = self.obj('secret', 'certissuer-runtime')
        original = base64.b64decode(runtime['data']['client-ca.crt']).decode()
        updated = h.h.append_pem(original, (self.output / 'listener-bootstrap-ca.crt').read_text())
        self.scoped_patch('secret', runtime, [{'op': 'test', 'path': '/data/client-ca.crt', 'value': runtime['data']['client-ca.crt']},
                                              {'op': 'replace', 'path': '/data/client-ca.crt', 'value': base64.b64encode(updated.encode()).decode()}])
        issuer = self.obj('deployment', 'certissuer')
        template = self.issuer_template(issuer, bootstrap=True)
        self.scoped_patch('deployment', issuer, [{'op': 'test', 'path': '/spec/template', 'value': issuer['spec']['template']},
                                                  {'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/certissuer', '--timeout=300s'], timeout=310)
        self.forward('issuer', NS, 'certissuer', 9443)
        self.check('temporary_seed_issuer_access', {'bootstrap_private_key_exported': False, 'temporary_trust': True})

    def issue_seed(self, issuer):
        self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--', 'sh', '-c',
                   'set -eu; umask 077; test ! -e ' + SEED_KEY + '; mkdir -p ' + SEED_DIR + '; '
                   'openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out ' + SEED_KEY + '; '
                   'openssl req -new -key ' + SEED_KEY + ' -subj /CN=' + ACCOUNT_DNS + ' -addext subjectAltName=DNS:' + ACCOUNT_DNS + ' -out ' + SEED_DIR + '/csr.pem'])
        csr = self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--', 'cat', SEED_DIR + '/csr.pem'])
        request = {'request_id': 'dev-account-listener-' + uuid.uuid4().hex, 'csr_pem': csr, 'ttl_days': 30, 'purpose': 'server'}
        self.save('seed-request.json', request)
        response = json.loads(m.command([self.probe, 'tls', self.output / 'root.pem', self.output / 'listener-bootstrap.crt',
                                         self.output / 'listener-bootstrap.key', 'certissuer.' + NS + '.svc', self.ports['issuer'][0],
                                         '/v1/certificates/gateway/issue'], json.dumps(request)))
        m.require(response['status'] == 200, 'Account Manager listener seed issuance failed')
        issued = json.loads(response['body'])
        m.require(issued['caller_identity'] == 'dev-account-manager-listener-provisioner' and issued['dns_names'] == [ACCOUNT_DNS],
                  'listener seed issuer response differs')
        self.save('seed-issued.json', {k: issued[k] for k in ('request_id', 'caller_identity', 'dns_names', 'serial_number', 'not_before', 'not_after', 'issued_at')})
        chain = issued['certificate_chain_pem']
        self.kube(['-n', AM_NS, 'exec', '-i', 'deployment/account-manager', '-c', 'pkimanagement', '--', 'sh', '-c',
                   'set -eu; umask 077; test ! -e ' + SEED_CERT + '; cat > ' + SEED_CERT], chain)
        self.check('server_seed_issued', {'issuer_id': issuer['issuer_id'], 'dns_name': ACCOUNT_DNS, 'seed_key_exported': False})

    def verify_saved_seed(self, issuer):
        issued = m.read(self.output / 'seed-issued.json')
        m.require(issued['caller_identity'] == 'dev-account-manager-listener-provisioner'
                  and issued['dns_names'] == [ACCOUNT_DNS], 'saved listener seed evidence differs')
        current = self.obj('deployment', 'certissuer')
        self.issuer_template(current, bootstrap=False)
        self.check('server_seed_reconciled', {'issuer_id': issuer['issuer_id'], 'seed_key_exported': False,
                                              'issuance_replayed': False, 'PVC_file_check': 'deferred until sidecar restart'})

    def create_listener_objects(self, issuer):
        source_bundle = self.obj('configmap', 'pki-service-bundles-v3-e0617745')
        refs = json.loads(source_bundle['data']['issuers.json'])
        m.require(source_bundle.get('immutable') and any(ref['issuer_id'] == issuer['issuer_id'] for ref in refs),
                  'active Service v3 bundle changed')
        source_crl = self.obj('configmap', 'pki-service-client-crls')
        entries = json.loads(source_crl['data']['crls.json'])
        self.create({'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': BUNDLE_NAME, 'namespace': AM_NS},
                     'immutable': True, 'data': {'issuers.json': json.dumps(refs)}})
        self.create({'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': CRL_NAME, 'namespace': AM_NS},
                     'immutable': True, 'data': {'crls.json': json.dumps(account_crls(entries))}})
        self.create({'apiVersion': 'v1', 'kind': 'Service', 'metadata': {'name': SERVICE_NAME, 'namespace': AM_NS},
                     'spec': {'selector': {'app.kubernetes.io/name': 'account-manager'},
                              'ports': [{'name': 'https', 'port': 8443, 'targetPort': 'account-service', 'protocol': 'TCP'}]}})
        self.create({'apiVersion': 'networking.k8s.io/v1', 'kind': 'NetworkPolicy',
                     'metadata': {'name': 'allow-account-manager-service-clients', 'namespace': AM_NS},
                     'spec': {'podSelector': {'matchLabels': {'app.kubernetes.io/name': 'account-manager'}}, 'policyTypes': ['Ingress'],
                              'ingress': [{'from': [{'namespaceSelector': {'matchLabels': {'kubernetes.io/metadata.name': NS}},
                                                     'podSelector': {'matchExpressions': [{'key': 'app.kubernetes.io/name', 'operator': 'In', 'values': ['video-cloud-api', 'factoryenroll']}]}}],
                                           'ports': [{'port': 8443, 'protocol': 'TCP'}]}]}})

    def install_listener(self, issuer):
        owner = self.obj('deployment', 'account-manager', AM_NS)
        image = self.args.image
        m.require(h.h.re.fullmatch(r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}', image or ''),
                  'verified dev Video Cloud listener image required')
        template = listener_template(owner, image, self.active_root()['certificate_fingerprint_sha256'])
        self.scoped_patch('deployment', owner, [{'op': 'test', 'path': '/spec/template', 'value': owner['spec']['template']},
                                                {'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', AM_NS, 'rollout', 'status', 'deployment/account-manager', '--timeout=360s'], timeout=370)
        self.verify_listener()

    def verify_listener(self):
        self.kube(['-n', AM_NS, 'get', 'endpointslice', '-l', 'kubernetes.io/service-name=' + SERVICE_NAME, '-o', 'json'])
        state = self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--', 'sh', '-c',
                           'stat -c %a /var/lib/account-pki/private; stat -c %a ' + STATE + '; sha256sum ' + STATE])
        self.save('listener-state.json', {'sha256': h.private_state_digest(state)})
        request = m.read(self.output / 'seed-issued.json')['request_id']
        row = self.sql("SELECT status || '|' || caller FROM pki_server_issuances WHERE request_id='" + request + "';")
        m.require(row == 'succeeded|dev-account-manager-listener-provisioner',
                  'listener server issuance record differs')
        self.check('listener_serving', {'service_endpoint_ready': True, 'listener_state_persistent': True,
                                        'server_issuance_registered': True, 'private_key_exported': False,
                                        'caller_admission': 'verified with real API identity next'})

    def seal(self):
        owner = self.obj('deployment', 'account-manager', AM_NS)
        template = without_seed(owner['spec']['template'])
        self.scoped_patch('deployment', owner, [{'op': 'test', 'path': '/spec/template', 'value': owner['spec']['template']},
                                                {'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', AM_NS, 'rollout', 'status', 'deployment/account-manager', '--timeout=360s'], timeout=370)
        self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--', 'sh', '-c',
                   'set -eu; rm -f ' + SEED_CERT + ' ' + SEED_KEY + ' ' + SEED_DIR + '/csr.pem; rmdir ' + SEED_DIR + '; test ! -e ' + SEED_KEY])
        self.verify_listener()
        state = self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--', 'sh', '-c',
                           'stat -c %a /var/lib/account-pki/private; stat -c %a ' + STATE + '; sha256sum ' + STATE])
        m.require(h.private_state_digest(state) == m.read(self.output / 'listener-state.json')['sha256'],
                  'listener state changed across bootstrap-free restart')
        runtime = self.obj('secret', 'certissuer-runtime')
        temporary = (self.output / 'listener-bootstrap-ca.crt').read_text().strip()
        trusted = base64.b64decode(runtime['data']['client-ca.crt']).decode()
        m.require(trusted.count(temporary) == 1, 'temporary bootstrap trust changed')
        final = trusted.replace(temporary, '').strip() + '\n'
        self.scoped_patch('secret', runtime, [{'op': 'test', 'path': '/data/client-ca.crt', 'value': runtime['data']['client-ca.crt']},
                                              {'op': 'replace', 'path': '/data/client-ca.crt', 'value': base64.b64encode(final.encode()).decode()}])
        issuer = self.obj('deployment', 'certissuer')
        template = self.issuer_template(issuer, bootstrap=False)
        self.scoped_patch('deployment', issuer, [{'op': 'test', 'path': '/spec/template', 'value': issuer['spec']['template']},
                                                  {'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/certissuer', '--timeout=300s'], timeout=310)
        self.forward('am', AM_NS, 'account-manager', 80)
        self.accounts = m.read(self.foundation / 'accounts.json')
        self.login(self.accounts['requester'])
        self.check('bootstrap_removed', {'listener_restart_without_seed': True, 'temporary_issuer_trust_removed': True,
                                         'login_mfa_changed': False, 'staging_touched': False})

    def install(self):
        self.preflight_listener()
        issuer = self.v3()
        self.save('root.pem', self.active_root()['certificate_pem'])
        self.install_temporary_issuer_access()
        self.issue_seed(issuer)
        self.create_listener_objects(issuer)
        self.install_listener(issuer)
        self.seal()
        self.device_baseline()
        self.check('complete', {'service': SERVICE_NAME, 'issuer_id': issuer['issuer_id'], 'seed_removed': True,
                                'device_baseline': 'passed', 'api_and_factory_callers': 'next T7 work'})

    def resume_install(self):
        self.preflight_listener()
        issuer = self.v3()
        self.save('root.pem', self.active_root()['certificate_pem'])
        self.verify_saved_seed(issuer)
        self.create_listener_objects(issuer)
        self.install_listener(issuer)
        self.seal()
        self.device_baseline()
        self.check('complete', {'service': SERVICE_NAME, 'issuer_id': issuer['issuer_id'], 'seed_removed': True,
                                'device_baseline': 'passed', 'api_and_factory_callers': 'next T7 work',
                                'seed_issuance_replayed': False})

    def recovery_preflight(self):
        m.require(self.kube(['config', 'current-context']).strip() == self.context, 'canonical dev context mismatch')
        self.forward('am', AM_NS, 'account-manager', 80)
        self.accounts = m.read(self.foundation / 'accounts.json')
        workspace = Path(__file__).resolve().parents[2]
        result = subprocess.run(['go', 'build', '-o', str(self.probe), './pki-dev-probe'], cwd=workspace / 'scripts/go',
                                env=dict(os.environ, GOWORK='off'), capture_output=True, timeout=180)
        m.require(result.returncode == 0, 'listener probe build failed')
        self.forward('api', NS, 'video-cloud-api-pki', 8443)
        self.forward('mqtt', NS, 'mqtt-pki', 8883)
        self.check('recovery_preflight', {'context': self.context, 'staging_touched': False})

    def recover_listener_deployment(self):
        owner = self.obj('deployment', 'account-manager', AM_NS)
        volumes = owner['spec']['template']['spec']['volumes']
        selected = [v for v in volumes if v['name'] == 'account-service-crls']
        m.require(len(selected) == 1 and selected[0].get('configMap', {}).get('name') == OLD_CRL_NAME,
                  'failed listener CRL mount changed')
        old = self.obj('configmap', OLD_CRL_NAME, AM_NS)
        entries = json.loads(old['data']['crls.json'])
        self.create({'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': CRL_NAME, 'namespace': AM_NS},
                     'immutable': True, 'data': {'crls.json': json.dumps(account_crls(entries))}})
        template = json.loads(json.dumps(owner['spec']['template']))
        target = next(v for v in template['spec']['volumes'] if v['name'] == 'account-service-crls')
        target['configMap']['name'] = CRL_NAME
        self.scoped_patch('deployment', owner, [{'op': 'test', 'path': '/spec/template', 'value': owner['spec']['template']},
                                                {'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', AM_NS, 'rollout', 'status', 'deployment/account-manager', '--timeout=360s'], timeout=370)
        self.verify_listener()
        current = self.obj('configmap', OLD_CRL_NAME, AM_NS)
        options = {'apiVersion': 'v1', 'kind': 'DeleteOptions', 'preconditions': {k: current['metadata'][k] for k in ('uid', 'resourceVersion')}}
        self.kube(['delete', '--raw', '/api/v1/namespaces/' + AM_NS + '/configmaps/' + OLD_CRL_NAME, '-f', '-'], json.dumps(options))
        stale = self.base / 'pki/controller-bootstrap/rollout' / (OLD_CRL_NAME + '-configmap.json')
        if stale.exists():
            stale.unlink()
        self.check('listener_crl_path_recovered', {'state_paths': 'owner-private flat files', 'failed_manifest_deleted': True})

    def resume_after_deploy(self):
        self.recovery_preflight()
        issuer = m.read(Path(self.args.activation) / 'intermediate-v3-active.json')
        m.require(issuer['status'] == 'active' and issuer['issuer_version'] == 3 and ACCOUNT_DNS in issuer['server_dns_names'],
                  'saved active Service v3 listener policy differs')
        m.require((self.output / 'root.pem').is_file(), 'saved Service Root evidence required for listener recovery')
        self.verify_saved_seed(issuer)
        self.recover_listener_deployment()
        self.seal()
        self.device_baseline()
        self.check('complete', {'service': SERVICE_NAME, 'issuer_id': issuer['issuer_id'], 'seed_removed': True,
                                'device_baseline': 'passed', 'api_and_factory_callers': 'next T7 work',
                                'seed_issuance_replayed': False})

    def resume_after_listener(self):
        self.recovery_preflight()
        issuer = self.v3()
        self.save('root.pem', self.active_root()['certificate_pem'])
        self.verify_listener()
        self.seal()
        self.device_baseline()
        self.check('complete', {'service': SERVICE_NAME, 'issuer_id': issuer['issuer_id'], 'seed_removed': True,
                                'device_baseline': 'passed', 'api_and_factory_callers': 'next T7 work',
                                'listener_deployment_replayed': False})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--activation', required=True)
    parser.add_argument('--authority', required=True)
    parser.add_argument('--image', required=True)
    parser.add_argument('--resume', action='store_true')
    parser.add_argument('--resume-after-deploy', action='store_true')
    parser.add_argument('--resume-after-listener', action='store_true')
    args = parser.parse_args()
    m.require(sum((args.resume, args.resume_after_deploy, args.resume_after_listener)) <= 1, 'choose one recovery phase')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = ListenerRun(args)
    try:
        (runner.resume_after_listener if args.resume_after_listener else runner.resume_after_deploy if args.resume_after_deploy else runner.resume_install if args.resume else runner.install)()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'
        runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        os.close(fd)
        print(json.dumps({'phase': runner.report['phase'], 'status': runner.report['status'],
                          'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    def interrupted(*_):
        raise RuntimeError('interrupted; reconcile saved listener evidence before retry')
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
