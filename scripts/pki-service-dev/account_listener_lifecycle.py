#!/usr/bin/env python3
"""Qualify the dev Account Manager listener renewal and retirement lifecycle."""
import argparse
import base64
import datetime as dt
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import signal
import time
import uuid


spec = importlib.util.spec_from_file_location('account_listener', Path(__file__).with_name('account_listener.py'))
a = importlib.util.module_from_spec(spec)
spec.loader.exec_module(a)
m, NS, AM_NS = a.m, a.NS, a.AM_NS
CLIENT_STATE = a.mg.STATE
HOST_STATE = a.STATE
SUBJECT = 'service:account-manager'


def current_service_issuer(items):
    candidates = [item for item in items
                  if item['environment'] == 'dev' and item['trust_domain'] == 'service'
                  and item['kind'] == 'intermediate' and item['status'] == 'active']
    m.require(len(candidates) == 1, 'one active Service intermediate is required')
    issuer = candidates[0]
    m.require(SUBJECT in issuer['service_client_ids'] and a.ACCOUNT_DNS in issuer['server_dns_names'],
              'active Service issuer does not authorize Account Manager')
    return issuer


def consumer_crl_entries(entries, path):
    result = []
    for entry in entries:
        issuer = entry.get('issuer', {})
        issuer_id = issuer.get('issuer_id', '')
        m.require(issuer.get('environment') == 'dev' and issuer.get('trust_domain') == 'service'
                  and issuer.get('status') in ('active', 'retiring')
                  and m.re.fullmatch('[0-9a-f-]{36}', issuer_id),
                  'canonical Service CRL manifest contains an invalid issuer')
        result.append({'issuer': issuer, 'state_path': path.format(issuer_id=issuer_id)})
    m.require(result, 'canonical Service CRL manifest is empty')
    return result


def replacement(before, after, rows, issuer, kind):
    m.require(after['subject'] == before['subject'] and after['root_sha256'] == before['root_sha256']
              and not after['pending'] and before['fingerprint'] != after['fingerprint']
              and before['public_key_sha256'] != after['public_key_sha256'], kind + ' did not rotate cleanly')
    added = [row for row in rows if row['fingerprint'] == after['fingerprint']]
    m.require(len(added) == 1 and added[0]['issuer_id'] == issuer['issuer_id']
              and added[0]['status'] == 'succeeded' and added[0]['revoked_at'] is None,
              kind + ' successor registry row differs')
    return added[0]


