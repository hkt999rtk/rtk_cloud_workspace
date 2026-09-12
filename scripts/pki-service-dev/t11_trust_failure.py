#!/usr/bin/env python3
"""Qualify Dev CertIssuer/controller trust failures and exact recovery."""
import argparse
import copy
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import signal
import time


spec = importlib.util.spec_from_file_location(
    'account_listener_lifecycle', Path(__file__).with_name('account_listener_lifecycle.py'))
lifecycle = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lifecycle)
m, NS = lifecycle.m, lifecycle.NS

TARGETS = {
    'certissuer': {
        'database': 'CERT_ISSUER_DB_DSN',
        'manifest': 'CERT_ISSUER_SERVICE_CLIENT_SERVER_CRL_MANIFEST',
        'port': '9443',
        'path': '/healthz',
        'subject': 'service:certissuer',
    },
    'pki-controller': {
        'database': 'PKI_DATABASE_URL',
        'manifest': 'PKI_SERVICE_CLIENT_SERVER_CRL_MANIFEST',
        'port': '18446',
        'subject': 'service:pki-controller',
    },
}


def update_env(template, container_name, name, value):
    containers = [item for item in template['spec']['containers']
                  if item['name'] == container_name]
    m.require(len(containers) == 1, 'target container topology changed')
    env = containers[0].get('env', [])
    m.require(len([item for item in env if item['name'] == name]) == 1,
              'required trust setting changed: ' + name)
    containers[0]['env'] = [item for item in env if item['name'] != name]
    containers[0]['env'].append({'name': name, 'value': value})


