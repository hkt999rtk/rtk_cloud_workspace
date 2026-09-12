#!/usr/bin/env python3
"""Qualify the dev Account Manager listener renewal and retirement lifecycle."""
import argparse
import base64
import datetime as dt
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import time
import uuid


spec = importlib.util.spec_from_file_location('account_listener', Path(__file__).with_name('account_listener.py'))
a = importlib.util.module_from_spec(spec)
spec.loader.exec_module(a)
m, NS, AM_NS = a.m, a.NS, a.AM_NS
CLIENT_STATE = a.mg.STATE
HOST_STATE = a.STATE
SUBJECT = 'service:account-manager'
FACTORY_STATE = '/state/identity/client.json'
FACTORY_ROOT = '/run/service-root/root.pem'
CONSUMER_MANIFEST_ANNOTATION = 'rtk.cloud/pki-service-crl-manifest-sha256'


def current_service_issuer(items):
    candidates = [item for item in items
                  if item['environment'] == 'dev' and item['trust_domain'] == 'service'
                  and item['kind'] == 'intermediate' and item['status'] == 'active']
    m.require(len(candidates) == 1, 'one active Service intermediate is required')
    issuer = candidates[0]
    m.require(SUBJECT in issuer['service_client_ids'] and a.ACCOUNT_DNS in issuer['server_dns_names'],
              'active Service issuer does not authorize Account Manager')
    return issuer


def consumer_crl_entries(entries, path):
    result = []
    for entry in entries:
        issuer = entry.get('issuer', {})
        issuer_id = issuer.get('issuer_id', '')
        m.require(issuer.get('environment') == 'dev' and issuer.get('trust_domain') == 'service'
                  and issuer.get('status') in ('active', 'retiring')
                  and m.re.fullmatch('[0-9a-f-]{36}', issuer_id),
                  'canonical Service CRL manifest contains an invalid issuer')
        result.append({'issuer': issuer, 'state_path': path.format(issuer_id=issuer_id)})
    m.require(result, 'canonical Service CRL manifest is empty')
    return result


def replacement(before, after, rows, issuer, kind):
    m.require(after['subject'] == before['subject'] and after['root_sha256'] == before['root_sha256']
              and not after['pending'] and before['fingerprint'] != after['fingerprint']
              and before['public_key_sha256'] != after['public_key_sha256'], kind + ' did not rotate cleanly')
    added = [row for row in rows if row['fingerprint'] == after['fingerprint']]
    m.require(len(added) == 1 and added[0]['issuer_id'] == issuer['issuer_id']
              and added[0]['status'] == 'succeeded' and added[0]['revoked_at'] is None,
              kind + ' successor registry row differs')
    return added[0]


