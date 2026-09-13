#!/usr/bin/env python3
"""Move the isolated Dev API controller Service client to the final authority."""
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

NAME, SUBJECT = 'video-cloud-api-pki', 'service:video-cloud-api'
OLD_STATE = '/var/lib/video-cloud-api-pki-controller-identity/private/identity.json'
NEW_STATE = '/var/lib/video-cloud-api-pki-controller-identity/private/identity-root-697e8e86.json'
OLD = '87099089d30f13a7b93035b59c1c0c91bdb05bbe3dab427258c0c48e38144cc2'
ROOT = '32bbbfd220db619ebcf54af5f62221ed49635e58ddaa42e67730676f073704eb'
FINAL = 'd61845ca-6b85-4f11-920b-f2f9685b0c13'
IMAGE = 'ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:bba34225ae40c47a3ce8444c61f5d568a87f700fa1b117e4a631e308dbf7ad58'
ROOT_CONFIGMAP = 'pki-service-host-root-697e8e86-5af'


def env_map(container):
    values = {entry['name']: entry for entry in container.get('env', [])}
    m.require(len(values) == len(container.get('env', [])), 'duplicate environment settings')
    return values


def update_env(container, updates, remove=()):
    container['env'] = [entry for entry in container['env'] if entry['name'] not in updates and entry['name'] not in remove]
    container['env'].extend({'name': key, 'value': value} for key, value in updates.items())


def service_root_volume(template, expected):
    matches = [volume for volume in template['spec']['volumes'] if volume['name'] == 'service-root']
    m.require(len(matches) == 1 and matches[0].get('configMap', {}).get('name') == expected,
              'isolated API Service Root mount changed')
    return matches[0]


def transition_template(owner, output):
    template = copy.deepcopy(owner['spec']['template'])
    containers = template['spec']['containers']
    m.require(len(containers) == 1 and containers[0]['name'] == 'app', 'isolated API owner topology changed')
    container = containers[0]
    values = env_map(container)
    expected = {
        'VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE': OLD_STATE,
        'VIDEO_CLOUD_CONTROLLER_IDENTITY_ROOT_SHA256': OLD,
        'VIDEO_CLOUD_CONTROLLER_IDENTITY_SERVER_PKI_ROOT_SHA256': OLD,
        'VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_SERVER_PKI_ROOT_SHA256': OLD,
    }
    m.require(all(values.get(key, {}).get('value') == value for key, value in expected.items()),
              'isolated API predecessor transition baseline changed')
    m.require(not values.get('VIDEO_CLOUD_CONTROLLER_IDENTITY_TRANSITION_FROM_STATE') and
              not values.get('VIDEO_CLOUD_CONTROLLER_IDENTITY_TRANSITION_FROM_ROOT_SHA256'),
              'isolated API transition is already configured; reconcile retained evidence')
    service_root_volume(template, 'pki-service-host-root')['configMap']['name'] = ROOT_CONFIGMAP
    container['image'] = IMAGE
    update_env(container, {
        'VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE': NEW_STATE,
        'VIDEO_CLOUD_CONTROLLER_IDENTITY_ROOT_SHA256': ROOT,
        'VIDEO_CLOUD_CONTROLLER_IDENTITY_TRANSITION_FROM_STATE': OLD_STATE,
        'VIDEO_CLOUD_CONTROLLER_IDENTITY_TRANSITION_FROM_ROOT_SHA256': OLD,
        # pki-controller retains its predecessor server leaf until R4.
        'VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_SERVER_PKI_ROOT_SHA256': ROOT,
    })
    template.setdefault('metadata', {}).setdefault('annotations', {})['rtk.cloud/r2-api-controller-final-client'] = output.name
    return template


def steady_template(owner, output):
    template = copy.deepcopy(owner['spec']['template'])
    container = template['spec']['containers'][0]
    values = env_map(container)
    expected = {
        'VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE': NEW_STATE,
        'VIDEO_CLOUD_CONTROLLER_IDENTITY_ROOT_SHA256': ROOT,
        'VIDEO_CLOUD_CONTROLLER_IDENTITY_TRANSITION_FROM_STATE': OLD_STATE,
        'VIDEO_CLOUD_CONTROLLER_IDENTITY_TRANSITION_FROM_ROOT_SHA256': OLD,
        'VIDEO_CLOUD_CONTROLLER_IDENTITY_SERVER_PKI_ROOT_SHA256': OLD,
        'VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_SERVER_PKI_ROOT_SHA256': ROOT,
    }
    m.require(all(values.get(key, {}).get('value') == value for key, value in expected.items()) and container['image'] == IMAGE,
              'isolated API transition deployment changed')
    service_root_volume(template, ROOT_CONFIGMAP)
    update_env(container, {}, ('VIDEO_CLOUD_CONTROLLER_IDENTITY_TRANSITION_FROM_STATE',
                               'VIDEO_CLOUD_CONTROLLER_IDENTITY_TRANSITION_FROM_ROOT_SHA256'))
    template.setdefault('metadata', {}).setdefault('annotations', {})['rtk.cloud/r2-api-controller-final-client-restart'] = output.name
    return template


