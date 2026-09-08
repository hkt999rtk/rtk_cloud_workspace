#!/usr/bin/env python3
"""Dev-only adoption of managed Service client egress for the two listeners."""
import argparse
import base64
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import uuid

spec = importlib.util.spec_from_file_location('service_retirement', Path(__file__).with_name('retirement.py'))
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)
h, m, NS = c.h, c.m, c.NS
NAMES = ['certissuer', 'pki-controller']
STATE = '/var/lib/pki-host/identity/client.json'
CA_MOUNT = '/run/pki-service-managed-egress-ca/ca.crt'


def profile(name):
    m.require(name in NAMES, 'unknown listener')
    prefix = 'CERT_ISSUER_SERVICE_CLIENT' if name == 'certissuer' else 'PKI_SERVICE_CLIENT'
    host_prefix = 'CERT_ISSUER_HOST_RENEWAL' if name == 'certissuer' else 'PKI_HOST_RENEWAL'
    return {'name': name, 'subject': 'service:' + name, 'prefix': prefix, 'host_prefix': host_prefix,
            'secret': 'pki-service-consumer-' + name, 'configmap': 'pki-service-managed-egress-' + name + '-ca'}


def env_values(container):
    values = {item['name']: item.get('value') for item in container.get('env', [])}
    m.require(len(values) == len(container.get('env', [])), 'duplicate listener environment')
    return values


def remove_pem(bundle, certificate):
    """Remove exactly one public certificate block without accepting malformed drift."""
    marker = certificate.strip()
    m.require(marker.startswith('-----BEGIN CERTIFICATE-----') and marker.endswith('-----END CERTIFICATE-----'),
              'expected public certificate block')
    m.require(bundle.count(marker) == 1, 'expected exactly one legacy CA block')
    updated = bundle.replace(marker, '').strip()
    m.require(updated and marker not in updated, 'legacy CA removal would empty or retain trust bundle')
    return updated + '\n'


def managed_template(owner, name, image):
    """Render a one-listener dynamic egress transition without changing other mounts."""
    selected = profile(name)
    template = json.loads(json.dumps(owner['spec']['template']))
    pod = template['spec']
    m.require(len(pod['containers']) == 1 and pod['containers'][0]['name'] == name, 'listener container changed')
    container = pod['containers'][0]
    values = env_values(container)
    prefix, host = selected['prefix'], selected['host_prefix']
    required = (prefix + '_ROOT_SHA256', prefix + '_BUNDLE_MANIFEST', prefix + '_PKI_CONTROLLER_URL',
                prefix + '_MANAGEMENT_CA', prefix + '_MANAGEMENT_CERT', prefix + '_MANAGEMENT_KEY',
                host + '_CLIENT_CERT', host + '_CLIENT_KEY')
    m.require(all(values.get(key) for key in required), 'static egress settings incomplete')
    m.require(not values.get(prefix + '_IDENTITY_STATE'), 'managed egress already configured; reconcile')
    mounts = {item['name']: item for item in container.get('volumeMounts', [])}
    volumes = {item['name']: item for item in pod.get('volumes', [])}
    m.require(mounts.get('service-consumer', {}).get('mountPath') == '/run/pki-service-consumer'
              and volumes.get('service-consumer', {}).get('secret', {}).get('secretName') == selected['secret'],
              'legacy consumer mount changed')
    m.require('service-managed-egress-ca' not in mounts and 'service-managed-egress-ca' not in volumes,
              'managed public CA mount already exists')
    remove = (prefix + '_MANAGEMENT_CERT', prefix + '_MANAGEMENT_KEY', host + '_CLIENT_CERT', host + '_CLIENT_KEY')
    container['env'] = h.h.h.with_env(container['env'], {prefix + '_IDENTITY_STATE': STATE,
                                                         prefix + '_MANAGEMENT_CA': CA_MOUNT}, remove)
    container['image'] = image
    container['volumeMounts'] = [item for item in container['volumeMounts'] if item['name'] != 'service-consumer']
    container['volumeMounts'].append({'name': 'service-managed-egress-ca',
                                      'mountPath': '/run/pki-service-managed-egress-ca', 'readOnly': True})
    pod['volumes'] = [item for item in pod['volumes'] if item['name'] != 'service-consumer']
    pod['volumes'].append({'name': 'service-managed-egress-ca', 'configMap': {'name': selected['configmap']}})
    return template


