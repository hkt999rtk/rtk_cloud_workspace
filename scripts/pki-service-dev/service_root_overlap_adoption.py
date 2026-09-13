#!/usr/bin/env python3
"""Adopt a prepared Service Root successor with a two-root Dev overlap."""
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
IMAGE = re.compile(r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}')

# Each entry owns only one Deployment, its public root ConfigMap, and its
# already-adopted policy settings. The successor state is separate so the
# existing state cannot suppress the reviewed additional anchor.
TARGETS = (
    {'name': 'pki-controller', 'namespace': NS, 'container': 'pki-controller',
     'consumer': 'pki-controller', 'volume': 'service-root-policy',
     'old_config': 'pki-service-root-policy', 'new_config': 'pki-service-root-policy-{suffix}',
     'prefixes': ('PKI_SERVICE_CLIENT',), 'state_indexes': (0,),
     'states': ('/var/lib/pki-host/identity/service-root-policy-{suffix}.json',),
     'bundle_volume': 'service-bundles', 'old_bundle_config': 'pki-service-bundles',
     'new_bundle_config': 'pki-service-bundles-{suffix}'},
    {'name': 'certissuer', 'namespace': NS, 'container': 'certissuer',
     'consumer': 'certissuer', 'volume': 'service-root-policy',
     'old_config': 'pki-service-root-policy', 'new_config': 'pki-service-root-policy-{suffix}',
     'prefixes': ('CERT_ISSUER_SERVICE_CLIENT',), 'state_indexes': (0,),
     'states': ('/var/lib/pki-host/identity/service-root-policy-{suffix}.json',),
     'bundle_volume': 'service-bundles', 'old_bundle_config': 'pki-service-bundles',
     'new_bundle_config': 'pki-service-bundles-{suffix}'},
    {'name': 'factoryenroll', 'namespace': NS, 'container': 'factoryenroll',
     'consumer': 'factory-enroll', 'volume': 'service-root',
     'old_config': 'pki-service-host-root', 'new_config': 'pki-service-host-root-{suffix}',
     'prefixes': ('FACTORY_ENROLL_CERT_ISSUER', 'FACTORY_ENROLL_ACCOUNT_MANAGER'), 'state_indexes': (0, 1),
     'states': ('/state/identity/issuer-root-policy-{suffix}.json',
                '/state/identity/account-manager-root-policy-{suffix}.json')},
    {'name': 'video-cloud-api', 'namespace': NS, 'container': 'app',
     'consumer': 'video-cloud-api', 'volume': 'service-root',
     'old_config': 'pki-service-host-root', 'new_config': 'pki-service-host-root-{suffix}',
     'prefixes': ('VIDEO_CLOUD_ACCOUNT_MANAGER', 'VIDEO_CLOUD_ACCOUNT_MANAGER_RENEWAL'), 'state_indexes': (0, 0),
     'states': ('/var/lib/video-cloud-api-pki/private/account-manager-root-policy-{suffix}.json',)},
    {'name': 'account-manager', 'namespace': AM_NS, 'container': 'pkimanagement',
     'consumer': 'account-manager', 'volume': 'management-root',
     'old_config': 'account-manager-service-root', 'new_config': 'account-manager-service-root-{suffix}',
     'prefixes': ('PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT', 'PKI_MANAGEMENT_ISSUER',
                  'PKI_MANAGEMENT_CONTROLLER'), 'state_indexes': (0, 1, 1),
     'states': ('/var/lib/account-pki/private/account-service-root-policy-{suffix}.json',
                '/var/lib/account-pki/private/service-root-policy-{suffix}.json')},
)


def env_map(container):
    values = {entry['name']: entry.get('value') for entry in container.get('env', [])}
    m.require(len(values) == len(container.get('env', [])), 'duplicate Service Root policy environment setting')
    return values


def with_env(entries, updates):
    return [entry for entry in entries if entry['name'] not in updates] + [
        {'name': key, 'value': value} for key, value in updates.items()]


def pem_blocks(value):
    return sorted('-----BEGIN CERTIFICATE-----' + block.strip() + '\n'
                  for block in value.split('-----BEGIN CERTIFICATE-----')[1:])



