#!/usr/bin/env python3
"""Dev-only factory Service identity bootstrap and adoption."""
import argparse
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import sys


spec = importlib.util.spec_from_file_location(
    'factory_adoption', Path(__file__).with_name('factory_adoption.py'))
fa = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fa)
r, m, NS = fa.r, fa.m, fa.r.NS

SUBJECT = 'service:factory-enroll'
PVC = 'factoryenroll-service-identity'
SEED_POD = 'factoryenroll-service-identity-seed'
STATE = '/state/identity/client.json'
BOOTSTRAP_PATTERN = '^factoryenroll$'
CLOSED_PATTERN = '^$'
CONSUMERS = ['certissuer', 'factory-enroll', 'pki-controller']


def env_values(container):
    values = {item['name']: item.get('value') for item in container.get('env', [])}
    m.require(len(values) == len(container.get('env', [])),
              'duplicate workload environment setting')
    return values


def select_active_v2(items, root):
    intermediates = [item for item in items
                     if item['environment'] == 'dev'
                     and item['trust_domain'] == 'service'
                     and item['kind'] == 'intermediate'
                     and item['parent_issuer_id'] == root['issuer_id']]
    active = [item for item in intermediates if item['status'] == 'active']
    retiring = [item for item in intermediates if item['status'] == 'retiring']
    m.require(len(active) == 1 and active[0]['service_client_ids'] == fa.V2_CLIENT_IDS,
              'exact active Service intermediate v2 required')
    m.require(len(retiring) == 1 and retiring[0]['service_client_ids'] == fa.OLD_CLIENT_IDS,
              'exact retiring Service intermediate v1 required')
    return retiring[0], active[0]


def provisioner_template(owner, expected, replacement):
    template = json.loads(json.dumps(owner['spec']['template']))
    containers = template['spec']['containers']
    m.require(len(containers) == 1 and containers[0]['name'] == 'certissuer',
              'certissuer container changed')
    values = env_values(containers[0])
    m.require(values.get('CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN') == expected,
              'certissuer bootstrap provisioner policy changed')
    containers[0]['env'] = r.with_env(
        containers[0]['env'],
        {'CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN': replacement})
    return template


def seed_pod(image, root_sha256, pull_secrets):
    m.require(pull_secrets and all(set(item) == {'name'} and item['name']
                                  for item in pull_secrets), 'registry pull references required')
    issuer = 'certissuer.' + NS + '.svc'
    return {
        'apiVersion': 'v1', 'kind': 'Pod',
        'metadata': {
            'name': SEED_POD, 'namespace': NS,
            'labels': {'app.kubernetes.io/name': 'factoryenroll',
                       'app.kubernetes.io/part-of': 'rtk-cloud',
                       'rtk.cloud/purpose': 'service-identity-seed'}},
        'spec': {
            'automountServiceAccountToken': False,
            'imagePullSecrets': pull_secrets,
            'restartPolicy': 'Never',
            'securityContext': {
                'runAsUser': 10001, 'runAsGroup': 10001, 'fsGroup': 10001,
                'fsGroupChangePolicy': 'OnRootMismatch'},
            'containers': [{
                'name': 'seed', 'image': image,
                'command': ['sh', '-c'],
                'args': [
                    'set -eu; test ! -e /state/identity.json; '
                    'test ! -e /state/identity; umask 077; mkdir -m 700 /state/identity; '
                    '/app/serviceidentity-bootstrap ' + SUBJECT + '; '
                    '/app/serviceidentity-bootstrap inspect ' + STATE + ' ' + SUBJECT + ' '
                    + root_sha256 + '; '
                    'stat -c %a ' + STATE + '; sha256sum ' + STATE],
                'env': [
                    {'name': 'POSTGRES_PASSWORD', 'valueFrom': {'secretKeyRef': {
                        'name': 'factoryenroll-runtime', 'key': 'POSTGRES_PASSWORD'}}},
                    {'name': 'PKI_DATABASE_URL', 'value':
                     'postgres://postgres:$(POSTGRES_PASSWORD)@postgresql.video-cloud-dev-platform.svc.cluster.local:5432/video_cloud?sslmode=disable'},
                    {'name': 'PKI_ENVIRONMENT', 'value': 'dev'},
                    {'name': 'PKI_CLIENT_IDENTITY_STATE', 'value': STATE},
                    {'name': 'PKI_CLIENT_ROOT_SHA256', 'value': root_sha256},
                    {'name': 'PKI_CLIENT_ISSUER_URL', 'value': 'https://' + issuer + ':9443'},
                    {'name': 'PKI_CLIENT_ISSUER_SERVER_PKI_ROOT_SHA256', 'value': root_sha256},
                    {'name': 'PKI_CLIENT_ISSUER_SERVER_PKI_NAME', 'value': issuer},
                    {'name': 'PKI_CLIENT_ISSUER_TLS_CA', 'value': '/run/service-root/root.pem'},
                    {'name': 'PKI_CLIENT_IDENTITY_BOOTSTRAP_CERT', 'value': '/run/bootstrap/client.crt'},
                    {'name': 'PKI_CLIENT_IDENTITY_BOOTSTRAP_KEY', 'value': '/run/bootstrap/client.key'},
                ],
                'volumeMounts': [
                    {'name': 'identity', 'mountPath': '/state'},
                    {'name': 'service-root', 'mountPath': '/run/service-root', 'readOnly': True},
                    {'name': 'bootstrap', 'mountPath': '/run/bootstrap', 'readOnly': True},
                ],
                'securityContext': {
                    'allowPrivilegeEscalation': False,
                    'capabilities': {'drop': ['ALL']},
                    'runAsNonRoot': True,
                    'readOnlyRootFilesystem': True,
                },
            }],
            'volumes': [
                {'name': 'identity', 'persistentVolumeClaim': {'claimName': PVC}},
                {'name': 'service-root', 'configMap': {'name': 'pki-service-host-root'}},
                {'name': 'bootstrap', 'secret': {
                    'secretName': 'factoryenroll-certissuer-client', 'defaultMode': 0o440}},
            ],
        },
    }


