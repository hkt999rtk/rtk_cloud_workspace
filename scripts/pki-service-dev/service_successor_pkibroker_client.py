#!/usr/bin/env python3
"""Move the Dev pkibroker Service client to the final Service authority."""
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

NAME, CONTAINER, SUBJECT = 'mqtt-pki', 'pkibroker', 'service:pkibroker'
OLD_STATE = '/run/pki-state/broker-identity/private/identity.json'
NEW_STATE = '/run/pki-state/broker-identity/private/identity-root-697e8e86.json'
OLD = '87099089d30f13a7b93035b59c1c0c91bdb05bbe3dab427258c0c48e38144cc2'
ROOT = '32bbbfd220db619ebcf54af5f62221ed49635e58ddaa42e67730676f073704eb'
FINAL = 'd61845ca-6b85-4f11-920b-f2f9685b0c13'
ROOT_CONFIGMAP = 'pki-service-host-root-697e8e86-5af'
IMAGE = 'ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:bba34225ae40c47a3ce8444c61f5d568a87f700fa1b117e4a631e308dbf7ad58'


def env_map(container):
    values = {entry['name']: entry for entry in container.get('env', [])}
    m.require(len(values) == len(container.get('env', [])), 'duplicate pkibroker environment settings')
    return values


def update_env(container, updates, remove=()):
    container['env'] = [entry for entry in container['env'] if entry['name'] not in updates and entry['name'] not in remove]
    container['env'].extend({'name': key, 'value': value} for key, value in updates.items())


def broker_container(template):
    containers = template['spec']['containers']
    matches = [container for container in containers if container['name'] == CONTAINER]
    m.require(len(matches) == 1 and len(containers) == 2, 'mqtt-pki owner topology changed')
    return matches[0]


def service_root_volume(template, expected):
    matches = [volume for volume in template['spec']['volumes'] if volume['name'] == 'pki-service-root']
    m.require(len(matches) == 1 and matches[0].get('configMap', {}).get('name') == expected,
              'pkibroker Service Root mount changed')
    return matches[0]


def transition_template(owner, output):
    template = copy.deepcopy(owner['spec']['template'])
    container = broker_container(template)
    values = env_map(container)
    expected = {
        'PKI_BROKER_IDENTITY_STATE': OLD_STATE,
        'PKI_BROKER_ROOT_SHA256': OLD,
        'PKI_BROKER_IDENTITY_SERVER_PKI_ROOT_SHA256': OLD,
        'PKI_BROKER_IDENTITY_RENEWAL_SERVER_PKI_ROOT_SHA256': OLD,
    }
    m.require(all(values.get(key, {}).get('value') == value for key, value in expected.items()),
              'pkibroker predecessor transition baseline changed')
    m.require(not values.get('PKI_BROKER_IDENTITY_TRANSITION_FROM_STATE') and
              not values.get('PKI_BROKER_IDENTITY_TRANSITION_FROM_ROOT_SHA256'),
              'pkibroker transition is already configured; reconcile retained evidence')
    service_root_volume(template, 'pki-service-host-root')['configMap']['name'] = ROOT_CONFIGMAP
    container['image'] = IMAGE
    update_env(container, {
        'PKI_BROKER_IDENTITY_STATE': NEW_STATE,
        'PKI_BROKER_ROOT_SHA256': ROOT,
        'PKI_BROKER_IDENTITY_TRANSITION_FROM_STATE': OLD_STATE,
        'PKI_BROKER_IDENTITY_TRANSITION_FROM_ROOT_SHA256': OLD,
        # pki-controller remains on its predecessor server leaf until R4.
        'PKI_BROKER_IDENTITY_RENEWAL_SERVER_PKI_ROOT_SHA256': ROOT,
    })
    template.setdefault('metadata', {}).setdefault('annotations', {})['rtk.cloud/r2-pkibroker-final-client'] = output.name
    return template


def steady_template(owner, output):
    template = copy.deepcopy(owner['spec']['template'])
    container = broker_container(template)
    values = env_map(container)
    expected = {
        'PKI_BROKER_IDENTITY_STATE': NEW_STATE,
        'PKI_BROKER_ROOT_SHA256': ROOT,
        'PKI_BROKER_IDENTITY_TRANSITION_FROM_STATE': OLD_STATE,
        'PKI_BROKER_IDENTITY_TRANSITION_FROM_ROOT_SHA256': OLD,
        'PKI_BROKER_IDENTITY_SERVER_PKI_ROOT_SHA256': OLD,
        'PKI_BROKER_IDENTITY_RENEWAL_SERVER_PKI_ROOT_SHA256': ROOT,
    }
    m.require(all(values.get(key, {}).get('value') == value for key, value in expected.items()) and container['image'] == IMAGE,
              'pkibroker transition deployment changed')
    service_root_volume(template, ROOT_CONFIGMAP)
    update_env(container, {}, ('PKI_BROKER_IDENTITY_TRANSITION_FROM_STATE',
                               'PKI_BROKER_IDENTITY_TRANSITION_FROM_ROOT_SHA256'))
    template.setdefault('metadata', {}).setdefault('annotations', {})['rtk.cloud/r2-pkibroker-final-client-restart'] = output.name
    return template


