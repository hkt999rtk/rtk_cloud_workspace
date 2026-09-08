#!/usr/bin/env python3
"""Pre-expiry dev Service CRL maintenance with durable publication recovery."""
import argparse
import base64
import datetime as dt
import fcntl
import http.client
import http.server
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import sys
import threading
import uuid

spec = importlib.util.spec_from_file_location('managed_account', Path(__file__).with_name('management.py'))
h = importlib.util.module_from_spec(spec)
spec.loader.exec_module(h)
m = h.m


def der_digest(pem):
    lines = pem.strip().splitlines()
    m.require(lines[0] == '-----BEGIN X509 CRL-----' and lines[-1] == '-----END X509 CRL-----', 'invalid CRL PEM')
    return m.digest(base64.b64decode(''.join(lines[1:-1]), validate=True))


def refresh_request(issuer, previous, entries, now):
    number = str(int(previous['crl_number']) + 1)
    m.require(0 < int(number) < 2 ** 159, 'CRL number exhausted')
    m.require(previous['issuer_id'] == issuer['issuer_id'] and der_digest(previous['crl_pem']) == previous['crl_sha256'], 'prior CRL identity changed')
    now = now.replace(microsecond=0)
    m.require(now >= dt.datetime.fromisoformat(previous['this_update'].replace('Z', '+00:00')), 'clock predates prior CRL')
    return {'issuer_id': issuer['issuer_id'], 'issuer_fingerprint_sha256': issuer['certificate_fingerprint_sha256'],
            'crl_number': number, 'this_update': m.stamp(now), 'next_update': m.stamp(now + dt.timedelta(hours=72)),
            'revocations': entries}


def publication_state(current, previous, desired):
    m.require(current['issuer_id'] == previous['issuer_id'] == desired['issuer_id'], 'publication issuer changed')
    if current == desired:
        return 'published'
    m.require(current == previous, 'unrelated CRL update; reconcile without overwriting')
    m.require(int(desired['crl_number']) > int(previous['crl_number']), 'CRL rollback')
    return 'pending'


