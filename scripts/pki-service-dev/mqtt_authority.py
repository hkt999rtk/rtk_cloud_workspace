#!/usr/bin/env python3
"""Prepare the isolated dev MQTT Root; activation is a later consumer-gated step."""
import argparse
import base64
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import secrets
import signal
import sys
import uuid


spec = importlib.util.spec_from_file_location('service_hierarchy', Path(__file__).with_name('run.py'))
s = importlib.util.module_from_spec(spec)
spec.loader.exec_module(s)
m = s.m


class MQTTAuthorityRun(s.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = 'Dev-only dedicated MQTT Root preparation; no broker rollout or activation'
        self.report['mqtt_authority_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def prepare_root(self):
        cursor = ''
        while True:
            page = self.api('/issuers/search', {'limit': 100, 'before': cursor})
            m.require(not any(item['trust_domain'] == 'mqtt' for item in page['items']),
                      'MQTT authority already exists; reconcile instead of creating another')
            cursor = page.get('next', '')
            if not cursor:
                break
        operation = self.api('/operations', {'environment': 'dev', 'trust_domain': 'mqtt', 'kind': 'root'},
                             key='dev-mqtt-root-' + uuid.uuid4().hex)
        self.save('root-operation.json', operation)
        self.approval(operation)
        issuer = self.api('/issuers/' + operation['issuer_id'])
        m.require(issuer['status'] == 'approved' and issuer['signer_provider'] == 'offline'
                  and issuer['trust_domain'] == 'mqtt', 'MQTT Root reservation differs')
        self.save('root-approved.json', issuer)
        passfile = self.base / 'pki/rehearsal-passphrases' / ('mqtt-root-' + issuer['issuer_id'])
        m.require(not passfile.exists(), 'MQTT Root passphrase already exists')
        m.write(passfile, secrets.token_urlsafe(48))
        self.save('passphrase-reference.json', {'path': str(passfile)})
        keydir = self.output / 'root-offline-simulation'
        self.ceremony_call(['generate', '--issuer', self.output / 'root-approved.json', '--passphrase-file', passfile,
                            '--expected-request-sha256', operation['request_sha256'], '--out', keydir])
        csr = (keydir / 'csr.pem').read_text()
        self.api('/operations/' + operation['operation_id'] + '/provision', {'csr_pem': csr})
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['csr_pem'] == csr and issuer['status'] == 'provisioning', 'MQTT Root CSR differs')
        self.save('root-provisioning.json', issuer)
        csr_hash = m.digest(base64.b64decode(''.join(csr.splitlines()[1:-1])))
        self.ceremony_call(['sign', '--issuer', self.output / 'root-provisioning.json', '--passphrase-file', passfile,
                            '--key', keydir / 'ca-key.encrypted.pem', '--expected-request-sha256', operation['request_sha256'],
                            '--expected-csr-sha256', csr_hash, '--out', self.output / 'root-signed'])
        self.api('/operations/' + operation['operation_id'] + '/import',
                 {'certificate_pem': (self.output / 'root-signed/certificate.pem').read_text()}, 204)
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['status'] == 'ready' and issuer['trust_domain'] == 'mqtt', 'MQTT Root import differs')
        self.save('root-ready.json', issuer)
        self.api('/operations/' + operation['operation_id'] + '/activate', {}, 409)
        self.check('mqtt_root_ready_gate_closed', {'issuer_id': issuer['issuer_id'],
                   'activation_without_consumer_receipts': 409,
                   'key_custody': 'encrypted offline dev simulation; distinct software approval accounts'})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    lock = Path(args.config_root).expanduser() / 'dev/pki/mqtt-host-rollout.lock'
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    args.phase = 'prepare-mqtt-root'
    runner = MQTTAuthorityRun(args)
    try:
        runner.preflight()
        runner.prepare_root()
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
    def interrupted(*_):
        raise RuntimeError('interrupted; reconcile saved MQTT Root evidence before retry')
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
