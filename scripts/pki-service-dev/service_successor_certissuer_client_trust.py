#!/usr/bin/env python3
"""Repair CertIssuer Dev renewal trust during the Service Root overlap."""
import argparse
import copy
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import re
import sys

spec = importlib.util.spec_from_file_location('service_hierarchy', Path(__file__).with_name('run.py'))
r = importlib.util.module_from_spec(spec); spec.loader.exec_module(r)
m, NS = r.m, r.NS

NAME = 'certissuer'
CM = 'pki-service-managed-egress-certissuer-ca'
ROOT_POLICY = 'pki-service-root-policy-697e8e86-5af'
HOST_CM = 'pki-service-host-root'
HOST_TARGET = 'pki-service-host-root-697e8e86-5af'
OLD = '87099089d30f13a7b93035b59c1c0c91bdb05bbe3dab427258c0c48e38144cc2'
NEW = '32bbbfd220db619ebcf54af5f62221ed49635e58ddaa42e67730676f073704eb'


class CertIssuerTrustRepair(r.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = 'Dev CertIssuer managed-egress and host-renewal dual-Service-Root trust repair'
        self.report['service_successor_certissuer_client_trust_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def repair(self):
        super().preflight()
        owner = self.obj('deployment', NAME)
        env = {item['name']: item.get('value') for item in owner['spec']['template']['spec']['containers'][0].get('env', [])}
        m.require(env.get('CERT_ISSUER_SERVICE_CLIENT_MANAGEMENT_CA') == '/run/pki-service-managed-egress-ca/ca.crt'
                  and env.get('CERT_ISSUER_SERVICE_CLIENT_ROOT_SHA256') == NEW
                  and env.get('CERT_ISSUER_SERVICE_CLIENT_VERIFY_ROOT_SHA256') in (OLD, NEW)
                  and env.get('CERT_ISSUER_SERVER_PKI_DOMAIN') == 'service'
                  and env.get('CERT_ISSUER_SERVER_PKI_ISSUER_ROOT_SHA256') == NEW
                  and env.get('CERT_ISSUER_SERVER_PKI_ROOT_SHA256') == OLD
                  and env.get('CERT_ISSUER_SERVER_PKI_VERIFY_ROOT_SHA256') == OLD,
                  'CertIssuer renewal policy baseline changed')
        roots = self.obj('configmap', ROOT_POLICY)
        ca = self.obj('configmap', CM)
        host_ca = self.obj('configmap', HOST_CM)
        host_target = self.obj('configmap', HOST_TARGET)
        expected = roots['data'].get('roots.pem', '')
        m.require(expected.count('-----BEGIN CERTIFICATE-----') == 2
                  and OLD not in expected and NEW not in expected,
                  'Service root policy bundle differs')
        # Fingerprints are public metadata, not PEM content.  The mounted policy
        # file is the sole source of the exact overlapping trust bundle.
        m.require(ca.get('immutable') is True and ca['data'].get('ca.crt') != expected,
                  'CertIssuer egress trust baseline is not the expected immutable predecessor bundle')
        m.require(host_ca.get('immutable') is True and host_ca['data'].get('root.pem') != expected
                  and host_target.get('immutable') is True and host_target['data'].get('root.pem') == expected,
                  'CertIssuer host renewal trust baseline is not the expected immutable overlap bundle')
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
        egress_volumes = [v for v in template['spec']['volumes'] if v['name'] == 'service-managed-egress-ca']
        host_volumes = [v for v in template['spec']['volumes'] if v['name'] == 'host-root']
        m.require(len(egress_volumes) == 1 and egress_volumes[0].get('configMap', {}).get('name') in (CM, target)
                  and len(host_volumes) == 1 and host_volumes[0].get('configMap', {}).get('name') in (HOST_CM, HOST_TARGET),
                  'CertIssuer renewal trust volume changed')
        egress_volumes[0]['configMap']['name'] = target
        host_volumes[0]['configMap']['name'] = HOST_TARGET
        verification_roots = [entry for entry in template['spec']['containers'][0].get('env', [])
                              if entry['name'] == 'CERT_ISSUER_SERVICE_CLIENT_VERIFY_ROOT_SHA256']
        m.require(len(verification_roots) == 1 and verification_roots[0].get('value') in (OLD, NEW),
                  'CertIssuer Service client verification-root baseline changed')
        verification_roots[0]['value'] = NEW
        server_roots = {entry['name']: entry for entry in template['spec']['containers'][0].get('env', [])
                        if entry['name'] in ('CERT_ISSUER_SERVER_PKI_ROOT_SHA256',
                                             'CERT_ISSUER_SERVER_PKI_VERIFY_ROOT_SHA256')}
        m.require(set(server_roots) == {'CERT_ISSUER_SERVER_PKI_ROOT_SHA256',
                                        'CERT_ISSUER_SERVER_PKI_VERIFY_ROOT_SHA256'}
                  and all(entry.get('value') == OLD for entry in server_roots.values()),
                  'CertIssuer server verification-root baseline changed')
        for entry in server_roots.values():
            entry['value'] = NEW
        if self.args.image:
            image = self.args.image.strip()
            m.require(re.fullmatch(r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}', image),
                      'CertIssuer image must be a pinned Dev video-cloud-api digest')
            template['spec']['containers'][0]['image'] = image
        template.setdefault('metadata', {}).setdefault('annotations', {})['rtk.cloud/r2-certissuer-egress-roots'] = self.output.name
        self.observed_patch('deployment', NAME, current, [
            {'op': 'test', 'path': '/spec/template', 'value': current['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': template},
        ])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + NAME, '--timeout=300s'], timeout=310)
        installed = self.obj('configmap', target)
        live = self.obj('deployment', NAME)
        selected = [v for v in live['spec']['template']['spec']['volumes'] if v['name'] == 'service-managed-egress-ca']
        selected_host = [v for v in live['spec']['template']['spec']['volumes'] if v['name'] == 'host-root']
        live_env = {entry['name']: entry.get('value') for entry in live['spec']['template']['spec']['containers'][0].get('env', [])}
        live_image = live['spec']['template']['spec']['containers'][0].get('image')
        m.require(installed.get('immutable') and installed['data'].get('ca.crt') == expected
                  and len(selected) == 1 and selected[0].get('configMap', {}).get('name') == target
                  and len(selected_host) == 1 and selected_host[0].get('configMap', {}).get('name') == HOST_TARGET
                  and live_env.get('CERT_ISSUER_SERVICE_CLIENT_VERIFY_ROOT_SHA256') == NEW
                  and live_env.get('CERT_ISSUER_SERVER_PKI_ROOT_SHA256') == NEW
                  and live_env.get('CERT_ISSUER_SERVER_PKI_VERIFY_ROOT_SHA256') == NEW,
                  'CertIssuer renewal trust did not persist')
        m.require(not self.args.image or live_image == self.args.image,
                  'CertIssuer image did not persist')
        self.check('certissuer_dual_root_renewal_trust', {
            'roots': [OLD, NEW], 'source_configmap': ROOT_POLICY,
            'host_renewal_configmap': HOST_TARGET,
            'service_client_verification_root': NEW,
            'server_verification_root': NEW,
            'restart_completed': True, 'private_keys_exported': False, 'staging_touched': False})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--image', help='optional pinned Dev video-cloud-api digest for the same rollout')
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