def parse_seed_log(raw):
    lines = [line.strip() for line in raw.splitlines() if line.strip()]
    m.require(len(lines) == 4, 'seed output shape changed')
    issued, inspected = json.loads(lines[0]), json.loads(lines[1])
    m.require(issued.get('subject') == SUBJECT
              and issued.get('certificate_sha256') == inspected.get('fingerprint')
              and inspected.get('subject') == SUBJECT
              and inspected.get('pending') is False,
              'seed public identity evidence differs')
    m.require(lines[2] == '600' and re.fullmatch(r'[0-9a-f]{64}  ' + re.escape(STATE), lines[3]),
              'seed private state permission or digest differs')
    return {'subject': SUBJECT, 'certificate_sha256': issued['certificate_sha256'],
            'state_sha256': lines[3].split()[0], 'inspection': inspected}


def verify_unused_pvc_recovery(source, factory, pvc):
    m.require(m.read(source / 'report.json')['status'] == 'failed'
              and m.read(source / 'interrupted-seed-cleanup.json')['seed_pod_deleted'],
              'failed pre-execution seed and cleanup evidence required')
    saved = m.read(source / 'factory-deployment-baseline.json')
    prior = m.read(source / 'factory-identity-pvc.json')
    m.require(factory['metadata']['uid'] == saved['metadata']['uid']
              and factory['spec']['template'] == saved['spec']['template']
              and pvc['metadata']['uid'] == prior['metadata']['uid']
              and pvc['spec'] == prior['spec'], 'factory/PVC changed since interrupted seed')


