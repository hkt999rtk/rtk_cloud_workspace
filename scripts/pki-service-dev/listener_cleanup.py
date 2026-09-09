#!/usr/bin/env python3
"""Remove retired dev listener bootstrap trust and unmounted credential Secrets."""
import argparse
import base64
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import signal
import sys

spec = importlib.util.spec_from_file_location('listener_retirement', Path(__file__).with_name('listener_retirement.py'))
t = importlib.util.module_from_spec(spec)
spec.loader.exec_module(t)
r, m, NS = t.r, t.m, t.NS


class ListenerCleanupRun(t.ListenerRetirementRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['listener_cleanup_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.report['foundation_scope'] = 'Dev retired listener bootstrap trust and Secret cleanup'

    def workload_references(self, secret):
        objects = json.loads(self.kube(['-n', NS, 'get', 'deploy,statefulset,daemonset,job,cronjob,pod', '-o', 'json']))['items']
        return [item['kind'] + '/' + item['metadata']['name'] for item in objects if secret in json.dumps(item.get('spec', {}), sort_keys=True)]

    def trust_target(self, name):
        if name == 'certissuer':
            return 'certissuer-runtime', 'client-ca.crt'
        return 'pki-controller-tls', 'ca.crt'

    def remove_trust(self, name, failed=None):
        secret_name, field = self.trust_target(name)
        before = self.obj('secret', secret_name)
        bundle = base64.b64decode(before['data'][field]).decode()
        legacy = r.e.legacy_inbound_ca_path(self.base, name).read_text()
        count = bundle.count(legacy.strip())
        if count == 1:
            updated = r.e.remove_pem(bundle, legacy)
            m.require(updated.count('-----BEGIN CERTIFICATE-----') == bundle.count('-----BEGIN CERTIFICATE-----') - 1,
                      'legacy trust cleanup removed an unexpected certificate count')
            self.scoped_patch('secret', before, [{'op': 'test', 'path': '/data/' + field, 'value': before['data'][field]},
                                                 {'op': 'replace', 'path': '/data/' + field,
                                                  'value': base64.b64encode(updated.encode()).decode()}])
            return {'secret': secret_name, 'field': field, 'before_count': bundle.count('-----BEGIN CERTIFICATE-----'),
                    'after_count': updated.count('-----BEGIN CERTIFICATE-----')}
        m.require(count == 0 and failed is not None, 'expected exactly one retired self-trust certificate')
        saved_before = m.read(failed / ('before-' + NS + '-' + secret_name + '-secret.json'))
        saved_after = m.read(failed / ('after-' + NS + '-' + secret_name + '-secret.json'))
        m.require(before['data'][field] == saved_after['data'][field]
                  and base64.b64decode(saved_before['data'][field]).decode().count(legacy.strip()) == 1,
                  'failed cleanup trust state differs')
        return {'secret': secret_name, 'field': field,
                'before_count': base64.b64decode(saved_before['data'][field]).decode().count('-----BEGIN CERTIFICATE-----'),
                'after_count': bundle.count('-----BEGIN CERTIFICATE-----')}

    def delete_bootstrap(self, name, failed=None):
        secret_name = r.e.profile(name)['secret']
        m.require(self.workload_references(secret_name) == [], 'bootstrap Secret is still referenced by a workload')
        present = self.kube(['-n', NS, 'get', 'secret', secret_name, '--ignore-not-found', '-o', 'name']).strip()
        if not present:
            m.require(failed is not None and (failed / ('deleted-' + secret_name + '.json')).is_file(),
                      'bootstrap Secret is already absent without recovery evidence')
            saved = m.read(failed / ('deleted-' + secret_name + '.json'))
            options = m.read(failed / ('delete-' + secret_name + '-options.json'))
            m.require(options['preconditions']['uid'] == saved['metadata']['uid'], 'failed deletion precondition differs')
            self.save('deleted-' + secret_name + '.json', saved)
            self.save('delete-' + secret_name + '-options.json', options)
            return {'secret': secret_name, 'uid': saved['metadata']['uid']}
        before = self.obj('secret', secret_name)
        self.save('deleted-' + secret_name + '.json', before)
        options = {'apiVersion': 'v1', 'kind': 'DeleteOptions',
                   'preconditions': {'uid': before['metadata']['uid']}}
        self.save('delete-' + secret_name + '-options.json', options)
        self.kube(['delete', '--raw', '/api/v1/namespaces/' + NS + '/secrets/' + secret_name, '-f', '-'],
                  json.dumps(options))
        m.require(self.kube(['-n', NS, 'get', 'secret', secret_name, '--ignore-not-found', '-o', 'name']).strip() == '',
                  'bootstrap Secret deletion not observed')
        return {'secret': secret_name, 'uid': before['metadata']['uid']}

    def prerequisites(self):
        publication = Path(self.args.publication)
        verification = Path(self.args.retirement_verification)
        m.require(m.read(publication / 'report.json')['status'] == 'passed'
                  and m.read(publication / 'report.json')['phase'] == 'publish'
                  and m.read(verification / 'report.json')['status'] == 'passed'
                  and m.read(verification / 'report.json')['phase'] == 'verify',
                  'successful listener retirement publication and audit required')
        sources = self.renewal_sources()
        _, issuer = self.current_successors(sources)
        final = m.read(publication / 'published-crl.json')
        m.require(self.api('/issuers/' + issuer['issuer_id'] + '/crl') == final,
                  'listener retirement CRL changed before cleanup')
        self.receipt_state(issuer, final)
        return sources

    def clean(self, resume=False):
        sources = self.prerequisites()
        failed = None
        if resume:
            failed = Path(self.args.failed)
            report = m.read(failed / 'report.json')
            m.require(report['status'] == 'failed' and report['phase'] in ('clean', 'resume-clean'),
                      'matching failed cleanup evidence required')
            self.report['reconciled_from'] = str(failed)
        inventory = {}
        for name in r.e.NAMES:
            bootstrap = r.e.profile(name)['secret']
            refs = self.workload_references(bootstrap)
            m.require(refs == [], 'retired bootstrap Secret remains mounted')
            inventory[name] = {'bootstrap_secret': bootstrap, 'references': refs}
        self.save('inventory.json', inventory)
        trust = [self.remove_trust(name, failed) for name in r.e.NAMES]
        deleted = [self.delete_bootstrap(name, failed) for name in r.e.NAMES]
        for name in r.e.NAMES:
            self.rollout(name)
        self.current_successors(sources)
        self.device_baseline()
        self.check('retired_listener_bootstrap_cleaned', {'trust_bundles': trust, 'deleted_secrets': deleted,
                   'bootstrap_free_restarts': 2, 'successors_admitted': 4})

    def verify(self):
        sources = self.prerequisites()
        for name in r.e.NAMES:
            secret_name = r.e.profile(name)['secret']
            m.require(self.workload_references(secret_name) == []
                      and self.kube(['-n', NS, 'get', 'secret', secret_name, '--ignore-not-found', '-o', 'name']).strip() == '',
                      'retired bootstrap Secret restored or referenced')
            trust_name, field = self.trust_target(name)
            bundle = base64.b64decode(self.obj('secret', trust_name)['data'][field]).decode()
            legacy = r.e.legacy_inbound_ca_path(self.base, name).read_text().strip()
            m.require(legacy not in bundle and bundle.count('-----BEGIN CERTIFICATE-----') > 0,
                      'retired self-trust remains or cleanup emptied bundle')
        self.current_successors(sources)
        self.device_baseline()
        self.check('listener_cleanup_final_audit', {'legacy_ca_blocks': 0, 'bootstrap_secrets': 0,
                   'workload_references': 0, 'current_listener_identities': 4})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', choices=['clean', 'resume-clean', 'verify'], required=True)
    parser.add_argument('--config-root', default=str(Path.home() / '.config/rtk_cloud'))
    for key in ('authority', 'intermediate', 'egress', 'crl', 'certissuer-renewal',
                'controller-renewal', 'renewal-verification', 'revocation', 'publication',
                'retirement-verification', 'output'):
        parser.add_argument('--' + key, required=True)
    parser.add_argument('--failed')
    args = parser.parse_args()
    m.require(args.phase != 'resume-clean' or args.failed, 'failed cleanup evidence required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = ListenerCleanupRun(args)
    try:
        runner.preflight()
        {'clean': runner.clean, 'resume-clean': lambda: runner.clean(True), 'verify': runner.verify}[args.phase]()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'; runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report); runner.close(); os.close(fd)
        print(json.dumps({'phase': args.phase, 'status': runner.report['status'],
                          'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    def interrupted(*_): raise RuntimeError('interrupted; reconcile saved trust and Secret snapshots')
    signal.signal(signal.SIGTERM, interrupted); signal.signal(signal.SIGINT, interrupted)
    try: main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr); sys.exit(1)