class AccountListenerLifecycle(a.ListenerRun):
    def __init__(self, args):
        super().__init__(args)
        self.remote_probe = '/tmp/pki-account-listener-lifecycle-' + uuid.uuid4().hex
        self.factory_probe = '/state/identity/.pki-account-listener-lifecycle-' + uuid.uuid4().hex
        self.probe_installed = False
        self.factory_probe_installed = False
        self.host_probe_owner = None
        self.report['phase'] = 'lifecycle'
        self.report['foundation_scope'] = 'Dev Account Manager managed client/listener renewal, held-session retirement and successor survival'
        self.report['account_listener_lifecycle_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def close(self):
        try:
            if self.probe_installed:
                self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--', 'rm', '-f', self.remote_probe])
            if self.factory_probe_installed:
                self.kube(['-n', NS, 'exec', 'deployment/factoryenroll', '-c', 'factoryenroll', '--', 'rm', '-f', self.factory_probe])
            if self.host_probe_owner:
                self.kube(['-n', NS, 'exec', 'deployment/' + self.host_probe_owner, '--', 'rm', '-f', self.host_probe])
        finally:
            super().close()

    def inspect(self, path):
        raw = self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--',
                         self.remote_probe, 'service-state', path])
        value = json.loads(raw)
        m.require(value['root_sha256'] == self.root['certificate_fingerprint_sha256'] and not value['pending'],
                  'managed Account Manager state is not current')
        return value

    def client_rows(self, subject=SUBJECT):
        m.require(subject in (SUBJECT, 'service:certissuer', 'service:pki-controller'), 'unexpected client scope')
        raw = self.sql("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,subject,caller,status,fingerprint,issued_at,revoked_at "
                       "FROM pki_service_client_issuances WHERE environment='dev' AND subject='" + subject + "' "
                       "ORDER BY issued_at,request_id) t;")
        return [json.loads(line) for line in raw.splitlines() if line]

    def server_rows(self, host=a.ACCOUNT_DNS):
        raw = self.sql("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,caller,status,fingerprint,dns_names,issued_at,revoked_at "
                       "FROM pki_server_issuances WHERE environment='dev' AND domain='service' "
                       "ORDER BY issued_at,request_id) t;")
        return [row for row in (json.loads(line) for line in raw.splitlines() if line)
                if row['dns_names'] == [host]]

    def v3(self):
        # The listener was introduced with Service v3, but it must continue
        # qualifying against the sole active Service issuer after later
        # hierarchy rotations.  The activation record remains an audited
        # lineage prerequisite; live policy is always selected from the
        # registry rather than its historical version number.
        source = Path(self.args.activation)
        m.require(m.read(source / 'report.json')['status'] == 'passed',
                  'successful Service activation evidence required')
        page = self.api('/issuers/search', {'limit': 100})
        issuer = current_service_issuer(page['items'])
        m.require(self.api('/issuers/' + issuer['issuer_id']) == issuer,
                  'active Service issuer changed during Account Manager lifecycle preflight')
        return issuer

    def probe_binary(self):
        binary = self.output / 'pki-dev-probe-linux'
        if not binary.exists():
            result = m.subprocess.run(['go', 'build', '-trimpath', '-o', str(binary), './pki-dev-probe'],
                cwd=m.WORKSPACE / 'scripts/go', env=dict(os.environ, GOOS='linux', GOARCH='amd64', CGO_ENABLED='0', GOWORK='off'),
                capture_output=True, timeout=180)
            m.require(result.returncode == 0, 'session probe build failed')
        return binary

    def install_probe_at(self, namespace, deployment, container, path):
        binary = self.probe_binary()
        self.kube(['-n', namespace, 'exec', '-i', 'deployment/' + deployment, '-c', container, '--', 'sh', '-c',
                   'umask 077; base64 -d > ' + path + ' && chmod 700 ' + path],
                  base64.b64encode(binary.read_bytes()).decode())
        actual = self.kube(['-n', namespace, 'exec', 'deployment/' + deployment, '-c', container, '--',
                            'sha256sum', path]).split()[0]
        m.require(actual == m.digest(binary.read_bytes()), 'installed session probe digest differs')
        return actual

    def install_probe(self):
        actual = self.install_probe_at(AM_NS, 'account-manager', 'pkimanagement', self.remote_probe)
        self.probe_installed = True
        self.save('probe-binary.json', {'sha256': actual})

    def install_factory_probe(self):
        actual = self.install_probe_at(NS, 'factoryenroll', 'factoryenroll', self.factory_probe)
        self.factory_probe_installed = True
        self.save('factory-probe-binary.json', {'sha256': actual})

    def session(self, identity):
        process = m.Process(self.k + ['-n', AM_NS, 'exec', '-i', 'deployment/account-manager', '-c', 'pkimanagement', '--',
            self.remote_probe, 'service-session', CLIENT_STATE, '/run/pki-root/root.pem',
            'certissuer.' + NS + '.svc', '9443', '/healthz', identity['fingerprint']], keep_stdin=True)
        self.children.append(process)
        event = process.event()
        m.require(event['event'] == 'ready' and event['fingerprint'] == identity['fingerprint'],
                  'held Account Manager client session differs')
        return process

    def controller_session(self, identity):
        # This 403 is intentional: service:account-manager is authenticated at
        # TLS but is not a CRL-consumer principal. Its real controller method
        # carries a signed human assertion through the private Unix socket.
        process = m.Process(self.k + ['-n', AM_NS, 'exec', '-i', 'deployment/account-manager', '-c', 'pkimanagement', '--',
            self.remote_probe, 'service-session', CLIENT_STATE, '/run/pki-root/root.pem',
            'pki-controller.' + NS + '.svc', '18446', '/v1/pki/issuers/' + self.issuer['issuer_id'] + '/crl',
            identity['fingerprint'], '403'], keep_stdin=True)
        self.children.append(process)
        event = process.event()
        m.require(event['event'] == 'ready' and event['fingerprint'] == identity['fingerprint'],
                  'held Account Manager controller TLS session differs')
        m.require(str(event.get('local_port', '')).isdigit(), 'held controller session local port missing')
        process.local_port = int(event['local_port'])
        return process

    def controller_connection_count(self, ignored_local_port=None):
        # 18446 is the controller's fixed Service port. Count only established
        # sidecar egress sockets; addresses and credential material never leave
        # the owner container.
        excluded = ''
        if ignored_local_port is not None:
            m.require(isinstance(ignored_local_port, int) and 0 < ignored_local_port < 65536,
                      'invalid controller probe local port')
            excluded = ' && $2 !~ /:' + format(ignored_local_port, '04X') + '$/'
        raw = self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--', 'sh', '-c',
                         "awk 'NR > 1 && $4 == \"01\" && $3 ~ /:480E$/" + excluded +
                         " { count++ } END { print count + 0 }' /proc/net/tcp /proc/net/tcp6"])
        return int(raw.strip())

    def controller_management_request(self, ignored_local_port=None):
        # This is the actual Account Manager management method. It causes the
        # app to sign a human assertion, pass it through its private socket and
        # use the sidecar's managed controller client; no probe receives a
        # bearer token or private key.
        record = self.api('/issuers/' + self.issuer['issuer_id'])
        m.require(record['issuer_id'] == self.issuer['issuer_id'], 'Account Manager controller response differs')
        connections = self.controller_connection_count(ignored_local_port)
        m.require(connections > 0, 'signed Account Manager controller request left no held managed connection')
        return connections

    def factory_session(self, host, port, expected_server=None, path='/healthz'):
        # Bind the held socket to the Factory identity currently owned by the
        # real Factory process. The probe loads the same private state without
        # exporting its key.
        identity = json.loads(self.kube(['-n', NS, 'exec', 'deployment/factoryenroll', '-c', 'factoryenroll', '--',
                                         self.factory_probe, 'service-state', FACTORY_STATE]))
        m.require(identity['root_sha256'] == self.root['certificate_fingerprint_sha256'] and not identity['pending'],
                  'managed Factory identity is not current')
        # This listener deliberately has no public health route. Its exact 403
        # proves an established mTLS socket, not business-route authorization.
        baseline = ['403'] if host == a.ACCOUNT_DNS else []
        process = m.Process(self.k + ['-n', NS, 'exec', '-i', 'deployment/factoryenroll', '-c', 'factoryenroll', '--',
            self.factory_probe, 'service-session', FACTORY_STATE, FACTORY_ROOT,
            host, port, path, identity['fingerprint']] + baseline, keep_stdin=True)
        self.children.append(process)
        event = process.event()
        m.require(event['event'] == 'ready' and event['fingerprint'] == identity['fingerprint'],
                  'held Factory session differs')
        if expected_server is not None:
            m.require(event.get('server_fingerprint') == expected_server['fingerprint'],
                      'held Factory session reached an unexpected Account Manager listener')
        return process

    def session_command(self, process, command, expected):
        process.child.stdin.write(command + '\n')
        process.child.stdin.flush()
        event = process.event(timeout=35)
        m.require(event['event'] == expected, 'held Account Manager session event differs')
        return event

    def stop_session(self, process):
        # kubectl's exec stdin may not deliver EOF promptly. End the remote
        # probe explicitly, then verify the owned command has actually exited.
        self.session_command(process, 'quit', 'stopped')
        process.child.stdin.close()
        m.require(process.child.wait(timeout=20) == 0, 'session probe did not stop')

    def wait_changed(self, path, before):
        deadline = time.monotonic() + 90
        while True:
            current = self.inspect(path)
            if current['fingerprint'] != before['fingerprint']:
                return current
            m.require(time.monotonic() < deadline, 'Account Manager renewal did not complete; do not signal again')
            time.sleep(2)

    def current_successors(self, client, host):
        for identity, rows, kind in ((client, self.client_rows(), 'client'), (host, self.server_rows(), 'listener')):
            selected = [row for row in rows if row['fingerprint'] == identity['fingerprint']]
            m.require(len(selected) == 1 and selected[0]['issuer_id'] == self.issuer['issuer_id']
                      and selected[0]['status'] == 'succeeded' and selected[0]['revoked_at'] is None,
                      'current Account Manager ' + kind + ' is not admitted')
        peer = json.loads(m.command([self.probe, 'tls-peer', self.output / 'root.pem', a.ACCOUNT_DNS, self.ports['listener'][0]]))
        m.require(peer['peer_sha256'] == host['fingerprint'], 'Account Manager listener does not serve its successor')

    def sync_account_manager_crls(self, entries):
        desired = json.dumps(consumer_crl_entries(
            entries, '/var/lib/account-pki/private/account-listener-crl-{issuer_id}.json'))
        digest = m.digest(desired.encode())
        name = 'account-manager-service-crls-' + digest[:12]
        current = self.obj('deployment', 'account-manager', AM_NS)
        volumes = current['spec']['template']['spec']['volumes']
        selected = [volume for volume in volumes if volume['name'] == 'account-service-crls']
        m.require(len(selected) == 1 and selected[0].get('configMap', {}).get('name'),
                  'Account Manager Service CRL volume differs')
        existing = self.kube(['-n', AM_NS, 'get', 'configmap', name, '-o', 'json', '--ignore-not-found'])
        created = False
        if existing:
            saved = json.loads(existing)
            m.require(saved.get('immutable') and saved.get('data', {}).get('crls.json') == desired,
                      'Account Manager Service CRL manifest name is already bound to other data')
        else:
            self.create({'apiVersion': 'v1', 'kind': 'ConfigMap',
                         'metadata': {'name': name, 'namespace': AM_NS}, 'immutable': True,
                         'data': {'crls.json': desired}})
            created = True
        return digest, name, created

    def sync_service_crl_consumers(self, issuer_id=None):
        source = self.obj('configmap', 'pki-service-client-crls')
        entries = json.loads(source['data']['crls.json'])
        m.require(issuer_id is None or any(entry['issuer']['issuer_id'] == issuer_id for entry in entries),
                  'retired issuer is absent from canonical Service CRL manifest')
        targets = (
            ('factoryenroll-service-crls', 'crls.json', '/state/identity/crl-{issuer_id}.json', 'factoryenroll'),
            ('video-cloud-api-pki-trust', 'service-crls.json', '/run/pki-state/service-{issuer_id}-crl.json', 'video-cloud-api-pki'),
        )
        # Prove every workload can accept a restart before changing any
        # manifest. ConfigMap projection is live, but a listener only reads a
        # changed manifest after its rollout; do not strand a healthy workload
        # behind an unrelated unavailable consumer.
        deployments = {}
        for _, _, _, deployment in targets:
            current = self.obj('deployment', deployment)
            m.require(current['spec']['replicas'] == 1 and current.get('status', {}).get('readyReplicas') == 1
                      and current.get('status', {}).get('observedGeneration') == current['metadata'].get('generation'),
                      'Service CRL consumer is not ready: ' + deployment)
            deployments[(NS, deployment)] = current
        account = self.obj('deployment', 'account-manager', AM_NS)
        m.require(account['spec']['replicas'] == 1 and account.get('status', {}).get('readyReplicas') == 1
                  and account.get('status', {}).get('observedGeneration') == account['metadata'].get('generation'),
                  'Service CRL consumer is not ready: account-manager')
        deployments[(AM_NS, 'account-manager')] = account
        changed = False
        desired_by_deployment = {}
        for name, key, state_path, deployment in targets:
            before = self.obj('configmap', name)
            m.require(key in before.get('data', {}), 'Service CRL consumer manifest key is missing: ' + name)
            desired = json.dumps(consumer_crl_entries(entries, state_path))
            desired_by_deployment[(NS, deployment)] = (m.digest(desired.encode()), None)
            if before['data'][key] != desired:
                self.scoped_patch('configmap', before, [
                    {'op': 'test', 'path': '/data/' + key, 'value': before['data'][key]},
                    {'op': 'replace', 'path': '/data/' + key, 'value': desired},
                ])
                changed = True
        account_digest, account_manifest, account_manifest_created = self.sync_account_manager_crls(entries)
        changed = changed or account_manifest_created
        desired_by_deployment[(AM_NS, 'account-manager')] = (account_digest, account_manifest)
        rollouts = []
        for (namespace, name), (digest, manifest) in desired_by_deployment.items():
            before = deployments[(namespace, name)]
            annotations = before['spec']['template'].get('metadata', {}).get('annotations', {})
            current_manifest = None
            if manifest:
                selected = [volume for volume in before['spec']['template']['spec']['volumes']
                            if volume['name'] == 'account-service-crls']
                m.require(len(selected) == 1, 'Account Manager Service CRL volume differs')
                current_manifest = selected[0].get('configMap', {}).get('name')
            if annotations.get(CONSUMER_MANIFEST_ANNOTATION) != digest or current_manifest != manifest:
                template = json.loads(json.dumps(before['spec']['template']))
                template.setdefault('metadata', {}).setdefault('annotations', {})[CONSUMER_MANIFEST_ANNOTATION] = digest
                if manifest:
                    target = next(volume for volume in template['spec']['volumes'] if volume['name'] == 'account-service-crls')
                    target['configMap']['name'] = manifest
                self.scoped_patch('deployment', before, [
                    {'op': 'test', 'path': '/spec/template', 'value': before['spec']['template']},
                    {'op': 'replace', 'path': '/spec/template', 'value': template},
                ])
                rollouts.append(name)
            # A failure can occur after the template patch but before this
            # wait. Recheck every consumer on retry even when the ConfigMap
            # already contains the desired manifest.
            self.kube(['-n', namespace, 'rollout', 'status', 'deployment/' + name, '--timeout=240s'], timeout=250)
        self.save('service-crl-consumer-sync.json', {
            'issuer_id': issuer_id, 'consumer_manifests_changed': changed,
            'consumer_rollouts_started': rollouts,
            'consumers': ['factory-enroll', 'video-cloud-api', 'account-manager'],
        })

    def require_service_crl_consumers_current(self, issuer_id):
        source = self.obj('configmap', 'pki-service-client-crls')
        entries = json.loads(source['data']['crls.json'])
        m.require(any(entry['issuer']['issuer_id'] == issuer_id for entry in entries),
                  'retired issuer is absent from canonical Service CRL manifest')
        expected = []
        for name, key, state_path, deployment in (
                ('factoryenroll-service-crls', 'crls.json', '/state/identity/crl-{issuer_id}.json', 'factoryenroll'),
                ('video-cloud-api-pki-trust', 'service-crls.json', '/run/pki-state/service-{issuer_id}-crl.json', 'video-cloud-api-pki')):
            desired = json.dumps(consumer_crl_entries(entries, state_path))
            current = self.obj('configmap', name)
            m.require(current.get('data', {}).get(key) == desired,
                      'Service CRL manifest drifted during held sessions: ' + name)
            expected.append((NS, deployment, m.digest(desired.encode()), None))
        account_desired = json.dumps(consumer_crl_entries(
            entries, '/var/lib/account-pki/private/account-listener-crl-{issuer_id}.json'))
        account_digest = m.digest(account_desired.encode())
        account_manifest = 'account-manager-service-crls-' + account_digest[:12]
        raw = self.kube(['-n', AM_NS, 'get', 'configmap', account_manifest, '-o', 'json', '--ignore-not-found'])
        saved = json.loads(raw) if raw else {}
        m.require(saved.get('immutable') and saved.get('data', {}).get('crls.json') == account_desired,
                  'Account Manager Service CRL manifest drifted during held sessions')
        expected.append((AM_NS, 'account-manager', account_digest, account_manifest))
        for namespace, name, digest, manifest in expected:
            deployment = self.obj('deployment', name, namespace)
            m.require(deployment['spec']['replicas'] == 1 and deployment.get('status', {}).get('readyReplicas') == 1
                      and deployment.get('status', {}).get('observedGeneration') == deployment['metadata'].get('generation'),
                      'Service CRL consumer became unavailable during held sessions: ' + name)
            annotations = deployment['spec']['template'].get('metadata', {}).get('annotations', {})
            m.require(annotations.get(CONSUMER_MANIFEST_ANNOTATION) == digest,
                      'Service CRL consumer template drifted during held sessions: ' + name)
            if manifest:
                volumes = deployment['spec']['template']['spec']['volumes']
                selected = [volume for volume in volumes if volume['name'] == 'account-service-crls']
                m.require(len(selected) == 1 and selected[0].get('configMap', {}).get('name') == manifest,
                          'Account Manager Service CRL volume drifted during held sessions')

    def recover_interrupted_lifecycle(self):
        source = Path(self.args.recover)
        failed = m.read(source / 'report.json')
        m.require(failed['status'] == 'failed' and failed['phase'] == 'lifecycle'
                  and (('revoke-' in failed.get('failure', '') and 'status 403' in failed['failure'])
                       or failed.get('failure') == 'consumer receipt deadline'
                       or failed.get('failure') == 'rotated Account Manager controller connection was not evicted'),
                  'recovery requires the recorded lifecycle interruption')
        baseline, renewed = m.read(source / 'baseline.json'), m.read(source / 'renewed.json')
        self.preflight_lifecycle()
        self.root = self.active_root()
        self.save('root.pem', self.root['certificate_pem'])
        self.issuer = self.v3()
        self.sync_service_crl_consumers()
        self.forward('listener', AM_NS, a.SERVICE_NAME, 8443)
        self.install_probe()
        client, host = self.inspect(CLIENT_STATE), self.inspect(HOST_STATE)
        m.require(client == renewed['client'] and host == renewed['listener'],
                  'Account Manager state changed after the interrupted lifecycle')
        for rows, before, current, kind in ((self.client_rows(), baseline['client'], client, 'client'),
                                            (self.server_rows(), baseline['listener'], host, 'listener')):
            retired = [row for row in rows if row['fingerprint'] == before['fingerprint']]
            admitted = [row for row in rows if row['fingerprint'] == current['fingerprint'] and row['revoked_at'] is None]
            m.require(len(retired) == 1 and len(admitted) == 1,
                      'recorded interrupted Account Manager ' + kind + ' pair no longer matches live registry state')
        self.revocation_started = dt.datetime.now(dt.timezone.utc)
        retired_clients = self.revoke_and_publish('client', self.client_rows(), client, baseline['client'])
        retired_hosts = self.revoke_and_publish('server', self.server_rows(), host, baseline['listener'])
        self.current_successors(client, host)
        self.restart_account_manager()
        # The probe is deliberately ephemeral in the sidecar's filesystem and
        # is removed by the Recreate restart. Reinstall it before inspecting
        # the persisted successor state.
        self.install_probe()
        self.forward('listener', AM_NS, a.SERVICE_NAME, 8443)
        m.require(self.inspect(CLIENT_STATE) == client and self.inspect(HOST_STATE) == host,
                  'Account Manager recovery restart changed successor state')
        self.current_successors(client, host)
        self.check('interrupted_account_manager_lifecycle_reconciled', {
            'source': source.name, 'client_retired': len(retired_clients), 'listener_retired': len(retired_hosts),
            'successor_survived': True, 'bootstrap_free_restart': True, 'private_keys_exported': False})

    def recover_pending_client_issuance(self):
        # A transport loss after the provider signs leaves the owner-local key
        # and durable claim intact. Reconcile only that exact claim through the
        # existing administrator-authorized controller route; restarting the
        # owner then consumes its normal idempotent renewal receipt.
        self.preflight_lifecycle()
        self.root = self.active_root()
        self.issuer = self.v3()
        self.install_probe()
        pending_state = self.managed_client_state()
        installed_before_resume = not pending_state.get('pending')
        if installed_before_resume:
            identity = self.inspect(CLIENT_STATE)
            m.require(self.sql("SELECT count(*) FROM pki_service_client_issuances WHERE environment='dev' AND subject='" +
                               SUBJECT + "' AND status='issuing';") == '0',
                      'another Account Manager client issuance is still pending')
            pending = self.current_client_claim(identity['fingerprint'])
        else:
            pending = self.pending_client_claim(pending_state['pending_request_id'])
        m.require(pending['issuer_id'] == self.issuer['issuer_id'],
                  'recoverable Account Manager claim is not under the active issuer')
        serial = None
        if pending['status'] == 'issuing':
            serial = self.provider_serial_for_pending_claim(pending)
            self.api('/issuers/' + self.issuer['issuer_id'] + '/reconcile-service-client',
                     {'caller': pending['caller'], 'request_id': pending['request_id'],
                      'serial_number': serial}, role='approver')
            pending = self.pending_client_claim(pending['request_id'])
        m.require(pending['status'] == 'succeeded' and pending['fingerprint'] and pending['revoked_at'] is None,
                  'Account Manager pending claim was not reconciled')
        self.restart_account_manager()
        self.install_probe()
        identity = self.inspect(CLIENT_STATE)
        rows = self.client_rows()
        reconciled = [row for row in rows if row['request_id'] == pending['request_id']]
        m.require(len(reconciled) == 1 and reconciled[0]['status'] == 'succeeded'
                  and reconciled[0]['fingerprint'] == identity['fingerprint'] and reconciled[0]['revoked_at'] is None,
                  'reconciled Account Manager successor was not installed')
        self.check('pending_account_manager_client_issuance_reconciled', {
            'request_id': pending['request_id'], 'provider_serial': serial,
            'already_reconciled_before_resume': installed_before_resume,
            'successor_fingerprint': identity['fingerprint'],
            'bootstrap_free_restart': True, 'private_keys_exported': False})

    def managed_client_state(self):
        return json.loads(self.kube([
            '-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--',
            self.remote_probe, 'service-state', CLIENT_STATE]))

    def pending_client_state(self):
        value = self.managed_client_state()
        m.require(value.get('pending') and re.fullmatch(r'[0-9a-f-]{36}', value.get('pending_request_id', '')),
                  'Account Manager has no recoverable pending client state')
        return value

    def current_client_claim(self, fingerprint):
        m.require(re.fullmatch(r'[0-9a-f]{64}', fingerprint), 'invalid current Account Manager fingerprint')
        raw = self.sql(
            "SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,subject,caller,status,"
            "fingerprint,revoked_at FROM pki_service_client_issuances WHERE environment='dev' "
            "AND subject='" + SUBJECT + "' AND fingerprint='" + fingerprint + "') t;")
        rows = [json.loads(line) for line in raw.splitlines() if line]
        m.require(len(rows) == 1 and rows[0]['caller'] == SUBJECT and rows[0]['status'] == 'succeeded'
                  and rows[0]['revoked_at'] is None, 'current Account Manager state is not registry admitted')
        return rows[0]

    def pending_client_claim(self, request_id):
        """Return the one retained claim, including its CSR only in owner memory."""
        m.require(re.fullmatch(r'[0-9a-f-]{36}', request_id), 'invalid pending Account Manager request id')
        raw = self.sql(
            "SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,subject,caller,status,"
            "csr_pem,request_digest,ttl_days,created_at,fingerprint,revoked_at FROM "
            "pki_service_client_issuances WHERE environment='dev' AND subject='" + SUBJECT + "' "
            "AND request_id='" + request_id + "') t;")
        rows = [json.loads(line) for line in raw.splitlines() if line]
        m.require(len(rows) == 1 and rows[0]['caller'] == SUBJECT and rows[0]['revoked_at'] is None
                  and rows[0]['status'] in ('issuing', 'succeeded') and rows[0]['csr_pem'],
                  'one pending Account Manager claim is required')
        return rows[0]

    def provider_serial_for_pending_claim(self, claim):
        """Match an already-signed provider leaf without exporting a key or certificate."""
        m.require(claim['subject'] == SUBJECT and claim['status'] == 'issuing'
                  and re.fullmatch(r'[0-9a-f-]{36}', claim['issuer_id'])
                  and re.fullmatch(r'[0-9a-f-]{36}', claim['request_id']),
                  'unexpected pending Account Manager claim')
        issuer = self.api('/issuers/' + claim['issuer_id'])
        mount = issuer.get('signer_reference', '')
        m.require(re.fullmatch(r'pki-issuers/service/[0-9a-f-]{36}/v[0-9]+', mount),
                  'unexpected Service provider mount')
        expected = m.command([self.openssl, 'req', '-pubkey', '-noout'], claim['csr_pem']).strip()
        serials = json.loads(self.bao(['list', '-format=json', mount + '/certs']))
        m.require(isinstance(serials, list) and len(serials) == len(set(serials)),
                  'invalid Service provider certificate inventory')
        matches = []
        for serial in serials:
            m.require(re.fullmatch(r'[0-9a-fA-F:-]+', serial), 'invalid provider certificate serial')
            record = json.loads(self.bao(['read', '-format=json', mount + '/cert/' + serial])).get('data', {})
            actual = m.command([self.openssl, 'x509', '-pubkey', '-noout'], record.get('certificate', '')).strip()
            if actual == expected:
                m.require(record.get('revocation_time') == 0, 'matching provider certificate is revoked')
                matches.append(serial)
        # The serial is public certificate metadata. Keep the CSR, leaf and
        # provider responses in process memory; evidence carries only counts.
        inventory = {
            'issuer_id': claim['issuer_id'], 'request_id': claim['request_id'],
            'certificate_count': len(serials), 'matching_serials': matches,
            'private_keys_exported': False}
        if len(matches) == 1:
            self.save('pending-provider-inventory.json', inventory)
            return matches[0]
        m.require(not matches, 'ambiguous pending Account Manager provider certificates')
        # The provider has no record for the original CSR.  Re-sign exactly
        # once only after all original Certissuer owners are fenced; the
        # durable marker prevents a later interrupted invocation from issuing
        # another certificate for this request.
        serial = self.resign_missing_pending_claim(claim, issuer, mount, expected)
        inventory.update({'recovery_signed': True, 'matching_serials': [serial]})
        self.save('pending-provider-inventory.json', inventory)
        return serial

    def resign_missing_pending_claim(self, claim, issuer, mount, expected_public_key):
        created = m.parse_time(claim['created_at'])
        now = dt.datetime.now(dt.timezone.utc)
        pods = json.loads(self.kube(['-n', NS, 'get', 'pods', '-l',
                                     'app.kubernetes.io/name=certissuer', '-o', 'json']))['items']
        m.require(pods and all(
            not pod['metadata'].get('deletionTimestamp')
            and pod['status']['phase'] == 'Running'
            and m.parse_time(pod['status']['startTime']) > created
            and now - m.parse_time(pod['status']['startTime']) > dt.timedelta(minutes=5)
            for pod in pods), 'original Certissuer owner is not fenced for pending recovery')
        expires = min(created + dt.timedelta(days=claim['ttl_days']),
                      m.parse_time(issuer['not_after']) - dt.timedelta(days=30))
        ttl = int((expires - now).total_seconds()) - 60
        m.require(ttl > 60, 'pending Account Manager certificate validity window has elapsed')
        marker = self.base / 'pki' / ('account-manager-provider-recovery-' + claim['request_id'] + '.json')
        marker.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        with marker.open('x') as stream:
            json.dump({'request_id': claim['request_id'], 'request_digest': claim['request_digest'],
                       'issuer_id': claim['issuer_id'], 'at': m.stamp(),
                       'certissuer_pod_uids': [pod['metadata']['uid'] for pod in pods]}, stream)
            stream.flush()
            os.fsync(stream.fileno())
        directory = os.open(marker.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
        jwt = self.kube(['-n', NS, 'create', 'token', 'certissuer-pki',
                         '--audience=openbao', '--duration=10m']).strip()
        login = json.loads(self.bao(['write', '-format=json', 'auth/kubernetes/login', '-'],
                                    json.dumps({'role': 'certissuer-pki-dev', 'jwt': jwt})))
        token = login['auth']['client_token']
        try:
            signed = json.loads(self.bao(['write', '-format=json', mount + '/sign/service-client', '-'],
                                         json.dumps({'csr': claim['csr_pem'], 'common_name': SUBJECT,
                                                     'exclude_cn_from_sans': True, 'ttl': str(ttl) + 's'}),
                                         token=token))['data']
            actual = m.command([self.openssl, 'x509', '-pubkey', '-noout'], signed['certificate']).strip()
            m.require(actual == expected_public_key and re.fullmatch(r'[0-9a-fA-F:-]+', signed['serial_number']),
                      'pending recovery provider result differs')
            return signed['serial_number']
        finally:
            self.bao(['write', 'auth/token/revoke', '-'], json.dumps({'token': token}))

    def fence_pending_provider_signer(self):
        """Replace the Dev signer before a one-time missing-provider recovery."""
        self.preflight_lifecycle()
        self.root = self.active_root()
        self.issuer = self.v3()
        self.install_probe()
        claim = self.pending_client_claim(self.pending_client_state()['pending_request_id'])
        m.require(claim['status'] == 'issuing', 'pending provider fence requires an unreconciled claim')
        m.require(claim['issuer_id'] == self.issuer['issuer_id'],
                  'pending Account Manager claim is not under the active issuer')
        before = self.obj('deployment', 'certissuer', NS)
        m.require(before['spec']['replicas'] == 1 and before.get('status', {}).get('readyReplicas') == 1,
                  'certissuer is not ready for a controlled fence')
        template = json.loads(json.dumps(before['spec']['template']))
        template.setdefault('metadata', {}).setdefault('annotations', {})[
            'rtk.cloud/pki-pending-provider-fence'] = self.output.name
        self.scoped_patch('deployment', before, [
            {'op': 'test', 'path': '/spec/template', 'value': before['spec']['template']},
            {'op': 'replace', 'path': '/spec/template', 'value': template},
        ])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/certissuer', '--timeout=240s'], timeout=250)
        after = self.obj('deployment', 'certissuer', NS)
        m.require(after['metadata']['uid'] == before['metadata']['uid']
                  and after['status'].get('readyReplicas') == 1
                  and after['status'].get('observedGeneration') == after['metadata']['generation'],
                  'certissuer fence rollout differs')
        self.check('pending_provider_signer_fenced', {
            'request_id': claim['request_id'], 'certissuer_restarted': True,
            'drain_seconds_required_before_recovery': 300, 'staging_touched': False})

    def revoke_and_publish(self, kind, rows, current, predecessor, held=(), keepalive=None):
        action = 'service-client' if kind == 'client' else 'server'
        targets = [row for row in rows if row['fingerprint'] == predecessor['fingerprint']]
        m.require(targets, 'no replaced Account Manager ' + kind + ' leaves to retire')
        # A renewal can move the Account Manager identity to the current Service
        # intermediate.  Its predecessor must still be revoked through the
        # intermediate that issued it; sending an old fingerprint to the new
        # issuer is correctly denied by the controller.
        issuer_ids = {row['issuer_id'] for row in targets}
        m.require(len(issuer_ids) == 1, 'replaced Account Manager ' + kind + ' leaves span multiple issuers')
        issuer_id = issuer_ids.pop()
        previous = self.api('/issuers/' + issuer_id + '/crl')
        if keepalive:
            keepalive()
        self.save(kind + '-previous-crl.json', previous)
        for index, row in enumerate(targets):
            body = {'certificate_sha256': row['fingerprint'], 'reason': 'Replaced Account Manager ' + kind + ' leaf'}
            self.save(kind + '-revocation-' + str(index) + '-intent.json', body)
            if row['revoked_at'] is None:
                self.api('/issuers/' + issuer_id + '/revoke-' + action, body, role='approver')
            if keepalive:
                keepalive()
        if held:
            cutoffs = {}
            for destination, process in held:
                closed = self.session_command(process, 'closed', 'closed')
                delay = (m.parse_time(closed['at']) - self.revocation_started).total_seconds()
                m.require(0 <= delay <= 30, 'replaced Account Manager client cutoff exceeded 30 seconds: ' + destination)
                denied = self.session_command(process, 'denied', 'denied')
                cutoffs[destination] = {'cutoff_seconds_upper_bound': delay, 'fresh_old_denied': denied}
            self.save('held-client-cutoff.json', cutoffs)
        final = previous
        for row in targets:
            final = self.api('/issuers/' + issuer_id + '/publish-' + action + '-revocation',
                             {'certificate_sha256': row['fingerprint']}, role='approver')
            if keepalive:
                keepalive()
            self.require_service_crl_consumers_current(issuer_id)
            if keepalive:
                keepalive()
            self.wait_receipts(issuer_id, final['crl_sha256'],
                               ['certissuer', 'factory-enroll', 'pki-controller', 'video-cloud-api'], 'crl')
            if keepalive:
                keepalive()
        for row in targets:
            result = self.api('/issuers/' + issuer_id + '/finalize-' + action + '-revocation',
                              {'certificate_sha256': row['fingerprint']}, role='approver')
            m.require(result['crl_sha256'] == final['crl_sha256'], 'Account Manager retirement finalization differs')
            if keepalive:
                keepalive()
        self.save(kind + '-retirement.json', {'issuer_id': issuer_id,
                                              'targets': [row['fingerprint'] for row in targets],
                                              'crl_sha256': final['crl_sha256']})
        return targets

    def lifecycle(self):
        self.preflight_lifecycle()
        self.root = self.active_root()
        self.save('root.pem', self.root['certificate_pem'])
        self.issuer = self.v3()
        self.sync_service_crl_consumers()
        self.forward('listener', AM_NS, a.SERVICE_NAME, 8443)
        self.install_probe()
        self.install_factory_probe()
        client_before, host_before = self.inspect(CLIENT_STATE), self.inspect(HOST_STATE)
        client_rows_before, host_rows_before = self.client_rows(), self.server_rows()
        self.save('baseline.json', {'client': client_before, 'listener': host_before})
        controller_before = self.controller_management_request()
        held = self.session(client_before)
        held_controller = self.controller_session(client_before)
        listener_held = self.factory_session(a.ACCOUNT_DNS, '8443', host_before)
        control_held = self.factory_session('certissuer.' + NS + '.svc', '9443')
        self.session_command(listener_held, 'check', 'alive')
        self.session_command(control_held, 'check', 'alive')
        m.require(self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--', 'cat', '/proc/1/comm']).strip() == 'pkimanagement',
                  'PID 1 is not the Account Manager identity owner')
        self.save('renewal-intent.json', {'at': m.stamp(), 'client': client_before['fingerprint'], 'listener': host_before['fingerprint']})
        self.listener_rotation_started = dt.datetime.now(dt.timezone.utc)
        self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--', 'sh', '-c',
                   'test "$(cat /proc/1/comm)" = pkimanagement && kill -HUP 1'])
        client_after, host_after = self.wait_changed(CLIENT_STATE, client_before), self.wait_changed(HOST_STATE, host_before)
        client_rows_after, host_rows_after = self.client_rows(), self.server_rows()
        client_receipt = replacement(client_before, client_after, client_rows_after, self.issuer, 'client')
        host_receipt = replacement(host_before, host_after, host_rows_after, self.issuer, 'listener')
        self.save('renewed.json', {'client': client_after, 'listener': host_after,
                                   'client_request_id': client_receipt['request_id'], 'listener_request_id': host_receipt['request_id']})
        deadline = time.monotonic() + 30
        while self.controller_connection_count(held_controller.local_port) != 0:
            m.require(time.monotonic() < deadline, 'rotated Account Manager controller connection was not evicted')
            time.sleep(1)
        controller_successor = self.controller_management_request(held_controller.local_port)
        listener_closed = self.session_command(listener_held, 'closed', 'closed')
        listener_cutoff = (m.parse_time(listener_closed['at']) - self.listener_rotation_started).total_seconds()
        m.require(0 <= listener_cutoff <= 30, 'replaced Account Manager listener cutoff exceeded 30 seconds')
        listener_successor = self.factory_session(a.ACCOUNT_DNS, '8443', host_after)
        self.session_command(listener_successor, 'check', 'alive')
        self.session_command(control_held, 'check', 'alive')
        self.save('held-listener-cutoff.json', {
            'cutoff_seconds_upper_bound': listener_cutoff,
            'old_server_fingerprint': host_before['fingerprint'],
            'successor_server_fingerprint': host_after['fingerprint'],
            'factory_control_survived': True,
        })
        successor = self.session(client_after)
        self.session_command(held, 'check', 'alive')
        self.session_command(held_controller, 'check', 'alive')
        self.session_command(successor, 'check', 'alive')
        self.current_successors(client_after, host_after)
        self.revocation_started = dt.datetime.now(dt.timezone.utc)
        retired_clients = self.revoke_and_publish('client', client_rows_after, client_after, client_before,
                                                  (('certissuer', held), ('pki-controller', held_controller)))
        self.session_command(successor, 'check', 'alive')
        m.require(self.controller_management_request() > 0, 'successor controller connection did not survive predecessor retirement')
        retired_hosts = self.revoke_and_publish('server', host_rows_after, host_after, host_before)
        self.current_successors(client_after, host_after)
        self.session_command(successor, 'check', 'alive')
        for process in (held, held_controller, successor, listener_held, listener_successor, control_held):
            self.stop_session(process)
        self.restart_account_manager()
        self.install_probe()
        self.forward('listener', AM_NS, a.SERVICE_NAME, 8443)
        m.require(self.inspect(CLIENT_STATE) == client_after and self.inspect(HOST_STATE) == host_after,
                  'Account Manager restart changed successor state')
        self.current_successors(client_after, host_after)
        restarted_listener = self.factory_session(a.ACCOUNT_DNS, '8443', host_after)
        self.session_command(restarted_listener, 'check', 'alive')
        self.stop_session(restarted_listener)
        self.check('account_manager_listener_lifecycle', {'client_retired': len(retired_clients),
                   'listener_retired': len(retired_hosts), 'held_old_client_cutoff': True,
                   'controller_signed_human_request': True,
                   'controller_idle_connections_before_rotation': controller_before,
                   'controller_idle_connections_after_rotation': controller_successor,
                   'held_old_listener_cutoff': True, 'factory_control_survived': True,
                   'successor_survived': True, 'bootstrap_free_restart': True, 'private_keys_exported': False})

    def host_lifecycle(self, name):
        """Exercise the same held-listener contract on the two core hosts."""
        m.require(name in ('certissuer', 'pki-controller'), 'unexpected host scope')
        self.report['foundation_scope'] = 'Dev ' + name + ' held listener, renewal, retirement and restart'
        self.preflight_lifecycle()
        self.root = self.active_root()
        self.save('root.pem', self.root['certificate_pem'])
        self.issuer = self.v3()
        self.sync_service_crl_consumers()
        self.install_factory_probe()
        self.host_probe = '/var/lib/pki-host/identity/.t11-probe-' + uuid.uuid4().hex
        self.install_probe_at(NS, name, name, self.host_probe)
        self.host_probe_owner = name
        host, port = name + '.' + NS + '.svc', '9443' if name == 'certissuer' else '18446'
        path = '/v1/pki/issuers/' + self.root['issuer_id'] + '/crl' if name == 'pki-controller' else '/healthz'
        paths = {'client': '/var/lib/pki-host/identity/client.json', 'listener': '/var/lib/pki-host/identity/state.json'}

        def inspect():
            result = {}
            for kind, path in paths.items():
                result[kind] = json.loads(self.kube(['-n', NS, 'exec', 'deployment/' + name, '--', self.host_probe, 'service-state', path]))
                m.require(result[kind]['root_sha256'] == self.root['certificate_fingerprint_sha256'], 'host root differs')
            return result

        before = inspect()
        m.require(not any(value['pending'] for value in before.values()), 'host has a pending renewal; reconcile first')
        self.save('baseline.json', before)
        held = self.factory_session(host, port, before['listener'], path)
        control = self.factory_session(a.ACCOUNT_DNS, '8443')
        self.session_command(held, 'check', 'alive')
        self.session_command(control, 'check', 'alive')
        owner = self.obj('deployment', name)
        expected_process = 'pkicontroller' if name == 'pki-controller' else 'certissuer'
        m.require(self.kube(['-n', NS, 'exec', 'deployment/' + name, '--', 'cat', '/proc/1/comm']).strip() == expected_process,
                  'unexpected host process')
        started = dt.datetime.now(dt.timezone.utc)
        self.save('renewal-intent.json', {'target': name, 'at': started.isoformat(), 'baseline': before})
        self.kube(['-n', NS, 'exec', 'deployment/' + name, '--', 'sh', '-c', 'kill -HUP 1'])
        deadline = time.monotonic() + 180
        while True:
            after = inspect()
            if all(not after[kind]['pending'] and after[kind]['fingerprint'] != before[kind]['fingerprint'] for kind in paths):
                break
            m.require(time.monotonic() < deadline, 'host renewal pending; retain intent and do not signal again')
            time.sleep(2)
        client_rows, host_rows = self.client_rows('service:' + name), self.server_rows(host)
        replacement(before['client'], after['client'], client_rows, self.issuer, 'client')
        replacement(before['listener'], after['listener'], host_rows, self.issuer, 'listener')
        self.save('renewed.json', after)
        closed = self.session_command(held, 'closed', 'closed')
        delay = (m.parse_time(closed['at']) - started).total_seconds()
        m.require(0 <= delay <= 30, 'host listener cutoff exceeded 30 seconds')
        successor = self.factory_session(host, port, after['listener'], path)
        self.session_command(successor, 'check', 'alive')
        self.session_command(control, 'check', 'alive')
        self.save('held-listener-cutoff.json', {'target': name, 'cutoff_seconds_upper_bound': delay,
            'old_server_fingerprint': before['listener']['fingerprint'], 'successor_server_fingerprint': after['listener']['fingerprint'],
            'account_manager_control_survived': True})
        self.revocation_started = dt.datetime.now(dt.timezone.utc)
        def keepalive():
            # Account Manager has a 30-second idle timeout. Keep the same
            # control socket active while publication and receipts complete.
            self.session_command(successor, 'check', 'alive')
            self.session_command(control, 'check', 'alive')
        self.revoke_and_publish('client', client_rows, after['client'], before['client'], keepalive=keepalive)
        self.revoke_and_publish('server', host_rows, after['listener'], before['listener'], keepalive=keepalive)
        for process in (held, successor, control):
            self.stop_session(process)
        current = self.obj('deployment', name)
        m.require(current['metadata']['uid'] == owner['metadata']['uid'] and current['spec'] == owner['spec'], 'host changed during qualification')
        m.require(current['spec']['strategy']['type'] == 'Recreate' and current['spec']['replicas'] == 1, 'expected one Recreate owner')
        self.kube(['-n', NS, 'exec', 'deployment/' + name, '--', 'sh', '-c', 'test ! -s /var/lib/pki-host/seed/key.pem'])
        template = json.loads(json.dumps(current['spec']['template']))
        template.setdefault('metadata', {}).setdefault('annotations', {})['rtk.cloud/pki-dev-acceptance'] = self.output.name
        self.scoped_patch('deployment', current, [{'op': 'test', 'path': '/spec/template', 'value': current['spec']['template']},
                                                 {'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + name, '--timeout=240s'], timeout=250)
        m.require(inspect() == after, 'host restart changed successor state')
        restarted = self.factory_session(host, port, after['listener'], path)
        self.session_command(restarted, 'check', 'alive')
        self.stop_session(restarted)
        self.check('core_host_listener_lifecycle', {'target': name, 'held_old_listener_cutoff': True,
            'account_manager_control_survived': True, 'client_and_server_retired': True,
            'successor_survived': True, 'bootstrap_free_restart': True, 'private_keys_exported': False})

    def restart_account_manager(self):
        before = self.obj('deployment', 'account-manager', AM_NS)
        m.require(before['spec']['strategy']['type'] == 'Recreate' and before['spec']['replicas'] == 1,
                  'expected one Account Manager Recreate owner')
        template = json.loads(json.dumps(before['spec']['template']))
        annotations = template.setdefault('metadata', {}).setdefault('annotations', {})
        annotations['rtk.cloud/pki-dev-acceptance'] = self.output.name
        self.scoped_patch('deployment', before, [{'op': 'test', 'path': '/spec/template', 'value': before['spec']['template']},
                                                  {'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', AM_NS, 'rollout', 'status', 'deployment/account-manager', '--timeout=240s'], timeout=250)

    def preflight_lifecycle(self):
        m.require(self.kube(['config', 'current-context']).strip() == self.context, 'canonical dev context mismatch')
        for name, ns in [('account-manager', AM_NS), ('certissuer', NS), ('pki-controller', NS), ('factoryenroll', NS)]:
            deployment = self.obj('deployment', name, ns)
            m.require(deployment['spec']['replicas'] == 1 and deployment.get('status', {}).get('readyReplicas') == 1
                      and deployment['status'].get('observedGeneration') == deployment['metadata']['generation'],
                      'deployment is not ready: ' + name)
        account = self.obj('deployment', 'account-manager', AM_NS)
        sidecar = next(c for c in account['spec']['template']['spec']['containers'] if c['name'] == 'pkimanagement')
        env = {e['name']: e.get('value') for e in sidecar['env']}
        m.require(env.get('PKI_MANAGEMENT_IDENTITY_STATE') == CLIENT_STATE
                  and env.get('PKI_MANAGEMENT_ACCOUNT_HOST_IDENTITY_STATE') == HOST_STATE
                  and env.get('PKI_MANAGEMENT_ACCOUNT_LISTEN_ADDR') == ':8443',
                  'Account Manager listener runtime differs')
        workspace = Path(__file__).resolve().parents[2]
        result = m.subprocess.run(['go', 'build', '-o', str(self.probe), './pki-dev-probe'], cwd=workspace / 'scripts/go',
                                  env=dict(os.environ, GOWORK='off'), capture_output=True, timeout=180)
        m.require(result.returncode == 0, 'listener probe build failed')
        self.forward('am', AM_NS, 'account-manager', 80)
        self.forward('issuer', NS, 'certissuer', 9443)
        self.forward('api', NS, 'video-cloud-api-pki', 8443)
        self.forward('mqtt', NS, 'mqtt-pki', 8883)
        self.accounts = m.read(self.foundation / 'accounts.json')
        self.check('preflight', {'context': self.context, 'account_listener_ready': True, 'staging_touched': False})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=str(Path.home() / '.config/rtk_cloud'))
    parser.add_argument('--authority', required=True)
    parser.add_argument('--activation', required=True)
    parser.add_argument('--image', required=True)
    parser.add_argument('--output', required=True)
    parser.add_argument('--target', choices=['account-manager', 'certissuer', 'pki-controller'], default='account-manager')
    parser.add_argument('--recover', type=Path,
                        help='recover only the exact predecessor pair recorded by an unauthorized lifecycle interruption')
    parser.add_argument('--recover-pending', action='store_true',
                        help='reconcile one current Account Manager pending client issuance through the controller')
    parser.add_argument('--fence-pending-provider', action='store_true',
                        help='restart Dev certissuer before one-time recovery of a provider-missing pending issuance')
    args = parser.parse_args()
    args.resume = args.resume_after_deploy = args.resume_after_listener = False
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = AccountListenerLifecycle(args)
    try:
        if args.recover:
            m.require(args.target == 'account-manager', 'recorded recovery is Account Manager only')
            runner.recover_interrupted_lifecycle()
        elif args.recover_pending:
            m.require(args.target == 'account-manager', 'pending recovery is Account Manager only')
            runner.recover_pending_client_issuance()
        elif args.fence_pending_provider:
            m.require(args.target == 'account-manager', 'pending provider fence is Account Manager only')
            runner.fence_pending_provider_signer()
        elif args.target != 'account-manager':
            runner.host_lifecycle(args.target)
        else:
            runner.lifecycle()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'; runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close(); os.close(fd)
        print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    def interrupted(*_):
        raise RuntimeError('interrupted; reconcile saved Account Manager renewal/revocation evidence before retry')
    signal.signal(signal.SIGTERM, interrupted); signal.signal(signal.SIGINT, interrupted)
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=os.sys.stderr); raise SystemExit(1)
