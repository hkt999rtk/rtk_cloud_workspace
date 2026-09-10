#!/usr/bin/env python3
"""Reconcile and qualify the isolated Device API controller identity in dev."""
import argparse
import base64
import datetime as dt
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import signal
import sys
import time
import uuid


spec = importlib.util.spec_from_file_location('managed_hosts', Path(__file__).with_name('hosts.py'))
h = importlib.util.module_from_spec(spec)
spec.loader.exec_module(h)
m, NS = h.m, h.NS

DEPLOYMENT = 'video-cloud-api-pki'
SUBJECT = 'service:video-cloud-api'
STATE = '/var/lib/video-cloud-api-pki-controller-identity/private/identity.json'
ROOT_CA = '/run/pki-service-root/root.pem'
CONTROLLER = 'pki-controller.' + NS + '.svc'
# The isolated API is a Service *client*: the controller enforces revocation
# during each admission.  It does not install a Service-issuer CRL, so only
# the three actual Service CRL consumers may acknowledge this publication.
RECEIPTS = ['certissuer', 'factory-enroll', 'pki-controller']


def current_and_stale(rows, state):
    """Return the one installed issuance and uninstalled active predecessors."""
    active = [row for row in rows if row['status'] == 'succeeded' and row['revoked_at'] is None]
    current = [row for row in active if row['fingerprint'] == state['fingerprint']]
    m.require(len(current) == 1 and state['subject'] == SUBJECT and not state['pending'],
              'installed API identity lacks exactly one active registry admission')
    m.require(all(row['subject'] == SUBJECT and row['issuer_id'] == current[0]['issuer_id'] for row in active),
              'API identity registry crosses subject or issuer')
    return current[0], [row for row in active if row != current[0]]


def replacement(before, after, rows_before, rows_after, issuer):
    m.require(after['subject'] == before['subject'] == SUBJECT and not after['pending']
              and after['root_sha256'] == before['root_sha256']
              and after['fingerprint'] != before['fingerprint']
              and after['public_key_sha256'] != before['public_key_sha256']
              and after['state_sha256'] != before['state_sha256'],
              'API controller identity did not rotate key and leaf cleanly')
    added = [row for row in rows_after if row not in rows_before]
    m.require(len(added) == 1 and len(rows_after) == len(rows_before) + 1
              and all(row in rows_after for row in rows_before),
              'unexpected API controller issuance changes; reconcile')
    row = added[0]
    m.require(row['fingerprint'] == after['fingerprint'] and row['issuer_id'] == issuer['issuer_id']
              and row['subject'] == row['caller'] == SUBJECT and row['status'] == 'succeeded'
              and row['revoked_at'] is None, 'API controller successor registry receipt differs')
    return row


