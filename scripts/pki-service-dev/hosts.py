#!/usr/bin/env python3
"""Scoped dev managed-server adoption. Private evidence and a new directory per phase."""
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

spec = importlib.util.spec_from_file_location('service_hierarchy', Path(__file__).with_name('run.py'))
h = importlib.util.module_from_spec(spec)
spec.loader.exec_module(h)
m, NS = h.m, h.NS
AM_NS = 'video-cloud-dev-account-manager'


def private_state_digest(value):
    lines = value.splitlines()
    # A directory can inherit setgid from the PVC without granting group access.
    m.require(len(lines) == 3 and lines[0] in ('700', '2700') and lines[1] == '600'
              and h.re.fullmatch('[0-9a-f]{64}', lines[2].split()[0]), 'managed state access permissions differ')
    return lines[2].split()[0]


def managed_settings(name, root_pin):
    m.require(name in h.SERVICE_CONSUMERS, 'unknown managed host')
    prefix = 'PKI' if name == 'pki-controller' else 'CERT_ISSUER'
    host = name + '.' + NS + '.svc'
    issuer = 'certissuer.' + NS + '.svc'
    return {prefix + '_SERVER_CERT': '/var/lib/pki-host/seed/chain.pem',
            prefix + '_SERVER_KEY': '/var/lib/pki-host/seed/key.pem',
            prefix + '_HOST_IDENTITY_STATE': '/var/lib/pki-host/identity/state.json',
            prefix + '_HOST_NAME': host, prefix + '_HOST_DNS_NAMES': host, prefix + '_HOST_ROOT_SHA256': root_pin,
            prefix + '_HOST_RENEWAL_CLIENT_CERT': '/run/pki-service-consumer/tls.crt',
            prefix + '_HOST_RENEWAL_CLIENT_KEY': '/run/pki-service-consumer/tls.key',
            prefix + '_HOST_RENEWAL_URL': 'https://' + issuer + ':9443',
            prefix + '_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256': root_pin,
            prefix + '_HOST_RENEWAL_SERVER_PKI_NAME': issuer,
            prefix + '_HOST_RENEWAL_TLS_CA': '/run/pki-host-root/root.pem',
            prefix + '_HOST_RENEWAL_SERVER_PKI_SWEEP_INTERVAL': '10s'}


def managed_template(owner, settings, pvc):
    m.require(owner['spec']['replicas'] == 1, 'managed dev host requires one replica')
    template = json.loads(json.dumps(owner['spec']['template']))
    pod = template['spec']
    m.require(len(pod['containers']) == 1 and not any(v['name'] == 'host-state' for v in pod['volumes']),
              'managed host already configured; reconcile')
    container = pod['containers'][0]
    m.require(not any('_HOST_' in e['name'] for e in container['env']), 'managed host settings already exist')
    container['env'] = h.with_env(container['env'], settings)
    container['volumeMounts'] += [{'name': 'host-state', 'mountPath': '/var/lib/pki-host'},
                                  {'name': 'host-root', 'mountPath': '/run/pki-host-root', 'readOnly': True}]
    pod['volumes'] += [{'name': 'host-state', 'persistentVolumeClaim': {'claimName': pvc}},
                      {'name': 'host-root', 'configMap': {'name': 'pki-service-host-root'}}]
    pod['securityContext'] = dict(pod.get('securityContext', {}), fsGroup=10001, fsGroupChangePolicy='OnRootMismatch',
                                  runAsUser=10001, runAsGroup=10001)
    return template


