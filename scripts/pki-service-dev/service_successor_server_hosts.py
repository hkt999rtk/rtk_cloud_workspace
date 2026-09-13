#!/usr/bin/env python3
"""Rotate the remaining Dev Service server identities to the final authority.

The controller and Account Manager already use final Service *client*
identities.  This procedure deliberately changes only their server identity
state.  A distinct successor state is bootstrapped from the retained
predecessor state.  If a successor state already has a current leaf, one
SIGUSR2 requests its final-authority renewal; otherwise startup creates the
initial request without a signal.  The transition setting is removed only
after the registry records exactly one final server issuance.
"""
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

OLD = '87099089d30f13a7b93035b59c1c0c91bdb05bbe3dab427258c0c48e38144cc2'
ROOT = '32bbbfd220db619ebcf54af5f62221ed49635e58ddaa42e67730676f073704eb'
FINAL = 'd61845ca-6b85-4f11-920b-f2f9685b0c13'
IMAGE = 'ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:195de19e7d501187fab9aac0a6fc4bfb8e74f9aa35679dd6378e25f2e1ae2bfa'
TARGETS = {
    'pki-controller': {
        'namespace': NS, 'deployment': 'pki-controller', 'container': 'pki-controller',
        'caller': 'service:pki-controller', 'host': 'pki-controller.' + NS + '.svc',
        'prefix': 'PKI', 'old_state': '/var/lib/pki-host/identity/state.json',
        'new_state': '/var/lib/pki-host/identity/state-root-697e8e86.json',
        'persisted_image_pin': 'PKI_CONTROLLER_IMAGE', 'persisted_deployment': 'pki-controller-deployment.json',
    },
    'account-manager': {
        'namespace': 'video-cloud-dev-account-manager', 'deployment': 'account-manager', 'container': 'pkimanagement',
        'caller': 'service:account-manager', 'host': 'account-manager-internal.video-cloud-dev-account-manager.svc',
        'prefix': 'PKI_MANAGEMENT_ACCOUNT', 'old_state': '/var/lib/account-pki/private/account-listener-state.json',
        'new_state': '/var/lib/account-pki/private/account-listener-state-root-697e8e86.json',
        'listener_client_root': 'PKI_MANAGEMENT_ACCOUNT_CLIENT_ROOT_SHA256',
        'persisted_image_pin': 'PKI_ACCOUNT_MANAGER_OWNER_IMAGE', 'persisted_deployment': 'account-manager-deployment.json',
    },
}


def environment(container):
    values = {entry['name']: entry for entry in container.get('env', [])}
    m.require(len(values) == len(container.get('env', [])), 'duplicate environment setting')
    return values


def update_env(container, updates, remove=()):
    container['env'] = [entry for entry in container['env']
                        if entry['name'] not in updates and entry['name'] not in remove]
    container['env'].extend({'name': key, 'value': value} for key, value in updates.items())


def selected_container(pod_spec, target):
    matches = [c for c in pod_spec['containers'] if c.get('name') == target['container']]
    m.require(len(matches) == 1, 'server identity owner topology changed: ' + target['deployment'])
    return matches[0]


def renewal_action(successor):
    return 'SIGUSR2' if successor else 'startup_bootstrap'


def transition_template(owner, target, output):
    """Build the one-time, successor-state server bootstrap deployment template."""
    template = copy.deepcopy(owner['spec']['template'])
    container = selected_container(template['spec'], target)
    env, prefix = environment(container), target['prefix']
    expected = {
        prefix + '_HOST_IDENTITY_STATE': target['old_state'],
        prefix + '_HOST_ROOT_SHA256': OLD,
        prefix + '_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256': ROOT,
    }
    if target.get('listener_client_root'):
        expected[target['listener_client_root']] = OLD
    m.require(all(env.get(key, {}).get('value') == value for key, value in expected.items()),
              'server transition baseline changed: ' + target['deployment'])
    m.require(not env.get(prefix + '_HOST_IDENTITY_TRANSITION_FROM_STATE') and
              not env.get(prefix + '_HOST_IDENTITY_TRANSITION_FROM_ROOT_SHA256'),
              'server transition already configured: ' + target['deployment'])
    m.require(container.get('image') == IMAGE, 'server-only image is not installed: ' + target['deployment'])
    update_env(container, {
        prefix + '_HOST_IDENTITY_STATE': target['new_state'],
        prefix + '_HOST_ROOT_SHA256': ROOT,
        prefix + '_HOST_IDENTITY_TRANSITION_FROM_STATE': target['old_state'],
        prefix + '_HOST_IDENTITY_TRANSITION_FROM_ROOT_SHA256': OLD,
    })
    if target.get('listener_client_root'):
        update_env(container, {target['listener_client_root']: ROOT})
    template.setdefault('metadata', {}).setdefault('annotations', {})[
        'rtk.cloud/r2-service-server-transition'] = output.name
    return template


