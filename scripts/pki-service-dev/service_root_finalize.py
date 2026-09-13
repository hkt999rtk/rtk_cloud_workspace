#!/usr/bin/env python3
"""Reconcile the final Dev Service-root listener overlays with live state."""
import argparse
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import sys

spec = importlib.util.spec_from_file_location(
    'service_hierarchy', Path(__file__).with_name('run.py'))
s = importlib.util.module_from_spec(spec)
spec.loader.exec_module(s)
m, NS = s.m, s.NS

ROOT = '32bbbfd220db619ebcf54af5f62221ed49635e58ddaa42e67730676f073704eb'
TARGETS = {
    'pki-controller': (
        'PKI_HOST_ROOT_SHA256', 'PKI_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256',
        'PKI_SERVICE_CLIENT_ROOT_SHA256'),
    'certissuer': (
        'CERT_ISSUER_SERVER_PKI_ROOT_SHA256',
        'CERT_ISSUER_SERVICE_CLIENT_ISSUER_ROOT_SHA256',
        'CERT_ISSUER_SERVICE_CLIENT_ROOT_SHA256',
        'CERT_ISSUER_HOST_ROOT_SHA256',
        'CERT_ISSUER_SERVER_PKI_ISSUER_ROOT_SHA256',
        'CERT_ISSUER_SERVER_PKI_VERIFY_ROOT_SHA256',
        'CERT_ISSUER_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256',
        'CERT_ISSUER_SERVICE_CLIENT_VERIFY_ROOT_SHA256'),
}


def literal_environment(container):
    entries = container.get('env', [])
    result = {entry['name']: entry.get('value') for entry in entries
              if 'value' in entry}
    m.require(len({entry['name'] for entry in entries}) == len(entries),
              'duplicate environment setting')
    return result


def normalized_deployment(owner):
    return {
        'apiVersion': owner['apiVersion'], 'kind': owner['kind'],
        'metadata': {'name': owner['metadata']['name'],
                     'namespace': owner['metadata']['namespace']},
        'spec': owner['spec'],
    }


def reconciled_settings(saved, live):
    missing = sorted(set(saved) - set(live))
    m.require(not missing, 'persisted settings absent from live deployment: ' +
              ','.join(missing))
    return {name: live[name] for name in saved}


class Finalize(s.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = (
            'Dev final Service-root persisted listener reconciliation')
        self.report['service_root_finalize_sha256'] = m.digest(
            Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def go(self):
        m.require(self.kube(['config', 'current-context']).strip() == self.context,
                  'canonical Dev context mismatch')
        rollout = self.base / 'pki/controller-bootstrap/rollout'
        results = {}
        for name, root_fields in TARGETS.items():
            owner = self.obj('deployment', name)
            m.require(owner['spec'].get('replicas') == 1
                      and owner.get('status', {}).get('readyReplicas') == 1
                      and owner.get('status', {}).get('updatedReplicas') == 1
                      and owner.get('status', {}).get('observedGeneration') ==
                      owner['metadata']['generation'],
                      'listener is not ready: ' + name)
            container = owner['spec']['template']['spec']['containers'][0]
            live = literal_environment(container)
            m.require(all(live.get(field) == ROOT for field in root_fields),
                      'withdrawn Service Root remains configured: ' + name)
            if name == 'pki-controller':
                m.require(live.get('PKI_REQUIRED_CONSUMERS_SERVICE') ==
                          ','.join(s.SERVICE_POLICY_CONSUMERS),
                          'Service receipt gate is incomplete')
            volumes = {volume['name']: volume for volume in
                       owner['spec']['template']['spec'].get('volumes', [])}
            crl = volumes.get('service-crls', {}).get('configMap', {}).get('name', '')
            m.require(crl.startswith('pki-service-client-crls-final-'),
                      'final Service CRL manifest is not mounted: ' + name)
            settings_path = rollout / (name + '-service-settings.json')
            settings = reconciled_settings(m.read(settings_path), live)
            m.write(settings_path, settings)
            m.write(rollout / (name + '-deployment.json'),
                    normalized_deployment(owner))
            rendered = s.render_persisted_listener(self.base, name)
            m.require(rendered['spec']['template'] == owner['spec']['template'],
                      'persisted rendering differs after reconciliation: ' + name)
            results[name] = {'root_fields': list(root_fields),
                             'service_crl_configmap': crl,
                             'persisted_render_matches_live': True}
        self.check('service_root_persistence_reconciled', {
            'root_sha256': ROOT, 'consumers': list(TARGETS),
            'results': results, 'staging_touched': False})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    args.phase = 'finalize-service-root-persistence'
    args.authority = args.image = args.activation = args.intermediate = None
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = Finalize(args)
    try:
        runner.go()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        os.close(fd)
        print(json.dumps({'status': runner.report['status'],
                          'report': str(runner.output / 'report.json')}),
              flush=True)


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
