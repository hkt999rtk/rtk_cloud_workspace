#!/usr/bin/env python3
"""Roll the dev Service authority to v5 for service:openbao."""
import argparse
import fcntl
import importlib.util
import os
from pathlib import Path
import signal
import sys


spec = importlib.util.spec_from_file_location(
    'pkibroker_authority', Path(__file__).with_name('pkibroker_authority.py'))
p = importlib.util.module_from_spec(spec)
spec.loader.exec_module(p)
m, r = p.m, p.r

V5_CLIENT_IDS = sorted(p.V4_CLIENT_IDS + ['service:openbao'])


class OpenBaoServiceAuthority(p.PKIBrokerAuthority):
    target_version = 5
    client_ids = V5_CLIENT_IDS
    dns_names = p.V4_DNS_NAMES
    predecessor_versions = (1, 2, 3, 4)

    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = (
            'Active dev Service v4; add only service:openbao in v5')
        self.save('report.json', self.report)

    def select_predecessors(self, items, root, transition_id=None):
        live = sorted((item for item in items
                       if item['environment'] == 'dev'
                       and item['trust_domain'] == 'service'
                       and item['kind'] == 'intermediate'
                       and item['status'] in ('active', 'retiring')),
                      key=lambda item: item['issuer_version'])
        m.require([item['issuer_version'] for item in live] == [1, 2, 3, 4],
                  'exact Service v1-v4 predecessors required')
        policies = [
            (p.fa.OLD_CLIENT_IDS, list(r.SERVICE_DNS_NAMES)),
            (p.fa.V2_CLIENT_IDS, list(r.SERVICE_DNS_NAMES)),
            (p.V3_CLIENT_IDS, p.V3_DNS_NAMES),
            (p.V4_CLIENT_IDS, p.V4_DNS_NAMES)]
        for number, (item, policy) in enumerate(zip(live, policies), 1):
            expected_status = 'active' if number == 4 else 'retiring'
            m.require(item['parent_issuer_id'] == root['issuer_id']
                      and item['status'] == expected_status
                      and item['service_client_ids'] == policy[0]
                      and item['server_dns_names'] == policy[1],
                      'Service v%d predecessor policy differs' % number)
        fourth = [item for item in items
                  if item['environment'] == 'dev'
                  and item['trust_domain'] == 'service'
                  and item['kind'] == 'intermediate'
                  and item['issuer_version'] == 4
                  and item['status'] == 'active']
        m.require(len(fourth) == 1
                  and fourth[0]['parent_issuer_id'] == root['issuer_id']
                  and fourth[0]['service_client_ids'] == p.V4_CLIENT_IDS
                  and fourth[0]['server_dns_names'] == p.V4_DNS_NAMES,
                  'active Service v4 policy differs')
        allowed = {item['issuer_id'] for item in live}
        if transition_id:
            allowed.add(transition_id)
        unfinished = [item for item in items
                      if item['environment'] == 'dev'
                      and item['trust_domain'] == 'service'
                      and item['kind'] == 'intermediate'
                      and item['issuer_id'] not in allowed
                      and item['status'] not in ('retired', 'cancelled',
                                                 'revoked', 'compromised',
                                                 'failed')]
        m.require(not unfinished,
                  'another Service successor already exists; reconcile')
        return live


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', choices=[
        'prepare', 'controller', 'recover-controller', 'certissuer',
        'activate', 'recover-activation'], default='prepare')
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True)
    parser.add_argument('--authority', required=True)
    parser.add_argument('--prepared')
    parser.add_argument('--failed')
    parser.add_argument('--image')
    parser.add_argument('--resume', action='store_true')
    args = parser.parse_args()
    args.activation, args.intermediate = None, None
    m.require(args.phase == 'prepare' or args.prepared,
              'successful v5 preparation evidence required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = OpenBaoServiceAuthority(args)
    try:
        runner.preflight()
        {'prepare': runner.resume_v4 if args.resume else runner.prepare_v4,
         'controller': runner.install_controller,
         'recover-controller': runner.recover_controller,
         'certissuer': runner.install_certissuer,
         'activate': runner.activate_v4,
         'recover-activation': runner.recover_activation}[args.phase]()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'
        runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        os.close(owner)


if __name__ == '__main__':
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(1))
    signal.signal(signal.SIGINT, lambda *_: sys.exit(1))
    main()
