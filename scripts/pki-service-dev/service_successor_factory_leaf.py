#!/usr/bin/env python3
"""Rotate the Dev Factory Service client to the active final authority.

The Factory workload retains its predecessor state on its PVC and creates a
separate successor-root state.  The first request is an authenticated renewal,
not a new bootstrap enrollment.  This runner never copies or reads either
private state; it records only the public inspect result and issuance metadata.
"""
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
r = importlib.util.module_from_spec(spec)
spec.loader.exec_module(r)
m, NS = r.m, r.NS

factory_spec = importlib.util.spec_from_file_location('factory_identity', Path(__file__).with_name('factory_identity.py'))
factory = importlib.util.module_from_spec(factory_spec)
factory_spec.loader.exec_module(factory)

SUBJECT = 'service:factory-enroll'
NAME = 'factoryenroll'
CONTAINER = 'factoryenroll'
OLD_STATE = '/state/identity/client.json'
NEW_STATE = '/state/identity/client-root-697e8e86.json'
OLD_ROOT = '87099089d30f13a7b93035b59c1c0c91bdb05bbe3dab427258c0c48e38144cc2'
NEW_ROOT = '32bbbfd220db619ebcf54af5f62221ed49635e58ddaa42e67730676f073704eb'
FINAL_ISSUER = 'd61845ca-6b85-4f11-920b-f2f9685b0c13'
IMAGE = re.compile(r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}')


def env_map(container):
    values = {item['name']: item.get('value') for item in container.get('env', [])}
    m.require(len(values) == len(container.get('env', [])), 'duplicate Factory environment setting')
    return values


def with_env(entries, updates, remove=()):
    return [item for item in entries if item['name'] not in updates and item['name'] not in remove] + [
        {'name': name, 'value': value} for name, value in updates.items()]


def transition_template(owner, image):
    template = copy.deepcopy(owner['spec']['template'])
    containers = template['spec'].get('containers', [])
    m.require(owner['spec'].get('replicas') == 1 and len(containers) == 1 and containers[0].get('name') == CONTAINER,
              'Factory deployment ownership changed')
    container = containers[0]
    values = env_map(container)
    required = {
        'FACTORY_ENROLL_SERVICE_IDENTITY_STATE': OLD_STATE,
        'FACTORY_ENROLL_SERVICE_IDENTITY_ROOT_SHA256': OLD_ROOT,
        'FACTORY_ENROLL_CERT_ISSUER_SERVER_PKI_ROOT_SHA256': OLD_ROOT,
        'FACTORY_ENROLL_ACCOUNT_MANAGER_SERVER_PKI_ROOT_SHA256': OLD_ROOT,
        'FACTORY_ENROLL_CERT_ISSUER_SERVICE_ROOT_ID': '697e8e86-5af6-4580-8456-7f91d17634f2',
        'FACTORY_ENROLL_ACCOUNT_MANAGER_SERVICE_ROOT_ID': '697e8e86-5af6-4580-8456-7f91d17634f2',
    }
    m.require(all(values.get(key) == value for key, value in required.items()), 'Factory predecessor baseline changed')
    m.require(not values.get('FACTORY_ENROLL_SERVICE_IDENTITY_TRANSITION_FROM_STATE') and
              not values.get('FACTORY_ENROLL_SERVICE_IDENTITY_TRANSITION_FROM_ROOT_SHA256'),
              'Factory transition is already configured; reconcile retained evidence')
    m.require(IMAGE.fullmatch(image), 'immutable Dev video-cloud image required')
    container['image'] = image
    container['env'] = with_env(container['env'], {
        'FACTORY_ENROLL_SERVICE_IDENTITY_STATE': NEW_STATE,
        'FACTORY_ENROLL_SERVICE_IDENTITY_ROOT_SHA256': NEW_ROOT,
        'FACTORY_ENROLL_SERVICE_IDENTITY_TRANSITION_FROM_STATE': OLD_STATE,
        'FACTORY_ENROLL_SERVICE_IDENTITY_TRANSITION_FROM_ROOT_SHA256': OLD_ROOT,
        'FACTORY_ENROLL_CERT_ISSUER_SERVER_PKI_ROOT_SHA256': NEW_ROOT,
        'FACTORY_ENROLL_ACCOUNT_MANAGER_SERVER_PKI_ROOT_SHA256': NEW_ROOT,
    })
    template.setdefault('metadata', {}).setdefault('annotations', {})['rtk.realtek.com/service-final-leaf'] = FINAL_ISSUER
    return template


