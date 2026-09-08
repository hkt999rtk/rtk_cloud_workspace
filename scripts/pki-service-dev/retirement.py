#!/usr/bin/env python3
"""Retire the replaced dev Service leaf and qualify actual listener CRL receipts."""
import argparse
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import sys
import time

spec = importlib.util.spec_from_file_location('service_crl', Path(__file__).with_name('crl.py'))
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)
h, m = c.h, c.m
NS = h.NS
NAMES = ['certissuer', 'pki-controller']
CONFIG = 'pki-service-client-crls'
STATE = '/var/lib/pki-host/identity/crls'


def retirement_target(original, current, rows):
    m.require(not current['pending'] and current['subject'] == 'service:account-manager'
              and original['fingerprint'] != current['fingerprint'], 'refuse to retire installed or pending identity')
    selected = [row for row in rows if row['fingerprint'] == original['fingerprint']]
    replacement = [row for row in rows if row['fingerprint'] == current['fingerprint']]
    m.require(len(rows) == 2 and len(selected) == len(replacement) == 1 and selected[0] == original
              and replacement[0]['status'] == 'succeeded' and replacement[0]['revoked_at'] is None,
              'retirement registry baseline changed')
    return selected[0]


class RetirementRun(c.CRLRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['retirement_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.report['foundation_scope'] = 'Dev replaced Service leaf retirement and listener CRL receipts'

    def selected(self):
        source = Path(self.args.retirement)
        m.require(m.read(source / 'report.json')['status'] == 'passed', 'successful retirement evidence required')
        return source, m.read(source / 'target.json'), m.read(source / 'published-crl.json')

    def finish(self, target, expected):
        return self.api('/issuers/' + target['issuer_id'] + '/finalize-service-client-revocation',
                        {'certificate_sha256': target['fingerprint']}, expected=expected)

    def unchanged(self, source, names=()):
        baseline = m.read(source / 'baseline.json')
        after = self.snapshot()
        expected = json.loads(json.dumps(baseline))
        for name in names:
            expected['images'][name][name] = self.args.image
        m.require(after == expected, 'identity/worker/unrelated image changed')
        return after

    def retire(self):
        root, issuer = self.hierarchy()
        renewal, adoption = Path(self.args.renewal), Path(self.args.adoption)
        m.require(m.read(renewal / 'report.json')['status'] == 'passed', 'successful managed renewal required')
        current = self.inspect()
        m.require(current == m.read(renewal / 'renewed-identity.json'), 'installed successor differs from renewal')
        rows = self.rows()
        m.require(rows == m.read(renewal / 'renewed-rows.json'), 'renewal registry changed')
        original = m.read(adoption / 'baseline.json')['rows'][0]
        target = retirement_target(original, current, rows)
        m.require(target['issuer_id'] == issuer['issuer_id'], 'old leaf has another issuer')
        self.save('target.json', target)
        self.save('baseline.json', self.snapshot())
        previous = self.api('/issuers/' + issuer['issuer_id'] + '/crl')
        self.save('previous-crl.json', previous)
        reason = 'Replaced by managed Account Manager renewal ' + self.issuance()['request_id']
        body = {'certificate_sha256': target['fingerprint'], 'reason': reason}
        self.save('revocation-request.json', body)
        record = self.api('/issuers/' + issuer['issuer_id'] + '/revoke-service-client', body)
        self.save('revocation.json', record)
        m.require(self.api('/issuers/' + issuer['issuer_id'] + '/revoke-service-client', body) == record, 'revocation replay changed record')
        self.finish(target, 409)
        published = self.api('/issuers/' + issuer['issuer_id'] + '/publish-service-client-revocation', {'certificate_sha256': target['fingerprint']})
        self.save('published-crl.json', published)
        self.check_published(target, previous, published)
        m.require(self.api('/issuers/' + issuer['issuer_id'] + '/publish-service-client-revocation', {'certificate_sha256': target['fingerprint']}) == published,
                  'publication replay changed CRL')
        self.finish(target, 409)
        m.require(self.receipts(issuer['issuer_id'], published['crl_sha256'], 'crl') == [], 'unexpected Service CRL receipts before consumer rollout')
        self.verify_identity()
        self.device_baseline()
        self.check('old_leaf_retired_publication_pending_receipts', {'fingerprint': target['fingerprint'], 'crl_sha256': published['crl_sha256'],
                    'publication_idempotent': True, 'finalization_without_receipts': 409})

    def check_published(self, target, previous, published):
        issuer = self.api('/issuers/' + target['issuer_id'])
        m.require(self.inspect_crl(published['crl_pem'], issuer) == published and int(published['crl_number']) > int(previous['crl_number']), 'invalid published CRL')
        entries = json.loads(m.command([self.probe, 'crl'], published['crl_pem'])) or []
        old_entries = json.loads(m.command([self.probe, 'crl'], previous['crl_pem'])) or []
        self.save('retired-leaf.pem', target['certificate_pem'])
        serial = m.command([self.openssl, 'x509', '-in', self.output / 'retired-leaf.pem', '-noout', '-serial']).strip().split('=')[1]
        m.require(all(e in entries for e in old_entries) and any(int(e['serial_hex'], 16) == int(serial, 16) for e in entries), 'CRL lost prior revocations or omits old leaf')
        rows = self.rows()
        old = [r for r in rows if r['fingerprint'] == target['fingerprint']]
        m.require(len(old) == 1 and old[0]['revoked_at'] is not None, 'old leaf not denied in registry')
        identifier, digest = target['issuer_id'], published['crl_sha256']
        m.require(c.uuid.UUID(identifier).hex and re.fullmatch('[0-9a-f]{64}', digest), 'invalid publication identity')
        counts = self.sql("SELECT (SELECT count(*) FROM pki_audit WHERE issuer_id='" + identifier + "' AND event='service_client_revoked:" + target['fingerprint'] + "'),"
                          "(SELECT count(*) FROM pki_crls WHERE issuer_id='" + identifier + "' AND digest='" + digest + "');")
        m.require(counts == '1|1', 'duplicate/missing revocation audit or CRL')

    def configure(self, name, resume=False):
        root, issuer = self.hierarchy()
        source, target, published = self.selected()
        m.require(self.api('/issuers/' + issuer['issuer_id'] + '/crl') == published, 'published CRL changed before rollout')
        if resume:
            failed = Path(self.args.resume)
            m.require(m.read(failed / 'report.json')['status'] == 'failed', 'failed rollout evidence required')
            # Compare the API-accepted template, including Kubernetes defaults.
            expected = m.read(failed / ('after-' + NS + '-' + name + '-deployment.json'))['spec']['template']
            m.require(expected['spec']['containers'][0]['name'] == name
                      and expected['spec']['containers'][0]['image'] == self.args.image
                      and self.obj('deployment', name)['spec']['template'] == expected,
                      'live rollout differs from saved template; reconcile')
            self.unchanged(source, [name] if name == 'certissuer' else NAMES)
            state = m.read(failed / 'identity-before.json')['sha256']
            m.require(self.state_digest(name) == state, 'listener identity changed during failed rollout')
            self.report['reconciled_from'] = str(failed)
            self.complete_rollout(name, root, issuer, target, published, state)
            return
        if name == 'certissuer':
            self.unchanged(source)
            manifest = [{'issuer': record, 'state_path': STATE + '/' + record['issuer_id'] + '.json'} for record in (root, issuer)]
            self.create({'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': CONFIG, 'namespace': NS}, 'data': {'crls.json': json.dumps(manifest)}})
        else:
            m.require(m.read(Path(self.args.certissuer) / 'report.json')['status'] == 'passed', 'successful certissuer receipt phase required')
            self.unchanged(source, ['certissuer'])
            self.finish(target, 409)
        owner = self.obj('deployment', name)
        pod = json.loads(json.dumps(owner['spec']['template']))
        container = pod['spec']['containers'][0]
        prefix = 'PKI_SERVICE_CLIENT' if name == 'pki-controller' else 'CERT_ISSUER_SERVICE_CLIENT'
        env = {e['name']: e.get('value') for e in container['env']}
        m.require(len(pod['spec']['containers']) == 1 and container['name'] == name
                  and not env.get(prefix + '_SERVER_CRL_MANIFEST') and not any(v['name'] == 'service-crls' for v in pod['spec']['volumes']), 'consumer already configured; reconcile saved rollout')
        state = self.state_digest(name)
        self.save('identity-before.json', {'sha256': state})
        self.kube(['-n', NS, 'exec', 'deployment/' + name, '--', 'sh', '-c', 'umask 077; mkdir -p ' + STATE + '; chmod 700 ' + STATE])
        container['env'] = h.h.h.with_env(container['env'], {prefix + '_SERVER_CRL_MANIFEST': '/run/pki-service-crls/crls.json'})
        container['image'] = self.args.image
        container['volumeMounts'].append({'name': 'service-crls', 'mountPath': '/run/pki-service-crls', 'readOnly': True})
        pod['spec']['volumes'].append({'name': 'service-crls', 'configMap': {'name': CONFIG}})
        self.save('expected-template.json', pod)
        self.scoped_patch('deployment', owner, [{'op': 'replace', 'path': '/spec/template', 'value': pod}])
        key = 'PKI_CONTROLLER_IMAGE' if name == 'pki-controller' else 'PKI_CERTISSUER_IMAGE'
        m.write(self.base / 'operator/env' / key, self.args.image + '\n')
        self.complete_rollout(name, root, issuer, target, published, state)

    def complete_rollout(self, name, root, issuer, target, published, state):
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + name, '--timeout=240s'], timeout=250)
        if name == 'certissuer': self.forward('issuer', NS, name, 9443)
        self.receipt_state(name, (root, issuer), fresh=True)
        if name == 'certissuer': self.finish(target, 409)
        else:
            self.wait_receipts(issuer['issuer_id'], published['crl_sha256'], NAMES, 'crl')
            self.save('finalized.json', self.finish(target, 200))
        self.rollout(name)
        self.receipt_state(name, (root, issuer))
        m.require(self.state_digest(name) == state, 'listener restart changed private identity')
        self.verify_identity()
        self.device_baseline()
        self.check(name + '_durable_crl_receipts', {'crl_sha256': published['crl_sha256'], 'bootstrap_free_restart': True,
                                                 'same_private_identity': True, 'finalized': name == 'pki-controller'})

    def receipt_state(self, name, issuers, fresh=False):
        # Initial installation requires a new receipt. Replays are immutable;
        # restart evidence combines retained state and real admitted traffic.
        selector = self.obj('deployment', name)['spec']['selector']['matchLabels']
        pods = json.loads(self.kube(['-n', NS, 'get', 'pods', '-l', ','.join(k+'='+v for k,v in selector.items()), '-o', 'json']))['items']
        pods = [p for p in pods if not p['metadata'].get('deletionTimestamp')]
        m.require(len(pods) == 1, 'unexpected listener owner')
        started = pods[0]['status']['containerStatuses'][0]['state']['running']['startedAt']
        for issuer in issuers:
            record = self.api('/issuers/' + issuer['issuer_id'] + '/crl')
            deadline = time.monotonic() + 100
            while True:
                stamp = self.sql("SELECT acknowledged_at FROM pki_crl_acknowledgments WHERE issuer_id='" + issuer['issuer_id'] + "' AND digest='" + record['crl_sha256'] + "' AND consumer_id='" + name + "';")
                if stamp and (not fresh or m.parse_time(stamp) >= m.parse_time(started)): break
                m.require(time.monotonic() < deadline, 'fresh listener receipt deadline')
                time.sleep(3)
            raw = self.kube(['-n', NS, 'exec', 'deployment/' + name, '--', 'cat', STATE + '/' + issuer['issuer_id'] + '.json'])
            state = json.loads(raw)
            m.require(state['issuer_fingerprint'] == issuer['certificate_fingerprint_sha256'] and state['crl'] == record, 'installed CRL differs from receipt')
            modes = self.kube(['-n', NS, 'exec', 'deployment/' + name, '--', 'stat', '-c', '%a', STATE, STATE + '/' + issuer['issuer_id'] + '.json']).split()
            m.require(modes in (['700', '600'], ['2700', '600']), 'CRL state permissions differ')
            self.save(name + '-' + issuer['issuer_id'] + '-state.json', state)
        self.check(name + '_installed_crls', {'pod_uid': pods[0]['metadata']['uid'], 'started_at': started, 'new_receipts_required': fresh})

    def verify(self):
        root, issuer = self.hierarchy()
        source, target, published = self.selected()
        self.unchanged(source, NAMES)
        self.check_published(target, m.read(source / 'previous-crl.json'), published)
        for name in NAMES:
            self.receipt_state(name, (root, issuer))
            desired = m.read(self.base / 'pki/controller-bootstrap/rollout' / (name + '-deployment.json'))
            m.require(self.obj('deployment', name)['spec']['template'] == desired['spec']['template'], 'listener persistence differs')
        self.wait_receipts(issuer['issuer_id'], published['crl_sha256'], NAMES, 'crl')
        finalized = self.finish(target, 200)
        m.require(finalized['crl_sha256'] == published['crl_sha256'], 'finalized revocation references another CRL')
        self.save('finalized.json', finalized)
        self.device_baseline()
        self.check('retirement_complete', {'old_fingerprint': target['fingerprint'], 'required_consumers': NAMES,
                    'current_identity_unchanged': True, 'active_old_session_eviction': 'not qualified; old key discarded during renewal'})