class TrustFailureRun(lifecycle.AccountListenerLifecycle):
    def __init__(self, args):
        super().__init__(args)
        self.report['phase'] = 't11-service-trust-failure'
        self.report['foundation_scope'] = (
            'Dev-only CertIssuer/controller registry and CRL startup failure recovery')
        self.report['t11_trust_failure_runner_sha256'] = m.digest(
            Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def database_state(self, target):
        subject = TARGETS[target]['subject']
        host = target + '.' + NS + '.svc'
        values = {
            'client_issuances': self.sql(
                "SELECT count(*) FROM pki_service_client_issuances "
                "WHERE environment='dev' AND subject='" + subject + "';").strip(),
            'server_issuances': self.sql(
                "SELECT count(*) FROM pki_server_issuances "
                "WHERE environment='dev' AND domain='service' "
                "AND dns_names='[\"" + host + "\"]'::jsonb;").strip(),
            'crl_acknowledgments': self.sql(
                "SELECT count(*) FROM pki_crl_acknowledgments "
                "WHERE consumer_id='" + target + "';").strip(),
        }
        m.require(all(value.isdigit() for value in values.values()),
                  'registry count query differs')
        return {key: int(value) for key, value in values.items()}

    def replace_template(self, target, before, template, label):
        patch = [
            {'op': 'test', 'path': '/metadata/resourceVersion',
             'value': before['metadata']['resourceVersion']},
            {'op': 'test', 'path': '/spec/template',
             'value': before['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': template},
        ]
        changed = json.loads(self.kube([
            '-n', NS, 'patch', 'deployment', target, '--type=json',
            '--patch-file=/dev/stdin', '-o', 'json'], json.dumps(patch)))
        self.save(label + '-' + target + '.json', {
            'deployment_uid': changed['metadata']['uid'],
            'resource_version': changed['metadata']['resourceVersion'],
            'generation': changed['metadata']['generation'],
            'template_sha256': m.digest(json.dumps(
                changed['spec']['template'], sort_keys=True,
                separators=(',', ':')).encode()),
        })
        return changed

    def wait_failed_closed(self, target, previous_pod_uid):
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            deployment = self.obj('deployment', target)
            pods = json.loads(self.kube([
                '-n', NS, 'get', 'pods', '-l',
                'app.kubernetes.io/name=' + target, '-o', 'json']))['items']
            endpoints = self.obj('endpoints', target)
            addresses = sum(len(item.get('addresses', []))
                            for item in endpoints.get('subsets', []))
            replacements = [pod for pod in pods
                            if pod['metadata']['uid'] != previous_pod_uid]
            if ((deployment.get('status', {}).get('readyReplicas') or 0) == 0
                    and addresses == 0 and replacements):
                return {
                    'previous_pod_uid': previous_pod_uid,
                    'failed_pod_uid': replacements[0]['metadata']['uid'],
                    'ready_replicas': 0,
                    'ready_endpoints': 0,
                }
            time.sleep(2)
        raise RuntimeError(target + ' did not fail closed within 90 seconds')

    def verify_fresh_traffic(self, target):
        settings = TARGETS[target]
        path = settings.get(
            'path', '/v1/pki/issuers/' + self.root['issuer_id'] + '/crl')
        process = self.factory_session(
            target + '.' + NS + '.svc', settings['port'], path=path)
        self.session_command(process, 'check', 'alive')
        self.stop_session(process)

    def exercise_fault(self, target, kind, variable, value):
        healthy = self.obj('deployment', target)
        m.require(healthy['spec'].get('strategy', {}).get('type') == 'Recreate'
                  and healthy['spec'].get('replicas') == 1
                  and healthy.get('status', {}).get('readyReplicas') == 1,
                  'expected one ready Recreate owner: ' + target)
        pods = json.loads(self.kube([
            '-n', NS, 'get', 'pods', '-l',
            'app.kubernetes.io/name=' + target, '-o', 'json']))['items']
        ready = [pod for pod in pods if any(
            condition['type'] == 'Ready' and condition['status'] == 'True'
            for condition in pod.get('status', {}).get('conditions', []))]
        m.require(len(ready) == 1, 'expected one ready owner Pod: ' + target)
        before_state = self.database_state(target)
        original = copy.deepcopy(healthy['spec']['template'])
        failed = copy.deepcopy(original)
        update_env(failed, target, variable, value)
        failed.setdefault('metadata', {}).setdefault('annotations', {})[
            'rtk.cloud/t11-trust-failure'] = self.output.name + '-' + kind
        restored = False
        observation = None
        try:
            changed = self.replace_template(
                target, healthy, failed, 'fault-' + kind)
            observation = self.wait_failed_closed(
                target, ready[0]['metadata']['uid'])
            m.require(self.database_state(target) == before_state,
                      'failed trust startup changed issuance or ACK rows')
        finally:
            current = self.obj('deployment', target)
            self.replace_template(
                target, current, original, 'restored-' + kind)
            self.kube(['-n', NS, 'rollout', 'status',
                       'deployment/' + target, '--timeout=240s'], timeout=250)
            restored = True
        m.require(observation is not None and restored,
                  'trust failure observation or restoration missing')
        m.require(self.database_state(target) == before_state,
                  'trust recovery changed issuance or ACK rows')
        self.verify_fresh_traffic(target)
        self.check(target + '_' + kind + '_recovered', {
            **observation,
            'failure_deadline_seconds': 90,
            'issuance_and_acknowledgment_rows_unchanged': True,
            'exact_template_restored': True,
            'fresh_factory_mtls_traffic': True,
            'staging_touched': False,
        })

    def run(self):
        self.preflight_lifecycle()
        self.root = self.active_root()
        self.save('root.pem', self.root['certificate_pem'])
        self.issuer = self.v3()
        self.install_factory_probe()
        for target, settings in TARGETS.items():
            self.exercise_fault(
                target, 'registry_unavailable', settings['database'],
                'postgres://127.0.0.1:1/unavailable?connect_timeout=1')
            self.exercise_fault(
                target, 'crl_manifest_unusable', settings['manifest'],
                '/var/lib/pki-host/identity/.t11-missing-crl-manifest.json')


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=str(Path.home() / '.config/rtk_cloud'))
    parser.add_argument('--authority', required=True)
    parser.add_argument('--activation', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    args.target = 'certissuer'
    args.recover = None
    args.resume = args.resume_after_deploy = args.resume_after_listener = False
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = TrustFailureRun(args)
    try:
        runner.run()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'
        runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        os.close(fd)
        print(json.dumps({'status': runner.report['status'],
                          'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    def interrupted(*_):
        raise RuntimeError('interrupted; exact Deployment restoration attempted')
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=os.sys.stderr)
        raise SystemExit(1)
