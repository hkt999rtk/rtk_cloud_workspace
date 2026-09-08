#!/usr/bin/env python3
"""Repeatable dev-only PKI acceptance. All credentials and raw responses stay private."""
import argparse
import base64
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import queue
import re
import shlex
import signal
import subprocess
import threading
import time
import traceback
import urllib.error
import urllib.request
import uuid

NS = 'video-cloud-dev-video-cloud'
CONSUMERS = ['pkibroker', 'video-cloud-api']
ROOT_STATE = '/run/pki-state/device/root-policy.json'
WORKSPACE = Path(__file__).resolve().parents[2]


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def device_consumers(env):
    domain_keys = ['PKI_REQUIRED_CONSUMERS_' + domain for domain in ('DEVICE', 'APP', 'SERVICE', 'MQTT', 'OPENBAO_TLS')]
    domain_mode = any(env.get(key) for key in domain_keys)
    require(not (domain_mode and env.get('PKI_REQUIRED_CONSUMERS')), 'mixed consumer policy modes')
    value = env.get('PKI_REQUIRED_CONSUMERS_DEVICE' if domain_mode else 'PKI_REQUIRED_CONSUMERS', '')
    return sorted(value.split(','))


def certissuer_server_name(env):
    host = env.get('CERT_ISSUER_HOST_NAME')
    if not host:
        require(not env.get('CERT_ISSUER_HOST_IDENTITY_STATE'), 'managed issuer host name missing')
        return 'certissuer.' + NS + '.svc.cluster.local'
    require(host == 'certissuer.' + NS + '.svc' and env.get('CERT_ISSUER_HOST_IDENTITY_STATE')
            and re.fullmatch('[0-9a-f]{64}', env.get('CERT_ISSUER_HOST_ROOT_SHA256', '')), 'managed issuer host policy differs')
    return host


def digest(raw):
    return hashlib.sha256(raw.encode() if isinstance(raw, str) else raw).hexdigest()


def stamp(value=None):
    return (value or dt.datetime.now(dt.timezone.utc)).isoformat(timespec='seconds').replace('+00:00', 'Z')


def parse_time(value):
    # PostgreSQL may serialize UTC as +00; Python 3.9 needs +00:00.
    value = re.sub(r'([+-]\d{2})$', r'\1:00', value)
    # Go emits RFC3339Nano; Python 3.9 accepts only 3 or 6 fractional digits.
    value = re.sub(r'\.(\d+)(?=Z$|[+-]\d{2}:\d{2}$)',
                   lambda match: '.' + match.group(1)[:6].ljust(6, '0'), value)
    return dt.datetime.fromisoformat(value.replace('Z', '+00:00'))


def read(path):
    return json.loads(Path(path).read_text())


def write(path, value):
    path = Path(path)
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    raw = value if isinstance(value, str) else json.dumps(value, indent=2) + '\n'
    # O_EXCL + rename prevents following an existing output symlink.
    temporary = path.with_name('.' + path.name + '-' + uuid.uuid4().hex)
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, 'w') as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def command(args, body=None, cwd=None, timeout=90):
    try:
        result = subprocess.run([str(a) for a in args], input=body, text=True,
                                capture_output=True, cwd=cwd, timeout=timeout)
    except subprocess.TimeoutExpired:
        raise RuntimeError('command observation timed out; reconcile before replay') from None
    # Arguments, stdout and stderr may contain credentials; never include them here.
    require(result.returncode == 0, 'command failed: ' + Path(str(args[0])).name)
    return result.stdout


