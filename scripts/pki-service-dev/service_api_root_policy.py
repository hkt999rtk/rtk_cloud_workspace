#!/usr/bin/env python3
"""Adopt the active Service Root policy for the Dev API's Account Manager callers.

This is intentionally scoped to the two API-to-Account-Manager HTTP clients:
the normal authorization path and its renewal path.  It changes neither the
managed API identity nor Account Manager's listener identity.  The caller must
obtain its policy through the existing authenticated controller path and write
its own receipt before the rollout is accepted.
"""
import argparse
import copy
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import sys
import time


spec = importlib.util.spec_from_file_location('service_hierarchy', Path(__file__).with_name('run.py'))
s = importlib.util.module_from_spec(spec)
spec.loader.exec_module(s)
m, NS = s.m, s.NS

NAME = 'video-cloud-api'
CONTAINER = 'app'
CONSUMER = 'video-cloud-api'
PREFIXES = ('VIDEO_CLOUD_ACCOUNT_MANAGER', 'VIDEO_CLOUD_ACCOUNT_MANAGER_RENEWAL')
ROOT_MOUNT = '/run/pki-service-root/root.pem'
STATE = '/var/lib/video-cloud-api-pki/private/account-manager-root-policy.json'
RENEWAL_STATE = '/var/lib/video-cloud-api-pki/private/account-manager-renewal-root-policy.json'
SETTINGS_FILE = 'video-cloud-api-account-manager-service-settings.json'
IMAGE = re.compile(r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}')


def env_map(container):
    result = {entry['name']: entry.get('value') for entry in container.get('env', [])}
    m.require(len(result) == len(container.get('env', [])), 'duplicate API environment setting')
    return result


def with_env(entries, updates):
    return [entry for entry in entries if entry['name'] not in updates] + [
        {'name': key, 'value': value} for key, value in updates.items()]


def root_settings(root_id):
    return {
        PREFIXES[0] + '_SERVICE_ROOT_ID': root_id,
        PREFIXES[0] + '_SERVICE_ROOT_STATE': STATE,
        PREFIXES[0] + '_SERVICE_ROOTS': ROOT_MOUNT,
        PREFIXES[1] + '_SERVICE_ROOT_ID': root_id,
        PREFIXES[1] + '_SERVICE_ROOT_STATE': RENEWAL_STATE,
        PREFIXES[1] + '_SERVICE_ROOTS': ROOT_MOUNT,
    }


def api_template(owner, root, image=None):
    """Return an exact API template patch, rejecting partial or changed adoption."""
    template = copy.deepcopy(owner['spec']['template'])
    containers = template['spec'].get('containers', [])
    selected = [container for container in containers if container.get('name') == CONTAINER]
    m.require(len(selected) == 1, 'API container ownership changed')
    container = selected[0]
    values = env_map(container)
    for prefix in PREFIXES:
        required = (prefix + '_SERVER_PKI_ROOT_SHA256', prefix + '_SERVER_PKI_NAME',
                    prefix + '_SERVER_CRL_MANIFEST', prefix + '_PKI_CONTROLLER_URL',
                    prefix + '_MANAGEMENT_CA')
        m.require(all(values.get(key) for key in required),
                  'API Account Manager caller baseline is incomplete: ' + prefix)
        m.require(values[prefix + '_SERVER_PKI_ROOT_SHA256'] == root['certificate_fingerprint_sha256'],
                  'API Account Manager caller static root differs: ' + prefix)
    m.require(values.get(PREFIXES[0] + '_IDENTITY_STATE') and
              values.get(PREFIXES[0] + '_IDENTITY_ROOT_SHA256') == root['certificate_fingerprint_sha256'],
              'API managed Service identity baseline is incomplete')
    volumes = {volume['name']: volume for volume in template['spec'].get('volumes', [])}
    mounts = {mount['name']: mount for mount in container.get('volumeMounts', [])}
    m.require(volumes.get('service-root', {}).get('configMap', {}).get('name') == 'pki-service-host-root' and
              mounts.get('service-root', {}).get('mountPath') == '/run/pki-service-root' and
              mounts.get('service-root', {}).get('readOnly') is True,
              'API public Service Root mount changed')
    settings = root_settings(root['issuer_id'])
    annotation = template.get('metadata', {}).get('annotations', {}).get('rtk.realtek.com/service-root-policy')
    m.require(annotation in (None, root['issuer_id']),
              'existing API Service Root policy annotation differs; reconcile manually')
    present = [values.get(key) is not None for key in settings]
    m.require(not any(present) or all(present), 'partial API Service Root policy differs; reconcile manually')
    m.require(all(values[key] == value for key, value in settings.items() if values.get(key) is not None),
              'existing API Service Root policy differs; reconcile manually')
    if not all(present):
        container['env'] = with_env(container['env'], settings)
    if image:
        m.require(IMAGE.fullmatch(image), 'immutable Dev Video Cloud image required')
        container['image'] = image
    template.setdefault('metadata', {}).setdefault('annotations', {})[
        'rtk.realtek.com/service-root-policy'] = root['issuer_id']
    return template


def installed_policy_sha(state):
    policy = state.get('policy') if isinstance(state, dict) else None
    value = policy.get('policy_sha256') if isinstance(policy, dict) else None
    m.require(isinstance(value, str) and re.fullmatch(r'[0-9a-f]{64}', value),
              'persisted API Service Root policy state is invalid')
    return value