def steady_template(owner, target, output):
    """Remove bootstrap inputs after the successor server identity exists."""
    template = copy.deepcopy(owner['spec']['template'])
    container = selected_container(template['spec'], target)
    env, prefix = environment(container), target['prefix']
    expected = {
        prefix + '_HOST_IDENTITY_STATE': target['new_state'],
        prefix + '_HOST_ROOT_SHA256': ROOT,
        prefix + '_HOST_IDENTITY_TRANSITION_FROM_STATE': target['old_state'],
        prefix + '_HOST_IDENTITY_TRANSITION_FROM_ROOT_SHA256': OLD,
    }
    if target.get('listener_client_root'):
        expected[target['listener_client_root']] = ROOT
    m.require(all(env.get(key, {}).get('value') == value for key, value in expected.items()),
              'server transition state changed: ' + target['deployment'])
    update_env(container, {}, (prefix + '_HOST_IDENTITY_TRANSITION_FROM_STATE',
                               prefix + '_HOST_IDENTITY_TRANSITION_FROM_ROOT_SHA256'))
    template.setdefault('metadata', {}).setdefault('annotations', {})[
        'rtk.cloud/r2-service-server-transition-restart'] = output.name
    return template


class ServerHosts(r.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = 'Dev final Service server identities; client identity state is deliberately unchanged'
        self.report['service_successor_server_hosts_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def patch(self, kind, name, before, changes):
        """Use the object namespace so Account Manager retains its own boundary."""
        namespace = before['metadata']['namespace']
        patches = [{'op': 'test', 'path': '/metadata/resourceVersion',
                    'value': before['metadata']['resourceVersion']}] + changes
        updated = json.loads(self.kube(['-n', namespace, 'patch', kind, name,
                                        '--type=json', '--patch-file=/dev/stdin', '-o', 'json'],
                                       json.dumps(patches)))
        clean = {'apiVersion': updated['apiVersion'], 'kind': updated['kind'],
                 'metadata': {key: updated['metadata'][key] for key in ('name', 'namespace')},
                 'spec': updated['spec']}
        m.write(self.base / 'pki/controller-bootstrap/rollout' /
                (name + '-' + updated['kind'].lower() + '.json'), clean)
        return updated

    def server_rows(self, target):
        query = ("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,caller,status,fingerprint,issued_at "
                 "FROM pki_server_issuances WHERE environment='dev' AND domain='service' "
                 "AND dns_names='[\"" + target['host'] + "\"]'::jsonb AND revoked_at IS NULL "
                 "ORDER BY issued_at,request_id) t;")
        raw = self.kube(['-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--', 'psql', '-X',
                         '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'video_cloud', '-At'], query)
        return [json.loads(line) for line in raw.splitlines() if line]

    def state(self, target, state, root):
        raw = self.kube(['-n', target['namespace'], 'exec', 'deployment/' + target['deployment'], '-c', target['container'],
                         '--', '/app/serviceidentity-bootstrap', 'inspect-server', state, 'service', target['host'], root, target['host']])
        value = json.loads(raw)
        m.require(value.get('subject') == target['host'] and value.get('root_sha256') == root and not value.get('pending')
                  and re.fullmatch(r'[0-9a-f]{64}', value.get('fingerprint', ''))
                  and re.fullmatch(r'[0-9a-f]{64}', value.get('public_key_sha256', '')),
                  'server identity state differs: ' + target['deployment'])
        return value

    def existing_successor_state(self, target):
        """Return a valid public successor state, or None when it is absent."""
        try:
            raw = self.kube(['-n', target['namespace'], 'exec', 'deployment/' + target['deployment'],
                             '-c', target['container'], '--', '/app/serviceidentity-bootstrap',
                             'inspect-server', target['new_state'], 'service', target['host'], ROOT, target['host']])
        except RuntimeError:
            return None
        value = json.loads(raw)
        m.require(value.get('subject') == target['host'] and value.get('root_sha256') == ROOT
                  and re.fullmatch(r'[0-9a-f]{64}', value.get('fingerprint', ''))
                  and re.fullmatch(r'[0-9a-f]{64}', value.get('public_key_sha256', '')),
                  'existing successor server state differs: ' + target['deployment'])
        return value

    def issuer(self, target):
        issuer = self.api('/issuers/' + FINAL)
        m.require(issuer.get('status') == 'active' and issuer.get('environment') == 'dev'
                  and issuer.get('trust_domain') == 'service' and target['host'] in issuer.get('server_dns_names', []),
                  'final server authority changed: ' + target['deployment'])
        self.api('/issuers/' + FINAL + '/crl')

    def persist_image(self, target):
        pin = self.base / 'operator/env' / target['persisted_image_pin']
        manifest = self.base / 'pki/controller-bootstrap/rollout' / target['persisted_deployment']
        m.require(pin.read_text().strip() == IMAGE, 'persisted server-only image pin changed')
        saved = m.read(manifest)
        container = selected_container(saved['spec']['template']['spec'], target)
        m.require(container.get('image') == IMAGE, 'persisted server-only deployment image changed')

    def rotate(self, target):
        self.issuer(target)
        owner = self.obj('deployment', target['deployment'], target['namespace'])
        m.require(owner.get('status', {}).get('readyReplicas') == 1
                  and self.kube(['-n', target['namespace'], 'exec', 'deployment/' + target['deployment'], '-c', target['container'],
                                 '--', 'cat', '/proc/1/comm']).strip() in (target['container'], 'pkimanagement'),
                  'server owner is not ready: ' + target['deployment'])
        self.persist_image(target)
        before = {'server': self.state(target, target['old_state'], OLD), 'rows': self.server_rows(target)}
        successor = self.existing_successor_state(target)
        m.require(before['rows'] and before['rows'][-1]['issuer_id'] != FINAL,
                  'predecessor server identity baseline differs: ' + target['deployment'])
        self.save(target['deployment'] + '-baseline.json', before)
        template = transition_template(owner, target, self.output)
        self.observed_patch('deployment', target['deployment'], owner, [
            {'op': 'test', 'path': '/spec/template', 'value': owner['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': template},
        ])
        self.kube(['-n', target['namespace'], 'rollout', 'status', 'deployment/' + target['deployment'], '--timeout=300s'], timeout=310)
        action = renewal_action(successor)
        intent = {'action': action, 'at': m.stamp(), 'previous_server': before['server']['fingerprint']}
        if successor:
            intent['existing_successor'] = successor['fingerprint']
            self.kube(['-n', target['namespace'], 'exec', 'deployment/' + target['deployment'], '-c', target['container'],
                       '--', 'sh', '-c', 'kill -USR2 1'])
        self.save(target['deployment'] + '-renewal-intent.json', intent)
        deadline = time.monotonic() + 180
        while True:
            try:
                after = {'server': self.state(target, target['new_state'], ROOT), 'rows': self.server_rows(target)}
                added = [row for row in after['rows'] if row not in before['rows']]
                if len(added) == 1:
                    break
            except RuntimeError:
                pass
            m.require(time.monotonic() < deadline, 'server-only renewal incomplete; retain evidence and do not signal again')
            time.sleep(2)
        m.require(added[0]['issuer_id'] == FINAL and added[0]['caller'] == target['caller']
                  and added[0]['status'] == 'succeeded' and added[0]['fingerprint'] == after['server']['fingerprint'],
                  'final server registry receipt differs: ' + target['deployment'])
        self.save(target['deployment'] + '-renewed.json', after)
        current = self.obj('deployment', target['deployment'], target['namespace'])
        template = steady_template(current, target, self.output)
        self.observed_patch('deployment', target['deployment'], current, [
            {'op': 'test', 'path': '/spec/template', 'value': current['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': template},
        ])
        self.kube(['-n', target['namespace'], 'rollout', 'status', 'deployment/' + target['deployment'], '--timeout=300s'], timeout=310)
        m.require(self.state(target, target['new_state'], ROOT) == after['server'] and self.server_rows(target) == after['rows'],
                  'restart changed final server identity: ' + target['deployment'])
        self.check(target['deployment'] + '_final_service_server', {
            'final_issuer_id': FINAL, 'server_request_id': added[0]['request_id'],
            'client_identity_untouched': True, 'restart_preserved_identity': True,
            'private_keys_exported': False, 'staging_touched': False,
        })

    def go(self):
        super().preflight()
        for name in self.args.targets:
            self.rotate(TARGETS[name])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--targets', nargs='+', choices=sorted(TARGETS), default=sorted(TARGETS))
    args = parser.parse_args(); args.phase = 'r2-final-service-server-identities'; args.authority = None
    os.umask(0o077)
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600); fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = ServerHosts(args)
    try:
        runner.go(); runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'], runner.report['failure'] = 'failed', str(error); raise
    finally:
        runner.save('report.json', runner.report); runner.close(); os.close(fd)
        print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    try: main()
    except Exception as error: print(json.dumps({'error': str(error)}), file=sys.stderr); sys.exit(1)
