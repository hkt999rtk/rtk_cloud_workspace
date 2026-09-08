#!/usr/bin/env python3
"""Dev managed Account Manager early renewal, without exporting private state."""
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

spec = importlib.util.spec_from_file_location('managed_account', Path(__file__).with_name('management.py'))
h = importlib.util.module_from_spec(spec)
spec.loader.exec_module(h)
m, NS, AM_NS = h.m, h.NS, h.AM_NS


def replacement(before, after, old_rows, rows):
    m.require(not after['pending'] and after['subject'] == before['subject'] == 'service:account-manager'
              and after['root_sha256'] == before['root_sha256'], 'installed replacement scope differs')
    m.require(after['fingerprint'] != before['fingerprint'] and after['public_key_sha256'] != before['public_key_sha256']
              and after['state_sha256'] != before['state_sha256'], 'replacement did not rotate identity/key')
    added = [row for row in rows if row not in old_rows]
    m.require(all(row in rows for row in old_rows) and len(rows) == len(old_rows) + 1 and len(added) == 1,
              'unexpected issuance changes; reconcile')
    current = added[0]
    m.require(current['fingerprint'] == after['fingerprint'] and current['status'] == 'succeeded'
              and current['revoked_at'] is None and current['caller'] == 'service:account-manager', 'replacement registry receipt differs')
    return current


