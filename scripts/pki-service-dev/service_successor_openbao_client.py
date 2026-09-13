#!/usr/bin/env python3
"""Move the Dev OpenBao Service client to the final Service authority."""
import argparse
import base64
import copy
import datetime as dt
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import sys
import time

spec = importlib.util.spec_from_file_location('service_hierarchy', Path(__file__).with_name('run.py'))
r = importlib.util.module_from_spec(spec); spec.loader.exec_module(r)
m, NS = r.m, r.NS
factory_spec = importlib.util.spec_from_file_location('factory_identity', Path(__file__).with_name('factory_identity.py'))
factory = importlib.util.module_from_spec(factory_spec); factory_spec.loader.exec_module(factory)

SECRETS_NS, NAME, CLIENT = 'video-cloud-dev-secrets', 'openbao', 'openbao-pki'
SUBJECT = 'service:openbao'
OLD_STATE = '/var/lib/openbao-pki/service/client.json'
NEW_STATE = '/var/lib/openbao-pki/service/client-root-697e8e86.json'
HOST_STATE = '/var/lib/openbao-pki/host/server.json'
OLD = '87099089d30f13a7b93035b59c1c0c91bdb05bbe3dab427258c0c48e38144cc2'
ROOT = '32bbbfd220db619ebcf54af5f62221ed49635e58ddaa42e67730676f073704eb'
FINAL = 'd61845ca-6b85-4f11-920b-f2f9685b0c13'
SOURCE_ROOT_CONFIGMAP = 'pki-service-host-root-697e8e86-5af'
ROOT_CONFIGMAP = 'openbao-pki-service-root-697e8e86-5af'
OLD_ROOT_CONFIGMAP = 'openbao-pki-service-root'
IMAGE = 'ghcr.io/hkt999rtk/rtk_cloud_dev/openbao-pki@sha256:cbdca931d6055202d7f8c240e1a8f3411915309e77b0ced78cb8dd7d7c8bb6ee'


def env_map(container):
    values = {entry['name']: entry for entry in container.get('env', [])}
    m.require(len(values) == len(container.get('env', [])), 'duplicate OpenBao PKI environment settings')
    return values


def update_env(container, updates, remove=()):
    container['env'] = [entry for entry in container['env'] if entry['name'] not in updates and entry['name'] not in remove]
    container['env'].extend({'name': key, 'value': value} for key, value in updates.items())


def client_container(template):
    matches = [container for container in template['spec']['containers'] if container['name'] == CLIENT]
    m.require(len(matches) == 1, 'OpenBao Service client owner changed')
    return matches[0]


def root_volume(template, expected):
    matches = [volume for volume in template['spec']['volumes'] if volume['name'] == 'service-root']
    m.require(len(matches) == 1 and matches[0].get('configMap', {}).get('name') == expected,
              'OpenBao Service Root mount changed')
    return matches[0]


def all_openbao_images(template, image):
    containers = template['spec']['containers']
    init = template['spec'].get('initContainers', [])
    m.require([container['name'] for container in containers] == ['openbao', CLIENT]
              and [container['name'] for container in init] == ['openbao-pki-install'],
              'OpenBao owner topology changed')
    for container in containers + init:
        container['image'] = image


def transition_template(owner, output):
    template = copy.deepcopy(owner['spec']['template'])
    container = client_container(template)
    values = env_map(container)
    expected = {
        'OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_STATE': OLD_STATE,
        'OPENBAO_PKI_SERVICE_CLIENT_ROOT_SHA256': OLD,
        'OPENBAO_PKI_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256': OLD,
    }
    m.require(all(values.get(key, {}).get('value') == value for key, value in expected.items()),
              'OpenBao predecessor transition baseline changed')
    m.require(not values.get('OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_TRANSITION_FROM_STATE') and
              not values.get('OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_TRANSITION_FROM_ROOT_SHA256'),
              'OpenBao transition is already configured; reconcile retained evidence')
    root_volume(template, OLD_ROOT_CONFIGMAP)['configMap']['name'] = ROOT_CONFIGMAP
    all_openbao_images(template, IMAGE)
    update_env(container, {
        'OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_STATE': NEW_STATE,
        'OPENBAO_PKI_SERVICE_CLIENT_ROOT_SHA256': ROOT,
        'OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_TRANSITION_FROM_STATE': OLD_STATE,
        'OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_TRANSITION_FROM_ROOT_SHA256': OLD,
        # This is transport trust for CertIssuer, not OpenBao-TLS host identity.
        'OPENBAO_PKI_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256': ROOT,
    })
    template.setdefault('metadata', {}).setdefault('annotations', {})['rtk.cloud/r2-openbao-final-client'] = output.name
    return template


