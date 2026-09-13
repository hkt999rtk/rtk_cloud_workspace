#!/usr/bin/env python3
"""Activate a prepared Dev Service Root only after exact listener receipts."""
import argparse
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import re
import sys

spec = importlib.util.spec_from_file_location('service_hierarchy', Path(__file__).with_name('run.py'))
s = importlib.util.module_from_spec(spec); spec.loader.exec_module(s)
m, NS = s.m, s.NS
AM_NS = 'video-cloud-dev-account-manager'
REQUIRED = ('pki-controller', 'certissuer')


def receipt_query(successor):
    return ("SELECT consumer_id FROM pki_bundle_acknowledgments WHERE issuer_id='" + successor['issuer_id'] +
            "' AND bundle_version='" + successor['trust_bundle_version'] + "' ORDER BY consumer_id;")


class ActivationRun(s.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = 'Dev Service Root successor activation after live listener receipts'
        self.report['service_root_activate_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def preflight(self):
        m.require(self.kube(['config', 'current-context']).strip() == self.context, 'canonical dev context mismatch')
        self.forward('am', AM_NS, 'account-manager', 80)
        self.accounts = m.read(self.foundation / 'accounts.json')
        for name, namespace in (('pki-controller', NS), ('certissuer', NS), ('factoryenroll', NS),
                                ('video-cloud-api', NS), ('account-manager', AM_NS)):
            deployment = self.obj('deployment', name, namespace)
            m.require(deployment['spec'].get('replicas') == 1 and deployment.get('status', {}).get('readyReplicas') == 1 and
                      deployment.get('status', {}).get('updatedReplicas') == 1 and
                      deployment.get('status', {}).get('observedGeneration') == deployment['metadata'].get('generation'),
                      'required Dev deployment is not ready: ' + name)

    def activate(self):
        self.preflight()
        evidence = Path(self.args.successor)
        ready, operation = m.read(evidence / 'root-ready.json'), m.read(evidence / 'root-operation.json')
        successor = self.api('/issuers/' + ready['issuer_id'])
        m.require(successor == ready and successor['status'] == 'ready' and successor['environment'] == 'dev' and
                  successor['trust_domain'] == 'service' and successor['kind'] == 'root' and
                  operation['issuer_id'] == successor['issuer_id'] and re.fullmatch(r'[0-9a-f-]{36}', operation['operation_id']),
                  'saved Service Root successor differs')
        rows = self.kube(['-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--', 'psql', '-X', '-v',
                          'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'video_cloud', '-At'], receipt_query(successor)).splitlines()
        m.require(rows == list(REQUIRED), 'exact successor listener receipts required before activation')
        self.check('service_successor_activation_preflight', {'successor_root_id': successor['issuer_id'],
                   'bundle_receipts': rows, 'staging_touched': False})
        self.api('/operations/' + operation['operation_id'] + '/activate', {}, 204)
        active = self.api('/issuers/' + successor['issuer_id'])
        m.require(active['status'] == 'active' and active['certificate_fingerprint_sha256'] == successor['certificate_fingerprint_sha256'],
                  'Service Root successor activation differs')
        self.save('successor-active.json', active)
        self.check('service_successor_activated', {'successor_root_id': active['issuer_id'],
                   'predecessor_status': 'retiring', 'withdrawal': 'not_attempted'})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--successor', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args(); args.phase = 'activate-service-root-successor'
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600); fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = ActivationRun(args)
    try:
        runner.activate(); runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error); raise
    finally:
        runner.save('report.json', runner.report); runner.close(); os.close(fd)
        print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)

if __name__ == '__main__':
    try: main()
    except Exception as error: print(json.dumps({'error': str(error)}), file=sys.stderr); sys.exit(1)