def managed_template(factory, image, root_pin):
    template = json.loads(json.dumps(factory['spec']['template']))
    pod = template['spec']
    m.require(factory['spec']['replicas'] == 1 and len(pod['containers']) == 1,
              'one factory owner required')
    c = pod['containers'][0]
    m.require(c['name'] == 'factoryenroll' and not env_values(c).get(
        'FACTORY_ENROLL_SERVICE_IDENTITY_STATE'), 'factory already managed or container changed')
    prefix = 'FACTORY_ENROLL_CERT_ISSUER'
    settings = {
        'FACTORY_ENROLL_SERVICE_IDENTITY_STATE': STATE,
        'FACTORY_ENROLL_SERVICE_IDENTITY_ROOT_SHA256': root_pin,
        prefix + '_SERVER_PKI_ROOT_SHA256': root_pin,
        prefix + '_SERVER_PKI_NAME': 'certissuer.' + NS + '.svc',
        prefix + '_TLS_CA': '/run/service-root/root.pem',
        prefix + '_SERVER_CRL_MANIFEST': '/run/service-crls/crls.json',
        prefix + '_PKI_CONTROLLER_URL': 'https://pki-controller.' + NS + '.svc:18446',
        prefix + '_MANAGEMENT_CA': '/run/service-root/root.pem',
    }
    c['env'] = r.with_env(c['env'], settings,
                          [prefix + suffix for suffix in ('_CLIENT_CERT', '_CLIENT_KEY', '_CA')])
    c['image'] = image
    old = 'factoryenroll-certissuer-client'
    m.require(any(v.get('secret', {}).get('secretName') == old for v in pod['volumes']),
              'factory static credential mount changed')
    c['volumeMounts'] = [v for v in c.get('volumeMounts', []) if v['name'] != old] + [
        {'name': 'identity', 'mountPath': '/state'},
        {'name': 'service-root', 'mountPath': '/run/service-root', 'readOnly': True},
        {'name': 'service-crls', 'mountPath': '/run/service-crls', 'readOnly': True}]
    pod['volumes'] = [v for v in pod['volumes'] if v['name'] != old] + [
        {'name': 'identity', 'persistentVolumeClaim': {'claimName': PVC}},
        {'name': 'service-root', 'configMap': {'name': 'pki-service-host-root', 'defaultMode': 0o644}},
        {'name': 'service-crls', 'configMap': {'name': 'factoryenroll-service-crls', 'defaultMode': 0o644}}]
    pod['securityContext'] = dict(pod.get('securityContext', {}), runAsUser=10001,
                                  runAsGroup=10001, fsGroup=10001, fsGroupChangePolicy='OnRootMismatch')
    return template