def steady_template(owner, output):
    template = copy.deepcopy(owner['spec']['template'])
    container = client_container(template)
    values = env_map(container)
    expected = {
        'OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_STATE': NEW_STATE,
        'OPENBAO_PKI_SERVICE_CLIENT_ROOT_SHA256': ROOT,
        'OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_TRANSITION_FROM_STATE': OLD_STATE,
        'OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_TRANSITION_FROM_ROOT_SHA256': OLD,
        'OPENBAO_PKI_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256': ROOT,
    }
    m.require(all(values.get(key, {}).get('value') == value for key, value in expected.items()),
              'OpenBao transition deployment changed')
    root_volume(template, ROOT_CONFIGMAP)
    all_openbao_images(template, IMAGE)
    update_env(container, {}, ('OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_TRANSITION_FROM_STATE',
                               'OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_TRANSITION_FROM_ROOT_SHA256'))
    template.setdefault('metadata', {}).setdefault('annotations', {})['rtk.cloud/r2-openbao-final-client-restart'] = output.name
    return template


class OpenBaoFinalClient(r.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.probes = {}
        self.report['foundation_scope'] = ('Dev OpenBao final Service-client transition; '
                                           'OpenBao-TLS host identity, seal and provider state remain unchanged')
        self.report['service_successor_openbao_client_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def pod(self):
        pod = self.obj('pod', NAME + '-0', SECRETS_NS)
        m.require(pod.get('status', {}).get('phase') == 'Running', 'OpenBao Pod is not running')
        return pod

    def install_probe(self, pod):
        uid = pod['metadata']['uid']
        if uid in self.probes:
            return self.probes[uid]
        binary = self.output / 'pki-dev-probe-linux'
        if not binary.exists():
            result = m.subprocess.run(['go', 'build', '-trimpath', '-ldflags=-s -w', '-o', str(binary), './pki-dev-probe'],
                                      cwd=m.WORKSPACE / 'scripts/go',
                                      env=dict(os.environ, GOOS='linux', GOARCH='amd64', CGO_ENABLED='0', GOWORK='off'),
                                      capture_output=True, timeout=180)
            m.require(result.returncode == 0, 'OpenBao public state probe build failed')
        remote = '/run/openbao-pki/pki-dev-probe'
        self.kube(['-n', SECRETS_NS, 'exec', '-i', pod['metadata']['name'], '-c', CLIENT, '--', 'sh', '-ec',
                   'umask 077; base64 -d > ' + remote + ' && chmod 700 ' + remote],
                  base64.b64encode(binary.read_bytes()).decode())
        self.probes[uid] = remote
        return remote

    def state(self, path, root):
        pod = self.pod()
        probe = self.install_probe(pod)
        value = json.loads(self.kube(['-n', SECRETS_NS, 'exec', pod['metadata']['name'], '-c', CLIENT, '--',
                                      probe, 'service-state', path]))
        m.require(value.get('subject') == SUBJECT and value.get('root_sha256') == root and not value.get('pending')
                  and re.fullmatch(r'[0-9a-f]{64}', value.get('fingerprint', ''))
                  and re.fullmatch(r'[0-9a-f]{64}', value.get('public_key_sha256', '')),
                  'OpenBao Service client state differs')
        return value

    def host_state(self):
        pod = self.pod()
        probe = self.install_probe(pod)
        value = json.loads(self.kube(['-n', SECRETS_NS, 'exec', pod['metadata']['name'], '-c', CLIENT, '--',
                                      probe, 'service-state', HOST_STATE]))
        owner = self.obj('statefulset', NAME, SECRETS_NS)
        values = env_map(client_container(owner['spec']['template']))
        expected_name, expected_root = values.get('OPENBAO_PKI_HOST_NAME', {}).get('value'), values.get('OPENBAO_PKI_HOST_ROOT_SHA256', {}).get('value')
        m.require(value.get('subject') == expected_name and value.get('root_sha256') == expected_root and not value.get('pending')
                  and re.fullmatch(r'[0-9a-f]{64}', value.get('fingerprint', ''))
                  and re.fullmatch(r'[0-9a-f]{64}', value.get('public_key_sha256', '')),
                  'OpenBao TLS host state differs')
        return value

    def rows(self):
        query = ("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,subject,caller,status,fingerprint,issued_at "
                 "FROM pki_service_client_issuances WHERE environment='dev' AND subject='service:openbao' "
                 "AND revoked_at IS NULL ORDER BY issued_at,request_id) t;")
        raw = self.kube(['-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--', 'psql', '-X',
                         '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'video_cloud', '-At'], query)
        return [json.loads(line) for line in raw.splitlines() if line]

    @staticmethod
    def admission(rows, identity):
        matches = [row for row in rows if row['fingerprint'] == identity['fingerprint']]
        m.require(len(matches) == 1 and matches[0]['subject'] == SUBJECT and matches[0]['status'] == 'succeeded',
                  'OpenBao identity lacks exactly one active registry admission')
        return matches[0]

    @staticmethod
    def replacement(before, after, rows_before, rows_after):
        predecessor = OpenBaoFinalClient.admission(rows_before, before)
        successor = OpenBaoFinalClient.admission(rows_after, after)
        m.require(successor not in rows_before and predecessor['issuer_id'] != FINAL
                  and successor['issuer_id'] == FINAL and successor['caller'] == SUBJECT
                  and after['fingerprint'] != before['fingerprint'] and after['public_key_sha256'] != before['public_key_sha256'],
                  'OpenBao final client registry result differs')
        return successor

    def issuer(self):
        issuer = self.api('/issuers/' + FINAL)
        m.require(issuer['status'] == 'active' and issuer['environment'] == 'dev' and issuer['trust_domain'] == 'service'
                  and SUBJECT in issuer['service_client_ids'], 'final OpenBao client authority changed')
        self.api('/issuers/' + FINAL + '/crl')

    def ensure_root_bundle(self):
        source = self.obj('configmap', SOURCE_ROOT_CONFIGMAP, NS)
        raw = source.get('data', {}).get('root.pem', '')
        m.require(source.get('immutable') and raw.count('-----BEGIN CERTIFICATE-----') == 2,
                  'reviewed Service two-root bundle changed')
        target = {'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': ROOT_CONFIGMAP, 'namespace': SECRETS_NS},
                  'immutable': True, 'data': {'root.pem': raw}}
        existing = self.kube(['-n', SECRETS_NS, 'get', 'configmap', ROOT_CONFIGMAP, '--ignore-not-found', '-o', 'json']).strip()
        if existing:
            value = json.loads(existing)
            m.require(value.get('immutable') and value.get('data', {}) == target['data'],
                      'OpenBao Service two-root bundle differs')
        else:
            self.kube(['-n', SECRETS_NS, 'create', '-f', '-'], json.dumps(target))
        self.save('openbao-service-root-bundle.json', {'name': ROOT_CONFIGMAP, 'immutable': True,
                                                        'sha256': hashlib.sha256(raw.encode()).hexdigest(), 'certificates': 2})

    def patch_in(self, kind, name, namespace, before, changes):
        self.save('before-' + name + '-' + kind + '.json', before)
        guarded = [{'op': 'test', 'path': '/metadata/resourceVersion', 'value': before['metadata']['resourceVersion']}]
        guarded.extend(changes)
        return json.loads(self.kube(['-n', namespace, 'patch', kind, name, '--type=json', '--patch-file=/dev/stdin', '-o', 'json'],
                                    json.dumps(guarded)))

    def restart(self):
        pod = self.pod()
        options = {'apiVersion': 'v1', 'kind': 'DeleteOptions', 'preconditions': {'uid': pod['metadata']['uid']}}
        self.kube(['delete', '--raw', '/api/v1/namespaces/' + SECRETS_NS + '/pods/' + pod['metadata']['name'], '-f', '-'],
                  json.dumps(options))
        deadline = time.monotonic() + 120
        while True:
            raw = self.kube(['-n', SECRETS_NS, 'get', 'pod', NAME + '-0', '--ignore-not-found', '-o', 'json']).strip()
            if raw:
                current = json.loads(raw)
                statuses = {item['name']: item for item in current.get('status', {}).get('containerStatuses', [])}
                if current['metadata']['uid'] != pod['metadata']['uid'] and current.get('status', {}).get('phase') == 'Running' and statuses.get('openbao', {}).get('state', {}).get('running'):
                    break
            m.require(time.monotonic() < deadline, 'OpenBao replacement Pod did not start')
            time.sleep(2)
        # OpenBao seals after every Pod replacement.  Unseal before requiring
        # the managed sidecar to become Ready; a retained client request is
        # reconciled below if the first start reached CertIssuer while sealed.
        key = self.base / 'openbao/unseal-key'
        info = os.lstat(key)
        m.require(not key.is_symlink() and info.st_mode & 0o077 == 0,
                  'Dev OpenBao unseal custody material changed')
        deadline = time.monotonic() + 90
        while True:
            try:
                self.kube(['-n', SECRETS_NS, 'exec', '-i', NAME + '-0', '-c', 'openbao', '--', 'sh', '-ec',
                           'read -r key; BAO_SKIP_VERIFY=true bao operator unseal "$key" >/dev/null'],
                          key.read_text())
                return
            except RuntimeError:
                m.require(time.monotonic() < deadline, 'OpenBao did not accept the retained Dev unseal material')
                time.sleep(2)

    def pending_claim(self):
        query = ("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,subject,caller,status,csr_pem,"
                 "request_digest,ttl_days,created_at,revoked_at FROM pki_service_client_issuances "
                 "WHERE environment='dev' AND issuer_id='" + FINAL + "' AND subject='service:openbao' "
                 "AND status='issuing' AND revoked_at IS NULL ORDER BY created_at,request_id) t;")
        rows = [json.loads(line) for line in self.kube([
            '-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--', 'psql', '-X',
            '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'video_cloud', '-At'], query).splitlines() if line]
        m.require(len(rows) <= 1, 'ambiguous retained OpenBao final-client claim')
        if not rows:
            return None
        claim = rows[0]
        m.require(claim['caller'] == SUBJECT and claim['subject'] == SUBJECT and claim['issuer_id'] == FINAL
                  and claim['status'] == 'issuing' and claim['csr_pem'] and claim['request_digest']
                  and 1 <= claim['ttl_days'] <= 90 and not claim['revoked_at'],
                  'retained OpenBao final-client claim differs')
        return claim

    @staticmethod
    def recovery_ttl(claim, issuer, now):
        """Preserve the validity window CertIssuer assigned to the request."""
        m.require(claim['status'] == 'issuing' and not claim['revoked_at']
                  and claim['subject'] == SUBJECT and 1 <= claim['ttl_days'] <= 90
                  and issuer['status'] == 'active' and issuer['trust_domain'] == 'service',
                  'OpenBao recovery claim or issuer is not eligible')
        end = min(m.parse_time(claim['created_at']) + dt.timedelta(days=claim['ttl_days']),
                  m.parse_time(issuer['not_after']) - dt.timedelta(days=30))
        ttl = int((end - now).total_seconds()) - 60
        m.require(ttl > 60, 'OpenBao recovery certificate validity window has elapsed')
        return ttl, end

    def provider_matches(self, claim, issuer):
        """Return active and revoked provider leaves matching this retained CSR."""
        public = lambda kind, pem: m.command([self.openssl, kind, '-pubkey', '-noout'], pem).strip()
        expected = public('req', claim['csr_pem'])
        serials = json.loads(self.bao(['list', '-format=json', issuer['signer_reference'] + '/certs']))
        m.require(isinstance(serials, list) and len(serials) == len(set(serials)),
                  'OpenBao provider certificate inventory differs')
        active, revoked = [], []
        for serial in serials:
            m.require(re.fullmatch(r'[0-9a-fA-F:-]+', serial), 'OpenBao provider certificate serial differs')
            record = json.loads(self.bao(['read', '-format=json', issuer['signer_reference'] + '/cert/' + serial]))['data']
            if public('x509', record['certificate']) == expected:
                item = {'serial_number': serial, 'certificate': record['certificate'],
                        'revocation_time': record.get('revocation_time', 0)}
                (revoked if item['revocation_time'] else active).append(item)
        m.require(len(active) <= 1 and len(revoked) <= 1,
                  'retained OpenBao client request has multiple provider certificates')
        return expected, active, revoked

    def certificate_not_after(self, certificate):
        value = m.command([self.openssl, 'x509', '-noout', '-enddate'], certificate).strip()
        match = re.fullmatch(r'notAfter=(.+ GMT)', value)
        m.require(match, 'OpenBao provider certificate expiry differs')
        return dt.datetime.strptime(match.group(1), '%b %d %H:%M:%S %Y GMT').replace(tzinfo=dt.timezone.utc)

    def recovery_marker(self, claim):
        marker = self.base / 'pki' / ('r2-openbao-final-client-recovery-' + claim['request_id'] + '.json')
        if marker.exists():
            info = os.lstat(marker)
            m.require(not marker.is_symlink() and info.st_mode & 0o077 == 0, 'OpenBao recovery marker custody changed')
            value = m.read(marker)
            m.require(value.get('request_id') == claim['request_id']
                      and value.get('request_digest') == claim['request_digest']
                      and value.get('csr_sha256') == hashlib.sha256(claim['csr_pem'].encode()).hexdigest(),
                      'OpenBao recovery marker does not match retained request')
        else:
            value = {'request_id': claim['request_id'], 'request_digest': claim['request_digest'],
                     'csr_sha256': hashlib.sha256(claim['csr_pem'].encode()).hexdigest()}
        return marker, value

    def revoke_invalid_provider_leaf(self, claim, issuer, active, end):
        """Revoke only the one unregistered leaf made by the old TTL bug."""
        m.require(len(active) == 1, 'OpenBao invalid provider leaf is not unique')
        actual = self.certificate_not_after(active[0]['certificate'])
        m.require(actual > end, 'OpenBao provider leaf is not invalid for the original request window')
        marker, value = self.recovery_marker(claim)
        # The pre-existing marker is proof this is the one sealed-start recovery
        # attempt.  It may be from the older runner, which did not record a phase.
        m.require(value.get('phase') in (None, 'initial_signing', 'invalid_leaf_revoked'),
                  'OpenBao recovery marker cannot authorize provider leaf repair')
        self.bao(['write', issuer['signer_reference'] + '/revoke', '-'],
                 json.dumps({'serial_number': active[0]['serial_number']}))
        _, after_active, after_revoked = self.provider_matches(claim, issuer)
        m.require(not after_active and len(after_revoked) == 1
                  and after_revoked[0]['serial_number'] == active[0]['serial_number'],
                  'OpenBao invalid provider leaf was not revoked')
        value.update({'phase': 'invalid_leaf_revoked', 'invalid_serial_number': active[0]['serial_number'],
                      'invalid_not_after': m.stamp(actual), 'allowed_not_after': m.stamp(end),
                      'invalid_leaf_revoked_at': m.stamp()})
        m.write(marker, value)
        self.save('openbao-invalid-provider-leaf-revoked.json', {
            'request_id': claim['request_id'], 'provider_serial': active[0]['serial_number'],
            'reason': 'validity exceeded original request window', 'private_keys_exported': False,
        })

    def sign_recovery_leaf(self, claim, issuer, expected, marker, value, ttl):
        phase = value.get('phase')
        m.require(phase in (None, 'invalid_leaf_revoked'),
                  'OpenBao recovery signing marker already exists; use provider discovery only')
        value.update({'phase': 'replacement_signing' if phase == 'invalid_leaf_revoked' else 'initial_signing',
                      'ttl_seconds': ttl, 'signing_started_at': m.stamp()})
        m.write(marker, value)
        jwt = self.kube(['-n', NS, 'create', 'token', 'certissuer-pki', '--audience=openbao', '--duration=10m']).strip()
        login = json.loads(self.bao(['write', '-format=json', 'auth/kubernetes/login', '-'],
                                    json.dumps({'role': 'certissuer-pki-dev', 'jwt': jwt})))
        token = login['auth']['client_token']
        try:
            signed = json.loads(self.bao(['write', '-format=json', issuer['signer_reference'] + '/sign/service-client', '-'],
                                         json.dumps({'csr': claim['csr_pem'], 'common_name': SUBJECT,
                                                     'exclude_cn_from_sans': True, 'ttl': str(ttl) + 's'}), token=token))['data']
        finally:
            self.bao(['write', 'auth/token/revoke', '-'], json.dumps({'token': token}))
        m.require(m.command([self.openssl, 'x509', '-pubkey', '-noout'], signed['certificate']).strip() == expected,
                  'OpenBao recovery provider returned a different public key')
        value.update({'phase': 'replacement_signed', 'replacement_serial_number': signed['serial_number'],
                      'replacement_signed_at': m.stamp()})
        m.write(marker, value)
        return signed['serial_number']

    def reconcile_pending_claim(self):
        """Complete only the retained request created while a replacement was sealed."""
        claim = self.pending_claim()
        if claim is None:
            return False
        issuer = self.api('/issuers/' + FINAL)
        m.require(issuer['status'] == 'active' and issuer['environment'] == 'dev'
                  and issuer['trust_domain'] == 'service'
                  and SUBJECT in issuer['service_client_ids']
                  and re.fullmatch(r'pki-issuers/service/[0-9a-f-]{36}/v[0-9]+', issuer.get('signer_reference', '')),
                  'final OpenBao client issuer differs during recovery')
        ttl, end = self.recovery_ttl(claim, issuer, dt.datetime.now(dt.timezone.utc))
        expected, active, revoked = self.provider_matches(claim, issuer)
        if active and self.certificate_not_after(active[0]['certificate']) > end:
            self.revoke_invalid_provider_leaf(claim, issuer, active, end)
            expected, active, revoked = self.provider_matches(claim, issuer)
        signed = False
        if not active:
            marker, value = self.recovery_marker(claim)
            serial = self.sign_recovery_leaf(claim, issuer, expected, marker, value, ttl)
            active = [{'serial_number': serial}]
            signed = True
        self.api('/issuers/' + FINAL + '/reconcile-service-client',
                 {'caller': SUBJECT, 'request_id': claim['request_id'], 'serial_number': active[0]['serial_number']}, role='approver')
        self.save('openbao-final-client-recovery.json', {
            'request_id': claim['request_id'], 'provider_serial': active[0]['serial_number'],
            'signed_during_recovery': signed, 'private_keys_exported': False,
        })
        return True

    def wait_successor(self, before, rows_before):
        deadline, recovered = time.monotonic() + 240, False
        while True:
            try:
                after, rows_after = self.state(NEW_STATE, ROOT), self.rows()
                return after, rows_after, self.replacement(before, after, rows_before, rows_after), recovered
            except RuntimeError:
                if not recovered and self.pending_claim() is not None:
                    recovered = self.reconcile_pending_claim()
                m.require(time.monotonic() < deadline, 'OpenBao final client transition incomplete; retain evidence')
                time.sleep(2)

    def rotate(self):
        super().preflight()
        self.issuer()
        self.ensure_root_bundle()
        owner = self.obj('statefulset', NAME, SECRETS_NS)
        m.require(owner['spec'].get('replicas') == 1 and owner['spec'].get('updateStrategy', {}).get('type') == 'OnDelete'
                  and owner.get('status', {}).get('readyReplicas') == 1, 'OpenBao StatefulSet is not ready for controlled restart')
        before, host_before, rows_before = self.state(OLD_STATE, OLD), self.host_state(), self.rows()
        m.require(self.admission(rows_before, before)['issuer_id'] != FINAL, 'OpenBao predecessor admission differs')
        self.save('baseline.json', {'client': before, 'host': host_before, 'rows': rows_before})
        template = transition_template(owner, self.output)
        self.patch_in('statefulset', NAME, SECRETS_NS, owner, [
            {'op': 'test', 'path': '/spec/template', 'value': owner['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': template},
        ])
        self.restart()
        after, rows_after, successor, recovered = self.wait_successor(before, rows_before)
        m.require(self.host_state() == host_before, 'OpenBao Service client transition changed the OpenBao-TLS host identity')
        self.save('renewed.json', {'client': after, 'host': host_before, 'rows': rows_after, 'request_id': successor['request_id']})
        factory.FactoryIdentityRun.factory_canary(self)
        self.device_baseline()
        current = self.obj('statefulset', NAME, SECRETS_NS)
        steady = steady_template(current, self.output)
        self.patch_in('statefulset', NAME, SECRETS_NS, current, [
            {'op': 'test', 'path': '/spec/template', 'value': current['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': steady},
        ])
        self.restart()
        self.kube(['-n', SECRETS_NS, 'wait', '--for=condition=Ready', 'pod/' + NAME + '-0', '--timeout=300s'], timeout=310)
        m.require(self.state(NEW_STATE, ROOT) == after and self.host_state() == host_before and self.rows() == rows_after,
                  'OpenBao restart changed the final Service or host identity')
        self.check('openbao_final_service_client_restart', {
            'final_issuer_id': FINAL, 'client_request_id': successor['request_id'],
            'openbao_tls_host_identity_changed': False, 'restart_preserved_identity': True,
            'sealed_start_request_reconciled': recovered,
            'factory_and_device_canaries_before_restart': 'passed', 'private_keys_exported': False, 'staging_touched': False,
        })

    def verify(self, source):
        saved, renewed = m.read(source / 'report.json'), m.read(source / 'renewed.json')
        m.require(saved.get('status') == 'passed' and renewed.get('client') and renewed.get('host') and renewed.get('rows'),
                  'completed OpenBao transition evidence required')
        super().preflight()
        self.issuer()
        after, host, rows = self.state(NEW_STATE, ROOT), self.host_state(), self.rows()
        m.require(after == renewed['client'] and host == renewed['host'] and rows == renewed['rows'],
                  'OpenBao final state changed after transition')
        owner = self.obj('statefulset', NAME, SECRETS_NS)
        values = env_map(client_container(owner['spec']['template']))
        expected = {'OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_STATE': NEW_STATE,
                    'OPENBAO_PKI_SERVICE_CLIENT_ROOT_SHA256': ROOT,
                    'OPENBAO_PKI_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256': ROOT}
        m.require(all(values.get(key, {}).get('value') == value for key, value in expected.items())
                  and not values.get('OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_TRANSITION_FROM_STATE')
                  and not values.get('OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_TRANSITION_FROM_ROOT_SHA256'),
                  'OpenBao final transition settings changed')
        root_volume(owner['spec']['template'], ROOT_CONFIGMAP)
        factory.FactoryIdentityRun.factory_canary(self)
        self.device_baseline()
        self.check('openbao_final_service_client_verified', {
            'final_issuer_id': FINAL, 'client_request_id': renewed['request_id'],
            'fresh_factory_and_device_canaries': 'passed', 'verification_source': str(source),
            'private_keys_exported': False, 'staging_touched': False,
        })

    def recover(self, source):
        saved = m.read(source / 'report.json')
        baseline = m.read(source / 'baseline.json')
        m.require(saved.get('status') == 'failed' and saved.get('phase') == 'openbao-final-service-client'
                  and baseline.get('client') and baseline.get('host') and baseline.get('rows'),
                  'failed OpenBao transition evidence required for recovery')
        super().preflight(); self.issuer(); self.ensure_root_bundle()
        owner = self.obj('statefulset', NAME, SECRETS_NS)
        values = env_map(client_container(owner['spec']['template']))
        expected = {
            'OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_STATE': NEW_STATE,
            'OPENBAO_PKI_SERVICE_CLIENT_ROOT_SHA256': ROOT,
            'OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_TRANSITION_FROM_STATE': OLD_STATE,
            'OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_TRANSITION_FROM_ROOT_SHA256': OLD,
            'OPENBAO_PKI_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256': ROOT,
        }
        m.require(all(values.get(key, {}).get('value') == value for key, value in expected.items()),
                  'OpenBao failed transition settings changed')
        root_volume(owner['spec']['template'], ROOT_CONFIGMAP)
        all_openbao_images(owner['spec']['template'], IMAGE)
        before, host_before, rows_before = baseline['client'], baseline['host'], baseline['rows']
        after, rows_after, successor, recovered = self.wait_successor(before, rows_before)
        m.require(self.host_state() == host_before, 'OpenBao recovery changed the OpenBao-TLS host identity')
        self.save('renewed.json', {'client': after, 'host': host_before, 'rows': rows_after, 'request_id': successor['request_id']})
        factory.FactoryIdentityRun.factory_canary(self); self.device_baseline()
        current = self.obj('statefulset', NAME, SECRETS_NS)
        steady = steady_template(current, self.output)
        self.patch_in('statefulset', NAME, SECRETS_NS, current, [
            {'op': 'test', 'path': '/spec/template', 'value': current['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': steady},
        ])
        self.restart(); self.kube(['-n', SECRETS_NS, 'wait', '--for=condition=Ready', 'pod/' + NAME + '-0', '--timeout=300s'], timeout=310)
        m.require(self.state(NEW_STATE, ROOT) == after and self.host_state() == host_before and self.rows() == rows_after,
                  'OpenBao recovery restart changed final Service or host identity')
        self.check('openbao_final_service_client_recovered', {
            'final_issuer_id': FINAL, 'client_request_id': successor['request_id'],
            'sealed_start_request_reconciled': recovered, 'restart_preserved_identity': True,
            'factory_and_device_canaries_before_restart': 'passed', 'private_keys_exported': False, 'staging_touched': False,
        })


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--verify-from', type=Path, help='Completed transition evidence; checks final state without mutation')
    parser.add_argument('--recover-from', type=Path, help='Failed sealed-start transition evidence; reconciles its retained request')
    args = parser.parse_args(); args.phase = 'openbao-final-service-client'; args.authority = None
    os.umask(0o077)
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600); fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = OpenBaoFinalClient(args)
    try:
        m.require(not (args.verify_from and args.recover_from), 'choose one OpenBao verification or recovery source')
        if args.verify_from:
            runner.verify(args.verify_from)
        elif args.recover_from:
            runner.recover(args.recover_from)
        else:
            runner.rotate()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error); raise
    finally:
        runner.save('report.json', runner.report); runner.close(); os.close(fd)
        print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    try: main()
    except Exception as error: print(json.dumps({'error': str(error)}), file=sys.stderr); sys.exit(1)
