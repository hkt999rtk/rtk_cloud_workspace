#!/usr/bin/env python3
"""Rotate only Account Manager's Dev Service client to the final authority."""
import argparse
import copy
import fcntl
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

AM_NS = 'video-cloud-dev-account-manager'
NAME, SUBJECT = 'account-manager', 'service:account-manager'
STATE = '/var/lib/account-pki/private/identity-root-697e8e86-5af.json'
ROOT = '32bbbfd220db619ebcf54af5f62221ed49635e58ddaa42e67730676f073704eb'
OLD = '87099089d30f13a7b93035b59c1c0c91bdb05bbe3dab427258c0c48e38144cc2'
FINAL = 'd61845ca-6b85-4f11-920b-f2f9685b0c13'
HOST = 'account-manager-internal.' + AM_NS + '.svc'
OWNER = 'ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:f37f50731edb184212b6ae1fc153f3014f58448c6d310ef58e17984d89916ec3'
OWNER_PIN = 'PKI_ACCOUNT_MANAGER_OWNER_IMAGE'
PERSISTED = Path('pki/controller-bootstrap/rollout/account-manager-deployment.json')


class AccountManagerFinalClient(r.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = ('Dev Account Manager final Service-client rotation; '
                                           'predecessor Account Manager listener and controller verification retained for R4')
        self.report['service_successor_account_manager_client_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def state(self):
        value = json.loads(self.kube(['-n', AM_NS, 'exec', 'deployment/' + NAME, '-c', 'pkimanagement', '--',
                                      '/app/serviceidentity-bootstrap', 'inspect', STATE, SUBJECT, ROOT]))
        m.require(value.get('subject') == SUBJECT and value.get('root_sha256') == ROOT and not value.get('pending')
                  and re.fullmatch(r'[0-9a-f]{64}', value.get('fingerprint', ''))
                  and re.fullmatch(r'[0-9a-f]{64}', value.get('public_key_sha256', '')),
                  'Account Manager client state differs')
        return value

    def rows(self, kind):
        if kind == 'client':
            query = ("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,subject,caller,status,fingerprint,issued_at "
                     "FROM pki_service_client_issuances WHERE environment='dev' AND subject='service:account-manager' "
                     "AND revoked_at IS NULL ORDER BY issued_at,request_id) t;")
        else:
            query = ("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,caller,status,fingerprint,issued_at "
                     "FROM pki_server_issuances WHERE environment='dev' AND domain='service' "
                     "AND dns_names='[\"" + HOST + "\"]'::jsonb AND revoked_at IS NULL ORDER BY issued_at,request_id) t;")
        raw = self.kube(['-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--', 'psql', '-X',
                         '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'video_cloud', '-At'], query)
        return [json.loads(line) for line in raw.splitlines() if line]

    def preflight_retry(self):
        recovery_deadline, successes = None, 0
        attempts = 0
        while successes < 3:
            attempts += 1
            try:
                super().preflight()
                successes += 1
            except RuntimeError as error:
                successes = 0
                if 'status 503' not in str(error):
                    raise
                if recovery_deadline is None:
                    recovery_deadline = time.monotonic() + 60
                if time.monotonic() >= recovery_deadline:
                    raise
                time.sleep(2)
                continue
            if successes < 3:
                time.sleep(2)
        self.report['control_plane_preflight'] = {
            'consecutive_successes': successes,
            'attempts': attempts,
            'transient_503_recovered': attempts != successes,
        }
        self.save('report.json', self.report)

    def issuer(self):
        deadline, failure = time.monotonic() + 60, None
        while True:
            try:
                value = self.api('/issuers/' + FINAL)
                m.require(value['status'] == 'active' and value['environment'] == 'dev'
                          and value['trust_domain'] == 'service' and SUBJECT in value['service_client_ids'],
                          'final Account Manager client authority changed')
                self.api('/issuers/' + FINAL + '/crl')
                return value
            except Exception as error:
                failure = error
                if time.monotonic() >= deadline:
                    raise failure
                time.sleep(2)

    @staticmethod
    def environment(template):
        containers = [c for c in template['spec']['containers'] if c['name'] == 'pkimanagement']
        m.require(len(containers) == 1, 'Account Manager identity owner topology changed')
        values = {entry['name']: entry for entry in containers[0].get('env', [])}
        m.require(len(values) == len(containers[0].get('env', [])), 'duplicate Account Manager identity owner environment')
        return containers[0], values

    def transition_template(self, owner):
        template = copy.deepcopy(owner['spec']['template'])
        container, env = self.environment(template)
        roots = self.obj('configmap', 'account-manager-service-root-697e8e86-5af', AM_NS)
        root_pem = roots.get('data', {}).get('root.pem', '')
        m.require(roots.get('immutable') and root_pem.count('-----BEGIN CERTIFICATE-----') == 2,
                  'reviewed Account Manager two-root overlap bundle changed')
        expected = {
            'PKI_MANAGEMENT_IDENTITY_ROOT_SHA256': ROOT,
            'PKI_MANAGEMENT_ISSUER_SERVER_PKI_ROOT_SHA256': OLD,
            'PKI_MANAGEMENT_CONTROLLER_SERVER_PKI_ROOT_SHA256': OLD,
            'PKI_MANAGEMENT_ACCOUNT_HOST_ROOT_SHA256': OLD,
            'PKI_MANAGEMENT_ACCOUNT_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256': OLD,
        }
        for key, value in expected.items():
            m.require(env.get(key, {}).get('value') == value, 'Account Manager transition baseline changed: ' + key)
        m.require(container['image'] != OWNER and container['image'].startswith('ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:'),
                  'Account Manager owner image baseline changed')
        env['PKI_MANAGEMENT_ISSUER_SERVER_PKI_ROOT_SHA256']['value'] = ROOT
        env['PKI_MANAGEMENT_ACCOUNT_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256']['value'] = ROOT
        container['image'] = OWNER
        template.setdefault('metadata', {}).setdefault('annotations', {})['rtk.cloud/r2-account-manager-final-client'] = self.output.name
        return template

    def persist_template(self, template):
        saved = m.read(self.base / PERSISTED)
        m.require(saved['metadata']['name'] == NAME and saved['metadata']['namespace'] == AM_NS,
                  'persisted Account Manager scope changed')
        persisted = copy.deepcopy(saved)
        live_container, _ = self.environment(template)
        persisted_container, persisted_env = self.environment(persisted['spec']['template'])
        persisted_container['image'] = live_container['image']
        for key in ('PKI_MANAGEMENT_ISSUER_SERVER_PKI_ROOT_SHA256',
                    'PKI_MANAGEMENT_ACCOUNT_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256'):
            m.require(key in persisted_env, 'persisted Account Manager setting missing: ' + key)
            persisted_env[key]['value'] = ROOT
        m.write(self.base / PERSISTED, persisted)
        m.write(self.base / 'operator/env' / OWNER_PIN, OWNER + '\n')

    def app_canary(self, label):
        args = ['/opt/homebrew/bin/python3', str(Path(__file__).with_name('account_callers.py')),
                '--database', str(self.args.app_database), '--email', self.args.app_email,
                '--owner-identity', str(self.args.app_owner_identity), '--output', str(self.output / label)]
        m.command(args, timeout=240)
        report = m.read(self.output / label / 'report.json')
        checks = report.get('checks', {})
        m.require(report.get('status') == 'passed' and checks.get('assigned_app_token', {}).get('status') == 'passed'
                  and checks.get('account_manager_internal_requests', {}).get('status') == 'passed',
                  'public App Account Manager canary failed')
        self.check(label, {'public_app_token': 'passed', 'account_manager_loopback_authorization': 'passed',
                           'private_key_exported': False})

    def rotate(self):
        self.preflight_retry()
        self.issuer()
        owner = self.obj('deployment', NAME, AM_NS)
        m.require(owner.get('status', {}).get('readyReplicas') == 1
                  and self.kube(['-n', AM_NS, 'exec', 'deployment/' + NAME, '-c', 'pkimanagement', '--', 'cat', '/proc/1/comm']).strip() == 'pkimanagement',
                  'Account Manager is not ready to receive one client-only renewal signal')
        before = {'client': self.state(), 'client_rows': self.rows('client'), 'server_rows': self.rows('server')}
        m.require(before['client_rows'] and before['server_rows'] and before['client_rows'][-1]['issuer_id'] != FINAL
                  and before['server_rows'][-1]['issuer_id'] != FINAL,
                  'Account Manager final client rotation baseline differs')
        self.save('baseline.json', before)
        template = self.transition_template(owner)
        self.observed_patch('deployment', NAME, owner, [
            {'op': 'test', 'path': '/spec/template', 'value': owner['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': template},
        ])
        self.kube(['-n', AM_NS, 'rollout', 'status', 'deployment/' + NAME, '--timeout=300s'], timeout=310)
        m.require(self.state() == before['client'] and self.rows('client') == before['client_rows']
                  and self.rows('server') == before['server_rows'],
                  'Account Manager image/trust preparation changed identity before renewal')
        self.persist_template(template)
        self.save('renewal-intent.json', {'previous_client': before['client']['fingerprint'], 'signal': 'SIGUSR1', 'at': m.stamp()})
        self.kube(['-n', AM_NS, 'exec', 'deployment/' + NAME, '-c', 'pkimanagement', '--', 'sh', '-c', 'kill -USR1 1'])
        deadline = time.monotonic() + 180
        while True:
            after = {'client': self.state(), 'client_rows': self.rows('client'), 'server_rows': self.rows('server')}
            added = [row for row in after['client_rows'] if row not in before['client_rows']]
            if after['client']['fingerprint'] != before['client']['fingerprint'] and len(added) == 1:
                break
            m.require(after['server_rows'] == before['server_rows'],
                      'Account Manager client-only renewal changed predecessor listener identity')
            m.require(time.monotonic() < deadline,
                      'Account Manager client-only renewal incomplete; retain evidence and do not signal again')
            time.sleep(2)
        m.require(added[0]['issuer_id'] == FINAL and added[0]['subject'] == SUBJECT and added[0]['caller'] == SUBJECT
                  and added[0]['status'] == 'succeeded' and added[0]['fingerprint'] == after['client']['fingerprint']
                  and after['server_rows'] == before['server_rows'], 'Account Manager final client registry result differs')
        self.save('renewed.json', after)
        factory.FactoryIdentityRun.factory_canary(self)
        self.device_baseline()
        self.app_canary('public-app-canary-before-restart')
        current = self.obj('deployment', NAME, AM_NS)
        restart = copy.deepcopy(current['spec']['template'])
        restart.setdefault('metadata', {}).setdefault('annotations', {})['rtk.cloud/r2-account-manager-final-client-restart'] = self.output.name
        self.observed_patch('deployment', NAME, current, [
            {'op': 'test', 'path': '/spec/template', 'value': current['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': restart},
        ])
        self.kube(['-n', AM_NS, 'rollout', 'status', 'deployment/' + NAME, '--timeout=300s'], timeout=310)
        m.require(self.state() == after['client'] and self.rows('client') == after['client_rows']
                  and self.rows('server') == before['server_rows'], 'Account Manager restart changed final client-only state')
        factory.FactoryIdentityRun.factory_canary(self)
        self.device_baseline()
        self.app_canary('public-app-canary-after-restart')
        self.check('account_manager_final_service_client', {
            'final_issuer_id': FINAL, 'client_request_id': added[0]['request_id'],
            'predecessor_listener_and_controller_retained_for_r4': True, 'restart_preserved_identity': True,
            'factory_device_and_app_canaries': 'passed', 'private_keys_exported': False, 'staging_touched': False})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--app-database', required=True, type=Path)
    parser.add_argument('--app-email', required=True)
    parser.add_argument('--app-owner-identity', required=True, type=Path)
    args = parser.parse_args(); args.phase = 'account-manager-final-service-client'; args.authority = None
    m.require(args.app_database.is_file() and args.app_owner_identity.is_dir()
              and (args.app_owner_identity / 'owner-chain.pem').is_file()
              and (args.app_owner_identity / 'owner-key.pem').is_file(),
              'retained Dev App canary inputs are incomplete')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600); fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = AccountManagerFinalClient(args)
    try:
        runner.rotate(); runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error); raise
    finally:
        runner.save('report.json', runner.report); runner.close(); os.close(fd)
        print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    try: main()
    except Exception as error: print(json.dumps({'error': str(error)}), file=sys.stderr); sys.exit(1)
