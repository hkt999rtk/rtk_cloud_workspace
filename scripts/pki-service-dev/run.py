#!/usr/bin/env python3
"""Dev-only Service Root rollout. Each phase uses a new private evidence directory."""
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
        m.require(m.device_consumers(env) == m.CONSUMERS and env.get('PKI_REQUIRED_CONSUMERS_SERVICE') == ','.join(SERVICE_CONSUMERS), 'persisted domain gates changed')
    return desired


class ServiceRun(m.Acceptance):
    def __init__(self, args):
        super().__init__(args.config_root, 'lke649805-ctx', args.output)
        self.args = args
        self.report['foundation_scope'] = 'Existing dev Device baseline; independent Service Root rollout'
        self.report['checks'] = {}
        self.report['phase'] = args.phase
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
        device = m.read(self.foundation / 'device-2/enroll-request.json')['devid']
        auth = self.auth(self.foundation / 'device-2/v4', device)
        attempts = self.wait_positive_mqtt(auth, device)
        self.check('device_baseline_after_service_root', {'direct_mtls': 'passed', 'mqtt_acl_qos1': 'passed', 'attempts': attempts, 'full_device_lifecycle_rerun': False})

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
        receipts = self.wait_receipts(root['issuer_id'], root['trust_bundle_version'], SERVICE_CONSUMERS)
        for name in SERVICE_CONSUMERS:
            desired = render_persisted_listener(self.base, name)
            current = self.obj('deployment', name)
            wanted = desired['spec']['template']['spec']['containers'][0]
            actual = current['spec']['template']['spec']['containers'][0]
            m.require(actual['image'] == wanted['image'] and {e['name']: e for e in actual['env']} == {e['name']: e for e in wanted['env']}, 'live/persisted listener configuration differs')
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
                  'service_intermediate_and_callers': 'not yet deployed'})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', choices=['prepare-root', 'controller', 'certissuer', 'activate-root', 'finish-root-crl', 'verify'], required=True)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--authority')
    parser.add_argument('--image')
    parser.add_argument('--activation')
    args = parser.parse_args()
    m.require(args.phase == 'prepare-root' or args.authority, 'prior prepared Root evidence required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    m.require(args.phase != 'finish-root-crl' or args.activation, 'saved signed activation evidence required')
    runner = ServiceRun(args)
    try:
        runner.preflight()
        {'prepare-root': runner.prepare_root, 'controller': lambda: runner.rollout_listener(True),
         'certissuer': lambda: runner.rollout_listener(False), 'activate-root': runner.activate_root, 'finish-root-crl': runner.finish_root_crl, 'verify': runner.verify_runtime}[args.phase]()
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