class APIControllerFinalClient(r.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = ('Dev isolated API controller final Service-client transition; '
                                           'pki-controller server remains predecessor-root for R4')
        self.report['service_successor_api_controller_client_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def state(self, path, root):
        value = json.loads(self.kube(['-n', NS, 'exec', 'deployment/' + NAME, '--',
                                      '/app/serviceidentity-bootstrap', 'inspect', path, SUBJECT, root]))
        m.require(value.get('subject') == SUBJECT and value.get('root_sha256') == root and not value.get('pending')
                  and re.fullmatch(r'[0-9a-f]{64}', value.get('fingerprint', ''))
                  and re.fullmatch(r'[0-9a-f]{64}', value.get('public_key_sha256', '')),
                  'isolated API controller identity state differs')
        return value

    def rows(self):
        query = ("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,subject,caller,status,fingerprint,issued_at "
                 "FROM pki_service_client_issuances WHERE environment='dev' AND subject='service:video-cloud-api' "
                 "AND revoked_at IS NULL ORDER BY issued_at,request_id) t;")
        raw = self.kube(['-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--', 'psql', '-X',
                         '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'video_cloud', '-At'], query)
        return [json.loads(line) for line in raw.splitlines() if line]

    @staticmethod
    def admission(rows, identity):
        matches = [row for row in rows if row['fingerprint'] == identity['fingerprint']]
        m.require(len(matches) == 1 and matches[0]['subject'] == SUBJECT and matches[0]['status'] == 'succeeded',
                  'isolated API identity lacks exactly one active registry admission')
        return matches[0]

    def issuer(self):
        issuer = self.api('/issuers/' + FINAL)
        m.require(issuer['status'] == 'active' and issuer['environment'] == 'dev' and issuer['trust_domain'] == 'service'
                  and SUBJECT in issuer['service_client_ids'], 'final isolated API authority changed')
        self.api('/issuers/' + FINAL + '/crl')
        certissuer = self.obj('deployment', 'certissuer')
        values = env_map(certissuer['spec']['template']['spec']['containers'][0])
        m.require(values.get('CERT_ISSUER_SERVICE_CLIENT_VERIFY_ROOT_SHA256', {}).get('value') == OLD
                  and values.get('CERT_ISSUER_SERVICE_CLIENT_ISSUER_ROOT_SHA256', {}).get('value') == ROOT,
                  'CertIssuer must retain predecessor verification for remaining R2 callers')

    def control_plane(self):
        # Recovery can repair this workload before it is ready, so it cannot use
        # the full preflight yet. Only initialize the authenticated management
        # client needed to read the final issuer and preserve the saved state.
        m.require(self.kube(['config', 'current-context']).strip() == self.context, 'canonical dev context mismatch')
        self.forward('am', 'video-cloud-dev-account-manager', 'account-manager', 80)
        self.accounts = m.read(self.foundation / 'accounts.json')

    def replacement(self, before, after, rows_before, rows_after):
        predecessor = self.admission(rows_before, before)
        successor = self.admission(rows_after, after)
        m.require(successor not in rows_before and predecessor['issuer_id'] != FINAL
                  and successor['issuer_id'] == FINAL and successor['caller'] == SUBJECT
                  and after['fingerprint'] != before['fingerprint'] and after['public_key_sha256'] != before['public_key_sha256'],
                  'isolated API final client registry result differs')
        return successor

    def finish(self, after, rows_after, successor):
        factory.FactoryIdentityRun.factory_canary(self)
        self.device_baseline()
        current = self.obj('deployment', NAME)
        template = steady_template(current, self.output)
        self.observed_patch('deployment', NAME, current, [
            {'op': 'test', 'path': '/spec/template', 'value': current['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': template},
        ])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + NAME, '--timeout=300s'], timeout=310)
        m.require(self.state(NEW_STATE, ROOT) == after and self.rows() == rows_after,
                  'isolated API restart changed final client state')
        factory.FactoryIdentityRun.factory_canary(self)
        self.device_baseline()
        self.check('api_controller_final_service_client', {
            'final_issuer_id': FINAL, 'client_request_id': successor['request_id'],
            'predecessor_pki_controller_server_retained_for_r4': True,
            'certissuer_predecessor_verifier_retained_for_remaining_r2_clients': True,
            'restart_preserved_identity': True, 'factory_and_device_canaries': 'passed',
            'private_keys_exported': False, 'staging_touched': False,
        })

    def recover(self, source):
        saved = m.read(source / 'report.json')
        baseline = m.read(source / 'baseline.json')
        m.require(saved.get('status') in ('failed', 'interrupted') and baseline.get('identity') and baseline.get('rows'),
                  'interrupted isolated API transition evidence required')
        self.control_plane()
        self.issuer()
        before, rows_before = baseline['identity'], baseline['rows']
        m.require(self.rows() == rows_before, 'isolated API registry changed before startup recovery')
        owner = self.obj('deployment', NAME)
        values = env_map(owner['spec']['template']['spec']['containers'][0])
        m.require(values.get('VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE', {}).get('value') == NEW_STATE
                  and values.get('VIDEO_CLOUD_CONTROLLER_IDENTITY_ROOT_SHA256', {}).get('value') == ROOT
                  and values.get('VIDEO_CLOUD_CONTROLLER_IDENTITY_TRANSITION_FROM_STATE', {}).get('value') == OLD_STATE
                  and values.get('VIDEO_CLOUD_CONTROLLER_IDENTITY_TRANSITION_FROM_ROOT_SHA256', {}).get('value') == OLD,
                  'isolated API transition owner changed during recovery')
        template = copy.deepcopy(owner['spec']['template'])
        service_root_volume(template, 'pki-service-host-root')['configMap']['name'] = ROOT_CONFIGMAP
        template.setdefault('metadata', {}).setdefault('annotations', {})['rtk.cloud/r2-api-controller-final-client-recovery'] = self.output.name
        self.observed_patch('deployment', NAME, owner, [
            {'op': 'test', 'path': '/spec/template', 'value': owner['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': template},
        ])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + NAME, '--timeout=300s'], timeout=310)
        deadline = time.monotonic() + 180
        while True:
            try:
                after, rows_after = self.state(NEW_STATE, ROOT), self.rows()
                successor = self.replacement(before, after, rows_before, rows_after)
                break
            except RuntimeError:
                m.require(time.monotonic() < deadline, 'isolated API final client recovery incomplete; retain evidence')
                time.sleep(2)
        self.save('baseline.json', {'identity': before, 'rows': rows_before, 'recovered_from': str(source)})
        self.save('renewed.json', {'identity': after, 'rows': rows_after, 'request_id': successor['request_id'],
                                   'recovered_from': str(source)})
        super().preflight()
        self.finish(after, rows_after, successor)

    def rotate(self):
        super().preflight()
        self.issuer()
        owner = self.obj('deployment', NAME)
        m.require(owner.get('status', {}).get('readyReplicas') == 1, 'isolated API is not ready')
        before, rows_before = self.state(OLD_STATE, OLD), self.rows()
        m.require(self.admission(rows_before, before)['issuer_id'] != FINAL, 'isolated API predecessor admission differs')
        self.save('baseline.json', {'identity': before, 'rows': rows_before})
        template = transition_template(owner, self.output)
        self.observed_patch('deployment', NAME, owner, [
            {'op': 'test', 'path': '/spec/template', 'value': owner['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': template},
        ])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + NAME, '--timeout=300s'], timeout=310)
        deadline = time.monotonic() + 180
        while True:
            try:
                after, rows_after = self.state(NEW_STATE, ROOT), self.rows()
                successor = self.replacement(before, after, rows_before, rows_after)
                break
            except RuntimeError:
                m.require(time.monotonic() < deadline, 'isolated API final client transition incomplete; retain evidence')
                time.sleep(2)
        self.save('renewed.json', {'identity': after, 'rows': rows_after, 'request_id': successor['request_id']})
        self.finish(after, rows_after, successor)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--recover-from', type=Path, help='Interrupted pre-issuance evidence; resumes without a renewal signal')
    args = parser.parse_args(); args.phase = 'api-controller-final-service-client'; args.authority = None
    os.umask(0o077)
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600); fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = APIControllerFinalClient(args)
    try:
        if args.recover_from:
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
