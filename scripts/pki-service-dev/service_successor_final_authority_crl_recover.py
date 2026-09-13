#!/usr/bin/env python3
"""Publish and record the active final Dev Service intermediate CRL."""
import argparse
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import sys
import time

spec = importlib.util.spec_from_file_location(
    'final_authority', Path(__file__).with_name('service_successor_final_authority.py'))
f = importlib.util.module_from_spec(spec)
spec.loader.exec_module(f)
r, m = f.r, f.m


def retry_reads(runner):
    original = runner.api
    def api(path, body=None, *args, **kwargs):
        if body is not None:
            return original(path, body, *args, **kwargs)
        for attempt in range(3):
            try:
                return original(path, None, *args, **kwargs)
            except RuntimeError as error:
                if 'status 503' not in str(error) or attempt == 2:
                    raise
                time.sleep(attempt + 1)
    runner.api = api


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--authority', required=True)
    parser.add_argument('--prepared', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    args.phase = 'recover-final-successor-intermediate-crl'
    args.resume = False
    args.image = args.activation = args.intermediate = None
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = f.FinalAuthority(args)
    try:
        retry_reads(runner)
        runner.preflight()
        saved = m.read(Path(args.prepared) / 'intermediate-ready.json')
        current = runner.api('/issuers/' + saved['issuer_id'])
        m.require(current['status'] == 'active' and
                  current['parent_issuer_id'] == runner.active_root()['issuer_id'] and
                  current['service_client_ids'] == f.CLIENT_IDS and
                  current['server_dns_names'] == f.DNS_NAMES,
                  'active final Service authority differs')
        provider = json.loads(runner.bao(['read', '-format=json', current['signer_reference'] + '/cert/crl']))
        record = runner.api('/issuers/' + current['issuer_id'] + '/crl',
                            {'crl_pem': provider['data']['certificate']})
        m.require(record.get('issuer_id') == current['issuer_id'],
                  'published final Service CRL differs')
        runner.save('successor-active.json', runner.active_root())
        runner.save('intermediate-active.json', current)
        runner.save('intermediate-active-crl.json', record)
        runner.check('final_successor_intermediate_crl_published', {
            'issuer_id': current['issuer_id'], 'crl_sha256': record['crl_sha256'],
            'predecessor_withdrawal': 'not_attempted', 'private_key_exported': False})
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        os.close(fd)
        print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
