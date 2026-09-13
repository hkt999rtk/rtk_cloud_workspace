#!/usr/bin/env python3
"""Install durable Service Root policy state on the two Service listeners.

Dev-only. This is deliberately a narrow first rollout: it makes the controller
and certissuer listeners persist and enforce the active Service root policy
without changing their managed identity or unrelated workload configuration.
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

NAMES = ('pki-controller', 'certissuer')
ROOT_CONFIG = 'pki-service-root-policy'
ROOT_MOUNT = '/run/pki-service-root-policy'
ROOT_STATE = '/var/lib/pki-host/identity/service-root-policy.json'
IMAGE = re.compile(r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}')


def env_map(container):
    values = {entry['name']: entry.get('value') for entry in container.get('env', [])}
    m.require(len(values) == len(container.get('env', [])), 'duplicate environment setting')
    return values


def with_env(entries, updates):
    return [entry for entry in entries if entry['name'] not in updates] + [
        {'name': key, 'value': value} for key, value in updates.items()]


def prefix(name):
    m.require(name in NAMES, 'unknown Service listener')
    return 'PKI_SERVICE_CLIENT' if name == 'pki-controller' else 'CERT_ISSUER_SERVICE_CLIENT'


def root_template(owner, name, root, image=None):
    """Return the exact listener patch, retaining only the owned additions."""
    template = copy.deepcopy(owner['spec']['template'])
    pod, containers = template['spec'], template['spec']['containers']
    m.require(len(containers) == 1 and containers[0]['name'] == name,
              'listener container ownership changed')
    container = containers[0]
    values = env_map(container)
    selected = prefix(name)
    required = (selected + '_ROOT_SHA256', selected + '_SERVER_CRL_MANIFEST',
                selected + '_PKI_CONTROLLER_URL', selected + '_MANAGEMENT_CA')
    m.require(all(values.get(key) for key in required),
              'listener CRL management path is incomplete')
    mounts = {item['name']: item for item in container.get('volumeMounts', [])}
    volumes = {item['name']: item for item in pod.get('volumes', [])}
    settings = {selected + '_SERVICE_ROOT_ID': root['issuer_id'],
                selected + '_SERVICE_ROOT_STATE': ROOT_STATE,
                selected + '_SERVICE_ROOTS': ROOT_MOUNT + '/roots.pem'}
    configured = [values.get(key) is not None for key in settings]
    expected_mount = {'name': 'service-root-policy', 'mountPath': ROOT_MOUNT,
                      'readOnly': True}
    expected_volume = {'name': 'service-root-policy',
                       'configMap': {'name': ROOT_CONFIG, 'defaultMode': 292}}
    if any(configured) or 'service-root-policy' in mounts or 'service-root-policy' in volumes:
        m.require(all(configured) and all(values[key] == value for key, value in settings.items())
                  and mounts.get('service-root-policy') == expected_mount
                  and volumes.get('service-root-policy') == expected_volume,
                  'existing Service Root policy differs; reconcile manually')
    else:
        container['env'] = with_env(container['env'], settings)
        container['volumeMounts'].append(expected_mount)
        pod['volumes'].append(expected_volume)
    if image:
        m.require(IMAGE.fullmatch(image), 'immutable Dev Video Cloud image required')
        container['image'] = image
    template['metadata'].setdefault('annotations', {})[
        'rtk.realtek.com/service-root-policy'] = root['issuer_id']
    return template


def installed_policy_sha(state):
    """Return the policy digest from the pkitrust on-disk state envelope."""
    policy = state.get('policy') if isinstance(state, dict) else None
    value = policy.get('policy_sha256') if isinstance(policy, dict) else None
    m.require(isinstance(value, str) and re.fullmatch(r'[0-9a-f]{64}', value),
              'persisted Service Root policy state is invalid')
    return value


class RootPolicyRun(s.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = 'Dev durable Service Root policy listener adoption'
        self.report['service_root_policy_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def active_root(self):
        root = self.api('/issuers/' + self.args.root_id)
        m.require(root['environment'] == 'dev' and root['trust_domain'] == 'service'
                  and root['kind'] == 'root' and root['status'] == 'active',
                  'active Service Root changed')
        crl = self.api('/issuers/' + root['issuer_id'] + '/crl')
        self.save('root.json', root)
        self.save('root-crl.json', crl)
        return root

    def preflight_root_policy(self, root):
        for name in NAMES:
            owner = self.obj('deployment', name)
            m.require(owner['spec'].get('replicas') == 1
                      and owner.get('status', {}).get('readyReplicas') == 1,
                      'listener is not ready: ' + name)
            values = env_map(owner['spec']['template']['spec']['containers'][0])
            m.require(values.get(prefix(name) + '_ROOT_SHA256') == root['certificate_fingerprint_sha256'],
                      'listener static root baseline differs: ' + name)
        self.check('preflight_service_root_policy', {
            'root_id': root['issuer_id'], 'listeners': list(NAMES),
            'staging_touched': False})

    def create_root_config(self, root):
        try:
            obj = self.obj('configmap', ROOT_CONFIG)
        except RuntimeError:
            obj = None
        if obj:
            m.require(obj.get('data', {}).get('roots.pem') == root['certificate_pem'],
                      'existing Service Root policy bundle differs')
            return
        obj = {'apiVersion': 'v1', 'kind': 'ConfigMap',
               'metadata': {'name': ROOT_CONFIG, 'namespace': NS},
               'immutable': True, 'data': {'roots.pem': root['certificate_pem']}}
        self.save('root-configmap.json', obj)
        self.kube(['-n', NS, 'create', '-f', '-'], json.dumps(obj))

    def provision_state(self, root, name):
        """Seed verified policy state through the healthy predecessor owner.

        A controller cannot fetch its first policy through the listener it has
        not started yet.  This uses the already healthy predecessor only to
        write public, authenticated policy state to its own existing PVC; it
        never copies a client key or changes a credential.
        """
        policy = self.api('/issuers/' + root['issuer_id'] + '/distrust')
        m.require(policy['environment'] == 'dev' and policy['trust_domain'] == 'service'
                  and re.fullmatch(r'[0-9a-f]{64}', policy['policy_sha256']),
                  'Service Root policy differs before bootstrap state')
        root_pem = self.output / (name + '-roots.pem')
        policy_path = self.output / (name + '-policy.json')
        state_path = self.output / (name + '-state.json')
        root_pem.write_text(root['certificate_pem'])
        m.write(policy_path, policy)
        result = subprocess.run(
            ['go', 'run', './cmd/pkitrust', 'apply', str(policy_path),
             str(root_pem), str(state_path)],
            cwd=str(m.WORKSPACE / 'repos/rtk_video_cloud'),
            env=dict(os.environ, GOWORK='off', GOCACHE='/private/tmp/r2-go-cache'),
            capture_output=True, text=True, timeout=120)
        m.require(result.returncode == 0, 'Service Root state preparation failed')
        state = state_path.read_text()
        # Existing state may be reused only when it is byte-for-byte the
        # reviewed state. A drifted or stale state must be reconciled manually.
        quoted = base64.b64encode(state.encode()).decode()
        check = self.kube(['-n', NS, 'exec', 'deployment/' + name, '--', 'sh', '-c',
                           'if test -e ' + ROOT_STATE + '; then cat ' + ROOT_STATE + '; fi'])
        if check:
            m.require(check == state, 'existing Service Root state differs: ' + name)
        else:
            command = ('umask 077; mkdir -p /var/lib/pki-host/identity; '
                       'printf %s ' + quoted + ' | base64 -d > ' + ROOT_STATE +
                       '; chmod 600 ' + ROOT_STATE)
            self.kube(['-n', NS, 'exec', 'deployment/' + name, '--', 'sh', '-c', command])
        self.save(name + '-prepared-state.json', json.loads(state))

    def rollout(self, name, root):
        owner = self.obj('deployment', name)
        template = root_template(owner, name, root, self.args.image)
        self.save(name + '-template.json', template)
        self.observed_patch('deployment', name, owner, [
            {'op': 'test', 'path': '/spec/template', 'value': owner['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + name, '--timeout=240s'], timeout=250)
        persisted = self.base / 'pki/controller-bootstrap/rollout' / (name + '-service-settings.json')
        settings = m.read(persisted)
        updates = {prefix(name) + '_SERVICE_ROOT_ID': root['issuer_id'],
                   prefix(name) + '_SERVICE_ROOT_STATE': ROOT_STATE,
                   prefix(name) + '_SERVICE_ROOTS': ROOT_MOUNT + '/roots.pem'}
        settings.update(updates)
        m.write(persisted, settings)
        self.check(name + '_service_root_policy_installed', {
            'root_id': root['issuer_id'], 'state_path': ROOT_STATE,
            'configmap': ROOT_CONFIG, 'image': template['spec']['containers'][0]['image']})

    def receipt(self, root, name):
        deadline = time.monotonic() + 90
        policy = None
        while time.monotonic() < deadline:
            policy = self.api('/issuers/' + root['issuer_id'] + '/distrust')
            rows = self.kube([
                '-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--',
                'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d',
                'video_cloud', '-At'],
                "SELECT loaded_roots_sha256 FROM pki_root_distrust_acknowledgments "
                "WHERE environment='dev' AND domain='service' AND policy_sha256='" +
                policy['policy_sha256'] + "' AND consumer_id='" + name + "';")
            if rows:
                raw = self.kube(['-n', NS, 'exec', 'deployment/' + name, '--', 'cat', ROOT_STATE])
                state = json.loads(raw)
                m.require(installed_policy_sha(state) == policy['policy_sha256'],
                          'persisted Service Root policy differs from receipt')
                return {'policy': policy, 'loaded_roots_sha256': rows, 'state': state}
            time.sleep(3)
        raise RuntimeError('Service Root policy receipt deadline: ' + name)

    def install(self):
        # The shared Dev acceptance preflight initializes the authenticated API
        # accounts before this runner can read the Service authority.
        self.preflight()
        root = self.active_root()
        self.preflight_root_policy(root)
        for name in NAMES:
            self.provision_state(root, name)
        self.create_root_config(root)
        # Controller first: its policy API must itself enforce the durable
        # Service listener state before any other listener acknowledges it.
        for name in NAMES:
            self.rollout(name, root)
        receipts = {name: self.receipt(root, name) for name in NAMES}
        self.check('service_root_policy_receipts', {
            'root_id': root['issuer_id'], 'policy_sha256': receipts['pki-controller']['policy']['policy_sha256'],
            'consumers': list(NAMES), 'state_paths': {name: ROOT_STATE for name in NAMES}})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--root-id', required=True)
    parser.add_argument('--image')
    args = parser.parse_args()
    # ServiceRun owns common Dev evidence plumbing and records the phase.
    args.phase = 'install'
    m.require(re.fullmatch(r'[0-9a-f-]{36}', args.root_id), 'canonical Service Root ID required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = RootPolicyRun(args)
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
        os.close(owner)
        print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(RuntimeError('interrupted')))
    signal.signal(signal.SIGINT, lambda *_: (_ for _ in ()).throw(RuntimeError('interrupted')))
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