class HostRun(h.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['host_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.report['foundation_scope'] = 'Existing active dev Service hierarchy; managed controller/certissuer server adoption'
        self.save('report.json', self.report)

    def hierarchy(self):
        root = self.active_root()
        ready = m.read(Path(self.args.intermediate) / 'intermediate-ready.json')
        issuer = self.api('/issuers/' + ready['issuer_id'])
        m.require(issuer['status'] == 'active' and issuer['parent_issuer_id'] == root['issuer_id']
                  and issuer['certificate_fingerprint_sha256'] == ready['certificate_fingerprint_sha256']
                  and issuer['server_dns_names'] == h.SERVICE_DNS_NAMES, 'active Service intermediate changed')
        self.api('/issuers/' + issuer['issuer_id'] + '/crl')
        self.save('service-intermediate.json', issuer)
        self.save('root.pem', root['certificate_pem'])
        self.save('intermediate.pem', issuer['certificate_pem'])
        return root, issuer

    def scoped_patch(self, kind, before, changes):
        name, ns = before['metadata']['name'], before['metadata']['namespace']
        m.require(ns in (NS, AM_NS), 'unexpected mutation namespace')
        self.save('before-' + ns + '-' + name + '-' + kind + '.json', before)
        patches = [{'op': 'test', 'path': '/metadata/resourceVersion', 'value': before['metadata']['resourceVersion']}] + changes
        obj = json.loads(self.kube(['-n', ns, 'patch', kind, name, '--type=json', '--patch-file=/dev/stdin', '-o', 'json'], json.dumps(patches)))
        desired = {k: obj[k] for k in ('apiVersion', 'kind', 'spec', 'data', 'type', 'immutable') if k in obj}
        desired['metadata'] = {k: obj['metadata'][k] for k in ('name', 'namespace', 'labels', 'annotations') if k in obj['metadata']}
        self.save('after-' + ns + '-' + name + '-' + kind + '.json', desired)
        m.write(self.base / 'pki/controller-bootstrap/rollout' / (name + '-' + kind.lower() + '.json'), desired)
        return obj

    def patch_trust(self, name, field, extra, ns=NS):
        before = self.obj('secret', name, ns)
        existing = base64.b64decode(before['data'][field]).decode()
        updated = h.append_pem(existing, extra)
        if updated != existing:
            self.scoped_patch('secret', before, [{'op': 'replace', 'path': '/data/' + field,
                                                 'value': base64.b64encode(updated.encode()).decode()}])

    def rollout(self, name, updates=None, ns=NS):
        before = self.obj('deployment', name, ns)
        template = json.loads(json.dumps(before['spec']['template']))
        if updates:
            m.require(len(template['spec']['containers']) == 1, 'unexpected caller containers')
            container = template['spec']['containers'][0]
            container['env'] = h.with_env(container['env'], updates)
        template.setdefault('metadata', {}).setdefault('annotations', {})['rtk.cloud/service-host-rollout'] = self.output.name
        self.scoped_patch('deployment', before, [{'op': 'test', 'path': '/spec/template', 'value': before['spec']['template']},
                                                {'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', ns, 'rollout', 'status', 'deployment/' + name, '--timeout=240s'], timeout=250)
        if name == 'account-manager':
            self.forward('am', AM_NS, name, 80)
        elif name == 'certissuer':
            self.forward('issuer', NS, name, 9443)
        elif name == 'video-cloud-api-pki':
            self.forward('api', NS, name, 8443)
        elif name == 'mqtt-pki':
            self.forward('mqtt', NS, name, 8883)
        elif name == 'factoryenroll':
            self.forward('factory', NS, name, 80)
        if updates:
            path = self.base / 'pki/controller-bootstrap/rollout' / (name + '-service-settings.json')
            current = m.read(path) if path.exists() else {}
            m.write(path, dict(current, **updates))

    def issuer_http(self, name, body, ca, expected=200):
        identity = self.base / 'pki/consumers' / name
        response = json.loads(m.command([self.probe, 'tls', ca, identity / 'tls.crt', identity / 'tls.key',
                                         'certissuer.' + NS + '.svc', self.ports['issuer'][0], '/v1/certificates/gateway/issue'], json.dumps(body)))
        self.save('http-' + name + '.json', response)
        m.require(response['status'] == expected, 'registered server issuance/replay failed: ' + str(response['status']))
        return json.loads(response['body']), response

    def prepare(self):
        root, issuer = self.hierarchy()
        for name in h.SERVICE_CONSUMERS:
            owner = self.obj('deployment', name)
            m.require(not any('_HOST_' in e['name'] for e in owner['spec']['template']['spec']['containers'][0]['env']), 'host already managed')
            self.kube(['-n', NS, 'exec', 'deployment/' + name, '--', 'sh', '-c', 'test -x /usr/bin/openssl'])
        policies = m.read(Path(self.args.intermediate) / 'provider-policies.json')
        m.require(policies['issuer_id'] == issuer['issuer_id'], 'signer policy issuer changed')
        self.save('certissuer-role-before.json', json.loads(self.bao(['read', '-format=json', 'auth/kubernetes/role/certissuer-pki-dev']))['data'])
        self.role_policy('certissuer-pki-dev', 'pki-service-server-dev-' + issuer['issuer_id'], policies['signer_policy'])
        for name in h.SERVICE_CONSUMERS:
            self.patch_trust('certissuer-runtime', 'client-ca.crt', (self.base / 'pki/consumers' / name / 'ca.crt').read_text())
        self.create({'apiVersion': 'networking.k8s.io/v1', 'kind': 'NetworkPolicy',
                     'metadata': {'name': 'allow-service-host-renewal', 'namespace': NS},
                     'spec': {'podSelector': {'matchLabels': {'app.kubernetes.io/name': 'certissuer'}}, 'policyTypes': ['Ingress'],
                              'ingress': [{'from': [{'podSelector': {'matchExpressions': [{'key': 'app.kubernetes.io/name', 'operator': 'In', 'values': h.SERVICE_CONSUMERS}]}}],
                                           'ports': [{'port': 9443, 'protocol': 'TCP'}]}]}})
        self.create({'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': 'pki-service-host-root', 'namespace': NS},
                     'immutable': True, 'data': {'root.pem': root['certificate_pem']}})
        self.rollout('certissuer', {'CERT_ISSUER_SERVER_PKI_DOMAIN': 'service',
                     'CERT_ISSUER_SERVER_PKI_ROOT_SHA256': root['certificate_fingerprint_sha256'],
                     'CERT_ISSUER_GATEWAY_CLIENT_CN_PATTERN': '^(certissuer|pki-controller)$',
                     'CERT_ISSUER_GATEWAY_DNS_NAMES': ','.join(h.SERVICE_DNS_NAMES), 'CERT_ISSUER_MAX_TTL_DAYS': '365'})
        ca = base64.b64decode(self.obj('secret', 'factoryenroll-certissuer-client')['data']['ca.crt']).decode()
        self.save('bootstrap-issuer-ca.pem', ca)
        for name in h.SERVICE_CONSUMERS:
            self.prepare_host(name, issuer)
        self.device_baseline()
        self.check('registered_host_seeds', {'hosts': h.SERVICE_CONSUMERS, 'keys': 'generated inside dedicated PVCs; never exported',
                                            'provider_policy': 'server signing only', 'managed_listeners': 'not switched yet'})

    def prepare_host(self, name, issuer):
        host = name + '.' + NS + '.svc'
        pvc = name + '-service-identity'
        owner = self.obj('deployment', name)
        podspec = owner['spec']['template']['spec']
        image = podspec['containers'][0]['image']
        self.create({'apiVersion': 'v1', 'kind': 'PersistentVolumeClaim', 'metadata': {'name': pvc, 'namespace': NS},
                     'spec': {'accessModes': ['ReadWriteOnce'], 'storageClassName': 'linode-block-storage-retain',
                              'resources': {'requests': {'storage': '10Gi'}}}})
        podname = 'pki-host-seed-' + name + '-' + uuid.uuid4().hex[:8]
        script = ('set -eu; umask 077; mkdir -p /state/seed; test ! -e /state/seed/key.pem; '
                  'openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out /state/seed/key.pem; '
                  'openssl req -new -key /state/seed/key.pem -subj /CN=' + host + ' -addext subjectAltName=DNS:' + host +
                  ' -out /state/seed/csr.pem; exec sleep 3600')
        pod = {'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': podname, 'namespace': NS},
               'spec': {'restartPolicy': 'Never', 'automountServiceAccountToken': False, 'imagePullSecrets': podspec.get('imagePullSecrets', []),
                        'securityContext': {'runAsUser': 10001, 'runAsGroup': 10001, 'fsGroup': 10001, 'fsGroupChangePolicy': 'OnRootMismatch'},
                        'containers': [{'name': 'seed', 'image': image, 'command': ['sh', '-c', script],
                                        'volumeMounts': [{'name': 'state', 'mountPath': '/state'}]}],
                        'volumes': [{'name': 'state', 'persistentVolumeClaim': {'claimName': pvc}}]}}
        self.save(name + '/seed-pod.json', pod)
        self.kube(['-n', NS, 'create', '-f', '-'], json.dumps(pod))
        self.kube(['-n', NS, 'wait', '--for=condition=Ready', 'pod/' + podname, '--timeout=240s'], timeout=250)
        csr = self.kube(['-n', NS, 'exec', podname, '--', 'cat', '/state/seed/csr.pem'])
        request = {'request_id': 'dev-server-' + uuid.uuid4().hex, 'csr_pem': csr, 'ttl_days': 30, 'purpose': 'server'}
        self.save(name + '/request.json', request)
        self.save(name + '/pod-reference.json', {'name': podname, 'uid': self.obj('pod', podname)['metadata']['uid'],
                                              'pvc': pvc, 'pvc_uid': self.obj('persistentvolumeclaim', pvc)['metadata']['uid']})
        self.finish_host(name, issuer, request, podname)

    def finish_host(self, name, issuer, request, podname):
        host = name + '.' + NS + '.svc'
        m.require(self.kube(['-n', NS, 'exec', podname, '--', 'cat', '/state/seed/csr.pem']) == request['csr_pem'],
                  'saved host CSR differs from PVC; do not sign')
        response, _ = self.issuer_http(name, request, self.output / 'bootstrap-issuer-ca.pem')
        self.save(name + '/issued.json', response)
        self.save(name + '/leaf.pem', response['certificate_pem'])
        self.save(name + '/chain.pem', response['certificate_chain_pem'])
        m.require(response['dns_names'] == [host] and response['caller_identity'] == name, 'server issuance identity differs')
        m.command([self.openssl, 'verify', '-CAfile', self.output / 'root.pem', '-untrusted', self.output / 'intermediate.pem',
                   '-purpose', 'sslserver', '-verify_hostname', host, self.output / name / 'leaf.pem'])
        public = m.command([self.openssl, 'x509', '-in', self.output / name / 'leaf.pem', '-pubkey', '-noout'])
        csr_public = self.kube(['-n', NS, 'exec', podname, '--', 'openssl', 'req', '-in', '/state/seed/csr.pem', '-pubkey', '-noout'])
        m.require(public == csr_public, 'server certificate does not match PVC key/CSR')
        self.kube(['-n', NS, 'exec', '-i', podname, '--', 'sh', '-c', 'set -eu; umask 077; test ! -e /state/seed/chain.pem; cat > /state/seed/chain.pem'], response['certificate_chain_pem'])
        self.check(name + '_seed_issued', {'request_id': request['request_id'], 'key_exported': False,
                                         'issuer_id': issuer['issuer_id'], 'dns_names': response['dns_names']})

    def resume_prepare(self):
        source = Path(self.args.prepared)
        prior = m.read(source / 'report.json')
        m.require(prior['status'] == 'failed' and prior['phase'] == 'prepare'
                  and not (source / 'certissuer/issued.json').exists()
                  and not (source / 'pki-controller/request.json').exists(), 'only the failed first host request may be reconciled')
        _, issuer = self.hierarchy()
        m.require(issuer == m.read(source / 'service-intermediate.json'), 'Service intermediate changed during recovery')
        request = m.read(source / 'certissuer/request.json')
        ref = m.read(source / 'certissuer/pod-reference.json')
        m.require(h.re.fullmatch('dev-server-[0-9a-f]{32}', request['request_id']), 'invalid saved request ID')
        query = "SELECT count(*) FROM pki_server_issuances WHERE request_id='" + request['request_id'] + "';"
        count = self.kube(['-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--',
                          'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'video_cloud', '-At'], query).strip()
        m.require(count == '0', 'signing claim exists; use issuance reconciliation instead of replay')
        m.require(self.obj('pod', ref['name'])['metadata']['uid'] == ref['uid']
                  and self.obj('persistentvolumeclaim', ref['pvc'])['metadata']['uid'] == ref['pvc_uid'], 'saved seed owner changed')
        for filename in ('request.json', 'pod-reference.json', 'seed-pod.json'):
            self.save('certissuer/' + filename, m.read(source / 'certissuer' / filename))
        self.save('bootstrap-issuer-ca.pem', (source / 'bootstrap-issuer-ca.pem').read_text())
        self.report['reconciled_from'] = str(source)
        self.finish_host('certissuer', issuer, request, ref['name'])
        self.prepare_host('pki-controller', issuer)
        self.device_baseline()
        self.check('registered_host_seeds', {'hosts': h.SERVICE_CONSUMERS, 'keys': 'generated inside dedicated PVCs; never exported',
                                            'first_key_and_request_reused': True, 'prior_signing_claim_count': 0})

    def callers(self):
        self.prepared('certissuer')
        root, _ = self.hierarchy()
        for ns, name, field in [(AM_NS, 'account-manager-pki-auth', 'management-ca.crt'),
                                (NS, 'pki-video-cloud-api-management', 'ca.crt'), (NS, 'pki-pkibroker-management', 'ca.crt'),
                                (NS, 'pki-service-consumer-certissuer', 'ca.crt'), (NS, 'pki-service-consumer-pki-controller', 'ca.crt'),
                                (NS, 'factoryenroll-certissuer-client', 'ca.crt'),
                                (AM_NS, 'account-manager-certissuer-client', 'ca.crt')]:
            self.patch_trust(name, field, root['certificate_pem'], ns)
        url = 'https://certissuer.' + NS + '.svc:9443'
        self.rollout('account-manager', {'APP_CERT_ISSUER_BASE_URL': url}, AM_NS)
        self.rollout('factoryenroll', {'FACTORY_ENROLL_CERT_ISSUER_URL': url})
        for name in ('video-cloud-api-pki', 'mqtt-pki'):
            self.rollout(name)
        self.api('/issuers/' + root['issuer_id'])
        self.device_baseline()
        self.check('caller_trust_prepared', {'controller_consumers': 5, 'issuer_callers': ['factoryenroll', 'account-manager'],
                                           'service_root_additive': True, 'issuer_name': 'certissuer.' + NS + '.svc'})

    def prepared(self, name):
        source = Path(self.args.prepared)
        m.require(m.read(source / 'report.json')['status'] == 'passed', 'successful prepared-host evidence required')
        return source, m.read(source / name / 'pod-reference.json'), m.read(source / name / 'issued.json')

    def switch(self, name):
        caller_report = m.read(Path(self.args.callers) / 'report.json')
        m.require(caller_report['status'] == 'passed' and caller_report['phase'] == 'callers', 'successful caller trust phase required')
        root, _ = self.hierarchy()
        source, ref, issued = self.prepared(name)
        m.require(self.obj('persistentvolumeclaim', ref['pvc'])['metadata']['uid'] == ref['pvc_uid'], 'host PVC changed')
        pod = self.obj('pod', ref['name'])
        m.require(pod['metadata']['uid'] == ref['uid'], 'seed pod changed')
        options = {'apiVersion': 'v1', 'kind': 'DeleteOptions', 'preconditions': {'uid': ref['uid'], 'resourceVersion': pod['metadata']['resourceVersion']}}
        self.kube(['delete', '--raw', '/api/v1/namespaces/' + NS + '/pods/' + ref['name'], '-f', '-'], json.dumps(options))
        self.kube(['-n', NS, 'wait', '--for=delete', 'pod/' + ref['name'], '--timeout=90s'], timeout=100)
        owner = self.obj('deployment', name)
        settings = managed_settings(name, root['certificate_fingerprint_sha256'])
        template = managed_template(owner, settings, ref['pvc'])
        self.scoped_patch('deployment', owner, [{'op': 'test', 'path': '/spec/template', 'value': owner['spec']['template']},
                                               {'op': 'replace', 'path': '/spec/strategy', 'value': {'type': 'Recreate'}},
                                               {'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + name, '--timeout=300s'], timeout=310)
        settings_path = self.base / 'pki/controller-bootstrap/rollout' / (name + '-service-settings.json')
        m.write(settings_path, dict(m.read(settings_path), **settings))
        if name == 'certissuer':
            self.forward('issuer', NS, name, 9443)
        self.finish_switch(name, source, ref, issued)

    def finish_switch(self, name, source, ref, issued):
        self.check_host(name, source, issued)
        state_before = self.state_digest(name)
        # Only our seed is removed, after a registered serving certificate and private managed state are proven.
        self.kube(['-n', NS, 'exec', 'deployment/' + name, '--', 'sh', '-c',
                   'set -eu; test -s /var/lib/pki-host/identity/state.json; test "$(stat -c %a /var/lib/pki-host/identity/state.json)" = 600; '
                   'rm /var/lib/pki-host/seed/key.pem /var/lib/pki-host/seed/csr.pem /var/lib/pki-host/seed/chain.pem'])
        self.rollout(name)
        self.check_host(name, source, issued)
        m.require(self.state_digest(name) == state_before, 'managed state changed across seedless restart')
        root = m.read(self.output / 'service-root.json')
        self.api('/issuers/' + root['issuer_id'])
        self.device_baseline()
        self.check(name + '_managed_restart', {'pvc_uid': ref['pvc_uid'], 'state_sha256': state_before,
                    'seed_removed': True, 'restart_without_seed': True, 'image_changed': False})

    def state_digest(self, name):
        value = self.kube(['-n', NS, 'exec', 'deployment/' + name, '--', 'sh', '-c',
                          'set -eu; stat -c %a /var/lib/pki-host/identity; '
                          'stat -c %a /var/lib/pki-host/identity/state.json; sha256sum /var/lib/pki-host/identity/state.json'])
        return private_state_digest(value)

    def resume_certissuer(self):
        old = Path(self.args.adoption)
        report = m.read(old / 'report.json')
        m.require(report['status'] == 'failed' and report['phase'] == 'certissuer', 'failed certissuer adoption evidence required')
        self.hierarchy()
        source, ref, issued = self.prepared('certissuer')
        current = self.obj('deployment', 'certissuer')
        saved = m.read(old / ('after-' + NS + '-certissuer-deployment.json'))
        m.require(current['spec']['template']['spec'] == saved['spec']['template']['spec']
                  and current['spec']['strategy']['type'] == 'Recreate'
                  and self.obj('persistentvolumeclaim', ref['pvc'])['metadata']['uid'] == ref['pvc_uid'], 'managed host changed during recovery')
        self.kube(['-n', NS, 'exec', 'deployment/certissuer', '--', 'sh', '-c', 'test -s /var/lib/pki-host/seed/key.pem'])
        self.report['reconciled_from'] = str(old)
        self.finish_switch('certissuer', source, ref, issued)

    def resume_controller(self):
        old = Path(self.args.adoption)
        report = m.read(old / 'report.json')
        m.require(report['status'] == 'failed' and report['phase'] == 'controller', 'failed controller adoption evidence required')
        self.hierarchy()
        name = 'pki-controller'
        source, ref, issued = self.prepared(name)
        saved = m.read(old / ('after-' + NS + '-' + name + '-deployment.json'))
        current = self.obj('deployment', name)
        m.require(current['spec']['template'] == saved['spec']['template']
                  and current['spec']['strategy']['type'] == 'Recreate'
                  and self.obj('persistentvolumeclaim', ref['pvc'])['metadata']['uid'] == ref['pvc_uid'],
                  'managed controller changed during recovery')
        state = m.read(old / (name + '-served.json'))['state_sha256']
        m.require(self.state_digest(name) == state, 'managed controller state changed during recovery')
        self.kube(['-n', NS, 'exec', 'deployment/' + name, '--', 'sh', '-c',
                   'test ! -e /var/lib/pki-host/seed/key.pem && test ! -e /var/lib/pki-host/seed/chain.pem && test ! -e /var/lib/pki-host/seed/csr.pem'])
        self.report['reconciled_from'] = str(old)
        self.check_host(name, source, issued)
        self.rollout(name)
        self.check_host(name, source, issued)
        m.require(self.state_digest(name) == state, 'managed state changed across seedless restart')
        self.device_baseline()
        self.check(name + '_managed_restart', {'pvc_uid': ref['pvc_uid'], 'state_sha256': state,
                    'seed_removed': True, 'restart_without_seed': True, 'image_changed': False})

    def check_host(self, name, source, issued):
        leaf = issued['certificate_pem']
        fingerprint = m.digest(base64.b64decode(''.join(leaf.strip().splitlines()[1:-1])))
        if name == 'certissuer':
            replay, transport = self.issuer_http(name, m.read(source / name / 'request.json'), self.output / 'root.pem')
            m.require(replay['certificate_pem'] == leaf, 'server request replay returned another certificate')
        else:
            self.forward('controller', NS, name, 18446)
            client = self.base / 'pki/consumers/pki-controller'
            transport = json.loads(m.command([self.probe, 'tls', self.output / 'root.pem', client / 'tls.crt', client / 'tls.key',
                                               name + '.' + NS + '.svc', self.ports['controller'][0], '/v1/pki/issuers/search'], json.dumps({'limit': 1})))
            m.require(transport['status'] == 403, 'controller human assertion gate changed')
        m.require(transport['peer_sha256'] == fingerprint, 'listener serves another certificate')
        self.save(name + '-served.json', {'certificate_sha256': fingerprint, 'state_sha256': self.state_digest(name)})

    def verify_hosts(self):
        root, issuer = self.hierarchy()
        for name in h.SERVICE_CONSUMERS:
            source, ref, issued = self.prepared(name)
            self.check_host(name, source, issued)
            self.kube(['-n', NS, 'exec', 'deployment/' + name, '--', 'sh', '-c', 'test ! -e /var/lib/pki-host/seed/key.pem'])
            owner = self.obj('deployment', name)
            wanted = h.render_persisted_listener(self.base, name)
            m.require(owner['spec']['template']['spec'] == wanted['spec']['template']['spec'] and owner['spec']['strategy']['type'] == 'Recreate',
                      'persisted managed host differs from runtime')
            m.require(self.obj('persistentvolumeclaim', ref['pvc'])['metadata']['uid'] == ref['pvc_uid'], 'managed host PVC replaced')
        self.verify_controller_callers(root)
        self.verify_server_signer(issuer)
        self.device_baseline()
        self.check('managed_hosts_verified', {'hosts': h.SERVICE_CONSUMERS, 'private_persistent_state': True,
                                            'served_certificates_match_registry': True, 'scheduled_renewal_and_revocation': 'not yet qualified'})

    def verify_server_signer(self, issuer, service_client=False):
        keys = json.loads(self.bao(['list', '-format=json', issuer['signer_reference'] + '/keys']))
        m.require(len(keys) == 1, 'intermediate key count changed')
        service_account = self.obj('deployment', 'certissuer')['spec']['template']['spec']['serviceAccountName']
        m.require(service_account, 'certificate issuer workload service account missing')
        jwt = self.kube(['-n', NS, 'create', 'token', service_account, '--audience=openbao', '--duration=10m']).strip()
        login = json.loads(self.bao(['write', '-format=json', 'auth/kubernetes/login', '-'],
                                   json.dumps({'role': 'certissuer-pki-dev', 'jwt': jwt})))
        token = login['auth']['client_token']
        try:
            paths = ['sign/server', 'sign/service-client', 'key/' + keys[0], 'roles/server', 'intermediate/generate/internal']
            if service_client:
                paths.append('roles/service-client')
            for path in paths:
                response = json.loads(self.bao(['write', '-format=json', 'sys/capabilities', '-'],
                                               json.dumps({'token': token, 'paths': [issuer['signer_reference'] + '/' + path]})))
                allowed = path == 'sign/server' or (service_client and path == 'sign/service-client')
                m.require(response['data']['capabilities'] == (['update'] if allowed else ['deny']),
                          'certificate-issuer provider capability differs: ' + path)
        finally:
            self.bao(['write', 'auth/token/revoke', '-'], json.dumps({'token': token}))
        self.check('server_signer_boundary', {'server_signing_allowed': True, 'service_client_signing_allowed': service_client,
                                             'key_read_role_write_and_key_generation_denied': True})

    def verify_controller_callers(self, root):
        host = 'pki-controller.' + NS + '.svc'
        device_root = m.read(self.foundation / 'device-root-active.json')
        results = []
        for name, container, directory, authority in [('pki-controller', 'pki-controller', '/run/pki-service-consumer', root),
                ('certissuer', 'certissuer', '/run/pki-service-consumer', root),
                ('mqtt-pki', 'pkibroker', '/run/pki-management', device_root)]:
            crl = self.api('/issuers/' + authority['issuer_id'] + '/crl')
            request = 'GET /v1/pki/issuers/' + authority['issuer_id'] + '/crl HTTP/1.1\r\nHost: ' + host + '\r\nConnection: close\r\n\r\n'
            response = self.kube(['-n', NS, 'exec', '-i', 'deployment/' + name, '-c', container, '--',
                                  'openssl', 's_client', '-quiet', '-verify_return_error', '-verify_hostname', host,
                                  '-CAfile', directory + '/ca.crt', '-cert', directory + '/tls.crt', '-key', directory + '/tls.key',
                                  '-connect', host + ':18446'], request, timeout=35)
            m.require(response.startswith('HTTP/1.1 200 ') and crl['crl_sha256'] in response,
                      'pod authenticated controller CRL read failed: ' + name)
            results.append({'consumer': name, 'crl_sha256': crl['crl_sha256'], 'http_status': 200})
        api = self.obj('deployment', 'video-cloud-api-pki')
        pod = api['spec']['template']['spec']
        app = next((c for c in pod['containers'] if c['name'] == 'app'), None)
        m.require(app is not None and len(pod['containers']) == 1, 'API controller identity topology changed')
        env = {entry['name']: entry.get('value', '') for entry in app.get('env', [])}
        required = ('VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE', 'VIDEO_CLOUD_CONTROLLER_ROOT_SHA256',
                    'VIDEO_CLOUD_CONTROLLER_IDENTITY_SERVER_PKI_ROOT_SHA256', 'VIDEO_CLOUD_CONTROLLER_IDENTITY_SERVER_PKI_NAME',
                    'VIDEO_CLOUD_CONTROLLER_IDENTITY_TLS_CA', 'VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_URL',
                    'VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_SERVER_PKI_ROOT_SHA256', 'VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_SERVER_PKI_NAME',
                    'VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_TLS_CA')
        m.require(all(env.get(name) for name in required), 'API managed controller identity settings incomplete')
        forbidden = ('VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_MANAGEMENT_CERT', 'VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_MANAGEMENT_KEY')
        m.require(not any(name in env for name in forbidden), 'API static controller credential setting remains')
        m.require(not any(mount['name'] == 'pki-management' or mount['mountPath'] == '/run/pki-management' for mount in app.get('volumeMounts', [])),
                  'API static controller credential mount remains')
        m.require(not any(volume['name'] == 'pki-management' for volume in pod.get('volumes', [])), 'API static controller credential volume remains')
        results.append({'consumer': 'video-cloud-api-pki', 'identity': 'managed', 'static_controller_key_mounted': False})
        self.check('controller_caller_transport', {'pod_mtls_crl_reads': results, 'account_manager': 'ordinary human API assertions passed'})

    def factory_canary(self):
        self.hierarchy()
        for name in h.SERVICE_CONSUMERS:
            source, _, issued = self.prepared(name)
            self.check_host(name, source, issued)
        profile = m.read(self.foundation / 'product.json')
        expected = m.read(self.foundation / 'product-v4-active.json')
        current = self.api('/issuers/' + expected['issuer_id'])
        m.require(current['status'] == 'active' and current['device_item_profile_id'] == profile['id'], 'baseline Product changed')
        now = m.dt.datetime.now(m.dt.timezone.utc)
        batch = 'service-host-' + uuid.uuid4().hex
        run = self.http('/admin/brand-clouds/' + profile['brand_cloud_id'] + '/device-item-profiles/' + profile['id'] + '/production-runs',
                        {'factory_id': 'dev-pki-rehearsal', 'batch_id': batch, 'allowed_quantity': 1,
                         'valid_from': m.stamp(now - m.dt.timedelta(minutes=1)), 'valid_until': m.stamp(now + m.dt.timedelta(hours=1))},
                        self.login(self.admin)[0], 201)
        self.save('production-run.json', run)
        device = 'pki-host-' + uuid.uuid4().hex
        body = {'request_id': uuid.uuid4().hex, 'devid': device, 'csr_pem': self.key('canary', device),
                'serial_number': batch, 'production_run_id': run['production_run']['id'], 'brand_cloud_id': profile['brand_cloud_id'],
                'device_item_profile_id': profile['id'], 'factory_id': 'dev-pki-rehearsal', 'batch_id': batch, 'entitlement_state': 'active'}
        body.update({key: profile[key] for key in ('profile_key', 'ca_profile', 'issuer_profile', 'service_options')})
        self.save('enroll-request.json', body)
        result = self.http('/factory/enroll', body, run['factory_jwt'], service='factory')
        self.save('enrolled.json', result)
        m.require(result['certificate_bundle']['issuance']['issuer_id'] == current['issuer_id'], 'factory selected another Product issuer')
        self.save('canary-chain.pem', result['certificate_chain_pem'])
        self.save('canary-certificate.pem', result['certificate_pem'])
        m.require(m.command([self.openssl, 'x509', '-in', self.output / 'canary-certificate.pem', '-pubkey', '-noout']) ==
                  m.command([self.openssl, 'pkey', '-in', self.output / 'canary-key.pem', '-pubout']), 'canary key/certificate mismatch')
        auth = self.auth(self.output / 'canary', device)
        self.wait_positive_mqtt(auth, device)
        self.mqtt(auth, device, 'roundtrip')
        self.check('factory_through_managed_issuer', {'device_id': device, 'issuer_id': current['issuer_id'],
                    'factory_enrollment': 'passed', 'direct_mtls': 'passed', 'mqtt_acl_qos1': 'passed',
                    'canary_retained_for_dev_testing': True, 'production_run_quantity': 1, 'production_run_hours': 1})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', choices=['prepare', 'resume-prepare', 'callers', 'certissuer', 'resume-certissuer', 'controller', 'resume-controller', 'canary', 'verify'], required=True)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--authority', required=True)
    parser.add_argument('--intermediate', required=True)
    parser.add_argument('--prepared')
    parser.add_argument('--callers')
    parser.add_argument('--adoption')
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    m.require(args.phase == 'prepare' or args.prepared, 'prepared host evidence required')
    m.require(args.phase not in ('certissuer', 'controller') or args.callers, 'caller trust evidence required')
    m.require(args.phase not in ('resume-certissuer', 'resume-controller') or args.adoption, 'failed adoption evidence required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    descriptor = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = HostRun(args)
    try:
        runner.preflight()
        {'prepare': runner.prepare, 'resume-prepare': runner.resume_prepare, 'callers': runner.callers, 'certissuer': lambda: runner.switch('certissuer'),
         'resume-certissuer': runner.resume_certissuer, 'controller': lambda: runner.switch('pki-controller'),
         'resume-controller': runner.resume_controller, 'canary': runner.factory_canary, 'verify': runner.verify_hosts}[args.phase]()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'
        runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        os.close(descriptor)
        print(json.dumps({'phase': args.phase, 'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    def interrupted(*_):
        raise RuntimeError('interrupted; reconcile saved state before replay')
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