def steady_template(owner, image):
    template = copy.deepcopy(owner['spec']['template'])
    containers = template['spec'].get('containers', [])
    m.require(len(containers) == 1 and containers[0].get('name') == CONTAINER, 'Factory deployment ownership changed')
    values = env_map(containers[0])
    required = {
        'FACTORY_ENROLL_SERVICE_IDENTITY_STATE': NEW_STATE,
        'FACTORY_ENROLL_SERVICE_IDENTITY_ROOT_SHA256': NEW_ROOT,
        'FACTORY_ENROLL_SERVICE_IDENTITY_TRANSITION_FROM_STATE': OLD_STATE,
        'FACTORY_ENROLL_SERVICE_IDENTITY_TRANSITION_FROM_ROOT_SHA256': OLD_ROOT,
        'FACTORY_ENROLL_CERT_ISSUER_SERVER_PKI_ROOT_SHA256': NEW_ROOT,
        'FACTORY_ENROLL_ACCOUNT_MANAGER_SERVER_PKI_ROOT_SHA256': NEW_ROOT,
    }
    m.require(all(values.get(key) == value for key, value in required.items()), 'Factory transition deployment changed')
    m.require(containers[0].get('image') == image, 'Factory image changed during transition')
    containers[0]['env'] = with_env(containers[0]['env'], {}, (
        'FACTORY_ENROLL_SERVICE_IDENTITY_TRANSITION_FROM_STATE',
        'FACTORY_ENROLL_SERVICE_IDENTITY_TRANSITION_FROM_ROOT_SHA256'))
    return template