def successor_template(owner, target, predecessor_id, successor_id, image):
    """Return the exact, narrow overlap template for one known consumer."""
    template = copy.deepcopy(owner['spec']['template'])
    selected = [c for c in template['spec'].get('containers', []) if c.get('name') == target['container']]
    m.require(len(selected) == 1, 'consumer container ownership changed: ' + target['name'])
    container = selected[0]
    values = env_map(container)
    suffix = successor_id[:12]
    states = tuple(path.format(suffix=suffix) for path in target['states'])
    m.require(len(target['state_indexes']) == len(target['prefixes']) and
              all(0 <= index < len(states) for index in target['state_indexes']),
              'Service Root state mapping changed: ' + target['name'])
    updates = {}
    for index, prefix in enumerate(target['prefixes']):
        state = states[target['state_indexes'][index]]
        current = {key: values.get(prefix + key) for key in ('_SERVICE_ROOT_ID', '_SERVICE_ROOT_STATE', '_SERVICE_ROOTS')}
        m.require(current['_SERVICE_ROOT_ID'] == predecessor_id and current['_SERVICE_ROOT_STATE'] and current['_SERVICE_ROOTS'],
                  'existing Service Root policy differs: ' + target['name'] + '/' + prefix)
        updates.update({prefix + '_SERVICE_ROOT_ID': predecessor_id,
                        prefix + '_SERVICE_ROOT_STATE': state,
                        prefix + '_SERVICE_ROOTS': current['_SERVICE_ROOTS']})
    volumes = {volume['name']: volume for volume in template['spec'].get('volumes', [])}
    volume = volumes.get(target['volume'], {})
    m.require(volume.get('configMap', {}).get('name') == target['old_config'],
              'public Service Root mount differs: ' + target['name'])
    volume['configMap']['name'] = target['new_config'].format(suffix=suffix)
    if target.get('bundle_volume'):
        bundle = volumes.get(target['bundle_volume'], {})
        m.require(bundle.get('configMap', {}).get('name') == target['old_bundle_config'],
                  'Service bundle receipt mount differs: ' + target['name'])
        bundle['configMap']['name'] = target['new_bundle_config'].format(suffix=suffix)
    container['env'] = with_env(container.get('env', []), updates)
    m.require(IMAGE.fullmatch(image), 'immutable Dev image required')
    container['image'] = image
    annotations = template.setdefault('metadata', {}).setdefault('annotations', {})
    annotations['rtk.realtek.com/service-root-policy'] = predecessor_id
    annotations['rtk.realtek.com/service-root-overlap'] = successor_id
    return template