class APILifecycle(h.HostRun):
    def __init__(self, args):
        super().__init__(args)
        self.remote_probe = '/tmp/pki-api-controller-lifecycle-' + uuid.uuid4().hex
        self.probe_pod = None
        self.report['phase'] = args.phase
        self.report['foundation_scope'] = ('Dev isolated Device API controller identity reconciliation, '
                                           'renewal, retirement and Device/MQTT verification')
        self.report['api_controller_lifecycle_runner_sha256'] = m.digest(Path(__file__).read_bytes())

    def close(self):
        try:
            if self.probe_pod:
                name, uid = self.probe_pod
                if self.obj('pod', name)['metadata']['uid'] == uid:
                    self.kube(['-n', NS, 'exec', name, '-c', 'app', '--', 'rm', '-f', self.remote_probe])
        except RuntimeError:
            pass
        finally:
            super().close()

    def pod(self):
        owner = self.obj('deployment', DEPLOYMENT)
        m.require(owner['spec']['replicas'] == 1 and owner['status'].get('readyReplicas') == 1
                  and owner['status'].get('observedGeneration') == owner['metadata']['generation'],
                  'isolated API deployment is not ready')
        template = owner['spec']['template']['spec']
        m.require([container['name'] for container in template['containers']] == ['app'],
                  'isolated API container topology changed')
        selector = ','.join(k + '=' + v for k, v in owner['spec']['selector']['matchLabels'].items())
        pods = json.loads(self.kube(['-n', NS, 'get', 'pods', '-l', selector, '-o', 'json']))['items']
        pods = [pod for pod in pods if not pod['metadata'].get('deletionTimestamp')]
        m.require(len(pods) == 1, 'isolated API pod count changed')
        return owner, pods[0]

    def install_probe(self):
        owner, pod = self.pod()
        name, uid = pod['metadata']['name'], pod['metadata']['uid']
        if self.probe_pod == (name, uid):
            return
        binary = self.output / 'pki-dev-probe-linux'
        result = m.subprocess.run(['go', 'build', '-trimpath', '-o', str(binary), './pki-dev-probe'],
                                  cwd=m.WORKSPACE / 'scripts/go',
                                  env=dict(os.environ, GOOS='linux', GOARCH='amd64', CGO_ENABLED='0', GOWORK='off'),
                                  capture_output=True, timeout=180)
        m.require(result.returncode == 0, 'API lifecycle probe build failed')
        self.kube(['-n', NS, 'exec', '-i', name, '-c', 'app', '--', 'sh', '-c',
                   'umask 077; base64 -d > ' + self.remote_probe + ' && chmod 700 ' + self.remote_probe],
                  base64.b64encode(binary.read_bytes()).decode())
        actual = self.kube(['-n', NS, 'exec', name, '-c', 'app', '--', 'sha256sum', self.remote_probe]).split()[0]
        m.require(actual == m.digest(binary.read_bytes()), 'installed API lifecycle probe differs')
        self.probe_pod = (name, uid)
        self.save('probe-binary.json', {'sha256': actual})

    def inspect(self, root):
        self.install_probe()
        name, _ = self.probe_pod
        state = json.loads(self.kube(['-n', NS, 'exec', name, '-c', 'app', '--', self.remote_probe,
                                      'service-state', STATE]))
        m.require(state['subject'] == SUBJECT and state['root_sha256'] == root['certificate_fingerprint_sha256'],
                  'isolated API public identity differs from the active Service Root')
        return state

    def rows(self):
        raw = self.sql("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,subject,caller,status,"
                       "fingerprint,issued_at,revoked_at FROM pki_service_client_issuances "
                       "WHERE environment='dev' AND subject='" + SUBJECT + "' ORDER BY issued_at,request_id) t;")
        return [json.loads(line) for line in raw.splitlines() if line]

    def sql(self, query):
        return self.kube(['-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--',
                          'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres',
                          '-d', 'video_cloud', '-At'], query).strip()

    def active_issuer(self, root, row):
        issuer = self.api('/issuers/' + row['issuer_id'])
        m.require(issuer['status'] == 'active' and issuer['trust_domain'] == 'service'
                  and issuer['parent_issuer_id'] == root['issuer_id'],
                  'API controller issuer no longer belongs to active Service Root')
        return issuer

    def session(self, issuer, identity):
        self.install_probe()
        name, _ = self.probe_pod
        process = m.Process(self.k + ['-n', NS, 'exec', '-i', name, '-c', 'app', '--', self.remote_probe,
                                      'service-session', STATE, ROOT_CA, CONTROLLER, '18446',
                                      '/v1/pki/issuers/' + issuer['issuer_id'] + '/crl', identity['fingerprint']],
                            keep_stdin=True)
        self.children.append(process)
        event = process.event()
        m.require(event['event'] == 'ready' and event['fingerprint'] == identity['fingerprint'],
                  'API controller held session identity differs')
        return process

    def session_command(self, process, command, expected):
        process.child.stdin.write(command + '\n')
        process.child.stdin.flush()
        event = process.event(timeout=35)
        m.require(event['event'] == expected, 'API controller held-session result differs')
        return event

    def check_runtime(self, root):
        owner, _ = self.pod()
        app = owner['spec']['template']['spec']['containers'][0]
        env = {entry['name']: entry.get('value', '') for entry in app.get('env', [])}
        required = ('VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE', 'VIDEO_CLOUD_CONTROLLER_IDENTITY_ROOT_SHA256',
                    'VIDEO_CLOUD_CONTROLLER_IDENTITY_SERVER_PKI_NAME', 'VIDEO_CLOUD_CONTROLLER_IDENTITY_TLS_CA',
                    'VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_URL',
                    'VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_SERVER_PKI_NAME',
                    'VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_TLS_CA')
        m.require(all(env.get(key) for key in required) and env['VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE'] == STATE
                  and env['VIDEO_CLOUD_CONTROLLER_IDENTITY_ROOT_SHA256'] == root['certificate_fingerprint_sha256'],
                  'isolated API managed controller configuration changed')
        forbidden = ('VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_MANAGEMENT_CERT',
                     'VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_MANAGEMENT_KEY')
        m.require(not any(key in env for key in forbidden)
                  and not any(mount['mountPath'] == '/run/pki-management' for mount in app.get('volumeMounts', [])),
                  'isolated API static controller credential remains')

    def retire(self, issuer, row, label):
        previous = self.api('/issuers/' + issuer['issuer_id'] + '/crl')
        body = {'certificate_sha256': row['fingerprint'], 'reason': label + ' ' + row['request_id']}
        self.save('revocation-' + row['fingerprint'] + '-intent.json', body)
        record = self.api('/issuers/' + issuer['issuer_id'] + '/revoke-service-client', body)
        m.require(self.api('/issuers/' + issuer['issuer_id'] + '/revoke-service-client', body) == record,
                  'API controller revocation replay changed record')
        published = self.api('/issuers/' + issuer['issuer_id'] + '/publish-service-client-revocation',
                             {'certificate_sha256': row['fingerprint']})
        m.require(int(published['crl_number']) > int(previous['crl_number']), 'API controller CRL did not advance')
        self.wait_receipts(issuer['issuer_id'], published['crl_sha256'], RECEIPTS, 'crl')
        final = self.api('/issuers/' + issuer['issuer_id'] + '/finalize-service-client-revocation',
                         {'certificate_sha256': row['fingerprint']})
        m.require(final['crl_sha256'] == published['crl_sha256'], 'API controller retirement finalized against another CRL')
        return {'fingerprint': row['fingerprint'], 'request_id': row['request_id'],
                'crl_sha256': published['crl_sha256'], 'crl_number': published['crl_number']}

    def reconcile(self):
        root = self.active_root()
        self.check_runtime(root)
        state, rows = self.inspect(root), self.rows()
        current, stale = current_and_stale(rows, state)
        m.require(stale, 'no uninstalled active API controller identities to reconcile')
        issuer = self.active_issuer(root, current)
        self.save('baseline.json', {'identity': state, 'rows': rows, 'current': current, 'stale': stale})
        retired = [self.retire(issuer, row, 'Uninstalled API controller predecessor') for row in stale]
        final_rows = self.rows()
        selected, remaining = current_and_stale(final_rows, self.inspect(root))
        m.require(selected == current and not remaining, 'API controller stale registry reconciliation incomplete')
        self.save('reconciled.json', {'current': current, 'retired': retired, 'rows': final_rows})
        self.device_baseline()
        self.check('api_controller_registry_reconciled', {'retired_uninstalled_predecessors': len(retired),
                   'one_installed_active_identity': True, 'private_keys_exported': False,
                   'device_mtls_and_mqtt': 'passed'})

    def recover_reconcile(self):
        source = Path(self.args.reconcile)
        previous = m.read(source / 'report.json')
        m.require(previous['status'] == 'failed' and previous.get('phase') == 'reconcile',
                  'failed API controller reconciliation evidence required')
        baseline = m.read(source / 'baseline.json')
        root = self.active_root()
        self.check_runtime(root)
        state, rows = self.inspect(root), self.rows()
        current, stale = current_and_stale(rows, state)
        m.require(current == baseline['current'] and not stale, 'API controller registry changed during reconciliation recovery')
        targets = baseline['stale']
        selected = {row['fingerprint']: row for row in rows}
        m.require(all(fingerprint in selected and selected[fingerprint]['revoked_at'] is not None
                      for fingerprint in (row['fingerprint'] for row in targets)),
                  'saved stale API controller predecessor was not revoked')
        issuer = self.active_issuer(root, current)
        published = self.api('/issuers/' + issuer['issuer_id'] + '/crl')
        self.wait_receipts(issuer['issuer_id'], published['crl_sha256'], RECEIPTS, 'crl')
        finalized = []
        for target in targets:
            record = self.api('/issuers/' + issuer['issuer_id'] + '/finalize-service-client-revocation',
                              {'certificate_sha256': target['fingerprint']})
            m.require(record['crl_sha256'] == published['crl_sha256'],
                      'API controller recovery finalized against another CRL')
            finalized.append(target['fingerprint'])
        self.save('reconciled.json', {'current': current, 'retired': finalized, 'rows': rows,
                                      'crl_sha256': published['crl_sha256'], 'recovered_from': str(source)})
        self.device_baseline()
        self.check('api_controller_registry_reconciliation_recovered',
                   {'retired_uninstalled_predecessors': len(finalized), 'service_crl_consumers': RECEIPTS,
                    'one_installed_active_identity': True, 'private_keys_exported': False,
                    'device_mtls_and_mqtt': 'passed'})

    def lifecycle(self):
        source = Path(self.args.reconcile)
        m.require(m.read(source / 'report.json')['status'] == 'passed', 'successful API registry reconciliation required')
        root = self.active_root()
        self.check_runtime(root)
        before, rows_before = self.inspect(root), self.rows()
        current, stale = current_and_stale(rows_before, before)
        m.require(current == m.read(source / 'reconciled.json')['current'] and not stale,
                  'API controller identity changed since reconciliation')
        issuer = self.active_issuer(root, current)
        held = self.session(issuer, before)
        owner, pod = self.pod()
        m.require(self.kube(['-n', NS, 'exec', pod['metadata']['name'], '-c', 'app', '--', 'cat', '/proc/1/comm']).strip() == 'api',
                  'PID 1 is not the API identity owner')
        self.save('renewal-intent.json', {'previous': before, 'pod_uid': pod['metadata']['uid'], 'at': m.stamp()})
        self.kube(['-n', NS, 'exec', pod['metadata']['name'], '-c', 'app', '--', 'sh', '-c', 'kill -HUP 1'])
        deadline = time.monotonic() + 150
        while True:
            after = self.inspect(root)
            if not after['pending'] and after['fingerprint'] != before['fingerprint']:
                break
            m.require(time.monotonic() < deadline, 'API controller renewal incomplete; do not signal again')
            time.sleep(2)
        rows_after = self.rows()
        successor = replacement(before, after, rows_before, rows_after, issuer)
        self.save('renewed.json', {'identity': after, 'rows': rows_after, 'request_id': successor['request_id']})
        new = self.session(issuer, after)
        self.session_command(held, 'check', 'alive')
        self.session_command(new, 'check', 'alive')
        started = dt.datetime.now(dt.timezone.utc)
        retired = self.retire(issuer, current, 'Replaced API controller identity')
        closed = self.session_command(held, 'closed', 'closed')
        delay = (m.parse_time(closed['at']) - started).total_seconds()
        m.require(0 <= delay <= 30, 'API controller old-session cutoff exceeded 30 seconds')
        denied = self.session_command(held, 'denied', 'denied')
        alive = self.session_command(new, 'check', 'alive')
        self.save('held-session-cutoff.json', {'closed': closed, 'cutoff_seconds_upper_bound': delay,
                                                'fresh_old_denied': denied, 'successor_alive': alive})
        for process in (held, new):
            process.child.stdin.close()
            m.require(process.child.wait(timeout=10) == 0, 'API controller session probe did not stop')
        self.restart(DEPLOYMENT)
        self.probe_pod = None
        restarted = self.inspect(root)
        restarted_current, restarted_stale = current_and_stale(self.rows(), restarted)
        m.require(restarted == after and restarted_current['fingerprint'] == successor['fingerprint'] and not restarted_stale,
                  'API controller restart changed successor identity or admission')
        self.device_baseline()
        self.check('api_controller_lifecycle', {'renewal_request_id': successor['request_id'],
                   'exactly_one_new_issuance': True, 'retired_predecessor': retired['fingerprint'],
                   'held_old_session_cutoff_seconds': delay, 'old_fresh_connection_denied': True,
                   'successor_and_restart_survived': True, 'device_mtls_and_mqtt': 'passed',
                   'private_keys_exported': False})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', required=True, choices=['reconcile', 'recover-reconcile', 'lifecycle'])
    parser.add_argument('--authority', required=True)
    parser.add_argument('--reconcile')
    parser.add_argument('--output', required=True)
    parser.add_argument('--config-root', default=str(Path.home() / '.config/rtk_cloud'))
    args = parser.parse_args()
    m.require(args.phase == 'reconcile' or args.reconcile, 'reconciliation evidence required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = APILifecycle(args)
    try:
        runner.preflight()
        {'reconcile': runner.reconcile, 'recover-reconcile': runner.recover_reconcile,
         'lifecycle': runner.lifecycle}[args.phase]()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'
        runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        os.close(fd)
        print(json.dumps({'phase': args.phase, 'status': runner.report['status'],
                          'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    def interrupted(*_):
        raise RuntimeError('interrupted; reconcile the saved phase before any new renewal signal')
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