class APIRootPolicyRun(m.Acceptance):
    def __init__(self, args):
        super().__init__(args.config_root, 'lke649805-ctx', args.output)
        self.args = args
        self.report['foundation_scope'] = 'Dev API Account Manager caller durable Service Root policy adoption'
        self.report['checks'] = {}
        self.report['service_api_root_policy_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def root(self):
        root = self.api('/issuers/' + self.args.root_id)
        m.require(root['environment'] == 'dev' and root['trust_domain'] == 'service' and
                  root['kind'] == 'root' and root['status'] == 'active',
                  'active Service Root differs')
        self.save('root.json', root)
        return root

    def establish_controller_access(self):
        """Use the existing Dev management login only for the public policy read."""
        self.forward('am', 'video-cloud-dev-account-manager', 'account-manager', 80)
        self.accounts = m.read(self.foundation / 'accounts.json')

    def preflight(self, root):
        m.require(self.kube(['config', 'current-context']).strip() == self.context,
                  'canonical dev context mismatch')
        api = self.obj('deployment', NAME)
        m.require(api['spec'].get('replicas') == 1 and api.get('status', {}).get('readyReplicas') == 1 and
                  api.get('status', {}).get('observedGeneration') == api['metadata'].get('generation'),
                  'API is not ready')
        controller = self.obj('deployment', 'pki-controller')
        controller_env = env_map(next(container for container in controller['spec']['template']['spec']['containers']
                                      if container['name'] == 'pki-controller'))
        consumers = controller_env.get('PKI_REQUIRED_CONSUMERS_SERVICE', '').split(',')
        m.require(CONSUMER in consumers, 'controller does not require the API Service Root receipt')
        self.check('preflight', {'root_id': root['issuer_id'], 'api_ready': True,
                                 'controller_requires_api_receipt': True, 'staging_touched': False})
        return api

    def patch(self, before, template):
        patch = [
            {'op': 'test', 'path': '/metadata/resourceVersion', 'value': before['metadata']['resourceVersion']},
            {'op': 'test', 'path': '/spec/template', 'value': before['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': template},
        ]
        return json.loads(self.kube(['-n', NS, 'patch', 'deployment', NAME, '--type=json',
                                     '--patch-file=/dev/stdin', '-o', 'json'], json.dumps(patch)))

    def persist(self, deployment, root):
        rollout = self.base / 'pki/controller-bootstrap/rollout'
        rollout.mkdir(parents=True, exist_ok=True)
        template = deployment['spec']['template']
        desired = {'apiVersion': deployment['apiVersion'], 'kind': deployment['kind'],
                   'metadata': {'name': NAME, 'namespace': NS},
                   'spec': {'template': template}}
        m.write(rollout / (NAME + '-deployment.json'), desired)
        m.write(rollout / SETTINGS_FILE, root_settings(root['issuer_id']))

    def receipt(self, root):
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            policy = self.api('/issuers/' + root['issuer_id'] + '/distrust')
            rows = self.kube(['-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--',
                              'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d',
                              'video_cloud', '-At'],
                             "SELECT loaded_roots_sha256 FROM pki_root_distrust_acknowledgments "
                             "WHERE environment='dev' AND domain='service' AND policy_sha256='" +
                             policy['policy_sha256'] + "' AND consumer_id='" + CONSUMER + "';")
            if rows:
                raw = self.kube(['-n', NS, 'exec', 'deployment/' + NAME, '-c', CONTAINER, '--', 'cat', STATE])
                renewal = self.kube(['-n', NS, 'exec', 'deployment/' + NAME, '-c', CONTAINER, '--', 'cat', RENEWAL_STATE])
                state, renewal_state = json.loads(raw), json.loads(renewal)
                m.require(installed_policy_sha(state) == policy['policy_sha256'] == installed_policy_sha(renewal_state),
                          'API persisted Service Root policy differs from receipt')
                return policy, rows
            time.sleep(3)
        raise RuntimeError('API Service Root policy receipt deadline')

    def install(self):
        self.establish_controller_access()
        root = self.root()
        before = self.preflight(root)
        template = api_template(before, root, self.args.image)
        if template != before['spec']['template']:
            after = self.patch(before, template)
            self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + NAME, '--timeout=240s'], timeout=250)
        else:
            after = before
        policy, rows = self.receipt(root)
        self.persist(after, root)
        self.check('api_service_root_policy_receipt', {
            'root_id': root['issuer_id'], 'policy_sha256': policy['policy_sha256'],
            'consumer': CONSUMER, 'loaded_roots_sha256': rows,
            'states': [STATE, RENEWAL_STATE], 'settings_file': SETTINGS_FILE,
        })


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--root-id', required=True)
    parser.add_argument('--image', help='optional immutable Dev API image digest')
    args = parser.parse_args()
    m.require(re.fullmatch(r'[0-9a-f-]{36}', args.root_id), 'canonical Service Root ID required')
    m.require(not args.image or IMAGE.fullmatch(args.image), 'immutable Dev API image digest required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = APIRootPolicyRun(args)
    try:
        runner.install()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'
        runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        os.close(fd)
        print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(RuntimeError('interrupted')))
    signal.signal(signal.SIGINT, lambda *_: (_ for _ in ()).throw(RuntimeError('interrupted')))
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
