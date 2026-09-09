#!/usr/bin/env python3
"""Dev managed listener client and host renewal without exporting private keys."""
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

spec = importlib.util.spec_from_file_location('managed_egress', Path(__file__).with_name('egress.py'))
e = importlib.util.module_from_spec(spec)
spec.loader.exec_module(e)
m, NS = e.m, e.NS
HOST_STATE = '/var/lib/pki-host/identity/state.json'


def added_row(before, after, identity, issuer, kind, name):
    added = [row for row in after if row not in before]
    m.require(all(row in after for row in before) and len(after) == len(before) + 1 and len(added) == 1,
              'unexpected ' + kind + ' issuance changes; reconcile')
    row = added[0]
    m.require(row['fingerprint'] == identity['fingerprint'] and row['issuer_id'] == issuer['issuer_id']
              and row['status'] == 'succeeded' and row['revoked_at'] is None
              and row['caller'] == 'service:' + name, 'replacement ' + kind + ' registry receipt differs')
    return row


def managed_runtime(owner, name):
    selected = e.profile(name)
    containers = owner['spec']['template']['spec']['containers']
    m.require(len(containers) == 1 and containers[0]['name'] == name, 'listener container changed')
    values = e.env_values(containers[0])
    m.require(values.get(selected['prefix'] + '_IDENTITY_STATE') == e.STATE
              and not values.get(selected['prefix'] + '_MANAGEMENT_CERT')
              and not values.get(selected['prefix'] + '_MANAGEMENT_KEY')
              and not values.get(selected['host_prefix'] + '_CLIENT_CERT')
              and not values.get(selected['host_prefix'] + '_CLIENT_KEY'),
              'listener is not exclusively using managed egress')
    mounts = {item['name'] for item in containers[0].get('volumeMounts', [])}
    secrets = {item.get('secret', {}).get('secretName') for item in owner['spec']['template']['spec'].get('volumes', [])}
    m.require(selected['secret'] not in secrets and 'service-consumer' not in mounts
              and 'service-managed-egress-ca' in mounts, 'legacy listener credential remains mounted')