def main():
    os.umask(0o077)
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--phase', choices=['retire', 'certissuer', 'controller', 'verify'], required=True)
    p.add_argument('--config-root', default=str(Path.home() / '.config/rtk_cloud'))
    for name in ('authority','intermediate','prepared','adoption','renewal','output'): p.add_argument('--'+name, required=True)
    for name in ('retirement','certissuer','image','resume'): p.add_argument('--'+name)
    args = p.parse_args()
    m.require(args.phase == 'retire' or (args.retirement and re.fullmatch(r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}', args.image or '')), 'retirement evidence and pinned listener image required')
    m.require(args.phase != 'controller' or args.certissuer, 'certissuer phase evidence required')
    m.require(not args.resume or args.phase in ('certissuer', 'controller'), 'resume only applies to a saved listener rollout')
    fd = os.open(Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock', os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX|fcntl.LOCK_NB)
    runner = RetirementRun(args)
    try:
        runner.preflight()
        {'retire': runner.retire, 'certissuer': lambda: runner.configure('certissuer', bool(args.resume)), 'controller': lambda: runner.configure('pki-controller', bool(args.resume)), 'verify': runner.verify}[args.phase]()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'; runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report); runner.close(); os.close(fd)
        print(json.dumps({'phase':args.phase, 'status':runner.report['status'], 'report':str(runner.output/'report.json')}), flush=True)


if __name__ == '__main__':
    def interrupted(*_): raise RuntimeError('interrupted; reconcile saved retirement and runtime before retry')
    signal.signal(signal.SIGTERM, interrupted); signal.signal(signal.SIGINT, interrupted)
    try: main()
    except Exception as error: print(json.dumps({'error':str(error)}), file=sys.stderr); sys.exit(1)