def drop_response(port, path, body, token):
    """Forward exactly one fixed loopback POST; discard its response, never retry."""
    result = {'forwarded': 0, 'response_lost': False}
    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_): pass
        def do_POST(self):
            self.connection.settimeout(50)
            if self.path != path or self.headers.get('Transfer-Encoding') or self.headers.get('Content-Length') != str(len(body)):
                self.send_error(400); return
            received = self.rfile.read(len(body))
            if received != body:
                self.send_error(400); return
            upstream = http.client.HTTPConnection('127.0.0.1', int(port), timeout=45)
            try:
                result['forwarded'] += 1
                upstream.request('POST', path, received, {'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})
                response = upstream.getresponse()
                result['upstream_status'] = response.status
                m.require(len(response.read(1048577)) <= 1048576, 'oversized upstream response')
            except Exception:
                result['upstream_error'] = True
            finally:
                upstream.close()
                self.close_connection = True
    server = http.server.HTTPServer(('127.0.0.1', 0), Handler)
    server.timeout = 55
    thread = threading.Thread(target=server.handle_request, daemon=True)
    thread.start()
    client = http.client.HTTPConnection('127.0.0.1', server.server_port, timeout=55)
    try:
        client.request('POST', path, body, {'Content-Type': 'application/json'})
        try:
            client.getresponse()
        except http.client.RemoteDisconnected:
            result['response_lost'] = True
    finally:
        client.close()
        thread.join(55)
        server.server_close()
    m.require(not thread.is_alive(), 'response-loss proxy did not stop')
    return result


class CRLRun(h.ManagementRun):
    def state_digest(self, name='account-manager'):
        if name in h.h.h.SERVICE_CONSUMERS:
            return h.h.HostRun.state_digest(self, name)
        return super().state_digest(name)

    def snapshot(self):
        self.verify_identity()
        for name in h.h.h.SERVICE_CONSUMERS:
            source, _, issued = self.prepared(name)
            self.check_host(name, source, issued)
        return {'states': {name: self.state_digest(name) for name in ['account-manager'] + h.h.h.SERVICE_CONSUMERS},
                'issuance': self.issuance(), 'images': self.report['checks']['preflight']['evidence']['images'],
                'workers': self.worker_images()}

    def prepare_root(self):
        root, _ = self.hierarchy()
        self.save('baseline.json', self.snapshot())
        previous = self.api('/issuers/' + root['issuer_id'] + '/crl')
        self.save('previous.json', previous)
        self.save('issuer.json', root)
        entries = json.loads(m.command([self.probe, 'crl'], previous['crl_pem'])) or []
        request = refresh_request(root, previous, entries, dt.datetime.now(dt.timezone.utc))
        self.save('request.json', request)
        digest = self.ceremony_call(['crl-digest', self.output / 'request.json']).strip()
        self.save('request-digest.json', {'sha256': digest})
        authority = Path(self.args.authority)
        self.ceremony_call(['crl', '--issuer', self.output / 'issuer.json', '--crl-request', self.output / 'request.json',
                           '--expected-request-sha256', digest, '--key', authority / 'root-offline-simulation/ca-key.encrypted.pem',
                           '--passphrase-file', m.read(authority / 'passphrase-reference.json')['path'], '--out', self.output / 'signed'])
        self.artifact(self.output, root)
        self.check('root_signed', {'number': request['crl_number'], 'next_update': request['next_update'], 'published': False})

    def inspect(self, raw, issuer):
        self.save('inspect.pem', raw)
        self.save('inspect-issuer.pem', issuer['certificate_pem'])
        # OpenSSL's verify failure is checked explicitly: some versions exit zero.
        result = m.subprocess.run([self.openssl, 'crl', '-in', str(self.output / 'inspect.pem'), '-noout', '-verify',
                                   '-CAfile', str(self.output / 'inspect-issuer.pem')], capture_output=True, timeout=15)
        m.require(result.returncode == 0 and b'verify OK' in result.stderr, 'CRL signature invalid')
        fields = dict(line.split('=', 1) for line in m.command([self.openssl, 'crl', '-in', self.output / 'inspect.pem',
                         '-noout', '-crlnumber', '-lastupdate', '-nextupdate']).strip().splitlines())
        def date(value):
            return m.stamp(dt.datetime.strptime(value, '%b %d %H:%M:%S %Y GMT').replace(tzinfo=dt.timezone.utc))
        return {'issuer_id': issuer['issuer_id'], 'crl_sha256': der_digest(raw), 'crl_number': str(int(fields['crlNumber'], 16)),
                'crl_pem': raw, 'this_update': date(fields['lastUpdate']), 'next_update': date(fields['nextUpdate'])}

    def artifact(self, source, issuer):
        m.require(m.read(source / 'issuer.json') == issuer, 'saved authority changed')
        raw = (source / 'signed/revocations.pem').read_text()
        desired = self.inspect(raw, issuer)
        previous = m.read(source / 'previous.json')
        m.require(int(desired['crl_number']) > int(previous['crl_number']), 'signed CRL rollback')
        old_entries = json.loads(m.command([self.probe, 'crl'], previous['crl_pem'])) or []
        entries = json.loads(m.command([self.probe, 'crl'], raw)) or []
        m.require(all(entry in entries for entry in old_entries), 'revocation entries lost')
        if issuer['kind'] == 'root':
            request = m.read(source / 'request.json')
            manifest = m.read(source / 'signed/public-manifest.json')
            digest = self.ceremony_call(['crl-digest', source / 'request.json']).strip()
            m.require(digest == m.read(source / 'request-digest.json')['sha256'] == manifest['request_sha256']
                      and manifest['request'] == request and manifest['crl_sha256'] == desired['crl_sha256'], 'signed request changed')
            m.require(request['issuer_fingerprint_sha256'] == issuer['certificate_fingerprint_sha256']
                      and all(request[k] == desired[k] for k in ('issuer_id', 'crl_number', 'this_update', 'next_update'))
                      and request['revocations'] == entries == old_entries, 'signed refresh differs from request')
        if (source / 'desired.json').exists():
            m.require(m.read(source / 'desired.json') == desired, 'saved signed artifact changed')
        self.save('desired.json', desired)
        return previous, desired

    def publish(self, fault=False):
        root, intermediate = self.hierarchy()
        source = Path(self.args.source)
        m.require((source / 'desired.json').is_file(), 'completed signed artifact evidence required')
        saved = m.read(source / 'issuer.json')
        issuer = root if saved['kind'] == 'root' else intermediate
        previous, desired = self.artifact(source, issuer)
        path = '/issuers/' + issuer['issuer_id'] + '/crl'
        state = publication_state(self.api(path), previous, desired)
        if fault:
            m.require(state == 'pending', 'response-loss experiment requires unpublished artifact')
            result = drop_response(self.ports['am'][0], '/v1/platform/pki' + path,
                                   json.dumps({'crl_pem': desired['crl_pem']}).encode(), self.token())
            self.save('response-loss.json', result)
            m.require(result == {'forwarded': 1, 'response_lost': True, 'upstream_status': 200}, 'publication response loss not established; reconcile')
            raise RuntimeError('intentional publication response loss; reconcile the saved signed artifact')
        if state == 'pending':
            m.require(self.api(path, {'crl_pem': desired['crl_pem']}) == desired, 'published CRL differs')
        m.require(self.api(path, {'crl_pem': desired['crl_pem']}) == desired, 'idempotent import changed CRL')
        m.require(self.api(path) == desired, 'current CRL changed')
        identifier, digest = str(uuid.UUID(issuer['issuer_id'])), desired['crl_sha256']
        m.require(re.fullmatch('[0-9a-f]{64}', digest), 'invalid digest')
        counts = self.sql("SELECT (SELECT count(*) FROM pki_crls WHERE issuer_id='" + identifier + "' AND digest='" + digest + "'),"
                          "(SELECT count(*) FROM pki_audit WHERE issuer_id='" + identifier + "' AND event='crl_imported:" + digest + "');")
        m.require(counts == '1|1', 'duplicate/missing CRL or import audit')
        self.api(path, {'crl_pem': previous['crl_pem']}, expected=409)
        m.require(self.api(path) == desired, 'rollback denial changed current CRL')
        self.check('publication_reconciled', {'issuer_id': identifier, 'initial_state': state, 'crl': desired,
                                            'rows': 1, 'import_audits': 1, 'rollback_denied': True, 'resigned': False})

    def prepare_intermediate(self, resume=False):
        _, issuer = self.hierarchy()
        self.save('issuer.json', issuer)
        previous = self.api('/issuers/' + issuer['issuer_id'] + '/crl')
        policies = m.read(Path(self.args.intermediate) / 'provider-policies.json')
        mount = 'pki-issuers/service/' + issuer['issuer_id'] + '/v1'
        m.require(policies['mount'] == mount, 'provider mount changed')
        self.save('previous.json', previous)
        jwt = self.kube(['-n', h.NS, 'create', 'token', 'pki-controller', '--audience=openbao', '--duration=10m']).strip()
        token = json.loads(self.bao(['write', '-format=json', 'auth/kubernetes/login', '-'],
                                   json.dumps({'role': 'pki-controller-dev', 'jwt': jwt})))['auth']['client_token']
        try:
            def read_crl():
                return json.loads(self.bao(['read', '-format=json', mount + '/cert/crl'], token=token))['data']['certificate']
            before = read_crl()
            if resume:
                source = Path(self.args.source)
                m.require(m.read(source / 'issuer.json') == issuer and m.read(source / 'previous.json') == previous,
                          'provider recovery baseline changed')
                m.require(m.read(source / 'rotation-intent.json')['mount'] == mount, 'rotation intent missing')
                m.require(der_digest(before) != der_digest((source / 'provider-before.pem').read_text()),
                          'provider CRL unchanged; no blind rotation retry')
                raw = before
            else:
                m.require(der_digest(before) == previous['crl_sha256'], 'provider has unpublished CRL; reconcile')
                self.save('provider-before.pem', before)
                self.save('rotation-intent.json', {'mount': mount, 'at': m.stamp()})
                self.bao(['read', '-format=json', mount + '/crl/rotate'], token=token)
                raw = read_crl()
            self.save('signed/revocations.pem', raw)
            self.artifact(self.output, issuer)
        finally:
            self.bao(['write', 'auth/token/revoke', '-'], json.dumps({'token': token}))
        self.check('intermediate_rotated', {'mount': mount, 'published': False, 'workload_auth': 'pki-controller-dev', 'recovered': resume})

    def verify(self):
        root, intermediate = self.hierarchy()
        for issuer, directory in zip((root, intermediate), (self.args.root_publication, self.args.intermediate_publication)):
            source = Path(directory)
            m.require(m.read(source / 'report.json')['status'] == 'passed', 'successful publication required')
            desired = m.read(source / 'desired.json')
            m.require(desired['issuer_id'] == issuer['issuer_id'] and self.api('/issuers/' + issuer['issuer_id'] + '/crl') == desired, 'published CRL changed before verification')
        baseline = m.read(Path(self.args.source) / 'baseline.json')
        m.require(self.snapshot() == baseline, 'managed identity or workload image changed')
        self.device_baseline()
        rows = [json.loads(line) for line in self.sql("SELECT row_to_json(t) FROM (SELECT i.domain AS trust_domain,i.kind,i.id AS issuer_id,c.number,c.digest,c.next_update "
            "FROM pki_issuers i LEFT JOIN LATERAL (SELECT number,digest,next_update FROM pki_crls WHERE issuer_id=i.id ORDER BY number DESC LIMIT 1) c ON true "
            "WHERE i.environment='dev' AND i.status='active' ORDER BY c.next_update) t;").splitlines()]
        m.require(rows and all(row['next_update'] and dt.datetime.fromisoformat(row['next_update']) > dt.datetime.now(dt.timezone.utc) + dt.timedelta(hours=1) for row in rows), 'active CRL close to expiry')
        self.save('active-crl-deadlines.json', rows)
        self.check('refresh_acceptance', {'unchanged_identity_and_images': True, 'earliest_deadline': rows[0],
                                         'installed_crl_receipts': 'unqualified', 'post_expiry_recovery': 'unqualified'})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', required=True, choices=['prepare-root', 'lose-response', 'publish', 'prepare-intermediate', 'resume-intermediate', 'verify'])
    parser.add_argument('--config-root', default=str(Path.home() / '.config/rtk_cloud'))
    for name in ('authority', 'intermediate', 'prepared', 'output'):
        parser.add_argument('--' + name, required=True)
    parser.add_argument('--source')
    parser.add_argument('--root-publication')
    parser.add_argument('--intermediate-publication')
    args = parser.parse_args()
    m.require(args.phase in ('prepare-root', 'prepare-intermediate') or args.source, 'saved phase evidence required')
    m.require(args.phase != 'verify' or (args.root_publication and args.intermediate_publication), 'both publication reports required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = CRLRun(args)
    runner.report['crl_runner_sha256'] = m.digest(Path(__file__).read_bytes())
    runner.report['foundation_scope'] = 'Pre-expiry dev Service CRL maintenance and publication recovery'
    try:
        runner.preflight()
        {'prepare-root': runner.prepare_root, 'lose-response': lambda: runner.publish(True), 'publish': runner.publish,
         'prepare-intermediate': runner.prepare_intermediate, 'resume-intermediate': lambda: runner.prepare_intermediate(True),
         'verify': runner.verify}[args.phase]()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'; runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report); runner.close(); os.close(fd)
        print(json.dumps({'phase': args.phase, 'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    def interrupted(*_): raise RuntimeError('interrupted; reconcile saved state before replay')
    signal.signal(signal.SIGTERM, interrupted); signal.signal(signal.SIGINT, interrupted)
    try: main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr); sys.exit(1)
