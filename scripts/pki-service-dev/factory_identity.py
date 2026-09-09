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
STATE = '/state/identity.json'
BOOTSTRAP_PATTERN = '^factoryenroll$'
CLOSED_PATTERN = '^$'


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


def seed_pod(image, root_sha256):
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
            'restartPolicy': 'Never',
            'securityContext': {
                'runAsUser': 10001, 'runAsGroup': 10001, 'fsGroup': 10001,
                'fsGroupChangePolicy': 'OnRootMismatch'},
            'containers': [{
                'name': 'seed', 'image': image,
                'command': ['sh', '-c'],
                'args': [
                    'set -eu; test ! -e ' + STATE + '; umask 077; '
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
            self.patch_provisioner(CLOSED_PATTERN, BOOTSTRAP_PATTERN)
            opened = True
            requested = seed_pod(self.args.image, root['certificate_fingerprint_sha256'])
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
            self.save('seed-public-state.json', public)
            self.save('seed-pod-identity.json', {
                'uid': pod['metadata']['uid'],
                'resource_version': pod['metadata']['resourceVersion'],
                'phase': pod['status']['phase']})
        finally:
            if opened:
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


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', choices=['seed'], required=True)
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--authority', required=True)
    parser.add_argument('--output', required=True)
    parser.add_argument('--image', required=True)
    args = parser.parse_args()
    m.require(re.fullmatch(
        r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}', args.image),
        'verified dev factory image digest required')
    args.prepared, args.resume = None, False
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = FactoryIdentityRun(args)
    try:
        runner.preflight()
        runner.seed()
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