class FactoryFinalLeaf(r.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = 'Dev Factory final Service-authority leaf rotation; no withdrawal or revocation'
        self.report['service_successor_factory_leaf_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def api(self, path, body=None, *args, **kwargs):
        if body is not None:
            return super().api(path, body, *args, **kwargs)
        for attempt in range(3):
            try:
                return super().api(path, None, *args, **kwargs)
            except RuntimeError as error:
                if 'status 503' not in str(error) or attempt == 2:
                    raise
                time.sleep(attempt + 1)

    def issuer(self):
        issuer = self.api('/issuers/' + FINAL_ISSUER)
        m.require(issuer['environment'] == 'dev' and issuer['trust_domain'] == 'service' and issuer['kind'] == 'intermediate' and
                  issuer['status'] == 'active' and issuer['parent_issuer_id'] == '697e8e86-5af6-4580-8456-7f91d17634f2' and
                  SUBJECT in issuer['service_client_ids'], 'active final Service issuer changed')
        crl = self.api('/issuers/' + FINAL_ISSUER + '/crl')
        m.require(crl.get('issuer_id') == FINAL_ISSUER and re.fullmatch(r'[0-9a-f]{64}', crl.get('crl_sha256', '')),
                  'final Service issuer CRL is unavailable')
        self.save('final-issuer.json', issuer)
        self.save('final-issuer-crl.json', crl)
        return issuer

    def preflight(self):
        # The common Dev preflight loads the existing disposable Factory canary
        # context and performs only read-only ownership and readiness checks.
        super().preflight()
        m.require(self.kube(['config', 'current-context']).strip() == self.context, 'canonical Dev context mismatch')
        owner = self.obj('deployment', NAME)
        m.require(owner.get('status', {}).get('readyReplicas') == 1 and owner.get('status', {}).get('updatedReplicas') == 1 and
                  owner.get('status', {}).get('observedGeneration') == owner['metadata'].get('generation'), 'Factory is not ready')
        target = self.kube(['-n', NS, 'exec', 'deployment/' + NAME, '--', 'sh', '-c',
                            'test ! -e ' + NEW_STATE + ' && stat -c %a ' + OLD_STATE]).strip()
        m.require(target == '600', 'Factory successor state exists or predecessor state permissions changed')
        self.issuer()
        self.save('factory-before.json', owner)
        self.check('factory_final_leaf_preflight', {'final_issuer_id': FINAL_ISSUER, 'staging_touched': False,
                                                     'predecessor_state_retained': True, 'successor_state_present': False})
        return owner

    def rollout_template(self, owner, template, label):
        self.save(label + '-template.json', template)
        self.observed_patch('deployment', NAME, owner, [
            {'op': 'test', 'path': '/spec/template', 'value': owner['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': template},
        ])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + NAME, '--timeout=300s'], timeout=310)

    def inspect(self):
        raw = self.kube(['-n', NS, 'exec', 'deployment/' + NAME, '--', '/app/serviceidentity-bootstrap', 'inspect',
                         NEW_STATE, SUBJECT, NEW_ROOT])
        state = json.loads(raw)
        m.require(state.get('subject') == SUBJECT and state.get('root_sha256') == NEW_ROOT and state.get('pending') is False and
                  re.fullmatch(r'[0-9a-f]{64}', state.get('fingerprint', '')) and re.fullmatch(r'[0-9a-f]{64}', state.get('state_sha256', '')),
                  'Factory successor identity inspection differs')
        return state

    def issuance(self, fingerprint):
        query = ("SELECT issuer_id,subject,caller,status,fingerprint FROM pki_service_client_issuances "
                 "WHERE environment='dev' AND subject='service:factory-enroll' ORDER BY issued_at DESC,request_id DESC LIMIT 1;")
        row = self.kube(['-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--', 'psql', '-X', '-v', 'ON_ERROR_STOP=1',
                         '-U', 'postgres', '-d', 'video_cloud', '-At', '-F', '|'], query).strip().split('|')
        m.require(len(row) == 5 and row[0] == FINAL_ISSUER and row[1] == SUBJECT and row[3] == 'succeeded' and row[4] == fingerprint,
                  'Factory final Service issuance differs')
        result = dict(zip(('issuer_id', 'subject', 'caller', 'status', 'fingerprint'), row))
        self.save('factory-final-issuance.json', result)
        return result

    def pending_claim(self, request_id):
        m.require(re.fullmatch(r'[0-9a-f-]{36}', request_id), 'canonical pending Factory request ID required')
        query = ("SELECT issuer_id,subject,caller,status,fingerprint FROM pki_service_client_issuances "
                 "WHERE environment='dev' AND request_id='" + request_id + "' LIMIT 1;")
        row = self.kube(['-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--', 'psql', '-X', '-v', 'ON_ERROR_STOP=1',
                         '-U', 'postgres', '-d', 'video_cloud', '-At', '-F', '|'], query).strip().split('|')
        m.require(len(row) == 5 and row[0] == FINAL_ISSUER and row[1] == SUBJECT and row[2] == SUBJECT and
                  row[3] in ('issuing', 'succeeded'), 'Factory pending claim differs')
        return dict(zip(('issuer_id', 'subject', 'caller', 'status', 'fingerprint'), row))

    def complete_pending(self, request_id):
        # The controller discovers an already signed result by the original
        # stored CSR.  It has no signing input and cannot replace the claim.
        self.forward('am', 'video-cloud-dev-account-manager', 'account-manager', 80)
        self.accounts = m.read(self.foundation / 'accounts.json')
        self.issuer()
        pending = self.pending_claim(request_id)
        self.save('factory-pending-before.json', pending)
        if pending['status'] == 'issuing':
            self.api('/issuers/' + FINAL_ISSUER + '/reconcile-service-client', {
                'caller': pending['caller'], 'request_id': request_id}, role='approver')
            pending = self.pending_claim(request_id)
        m.require(pending['status'] == 'succeeded' and re.fullmatch(r'[0-9a-f]{64}', pending['fingerprint']),
                  'Factory pending claim was not recovered from an existing provider certificate')
        self.save('factory-pending-reconciled.json', pending)
        self.kube(['-n', NS, 'rollout', 'restart', 'deployment/' + NAME])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + NAME, '--timeout=300s'], timeout=310)
        super().preflight()
        identity = self.inspect()
        m.require(identity['fingerprint'] == pending['fingerprint'], 'Factory did not install recovered final leaf')
        self.save('factory-transition-identity.json', identity)
        self.issuance(identity['fingerprint'])
        factory.FactoryIdentityRun.factory_canary(self)
        current = self.obj('deployment', NAME)
        self.rollout_template(current, steady_template(current, self.args.image), 'factory-steady')
        m.require(self.inspect() == identity, 'Factory restart changed recovered final Service identity')
        factory.FactoryIdentityRun.factory_canary(self)
        self.check('factory_final_service_leaf_recovered', {
            'request_id': request_id, 'final_issuer_id': FINAL_ISSUER,
            'provider_discovery_only': True, 'bootstrap_transition_removed': True,
            'restart_preserved_identity': True, 'factory_canary': 'passed',
            'private_keys_exported': False, 'predecessor_revocation': 'deferred to R2 retirement'})

    def rotate(self):
        before = self.preflight()
        self.rollout_template(before, transition_template(before, self.args.image), 'factory-transition')
        identity = self.inspect()
        issuance = self.issuance(identity['fingerprint'])
        self.save('factory-transition-identity.json', identity)
        factory.FactoryIdentityRun.factory_canary(self)
        current = self.obj('deployment', NAME)
        self.rollout_template(current, steady_template(current, self.args.image), 'factory-steady')
        m.require(self.inspect() == identity, 'Factory restart changed final Service identity')
        factory.FactoryIdentityRun.factory_canary(self)
        self.check('factory_final_service_leaf', {'final_issuer_id': FINAL_ISSUER, 'issuance': issuance,
                                                   'bootstrap_transition_removed': True, 'restart_preserved_identity': True,
                                                   'factory_canary': 'passed', 'private_keys_exported': False,
                                                   'predecessor_revocation': 'deferred to R2 retirement'})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--image', required=True)
    parser.add_argument('--recover-request-id')
    args = parser.parse_args()
    args.phase, args.authority = 'factory-final-service-leaf', None
    m.require(IMAGE.fullmatch(args.image), 'immutable Dev video-cloud image digest required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = FactoryFinalLeaf(args)
    try:
        if args.recover_request_id:
            runner.complete_pending(args.recover_request_id)
        else:
            runner.rotate()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        os.close(fd)
        print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
