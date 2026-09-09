#!/usr/bin/env python3
"""Dev-only factory renewal, held-socket retirement and legacy cleanup."""
import argparse
import base64
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

spec = importlib.util.spec_from_file_location('factory_identity', Path(__file__).with_name('factory_identity.py'))
f = importlib.util.module_from_spec(spec)
spec.loader.exec_module(f)
m, NS = f.m, f.NS


def replacement(before, after, rows, updated, issuer):
    m.require(after['subject'] == before['subject'] == f.SUBJECT
              and after['root_sha256'] == before['root_sha256'] and not after['pending']
              and all(after[k] != before[k] for k in ('fingerprint', 'public_key_sha256', 'state_sha256')),
              'factory identity did not rotate cleanly')
    added = [row for row in updated if row not in rows]
    m.require(len(added) == 1 and len(updated) == len(rows) + 1
              and all(row in updated for row in rows), 'unexpected factory issuance changes')
    row = added[0]
    m.require(row['fingerprint'] == after['fingerprint'] and row['issuer_id'] == issuer['issuer_id']
              and row['subject'] == row['caller'] == f.SUBJECT
              and row['status'] == 'succeeded' and row['revoked_at'] is None,
              'factory renewal registry receipt differs')
    return row


class FactoryLifecycle(f.FactoryIdentityRun):
    def __init__(self, args):
        super().__init__(args)
        self.remote_probe = '/state/identity/.pki-dev-probe-' + uuid.uuid4().hex
        self.probe_installed = False
        self.report['factory_lifecycle_runner_sha256'] = m.digest(Path(__file__).read_bytes())

    def close(self):
        super().close()
        if self.probe_installed:
            self.kube(['-n', NS, 'exec', 'deployment/factoryenroll', '--', 'rm', '-f', self.remote_probe])

    def prerequisite(self):
        source = Path(self.args.adoption)
        report = m.read(source / 'report.json')
        m.require(report['status'] == 'passed' and report['phase'] == 'adopt', 'successful factory adoption required')
        root, issuer = self.hierarchy_v2()
        owner = self.obj('deployment', 'factoryenroll')
        container = owner['spec']['template']['spec']['containers'][0]
        env = f.env_values(container)
        m.require(owner['spec']['replicas'] == 1 and owner['spec']['strategy'] == {'type': 'Recreate'}
                  and env.get('FACTORY_ENROLL_SERVICE_IDENTITY_STATE') == f.STATE
                  and container['image'] == report['checks']['factory_managed_adoption']['evidence']['image']
                  and not any(env.get('FACTORY_ENROLL_CERT_ISSUER_' + suffix) for suffix in
                              ('CLIENT_KEY', 'CLIENT_CERT', 'MANAGEMENT_KEY', 'MANAGEMENT_CERT')),
                  'factory managed runtime changed')
        persisted = m.read(self.base / 'pki/controller-bootstrap/rollout/factoryenroll-deployment.json')
        m.require(owner['spec']['template'] == persisted['spec']['template'], 'factory persisted template differs')
        m.require(f.env_values(self.obj('deployment', 'pki-controller')['spec']['template']['spec']['containers'][0])[
            'PKI_REQUIRED_CONSUMERS_SERVICE'] == ','.join(f.CONSUMERS), 'Service consumer gate differs')
        self.save('owner-before.json', owner)
        self.save('pvc-before.json', self.obj('persistentvolumeclaim', f.PVC))
        return root, issuer

    def install_probe(self):
        binary = self.output / 'pki-dev-probe-linux'
        result = m.subprocess.run(['go', 'build', '-trimpath', '-o', str(binary), './pki-dev-probe'],
            cwd=m.WORKSPACE / 'scripts/go', env=dict(os.environ, GOOS='linux', GOARCH='amd64', CGO_ENABLED='0', GOWORK='off'),
            capture_output=True, timeout=180)
        m.require(result.returncode == 0, 'session probe build failed')
        self.probe_installed = True
        self.kube(['-n', NS, 'exec', '-i', 'deployment/factoryenroll', '--', 'sh', '-c',
                   'umask 077; base64 -d > ' + self.remote_probe + ' && chmod 700 ' + self.remote_probe],
                  base64.b64encode(binary.read_bytes()).decode())
        actual = self.kube(['-n', NS, 'exec', 'deployment/factoryenroll', '--', 'sha256sum', self.remote_probe]).split()[0]
        m.require(actual == m.digest(binary.read_bytes()), 'installed probe digest differs')
        self.save('probe-binary.json', {'sha256': actual})

    def session(self, endpoint, issuer, identity):
        host = endpoint + '.' + NS + '.svc'
        port, path = ('18446', '/v1/pki/issuers/' + issuer['issuer_id'] + '/crl') if endpoint == 'pki-controller' else ('9443', '/healthz')
        process = m.Process(self.k + ['-n', NS, 'exec', '-i', 'deployment/factoryenroll', '--', self.remote_probe,
            'service-session', f.STATE, '/run/service-root/root.pem', host, port, path, identity['fingerprint']], keep_stdin=True)
        self.children.append(process)
        event = process.event()
        m.require(event['event'] == 'ready' and event['fingerprint'] == identity['fingerprint'], 'session identity differs')
        self.save(endpoint + '-' + identity['fingerprint'] + '-session.json', event)
        return process

    def session_command(self, process, command, expected):
        process.child.stdin.write(command + '\n')
        process.child.stdin.flush()
        event = process.event(timeout=30)
        m.require(event['event'] == expected, 'held session result differs')
        return event

    def lifecycle(self):
        root, issuer = self.prerequisite()
        before, rows = self.inspect_factory(root), self.client_rows()
        m.require(before == m.read(Path(self.args.adoption) / 'adopted-identity.json')
                  and len(rows) == 1 and rows[0]['fingerprint'] == before['fingerprint']
                  and rows[0]['revoked_at'] is None, 'initial factory identity changed; reconcile before signaling')
        self.save('baseline.json', {'identity': before, 'rows': rows})
        self.install_probe()
        endpoints = ('certissuer', 'pki-controller')
        old = {name: self.session(name, issuer, before) for name in endpoints}
        m.require(self.kube(['-n', NS, 'exec', 'deployment/factoryenroll', '--', 'cat', '/proc/1/comm']).strip() == 'factoryenroll',
                  'PID 1 is not the factory identity owner')
        self.save('renewal-intent.json', {'at': m.stamp(), 'previous_fingerprint': before['fingerprint']})
        self.kube(['-n', NS, 'exec', 'deployment/factoryenroll', '--', 'sh', '-c', 'kill -HUP 1'])
        deadline = time.monotonic() + 90
        while True:
            after = self.inspect_factory(root)
            if not after['pending'] and after['fingerprint'] != before['fingerprint']:
                break
            m.require(time.monotonic() < deadline, 'renewal incomplete; do not signal again')
            time.sleep(2)
        updated = self.client_rows()
        receipt = replacement(before, after, rows, updated, issuer)
        self.save('renewed.json', {'identity': after, 'rows': updated})
        new = {name: self.session(name, issuer, after) for name in endpoints}
        for process in list(old.values()) + list(new.values()):
            self.session_command(process, 'check', 'alive')
        previous = self.api('/issuers/' + issuer['issuer_id'] + '/crl')
        self.save('previous-crl.json', previous)
        body = {'certificate_sha256': before['fingerprint'], 'reason': 'Replaced by factory renewal ' + receipt['request_id']}
        self.save('revocation-intent.json', body)
        started = m.dt.datetime.now(m.dt.timezone.utc)
        revoked = self.api('/issuers/' + issuer['issuer_id'] + '/revoke-service-client', body)
        self.save('revoked.json', revoked)
        sockets = {}
        for name in endpoints:
            closed = self.session_command(old[name], 'closed', 'closed')
            delay = (m.parse_time(closed['at']) - started).total_seconds()
            m.require(0 <= delay <= 30, 'factory old-socket cutoff exceeded 30 seconds')
            denied = self.session_command(old[name], 'denied', 'denied')
            alive = self.session_command(new[name], 'check', 'alive')
            sockets[name] = {'closed': closed, 'cutoff_seconds_upper_bound': delay, 'fresh_old_denied': denied, 'successor_alive': alive}
        self.save('socket-cutoff.json', sockets)
        publication = self.api('/issuers/' + issuer['issuer_id'] + '/publish-service-client-revocation',
                               {'certificate_sha256': before['fingerprint']})
        self.save('published-crl.json', publication)
        m.require(int(publication['crl_number']) > int(previous['crl_number']), 'CRL did not advance')
        old_entries = json.loads(m.command([self.probe, 'crl'], previous['crl_pem'])) or []
        entries = json.loads(m.command([self.probe, 'crl'], publication['crl_pem'])) or []
        certificate = self.sql("SELECT certificate_pem FROM pki_service_client_issuances WHERE fingerprint='" + before['fingerprint'] + "';")
        self.save('replaced-leaf.pem', certificate)
        serial = m.command([self.openssl, 'x509', '-in', self.output / 'replaced-leaf.pem', '-noout', '-serial']).strip().split('=')[1]
        m.require(all(entry in entries for entry in old_entries)
                  and any(int(entry['serial_hex'], 16) == int(serial, 16) for entry in entries), 'CRL omitted revoked serials')
        self.wait_receipts(issuer['issuer_id'], publication['crl_sha256'], f.CONSUMERS, 'crl')
        final = self.api('/issuers/' + issuer['issuer_id'] + '/finalize-service-client-revocation',
                         {'certificate_sha256': before['fingerprint']})
        m.require(final['crl_sha256'] == publication['crl_sha256'], 'retirement finalized against another CRL')
        self.save('finalized.json', final)
        for process in new.values():
            self.session_command(process, 'check', 'alive')
        for process in list(old.values()) + list(new.values()):
            process.child.stdin.close()
            m.require(process.child.wait(timeout=10) == 0, 'remote session probe did not stop')
        self.restart('factoryenroll')
        m.require(self.inspect_factory(root) == after, 'factory restart changed renewed identity')
        self.factory_canary()
        self.device_baseline()
        self.check('factory_lifecycle', {'renewal_request_id': receipt['request_id'], 'exactly_one_renewal': True,
                   'held_socket_cutoff': sockets, 'required_receipts': f.CONSUMERS, 'bootstrap_free_restart': True,
                   'private_keys_exported': False, 'legacy_cleanup_pending': True})

    def cleanup(self):
        root, issuer = self.prerequisite()
        source = Path(self.args.lifecycle)
        report = m.read(source / 'report.json')
        m.require(report['status'] == 'passed' and report['phase'] == 'lifecycle', 'successful factory lifecycle required')
        current = m.read(source / 'renewed.json')['identity']
        m.require(self.inspect_factory(root) == current, 'factory successor changed')
        published = m.read(source / 'published-crl.json')
        m.require(self.api('/issuers/' + issuer['issuer_id'] + '/crl') == published, 'retirement CRL changed')
        self.wait_receipts(issuer['issuer_id'], published['crl_sha256'], f.CONSUMERS, 'crl')
        name = 'factoryenroll-certissuer-client'
        workloads = json.loads(self.kube(['-n', NS, 'get', 'deploy,statefulset,daemonset,job,cronjob,pod', '-o', 'json']))['items']
        references = [item['kind'] + '/' + item['metadata']['name'] for item in workloads if name in json.dumps(item.get('spec', {}))]
        m.require(not references, 'factory bootstrap Secret is still referenced')
        secret = self.obj('secret', name)
        decode = lambda value: base64.b64decode(value).decode()
        def fingerprint(pem):
            return m.command([self.openssl, 'x509', '-noout', '-fingerprint', '-sha256'], pem).strip().split('=')[1].replace(':', '').lower()
        leaf = decode(secret['data']['client.crt'])
        leaf_fp = fingerprint(leaf)
        m.require(self.sql("SELECT count(*) FROM pki_service_client_issuances WHERE fingerprint='" + leaf_fp + "';") == '0',
                  'legacy factory leaf unexpectedly registered')
        certs = re.findall(r'-----BEGIN CERTIFICATE-----.*?-----END CERTIFICATE-----', decode(secret['data']['ca.crt']), re.S)
        legacy = [pem for pem in certs if fingerprint(pem) != root['certificate_fingerprint_sha256']]
        m.require(len(legacy) == 1, 'expected one legacy factory CA alongside Service Root')
        am_secret = self.obj('secret', 'account-manager-certissuer-client', 'video-cloud-dev-account-manager')
        self.save('legacy-ca.pem', legacy[0])
        self.save('legacy-factory-leaf.pem', leaf)
        self.save('am-legacy-leaf.pem', decode(am_secret['data']['client.crt']))
        for certificate in ('legacy-factory-leaf.pem', 'am-legacy-leaf.pem'):
            m.command([self.openssl, 'verify', '-purpose', 'sslclient', '-CAfile', self.output / 'legacy-ca.pem', self.output / certificate])
        trust = self.obj('secret', 'certissuer-runtime')['data']['client-ca.crt']
        m.require(legacy[0] in decode(trust), 'shared legacy CA is no longer trusted; reconcile inventory')
        am = self.obj('deployment', 'account-manager', 'video-cloud-dev-account-manager')
        m.require(any(v.get('secret', {}).get('secretName') == 'account-manager-certissuer-client'
                      for v in am['spec']['template']['spec']['volumes']), 'Account Manager trust dependency changed')
        policies = f.env_values(self.obj('deployment', 'certissuer')['spec']['template']['spec']['containers'][0])
        m.require(policies['CERT_ISSUER_FACTORY_CLIENT_CN_PATTERN'] == '^service:factory-enroll$'
                  and policies['CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN'] == '^$', 'factory legacy permissions reopened')
        saved = self.base / ('pki/controller-bootstrap/rollout/' + name + '-secret.json')
        if saved.exists():
            m.require(m.read(saved)['data'] == secret['data'], 'persisted bootstrap Secret differs')
        options = {'apiVersion': 'v1', 'kind': 'DeleteOptions', 'preconditions': {
            'uid': secret['metadata']['uid'], 'resourceVersion': secret['metadata']['resourceVersion']}}
        self.save('legacy-inventory.json', {'secret': name, 'deletion': options, 'references': references,
                   'legacy_leaf_sha256': leaf_fp, 'shared_ca_sha256': fingerprint(legacy[0]),
                   'retained_for': 'account-manager-certissuer-client', 'legacy_registry_rows': 0})
        self.kube(['delete', '--raw', '/api/v1/namespaces/' + NS + '/secrets/' + name, '-f', '-'], json.dumps(options))
        saved.unlink(missing_ok=True)
        m.require(not self.kube(['-n', NS, 'get', 'secret', name, '--ignore-not-found', '-o', 'name']).strip(), 'bootstrap Secret remains')
        m.require(self.obj('secret', 'certissuer-runtime')['data']['client-ca.crt'] == trust, 'shared CA changed during cleanup')
        self.restart('factoryenroll')
        m.require(self.inspect_factory(root) == current, 'cleanup restart changed successor')
        self.factory_canary()
        self.device_baseline()
        self.check('factory_legacy_secret_removed', {'references': 0, 'bootstrap_secret_present': False,
                   'shared_ca_retained_for_account_manager': True, 'shared_ca_withdrawal_pending': True})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', choices=['lifecycle', 'cleanup'], default='lifecycle')
    parser.add_argument('--lifecycle', help='successful factory renewal/retirement evidence for cleanup')
    for name in ('authority', 'adoption', 'output'):
        parser.add_argument('--' + name, required=True)
    parser.add_argument('--config-root', default=str(Path.home() / '.config/rtk_cloud'))
    args = parser.parse_args()
    args.resume, args.prepared = False, None
    m.require(args.phase != 'cleanup' or args.lifecycle, 'successful lifecycle evidence required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = FactoryLifecycle(args)
    try:
        runner.preflight()
        {'lifecycle': runner.lifecycle, 'cleanup': runner.cleanup}[args.phase]()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report.update(status='failed', failure=str(error))
        raise
    finally:
        try:
            runner.close()
        except Exception as error:
            runner.report.update(status='failed', cleanup_failure=str(error))
            raise
        finally:
            runner.save('report.json', runner.report)
            os.close(fd)
            print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    def interrupted(*_): raise RuntimeError('interrupted; reconcile saved renewal/revocation evidence before retry')
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try: main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