def enrollment_template(owner, image, provisioner):
    template = json.loads(json.dumps(owner['spec']['template']))
    pod = template['spec']
    m.require(len(pod['containers']) == 1 and pod['containers'][0]['name'] == 'certissuer', 'issuer container changed')
    values = env_values(pod['containers'][0])
    m.require(values.get('CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN'), 'issuer provisioner policy missing')
    pod['containers'][0]['image'] = image
    pod['containers'][0]['env'] = h.h.h.with_env(pod['containers'][0]['env'],
                                                {'CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN': provisioner})
    return template


def enrollment_database_reference(name):
    m.require(name in NAMES, 'unknown listener')
    # Resolve only inside the workload shell; never load or write the DSN into evidence.
    return '$CERT_ISSUER_DB_DSN' if name == 'certissuer' else '${PKI_DATABASE_URL:?}'


class EgressRun(c.c.CRLRun):
    def __init__(self, args):
        super().__init__(args)
        self.probe_pods = {}
        self.remote_probe = '/tmp/pki-egress-state-' + uuid.uuid4().hex
        self.report['egress_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.report['foundation_scope'] = 'Dev managed listener outgoing Service credentials'

    def selected(self):
        root, issuer = self.hierarchy()
        retired = Path(self.args.retirement)
        m.require(m.read(retired / 'report.json')['status'] == 'passed', 'successful Service retirement evidence required')
        m.require(self.api('/issuers/' + issuer['issuer_id'] + '/crl') == m.read(retired / 'published-crl.json'),
                  'Service CRL changed; reconcile maintenance before egress adoption')
        return root, issuer

    def deployment(self, name):
        owner = self.obj('deployment', name)
        m.require(owner['spec']['replicas'] == 1 and owner['spec']['strategy']['type'] == 'Recreate',
                  'listener must retain exclusive PVC ownership')
        return owner

    def apply_template(self, name, owner, template, image, settings=None):
        self.scoped_patch('deployment', owner, [{'op': 'test', 'path': '/spec/template', 'value': owner['spec']['template']},
                                                {'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + name, '--timeout=300s'], timeout=310)
        if name == 'certissuer':
            self.forward('issuer', NS, name, 9443)
        key = 'PKI_CERTISSUER_IMAGE' if name == 'certissuer' else 'PKI_CONTROLLER_IMAGE'
        m.write(self.base / 'operator/env' / key, image + '\n')
        if settings is not None:
            m.write(self.base / 'pki/controller-bootstrap/rollout' / (name + '-service-settings.json'), settings)

    def client_rows(self, subject):
        raw = self.sql("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,subject,caller,status,fingerprint,certificate_pem,issued_at,revoked_at FROM pki_service_client_issuances WHERE environment='dev' AND subject='" + subject + "' ORDER BY issued_at,request_id) t;")
        return [json.loads(line) for line in raw.splitlines() if line]

    def inspect_client(self, name, root):
        owner = self.deployment(name)
        selector = ','.join(k + '=' + v for k, v in owner['spec']['selector']['matchLabels'].items())
        pods = [p for p in json.loads(self.kube(['-n', NS, 'get', 'pods', '-l', selector, '-o', 'json']))['items']
                if not p['metadata'].get('deletionTimestamp')]
        m.require(len(pods) == 1, 'unexpected listener pod count')
        pod = pods[0]
        uid, podname = pod['metadata']['uid'], pod['metadata']['name']
        if uid not in self.probe_pods:
            binary = self.output / 'pki-dev-probe-linux'
            if not binary.exists():
                result = subprocess.run(['go', 'build', '-o', str(binary), './pki-dev-probe'], cwd=m.WORKSPACE / 'scripts/go',
                                        env=dict(os.environ, GOOS='linux', GOARCH='amd64', CGO_ENABLED='0', GOWORK='off'),
                                        capture_output=True, timeout=180)
                m.require(result.returncode == 0, 'public egress inspection probe build failed')
            self.kube(['-n', NS, 'exec', '-i', podname, '-c', name, '--', 'sh', '-c',
                       'umask 077; base64 -d > ' + self.remote_probe + ' && chmod 700 ' + self.remote_probe],
                      base64.b64encode(binary.read_bytes()).decode())
            self.probe_pods[uid] = (podname, name)
        result = json.loads(self.kube(['-n', NS, 'exec', podname, '-c', name, '--', self.remote_probe, 'service-state', STATE]))
        m.require(result['subject'] == profile(name)['subject'] and result['root_sha256'] == root['certificate_fingerprint_sha256']
                  and not result['pending'], 'managed client state differs')
        return result

    def close(self):
        for uid, record in self.probe_pods.items():
            name, container = record
            try:
                if self.obj('pod', name)['metadata']['uid'] == uid:
                    self.kube(['-n', NS, 'exec', name, '-c', container, '--', 'rm', '-f', self.remote_probe])
            except RuntimeError:
                pass
        super().close()

    def enroll(self, name):
        root, issuer = self.selected()
        selected = profile(name)
        owner = self.deployment(name)
        container = owner['spec']['template']['spec']['containers'][0]
        values = env_values(container)
        m.require(not values.get(selected['prefix'] + '_IDENTITY_STATE'), 'managed state already selected; reconcile')
        pvc = self.obj('persistentvolumeclaim', name + '-service-identity')
        baseline = {'deployment_uid': owner['metadata']['uid'], 'pvc_uid': pvc['metadata']['uid'],
                    'image': container['image'], 'static_secret': selected['secret'],
                    'existing_rows': self.client_rows(selected['subject'])}
        self.save('baseline.json', baseline)
        issuer_owner = self.deployment('certissuer')
        pattern = '^' + name + '$'
        if name == 'certissuer':
            image = self.args.image
        else:
            image = issuer_owner['spec']['template']['spec']['containers'][0]['image']
        self.apply_template('certissuer', issuer_owner, enrollment_template(issuer_owner, image, pattern), image)
        if name == 'pki-controller' and container['image'] != self.args.image:
            template = json.loads(json.dumps(owner['spec']['template']))
            template['spec']['containers'][0]['image'] = self.args.image
            self.apply_template(name, owner, template, self.args.image)
        self.kube(['-n', NS, 'exec', 'deployment/' + name, '-c', name, '--', 'test', '-x', '/app/serviceidentity-bootstrap'])
        database = enrollment_database_reference(name)
        command = ('set -eu; test ! -e ' + STATE + '; umask 077; exec env '
                   'PKI_DATABASE_URL="' + database + '" '
                   'PKI_CLIENT_IDENTITY_STATE=' + STATE + ' '
                   'PKI_CLIENT_ROOT_SHA256=' + root['certificate_fingerprint_sha256'] + ' '
                   'PKI_CLIENT_ISSUER_URL=https://certissuer.' + NS + '.svc:9443 '
                   'PKI_CLIENT_ISSUER_SERVER_PKI_ROOT_SHA256=' + root['certificate_fingerprint_sha256'] + ' '
                   'PKI_CLIENT_ISSUER_SERVER_PKI_NAME=certissuer.' + NS + '.svc '
                   'PKI_CLIENT_ISSUER_TLS_CA=/run/pki-host-root/root.pem '
                   'PKI_CLIENT_IDENTITY_BOOTSTRAP_CERT=/run/pki-service-consumer/tls.crt '
                   'PKI_CLIENT_IDENTITY_BOOTSTRAP_KEY=/run/pki-service-consumer/tls.key '
                   '/app/serviceidentity-bootstrap ' + selected['subject'])
        result = json.loads(self.kube(['-n', NS, 'exec', 'deployment/' + name, '-c', name, '--', 'sh', '-c', command]))
        rows = self.client_rows(selected['subject'])
        added = [row for row in rows if row not in baseline['existing_rows']]
        m.require(len(added) == 1 and added[0]['status'] == 'succeeded' and added[0]['revoked_at'] is None
                  and added[0]['fingerprint'] == result['certificate_sha256'] and added[0]['issuer_id'] == issuer['issuer_id'],
                  'initial managed client issuance differs')
        modes = self.kube(['-n', NS, 'exec', 'deployment/' + name, '-c', name, '--', 'sh', '-c',
                           'stat -c %a /var/lib/pki-host/identity; stat -c %a ' + STATE + '; sha256sum ' + STATE]).splitlines()
        m.require(len(modes) == 3 and modes[0] in ('700', '2700') and modes[1] == '600', 'client private state permissions differ')
        self.save('enrolled.json', {'subject': selected['subject'], 'certificate_sha256': result['certificate_sha256'],
                                    'state_sha256': modes[2].split()[0], 'issuance': added[0]})
        self.check(name + '_initial_managed_client', {'subject': selected['subject'], 'certificate_sha256': result['certificate_sha256'],
                                                      'private_key_exported': False, 'provisioner_pattern': pattern})

    def resume_enroll(self, name):
        failed = Path(self.args.failed)
        report = m.read(failed / 'report.json')
        m.require(report['status'] == 'failed' and report['phase'] == name + '-enroll', 'matching failed enrollment evidence required')
        root, _ = self.selected()
        baseline = m.read(failed / 'baseline.json')
        owner = self.deployment(name)
        saved = m.read(failed / ('after-' + NS + '-' + name + '-deployment.json'))
        m.require(owner['metadata']['uid'] == baseline['deployment_uid']
                  and self.obj('persistentvolumeclaim', name + '-service-identity')['metadata']['uid'] == baseline['pvc_uid']
                  and owner['spec']['template'] == saved['spec']['template'], 'failed enrollment runtime drifted; reconcile manually')
        values = env_values(owner['spec']['template']['spec']['containers'][0])
        m.require(values.get(profile(name)['prefix'] + '_IDENTITY_STATE') is None
                  and values.get('CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN') == '^' + name + '$',
                  'failed enrollment policy/state differs')
        self.kube(['-n', NS, 'exec', 'deployment/' + name, '-c', name, '--', 'test', '!', '-e', STATE])
        m.require(not self.client_rows(profile(name)['subject']), 'client issuance exists; do not retry enrollment')
        self.report['reconciled_from'] = str(failed)
        self.save('failed-baseline.json', baseline)
        self.enroll(name)

    def adopt(self, name):
        root, _ = self.selected()
        selected = profile(name)
        enrollment = Path(self.args.enrollment)
        m.require(m.read(enrollment / 'report.json')['status'] == 'passed', 'successful matching enrollment evidence required')
        baseline, enrolled = m.read(enrollment / 'baseline.json'), m.read(enrollment / 'enrolled.json')
        m.require(enrolled['subject'] == selected['subject'], 'enrollment subject differs')
        owner = self.deployment(name)
        m.require(owner['metadata']['uid'] == baseline['deployment_uid'] and self.obj('persistentvolumeclaim', name + '-service-identity')['metadata']['uid'] == baseline['pvc_uid'],
                  'listener/PVC changed after enrollment')
        secret = self.obj('secret', selected['secret'])
        public_ca = base64.b64decode(secret['data']['ca.crt']).decode()
        self.create({'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': selected['configmap'], 'namespace': NS},
                     'immutable': True, 'data': {'ca.crt': public_ca}})
        template = managed_template(owner, name, self.args.image)
        values = env_values(template['spec']['containers'][0])
        self.apply_template(name, owner, template, self.args.image,
                            {key: value for key, value in values.items() if key.startswith(selected['prefix'])})
        current = self.inspect_client(name, root)
        m.require(current['fingerprint'] == enrolled['certificate_sha256'] and current['state_sha256'] == enrolled['state_sha256'],
                  'managed client changed during adoption')
        # The paired listener must no longer accept this legacy client CA after its owner changed.
        trust_secret, field = ('pki-controller-tls', 'ca.crt') if name == 'certissuer' else ('certissuer-runtime', 'client-ca.crt')
        trust = self.obj('secret', trust_secret)
        bundle = base64.b64decode(trust['data'][field]).decode()
        updated = remove_pem(bundle, public_ca)
        self.scoped_patch('secret', trust, [{'op': 'replace', 'path': '/data/' + field,
                                             'value': base64.b64encode(updated.encode()).decode()}])
        if name == 'certissuer':
            issuer = self.deployment('certissuer')
            self.apply_template('certissuer', issuer, enrollment_template(issuer, self.args.image, '^pki-controller$'), self.args.image)
        else:
            issuer = self.deployment('certissuer')
            self.apply_template('certissuer', issuer, enrollment_template(issuer, self.args.image, '^$'), self.args.image)
        owner = self.deployment(name)
        self.apply_template(name, owner, owner['spec']['template'], self.args.image,
                            {key: value for key, value in env_values(owner['spec']['template']['spec']['containers'][0]).items() if key.startswith(selected['prefix'])})
        m.require(self.inspect_client(name, root) == current, 'restart changed managed client state')
        self.check(name + '_managed_egress_adopted', {'subject': selected['subject'], 'state_sha256': current['state_sha256'],
                                                       'bootstrap_free_restart': True, 'static_ca_removed_from_peer': trust_secret})

    def verify(self):
        root, issuer = self.selected()
        for name in NAMES:
            selected = profile(name)
            source = Path(self.args.certissuer if name == 'certissuer' else self.args.controller)
            m.require(m.read(source / 'report.json')['status'] == 'passed', 'successful listener adoption evidence required')
            identity = self.inspect_client(name, root)
            rows = [row for row in self.client_rows(selected['subject']) if row['fingerprint'] == identity['fingerprint']]
            m.require(len(rows) == 1 and rows[0]['status'] == 'succeeded' and rows[0]['revoked_at'] is None and rows[0]['issuer_id'] == issuer['issuer_id'],
                      'managed client lacks current registry admission')
            owner = self.deployment(name)
            values = env_values(owner['spec']['template']['spec']['containers'][0])
            m.require(values.get(selected['prefix'] + '_IDENTITY_STATE') == STATE
                      and not values.get(selected['prefix'] + '_MANAGEMENT_CERT')
                      and not values.get(selected['prefix'] + '_MANAGEMENT_KEY')
                      and not values.get(selected['host_prefix'] + '_CLIENT_CERT')
                      and not values.get(selected['host_prefix'] + '_CLIENT_KEY'), 'static egress settings remain')
            mounts = [item['name'] for item in owner['spec']['template']['spec']['containers'][0]['volumeMounts']]
            m.require('service-consumer' not in mounts and 'service-managed-egress-ca' in mounts, 'runtime egress mounts differ')
        issuer_owner = self.deployment('certissuer')
        issuer_values = env_values(issuer_owner['spec']['template']['spec']['containers'][0])
        m.require(issuer_values.get('CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN') == '^$', 'bootstrap provisioner remains enabled')
        self.device_baseline()
        self.check('managed_listener_egress_verified', {'listeners': NAMES, 'private_client_states': True,
                                                        'legacy_secret_deletion': 'requires separate guarded cleanup after fresh CRL receipts'})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', choices=['certissuer-enroll', 'resume-certissuer-enroll', 'certissuer-adopt', 'controller-enroll', 'controller-adopt', 'verify'], required=True)
    parser.add_argument('--config-root', default=str(Path.home() / '.config/rtk_cloud'))
    for key in ('authority', 'intermediate', 'retirement', 'output'):
        parser.add_argument('--' + key, required=True)
    parser.add_argument('--image')
    parser.add_argument('--enrollment')
    parser.add_argument('--certissuer')
    parser.add_argument('--controller')
    parser.add_argument('--failed')
    args = parser.parse_args()
    m.require(args.phase == 'verify' or re.fullmatch(r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}', args.image or ''),
              'verified dev listener image required')
    m.require(args.phase not in ('certissuer-adopt', 'controller-adopt') or args.enrollment, 'matching enrollment evidence required')
    m.require(args.phase != 'resume-certissuer-enroll' or args.failed, 'failed enrollment evidence required')
    m.require(args.phase not in ('controller-enroll', 'controller-adopt', 'verify') or args.certissuer, 'certissuer evidence required')
    m.require(args.phase != 'verify' or args.controller, 'controller evidence required')
    fd = os.open(Path(args.config_root).expanduser() / 'dev/pki/service-egress.lock', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = EgressRun(args)
    try:
        runner.preflight()
        {'certissuer-enroll': lambda: runner.enroll('certissuer'), 'resume-certissuer-enroll': lambda: runner.resume_enroll('certissuer'),
         'certissuer-adopt': lambda: runner.adopt('certissuer'),
         'controller-enroll': lambda: runner.enroll('pki-controller'), 'controller-adopt': lambda: runner.adopt('pki-controller'),
         'verify': runner.verify}[args.phase]()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'; runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report); runner.close(); os.close(fd)
        print(json.dumps({'phase': args.phase, 'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    def interrupted(*_):
        raise RuntimeError('interrupted; reconcile saved egress evidence and runtime before retry')
    signal.signal(signal.SIGTERM, interrupted); signal.signal(signal.SIGINT, interrupted)
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr); sys.exit(1)
