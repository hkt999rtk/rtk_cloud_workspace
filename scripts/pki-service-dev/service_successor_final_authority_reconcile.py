#!/usr/bin/env python3
"""Recover ready evidence for the final Dev Service successor authority."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import sys
import time

spec = importlib.util.spec_from_file_location('final_authority', Path(__file__).with_name('service_successor_final_authority.py'))
f = importlib.util.module_from_spec(spec); spec.loader.exec_module(f)
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
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    p.add_argument('--authority', required=True)
    p.add_argument('--source', required=True)
    p.add_argument('--output', required=True)
    a = p.parse_args(); a.phase = 'reconcile-final-successor-intermediate'; a.prepared = None; a.resume = False
    a.image = a.activation = a.intermediate = None
    runner = f.FinalAuthority(a)
    source = Path(a.source)
    try:
        retry_reads(runner)
        runner.preflight()
        root = runner.active_root(); operation = m.read(source / 'intermediate-operation.json')
        old = m.read(source / 'intermediate-v1.json')
        issuer = runner.api('/issuers/' + operation['issuer_id'])
        m.require(operation['issuer_id'] == issuer['issuer_id'] and issuer['status'] == 'ready'
                  and issuer['parent_issuer_id'] == root['issuer_id']
                  and issuer['service_client_ids'] == f.CLIENT_IDS and issuer['server_dns_names'] == f.DNS_NAMES,
                  'retained final Service authority differs')
        m.require(runner.api('/issuers/' + old['issuer_id']) == old,
                  'predecessor Service authority changed while final authority was prepared')
        runner.save('intermediate-operation.json', operation); runner.save('intermediate-v1.json', old)
        runner.save('intermediate-ready.json', issuer)
        policies = m.read(source / 'provider-policies.json')
        m.require(policies.get('mount') == issuer['signer_reference'] and
                  policies.get('environment') == 'dev',
                  'retained final Service provider policy differs')
        runner.save('provider-policies.json', policies)
        runner.api('/operations/' + operation['operation_id'] + '/activate', {}, 409)
        runner.check('final_successor_authority_reconciled', {'issuer_id': issuer['issuer_id'],
                     'activation_without_receipts': 409, 'private_key_exported': False})
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error); raise
    finally:
        runner.save('report.json', runner.report); runner.close()
        print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)

if __name__ == '__main__':
    try: main()
    except Exception as error: print(json.dumps({'error': str(error)}), file=sys.stderr); sys.exit(1)