class FactoryIdentityRun(fa.FactoryAdoption):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = 'Active dev Service v2; factory-owned Service identity'
        self.report['factory_identity_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def hierarchy_v2(self):
        root = self.active_root()
        old, active = select_active_v2(self.service_intermediates(), root)
        self.api('/issuers/' + root['issuer_id'] + '/crl')
        self.api('/issuers/' + old['issuer_id'] + '/crl')
        self.api('/issuers/' + active['issuer_id'] + '/crl')
        self.save('service-root.json', root)
        self.save('service-intermediate-v1.json', old)
        self.save('service-intermediate-v2.json', active)
        return root, active

    def sql(self, query):
        return self.kube([
            '-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--',
            'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres',
            '-d', 'video_cloud', '-At'], query).strip()

    def client_rows(self):
        raw = self.sql(
            "SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,subject,caller,status,"
            "fingerprint,issued_at,revoked_at FROM pki_service_client_issuances "
            "WHERE environment='dev' AND subject='service:factory-enroll' "
            "ORDER BY issued_at,request_id) t;")
        return [json.loads(line) for line in raw.splitlines() if line]

    def patch_provisioner(self, expected, replacement):
        owner = self.obj('deployment', 'certissuer')
        template = provisioner_template(owner, expected, replacement)
        self.save('certissuer-policy-' + ('open' if replacement != CLOSED_PATTERN else 'closed')
                  + '-before.json', owner)
        updated = self.patch('deployment', 'certissuer', owner, [
            {'op': 'test', 'path': '/spec/template', 'value': owner['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': template},
        ])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/certissuer',
                   '--timeout=300s'], timeout=310)
        return updated

    def seed(self):
        root, active = self.hierarchy_v2()
        root_map = self.obj('configmap', 'pki-service-host-root')
        m.require(root_map.get('immutable') is True
                  and root_map.get('data', {}).get('root.pem') == root['certificate_pem'],
                  'public Service Root ConfigMap changed')
        m.require(not self.client_rows(), 'factory managed issuance already exists; reconcile')
        factory = self.obj('deployment', 'factoryenroll')
        m.require(factory['spec']['replicas'] == 1
                  and factory['spec']['template']['spec'].get('automountServiceAccountToken') is False,
                  'factory deployment baseline changed')
        values = env_values(factory['spec']['template']['spec']['containers'][0])
        m.require(values.get('FACTORY_ENROLL_CERT_ISSUER_CLIENT_CERT')
                  and values.get('FACTORY_ENROLL_CERT_ISSUER_CLIENT_KEY')
                  and not values.get('FACTORY_ENROLL_SERVICE_IDENTITY_STATE'),
                  'factory bootstrap baseline changed')
        self.save('factory-deployment-baseline.json', factory)
        pull_secrets = factory['spec']['template']['spec'].get('imagePullSecrets', [])
        requested = seed_pod(self.args.image, root['certificate_fingerprint_sha256'], pull_secrets)
        m.require(not self.kube(['-n', NS, 'get', 'pod', SEED_POD,
                                 '--ignore-not-found', '-o', 'name']).strip(),
                  'seed Pod already exists; reconcile')
        if self.args.failed:
            source = Path(self.args.failed)
            verify_unused_pvc_recovery(source, factory, self.obj('persistentvolumeclaim', PVC))
            self.report['reconciled_from'] = str(source)
        else:
            self.create({
                'apiVersion': 'v1', 'kind': 'PersistentVolumeClaim',
                'metadata': {'name': PVC, 'namespace': NS},
                'spec': {'accessModes': ['ReadWriteOnce'],
                         'storageClassName': 'linode-block-storage-retain',
                         'resources': {'requests': {'storage': '10Gi'}}},
            })
        self.kube(['-n', NS, 'wait', '--for=jsonpath={.status.phase}=Bound',
                   'persistentvolumeclaim/' + PVC, '--timeout=300s'], timeout=310)
        pvc = self.obj('persistentvolumeclaim', PVC)
        m.require(pvc['status']['phase'] == 'Bound'
                  and pvc['spec']['storageClassName'] == 'linode-block-storage-retain',
                  'factory identity PVC did not bind with retain policy')
        self.save('factory-identity-pvc.json', pvc)
        before_rows = self.client_rows()
        opened = False
        try:
            # Mark intent before waiting for rollout so a rollout failure still
            # closes an already-applied temporary permission.
            opened = True
            self.patch_provisioner(CLOSED_PATTERN, BOOTSTRAP_PATTERN)
            self.save('seed-pod-request.json', requested)
            created = json.loads(self.kube(
                ['-n', NS, 'create', '-f', '-', '-o', 'json'], json.dumps(requested)))
            self.save('seed-pod-created.json', {
                'name': created['metadata']['name'], 'uid': created['metadata']['uid'],
                'resource_version': created['metadata']['resourceVersion']})
            self.kube(['-n', NS, 'wait', '--for=jsonpath={.status.phase}=Succeeded',
                       'pod/' + SEED_POD, '--timeout=300s'], timeout=310)
            pod = self.obj('pod', SEED_POD)
            m.require(pod['status']['phase'] == 'Succeeded', 'factory seed Pod did not succeed')
            public = parse_seed_log(self.kube(['-n', NS, 'logs', SEED_POD, '-c', 'seed']))
            m.require(public['inspection']['root_sha256'] == root['certificate_fingerprint_sha256']
                      and public['inspection']['state_sha256'] == public['state_sha256'],
                      'seed root pin or state digest differs')
            self.save('seed-public-state.json', public)
            self.save('seed-pod-identity.json', {
                'uid': pod['metadata']['uid'],
                'resource_version': pod['metadata']['resourceVersion'],
                'phase': pod['status']['phase']})
        finally:
            if opened:
                owner = self.obj('deployment', 'certissuer')
                current = env_values(owner['spec']['template']['spec']['containers'][0]).get(
                    'CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN')
                m.require(current in (CLOSED_PATTERN, BOOTSTRAP_PATTERN),
                          'provisioner policy drifted; reconcile')
                if current == BOOTSTRAP_PATTERN:
                    self.patch_provisioner(BOOTSTRAP_PATTERN, CLOSED_PATTERN)
        rows = self.client_rows()
        added = [row for row in rows if row not in before_rows]
        m.require(len(added) == 1 and added[0]['issuer_id'] == active['issuer_id']
                  and added[0]['subject'] == SUBJECT and added[0]['caller'] == 'factoryenroll'
                  and added[0]['status'] == 'succeeded' and added[0]['revoked_at'] is None
                  and added[0]['fingerprint'] == public['certificate_sha256'],
                  'factory initial managed issuance differs')
        self.save('seed-issuance.json', added[0])
        self.check('factory_managed_identity_seeded', {
            'subject': SUBJECT, 'issuer_id': active['issuer_id'],
            'certificate_sha256': public['certificate_sha256'],
            'state_sha256': public['state_sha256'], 'private_key_exported': False,
            'pvc_uid': pvc['metadata']['uid'], 'bootstrap_policy_closed': True,
            'seed_pod_retained_for_guarded_adoption': True,
        })

    def release_seed(self):
        source = Path(self.args.enrollment)
        m.require(m.read(source / 'report.json')['status'] == 'passed',
                  'successful seed evidence required')
        pod = self.obj('pod', SEED_POD)
        saved = m.read(source / 'seed-pod-identity.json')
        m.require(pod['metadata']['uid'] == saved['uid'] and pod['status']['phase'] == 'Succeeded',
                  'seed owner differs')
        options = {'apiVersion': 'v1', 'kind': 'DeleteOptions', 'preconditions': {
            'uid': saved['uid'], 'resourceVersion': pod['metadata']['resourceVersion']}}
        self.save('seed-release.json', options)
        self.kube(['delete', '--raw', '/api/v1/namespaces/' + NS + '/pods/' + SEED_POD,
                   '-f', '-'], json.dumps(options))
        self.kube(['-n', NS, 'wait', '--for=delete', 'pod/' + SEED_POD, '--timeout=60s'])

    def install_template(self, name, owner, template, strategy=None):
        changes = [{'op': 'test', 'path': '/spec/template', 'value': owner['spec']['template']},
                   {'op': 'replace', 'path': '/spec/template', 'value': template}]
        if strategy:
            changes.append({'op': 'replace', 'path': '/spec/strategy', 'value': {'type': strategy}})
        self.save(name + '-before.json', owner)
        self.patch('deployment', name, owner, changes)
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + name, '--timeout=300s'], timeout=310)

    def inspect_factory(self, root):
        return json.loads(self.kube(['-n', NS, 'exec', 'deployment/factoryenroll', '--',
                                     '/app/serviceidentity-bootstrap', 'inspect', STATE,
                                     SUBJECT, root['certificate_fingerprint_sha256']]))

    def adopt(self):
        source = Path(self.args.enrollment)
        root, active = self.hierarchy_v2()
        factory = self.obj('deployment', 'factoryenroll')
        saved = m.read(source / 'factory-deployment-baseline.json')
        m.require(factory['metadata']['uid'] == saved['metadata']['uid']
                  and self.obj('persistentvolumeclaim', PVC)['metadata']['uid'] ==
                  m.read(source / 'factory-identity-pvc.json')['metadata']['uid'],
                  'factory or PVC changed after seed')
        seed = m.read(source / 'seed-public-state.json')
        m.require(self.client_rows() == [m.read(source / 'seed-issuance.json')],
                  'factory issuance changed after seed')
        template = managed_template(saved, self.args.image, root['certificate_fingerprint_sha256'])
        crls = json.loads(self.obj('configmap', 'pki-service-client-crls')['data']['crls.json'])
        m.require([e['issuer']['issuer_id'] for e in crls] == [
            root['issuer_id'], m.read(self.output / 'service-intermediate-v1.json')['issuer_id'],
            active['issuer_id']], 'Service CRL manifest differs')
        for entry in crls:
            entry['state_path'] = '/state/identity/crl-' + entry['issuer']['issuer_id'] + '.json'
        if self.args.failed:
            failed = Path(self.args.failed)
            report = m.read(failed / 'report.json')
            prior = m.read(failed / 'factoryenroll-before.json')
            m.require(report['status'] == 'failed' and report['phase'] == 'adopt'
                      and prior['metadata']['uid'] == factory['metadata']['uid']
                      and prior['spec']['template'] == saved['spec']['template']
                      and template == factory['spec']['template']
                      and factory['spec']['strategy'] == {'type': 'Recreate'},
                      'failed adoption differs from reviewed factory deployment')
            m.require(json.loads(self.obj('configmap', 'factoryenroll-service-crls')['data']['crls.json']) == crls,
                      'reconcile failed CRL manifest before resuming adoption')
            controller = self.obj('deployment', 'pki-controller')
            m.require(env_values(controller['spec']['template']['spec']['containers'][0]).get(
                'PKI_REQUIRED_CONSUMERS_SERVICE') == ','.join(CONSUMERS), 'Service gate differs')
            self.report['reconciled_from'] = str(failed)
            self.finish_adoption(root, crls, seed)
            return
        m.require(factory['spec']['template'] == saved['spec']['template'],
                  'factory changed after seed')
        self.create({'apiVersion': 'v1', 'kind': 'ConfigMap',
                     'metadata': {'name': 'factoryenroll-service-crls', 'namespace': NS},
                     'data': {'crls.json': json.dumps(crls)}})
        network = self.obj('networkpolicy', 'allow-service-listener-controller')
        path = '/spec/ingress/0/from/0/podSelector/matchExpressions/0/values'
        values = network['spec']['ingress'][0]['from'][0]['podSelector']['matchExpressions'][0]['values']
        m.require(values == r.SERVICE_CONSUMERS, 'controller ingress changed')
        self.observed_patch('networkpolicy', network['metadata']['name'], network, [
            {'op': 'test', 'path': path, 'value': values},
            {'op': 'replace', 'path': path, 'value': ['certissuer', 'factoryenroll', 'pki-controller']}])
        controller_owner = self.obj('deployment', 'pki-controller')
        controller = json.loads(json.dumps(controller_owner['spec']['template']))
        env = controller['spec']['containers'][0]['env']
        m.require(env_values(controller['spec']['containers'][0]).get(
            'PKI_REQUIRED_CONSUMERS_SERVICE') == ','.join(r.SERVICE_CONSUMERS), 'Service gate changed')
        controller['spec']['containers'][0]['env'] = r.with_env(
            env, {'PKI_REQUIRED_CONSUMERS_SERVICE': ','.join(CONSUMERS)})
        self.install_template('pki-controller', controller_owner, controller)
        settings_path = self.base / 'pki/controller-bootstrap/rollout/pki-controller-service-settings.json'
        m.write(settings_path, dict(m.read(settings_path), PKI_REQUIRED_CONSUMERS_SERVICE=','.join(CONSUMERS)))
        self.install_template('factoryenroll', factory, template, 'Recreate')
        self.finish_adoption(root, crls, seed)

    def finish_adoption(self, root, crls, seed):
        network = self.obj('networkpolicy', 'allow-service-listener-controller')
        m.require(network['spec']['ingress'][0]['from'][0]['podSelector']['matchExpressions'][0]['values'] ==
                  ['certissuer', 'factoryenroll', 'pki-controller'], 'factory controller ingress differs')
        current = self.inspect_factory(root)
        m.require(current == seed['inspection'], 'factory identity changed during adoption')
        issuer_owner = self.obj('deployment', 'certissuer')
        issuer = json.loads(json.dumps(issuer_owner['spec']['template']))
        issuer['spec']['containers'][0]['env'] = r.with_env(issuer['spec']['containers'][0]['env'], {
            'CERT_ISSUER_FACTORY_CLIENT_CN_PATTERN': '^service:factory-enroll$'})
        self.install_template('certissuer', issuer_owner, issuer)
        settings_path = self.base / 'pki/controller-bootstrap/rollout/certissuer-service-settings.json'
        m.write(settings_path, dict(m.read(settings_path),
                                    CERT_ISSUER_FACTORY_CLIENT_CN_PATTERN='^service:factory-enroll$'))
        self.restart('factoryenroll')
        m.require(self.inspect_factory(root) == current, 'factory restart changed identity')
        installed = []
        for entry in crls:
            issuer_id = entry['issuer']['issuer_id']
            record = self.api('/issuers/' + issuer_id + '/crl')
            self.wait_receipts(issuer_id, record['crl_sha256'], CONSUMERS, kind='crl')
            state = json.loads(self.kube(['-n', NS, 'exec', 'deployment/factoryenroll', '--',
                                         'cat', entry['state_path']]))
            m.require(state['crl'] == record and state['issuer_fingerprint'] ==
                      entry['issuer']['certificate_fingerprint_sha256'], 'factory installed CRL differs')
            installed.append({'issuer_id': issuer_id, 'record': record, 'consumers': CONSUMERS})
        self.save('installed-crls.json', installed)
        self.save('adopted-identity.json', current)
        for name in r.SERVICE_CONSUMERS:
            desired = r.render_persisted_listener(self.base, name)['spec']['template']
            live = self.obj('deployment', name)['spec']['template']
            for value in (desired, live):
                value['spec']['containers'][0]['env'].sort(key=lambda e: e['name'])
            m.require(desired == live, 'persisted listener differs: ' + name)
        persisted = m.read(self.base / 'pki/controller-bootstrap/rollout/factoryenroll-deployment.json')
        m.require(persisted['spec']['template'] == self.obj('deployment', 'factoryenroll')['spec']['template'],
                  'persisted factory differs')
        self.factory_canary()
        self.device_baseline()
        self.check('factory_managed_adoption', {'subject': SUBJECT, 'bootstrap_free_restart': True,
                   'crl_consumers': CONSUMERS, 'image': self.args.image,
                   'state_sha256': current['state_sha256'], 'private_key_exported': False})

    def factory_canary(self):
        self.forward('factory', NS, 'factoryenroll', 80)
        body = m.read(self.foundation / 'device-2/enroll-request.json')
        now = r.dt.datetime.now(r.dt.timezone.utc)
        batch = 'factory-service-' + r.uuid.uuid4().hex
        run = self.http('/admin/brand-clouds/' + body['brand_cloud_id'] + '/device-item-profiles/' +
                        body['device_item_profile_id'] + '/production-runs', {
                            'factory_id': body['factory_id'], 'batch_id': batch, 'allowed_quantity': 1,
                            'valid_from': m.stamp(now - r.dt.timedelta(minutes=1)),
                            'valid_until': m.stamp(now + r.dt.timedelta(hours=1))}, self.login(self.admin)[0], 201)
        device = 'pki-factory-' + r.uuid.uuid4().hex
        body.update(request_id=r.uuid.uuid4().hex, devid=device, csr_pem=self.key('canary', device),
                    batch_id=batch, serial_number=batch, production_run_id=run['production_run']['id'])
        self.save('canary-request.json', body)
        result = self.http('/factory/enroll', body, run['factory_jwt'], service='factory')
        self.save('canary-response.json', result)
        self.save('canary-chain.pem', result['certificate_chain_pem'])
        auth = self.auth(self.output / 'canary', device)
        self.wait_positive_mqtt(auth, device)
        self.mqtt(auth, device, 'roundtrip')
        self.check('factory_canary', {'device': device, 'factory_enrollment': 'passed',
                                      'direct_mtls': 'passed', 'mqtt_acl_qos1': 'passed'})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', choices=['seed', 'adopt'], required=True)
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--authority', required=True)
    parser.add_argument('--output', required=True)
    parser.add_argument('--image', required=True)
    parser.add_argument('--failed', help='reconcile saved failed seed/adoption evidence')
    parser.add_argument('--enrollment', help='successful factory seed evidence')
    args = parser.parse_args()
    m.require(re.fullmatch(
        r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}', args.image),
        'verified dev factory image digest required')
    args.prepared, args.resume = None, False
    m.require(args.phase != 'adopt' or args.enrollment, 'successful seed evidence required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = FactoryIdentityRun(args)
    try:
        if args.phase == 'adopt' and not args.failed:
            runner.release_seed()
        runner.preflight()
        {'seed': runner.seed, 'adopt': runner.adopt}[args.phase]()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'
        runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        os.close(owner)
        print(json.dumps({'phase': args.phase, 'status': runner.report['status'],
                          'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    def interrupted(*_):
        raise RuntimeError('interrupted; reconcile saved factory seed evidence before retry')
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
