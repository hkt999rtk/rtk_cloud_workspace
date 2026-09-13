#!/usr/bin/env python3
"""Install the durable Service Root policy on the Dev Account Manager listener."""
import argparse
import base64
import copy
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time

spec = importlib.util.spec_from_file_location('service_hierarchy', Path(__file__).with_name('run.py'))
s = importlib.util.module_from_spec(spec)
spec.loader.exec_module(s)
m, NS = s.m, s.NS
AM_NS = 'video-cloud-dev-account-manager'
PREFIX = 'PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT'
STATE = '/var/lib/account-pki/private/account-service-root-policy.json'
ROOTS = '/run/pki-root/root.pem'
IMAGE = re.compile(r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}')


def env_map(container):
    values = {item['name']: item.get('value') for item in container.get('env', [])}
    m.require(len(values) == len(container.get('env', [])), 'duplicate Account Manager environment setting')
    return values


def with_env(entries, updates):
    return [entry for entry in entries if entry['name'] not in updates] + [
        {'name': key, 'value': value} for key, value in updates.items()]


def policy_sha(state):
    policy = state.get('policy') if isinstance(state, dict) else None
    value = policy.get('policy_sha256') if isinstance(policy, dict) else None
    m.require(isinstance(value, str) and re.fullmatch(r'[0-9a-f]{64}', value), 'invalid Account Manager policy state')
    return value


def listener_template(owner, root, image=None):
    template = copy.deepcopy(owner['spec']['template'])
    pod, containers = template['spec'], template['spec']['containers']
    m.require(owner['spec'].get('replicas') == 1 and [c.get('name') for c in containers] == ['app', 'pkimanagement'],
              'Account Manager listener ownership changed')
    sidecar = containers[1]
    values = env_map(sidecar)
    baseline = (PREFIX + '_BUNDLE_MANIFEST', PREFIX + '_SERVER_CRL_MANIFEST',
                'PKI_MANAGEMENT_ACCOUNT_LISTEN_ADDR', 'PKI_MANAGEMENT_ACCOUNT_CLIENT_CA',
                'PKI_MANAGEMENT_ACCOUNT_CLIENT_ROOT_SHA256', 'PKI_MANAGEMENT_IDENTITY_STATE')
    m.require(all(values.get(key) for key in baseline)
              and values['PKI_MANAGEMENT_ACCOUNT_CLIENT_ROOT_SHA256'] == root['certificate_fingerprint_sha256'],
              'Account Manager listener baseline differs')
    mounts = {item['name']: item for item in sidecar.get('volumeMounts', [])}
    volumes = {item['name']: item for item in pod.get('volumes', [])}
    m.require(mounts.get('management-state', {}).get('mountPath') == '/var/lib/account-pki'
              and mounts.get('management-root', {}).get('mountPath') == '/run/pki-root'
              and volumes.get('management-state', {}).get('persistentVolumeClaim', {}).get('claimName') == 'account-manager-service-identity',
              'Account Manager policy state ownership changed')
    updates = {PREFIX + '_SERVICE_ROOT_ID': root['issuer_id'], PREFIX + '_SERVICE_ROOT_STATE': STATE,
               PREFIX + '_SERVICE_ROOTS': ROOTS}
    found = [values.get(key) is not None for key in updates]
    if any(found):
        m.require(all(found) and all(values[key] == value for key, value in updates.items()),
                  'existing Account Manager Service Root policy differs')
    else:
        sidecar['env'] = with_env(sidecar['env'], updates)
    if image:
        m.require(IMAGE.fullmatch(image), 'immutable Dev Video Cloud image required')
        sidecar['image'] = image
    template.setdefault('metadata', {}).setdefault('annotations', {})['rtk.realtek.com/service-root-policy'] = root['issuer_id']
    return template