class RenewalRun(h.ManagementRun):
    def __init__(self, args):
        super().__init__(args)
        self.probe_pods = {}
        self.remote_probe = '/tmp/pki-state-' + uuid.uuid4().hex
        self.report['renewal_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.report['probe_source_sha256'] = {p.name: m.digest(p.read_bytes()) for p in (m.WORKSPACE / 'scripts/go/pki-dev-probe').glob('*.go') if not p.name.endswith('_test.go')}
        self.report['foundation_scope'] = 'Dev operator-requested Account Manager renewal'

    def pod(self):
        owner = self.obj('deployment', 'account-manager', AM_NS)
        m.require(owner['spec']['replicas'] == 1 and owner['spec']['strategy']['type'] == 'Recreate', 'unexpected identity owners')
        selector = ','.join(k + '=' + v for k, v in owner['spec']['selector']['matchLabels'].items())
        pods = json.loads(self.kube(['-n', AM_NS, 'get', 'pods', '-l', selector, '-o', 'json']))['items']
        pods = [p for p in pods if not p['metadata'].get('deletionTimestamp')]
        m.require(len(pods) == 1, 'unexpected identity pod count')
        return pods[0]

    def inspect(self):
        pod = self.pod()
        name, uid = pod['metadata']['name'], pod['metadata']['uid']
        if uid not in self.probe_pods:
            binary = self.output / 'pki-dev-probe-linux'
            if not binary.exists():
                result = m.subprocess.run(['go', 'build', '-o', str(binary), './pki-dev-probe'], cwd=m.WORKSPACE / 'scripts/go',
                         env=dict(os.environ, GOOS='linux', GOARCH='amd64', CGO_ENABLED='0', GOWORK='off'), capture_output=True, timeout=180)
                m.require(result.returncode == 0, 'owner-local public inspection probe build failed')
            self.kube(['-n', AM_NS, 'exec', '-i', name, '-c', 'pkimanagement', '--', 'sh', '-c',
                       'umask 077; base64 -d > ' + self.remote_probe + ' && chmod 700 ' + self.remote_probe], base64.b64encode(binary.read_bytes()).decode())
            self.probe_pods[uid] = name
        result = json.loads(self.kube(['-n', AM_NS, 'exec', name, '-c', 'pkimanagement', '--', self.remote_probe, 'service-state', h.STATE]))
        m.require(result['root_sha256'] == m.read(Path(self.args.authority) / 'root-ready.json')['certificate_fingerprint_sha256']
                  and result['subject'] == 'service:account-manager', 'owner public identity differs')
        return result

    def close(self):
        for uid, name in self.probe_pods.items():
            try:
                if self.obj('pod', name, AM_NS)['metadata']['uid'] == uid:
                    self.kube(['-n', AM_NS, 'exec', name, '-c', 'pkimanagement', '--', 'rm', '-f', self.remote_probe])
            except RuntimeError:
                pass  # An already replaced pod has no remaining ephemeral probe.
        super().close()

    def rows(self):
        raw = self.sql("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,subject,caller,status,fingerprint,certificate_pem,issued_at,revoked_at "
                       "FROM pki_service_client_issuances WHERE environment='dev' AND subject='service:account-manager' ORDER BY issued_at,request_id) t;")
        return [json.loads(line) for line in raw.splitlines() if line]

    def issuance(self):
        info = self.inspect()
        rows = [r for r in self.rows() if r['fingerprint'] == info['fingerprint']]
        m.require(len(rows) == 1 and rows[0]['status'] == 'succeeded' and rows[0]['revoked_at'] is None and not info['pending'], 'installed identity lacks current registry admission')
        return rows[0]

    def baseline(self):
        source = Path(self.args.adoption)
        m.require(m.read(source / 'report.json')['status'] == 'passed', 'successful owner adoption required')
        return m.read(source / 'baseline.json'), m.read(source / 'image.json')['image']

    def runtime(self, baseline, image):
        owner = self.obj('deployment', 'account-manager', AM_NS)
        m.require(owner['metadata']['uid'] == baseline['deployment_uid']
                  and self.obj('persistentvolumeclaim', h.PVC, AM_NS)['metadata']['uid'] == baseline['pvc_uid'], 'owner/PVC replaced')
        desired = h.render_management(self.base)
        m.require(owner['spec']['template'] == desired['spec']['template'], 'persisted owner differs from runtime')
        containers = owner['spec']['template']['spec']['containers']
        m.require([c['name'] for c in containers] == ['app', 'pkimanagement'] and containers[1]['image'] == image
                  and not any(e['name'].startswith('PKI_MANAGEMENT_BOOTSTRAP_') for e in containers[1]['env']), 'owner image/bootstrap differs')
        expected = json.loads(json.dumps(baseline['images']))
        expected['account-manager']['pkimanagement'] = image
        m.require(self.report['checks']['preflight']['evidence']['images'] == expected, 'unrelated workload image changed')
        m.require(self.worker_images() == baseline['workers'], 'worker images changed')

    def adopt(self):
        self.hierarchy()
        m.require(re.fullmatch(r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}', self.args.image), 'pinned dev owner image required')
        self.verify_identity()
        info, rows = self.inspect(), self.rows()
        m.require(len(rows) == 1 and not info['pending'], 'expected initial single identity; reconcile')
        owner = self.obj('deployment', 'account-manager', AM_NS)
        m.require(owner['spec']['template'] == h.render_management(self.base)['spec']['template'], 'saved owner differs before upgrade')
        baseline = {'identity': info, 'rows': rows, 'workers': self.worker_images(),
                    'images': self.report['checks']['preflight']['evidence']['images'], 'deployment_uid': owner['metadata']['uid'],
                    'pvc_uid': self.obj('persistentvolumeclaim', h.PVC, AM_NS)['metadata']['uid']}
        self.save('baseline.json', baseline)
        self.save('image.json', {'image': self.args.image})
        containers = owner['spec']['template']['spec']['containers']
        m.require([c['name'] for c in containers] == ['app', 'pkimanagement'] and containers[1]['command'] == ['/app/pkimanagement'], 'owner container changed')
        self.scoped_patch('deployment', owner, [{'op': 'replace', 'path': '/spec/template/spec/containers/1/image', 'value': self.args.image}])
        m.write(self.base / 'operator/env/PKI_ACCOUNT_MANAGER_OWNER_IMAGE', self.args.image + '\n')
        self.kube(['-n', AM_NS, 'rollout', 'status', 'deployment/account-manager', '--timeout=240s'], timeout=250)
        self.forward('am', AM_NS, 'account-manager', 80)
        m.require(self.inspect() == info and self.rows() == rows, 'upgrade changed credential before signal')
        self.verify_identity()
        m.require(self.worker_images() == baseline['workers'], 'worker images changed')
        self.check('owner_upgrade', {'image': self.args.image, 'identity_unchanged': True, 'api_and_workers_unchanged': True})

    def renew(self, resume=False):
        self.hierarchy()
        baseline, image = self.baseline()
        self.runtime(baseline, image)
        before, old_rows = baseline['identity'], baseline['rows']
        if resume:
            failed = Path(self.args.renewal)
            m.require(m.read(failed / 'report.json')['status'] == 'failed'
                      and m.read(failed / 'renewal-intent.json')['previous'] == before, 'failed signal evidence required')
            self.report['reconciled_from'] = str(failed)
        else:
            m.require(self.inspect() == before and self.rows() == old_rows, 'identity changed before signal; reconcile')
            pod = self.pod()
            name = pod['metadata']['name']
            command = self.kube(['-n', AM_NS, 'exec', name, '-c', 'pkimanagement', '--', 'cat', '/proc/1/comm']).strip()
            m.require(command == 'pkimanagement', 'PID 1 is not the managed owner')
            self.save('renewal-intent.json', {'previous': before, 'pod_uid': pod['metadata']['uid'], 'at': m.stamp()})
            self.kube(['-n', AM_NS, 'exec', name, '-c', 'pkimanagement', '--', 'sh', '-c', 'kill -HUP 1'])
        deadline = time.monotonic() + 150
        while True:
            after = self.inspect()
            if not after['pending'] and after['fingerprint'] != before['fingerprint']:
                break
            m.require(time.monotonic() < deadline, 'renewal not installed; retain evidence and do not signal again')
            time.sleep(2)
        rows = self.rows()
        receipt = replacement(before, after, old_rows, rows)
        self.save('renewed-identity.json', after)
        self.save('renewed-rows.json', rows)
        self.verify_identity()
        self.rollout('account-manager', ns=AM_NS)
        m.require(self.inspect() == after and self.rows() == rows, 'restart changed renewed identity/request')
        self.verify_identity()
        self.device_baseline()
        self.check('managed_early_renewal', {'request_id': receipt['request_id'], 'fingerprint': receipt['fingerprint'],
                   'private_key_exported': False, 'bootstrap_free_restart': True, 'exactly_one_new_issuance': True,
                   'live_old_credential_revocation_and_crl_receipts': 'not yet qualified'})

    def verify(self):
        self.hierarchy()
        baseline, image = self.baseline()
        self.runtime(baseline, image)
        source = Path(self.args.renewal)
        m.require(m.read(source / 'report.json')['status'] == 'passed', 'successful renewal required')
        after, rows = self.inspect(), self.rows()
        m.require(after == m.read(source / 'renewed-identity.json') and rows == m.read(source / 'renewed-rows.json'), 'renewed identity changed')
        replacement(baseline['identity'], after, baseline['rows'], rows)
        self.verify_identity()
        self.device_baseline()
        self.check('renewal_final_audit', {'identity': after, 'unchanged_other_images': True, 'original_pvc': True,
                    'live_timer_due_boundary': 'not qualified', 'old_credential_retirement': 'next step'})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', required=True, choices=['adopt', 'renew', 'resume-renew', 'verify'])
    parser.add_argument('--config-root', default=str(Path.home() / '.config/rtk_cloud'))
    for name in ('authority', 'intermediate', 'output'):
        parser.add_argument('--' + name, required=True)
    for name in ('adoption', 'renewal', 'image'):
        parser.add_argument('--' + name)
    args = parser.parse_args()
    m.require(args.phase != 'adopt' or args.image, 'owner image required')
    m.require(args.phase == 'adopt' or args.adoption, 'owner adoption evidence required')
    m.require(args.phase not in ('verify', 'resume-renew') or args.renewal, 'renewal evidence required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = RenewalRun(args)
    try:
        runner.preflight()
        {'adopt': runner.adopt, 'renew': runner.renew, 'resume-renew': lambda: runner.renew(True), 'verify': runner.verify}[args.phase]()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'; runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report); runner.close(); os.close(fd)
        print(json.dumps({'phase': args.phase, 'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    def interrupted(*_): raise RuntimeError('interrupted; reconcile saved request before any further signal')
    signal.signal(signal.SIGTERM, interrupted); signal.signal(signal.SIGINT, interrupted)
    try: main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr); sys.exit(1)
