#!/usr/bin/env python3
"""Adopt the durable Service Root policy for the Dev Factory issuer caller.

The caller keeps its managed Service identity on its existing PVC.  Before the
single-pod rollout, this runner seeds only the verified *public* root-policy
state through that already healthy caller.  It never reads or copies a private
key.  The new process must acknowledge the policy and complete a real Factory
enrollment canary before this rollout is considered successful.
"""
import argparse
import base64
import copy
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import time

spec = importlib.util.spec_from_file_location('service_hierarchy', Path(__file__).with_name('run.py'))
s = importlib.util.module_from_spec(spec)
spec.loader.exec_module(s)
m, NS = s.m, s.NS

NAME = 'factoryenroll'
CONTAINER = 'factoryenroll'
CONSUMER = 'factory-enroll'
PREFIX = 'FACTORY_ENROLL_CERT_ISSUER'
STATE = '/state/identity/issuer-root-policy.json'
ROOTS = '/run/service-root/root.pem'
IMAGE = re.compile(r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}')


def env_map(container):
    values = {entry['name']: entry.get('value') for entry in container.get('env', [])}
    m.require(len(values) == len(container.get('env', [])), 'duplicate Factory environment setting')
    return values


def with_env(entries, updates):
    return [entry for entry in entries if entry['name'] not in updates] + [
        {'name': key, 'value': value} for key, value in updates.items()]


def installed_policy_sha(state):
    policy = state.get('policy') if isinstance(state, dict) else None
    value = policy.get('policy_sha256') if isinstance(policy, dict) else None
    m.require(isinstance(value, str) and re.fullmatch(r'[0-9a-f]{64}', value),
              'persisted Factory Service Root policy state is invalid')
    return value


def caller_template(owner, root, image=None):
    """Return the exact Factory caller patch without changing its identity."""
    template = copy.deepcopy(owner['spec']['template'])
    pod = template['spec']
    m.require(owner['spec'].get('replicas') == 1, 'one Dev Factory caller required')
    containers = pod.get('containers', [])
    m.require(len(containers) == 1 and containers[0].get('name') == CONTAINER,
              'Factory caller ownership changed')
    container = containers[0]
    values = env_map(container)
    baseline = (PREFIX + '_URL', PREFIX + '_SERVER_PKI_ROOT_SHA256', PREFIX + '_SERVER_PKI_NAME',
                PREFIX + '_TLS_CA', PREFIX + '_SERVER_CRL_MANIFEST', PREFIX + '_PKI_CONTROLLER_URL',
                PREFIX + '_MANAGEMENT_CA', 'FACTORY_ENROLL_SERVICE_IDENTITY_STATE',
                'FACTORY_ENROLL_SERVICE_IDENTITY_ROOT_SHA256')
    m.require(all(values.get(name) for name in baseline), 'Factory issuer caller baseline is incomplete')
    m.require(values[PREFIX + '_SERVER_PKI_ROOT_SHA256'] == root['certificate_fingerprint_sha256']
              and values['FACTORY_ENROLL_SERVICE_IDENTITY_ROOT_SHA256'] == root['certificate_fingerprint_sha256'],
              'Factory issuer caller root baseline differs')
    mounts = {entry['name']: entry for entry in container.get('volumeMounts', [])}
    volumes = {entry['name']: entry for entry in pod.get('volumes', [])}
    m.require(mounts.get('identity', {}).get('mountPath') == '/state'
              and mounts.get('service-root', {}).get('mountPath') == '/run/service-root'
              and volumes.get('identity', {}).get('persistentVolumeClaim', {}).get('claimName') == 'factoryenroll-service-identity'
              and volumes.get('service-root', {}).get('configMap', {}).get('name') == 'pki-service-host-root',
              'Factory policy state ownership changed')
    settings = {PREFIX + '_SERVICE_ROOT_ID': root['issuer_id'],
                PREFIX + '_SERVICE_ROOT_STATE': STATE,
                PREFIX + '_SERVICE_ROOTS': ROOTS}
    configured = [values.get(key) is not None for key in settings]
    if any(configured):
        m.require(all(configured) and all(values[key] == value for key, value in settings.items()),
                  'existing Factory Service Root policy differs; reconcile manually')
    else:
        container['env'] = with_env(container['env'], settings)
    if image:
        m.require(IMAGE.fullmatch(image), 'immutable Dev Video Cloud image required')
        container['image'] = image
    template.setdefault('metadata', {}).setdefault('annotations', {})[
        'rtk.realtek.com/service-root-policy'] = root['issuer_id']
    return template


