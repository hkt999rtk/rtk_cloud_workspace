#!/usr/bin/env python3
"""Prepare the final Dev Service successor intermediate for R2 leaf rotation.

This authority replaces the first successor intermediate, which intentionally
covered only CertIssuer, pki-controller and Account Manager.  It retains that
approved scope and adds the remaining governed Service clients.  Activation is
deliberately separate from this preparation step.
"""
import argparse
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import signal
import sys

spec = importlib.util.spec_from_file_location('factory_adoption', Path(__file__).with_name('factory_adoption.py'))
f = importlib.util.module_from_spec(spec); spec.loader.exec_module(f)
r, m = f.r, f.m

CLIENT_IDS = ['service:account-manager', 'service:certissuer', 'service:factory-enroll',
              'service:openbao', 'service:pki-controller', 'service:pkibroker',
              'service:video-cloud-api']
DNS_NAMES = ['account-manager-internal.video-cloud-dev-account-manager.svc',
             'certissuer.video-cloud-dev-video-cloud.svc',
             'pki-controller.video-cloud-dev-video-cloud.svc']
PREDECESSOR_CLIENT_IDS = ['service:account-manager', 'service:certissuer', 'service:pki-controller']
PREDECESSOR_DNS_NAMES = DNS_NAMES[1:]

def select_predecessor(items, root):
    active = [i for i in items if i['environment'] == 'dev' and i['trust_domain'] == 'service'
              and i['kind'] == 'intermediate' and i['parent_issuer_id'] == root['issuer_id']
              and i['status'] == 'active']
    m.require(len(active) == 1, 'exactly one active successor-root Service intermediate required')
    old = active[0]
    m.require(old['service_client_ids'] == PREDECESSOR_CLIENT_IDS and old['server_dns_names'] == PREDECESSOR_DNS_NAMES,
              'successor-root Service intermediate policy differs')
    return old

# Reuse the reviewed ceremony and provider-boundary implementation.  Its v2
# labels are historical; this invocation produces successor-root version seven.
f.V2_CLIENT_IDS = CLIENT_IDS
r.SERVICE_DNS_NAMES = DNS_NAMES
f.select_v1 = select_predecessor

class FinalAuthority(f.FactoryAdoption):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = 'Dev final successor Service authority preparation before R2 leaf rotation'
        self.report['service_successor_final_authority_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    p.add_argument('--authority', required=True)
    p.add_argument('--output', required=True)
    args = p.parse_args(); args.phase = 'prepare-final-successor-intermediate'; args.prepared = None; args.resume = False
    args.image = args.activation = args.intermediate = None
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600); fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = FinalAuthority(args)
    try:
        runner.preflight(); runner.prepare_intermediate_v2(); runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error); raise
    finally:
        runner.save('report.json', runner.report); runner.close(); os.close(fd)
        print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)

if __name__ == '__main__':
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(RuntimeError('interrupted; reconcile saved authority evidence')))
    try: main()
    except Exception as error: print(json.dumps({'error': str(error)}), file=sys.stderr); sys.exit(1)