class ListenerRenewalRun(e.EgressRun):
    def __init__(self, args):
        super().__init__(args)
        # Controller has a read-only root filesystem; its private identity PVC
        # is the only writable owner-local path shared by both listeners.
        self.remote_probe = '/var/lib/pki-host/identity/.pki-dev-probe-' + uuid.uuid4().hex
        self.probe_pods = {}
        self.report['listener_renewal_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.report['foundation_scope'] = 'Dev managed listener client and host operator renewal'

    def close(self):
        for uid, item in self.probe_pods.items():
            name, pod = item
            try:
                if self.obj('pod', pod)['metadata']['uid'] == uid:
                    self.kube(['-n', NS, 'exec', pod, '-c', name, '--', 'rm', '-f', self.remote_probe])
            except RuntimeError:
                pass
        super().close()

    def pod(self, name):
        owner = self.deployment(name)
        selector = ','.join(k + '=' + v for k, v in owner['spec']['selector']['matchLabels'].items())
        pods = json.loads(self.kube(['-n', NS, 'get', 'pods', '-l', selector, '-o', 'json']))['items']
        pods = [pod for pod in pods if not pod['metadata'].get('deletionTimestamp')]
        m.require(len(pods) == 1 and all(c['ready'] for c in pods[0]['status'].get('containerStatuses', [])),
                  'expected one ready listener identity owner')
        return pods[0]

    def install_probe(self, name):
        pod = self.pod(name)
        uid, podname = pod['metadata']['uid'], pod['metadata']['name']
        if uid not in self.probe_pods:
            binary = self.output / 'pki-dev-probe-linux'
            if not binary.exists():
                result = m.subprocess.run(['go', 'build', '-trimpath', '-ldflags=-s -w', '-o', str(binary), './pki-dev-probe'],
                    cwd=m.WORKSPACE / 'scripts/go', env=dict(os.environ, GOOS='linux', GOARCH='amd64', CGO_ENABLED='0', GOWORK='off'),
                    capture_output=True, timeout=180)
                m.require(result.returncode == 0, 'owner-local public inspection probe build failed')
            self.kube(['-n', NS, 'exec', '-i', podname, '-c', name, '--', 'sh', '-c',
                       'umask 077; base64 -d > ' + self.remote_probe + ' && chmod 700 ' + self.remote_probe],
                      base64.b64encode(binary.read_bytes()).decode())
            self.probe_pods[uid] = (name, podname)
        return podname

    def inspect_state(self, name, path):
        pod = self.install_probe(name)
        return json.loads(self.kube(['-n', NS, 'exec', pod, '-c', name, '--', self.remote_probe, 'service-state', path]))

    def client_rows(self, name):
        return super().client_rows(e.profile(name)['subject'])

    def server_rows(self, name):
        raw = self.sql("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,caller,status,fingerprint,dns_names,issued_at,revoked_at "
                       "FROM pki_server_issuances WHERE environment='dev' AND domain='service' ORDER BY issued_at,request_id) t;")
        host = name + '.' + NS + '.svc'
        return [row for row in (json.loads(line) for line in raw.splitlines() if line) if row['dns_names'] == [host]]

    def served(self, name, root):
        label, port = name + '-renewed-peer', 9443 if name == 'certissuer' else 18446
        # kubectl port-forward resolves a Service to one pod and exits when a
        # rollout replaces that pod. Always bind a fresh forward for wire checks.
        self.forward(label, NS, name, port)
        return json.loads(m.command([self.probe, 'tls-peer', self.output / 'root.pem', name + '.' + NS + '.svc',
                                     self.ports[label][0]]))['peer_sha256']

    def prerequisites(self, name):
        root, issuer = self.hierarchy()
        egress_report = m.read(Path(self.args.egress) / 'report.json')
        crl_report = m.read(Path(self.args.crl) / 'report.json')
        m.require(egress_report['status'] == 'passed' and egress_report['phase'] == 'verify'
                  and crl_report['status'] == 'passed' and crl_report['phase'] == 'qualify-listeners',
                  'successful managed egress verification and fresh listener CRL qualification required')
        receipt_check = crl_report['checks'][name + '_managed_crl_receipts']
        receipts = receipt_check['evidence']['receipts']
        current = {item['issuer_id']: item['crl_sha256'] for item in receipts}
        m.require(receipt_check['status'] == 'passed' and len(current) == 2
                  and current == {item['issuer_id']: self.api('/issuers/' + item['issuer_id'] + '/crl')['crl_sha256']
                                  for item in receipts},
                  'qualified listener CRL receipts no longer match current publications')
        owner = self.deployment(name)
        managed_runtime(owner, name)
        m.require(owner['metadata']['uid'] and self.obj('persistentvolumeclaim', name + '-service-identity')['metadata']['uid'],
                  'listener/PVC identity unavailable')
        self.save('root.pem', root['certificate_pem'])
        return root, issuer, owner

    def snapshot(self, name, root, owner):
        return {'deployment_uid': owner['metadata']['uid'],
                'pvc_uid': self.obj('persistentvolumeclaim', name + '-service-identity')['metadata']['uid'],
                'image': owner['spec']['template']['spec']['containers'][0]['image'],
                'client': self.inspect_state(name, e.STATE), 'host': self.inspect_state(name, HOST_STATE),
                'client_rows': self.client_rows(name), 'server_rows': self.server_rows(name)}

    def assert_replacement(self, name, issuer, before, after):
        for kind in ('client', 'host'):
            old, new = before[kind], after[kind]
            m.require(not new['pending'] and new['subject'] == old['subject'] and new['root_sha256'] == old['root_sha256']
                      and new['fingerprint'] != old['fingerprint'] and new['public_key_sha256'] != old['public_key_sha256']
                      and new['state_sha256'] != old['state_sha256'], kind + ' identity did not rotate cleanly')
        client = added_row(before['client_rows'], after['client_rows'], after['client'], issuer, 'client', name)
        server = added_row(before['server_rows'], after['server_rows'], after['host'], issuer, 'host', name)
        return client, server

    def signal(self, name, before):
        pod = self.pod(name)
        command = self.kube(['-n', NS, 'exec', pod['metadata']['name'], '-c', name, '--', 'cat', '/proc/1/comm']).strip()
        expected = 'pkicontroller' if name == 'pki-controller' else 'certissuer'
        m.require(command == expected, 'PID 1 is not the listener identity owner')
        intent = {'listener': name, 'previous_client': before['client']['fingerprint'],
                  'previous_host': before['host']['fingerprint'],
                  'pod_uid': pod['metadata']['uid'], 'at': m.stamp()}
        self.save('renewal-intent.json', intent)
        self.kube(['-n', NS, 'exec', pod['metadata']['name'], '-c', name, '--', 'sh', '-c', 'kill -HUP 1'])

    def renew(self, name, resume=False):
        root, issuer, owner = self.prerequisites(name)
        if resume:
            failed = Path(self.args.failed)
            report = m.read(failed / 'report.json')
            m.require(report['status'] == 'failed' and report['phase'] in (name + '-renew', 'resume-' + name + '-renew'),
                      'matching failed listener renewal evidence required')
            before = m.read(failed / 'baseline.json')
            image = m.read(failed / 'image.json')['image']
            self.save('baseline.json', before)
            self.save('image.json', {'image': image})
            m.require(owner['metadata']['uid'] == before['deployment_uid']
                      and self.obj('persistentvolumeclaim', name + '-service-identity')['metadata']['uid'] == before['pvc_uid']
                      and owner['spec']['template']['spec']['containers'][0]['image'] == image,
                      'failed renewal owner/image/PVC drifted; reconcile')
            self.report['reconciled_from'] = str(failed)
            if (failed / 'renewal-intent.json').is_file():
                self.save('renewal-intent.json', m.read(failed / 'renewal-intent.json'))
            else:
                saved = m.read(failed / ('after-' + NS + '-' + name + '-deployment.json'))
                m.require(owner['spec']['template'] == saved['spec']['template'],
                          'failed pre-intent rollout template drifted; reconcile')
                current = self.snapshot(name, root, owner)
                m.require(current | {'image': before['image']} == before,
                          'identity changed after failed pre-intent rollout; do not signal')
                self.signal(name, before)
        else:
            before = self.snapshot(name, root, owner)
            image = self.args.image
            m.require(before['image'] != image, 'listener already uses renewal image; reconcile instead of signaling')
            self.save('baseline.json', before)
            self.save('image.json', {'image': image})
            template = json.loads(json.dumps(owner['spec']['template']))
            template['spec']['containers'][0]['image'] = image
            self.apply_template(name, owner, template, image)
            owner = self.deployment(name)
            m.require(self.snapshot(name, root, owner) | {'image': before['image']} == before,
                      'image rollout changed identity, registry rows or owner')
            self.signal(name, before)
        deadline = time.monotonic() + 180
        while True:
            after = {'deployment_uid': before['deployment_uid'], 'pvc_uid': before['pvc_uid'], 'image': image,
                     'client': self.inspect_state(name, e.STATE), 'host': self.inspect_state(name, HOST_STATE),
                     'client_rows': self.client_rows(name), 'server_rows': self.server_rows(name)}
            if (not after['client']['pending'] and not after['host']['pending']
                    and after['client']['fingerprint'] != before['client']['fingerprint']
                    and after['host']['fingerprint'] != before['host']['fingerprint']):
                break
            m.require(time.monotonic() < deadline, 'listener renewal incomplete; retain evidence and do not signal again')
            time.sleep(2)
        client, server = self.assert_replacement(name, issuer, before, after)
        m.require(self.served(name, root) == after['host']['fingerprint'], 'listener does not serve renewed host identity')
        self.save('renewed.json', after)
        restart_already_completed = False
        if resume:
            failed = Path(self.args.failed)
            saved_restart = failed / ('after-' + NS + '-' + name + '-deployment.json')
            if (failed / 'renewed.json').is_file() and saved_restart.is_file():
                owner = self.deployment(name)
                restart_already_completed = (m.read(failed / 'renewed.json') == after
                                             and owner['spec']['template'] == m.read(saved_restart)['spec']['template'])
        if not restart_already_completed:
            self.rollout(name)
            owner = self.deployment(name)
        restarted = self.snapshot(name, root, owner)
        m.require(restarted == after and self.served(name, root) == after['host']['fingerprint'],
                  'bootstrap-free restart changed renewed listener identity')
        self.device_baseline()
        self.check(name + '_managed_identities_renewed', {'client_request_id': client['request_id'],
                   'server_request_id': server['request_id'], 'client_then_host': True,
                   'exactly_one_new_client_issuance': True, 'exactly_one_new_server_issuance': True,
                   'served_host_replaced': True, 'bootstrap_free_restart': True, 'private_keys_exported': False})

    def verify(self):
        for name, source in (('certissuer', self.args.certissuer), ('pki-controller', self.args.controller)):
            root, issuer, owner = self.prerequisites(name)
            report = m.read(Path(source) / 'report.json')
            m.require(report['status'] == 'passed' and report['phase'] in (name + '-renew', 'resume-' + name + '-renew'),
                      'successful matching listener renewal evidence required')
            before, expected = m.read(Path(source) / 'baseline.json'), m.read(Path(source) / 'renewed.json')
            current = self.snapshot(name, root, owner)
            m.require(current == expected and owner['spec']['template']['spec']['containers'][0]['image'] == self.args.image,
                      'renewed listener state/image changed')
            self.assert_replacement(name, issuer, before, current)
            m.require(self.served(name, root) == current['host']['fingerprint'], 'renewed listener wire identity changed')
            rendered = e.h.h.h.render_persisted_listener(self.base, name)
            m.require(e.normalized_template(rendered['spec']['template']) == e.normalized_template(owner['spec']['template']),
                      'persisted renewed listener template differs from live state')
        self.device_baseline()
        self.check('managed_listener_renewal_final_audit', {'listeners': e.NAMES, 'client_renewals': 2,
                   'server_renewals': 2, 'bootstrap_free_restarts': 2, 'private_keys_exported': False,
                   'old_identity_revocation': 'next step'})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', choices=['certissuer-renew', 'resume-certissuer-renew',
                                           'pki-controller-renew', 'resume-pki-controller-renew', 'verify'], required=True)
    parser.add_argument('--config-root', default=str(Path.home() / '.config/rtk_cloud'))
    for key in ('authority', 'intermediate', 'egress', 'crl', 'output'):
        parser.add_argument('--' + key, required=True)
    parser.add_argument('--image')
    parser.add_argument('--failed')
    parser.add_argument('--certissuer')
    parser.add_argument('--controller')
    args = parser.parse_args()
    resume = args.phase.startswith('resume-')
    name = args.phase.removeprefix('resume-').removesuffix('-renew')
    m.require(resume or re.fullmatch(r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}', args.image or ''),
              'verified dev listener image required')
    m.require(not resume or args.failed, 'failed renewal evidence required')
    m.require(args.phase != 'verify' or (args.certissuer and args.controller), 'both listener renewal reports required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = ListenerRenewalRun(args)
    try:
        runner.preflight()
        runner.verify() if args.phase == 'verify' else runner.renew(name, resume)
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'; runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report); runner.close(); os.close(fd)
        print(json.dumps({'phase': args.phase, 'status': runner.report['status'],
                          'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    def interrupted(*_): raise RuntimeError('interrupted; reconcile saved intent before any further signal')
    signal.signal(signal.SIGTERM, interrupted); signal.signal(signal.SIGINT, interrupted)
    try: main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr); sys.exit(1)