class CallerRun(s.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = 'Dev Factory issuer caller durable Service Root policy adoption'
        self.report['service_caller_root_policy_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def active_root(self):
        root = self.api('/issuers/' + self.args.root_id)
        m.require(root['environment'] == 'dev' and root['trust_domain'] == 'service'
                  and root['kind'] == 'root' and root['status'] == 'active',
                  'active Service Root changed')
        self.save('root.json', root)
        return root

    def preflight_factory(self, root):
        owner = self.obj('deployment', NAME)
        m.require(owner.get('status', {}).get('readyReplicas') == 1
                  and owner.get('status', {}).get('updatedReplicas') == 1
                  and owner.get('status', {}).get('observedGeneration') == owner['metadata']['generation'],
                  'Factory caller is not ready')
        caller_template(owner, root)
        self.check('preflight_factory_service_root_caller', {
            'root_id': root['issuer_id'], 'caller': CONSUMER, 'staging_touched': False})

    def provision_state(self, root):
        """Write a reviewed public policy envelope to Factory's existing PVC."""
        policy = self.api('/issuers/' + root['issuer_id'] + '/distrust')
        m.require(policy['environment'] == 'dev' and policy['trust_domain'] == 'service'
                  and re.fullmatch(r'[0-9a-f]{64}', policy['policy_sha256']),
                  'Service Root policy differs before Factory bootstrap state')
        root_pem, policy_path, state_path = (self.output / name for name in
                                             ('factory-roots.pem', 'factory-policy.json', 'factory-state.json'))
        root_pem.write_text(root['certificate_pem'])
        m.write(policy_path, policy)
        result = subprocess.run(
            ['go', 'run', './cmd/pkitrust', 'apply', str(policy_path), str(root_pem), str(state_path)],
            cwd=str(m.WORKSPACE / 'repos/rtk_video_cloud'),
            env=dict(os.environ, GOWORK='off', GOCACHE='/private/tmp/r2-go-cache'),
            capture_output=True, text=True, timeout=120)
        m.require(result.returncode == 0, 'Factory Service Root state preparation failed')
        state = state_path.read_text()
        encoded = base64.b64encode(state.encode()).decode()
        existing = self.kube(['-n', NS, 'exec', 'deployment/' + NAME, '--', 'sh', '-c',
                              'if test -e ' + STATE + '; then cat ' + STATE + '; fi'])
        if existing:
            m.require(existing == state, 'existing Factory Service Root state differs')
        else:
            command = ('umask 077; mkdir -p /state/identity; printf %s ' + encoded
                       + ' | base64 -d > ' + STATE + '; chmod 600 ' + STATE)
            self.kube(['-n', NS, 'exec', 'deployment/' + NAME, '--', 'sh', '-c', command])
        self.save('factory-prepared-state.json', json.loads(state))
        return policy

    def rollout(self, root):
        owner = self.obj('deployment', NAME)
        template = caller_template(owner, root, self.args.image)
        self.save('factory-template.json', template)
        self.observed_patch('deployment', NAME, owner, [
            {'op': 'test', 'path': '/spec/template', 'value': owner['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + NAME, '--timeout=300s'], timeout=310)
        self.check('factory_service_root_policy_installed', {
            'root_id': root['issuer_id'], 'state_path': STATE,
            'image': template['spec']['containers'][0]['image']})

    def receipt(self, root, expected):
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            policy = self.api('/issuers/' + root['issuer_id'] + '/distrust')
            m.require(policy['policy_sha256'] == expected['policy_sha256'], 'Service Root policy changed during rollout')
            rows = self.kube([
                '-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--',
                'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'video_cloud', '-At'],
                "SELECT loaded_roots_sha256 FROM pki_root_distrust_acknowledgments "
                "WHERE environment='dev' AND domain='service' AND policy_sha256='" + policy['policy_sha256']
                + "' AND consumer_id='" + CONSUMER + "';")
            if rows:
                raw = self.kube(['-n', NS, 'exec', 'deployment/' + NAME, '--', 'cat', STATE])
                state = json.loads(raw)
                m.require(installed_policy_sha(state) == policy['policy_sha256'],
                          'persisted Factory policy differs from receipt')
                self.check('factory_service_root_policy_receipt', {
                    'root_id': root['issuer_id'], 'policy_sha256': policy['policy_sha256'],
                    'consumer': CONSUMER, 'loaded_roots_sha256': rows})
                return
            time.sleep(3)
        raise RuntimeError('Factory Service Root policy receipt deadline')

    def canary(self):
        # Reuse the established Dev Factory enrollment probe. It exercises this
        # caller's actual certissuer connection, then validates the issued
        # device through direct mTLS and MQTT.
        source = Path(__file__).with_name('factory_identity.py')
        module_spec = importlib.util.spec_from_file_location('factory_identity', source)
        factory = importlib.util.module_from_spec(module_spec)
        module_spec.loader.exec_module(factory)
        factory.FactoryIdentityRun.factory_canary(self)

    def install(self):
        self.preflight()
        root = self.active_root()
        self.preflight_factory(root)
        policy = self.provision_state(root)
        self.rollout(root)
        self.receipt(root, policy)
        self.canary()


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--root-id', required=True)
    parser.add_argument('--image', required=True)
    args = parser.parse_args()
    args.phase = 'install'
    m.require(re.fullmatch(r'[0-9a-f-]{36}', args.root_id), 'canonical Service Root ID required')
    m.require(IMAGE.fullmatch(args.image), 'immutable Dev Video Cloud image required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = CallerRun(args)
    try:
        runner.install()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
