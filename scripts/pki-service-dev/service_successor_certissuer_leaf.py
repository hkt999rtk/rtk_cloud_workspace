#!/usr/bin/env python3
"""Rotate CertIssuer's Dev Service client and host leaves to the final authority."""
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

NAME, SUBJECT = 'certissuer', 'service:certissuer'
STATE = '/var/lib/pki-host/identity/client-root-697e8e86.json'
ROOT = '32bbbfd220db619ebcf54af5f62221ed49635e58ddaa42e67730676f073704eb'
FINAL = 'd61845ca-6b85-4f11-920b-f2f9685b0c13'
HOST = 'certissuer.video-cloud-dev-video-cloud.svc'


class CertIssuerFinalLeaf(r.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = 'Dev CertIssuer final Service client/server leaf rotation'
        self.report['service_successor_certissuer_leaf_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def state(self):
        value = json.loads(self.kube(['-n', NS, 'exec', 'deployment/' + NAME, '--',
                                      '/app/serviceidentity-bootstrap', 'inspect', STATE, SUBJECT, ROOT]))
        m.require(value.get('subject') == SUBJECT and value.get('root_sha256') == ROOT and not value.get('pending')
                  and re.fullmatch(r'[0-9a-f]{64}', value.get('fingerprint', ''))
                  and re.fullmatch(r'[0-9a-f]{64}', value.get('public_key_sha256', '')),
                  'CertIssuer client state differs')
        return value

    def rows(self, kind):
        if kind == 'client':
            query = ("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,subject,caller,status,fingerprint,issued_at "
                     "FROM pki_service_client_issuances WHERE environment='dev' AND subject='service:certissuer' "
                     "AND revoked_at IS NULL ORDER BY issued_at,request_id) t;")
        else:
            query = ("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,caller,status,fingerprint,issued_at "
                     "FROM pki_server_issuances WHERE environment='dev' AND domain='service' "
                     "AND dns_names='[\"" + HOST + "\"]'::jsonb AND revoked_at IS NULL ORDER BY issued_at,request_id) t;")
        raw = self.kube(['-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--', 'psql', '-X',
                         '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'video_cloud', '-At'], query)
        return [json.loads(line) for line in raw.splitlines() if line]

    def issuer(self):
        value = self.api('/issuers/' + FINAL)
        m.require(value['status'] == 'active' and value['environment'] == 'dev'
                  and value['trust_domain'] == 'service' and SUBJECT in value['service_client_ids']
                  and HOST in value['server_dns_names'], 'final CertIssuer authority changed')
        self.api('/issuers/' + FINAL + '/crl')
        return value

    def rotate(self):
        super().preflight()
        self.issuer()
        owner = self.obj('deployment', NAME)
        m.require(owner.get('status', {}).get('readyReplicas') == 1
                  and self.kube(['-n', NS, 'exec', 'deployment/' + NAME, '--', 'cat', '/proc/1/comm']).strip() == NAME,
                  'CertIssuer is not ready to receive one renewal signal')
        before = {'client': self.state(), 'client_rows': self.rows('client'), 'server_rows': self.rows('server')}
        m.require(before['client_rows'] and before['server_rows']
                  and before['client_rows'][-1]['issuer_id'] != FINAL and before['server_rows'][-1]['issuer_id'] != FINAL,
                  'CertIssuer final leaf rotation is already complete or baseline changed')
        self.save('baseline.json', before)
        self.save('renewal-intent.json', {'previous_client': before['client']['fingerprint'],
                                          'previous_server': before['server_rows'][-1]['fingerprint'], 'at': m.stamp()})
        self.kube(['-n', NS, 'exec', 'deployment/' + NAME, '--', 'kill', '-HUP', '1'])
        deadline = time.monotonic() + 180
        while True:
            after = {'client': self.state(), 'client_rows': self.rows('client'), 'server_rows': self.rows('server')}
            added_client = [row for row in after['client_rows'] if row not in before['client_rows']]
            added_server = [row for row in after['server_rows'] if row not in before['server_rows']]
            if (after['client']['fingerprint'] != before['client']['fingerprint'] and len(added_client) == 1
                    and len(added_server) == 1):
                break
            m.require(time.monotonic() < deadline, 'CertIssuer renewal incomplete; retain evidence and do not signal again')
            time.sleep(2)
        m.require(added_client[0]['issuer_id'] == FINAL and added_client[0]['subject'] == SUBJECT
                  and added_client[0]['caller'] == SUBJECT and added_client[0]['status'] == 'succeeded'
                  and added_client[0]['fingerprint'] == after['client']['fingerprint']
                  and added_server[0]['issuer_id'] == FINAL and added_server[0]['caller'] == SUBJECT
                  and added_server[0]['status'] == 'succeeded', 'CertIssuer final leaf registry result differs')
        self.save('renewed.json', after)
        factory.FactoryIdentityRun.factory_canary(self)
        current = self.obj('deployment', NAME)
        template = copy.deepcopy(current['spec']['template'])
        template.setdefault('metadata', {}).setdefault('annotations', {})['rtk.cloud/r2-certissuer-final-leaf'] = self.output.name
        self.observed_patch('deployment', NAME, current, [
            {'op': 'test', 'path': '/spec/template', 'value': current['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': template},
        ])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + NAME, '--timeout=300s'], timeout=310)
        m.require(self.state() == after['client'] and self.rows('client') == after['client_rows']
                  and self.rows('server') == after['server_rows'], 'CertIssuer restart changed final leaf state')
        factory.FactoryIdentityRun.factory_canary(self)
        self.check('certissuer_final_service_leaves', {
            'final_issuer_id': FINAL, 'client_request_id': added_client[0]['request_id'],
            'server_request_id': added_server[0]['request_id'], 'restart_preserved_identity': True,
            'factory_canary': 'passed', 'private_keys_exported': False, 'staging_touched': False})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    args = parser.parse_args(); args.phase = 'certissuer-final-service-leaf'; args.authority = None
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600); fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = CertIssuerFinalLeaf(args)
    try:
        runner.rotate(); runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error); raise
    finally:
        runner.save('report.json', runner.report); runner.close(); os.close(fd)
        print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)

if __name__ == '__main__':
    try: main()
    except Exception as error: print(json.dumps({'error': str(error)}), file=sys.stderr); sys.exit(1)
