#!/usr/bin/env python3
"""Repair CertIssuer Dev managed-egress trust during the Service Root overlap."""
import argparse
import copy
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import sys

spec = importlib.util.spec_from_file_location('service_hierarchy', Path(__file__).with_name('run.py'))
r = importlib.util.module_from_spec(spec); spec.loader.exec_module(r)
m, NS = r.m, r.NS

NAME = 'certissuer'
CM = 'pki-service-managed-egress-certissuer-ca'
ROOT_POLICY = 'pki-service-root-policy-697e8e86-5af'
OLD = '87099089d30f13a7b93035b59c1c0c91bdb05bbe3dab427258c0c48e38144cc2'
NEW = '32bbbfd220db619ebcf54af5f62221ed49635e58ddaa42e67730676f073704eb'


class CertIssuerTrustRepair(r.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = 'Dev CertIssuer managed-egress dual-Service-Root trust repair'
        self.report['service_successor_certissuer_client_trust_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def repair(self):
        super().preflight()
        owner = self.obj('deployment', NAME)
        env = {item['name']: item.get('value') for item in owner['spec']['template']['spec']['containers'][0].get('env', [])}
        m.require(env.get('CERT_ISSUER_SERVICE_CLIENT_MANAGEMENT_CA') == '/run/pki-service-managed-egress-ca/ca.crt'
                  and env.get('CERT_ISSUER_SERVICE_CLIENT_ROOT_SHA256') == NEW
                  and env.get('CERT_ISSUER_SERVICE_CLIENT_VERIFY_ROOT_SHA256') == OLD,
                  'CertIssuer managed egress baseline changed')
        roots = self.obj('configmap', ROOT_POLICY)
        ca = self.obj('configmap', CM)
        expected = roots['data'].get('roots.pem', '')
        m.require(expected.count('-----BEGIN CERTIFICATE-----') == 2
                  and OLD not in expected and NEW not in expected,
                  'Service root policy bundle differs')
        # Fingerprints are public metadata, not PEM content.  The mounted policy
        # file is the sole source of the exact overlapping trust bundle.
        m.require(ca.get('immutable') is True and ca['data'].get('ca.crt') != expected,
                  'CertIssuer egress trust baseline is not the expected immutable predecessor bundle')
        target = CM + '-r2-' + m.digest(expected.encode())[:8]
        self.save('before-managed-egress-ca.json', {'name': CM, 'resource_version': ca['metadata']['resourceVersion'],
                                                    'previous_pem_certificates': ca['data'].get('ca.crt', '').count('-----BEGIN CERTIFICATE-----'),
                                                    'target_name': target, 'target_pem_certificates': 2, 'source': ROOT_POLICY})
        raw = self.kube(['-n', NS, 'get', 'configmap', target, '--ignore-not-found', '-o', 'json'])
        if raw.strip():
            present = json.loads(raw)
            m.require(present.get('immutable') and present.get('data', {}).get('ca.crt') == expected,
                      'existing CertIssuer overlap trust ConfigMap differs')
        else:
            self.create({'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': target, 'namespace': NS},
                         'immutable': True, 'data': {'ca.crt': expected}})
        current = self.obj('deployment', NAME)
        template = copy.deepcopy(current['spec']['template'])
        volumes = [v for v in template['spec']['volumes'] if v['name'] == 'service-managed-egress-ca']
        m.require(len(volumes) == 1 and volumes[0].get('configMap', {}).get('name') == CM,
                  'CertIssuer egress trust volume changed')
        volumes[0]['configMap']['name'] = target
        template.setdefault('metadata', {}).setdefault('annotations', {})['rtk.cloud/r2-certissuer-egress-roots'] = self.output.name
        self.observed_patch('deployment', NAME, current, [
            {'op': 'test', 'path': '/spec/template', 'value': current['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': template},
        ])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + NAME, '--timeout=300s'], timeout=310)
        installed = self.obj('configmap', target)
        live = self.obj('deployment', NAME)
        selected = [v for v in live['spec']['template']['spec']['volumes'] if v['name'] == 'service-managed-egress-ca']
        m.require(installed.get('immutable') and installed['data'].get('ca.crt') == expected
                  and len(selected) == 1 and selected[0].get('configMap', {}).get('name') == target,
                  'CertIssuer egress trust did not persist')
        self.check('certissuer_managed_egress_dual_root_trust', {
            'roots': [OLD, NEW], 'source_configmap': ROOT_POLICY,
            'restart_completed': True, 'private_keys_exported': False, 'staging_touched': False})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    args = parser.parse_args(); args.phase = 'certissuer-managed-egress-dual-root-trust'; args.authority = None
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600); fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = CertIssuerTrustRepair(args)
    try:
        runner.repair(); runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error); raise
    finally:
        runner.save('report.json', runner.report); runner.close(); os.close(fd)
        print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)
if __name__ == '__main__':
    try: main()
    except Exception as error: print(json.dumps({'error': str(error)}), file=sys.stderr); sys.exit(1)