class AccountRun(s.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = 'Dev Account Manager Service listener durable root-policy adoption'
        self.report['account_listener_root_policy_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def scoped_obj(self):
        return self.obj('deployment', 'account-manager', AM_NS)

    def active_root(self):
        root = self.api('/issuers/' + self.args.root_id)
        m.require(root['environment'] == 'dev' and root['trust_domain'] == 'service' and root['kind'] == 'root' and root['status'] == 'active',
                  'active Service Root changed')
        self.save('root.json', root)
        return root

    def preflight_listener(self, root):
        owner = self.scoped_obj()
        m.require(owner.get('status', {}).get('readyReplicas') == 1 and owner.get('status', {}).get('updatedReplicas') == 1
                  and owner.get('status', {}).get('observedGeneration') == owner['metadata']['generation'], 'Account Manager is not ready')
        listener_template(owner, root)
        self.check('preflight_account_listener_root_policy', {'root_id': root['issuer_id'], 'consumer': 'account-manager', 'staging_touched': False})

    def provision_state(self, root):
        policy = self.api('/issuers/' + root['issuer_id'] + '/distrust')
        root_pem, policy_path, state_path = (self.output / name for name in ('account-roots.pem', 'account-policy.json', 'account-state.json'))
        root_pem.write_text(root['certificate_pem'])
        m.write(policy_path, policy)
        result = subprocess.run(['go', 'run', './cmd/pkitrust', 'apply', str(policy_path), str(root_pem), str(state_path)],
                                cwd=str(m.WORKSPACE / 'repos/rtk_video_cloud'),
                                env=dict(os.environ, GOWORK='off', GOCACHE='/private/tmp/r2-go-cache'), capture_output=True, text=True, timeout=120)
        m.require(result.returncode == 0, 'Account Manager policy state preparation failed')
        state, encoded = state_path.read_text(), base64.b64encode(state_path.read_bytes()).decode()
        existing = self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--', 'sh', '-c',
                              'if test -e ' + STATE + '; then cat ' + STATE + '; fi'])
        if existing:
            m.require(existing == state, 'existing Account Manager policy state differs')
        else:
            self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--', 'sh', '-c',
                       'umask 077; printf %s ' + encoded + ' | base64 -d > ' + STATE + '; chmod 600 ' + STATE])
        self.save('account-prepared-state.json', json.loads(state))
        return policy

    def rollout(self, root):
        owner, template = self.scoped_obj(), None
        template = listener_template(owner, root, self.args.image)
        self.save('account-template.json', template)
        patch = [{'op': 'test', 'path': '/metadata/resourceVersion', 'value': owner['metadata']['resourceVersion']},
                 {'op': 'test', 'path': '/spec/template', 'value': owner['spec']['template']},
                 {'op': 'replace', 'path': '/spec/template', 'value': template}]
        self.kube(['-n', AM_NS, 'patch', 'deployment', 'account-manager', '--type=json', '--patch-file=/dev/stdin'], json.dumps(patch))
        self.kube(['-n', AM_NS, 'rollout', 'status', 'deployment/account-manager', '--timeout=360s'], timeout=370)
        self.check('account_listener_root_policy_installed', {'root_id': root['issuer_id'], 'state_path': STATE, 'image': self.args.image})

    def receipt(self, root, expected):
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            policy = self.api('/issuers/' + root['issuer_id'] + '/distrust')
            m.require(policy['policy_sha256'] == expected['policy_sha256'], 'Service Root policy changed during Account Manager rollout')
            rows = self.kube(['-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--', 'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'video_cloud', '-At'],
                             "SELECT loaded_roots_sha256 FROM pki_root_distrust_acknowledgments WHERE environment='dev' AND domain='service' AND policy_sha256='" + policy['policy_sha256'] + "' AND consumer_id='account-manager';")
            if rows:
                state = json.loads(self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--', 'cat', STATE]))
                m.require(policy_sha(state) == policy['policy_sha256'], 'Account Manager policy state differs from receipt')
                self.check('account_listener_root_policy_receipt', {'root_id': root['issuer_id'], 'policy_sha256': policy['policy_sha256'], 'consumer': 'account-manager', 'loaded_roots_sha256': rows})
                return
            time.sleep(3)
        raise RuntimeError('Account Manager Service Root policy receipt deadline')

    def install(self):
        self.preflight()
        root = self.active_root()
        self.preflight_listener(root)
        policy = self.provision_state(root)
        self.rollout(root)
        self.receipt(root, policy)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True); parser.add_argument('--root-id', required=True); parser.add_argument('--image', required=True)
    args = parser.parse_args(); args.phase = 'install'
    m.require(re.fullmatch(r'[0-9a-f-]{36}', args.root_id) and IMAGE.fullmatch(args.image), 'canonical root and immutable Dev image required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600); fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = AccountRun(args)
    try:
        runner.install(); runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error); raise
    finally:
        runner.save('report.json', runner.report); runner.close()


if __name__ == '__main__':
    try: main()
    except Exception as error:
        print(str(error), file=sys.stderr); sys.exit(1)