class AccountListenerLifecycle(a.ListenerRun):
    def __init__(self, args):
        super().__init__(args)
        self.remote_probe = '/tmp/pki-account-listener-lifecycle-' + uuid.uuid4().hex
        self.probe_installed = False
        self.report['phase'] = 'lifecycle'
        self.report['foundation_scope'] = 'Dev Account Manager managed client/listener renewal, held-session retirement and successor survival'
        self.report['account_listener_lifecycle_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def close(self):
        try:
            if self.probe_installed:
                self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--', 'rm', '-f', self.remote_probe])
        finally:
            super().close()

    def inspect(self, path):
        raw = self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--',
                         self.remote_probe, 'service-state', path])
        value = json.loads(raw)
        m.require(value['root_sha256'] == self.root['certificate_fingerprint_sha256'] and not value['pending'],
                  'managed Account Manager state is not current')
        return value

    def client_rows(self):
        raw = self.sql("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,subject,caller,status,fingerprint,issued_at,revoked_at "
                       "FROM pki_service_client_issuances WHERE environment='dev' AND subject='service:account-manager' "
                       "ORDER BY issued_at,request_id) t;")
        return [json.loads(line) for line in raw.splitlines() if line]

    def server_rows(self):
        raw = self.sql("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,caller,status,fingerprint,dns_names,issued_at,revoked_at "
                       "FROM pki_server_issuances WHERE environment='dev' AND domain='service' "
                       "ORDER BY issued_at,request_id) t;")
        return [row for row in (json.loads(line) for line in raw.splitlines() if line)
                if row['dns_names'] == [a.ACCOUNT_DNS]]

    def v3(self):
        # The listener was introduced with Service v3, but it must continue
        # qualifying against the sole active Service issuer after later
        # hierarchy rotations.  The activation record remains an audited
        # lineage prerequisite; live policy is always selected from the
        # registry rather than its historical version number.
        source = Path(self.args.activation)
        m.require(m.read(source / 'report.json')['status'] == 'passed',
                  'successful Service activation evidence required')
        page = self.api('/issuers/search', {'limit': 100})
        issuer = current_service_issuer(page['items'])
        m.require(self.api('/issuers/' + issuer['issuer_id']) == issuer,
                  'active Service issuer changed during Account Manager lifecycle preflight')
        return issuer

    def install_probe(self):
        binary = self.output / 'pki-dev-probe-linux'
        result = m.subprocess.run(['go', 'build', '-trimpath', '-o', str(binary), './pki-dev-probe'],
            cwd=m.WORKSPACE / 'scripts/go', env=dict(os.environ, GOOS='linux', GOARCH='amd64', CGO_ENABLED='0', GOWORK='off'),
            capture_output=True, timeout=180)
        m.require(result.returncode == 0, 'session probe build failed')
        self.kube(['-n', AM_NS, 'exec', '-i', 'deployment/account-manager', '-c', 'pkimanagement', '--', 'sh', '-c',
                   'umask 077; base64 -d > ' + self.remote_probe + ' && chmod 700 ' + self.remote_probe],
                  base64.b64encode(binary.read_bytes()).decode())
        actual = self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--',
                            'sha256sum', self.remote_probe]).split()[0]
        m.require(actual == m.digest(binary.read_bytes()), 'installed session probe digest differs')
        self.probe_installed = True
        self.save('probe-binary.json', {'sha256': actual})

    def session(self, identity):
        process = m.Process(self.k + ['-n', AM_NS, 'exec', '-i', 'deployment/account-manager', '-c', 'pkimanagement', '--',
            self.remote_probe, 'service-session', CLIENT_STATE, '/run/pki-root/root.pem',
            'certissuer.' + NS + '.svc', '9443', '/healthz', identity['fingerprint']], keep_stdin=True)
        self.children.append(process)
        event = process.event()
        m.require(event['event'] == 'ready' and event['fingerprint'] == identity['fingerprint'],
                  'held Account Manager client session differs')
        return process

    def session_command(self, process, command, expected):
        process.child.stdin.write(command + '\n')
        process.child.stdin.flush()
        event = process.event(timeout=35)
        m.require(event['event'] == expected, 'held Account Manager session event differs')
        return event

    def wait_changed(self, path, before):
        deadline = time.monotonic() + 90
        while True:
            current = self.inspect(path)
            if current['fingerprint'] != before['fingerprint']:
                return current
            m.require(time.monotonic() < deadline, 'Account Manager renewal did not complete; do not signal again')
            time.sleep(2)

    def current_successors(self, client, host):
        for identity, rows, kind in ((client, self.client_rows(), 'client'), (host, self.server_rows(), 'listener')):
            selected = [row for row in rows if row['fingerprint'] == identity['fingerprint']]
            m.require(len(selected) == 1 and selected[0]['issuer_id'] == self.issuer['issuer_id']
                      and selected[0]['status'] == 'succeeded' and selected[0]['revoked_at'] is None,
                      'current Account Manager ' + kind + ' is not admitted')
        peer = json.loads(m.command([self.probe, 'tls-peer', self.output / 'root.pem', a.ACCOUNT_DNS, self.ports['listener'][0]]))
        m.require(peer['peer_sha256'] == host['fingerprint'], 'Account Manager listener does not serve its successor')

    def sync_service_crl_consumers(self, issuer_id):
        source = self.obj('configmap', 'pki-service-client-crls')
        entries = json.loads(source['data']['crls.json'])
        m.require(any(entry['issuer']['issuer_id'] == issuer_id for entry in entries),
                  'retired issuer is absent from canonical Service CRL manifest')
        targets = (
            ('factoryenroll-service-crls', 'crls.json', '/state/identity/crl-{issuer_id}.json'),
            ('video-cloud-api-pki-trust', 'service-crls.json', '/run/pki-state/service-{issuer_id}-crl.json'),
        )
        changed = False
        for name, key, state_path in targets:
            before = self.obj('configmap', name)
            m.require(key in before.get('data', {}), 'Service CRL consumer manifest key is missing: ' + name)
            desired = json.dumps(consumer_crl_entries(entries, state_path))
            if before['data'][key] != desired:
                self.scoped_patch('configmap', before, [
                    {'op': 'test', 'path': '/data/' + key, 'value': before['data'][key]},
                    {'op': 'replace', 'path': '/data/' + key, 'value': desired},
                ])
                changed = True
        if changed:
            for name in ('factoryenroll', 'video-cloud-api-pki'):
                before = self.obj('deployment', name)
                m.require(before['spec']['replicas'] == 1 and before.get('status', {}).get('readyReplicas') == 1,
                          'Service CRL consumer is not ready: ' + name)
                template = json.loads(json.dumps(before['spec']['template']))
                template.setdefault('metadata', {}).setdefault('annotations')[
                    'rtk.cloud/pki-dev-acceptance'] = self.output.name
                self.scoped_patch('deployment', before, [
                    {'op': 'test', 'path': '/spec/template', 'value': before['spec']['template']},
                    {'op': 'replace', 'path': '/spec/template', 'value': template},
                ])
                self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + name, '--timeout=240s'], timeout=250)
        self.save('service-crl-consumer-sync.json', {
            'issuer_id': issuer_id, 'consumer_manifests_changed': changed,
            'consumers': ['factory-enroll', 'video-cloud-api'],
        })

    def recover_interrupted_lifecycle(self):
        source = Path(self.args.recover)
        failed = m.read(source / 'report.json')
        m.require(failed['status'] == 'failed' and failed['phase'] == 'lifecycle'
                  and (('revoke-' in failed.get('failure', '') and 'status 403' in failed['failure'])
                       or failed.get('failure') == 'consumer receipt deadline'),
                  'recovery requires the recorded lifecycle interruption')
        baseline, renewed = m.read(source / 'baseline.json'), m.read(source / 'renewed.json')
        self.preflight_lifecycle()
        self.root = self.active_root()
        self.save('root.pem', self.root['certificate_pem'])
        self.issuer = self.v3()
        self.forward('listener', AM_NS, a.SERVICE_NAME, 8443)
        self.install_probe()
        client, host = self.inspect(CLIENT_STATE), self.inspect(HOST_STATE)
        m.require(client == renewed['client'] and host == renewed['listener'],
                  'Account Manager state changed after the interrupted lifecycle')
        for rows, before, current, kind in ((self.client_rows(), baseline['client'], client, 'client'),
                                            (self.server_rows(), baseline['listener'], host, 'listener')):
            retired = [row for row in rows if row['fingerprint'] == before['fingerprint']]
            admitted = [row for row in rows if row['fingerprint'] == current['fingerprint'] and row['revoked_at'] is None]
            m.require(len(retired) == 1 and len(admitted) == 1,
                      'recorded interrupted Account Manager ' + kind + ' pair no longer matches live registry state')
        self.revocation_started = dt.datetime.now(dt.timezone.utc)
        retired_clients = self.revoke_and_publish('client', self.client_rows(), client, baseline['client'])
        retired_hosts = self.revoke_and_publish('server', self.server_rows(), host, baseline['listener'])
        self.current_successors(client, host)
        self.restart_account_manager()
        # The probe is deliberately ephemeral in the sidecar's filesystem and
        # is removed by the Recreate restart. Reinstall it before inspecting
        # the persisted successor state.
        self.install_probe()
        self.forward('listener', AM_NS, a.SERVICE_NAME, 8443)
        m.require(self.inspect(CLIENT_STATE) == client and self.inspect(HOST_STATE) == host,
                  'Account Manager recovery restart changed successor state')
        self.current_successors(client, host)
        self.check('interrupted_account_manager_lifecycle_reconciled', {
            'source': source.name, 'client_retired': len(retired_clients), 'listener_retired': len(retired_hosts),
            'successor_survived': True, 'bootstrap_free_restart': True, 'private_keys_exported': False})

    def revoke_and_publish(self, kind, rows, current, predecessor, held=None):
        action = 'service-client' if kind == 'client' else 'server'
        targets = [row for row in rows if row['fingerprint'] == predecessor['fingerprint']]
        m.require(targets, 'no replaced Account Manager ' + kind + ' leaves to retire')
        # A renewal can move the Account Manager identity to the current Service
        # intermediate.  Its predecessor must still be revoked through the
        # intermediate that issued it; sending an old fingerprint to the new
        # issuer is correctly denied by the controller.
        issuer_ids = {row['issuer_id'] for row in targets}
        m.require(len(issuer_ids) == 1, 'replaced Account Manager ' + kind + ' leaves span multiple issuers')
        issuer_id = issuer_ids.pop()
        previous = self.api('/issuers/' + issuer_id + '/crl')
        self.save(kind + '-previous-crl.json', previous)
        for index, row in enumerate(targets):
            body = {'certificate_sha256': row['fingerprint'], 'reason': 'Replaced Account Manager ' + kind + ' leaf'}
            self.save(kind + '-revocation-' + str(index) + '-intent.json', body)
            if row['revoked_at'] is None:
                self.api('/issuers/' + issuer_id + '/revoke-' + action, body, role='approver')
        if held is not None:
            closed = self.session_command(held, 'closed', 'closed')
            delay = (m.parse_time(closed['at']) - self.revocation_started).total_seconds()
            m.require(0 <= delay <= 30, 'replaced Account Manager client cutoff exceeded 30 seconds')
            denied = self.session_command(held, 'denied', 'denied')
            self.save('held-client-cutoff.json', {'cutoff_seconds_upper_bound': delay, 'fresh_old_denied': denied})
        final = previous
        for row in targets:
            final = self.api('/issuers/' + issuer_id + '/publish-' + action + '-revocation',
                             {'certificate_sha256': row['fingerprint']}, role='approver')
            self.sync_service_crl_consumers(issuer_id)
            self.wait_receipts(issuer_id, final['crl_sha256'],
                               ['certissuer', 'factory-enroll', 'pki-controller', 'video-cloud-api'], 'crl')
        for row in targets:
            result = self.api('/issuers/' + issuer_id + '/finalize-' + action + '-revocation',
                              {'certificate_sha256': row['fingerprint']}, role='approver')
            m.require(result['crl_sha256'] == final['crl_sha256'], 'Account Manager retirement finalization differs')
        self.save(kind + '-retirement.json', {'issuer_id': issuer_id,
                                              'targets': [row['fingerprint'] for row in targets],
                                              'crl_sha256': final['crl_sha256']})
        return targets

    def lifecycle(self):
        self.preflight_lifecycle()
        self.root = self.active_root()
        self.save('root.pem', self.root['certificate_pem'])
        self.issuer = self.v3()
        self.forward('listener', AM_NS, a.SERVICE_NAME, 8443)
        self.install_probe()
        client_before, host_before = self.inspect(CLIENT_STATE), self.inspect(HOST_STATE)
        client_rows_before, host_rows_before = self.client_rows(), self.server_rows()
        self.save('baseline.json', {'client': client_before, 'listener': host_before})
        held = self.session(client_before)
        m.require(self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--', 'cat', '/proc/1/comm']).strip() == 'pkimanagement',
                  'PID 1 is not the Account Manager identity owner')
        self.save('renewal-intent.json', {'at': m.stamp(), 'client': client_before['fingerprint'], 'listener': host_before['fingerprint']})
        self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--', 'sh', '-c',
                   'test "$(cat /proc/1/comm)" = pkimanagement && kill -HUP 1'])
        client_after, host_after = self.wait_changed(CLIENT_STATE, client_before), self.wait_changed(HOST_STATE, host_before)
        client_rows_after, host_rows_after = self.client_rows(), self.server_rows()
        client_receipt = replacement(client_before, client_after, client_rows_after, self.issuer, 'client')
        host_receipt = replacement(host_before, host_after, host_rows_after, self.issuer, 'listener')
        self.save('renewed.json', {'client': client_after, 'listener': host_after,
                                   'client_request_id': client_receipt['request_id'], 'listener_request_id': host_receipt['request_id']})
        successor = self.session(client_after)
        self.session_command(held, 'check', 'alive')
        self.session_command(successor, 'check', 'alive')
        self.current_successors(client_after, host_after)
        self.revocation_started = dt.datetime.now(dt.timezone.utc)
        retired_clients = self.revoke_and_publish('client', client_rows_after, client_after, client_before, held)
        self.session_command(successor, 'check', 'alive')
        retired_hosts = self.revoke_and_publish('server', host_rows_after, host_after, host_before)
        self.current_successors(client_after, host_after)
        self.session_command(successor, 'check', 'alive')
        for process in (held, successor):
            if not process.child.stdin.closed:
                process.child.stdin.close()
            m.require(process.child.wait(timeout=10) == 0, 'session probe did not stop')
        self.restart_account_manager()
        self.install_probe()
        self.forward('listener', AM_NS, a.SERVICE_NAME, 8443)
        m.require(self.inspect(CLIENT_STATE) == client_after and self.inspect(HOST_STATE) == host_after,
                  'Account Manager restart changed successor state')
        self.current_successors(client_after, host_after)
        self.check('account_manager_listener_lifecycle', {'client_retired': len(retired_clients),
                   'listener_retired': len(retired_hosts), 'held_old_client_cutoff': True,
                   'successor_survived': True, 'bootstrap_free_restart': True, 'private_keys_exported': False})

    def restart_account_manager(self):
        before = self.obj('deployment', 'account-manager', AM_NS)
        m.require(before['spec']['strategy']['type'] == 'Recreate' and before['spec']['replicas'] == 1,
                  'expected one Account Manager Recreate owner')
        template = json.loads(json.dumps(before['spec']['template']))
        annotations = template.setdefault('metadata', {}).setdefault('annotations', {})
        annotations['rtk.cloud/pki-dev-acceptance'] = self.output.name
        self.scoped_patch('deployment', before, [{'op': 'test', 'path': '/spec/template', 'value': before['spec']['template']},
                                                  {'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', AM_NS, 'rollout', 'status', 'deployment/account-manager', '--timeout=240s'], timeout=250)

    def preflight_lifecycle(self):
        m.require(self.kube(['config', 'current-context']).strip() == self.context, 'canonical dev context mismatch')
        for name, ns in [('account-manager', AM_NS), ('certissuer', NS), ('pki-controller', NS)]:
            deployment = self.obj('deployment', name, ns)
            m.require(deployment['spec']['replicas'] == 1 and deployment.get('status', {}).get('readyReplicas') == 1
                      and deployment['status'].get('observedGeneration') == deployment['metadata']['generation'],
                      'deployment is not ready: ' + name)
        account = self.obj('deployment', 'account-manager', AM_NS)
        sidecar = next(c for c in account['spec']['template']['spec']['containers'] if c['name'] == 'pkimanagement')
        env = {e['name']: e.get('value') for e in sidecar['env']}
        m.require(env.get('PKI_MANAGEMENT_IDENTITY_STATE') == CLIENT_STATE
                  and env.get('PKI_MANAGEMENT_ACCOUNT_HOST_IDENTITY_STATE') == HOST_STATE
                  and env.get('PKI_MANAGEMENT_ACCOUNT_LISTEN_ADDR') == ':8443',
                  'Account Manager listener runtime differs')
        workspace = Path(__file__).resolve().parents[2]
        result = m.subprocess.run(['go', 'build', '-o', str(self.probe), './pki-dev-probe'], cwd=workspace / 'scripts/go',
                                  env=dict(os.environ, GOWORK='off'), capture_output=True, timeout=180)
        m.require(result.returncode == 0, 'listener probe build failed')
        self.forward('am', AM_NS, 'account-manager', 80)
        self.forward('issuer', NS, 'certissuer', 9443)
        self.forward('api', NS, 'video-cloud-api-pki', 8443)
        self.forward('mqtt', NS, 'mqtt-pki', 8883)
        self.accounts = m.read(self.foundation / 'accounts.json')
        self.check('preflight', {'context': self.context, 'account_listener_ready': True, 'staging_touched': False})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=str(Path.home() / '.config/rtk_cloud'))
    parser.add_argument('--authority', required=True)
    parser.add_argument('--activation', required=True)
    parser.add_argument('--image', required=True)
    parser.add_argument('--output', required=True)
    parser.add_argument('--recover', type=Path,
                        help='recover only the exact predecessor pair recorded by an unauthorized lifecycle interruption')
    args = parser.parse_args()
    args.resume = args.resume_after_deploy = args.resume_after_listener = False
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = AccountListenerLifecycle(args)
    try:
        if args.recover:
            runner.recover_interrupted_lifecycle()
        else:
            runner.lifecycle()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'; runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close(); os.close(fd)
        print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    def interrupted(*_):
        raise RuntimeError('interrupted; reconcile saved Account Manager renewal/revocation evidence before retry')
    signal.signal(signal.SIGTERM, interrupted); signal.signal(signal.SIGINT, interrupted)
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=os.sys.stderr); raise SystemExit(1)