class Process:
    """Own one child and drain lines in a thread (no buffered-read/select race)."""
    def __init__(self, args, body=None):
        self.child = subprocess.Popen([str(a) for a in args], stdin=subprocess.PIPE,
                                      stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        self.lines = queue.Queue()
        def drain():
            for line in self.child.stdout:
                self.lines.put(line)
            self.lines.put(None)
        self.thread = threading.Thread(target=drain, daemon=True)
        self.thread.start()
        try:
            if body is not None:
                self.child.stdin.write(body + '\n')
            self.child.stdin.close()
        except BaseException:
            self.close()
            raise

    def line(self, timeout=20):
        try:
            line = self.lines.get(timeout=timeout)
        except queue.Empty:
            raise RuntimeError('child event deadline') from None
        require(line is not None, 'child ended before expected event')
        return line

    def event(self, timeout=20):
        line = self.line(timeout)
        try:
            return json.loads(line)
        except ValueError:
            # Only our local Go probe uses this method; its errors never include
            # configuration values, passwords, tokens or certificate contents.
            raise RuntimeError('probe failed: ' + line.strip()[:200]) from None

    def stable(self, seconds):
        try:
            self.lines.get(timeout=seconds)
        except queue.Empty:
            require(self.child.poll() is None, 'session process ended')
            return
        raise RuntimeError('session ended before stability deadline')

    def close(self):
        if self.child.poll() is None:
            self.child.terminate()
            try:
                self.child.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.child.kill()
                self.child.wait(timeout=5)
        self.thread.join(timeout=2)
        self.child.stdout.close()


class Acceptance:
    def __init__(self, config_root, context, output, resume=False):
        self.base = Path(config_root).expanduser().resolve() / 'dev'
        self.foundation = self.base / 'pki/fresh-rehearsal'
        self.output = Path(output).resolve()
        if resume:
            prior = read(self.output / 'report.json')
            require(prior['status'] == 'failed' and prior['environment'] == 'dev' and prior['context'] == context,
                    'only a failed run in the same dev context may be reconciled')
            if resume not in ('ready', 'enrolled', 'revocation', 'recovery'):
                require(not (self.output / 'signed').exists(), 'signing already attempted: reconcile manually')
            write(self.output / ('report-before-resume-' + uuid.uuid4().hex + '.json'), prior)
        else:
            self.output.mkdir(mode=0o700, parents=True, exist_ok=False)
        self.context = context
        self.k = ['kubectl', '--kubeconfig', str(self.base / 'kube/kubeconfig.yaml'), '--context', context]
        self.children = []
        self.ports = {}
        self.report = {'environment': 'dev', 'context': context, 'status': 'running',
                       'run_id': self.output.name, 'checks': {},
                       'foundation_scope': 'Existing reviewed Root/Brand and controller baseline; not a cold bootstrap rerun',
                       'not_qualified': ['legacy migration', 'staging', 'independent human custody', 'SDK/hardware']}
        for name in ('preflight', 'provider_key_custody', 'activation_missing_broker_denied', 'product_active',
                     'enrollment', 'renewal_replacement', 'replacement_restart', 'worker_fault_fail_closed',
                     'revocation_missing_broker_denied', 'fault_restored', 'revocation_recovery_restart', 'run_cleanup'):
            self.report['checks'][name] = {'status': 'not_run'}
        if resume:
            self.report['checks'] = prior['checks']
            self.report['resumed_after_failure'] = prior.get('failure')
        self.save('report.json', self.report)
        self.openssl = '/opt/homebrew/opt/openssl@3/bin/openssl'
        require(Path(self.openssl).is_file(), 'OpenSSL 3 prerequisite missing')
        self.probe = self.output / 'pki-dev-probe'
        self.ceremony = self.output / 'pkiceremony'

    def save(self, name, value):
        write(self.output / name, value)

    def check(self, name, evidence):
        self.report['checks'][name] = {'status': 'passed', 'at': stamp(), 'evidence': evidence}
        self.save('report.json', self.report)
        print(json.dumps({'check': name, 'status': 'passed'}), flush=True)

    def kube(self, args, body=None, timeout=90):
        return command(self.k + args, body, timeout=timeout)

    def obj(self, kind, name, ns=NS):
        return json.loads(self.kube(['-n', ns, 'get', kind, name, '-o', 'json']))

    def forward(self, name, ns, service, port):
        if name in self.ports:
            self.ports[name][1].close()
        obj = self.obj('service', service, ns)
        require(any(p['port'] == port for p in obj['spec']['ports']), 'service port changed')
        child = Process(self.k + ['-n', ns, 'port-forward', '--address=127.0.0.1',
                                  'service/' + service, ':' + str(port)])
        self.children.append(child)
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            match = re.search(r'Forwarding from 127\.0\.0\.1:(\d+) ->', child.line())
            if match:
                self.ports[name] = (match.group(1), child)
                return
        raise RuntimeError('port-forward startup deadline')

    def http(self, path, data=None, token=None, expected=200, key=None, service='am'):
        headers = {'Content-Type': 'application/json'}
        if token:
            headers['Authorization'] = 'Bearer ' + token
        if key:
            headers['Idempotency-Key'] = key
        request = urllib.request.Request('http://127.0.0.1:' + self.ports[service][0] + '/v1' + path,
                                         data=None if data is None else json.dumps(data).encode(), headers=headers)
        # No redirects/proxies: keep credentials on this owned loopback forward.
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, *args, **kwargs):
                return None
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        try:
            with opener.open(request, timeout=45) as response:
                status, raw = response.status, response.read((1 << 20) + 1)
        except urllib.error.HTTPError as error:
            status, raw = error.code, error.read((1 << 20) + 1)
        except (OSError, urllib.error.URLError):
            raise RuntimeError('HTTP transport failed; reconcile mutation before replay') from None
        require(len(raw) <= 1 << 20, 'oversized HTTP response')
        require(status == expected, f'HTTP {path}: status {status}, expected {expected}')
        return json.loads(raw) if raw and status < 400 else None

    def login(self, credentials):
        response = self.http('/auth/login', {k: credentials[k] for k in ('email', 'password')})
        token = response['tokens']['access_token']
        decode = lambda value: json.loads(base64.urlsafe_b64decode(value + '=' * (-len(value) % 4)))
        require(decode(token.split('.')[0])['alg'] == 'RS256', 'login algorithm changed')
        require(not decode(token.split('.')[1]).get('mfa', False), 'MFA unexpectedly enabled')
        return token, response['user']['id']

    def token(self, role='requester'):
        return self.login(self.accounts[role])[0]

    def api(self, path, data=None, expected=200, role='requester', key=None):
        return self.http('/platform/pki' + path, data, self.token(role), expected, key)

    def preflight(self):
        # Explicitly check both selected context and dev resource ownership before any mutation.
        require(self.kube(['config', 'current-context']).strip() == self.context, 'canonical dev context mismatch')
        namespace = self.obj('namespace', NS)
        require(namespace['metadata']['name'] == NS, 'wrong namespace')
        images = {}
        running_images = {}
        for deployment, ns in [('pki-controller', NS), ('mqtt-pki', NS), ('video-cloud-api-pki', NS),
                               ('certissuer', NS), ('factoryenroll', NS),
                               ('account-manager', 'video-cloud-dev-account-manager')]:
            obj = self.obj('deployment', deployment, ns)
            require(obj.get('status', {}).get('readyReplicas', 0) == obj['spec']['replicas'] > 0,
                    'deployment not ready: ' + deployment)
            containers = obj['spec']['template']['spec']['containers']
            require(obj['status'].get('observedGeneration') == obj['metadata']['generation']
                    and obj['status'].get('updatedReplicas') == obj['spec']['replicas'], 'deployment rollout is incomplete')
            selector = ','.join(k + '=' + v for k, v in obj['spec']['selector']['matchLabels'].items())
            pods = json.loads(self.kube(['-n', ns, 'get', 'pods', '-l', selector, '-o', 'json']))['items']
            pods = [p for p in pods if not p['metadata'].get('deletionTimestamp')]
            require(len(pods) == obj['spec']['replicas'], 'unexpected live pod count')
            running_images[deployment] = []
            for pod in pods:
                statuses = {c['name']: c for c in pod['status'].get('containerStatuses', [])}
                for container in containers:
                    state = statuses.get(container['name'], {})
                    require(state.get('ready') and state.get('imageID', '').endswith(container['image'].split('@')[-1]),
                            'running image/ready status differs from pinned deployment')
                    running_images[deployment].append({'pod_uid': pod['metadata']['uid'],
                                                       'container': container['name'], 'image_id': state['imageID']})
            images[deployment] = {c['name']: c['image'] for c in containers}
            require(all('@sha256:' in c['image'] for c in containers), 'image not pinned: ' + deployment)
            if deployment == 'pki-controller':
                env = {v['name']: v.get('value') for c in containers for v in c.get('env', [])}
                require(device_consumers(env) == CONSUMERS,
                        'required consumer set changed')
            if deployment == 'certissuer':
                env = {v['name']: v.get('value') for c in containers for v in c.get('env', [])}
                self.certissuer_host = certissuer_server_name(env)
        broker_config = self.obj('configmap', 'mqtt-pki-config')['data']['base.hocon']
        require(broker_config.count('dest_topic = "_bc/${username}/$1"') == 2,
                'broker publish/subscribe tenant rewrite differs from reviewed baseline')
        self.forward('am', 'video-cloud-dev-account-manager', 'account-manager', 80)
        self.accounts = read(self.foundation / 'accounts.json')
        identities = {name: self.login(credentials)[1] for name, credentials in self.accounts.items()}
        require(len(set(identities.values())) == 3, 'approval accounts are not distinct')
        for role in ('requester', 'approver', 'custodian'):
            self.api('/issuers/search', {'limit': 1}, role=role)
        secret = self.obj('secret', 'account-manager-runtime', 'video-cloud-dev-account-manager')['data']
        self.admin = {name: base64.b64decode(secret[key]).decode() for name, key in (
            ('email', 'ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL'),
            ('password', 'ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD'))}
        self.brand = read(self.foundation / 'brand-active.json')
        self.root = read(self.foundation / 'device-root-active.json')
        for label, saved in [('brand', self.brand), ('device-root', self.root)]:
            current = self.api('/issuers/' + saved['issuer_id'])
            require(current['status'] == 'active' and current['trust_domain'] == 'device'
                    and current['environment'] == 'dev'
                    and current['trust_bundle_version'] == saved['trust_bundle_version'], 'baseline issuer changed')
            self.save(label + '.json', current)
            self.save(label + '.pem', current['certificate_pem'])
            public = command([self.openssl, 'x509', '-in', self.output / (label + '.pem'), '-pubkey', '-noout'])
            key_public = command([self.openssl, 'pkey', '-in', self.foundation / (label + '-offline-simulation/ca-key.encrypted.pem'),
                                  '-passin', 'file:' + str(self.base / 'pki/rehearsal-passphrases' / label), '-pubout'])
            require(public == key_public, 'offline key does not match reviewed certificate')
            crl = self.api('/issuers/' + saved['issuer_id'] + '/crl')
            require(dt.datetime.fromisoformat(crl['next_update'].replace('Z', '+00:00')) >
                    dt.datetime.now(dt.timezone.utc) + dt.timedelta(hours=1), 'baseline CRL needs reviewed refresh')
            self.save(label + '-baseline-crl.json', crl)
        env = dict(os.environ, GOWORK='off')
        for directory, target, output in [(WORKSPACE / 'scripts/go', './pki-dev-probe', self.probe),
                                           (WORKSPACE / 'repos/rtk_video_cloud', './cmd/pkiceremony', self.ceremony)]:
            result = subprocess.run(['go', 'build', '-o', str(output), target], cwd=directory,
                                    env=env, capture_output=True, timeout=180)
            require(result.returncode == 0, 'acceptance binary build failed')
        for name, service, port in [('api', 'video-cloud-api-pki', 8443), ('issuer', 'certissuer', 9443),
                                     ('factory', 'factoryenroll', 80), ('mqtt', 'mqtt-pki', 8883)]:
            self.forward(name, NS, service, port)
        ca = self.obj('secret', 'certissuer-runtime')['data']['client-ca.crt']
        self.save('issuer-ca.pem', base64.b64decode(ca).decode())
        self.check('preflight', {'images': images, 'running_images': running_images, 'approval_accounts': identities,
                                'offline_root_brand_key_match': True,
                                'workspace_head': command(['git', 'rev-parse', 'HEAD'], cwd=WORKSPACE).strip(),
                                'tool_source_sha256': {str(p.relative_to(WORKSPACE)): digest(p.read_bytes()) for p in
                                                      [Path(__file__).resolve(), WORKSPACE / 'scripts/go/pki-dev-probe/main.go']},
                                'service_head': command(['git', 'rev-parse', 'HEAD'], cwd=WORKSPACE / 'repos/rtk_video_cloud').strip()})

    def close(self):
        for child in reversed(self.children):
            child.close()

    def bao(self, args, body='', token=None):
        script = ('read -r BAO_TOKEN; export BAO_TOKEN; '
                  'export BAO_ADDR=https://openbao.video-cloud-dev-secrets.svc:8200; '
                  'export BAO_CACERT=/openbao/tls/ca.crt; exec bao ' + shlex.join(args))
        if token is None:
            token = (self.base / 'openbao/root-token').read_text().strip()
        return self.kube(['-n', 'video-cloud-dev-secrets', 'exec', '-i', 'openbao-0',
                          '--', 'sh', '-c', script], token + '\n' + body)

    def approval(self, operation):
        path = '/operations/' + operation['operation_id']
        for account, role in [('approver', 'pki_admin'), ('custodian', 'security_custodian')]:
            self.api(path + '/approvals', {'request_sha256': operation['request_sha256'], 'role': role}, 204, account)
        require(self.api(path)['status'] == 'approved', 'operation not approved')

    def role_policy(self, role_name, policy_name, policy=None):
        role_path = 'auth/kubernetes/role/' + role_name
        before = json.loads(self.bao(['read', '-format=json', role_path]))['data']
        require(before['token_no_default_policy'] and before['audience'] == 'openbao'
                and before['bound_service_account_namespaces'] == [NS], 'unexpected provider role binding')
        role = dict(before)
        policies = list(before['token_policies'])
        if policy is not None:
            require(policy_name not in json.loads(self.bao(['policy', 'list', '-format=json'])), 'run policy already exists')
            self.bao(['policy', 'write', policy_name, '-'], policy)
            require(self.bao(['policy', 'read', policy_name]).strip() == policy.strip(), 'provider policy differs')
            policies.append(policy_name)
        else:
            require(policy_name in policies, 'run policy missing from role')
            policies.remove(policy_name)
        role['token_policies'] = policies
        require(json.loads(self.bao(['read', '-format=json', role_path]))['data'] == before, 'provider role changed concurrently')
        self.bao(['write', role_path, '-'], json.dumps(role))
        require(json.loads(self.bao(['read', '-format=json', role_path]))['data'] == role, 'provider role update differs')
        if policy is None:
            self.bao(['policy', 'delete', policy_name])
        self.save('role-' + role_name + '.json', role)

    def patch(self, kind, name, old, changes):
        patches = [{'op': 'test', 'path': '/metadata/resourceVersion', 'value': old['metadata']['resourceVersion']}]
        updated = json.loads(self.kube(['-n', NS, 'patch', kind, name, '--type=json',
                                       '--patch-file=/dev/stdin', '-o', 'json'], json.dumps(patches + changes)))
        clean = {k: updated[k] for k in ('apiVersion', 'kind')}
        clean['metadata'] = {k: updated['metadata'][k] for k in ('name', 'namespace')}
        for key in ('spec', 'data', 'type'):
            if key in updated:
                clean[key] = updated[key]
        directory = self.base / 'pki/controller-bootstrap/rollout'
        write(directory / (name + '-' + updated['kind'].lower() + '.json'), clean)
        if name == 'video-cloud-api-pki-trust':
            write(directory / (name + '.json'), clean)
        return updated

    def restart(self, deployment):
        old = self.obj('deployment', deployment)
        require(old['spec']['strategy']['type'] == 'Recreate' and old['spec']['replicas'] == 1,
                'expected single writer Recreate deployment')
        annotations = dict(old['spec']['template']['metadata'].get('annotations', {}))
        annotations['rtk.cloud/pki-dev-acceptance'] = uuid.uuid4().hex
        self.patch('deployment', deployment, old, [{'op': 'add', 'path': '/spec/template/metadata/annotations', 'value': annotations}])
        self.kube(['-n', NS, 'rollout', 'status', 'deployment/' + deployment, '--timeout=180s'], timeout=190)
        if deployment == 'video-cloud-api-pki':
            self.forward('api', NS, deployment, 8443)
        if deployment == 'mqtt-pki':
            self.forward('mqtt', NS, deployment, 8883)
        # Deployment readiness can precede Service/callback route convergence.
        device = read(self.foundation / 'device-2/enroll-request.json')['devid']
        auth = self.auth(self.foundation / 'device-2/v4', device)
        attempts = self.wait_positive_mqtt(auth, device)
        self.report.setdefault('rollout_auth_checks', []).append({'deployment': deployment, 'attempts': attempts, 'at': stamp()})
        self.save('report.json', self.report)

    def manifest(self, deployment, field, change):
        name = deployment + '-trust'
        old = self.obj('configmap', name)
        entries = json.loads(old['data'][field])
        updated = change(entries)
        if updated == entries:
            return
        self.patch('configmap', name, old, [{'op': 'test', 'path': '/data/' + field, 'value': old['data'][field]},
                                          {'op': 'replace', 'path': '/data/' + field, 'value': json.dumps(updated)}])
        self.restart(deployment)

    def receipts(self, issuer, value, kind='bundle'):
        require(str(uuid.UUID(issuer)) == issuer and re.fullmatch('[0-9a-f]{64}', value), 'invalid receipt query identity')
        table, field = ('pki_bundle_acknowledgments', 'bundle_version') if kind == 'bundle' else ('pki_crl_acknowledgments', 'digest')
        query = f"SELECT consumer_id FROM {table} WHERE issuer_id='{issuer}' AND {field}='{value}' ORDER BY consumer_id;"
        return self.kube(['-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--',
                          'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'video_cloud', '-At'], query).split()

    def wait_receipts(self, issuer, value, expected, kind='bundle', absent=None):
        deadline = time.monotonic() + 90
        while True:
            actual = self.receipts(issuer, value, kind)
            require(absent is None or absent not in actual, 'failed consumer issued an unexpected receipt')
            if actual == expected:
                return actual
            require(time.monotonic() < deadline, 'consumer receipt deadline')
            time.sleep(3)

    def provision(self):
        cloud = read(self.foundation / 'cloud.json')['id']
        suffix = uuid.uuid4().hex[:16]
        body = {'profile_key': 'pki-' + suffix, 'display_name': 'Disposable PKI acceptance ' + suffix,
                'category': 'mqtt_device', 'ca_profile': 'pki-product', 'issuer_profile': 'pki-product-issuer',
                'service_options': ['mqtt']}
        self.save('product-request.json', body)
        profile = self.http('/admin/brand-clouds/' + cloud + '/device-item-profiles', body,
                            self.login(self.admin)[0], 201)['device_item_profile']
        self.save('product.json', profile)
        self.profile = profile
        operation = self.api('/operations', {'environment': 'dev', 'trust_domain': 'device', 'kind': 'product',
                             'brand_cloud_id': cloud, 'device_item_profile_id': profile['id'],
                             'parent_issuer_id': self.brand['issuer_id']}, key='dev-acceptance-' + suffix)
        self.save('provision-operation.json', operation)
        self.approval(operation)
        path = '/operations/' + operation['operation_id']
        issuer = self.api('/issuers/' + operation['issuer_id'])
        require(issuer['status'] == 'approved' and issuer['issuer_version'] == 1, 'new Product was not isolated')
        require(issuer['signer_reference'] + '/' not in json.loads(self.bao(['secrets', 'list', '-format=json'])),
                'provider mount already exists; reconcile, do not reprovision')
        policies = json.loads(self.kube(['-n', NS, 'exec', 'deployment/pki-controller', '--',
                                        '/app/pkicontroller', 'render-openbao-policy', issuer['issuer_id']]))
        require(policies['mount'] == issuer['signer_reference'] and policies['environment'] == 'dev', 'policy scope differs')
        self.save('provider-policies.json', policies)
        self.role_policy('pki-controller-dev', 'pki-controller-dev-' + issuer['issuer_id'], policies['controller_policy'])
        self.api(path + '/provision', {})  # Exactly once: failures are preserved, never blindly replayed.
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        require(issuer['status'] == 'provisioning' and issuer['csr_pem'], 'CSR not durably recorded')
        self.save('provisioning.json', issuer)
        self.finish_provision(issuer, operation, policies)

    def resume_provision(self):
        operation = read(self.output / 'provision-operation.json')
        issuer = read(self.output / 'provisioning.json')
        self.profile = read(self.output / 'product.json')
        require(self.api('/issuers/' + issuer['issuer_id']) == issuer and issuer['status'] == 'provisioning',
                'live provisioned issuer differs; reconcile manually')
        current_op = self.api('/operations/' + operation['operation_id'])
        require(current_op['request_sha256'] == operation['request_sha256'] and current_op['issuer_id'] == issuer['issuer_id'],
                'provision operation binding changed')
        require(issuer['device_item_profile_id'] == self.profile['id'] and issuer['parent_issuer_id'] == self.brand['issuer_id'],
                'resume Product/Brand binding differs')
        self.finish_provision(issuer, operation, read(self.output / 'provider-policies.json'))

    def finish_provision(self, issuer, operation, policies):
        path = '/operations/' + operation['operation_id']
        keys = json.loads(self.bao(['list', '-format=json', issuer['signer_reference'] + '/keys']))
        require(len(keys) == 1, 'provider did not create exactly one internal key')
        jwt = self.kube(['-n', NS, 'create', 'token', 'pki-controller', '--audience=openbao', '--duration=10m']).strip()
        login = json.loads(self.bao(['write', '-format=json', 'auth/kubernetes/login', '-'],
                                   json.dumps({'role': 'pki-controller-dev', 'jwt': jwt})))
        provider_token = login['auth']['client_token']
        try:
            for suffix in ('keys', 'key/' + keys[0], 'sign/device', 'sign/default', 'sign-verbatim/default', 'intermediate/generate/exported'):
                result = json.loads(self.bao(['write', '-format=json', 'sys/capabilities', '-'],
                                            json.dumps({'token': provider_token, 'paths': [issuer['signer_reference'] + '/' + suffix]})))
                expected = ['list'] if suffix == 'keys' else ['deny']
                require(result['data']['capabilities'] == expected, 'controller provider capability differs from design')
        finally:
            self.bao(['write', 'auth/token/revoke', '-'], json.dumps({'token': provider_token}))
        self.check('provider_key_custody', {'internal_key_count': 1, 'controller_key_read_export_and_sign_denied': True})
        csr_digest = digest(base64.b64decode(''.join(issuer['csr_pem'].splitlines()[1:-1])))
        env_path = os.environ.get('PATH', '')
        try:
            os.environ['PATH'] = str(Path(self.openssl).parent) + os.pathsep + env_path
            command([self.ceremony, 'sign', '--issuer', self.output / 'provisioning.json',
                     '--expected-request-sha256', operation['request_sha256'], '--expected-csr-sha256', csr_digest,
                     '--parent', self.output / 'brand.json', '--expected-parent-sha256', self.brand['certificate_fingerprint_sha256'],
                     '--key', self.foundation / 'brand-offline-simulation/ca-key.encrypted.pem',
                     '--passphrase-file', self.base / 'pki/rehearsal-passphrases/brand', '--out', self.output / 'signed'])
        finally:
            os.environ['PATH'] = env_path
        self.api(path + '/import', {'certificate_pem': (self.output / 'signed/certificate.pem').read_text()}, 204)
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        require(issuer['status'] == 'ready', 'import did not reach ready')
        self.save('ready.json', issuer)
        self.activate_product(issuer, operation, policies)

    def resume_ready(self):
        operation = read(self.output / 'provision-operation.json')
        issuer = read(self.output / 'ready.json')
        self.profile = read(self.output / 'product.json')
        require(self.api('/issuers/' + issuer['issuer_id']) == issuer and issuer['status'] == 'ready',
                'live ready issuer differs; reconcile manually')
        require(issuer['certificate_pem'] == (self.output / 'signed/certificate.pem').read_text(), 'signed certificate differs')
        current = self.api('/operations/' + operation['operation_id'])
        require(current['request_sha256'] == operation['request_sha256'] and current['issuer_id'] == issuer['issuer_id']
                and issuer['device_item_profile_id'] == self.profile['id'] and issuer['parent_issuer_id'] == self.brand['issuer_id'],
                'ready operation or Product/Brand binding differs')
        self.activate_product(issuer, operation, read(self.output / 'provider-policies.json'))

    def activate_product(self, issuer, operation, policies):
        path = '/operations/' + operation['operation_id']
        self.api(path + '/activate', {}, 409)
        ref = {'issuer_id': issuer['issuer_id'], 'trust_bundle_version': issuer['trust_bundle_version']}
        self.manifest('video-cloud-api-pki', 'issuers.json', lambda entries: entries + [ref])
        self.wait_receipts(issuer['issuer_id'], issuer['trust_bundle_version'], ['video-cloud-api'], absent='pkibroker')
        self.api(path + '/activate', {}, 409)
        require(self.api('/issuers/' + issuer['issuer_id'])['status'] == 'ready', 'receipt gate changed issuer status')
        self.check('activation_missing_broker_denied', {'issuer_id': issuer['issuer_id'], 'http_status': 409})
        entry = {'issuer': issuer, 'state_path': '/run/pki-state/device/' + issuer['issuer_id'] + '-crl.json'}
        self.manifest('mqtt-pki', 'crls.json', lambda entries: entries + [entry])
        self.wait_receipts(issuer['issuer_id'], issuer['trust_bundle_version'], CONSUMERS)
        self.api(path + '/activate', {}, 204)
        self.issuer = self.api('/issuers/' + issuer['issuer_id'])
        require(self.issuer['status'] == 'active', 'activation failed')
        self.save('active.json', self.issuer)
        provider = json.loads(self.bao(['read', '-format=json', issuer['signer_reference'] + '/cert/crl']))
        self.crl = self.api('/issuers/' + issuer['issuer_id'] + '/crl', {'crl_pem': provider['data']['certificate']})
        self.save('product-crl.json', self.crl)
        self.role_policy('certissuer-pki-dev', 'pki-signer-dev-' + issuer['issuer_id'], policies['signer_policy'])
        entry = {'issuer': self.issuer, 'state_path': '/run/pki-state/' + issuer['issuer_id'] + '-crl.json'}
        self.manifest('video-cloud-api-pki', 'crls.json', lambda entries: entries + [entry])
        self.wait_receipts(issuer['issuer_id'], self.crl['crl_sha256'], CONSUMERS, 'crl')
        self.check('product_active', {'issuer_id': issuer['issuer_id'], 'required_receipts': CONSUMERS, 'provider_key_count': 1})

    def key(self, name, device):
        command([self.openssl, 'genpkey', '-algorithm', 'EC', '-pkeyopt', 'ec_paramgen_curve:P-256',
                 '-out', self.output / (name + '-key.pem')])
        command([self.openssl, 'req', '-new', '-key', self.output / (name + '-key.pem'),
                 '-subj', '/CN=' + device, '-out', self.output / (name + '-csr.pem')])
        return (self.output / (name + '-csr.pem')).read_text()

    def tls(self, service, path, body, identity, expected=200):
        host, ca = ('video-cloud-api-pki.' + NS + '.svc', self.base / 'pki/servers/video-cloud-api-pki/ca.crt') if service == 'api' else (
            getattr(self, 'certissuer_host', 'certissuer.' + NS + '.svc.cluster.local'), self.output / 'issuer-ca.pem')
        prefix = Path(identity)
        result = json.loads(command([self.probe, 'tls-denial' if expected == 'denied' else 'tls', ca, str(prefix) + '-chain.pem', str(prefix) + '-key.pem',
                                     host, self.ports[service][0], path], json.dumps(body)))
        if expected == 'denied':
            require(result['status'] in (401, 403) or (result['status'] == 0 and result.get('remote_certificate_rejected') is True),
                    'revoked client certificate was not explicitly rejected')
            return None
        require(result['status'] == expected, f'TLS {path}: status {result["status"]}, expected {expected}')
        return json.loads(result['body']) if result['body'] and expected < 400 else None

    def auth(self, identity, device, expected=200):
        return self.tls('api', '/request_token', {'scope': 'device', 'devid': device, 'service': 'mqtt'}, identity, expected)

    def mqtt(self, auth, device, mode='roundtrip', expected=0):
        config = {'CA': str(self.base / 'pki/servers/mqtt-pki/ca.crt'), 'Host': 'mqtt-pki.' + NS + '.svc',
                  'Port': self.ports['mqtt'][0], 'Username': auth['mqtt']['username'], 'Password': auth['access_token'],
                  'ClientID': auth['mqtt']['client_id'], 'Topic': 'devices/' + device + '/pki-rehearsal',
                  'DeliveryTopic': '_bc/' + auth['mqtt']['username'] + '/devices/' + device + '/pki-rehearsal',
                  'Payload': self.output.name, 'Mode': mode}
        process = Process([self.probe, 'mqtt'], json.dumps(config))
        self.children.append(process)
        opened = process.event()
        require(opened.get('event') == 'connack' and opened.get('code') == expected,
                'MQTT authorization result differs: ' + str(opened.get('code')))
        if expected == 0 and mode != 'connect':
            require(process.event().get('event') == 'acl_and_roundtrip_passed', 'MQTT ACL/roundtrip failed')
        if mode in ('roundtrip', 'connect'):
            require(process.child.wait(timeout=5) == 0, 'MQTT probe did not complete successfully')
        return process, opened

    def wait_positive_mqtt(self, auth, device):
        deadline = time.monotonic() + 30
        attempts = 0
        while True:
            attempts += 1
            try:
                self.mqtt(auth, device, 'connect')
                return attempts
            except RuntimeError as error:
                if str(error) != 'MQTT authorization result differs: 5' or time.monotonic() >= deadline:
                    raise
                time.sleep(2)

    def enroll_replace(self):
        profile = self.profile
        cloud = profile['brand_cloud_id']
        now = dt.datetime.now(dt.timezone.utc)
        batch = 'dev-pki-' + uuid.uuid4().hex
        run = self.http('/admin/brand-clouds/' + cloud + '/device-item-profiles/' + profile['id'] + '/production-runs',
                        {'factory_id': 'dev-pki-rehearsal', 'batch_id': batch, 'allowed_quantity': 1,
                         'valid_from': stamp(now - dt.timedelta(minutes=1)), 'valid_until': stamp(now + dt.timedelta(hours=24))},
                        self.login(self.admin)[0], 201)
        self.save('production-run.json', run)
        self.device = 'pki-dev-' + uuid.uuid4().hex
        body = {'request_id': uuid.uuid4().hex, 'devid': self.device, 'csr_pem': self.key('initial', self.device),
                'serial_number': batch, 'production_run_id': run['production_run']['id'], 'brand_cloud_id': cloud,
                'device_item_profile_id': profile['id'], 'factory_id': 'dev-pki-rehearsal', 'batch_id': batch,
                'entitlement_state': 'active'}
        body.update({key: profile[key] for key in ('profile_key', 'ca_profile', 'issuer_profile', 'service_options')})
        self.save('enroll-request.json', body)
        result = self.http('/factory/enroll', body, run['factory_jwt'], service='factory')
        require(result['certificate_bundle']['issuance']['issuer_id'] == self.issuer['issuer_id'], 'wrong enrollment issuer')
        self.save('enrolled.json', result)
        self.save('initial-chain.pem', result['certificate_chain_pem'])
        self.save('initial-certificate.pem', result['certificate_pem'])
        require(command([self.openssl, 'x509', '-in', self.output / 'initial-certificate.pem', '-pubkey', '-noout']) ==
                command([self.openssl, 'pkey', '-in', self.output / 'initial-key.pem', '-pubout']), 'device key/certificate mismatch')
        self.replace_device()

    def resume_enrolled(self):
        self.issuer = read(self.output / 'active.json')
        self.crl = read(self.output / 'product-crl.json')
        self.profile = read(self.output / 'product.json')
        enrolled = read(self.output / 'enrolled.json')
        request = read(self.output / 'enroll-request.json')
        self.device = request['devid']
        require(not (self.output / 'renew-request.json').exists(), 'renewal already attempted; reconcile manually')
        require(self.api('/issuers/' + self.issuer['issuer_id']) == self.issuer and self.issuer['status'] == 'active',
                'live active issuer differs; reconcile manually')
        require(request['device_item_profile_id'] == self.issuer['device_item_profile_id'] == self.profile['id']
                and enrolled['certificate_bundle']['issuance']['issuer_id'] == self.issuer['issuer_id'], 'enrollment binding differs')
        self.replace_device()

    def replace_device(self):
        initial = self.output / 'initial'
        successor = self.output / 'successor'
        old_auth = self.auth(initial, self.device)
        self.save('initial-auth.json', old_auth)
        readiness_attempts = self.wait_positive_mqtt(old_auth, self.device)
        self.mqtt(old_auth, self.device)
        self.check('enrollment', {'device': self.device, 'issuer_id': self.issuer['issuer_id'], 'local_key_match': True, 'mqtt_readiness_attempts': readiness_attempts})
        request = {'request_id': uuid.uuid4().hex, 'csr_pem': self.key('successor', self.device), 'ttl_days': 30}
        self.save('renew-request.json', request)
        renewed = self.tls('issuer', '/v1/certificates/renew', request, initial)
        self.save('renewed.json', renewed)
        require(renewed['issuer_id'] == self.issuer['issuer_id'], 'wrong renewal issuer')
        self.save('successor-chain.pem', renewed['certificate_chain_pem'])
        # Simulate losing the response before installing it: retry the same durable request.
        require(self.tls('issuer', '/v1/certificates/renew', request, initial) == renewed, 'renewal replay differs')
        ack = {'request_id': request['request_id']}
        self.tls('issuer', '/v1/certificates/renew/ack', ack, initial, 403)
        new_auth = self.auth(successor, self.device)
        self.save('successor-auth.json', new_auth)
        old, opened = self.mqtt(old_auth, self.device, 'hold')
        new, _ = self.mqtt(new_auth, self.device, 'hold')
        try:
            old.stable(10)
            before = dt.datetime.now(dt.timezone.utc)
            self.tls('issuer', '/v1/certificates/renew/ack', ack, successor, 204)
            elapsed, age = self.cutoff(old, opened, before)
            new.stable(12)
            self.mqtt(old_auth, self.device, 'connect', 5)
            self.auth(initial, self.device, 401)
            self.auth(successor, self.device)
            self.tls('issuer', '/v1/certificates/renew/ack', ack, successor, 204)
        finally:
            old.close()
            new.close()
        self.check('renewal_replacement', {'replay_identical': True, 'old_ack_status': 403, 'successor_ack_status': 204,
                    'predecessor_closed_seconds': elapsed, 'predecessor_connection_age_seconds': age,
                    'successor_preserved_seconds': 12, 'old_mqtt_code': 5, 'old_api_status': 401})
        self.restart('mqtt-pki')
        self.auth(initial, self.device, 401)
        self.mqtt(self.auth(successor, self.device), self.device)
        self.check('replacement_restart', {'predecessor_denied': True, 'successor_accepted': True})

    def cutoff(self, process, opened, before):
        closed = process.event(20)
        require(closed.get('event') == 'closed', 'expected session closure')
        parsed = parse_time
        elapsed = (parsed(closed['at']) - before).total_seconds()
        age = (parsed(closed['at']) - parsed(opened['at'])).total_seconds()
        require(0 <= elapsed <= 20 and age < 45, 'cutoff timing cannot exclude natural lease expiry')
        require(process.child.wait(timeout=5) == 0, 'session probe failed')
        return elapsed, age

    def worker(self, args, body=None):
        return self.kube(['-n', NS, 'exec', '-i', 'deployment/mqtt-pki', '-c', 'pkibroker', '--'] + args, body)

    def root_policy(self):
        return self.api('/issuers/' + self.root['issuer_id'] + '/distrust')

    def replace_state(self, expected_digest, raw):
        require(re.fullmatch('[0-9a-f]{64}', expected_digest), 'invalid state precondition')
        script = '''set -eu
file=/run/pki-state/device/root-policy.json
exec 9>"$file.lock"
flock -x 9
actual=$(sha256sum "$file" | cut -d ' ' -f 1)
[ "$actual" = "$1" ]
tmp=$(mktemp /run/pki-state/device/.dev-fault-XXXXXX)
trap 'rm -f "$tmp"' EXIT
cat >"$tmp"
chmod 600 "$tmp"
sync "$tmp"
mv "$tmp" "$file"
sync /run/pki-state/device
'''
        self.worker(['sh', '-c', script, 'sh', expected_digest], raw)

    def resume_revocation(self):
        self.issuer = read(self.output / 'active.json')
        self.crl = read(self.output / 'product-crl.json')
        self.profile = read(self.output / 'product.json')
        self.device = read(self.output / 'enroll-request.json')['devid']
        operation = read(self.output / 'revocation-operation.json')
        current = self.api('/operations/' + operation['operation_id'])
        require(current['status'] == 'approved' and current['request_sha256'] == operation['request_sha256']
                and current['issuer_id'] == self.issuer['issuer_id'], 'revocation already executed or operation differs')
        require(not (self.output / 'brand-crl-request.json').exists(), 'CRL ceremony already attempted; reconcile manually')
        require(self.api('/issuers/' + self.issuer['issuer_id']) == self.issuer and self.issuer['status'] == 'active',
                'issuer no longer matches approved revocation')
        require(self.worker(['cat', ROOT_STATE]) == (self.output / 'root-state-before-fault.json').read_text(),
                'previous fault was not restored exactly')
        self.revoke_recover(operation)

    def revoke_recover(self, operation=None):
        issuer = self.issuer
        path = '/issuers/' + issuer['issuer_id']
        if operation is None:
            operation = self.api(path + '/operations', {'issuer_id': issuer['issuer_id'], 'action': 'revoke',
                                'reason': 'Dispose isolated dev acceptance Product and verify real consumer receipt gates.'},
                                key='dev-revoke-' + issuer['issuer_id'])
            self.save('revocation-operation.json', operation)
            self.approval(operation)
        op_path = '/operations/' + operation['operation_id']
        self.manifest('video-cloud-api-pki', 'crls.json',
                      lambda entries: [e for e in entries if e['issuer']['issuer_id'] != issuer['issuer_id']])
        baseline_device = read(self.foundation / 'device-2/enroll-request.json')['devid']
        baseline_identity = self.foundation / 'device-2/v4'
        original = self.worker(['cat', ROOT_STATE])
        policy = self.root_policy()
        require(json.loads(original)['policy'] == policy, 'worker Root policy is stale')
        self.save('root-state-before-fault.json', original)
        fault = '{"invalid_dev_acceptance_state":true}\n'
        faulted = False
        previous = self.api('/issuers/' + self.brand['issuer_id'] + '/crl')
        self.save('brand-prior-crl.json', previous)
        entries = json.loads(command([self.probe, 'crl'], previous['crl_pem']))
        require(digest(base64.b64decode(''.join(previous['crl_pem'].splitlines()[1:-1]))) == previous['crl_sha256'],
                'prior CRL digest does not match signed DER')
        session, opened = self.mqtt(self.auth(baseline_identity, baseline_device), baseline_device, 'hold')
        try:
            session.stable(10)
            before = dt.datetime.now(dt.timezone.utc)
            faulted = True
            self.replace_state(digest(original), fault)
            elapsed, age = self.cutoff(session, opened, before)
            self.auth(baseline_identity, baseline_device)
            self.check('worker_fault_fail_closed', {'closed_seconds': elapsed, 'connection_age_seconds': age,
                                                  'healthy_api_still_allowed': True})
            revoked_at = stamp()
            self.api(op_path + '/execute', {}, 204)
            self.api(op_path + '/revocation-complete', {}, 409)
            require(self.api(path)['status'] == 'revoked', 'Product not revoked')
            entries.append({'serial_hex': format(int(issuer['serial_number'].replace(':', ''), 16), 'x'),
                            'revoked_at': revoked_at, 'reason_code': 5})
            now = dt.datetime.now(dt.timezone.utc)
            request = {'issuer_id': self.brand['issuer_id'],
                       'issuer_fingerprint_sha256': self.brand['certificate_fingerprint_sha256'],
                       'crl_number': str(int(previous['crl_number']) + 1), 'this_update': stamp(now),
                       'next_update': stamp(now + dt.timedelta(days=3)), 'revocations': entries}
            self.save('brand-crl-request.json', request)
            request_digest = command([self.ceremony, 'crl-digest', self.output / 'brand-crl-request.json']).strip()
            env_path = os.environ.get('PATH', '')
            try:
                os.environ['PATH'] = str(Path(self.openssl).parent) + os.pathsep + env_path
                command([self.ceremony, 'crl', '--issuer', self.output / 'brand.json',
                         '--crl-request', self.output / 'brand-crl-request.json', '--expected-request-sha256', request_digest,
                         '--key', self.foundation / 'brand-offline-simulation/ca-key.encrypted.pem',
                         '--passphrase-file', self.base / 'pki/rehearsal-passphrases/brand', '--out', self.output / 'brand-crl'])
            finally:
                os.environ['PATH'] = env_path
            require(self.api('/issuers/' + self.brand['issuer_id'] + '/crl') == previous,
                    'Brand CRL changed concurrently; do not import signed replacement')
            record = self.api('/issuers/' + self.brand['issuer_id'] + '/crl',
                              {'crl_pem': (self.output / 'brand-crl/revocations.pem').read_text()})
            self.save('brand-crl-imported.json', record)
            self.wait_receipts(self.brand['issuer_id'], record['crl_sha256'], ['video-cloud-api'], 'crl', absent='pkibroker')
            self.api(op_path + '/revocation-complete', {}, 409)
            require(self.api(op_path)['status'] == 'revocation_pending', 'revocation gate did not remain pending')
            self.check('revocation_missing_broker_denied', {'http_status': 409, 'crl_sha256': record['crl_sha256'],
                       'preserved_prior_revocations': len(entries) - 1})
        finally:
            session.close()
            if faulted:
                require(self.root_policy() == policy, 'Root policy changed: fault restoration requires reconciliation')
                observed = self.worker(['cat', ROOT_STATE])
                if observed == fault:
                    self.replace_state(digest(fault), original)
                else:
                    require(observed == original, 'concurrent Root-state mutation: preserve for reconciliation')
                require(self.worker(['cat', ROOT_STATE]) == original, 'Root-state restoration differs')
                self.check('fault_restored', {'original_sha256': digest(original), 'current_policy_unchanged': True})
        self.finish_revocation(operation, record, original)

    def resume_recovery(self):
        self.issuer = read(self.output / 'active.json')
        self.crl = read(self.output / 'product-crl.json')
        self.device = read(self.output / 'enroll-request.json')['devid']
        operation = read(self.output / 'revocation-operation.json')
        record = read(self.output / 'brand-crl-imported.json')
        original = (self.output / 'root-state-before-fault.json').read_text()
        current = self.api('/operations/' + operation['operation_id'])
        require(current['status'] in ('completed', 'revocation_pending') and current['issuer_id'] == self.issuer['issuer_id']
                and current['request_sha256'] == operation['request_sha256'], 'revocation operation differs')
        issuer = self.api('/issuers/' + self.issuer['issuer_id'])
        require(issuer['status'] == 'revoked' and issuer['certificate_fingerprint_sha256'] == self.issuer['certificate_fingerprint_sha256'],
                'revoked issuer differs')
        require(self.api('/issuers/' + self.brand['issuer_id'] + '/crl') == record, 'Brand CRL changed; reconcile manually')
        require(self.worker(['cat', ROOT_STATE]) == original, 'fault restoration differs')
        self.finish_revocation(operation, record, original)

    def finish_revocation(self, operation, record, original):
        issuer = self.issuer
        op_path = '/operations/' + operation['operation_id']
        baseline_device = read(self.foundation / 'device-2/enroll-request.json')['devid']
        baseline_identity = self.foundation / 'device-2/v4'
        self.wait_receipts(self.brand['issuer_id'], record['crl_sha256'], CONSUMERS, 'crl')
        if self.api(op_path)['status'] == 'revocation_pending':
            self.api(op_path + '/revocation-complete', {}, 204)
        require(self.api(op_path)['status'] == 'completed', 'revocation completion not durable')
        terminal_path = '/run/pki-state/device/' + issuer['issuer_id'] + '-crl.json'
        terminal = self.worker(['cat', terminal_path])
        document = json.loads(terminal)
        require(document['terminal_issuer_id'] == issuer['issuer_id'] and not document['crl'].get('issuer_id')
                and document['retained_crl']['crl_sha256'] == self.crl['crl_sha256'], 'invalid terminal CRL state')
        self.save('terminal-state.json', terminal)
        revoked_auth = read(self.output / 'successor-auth.json')
        encoded = revoked_auth['access_token'].split('.')[1]
        claims = json.loads(base64.urlsafe_b64decode(encoded + '=' * (-len(encoded) % 4)))
        require(claims['exp'] > time.time() + 120, 'revoked test token expires too soon for restart proof')
        self.auth(self.output / 'successor', self.device, 'denied')
        self.mqtt(revoked_auth, self.device, 'connect', 5)
        self.mqtt(self.auth(baseline_identity, baseline_device), baseline_device)
        pvc = self.obj('persistentvolumeclaim', 'mqtt-pki-trust')['metadata']['uid']
        self.restart('mqtt-pki')
        require(self.obj('persistentvolumeclaim', 'mqtt-pki-trust')['metadata']['uid'] == pvc, 'trust PVC replaced')
        require(self.worker(['cat', terminal_path]) == terminal, 'terminal state changed after restart')
        require(self.worker(['cat', ROOT_STATE]) == original, 'Root state changed after restart')
        mode = self.worker(['stat', '-c', '%a %u', terminal_path]).strip()
        require(mode == '600 10001', 'terminal state permissions changed after remount')
        require(claims['exp'] > time.time(), 'revoked token expired during restart proof')
        self.auth(self.output / 'successor', self.device, 'denied')
        self.mqtt(revoked_auth, self.device, 'connect', 5)
        self.mqtt(self.auth(baseline_identity, baseline_device), baseline_device)
        self.check('revocation_recovery_restart', {'operation_status': 'completed', 'terminal_persisted': True,
                   'trust_pvc_uid': pvc, 'terminal_mode_owner': mode, 'unexpired_revoked_mqtt_code': 5,
                   'revoked_client_certificate_rejected': True, 'baseline_api_mqtt_healthy': True})
        for role, prefix in [('pki-controller-dev', 'pki-controller-dev-'), ('certissuer-pki-dev', 'pki-signer-dev-')]:
            self.role_policy(role, prefix + issuer['issuer_id'])
        self.manifest('video-cloud-api-pki', 'issuers.json',
                      lambda entries: [e for e in entries if e['issuer_id'] != issuer['issuer_id']])
        self.manifest('mqtt-pki', 'crls.json',
                      lambda entries: [e for e in entries if e['issuer']['issuer_id'] != issuer['issuer_id']])
        require(len(json.loads(self.bao(['list', '-format=json', issuer['signer_reference'] + '/keys']))) == 1,
                'retained provider key changed')
        self.mqtt(self.auth(baseline_identity, baseline_device), baseline_device)
        self.check('run_cleanup', {'own_policies_and_manifest_entries_removed': True,
                                  'provider_key_and_terminal_audit_retained': True, 'baseline_healthy': True})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', '~/.config/rtk_cloud'))
    parser.add_argument('--context', default='lke649805-ctx')
    parser.add_argument('--output', required=True, help='New private directory; existing directories are refused')
    parser.add_argument('--run', action='store_true', help='Execute disposable Product/device and consumer fault tests')
    parser.add_argument('--resume', choices=('provision', 'ready', 'enrolled', 'revocation', 'recovery'),
                        help='Explicitly reconcile a failed run at a guarded durable checkpoint')
    args = parser.parse_args()
    require(not args.resume or args.run, '--resume requires --run')
    os.umask(0o077)
    def interrupted(signum, frame):
        raise KeyboardInterrupt()
    signal.signal(signal.SIGTERM, interrupted)
    import fcntl
    lock_path = Path(args.config_root).expanduser() / 'dev/pki/acceptance.lock'
    with lock_path.open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        runner = Acceptance(args.config_root, args.context, args.output, args.resume)
        try:
            phases = [runner.preflight]
            if args.run:
                phases += {
                    None: [runner.provision, runner.enroll_replace, runner.revoke_recover],
                    'provision': [runner.resume_provision, runner.enroll_replace, runner.revoke_recover],
                    'ready': [runner.resume_ready, runner.enroll_replace, runner.revoke_recover],
                    'enrolled': [runner.resume_enrolled, runner.revoke_recover],
                    'revocation': [runner.resume_revocation],
                    'recovery': [runner.resume_recovery],
                }[args.resume]
            for phase in phases:
                runner.report['active_phase'] = phase.__name__
                runner.save('report.json', runner.report)
                print(json.dumps({'phase': phase.__name__, 'status': 'running'}), flush=True)
                phase()
            if args.run:
                require(all(check['status'] == 'passed' for check in runner.report['checks'].values()), 'unexecuted acceptance check')
            runner.report['status'] = 'passed' if args.run else 'preflight_passed'
        except BaseException as error:
            runner.report['status'] = 'failed'
            frame = traceback.extract_tb(error.__traceback__)[-1]
            runner.report['failure_location'] = {'file': Path(frame.filename).name, 'line': frame.lineno, 'function': frame.name}
            runner.report['failure'] = str(error) if type(error) is RuntimeError else type(error).__name__
            print(json.dumps({'status': 'failed', 'reason': runner.report['failure']}), flush=True)
            return 1
        finally:
            runner.close()
            runner.save('report.json', runner.report)
        print(json.dumps({'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