class OverlapAdoption(s.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = 'Dev Service Root successor overlap adoption; no activation or withdrawal'
        self.report['service_root_overlap_adoption_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def evidence_root(self, directory, status):
        directory = Path(directory)
        path = next((directory / name for name in ('root-ready.json', 'service-root.json') if (directory / name).is_file()), None)
        m.require(path is not None, 'reviewed Service Root evidence is missing')
        saved = m.read(path)
        current = self.api('/issuers/' + saved['issuer_id'])
        m.require(current == saved and current['status'] == status and current['environment'] == 'dev' and
                  current['trust_domain'] == 'service' and current['kind'] == 'root', 'reviewed Service Root changed')
        return current

    def preflight(self):
        m.require(self.kube(['config', 'current-context']).strip() == self.context, 'canonical dev context mismatch')
        self.forward('am', AM_NS, 'account-manager', 80)
        self.accounts = m.read(self.foundation / 'accounts.json')
        for target in TARGETS:
            owner = self.obj('deployment', target['name'], target['namespace'])
            m.require(owner['spec'].get('replicas') == 1 and owner.get('status', {}).get('readyReplicas') == 1 and
                      owner.get('status', {}).get('updatedReplicas') == 1 and
                      owner.get('status', {}).get('observedGeneration') == owner['metadata'].get('generation'),
                      'required Dev deployment is not ready: ' + target['name'])
        self.check('service_successor_overlap_preflight', {'consumers': [target['consumer'] for target in TARGETS],
                   'staging_touched': False})

    def prepared_state(self, policy, roots):
        m.write(self.output / 'policy.json', policy)
        (self.output / 'roots.pem').write_text(roots)
        state_path = self.output / 'state.json'
        result = subprocess.run(['go', 'run', './cmd/pkitrust', 'apply', str(self.output / 'policy.json'),
                                 str(self.output / 'roots.pem'), str(state_path)],
                                cwd=str(m.WORKSPACE / 'repos/rtk_video_cloud'),
                                env=dict(os.environ, GOWORK='off', GOCACHE='/private/tmp/r2-go-cache'),
                                capture_output=True, text=True, timeout=120)
        m.require(result.returncode == 0, 'Service Root overlap state preparation failed')
        state = state_path.read_text()
        parsed = json.loads(state)
        m.require(parsed.get('policy', {}).get('policy_sha256') == policy['policy_sha256'] and
                  pem_blocks(parsed.get('roots_pem', '')) == pem_blocks(roots),
                  'prepared overlap state differs')
        self.save('prepared-state.json', parsed)
        return state

    def write_state(self, target, state, suffix):
        encoded = base64.b64encode(state.encode()).decode()
        paths = [path.format(suffix=suffix) for path in target['states']]
        for path in paths:
            existing = self.kube(['-n', target['namespace'], 'exec', 'deployment/' + target['name'], '-c',
                                  target['container'], '--', 'sh', '-c', 'if test -e ' + path + '; then cat ' + path + '; fi'])
            if existing:
                m.require(existing == state, 'existing successor state differs: ' + target['name'])
                continue
            parent = str(Path(path).parent)
            command = 'umask 077; mkdir -p ' + parent + '; printf %s ' + encoded + ' | base64 -d > ' + path + '; chmod 600 ' + path
            self.kube(['-n', target['namespace'], 'exec', 'deployment/' + target['name'], '-c',
                       target['container'], '--', 'sh', '-c', command])
        return paths

    def rollout(self, target, predecessor, successor):
        owner = self.obj('deployment', target['name'], target['namespace'])
        template = successor_template(owner, target, predecessor['issuer_id'], successor['issuer_id'], self.args.image)
        self.save(target['name'] + '-template.json', template)
        patch = [{'op': 'test', 'path': '/metadata/resourceVersion', 'value': owner['metadata']['resourceVersion']},
                 {'op': 'test', 'path': '/spec/template', 'value': owner['spec']['template']},
                 {'op': 'replace', 'path': '/spec/template', 'value': template}]
        self.kube(['-n', target['namespace'], 'patch', 'deployment', target['name'], '--type=json',
                   '--patch-file=/dev/stdin'], json.dumps(patch))
        self.kube(['-n', target['namespace'], 'rollout', 'status', 'deployment/' + target['name'], '--timeout=300s'], timeout=310)

    def receipt(self, target, policy, state, suffix, successor):
        state_path = target['states'][0].format(suffix=suffix)
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            raw = self.kube(['-n', target['namespace'], 'exec', 'deployment/' + target['name'], '-c', target['container'], '--',
                             'cat', state_path])
            if raw != state:
                time.sleep(3)
                continue
            # The predecessor policy digest is unchanged during overlap, so its
            # existing durable receipt is intentionally immutable. The two
            # listener bundle acknowledgments are the new, activation-gating
            # proof that the ready successor is installed in their live pools.
            if target['consumer'] not in ('pki-controller', 'certissuer'):
                return {'state_verified': True}
            rows = self.kube(['-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--', 'psql', '-X',
                              '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'video_cloud', '-At'],
                             "SELECT consumer_id FROM pki_bundle_acknowledgments WHERE issuer_id='" + successor['issuer_id'] +
                             "' AND bundle_version='" + successor['trust_bundle_version'] + "' AND consumer_id='" +
                             target['consumer'] + "';")
            if rows.strip() == target['consumer']:
                return {'state_verified': True, 'bundle_acknowledged': True}
            time.sleep(3)
        raise RuntimeError('successor overlap evidence deadline: ' + target['consumer'])

    def adopt(self):
        self.preflight()
        predecessor = self.evidence_root(self.args.predecessor, 'active')
        successor = self.evidence_root(self.args.successor, 'ready')
        policy = self.api('/issuers/' + predecessor['issuer_id'] + '/distrust')
        m.require(policy.get('environment') == 'dev' and policy.get('trust_domain') == 'service' and
                  re.fullmatch(r'[0-9a-f]{64}', policy.get('policy_sha256', '')), 'successor policy differs')
        roots = predecessor['certificate_pem'].rstrip() + '\n' + successor['certificate_pem'].rstrip() + '\n'
        state, suffix = self.prepared_state(policy, roots), successor['issuer_id'][:12]
        paths = {target['consumer']: self.write_state(target, state, suffix) for target in TARGETS}
        for target in TARGETS:
            self.rollout(target, predecessor, successor)
        receipts = {target['consumer']: self.receipt(target, policy, state, suffix, successor) for target in TARGETS}
        self.check('service_successor_overlap_receipts', {
            'predecessor_root_id': predecessor['issuer_id'], 'successor_root_id': successor['issuer_id'],
            'policy_authority_root_id': predecessor['issuer_id'], 'policy_sha256': policy['policy_sha256'],
            'state_paths': paths, 'loaded_roots_sha256': receipts,
            'activation': 'not_attempted', 'withdrawal': 'not_attempted'})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--predecessor', required=True)
    parser.add_argument('--successor', required=True)
    parser.add_argument('--output', required=True)
    parser.add_argument('--image', required=True)
    args = parser.parse_args(); args.phase = 'adopt-service-root-overlap'
    m.require(IMAGE.fullmatch(args.image), 'immutable Dev image required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600); fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = OverlapAdoption(args)
    try:
        runner.adopt(); runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error); raise
    finally:
        runner.save('report.json', runner.report); runner.close(); os.close(fd)
        print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)

if __name__ == '__main__':
    try: main()
    except Exception as error: print(json.dumps({'error': str(error)}), file=sys.stderr); sys.exit(1)
