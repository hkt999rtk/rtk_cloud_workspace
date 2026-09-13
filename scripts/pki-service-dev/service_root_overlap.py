#!/usr/bin/env python3
"""Prepare immutable Dev Service Root overlap bundles before consumer rollout."""
import argparse
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys

spec = importlib.util.spec_from_file_location('service_hierarchy', Path(__file__).with_name('run.py'))
s = importlib.util.module_from_spec(spec)
spec.loader.exec_module(s)
m, NS = s.m, s.NS
AM_NS = 'video-cloud-dev-account-manager'


def overlap_pem(predecessor, successor):
    for root in (predecessor, successor):
        m.require(root.get('environment') == 'dev' and root.get('trust_domain') == 'service' and
                  root.get('kind') == 'root' and root.get('certificate_pem', '').startswith('-----BEGIN CERTIFICATE-----'),
                  'reviewed Service Root certificate required')
    m.require(predecessor['issuer_id'] != successor['issuer_id'], 'Service Root successor must differ')
    return ''.join(root['certificate_pem'].rstrip() + '\n' for root in (predecessor, successor))


def overlap_configs(predecessor, successor):
    suffix = successor['issuer_id'][:12]
    roots = overlap_pem(predecessor, successor)
    return [
        {'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': 'pki-service-root-policy-' + suffix, 'namespace': NS},
         'immutable': True, 'data': {'roots.pem': roots}},
        {'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': 'pki-service-host-root-' + suffix, 'namespace': NS},
         'immutable': True, 'data': {'root.pem': roots}},
        {'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': 'account-manager-service-root-' + suffix, 'namespace': AM_NS},
         'immutable': True, 'data': {'root.pem': roots}},
    ]


class OverlapRun(s.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = 'Dev Service Root overlap bundle preparation; no consumer Deployment mutation'
        self.report['service_root_overlap_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def root(self, path, status):
        evidence = Path(path)
        saved_path = next((evidence / name for name in ('root-ready.json', 'service-root.json') if (evidence / name).is_file()), None)
        m.require(saved_path is not None, 'reviewed Service Root evidence is missing')
        saved = m.read(saved_path)
        current = self.api('/issuers/' + saved['issuer_id'])
        m.require(current == saved and current['status'] == status and current['trust_domain'] == 'service',
                  'reviewed Service Root changed: ' + status)
        return current

    def preflight_overlap(self, predecessor, successor):
        m.require(self.kube(['config', 'current-context']).strip() == self.context, 'canonical dev context mismatch')
        for name, namespace in (('pki-controller', NS), ('certissuer', NS), ('factoryenroll', NS),
                                ('video-cloud-api', NS), ('account-manager', AM_NS)):
            deployment = self.obj('deployment', name, namespace)
            m.require(deployment.get('status', {}).get('readyReplicas') == 1 and
                      deployment.get('status', {}).get('observedGeneration') == deployment['metadata'].get('generation'),
                      'required Dev deployment is not ready: ' + name)
        self.check('service_overlap_preflight', {'predecessor_root_id': predecessor['issuer_id'],
                   'successor_root_id': successor['issuer_id'], 'staging_touched': False})

    def ensure(self, obj):
        ns, name = obj['metadata']['namespace'], obj['metadata']['name']
        result = subprocess.run(self.k + ['-n', ns, 'get', 'configmap', name, '-o', 'json'], capture_output=True, text=True)
        if result.returncode == 0:
            current = json.loads(result.stdout)
            m.require(current.get('immutable') is True and current.get('data') == obj['data'],
                      'existing overlap ConfigMap differs: ' + name)
            return False
        self.kube(['-n', ns, 'create', '-f', '-'], json.dumps(obj))
        return True

    def prepare(self):
        m.require(self.kube(['config', 'current-context']).strip() == self.context, 'canonical dev context mismatch')
        self.forward('am', AM_NS, 'account-manager', 80)
        self.accounts = m.read(self.foundation / 'accounts.json')
        predecessor, successor = self.root(self.args.predecessor, 'active'), self.root(self.args.successor, 'ready')
        self.preflight_overlap(predecessor, successor)
        configs = overlap_configs(predecessor, successor)
        created = [obj['metadata']['name'] for obj in configs if self.ensure(obj)]
        rollout = self.base / 'pki/controller-bootstrap/rollout'
        rollout.mkdir(parents=True, exist_ok=True)
        for obj in configs:
            m.write(rollout / (obj['metadata']['name'] + '-configmap.json'), obj)
        self.save('overlap-configmaps.json', configs)
        self.check('service_overlap_bundles_prepared', {'predecessor_root_id': predecessor['issuer_id'],
                   'successor_root_id': successor['issuer_id'], 'configmaps': [obj['metadata']['name'] for obj in configs],
                   'created': created, 'deployment_mutations': 0})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--predecessor', required=True)
    parser.add_argument('--successor', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args(); args.phase = 'prepare-service-root-overlap'
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600); fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = OverlapRun(args)
    try:
        runner.prepare(); runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error); raise
    finally:
        runner.save('report.json', runner.report); runner.close(); os.close(fd)
        print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)

if __name__ == '__main__':
    try: main()
    except Exception as error: print(json.dumps({'error': str(error)}), file=sys.stderr); sys.exit(1)
