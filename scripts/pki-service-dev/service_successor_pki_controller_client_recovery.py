#!/usr/bin/env python3
"""Recover pki-controller's pending Dev Service-client renewal after trust preparation."""
import argparse
import copy
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import re
import sys
import time

spec = importlib.util.spec_from_file_location('service_hierarchy', Path(__file__).with_name('run.py'))
r = importlib.util.module_from_spec(spec); spec.loader.exec_module(r)
m, NS = r.m, r.NS
factory_spec = importlib.util.spec_from_file_location('factory_identity', Path(__file__).with_name('factory_identity.py'))
factory = importlib.util.module_from_spec(factory_spec); factory_spec.loader.exec_module(factory)

NAME, SUBJECT = 'pki-controller', 'service:pki-controller'
STATE = '/var/lib/pki-host/identity/client-root-697e8e86.json'
OLD = '87099089d30f13a7b93035b59c1c0c91bdb05bbe3dab427258c0c48e38144cc2'
NEW = '32bbbfd220db619ebcf54af5f62221ed49635e58ddaa42e67730676f073704eb'
FINAL = 'd61845ca-6b85-4f11-920b-f2f9685b0c13'
HOST = 'pki-controller.video-cloud-dev-video-cloud.svc'
ROOT_POLICY, HOST_OLD, HOST_OVERLAP = ('pki-service-root-policy-697e8e86-5af',
                                       'pki-service-host-root', 'pki-service-host-root-697e8e86-5af')


class Recovery(r.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = 'Dev pki-controller pending final Service-client renewal recovery; predecessor server retained for R4'
        self.report['service_successor_pki_controller_client_recovery_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def state(self):
        value = json.loads(self.kube(['-n', NS, 'exec', 'deployment/' + NAME, '--',
                                      '/app/serviceidentity-bootstrap', 'inspect', STATE, SUBJECT, NEW]))
        m.require(value.get('subject') == SUBJECT and value.get('root_sha256') == NEW
                  and re.fullmatch(r'[0-9a-f]{64}', value.get('fingerprint', ''))
                  and re.fullmatch(r'[0-9a-f]{64}', value.get('public_key_sha256', '')),
                  'pki-controller client state differs')
        return value

    def rows(self, table):
        if table == 'client':
            query = ("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,subject,caller,status,fingerprint,issued_at "
                     "FROM pki_service_client_issuances WHERE environment='dev' AND subject='service:pki-controller' "
                     "AND revoked_at IS NULL ORDER BY issued_at,request_id) t;")
        else:
            query = ("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,caller,status,fingerprint,issued_at "
                     "FROM pki_server_issuances WHERE environment='dev' AND domain='service' "
                     "AND dns_names='[\"" + HOST + "\"]'::jsonb AND revoked_at IS NULL ORDER BY issued_at,request_id) t;")
        raw = self.kube(['-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--', 'psql', '-X',
                         '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'video_cloud', '-At'], query)
        return [json.loads(line) for line in raw.splitlines() if line]

    def api_retry(self, path):
        """Keep the normal authenticated management check, tolerating controller startup races."""
        deadline, failure = time.monotonic() + 60, None
        while True:
            try:
                return self.api(path)
            except Exception as error:
                failure = error
                if time.monotonic() >= deadline:
                    raise failure
                time.sleep(2)

    def recover(self):
        super().preflight()
        final = self.api_retry('/issuers/' + FINAL)
        m.require(final['status'] == 'active' and final['environment'] == 'dev' and final['trust_domain'] == 'service'
                  and SUBJECT in final['service_client_ids'], 'final Service client authority changed')
        self.api_retry('/issuers/' + FINAL + '/crl')
        before_state, before_client, before_server = self.state(), self.rows('client'), self.rows('server')
        m.require(before_state.get('pending') and before_client and before_server
                  and before_client[-1]['issuer_id'] != FINAL and before_server[-1]['issuer_id'] != FINAL,
                  'pki-controller pending recovery baseline differs')
        self.save('baseline.json', {'client': before_state, 'client_rows': before_client, 'server_rows': before_server})
        roots, old_host, overlap = (self.obj('configmap', ROOT_POLICY), self.obj('configmap', HOST_OLD),
                                    self.obj('configmap', HOST_OVERLAP))
        expected = roots['data'].get('roots.pem', '')
        m.require(expected.count('-----BEGIN CERTIFICATE-----') == 2 and old_host.get('immutable')
                  and overlap.get('immutable') and overlap['data'].get('root.pem') == expected,
                  'reviewed pki-controller overlap trust material changed')
        owner = self.obj('deployment', NAME)
        template = copy.deepcopy(owner['spec']['template'])
        volumes = [v for v in template['spec']['volumes'] if v['name'] == 'host-root']
        env = {e['name']: e for e in template['spec']['containers'][0].get('env', [])}
        m.require(len(volumes) == 1 and volumes[0].get('configMap', {}).get('name') in (HOST_OLD, HOST_OVERLAP)
                  and env.get('PKI_HOST_ROOT_SHA256', {}).get('value') == OLD
                  and env.get('PKI_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256', {}).get('value') in (OLD, NEW),
                  'pki-controller host renewal baseline changed')
        volumes[0]['configMap']['name'] = HOST_OVERLAP
        env['PKI_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256']['value'] = NEW
        template.setdefault('metadata', {}).setdefault('annotations', {})['rtk.cloud/r2-pki-controller-pending-client-recovery'] = self.output.name
        self.observed_patch('deployment', NAME, owner, [
            {'op': 'test', 'path': '/spec/template', 'value': owner['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': template},
        ])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + NAME, '--timeout=300s'], timeout=310)
        deadline = time.monotonic() + 180
        while True:
            after_state, after_client, after_server = self.state(), self.rows('client'), self.rows('server')
            added = [row for row in after_client if row not in before_client]
            if not after_state.get('pending') and len(added) == 1:
                break
            m.require(after_server == before_server, 'pending client recovery changed predecessor server identity')
            m.require(time.monotonic() < deadline, 'pending client renewal did not recover after prepared trust; retain evidence')
            time.sleep(2)
        m.require(added[0]['issuer_id'] == FINAL and added[0]['subject'] == SUBJECT and added[0]['caller'] == SUBJECT
                  and added[0]['status'] == 'succeeded' and added[0]['fingerprint'] == after_state['fingerprint']
                  and after_server == before_server, 'pki-controller recovered client receipt differs')
        factory.FactoryIdentityRun.factory_canary(self)
        self.save('recovered.json', {'client': after_state, 'client_rows': after_client, 'server_rows': after_server})
        self.check('pki_controller_pending_final_service_client_recovered', {
            'final_issuer_id': FINAL, 'client_request_id': added[0]['request_id'],
            'host_renewal_roots': [OLD, NEW], 'predecessor_server_retained_for_r4': True,
            'factory_canary': 'passed', 'private_keys_exported': False, 'staging_touched': False})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    args = parser.parse_args(); args.phase = 'pki-controller-pending-final-service-client-recovery'; args.authority = None
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600); fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = Recovery(args)
    try:
        runner.recover(); runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error); raise
    finally:
        runner.save('report.json', runner.report); runner.close(); os.close(fd)
        print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    try: main()
    except Exception as error: print(json.dumps({'error': str(error)}), file=sys.stderr); sys.exit(1)
