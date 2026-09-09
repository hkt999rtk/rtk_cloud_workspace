#!/usr/bin/env python3
"""Retire replaced dev listener client/server leaves and publish their CRLs."""
import argparse
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import signal
import sys
import time

spec = importlib.util.spec_from_file_location('listener_renewal', Path(__file__).with_name('listener_renewal.py'))
r = importlib.util.module_from_spec(spec)
spec.loader.exec_module(r)
m, NS = r.m, r.NS
CRL_STATE = '/var/lib/pki-host/identity/crls'


class ListenerRetirementRun(r.ListenerRenewalRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['listener_retirement_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.report['foundation_scope'] = 'Dev replaced managed-listener client and server leaf retirement'

    def renewal_sources(self):
        verify = m.read(Path(self.args.renewal_verification) / 'report.json')
        m.require(verify['status'] == 'passed' and verify['phase'] == 'verify', 'successful listener renewal audit required')
        return {'certissuer': Path(self.args.certissuer_renewal), 'pki-controller': Path(self.args.controller_renewal)}

    def full_client_rows(self, name):
        subject = r.e.profile(name)['subject']
        raw = self.sql("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,subject,caller,status,fingerprint,certificate_pem,issued_at,revoked_at "
                       "FROM pki_service_client_issuances WHERE environment='dev' AND subject='" + subject + "' ORDER BY issued_at,request_id) t;")
        return [json.loads(line) for line in raw.splitlines() if line]

    def full_server_rows(self, name):
        raw = self.sql("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,caller,status,fingerprint,dns_names,certificate_pem,issued_at,revoked_at "
                       "FROM pki_server_issuances WHERE environment='dev' AND domain='service' ORDER BY issued_at,request_id) t;")
        host = name + '.' + NS + '.svc'
        return [row for row in (json.loads(line) for line in raw.splitlines() if line) if row['dns_names'] == [host]]

    def targets(self):
        result = []
        for name, source in self.renewal_sources().items():
            report = m.read(source / 'report.json')
            m.require(report['status'] == 'passed' and report['phase'] in (name + '-renew', 'resume-' + name + '-renew'),
                      'matching successful listener renewal required')
            before, after = m.read(source / 'baseline.json'), m.read(source / 'renewed.json')
            for kind, rows in (('client', self.full_client_rows(name)), ('server', self.full_server_rows(name))):
                state = 'client' if kind == 'client' else 'host'
                old = [row for row in rows if row['fingerprint'] == before[state]['fingerprint']]
                new = [row for row in rows if row['fingerprint'] == after[state]['fingerprint']]
                m.require(len(old) == len(new) == 1 and old[0]['issuer_id'] == new[0]['issuer_id']
                          and old[0]['status'] == new[0]['status'] == 'succeeded'
                          and new[0]['revoked_at'] is None, 'listener renewal registry target changed')
                result.append({'listener': name, 'kind': kind, 'issuer_id': old[0]['issuer_id'],
                               'fingerprint': old[0]['fingerprint'], 'certificate_pem': old[0]['certificate_pem'],
                               'successor_fingerprint': new[0]['fingerprint'],
                               'successor_request_id': new[0]['request_id']})
        m.require(len(result) == 4 and len({item['fingerprint'] for item in result}) == 4
                  and len({item['issuer_id'] for item in result}) == 1, 'expected four distinct replaced leaves under one issuer')
        return result

    def require_target_revocation_state(self, targets, revoked):
        for target in targets:
            rows = self.full_client_rows(target['listener']) if target['kind'] == 'client' else self.full_server_rows(target['listener'])
            selected = [row for row in rows if row['fingerprint'] == target['fingerprint']]
            m.require(len(selected) == 1 and (selected[0]['revoked_at'] is not None) == revoked,
                      'replaced listener revocation state changed')

    def current_successors(self, sources):
        root, issuer = self.hierarchy()
        for name, source in sources.items():
            expected = m.read(source / 'renewed.json')
            client, host = self.inspect_state(name, r.e.STATE), self.inspect_state(name, r.HOST_STATE)
            m.require(client == expected['client'] and host == expected['host'], 'installed listener successor changed')
            for kind, rows, identity in (('client', self.full_client_rows(name), client),
                                         ('server', self.full_server_rows(name), host)):
                selected = [row for row in rows if row['fingerprint'] == identity['fingerprint']]
                m.require(len(selected) == 1 and selected[0]['issuer_id'] == issuer['issuer_id']
                          and selected[0]['status'] == 'succeeded' and selected[0]['revoked_at'] is None,
                          'current listener ' + kind + ' is not admitted')
            m.require(self.served(name, root) == host['fingerprint'], 'listener successor is not served')
        return root, issuer

    def revoke(self):
        sources = self.renewal_sources()
        _, issuer = self.current_successors(sources)
        targets = self.targets()
        m.require(all(item['issuer_id'] == issuer['issuer_id'] for item in targets), 'replaced listener issuer changed')
        self.require_target_revocation_state(targets, False)
        previous = self.api('/issuers/' + issuer['issuer_id'] + '/crl')
        self.save('targets.json', targets)
        self.save('previous-crl.json', previous)
        records = []
        for index, target in enumerate(targets):
            action = 'service-client' if target['kind'] == 'client' else 'server'
            body = {'certificate_sha256': target['fingerprint'],
                    'reason': 'Replaced by managed ' + target['listener'] + ' ' + target['kind'] + ' renewal ' + target['successor_request_id']}
            self.save('revocation-request-' + str(index) + '.json', body)
            record = self.api('/issuers/' + issuer['issuer_id'] + '/revoke-' + action, body)
            m.require(self.api('/issuers/' + issuer['issuer_id'] + '/revoke-' + action, body) == record,
                      'listener revocation replay changed record')
            self.save('revocation-' + str(index) + '.json', record)
            records.append(record)
        m.require(self.api('/issuers/' + issuer['issuer_id'] + '/crl') == previous,
                  'registry revocation unexpectedly changed provider CRL')
        self.current_successors(sources)
        self.device_baseline()
        self.check('replaced_listener_leaves_denied_in_registry', {'targets': 4, 'client_leaves': 2,
                   'server_leaves': 2, 'successors_admitted': 4, 'provider_publication_pending': True})

    def receipt_state(self, issuer, record):
        receipts = []
        for name in r.e.NAMES:
            deadline = time.monotonic() + 120
            stamp = ''
            while not stamp:
                stamp = self.sql("SELECT acknowledged_at FROM pki_crl_acknowledgments WHERE issuer_id='" + issuer['issuer_id']
                                 + "' AND digest='" + record['crl_sha256'] + "' AND consumer_id='" + name + "';")
                if stamp:
                    break
                m.require(time.monotonic() < deadline, 'listener revocation CRL receipt deadline')
                time.sleep(3)
            state_path = CRL_STATE + '/' + issuer['issuer_id'] + '.json'
            state = json.loads(self.kube(['-n', NS, 'exec', 'deployment/' + name, '-c', name, '--', 'cat', state_path]))
            m.require(state['issuer_fingerprint'] == issuer['certificate_fingerprint_sha256'] and state['crl'] == record,
                      'installed listener revocation CRL differs')
            modes = self.kube(['-n', NS, 'exec', 'deployment/' + name, '-c', name, '--',
                               'stat', '-c', '%a', CRL_STATE, state_path]).split()
            m.require(modes in (['700', '600'], ['2700', '600']), 'listener CRL state permissions differ')
            receipts.append({'consumer': name, 'acknowledged_at': stamp})
        return receipts

    def publish(self):
        source = Path(self.args.revocation)
        report = m.read(source / 'report.json')
        m.require(report['status'] == 'passed' and report['phase'] == 'revoke', 'successful registry revocation evidence required')
        sources = self.renewal_sources()
        _, issuer = self.current_successors(sources)
        targets = m.read(source / 'targets.json')
        m.require(targets == self.targets(), 'saved revoked listener targets changed')
        self.require_target_revocation_state(targets, True)
        previous = m.read(source / 'previous-crl.json')
        publications = []
        for index, target in enumerate(targets):
            current = self.api('/issuers/' + issuer['issuer_id'] + '/crl')
            self.receipt_state(issuer, current)
            action = 'service-client' if target['kind'] == 'client' else 'server'
            body = {'certificate_sha256': target['fingerprint']}
            published = self.api('/issuers/' + issuer['issuer_id'] + '/publish-' + action + '-revocation', body)
            self.save('publication-' + str(index) + '.json', published)
            receipts = self.receipt_state(issuer, published)
            m.require(self.api('/issuers/' + issuer['issuer_id'] + '/publish-' + action + '-revocation', body) == published,
                      'immediate listener CRL publication replay changed record')
            publications.append({'record': published, 'receipts': receipts})
        final = publications[-1]['record']
        self.save('published-crl.json', final)
        old_entries = json.loads(m.command([self.probe, 'crl'], previous['crl_pem'])) or []
        entries = json.loads(m.command([self.probe, 'crl'], final['crl_pem'])) or []
        m.require(all(entry in entries for entry in old_entries), 'listener retirement CRL lost prior entries')
        for index, target in enumerate(targets):
            path = self.output / ('target-' + str(index) + '.pem')
            self.save(path.name, target['certificate_pem'])
            serial = m.command([self.openssl, 'x509', '-in', path, '-noout', '-serial']).strip().split('=')[1]
            m.require(any(int(entry['serial_hex'], 16) == int(serial, 16) for entry in entries),
                      'final listener CRL omits a replaced leaf')
        finalized = []
        for target in targets:
            action = 'service-client' if target['kind'] == 'client' else 'server'
            result = self.api('/issuers/' + issuer['issuer_id'] + '/finalize-' + action + '-revocation',
                              {'certificate_sha256': target['fingerprint']})
            m.require(result['crl_sha256'] == final['crl_sha256'], 'listener revocation finalized against another CRL')
            finalized.append(result)
        self.save('finalized.json', finalized)
        self.current_successors(sources)
        self.device_baseline()
        self.check('replaced_listener_leaves_published', {'targets': 4, 'successive_publications': len(publications),
                   'final_crl_sha256': final['crl_sha256'], 'required_receipts': 2,
                   'all_finalized_against_final_crl': True})

    def verify(self):
        source = Path(self.args.publication)
        report = m.read(source / 'report.json')
        m.require(report['status'] == 'passed' and report['phase'] == 'publish', 'successful listener publication evidence required')
        sources = self.renewal_sources()
        _, issuer = self.current_successors(sources)
        final = m.read(source / 'published-crl.json')
        m.require(self.api('/issuers/' + issuer['issuer_id'] + '/crl') == final, 'listener retirement CRL changed')
        self.receipt_state(issuer, final)
        targets = m.read(Path(self.args.revocation) / 'targets.json')
        for target in targets:
            rows = self.full_client_rows(target['listener']) if target['kind'] == 'client' else self.full_server_rows(target['listener'])
            selected = [row for row in rows if row['fingerprint'] == target['fingerprint']]
            m.require(len(selected) == 1 and selected[0]['revoked_at'] is not None, 'replaced listener leaf remains admitted')
            action = 'service-client' if target['kind'] == 'client' else 'server'
            result = self.api('/issuers/' + issuer['issuer_id'] + '/finalize-' + action + '-revocation',
                              {'certificate_sha256': target['fingerprint']})
            m.require(result['crl_sha256'] == final['crl_sha256'], 'finalized listener revocation changed')
        for name in r.e.NAMES:
            self.rollout(name)
        self.current_successors(sources)
        self.device_baseline()
        self.check('listener_retirement_final_audit', {'revoked_and_crl_covered': 4, 'current_successors_admitted': 4,
                   'bootstrap_free_restarts': 2, 'old_private_keys_available': False,
                   'preexisting_old_session_cutoff': 'unqualified; old keys were discarded at renewal'})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', choices=['revoke', 'publish', 'verify'], required=True)
    parser.add_argument('--config-root', default=str(Path.home() / '.config/rtk_cloud'))
    for key in ('authority', 'intermediate', 'egress', 'crl', 'certissuer-renewal',
                'controller-renewal', 'renewal-verification', 'output'):
        parser.add_argument('--' + key, required=True)
    parser.add_argument('--revocation')
    parser.add_argument('--publication')
    args = parser.parse_args()
    m.require(args.phase == 'revoke' or args.revocation, 'registry revocation evidence required')
    m.require(args.phase != 'verify' or args.publication, 'CRL publication evidence required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = ListenerRetirementRun(args)
    try:
        runner.preflight()
        {'revoke': runner.revoke, 'publish': runner.publish, 'verify': runner.verify}[args.phase]()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'; runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report); runner.close(); os.close(fd)
        print(json.dumps({'phase': args.phase, 'status': runner.report['status'],
                          'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    def interrupted(*_): raise RuntimeError('interrupted; reconcile saved revocations before replay')
    signal.signal(signal.SIGTERM, interrupted); signal.signal(signal.SIGINT, interrupted)
    try: main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr); sys.exit(1)