class PkiBrokerFinalClient(r.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = ('Dev pkibroker final Service-client transition; '
                                           'MQTT host and pki-controller server remain predecessor-root for R4')
        self.report['service_successor_pkibroker_client_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def state(self, path, root):
        value = json.loads(self.kube(['-n', NS, 'exec', 'deployment/' + NAME, '-c', CONTAINER, '--',
                                      '/app/serviceidentity-bootstrap', 'inspect', path, SUBJECT, root]))
        m.require(value.get('subject') == SUBJECT and value.get('root_sha256') == root and not value.get('pending')
                  and re.fullmatch(r'[0-9a-f]{64}', value.get('fingerprint', ''))
                  and re.fullmatch(r'[0-9a-f]{64}', value.get('public_key_sha256', '')),
                  'pkibroker client state differs')
        return value

    def rows(self):
        query = ("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,subject,caller,status,fingerprint,issued_at "
                 "FROM pki_service_client_issuances WHERE environment='dev' AND subject='service:pkibroker' "
                 "AND revoked_at IS NULL ORDER BY issued_at,request_id) t;")
        raw = self.kube(['-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--', 'psql', '-X',
                         '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'video_cloud', '-At'], query)
        return [json.loads(line) for line in raw.splitlines() if line]

    @staticmethod
    def admission(rows, identity):
        matches = [row for row in rows if row['fingerprint'] == identity['fingerprint']]
        m.require(len(matches) == 1 and matches[0]['subject'] == SUBJECT and matches[0]['status'] == 'succeeded',
                  'pkibroker identity lacks exactly one active registry admission')
        return matches[0]

    @staticmethod
    def replacement(before, after, rows_before, rows_after):
        predecessor = PkiBrokerFinalClient.admission(rows_before, before)
        successor = PkiBrokerFinalClient.admission(rows_after, after)
        m.require(successor not in rows_before and predecessor['issuer_id'] != FINAL
                  and successor['issuer_id'] == FINAL and successor['caller'] == SUBJECT
                  and after['fingerprint'] != before['fingerprint'] and after['public_key_sha256'] != before['public_key_sha256'],
                  'pkibroker final client registry result differs')
        return successor

    def issuer(self):
        issuer = self.api('/issuers/' + FINAL)
        m.require(issuer['status'] == 'active' and issuer['environment'] == 'dev' and issuer['trust_domain'] == 'service'
                  and SUBJECT in issuer['service_client_ids'], 'final pkibroker client authority changed')
        self.api('/issuers/' + FINAL + '/crl')

    def finish(self, before, after, rows_before, rows_after, successor):
        factory.FactoryIdentityRun.factory_canary(self)
        self.device_baseline()
        current = self.obj('deployment', NAME)
        steady = steady_template(current, self.output)
        self.observed_patch('deployment', NAME, current, [
            {'op': 'test', 'path': '/spec/template', 'value': current['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': steady},
        ])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + NAME, '--timeout=300s'], timeout=310)
        m.require(self.state(NEW_STATE, ROOT) == after and self.rows() == rows_after,
                  'pkibroker restart changed final client state')
        factory.FactoryIdentityRun.factory_canary(self)
        self.device_baseline()
        self.check('pkibroker_final_service_client', {
            'final_issuer_id': FINAL, 'client_request_id': successor['request_id'],
            'mqtt_host_identity_changed': False, 'pki_controller_server_retained_for_r4': True,
            'restart_preserved_identity': True, 'factory_and_device_canaries': 'passed',
            'private_keys_exported': False, 'staging_touched': False,
        })

    def recover(self, source):
        saved, baseline = m.read(source / 'report.json'), m.read(source / 'baseline.json')
        m.require(saved.get('status') == 'failed' and baseline.get('identity') and baseline.get('rows'),
                  'failed pkibroker transition evidence required')
        super().preflight()
        self.issuer()
        before, rows_before = baseline['identity'], baseline['rows']
        after, rows_after = self.state(NEW_STATE, ROOT), self.rows()
        successor = self.replacement(before, after, rows_before, rows_after)
        owner = self.obj('deployment', NAME)
        container = broker_container(owner['spec']['template'])
        values = env_map(container)
        expected = {
            'PKI_BROKER_IDENTITY_STATE': NEW_STATE,
            'PKI_BROKER_ROOT_SHA256': ROOT,
            'PKI_BROKER_IDENTITY_TRANSITION_FROM_STATE': OLD_STATE,
            'PKI_BROKER_IDENTITY_TRANSITION_FROM_ROOT_SHA256': OLD,
            'PKI_BROKER_IDENTITY_SERVER_PKI_ROOT_SHA256': OLD,
            'PKI_BROKER_IDENTITY_RENEWAL_SERVER_PKI_ROOT_SHA256': ROOT,
        }
        m.require(all(values.get(key, {}).get('value') == value for key, value in expected.items()) and container['image'] == IMAGE,
                  'pkibroker retained transition settings changed')
        service_root_volume(owner['spec']['template'], ROOT_CONFIGMAP)
        self.save('baseline.json', {'identity': before, 'rows': rows_before, 'recovered_from': str(source)})
        self.save('renewed.json', {'identity': after, 'rows': rows_after, 'request_id': successor['request_id'],
                                   'recovered_from': str(source)})
        self.finish(before, after, rows_before, rows_after, successor)

    def verify(self, source):
        saved, renewed = m.read(source / 'report.json'), m.read(source / 'renewed.json')
        m.require(saved.get('status') == 'failed' and renewed.get('identity') and renewed.get('rows'),
                  'failed post-restart pkibroker evidence required')
        super().preflight()
        self.issuer()
        after, rows_after = self.state(NEW_STATE, ROOT), self.rows()
        m.require(after == renewed['identity'] and rows_after == renewed['rows'],
                  'pkibroker final identity or registry changed after failed verification')
        owner = self.obj('deployment', NAME)
        container = broker_container(owner['spec']['template'])
        values = env_map(container)
        expected = {
            'PKI_BROKER_IDENTITY_STATE': NEW_STATE,
            'PKI_BROKER_ROOT_SHA256': ROOT,
            'PKI_BROKER_IDENTITY_SERVER_PKI_ROOT_SHA256': OLD,
            'PKI_BROKER_IDENTITY_RENEWAL_SERVER_PKI_ROOT_SHA256': ROOT,
        }
        m.require(all(values.get(key, {}).get('value') == value for key, value in expected.items())
                  and not values.get('PKI_BROKER_IDENTITY_TRANSITION_FROM_STATE')
                  and not values.get('PKI_BROKER_IDENTITY_TRANSITION_FROM_ROOT_SHA256')
                  and container['image'] == IMAGE,
                  'pkibroker final transition settings changed')
        service_root_volume(owner['spec']['template'], ROOT_CONFIGMAP)
        factory.FactoryIdentityRun.factory_canary(self)
        self.device_baseline()
        self.check('pkibroker_final_service_client_recovered', {
            'final_issuer_id': FINAL, 'client_request_id': renewed['request_id'],
            'restart_preserved_identity': True, 'factory_and_device_canaries': 'passed',
            'verification_source': str(source), 'private_keys_exported': False, 'staging_touched': False,
        })

    def rotate(self):
        super().preflight()
        self.issuer()
        bundle = self.obj('configmap', ROOT_CONFIGMAP)
        m.require(bundle.get('immutable') and bundle.get('data', {}).get('root.pem', '').count('-----BEGIN CERTIFICATE-----') == 2,
                  'reviewed pkibroker two-root overlap bundle changed')
        owner = self.obj('deployment', NAME)
        m.require(owner.get('status', {}).get('readyReplicas') == 1, 'mqtt-pki is not ready')
        before, rows_before = self.state(OLD_STATE, OLD), self.rows()
        m.require(self.admission(rows_before, before)['issuer_id'] != FINAL, 'pkibroker predecessor admission differs')
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
                m.require(time.monotonic() < deadline, 'pkibroker final client transition incomplete; retain evidence')
                time.sleep(2)
        self.save('renewed.json', {'identity': after, 'rows': rows_after, 'request_id': successor['request_id']})
        self.finish(before, after, rows_before, rows_after, successor)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--recover-from', type=Path, help='Failed transition evidence after a completed successor issuance')
    parser.add_argument('--verify-from', type=Path, help='Failed post-restart evidence; verifies final state without mutation')
    args = parser.parse_args(); args.phase = 'pkibroker-final-service-client'; args.authority = None
    os.umask(0o077)
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600); fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = PkiBrokerFinalClient(args)
    try:
        m.require(not (args.recover_from and args.verify_from), 'select one recovery mode')
        if args.verify_from:
            runner.verify(args.verify_from)
        elif args.recover_from:
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
