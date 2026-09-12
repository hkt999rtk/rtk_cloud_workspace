#!/usr/bin/env python3
"""Qualify Dev App Root withdrawal across public API, MQTT and TURN."""
import argparse
import base64
import fcntl
import hashlib
import http.client
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import sqlite3
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

from cryptography.hazmat.primitives.ciphers.aead import AESGCM


spec = importlib.util.spec_from_file_location(
    'app_hierarchy', Path(__file__).with_name('app_hierarchy.py'))
a = importlib.util.module_from_spec(spec)
spec.loader.exec_module(a)
m, NS = a.m, a.NS

PUBLIC_HOST = 'app.video-cloud-dev.realtekconnect.com'
TURN_HOST = 'turn.video-cloud-dev.realtekconnect.com:3478'
TURN_REALM = 'video_cloud'
AM_NS = 'video-cloud-dev-account-manager'


def token_claims(token):
    parts = token.split('.')
    m.require(len(parts) == 3, 'App access token is not a JWT')
    return json.loads(base64.urlsafe_b64decode(
        parts[1] + '=' * (-len(parts[1]) % 4)))


class AppRootAcceptance(a.AppHierarchy):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = (
            'Dev App Root withdrawal at public API, MQTT and TURN boundaries')
        self.report['app_root_acceptance_sha256'] = m.digest(
            Path(__file__).read_bytes())
        self.database = Path(args.database).resolve()
        self.turn_probe = self.output / 'pkiappacceptance'
        self.turn_probe_linux = self.output / 'pkiappacceptance-linux-amd64'
        self.save('report.json', self.report)

    def preflight_acceptance(self):
        self.preflight_app()
        m.require(self.database.is_file() and not self.database.is_symlink()
                  and self.database.stat().st_mode & 0o777 == 0o600,
                  'mode-0600 private test database required')
        expected = (Path.cwd() /
                    'cloud_env/dev/runtime/artifacts/test-data/rtk-test-data.sqlite').resolve()
        m.require(self.database == expected,
                  'canonical Dev test database required')
        for output, extra in ((self.turn_probe, {}), (self.turn_probe_linux, {
                'CGO_ENABLED': '0', 'GOOS': 'linux', 'GOARCH': 'amd64'})):
            build = subprocess.run([
                'go', 'build', '-o', str(output), './cmd/pkiappacceptance'],
                cwd=m.WORKSPACE / 'repos/rtk_video_cloud',
                env=dict(os.environ, GOWORK='off', **extra),
                capture_output=True, timeout=180)
            m.require(build.returncode == 0,
                      'App TURN acceptance probe build failed')
        self.forward('app', NS, 'video-cloud-api-app-pki', 8443)
        self.forward_turn_cli()
        self.install_turn_probe()
        self.check('r1_acceptance_preflight', {
            'public_host': PUBLIC_HOST, 'turn_host': TURN_HOST,
            'database_private': True, 'staging_touched': False})

    def forward_turn_cli(self):
        if 'turn-cli' in self.ports:
            self.ports['turn-cli'][1].close()
        deployment = self.obj('deployment', 'pkiturn')
        selector = ','.join(
            key + '=' + value for key, value in
            deployment['spec']['selector']['matchLabels'].items())
        pods = json.loads(self.kube([
            '-n', NS, 'get', 'pods', '-l', selector, '-o', 'json']))['items']
        pods = [pod for pod in pods
                if not pod['metadata'].get('deletionTimestamp')]
        m.require(len(pods) == 1, 'expected one live pkiturn Pod')
        self.turn_pod = pods[0]['metadata']['name']
        child = m.Process(self.k + [
            '-n', NS, 'port-forward', '--address=127.0.0.1',
            'pod/' + pods[0]['metadata']['name'], ':5766'])
        self.children.append(child)
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            match = re.search(r'Forwarding from 127\.0\.0\.1:(\d+) ->',
                              child.line())
            if match:
                self.ports['turn-cli'] = (match.group(1), child)
                return
        raise RuntimeError('TURN CLI port-forward startup deadline')

    def install_turn_probe(self):
        body = base64.b64encode(self.turn_probe_linux.read_bytes()).decode()
        self.kube([
            '-n', NS, 'exec', '-i', 'pod/' + self.turn_pod,
            '-c', 'coturn-cli-tunnel', '--', 'sh', '-c',
            'umask 077; base64 -d > /tmp/pkiappacceptance; '
            'chmod 700 /tmp/pkiappacceptance'], body, timeout=120)
        self.kube([
            '-n', NS, 'exec', 'pod/' + self.turn_pod,
            '-c', 'coturn-cli-tunnel', '--', 'test', '-x',
            '/tmp/pkiappacceptance'], timeout=20)

    def sql_rows(self, query):
        raw = self.kube([
            '-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0',
            '--', 'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres',
            '-d', 'video_cloud', '-At'], query)
        return [json.loads(line) for line in raw.splitlines() if line]

    def account_sql_rows(self, query):
        raw = self.kube([
            '-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0',
            '--', 'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres',
            '-d', 'rtk_account_manager', '-At'], query)
        return [json.loads(line) for line in raw.splitlines() if line]

    def local_identity(self, email, label, root_id, intermediate_id=None):
        m.require(re.fullmatch(r'[A-Za-z0-9+_.@-]{3,254}', email),
                  'invalid acceptance email')
        database = sqlite3.connect(
            'file:' + urllib.parse.quote(str(self.database)) + '?mode=ro',
            uri=True)
        try:
            rows = database.execute(
                'SELECT user_id,password,app_credentials_json,'
                'app_certificate_json FROM users WHERE brandname=? AND email=?',
                ('RTK', email)).fetchall()
        finally:
            database.close()
        m.require(len(rows) == 1, 'expected one private App identity: ' + label)
        user_id, password, credentials_raw, certificate_raw = rows[0]
        credentials, certificate = (json.loads(credentials_raw),
                                    json.loads(certificate_raw))
        fingerprint = certificate.get('fingerprint_sha256', '').lower()
        subject = 'app-user:' + user_id
        m.require(password and certificate.get('status') == 'issued'
                  and certificate.get('subject') == subject
                  and re.fullmatch(r'[0-9a-f]{64}', fingerprint)
                  and credentials.get('subject') == subject
                  and credentials.get('private_key_pem')
                  and certificate.get('certificate_pem')
                  and certificate.get('certificate_chain_pem'),
                  'incomplete private App identity: ' + label)
        key = self.output / (label + '-key.pem')
        cert = self.output / (label + '-certificate.pem')
        chain = self.output / (label + '-chain.pem')
        self.save(key.name, credentials['private_key_pem'])
        self.save(cert.name, certificate['certificate_pem'])
        self.save(chain.name, certificate['certificate_chain_pem'])
        m.require(m.command([self.openssl, 'x509', '-in', cert,
                            '-pubkey', '-noout']) ==
                  m.command([self.openssl, 'pkey', '-in', key,
                            '-pubout']), 'App certificate/private key mismatch')
        rows = self.sql_rows(
            "SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,subject,"
            "fingerprint,status,revoked_at FROM pki_app_issuances WHERE "
            "fingerprint='" + fingerprint + "') t;")
        m.require(len(rows) == 1 and rows[0]['subject'] == subject
                  and rows[0]['status'] == 'succeeded'
                  and rows[0]['revoked_at'] is None,
                  'registry App issuance differs: ' + label)
        issuer = self.api('/issuers/' + rows[0]['issuer_id'])
        root = self.api('/issuers/' + issuer['parent_issuer_id'])
        m.require(issuer['kind'] == 'intermediate'
                  and issuer['trust_domain'] == 'app'
                  and issuer['environment'] == 'dev'
                  and root['issuer_id'] == root_id,
                  'App identity lineage differs: ' + label)
        if intermediate_id:
            m.require(issuer['issuer_id'] == intermediate_id,
                      'successor App intermediate differs')
        return {
            'email': email, 'password': password, 'user_id': user_id,
            'fingerprint': fingerprint, 'subject': subject,
            'issuer_id': issuer['issuer_id'], 'root_id': root_id,
            'key': key, 'chain': chain,
        }

    def account_http(self, path, data, token, expected):
        request = urllib.request.Request(
            'http://127.0.0.1:' + self.ports['am'][0] + '/v1' + path,
            data=None if data is None else json.dumps(data).encode(),
            headers={'Content-Type': 'application/json',
                     'Authorization': 'Bearer ' + token})
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        try:
            with opener.open(request, timeout=45) as response:
                status, raw = response.status, response.read((1 << 20) + 1)
        except urllib.error.HTTPError as error:
            status, raw = error.code, error.read((1 << 20) + 1)
        m.require(status in expected and len(raw) <= 1 << 20,
                  'Account Manager membership request failed')
        return status, json.loads(raw) if raw else {}

    def scale_email_worker(self, replicas):
        worker = self.obj('deployment', 'account-manager-email-worker', AM_NS)
        current = worker['spec']['replicas']
        m.require(current in (0, 1) and replicas in (0, 1),
                  'unexpected Dev email worker replica count')
        if current != replicas:
            patch = [
                {'op': 'test', 'path': '/metadata/resourceVersion',
                 'value': worker['metadata']['resourceVersion']},
                {'op': 'replace', 'path': '/spec/replicas', 'value': replicas},
            ]
            self.kube(['-n', AM_NS, 'patch', 'deployment',
                       'account-manager-email-worker', '--type=json',
                       '--patch-file=/dev/stdin'], json.dumps(patch))
        self.kube(['-n', AM_NS, 'rollout', 'status',
                   'deployment/account-manager-email-worker', '--timeout=90s'],
                  timeout=100)

    def invitation_token(self, email):
        secret = self.obj('secret', 'account-manager-runtime', AM_NS)['data']
        encoded_key = base64.b64decode(
            secret['EMAIL_OUTBOX_ENCRYPTION_KEY']).decode()
        cipher = AESGCM(base64.b64decode(encoded_key))
        rows = self.account_sql_rows(
            "SELECT row_to_json(t) FROM (SELECT "
            "encode(payload_nonce,'base64') nonce,"
            "encode(payload_ciphertext,'base64') ciphertext "
            "FROM email_outbox WHERE "
            "message_type='product_collaborator_invitation' "
            "AND payload_nonce IS NOT NULL "
            "AND payload_ciphertext IS NOT NULL "
            "ORDER BY created_at DESC LIMIT 20) t;")
        for row in rows:
            payload = json.loads(cipher.decrypt(
                base64.b64decode(row['nonce']),
                base64.b64decode(row['ciphertext']), None))
            if payload.get('recipient_email') == email and payload.get('token'):
                return payload['token']
        raise RuntimeError('fresh Product invitation token was not retained')

    def ensure_product_admission(self, identity, cloud_id, product_id):
        def admission():
            rows = self.account_sql_rows(
                "SELECT row_to_json(t) FROM (SELECT "
                "user_can_access_brand_cloud_product('" +
                identity['user_id'] + "','" + cloud_id + "','" +
                product_id + "') allowed,"
                "EXISTS(SELECT 1 FROM brand_cloud_product_admissions "
                "WHERE organization_id='" + cloud_id + "' AND user_id='" +
                identity['user_id'] + "' AND product_id='" + product_id +
                "') admitted) t;")
            m.require(len(rows) == 1, 'Product admission lookup differs')
            return rows[0]

        current = admission()
        if current['allowed'] and current['admitted']:
            return 'existing'
        owner_token, owner_id = self.login(self.admin)
        owner = self.account_sql_rows(
            "SELECT row_to_json(t) FROM (SELECT "
            "user_can_access_brand_cloud_product('" + owner_id + "','" +
            cloud_id + "','" + product_id + "') allowed,"
            "EXISTS(SELECT 1 FROM organization_members WHERE "
            "organization_id='" + cloud_id + "' AND user_id='" + owner_id +
            "' AND role='owner' AND disabled_at IS NULL) owner) t;")
        m.require(len(owner) == 1 and owner[0]['allowed'] and owner[0]['owner'],
                  'Dev platform admin no longer owns the selected Product')
        base = ('/developer/brand-clouds/' + cloud_id + '/products/' +
                product_id + '/collaborator-invitations')
        _, listed = self.account_http(base, None, owner_token, (200,))
        for invitation in listed.get('invitations', []):
            if (invitation.get('target_email') == identity['email']
                    and invitation.get('status') == 'pending'):
                self.account_http(base + '/' + invitation['id'] + '/cancel',
                                  {}, owner_token, (200,))
        _, created = self.account_http(
            base, {'email': identity['email'], 'role': 'product_editor'},
            owner_token, (202,))
        m.require(created.get('invitation', {}).get('status') == 'pending',
                  'Product invitation was not pending')
        token = self.invitation_token(identity['email'])
        user_token = self.login(identity)[0]
        self.account_http('/developer/product-collaborator-invitations/accept',
                          {'token': token}, user_token, (200,))
        current = admission()
        m.require(current['allowed'] and current['admitted'],
                  'Product admission was not installed')
        return 'invited_and_accepted'

    def app_fixture(self):
        owner_id = self.login(self.admin)[1]
        rows = self.account_sql_rows(
            "SELECT row_to_json(t) FROM (SELECT d.organization_id cloud_id,"
            "d.device_item_profile_id product_id,"
            "d.metadata->>'video_cloud_devid' device_id "
            "FROM devices d JOIN device_item_profiles p "
            "ON p.id=d.device_item_profile_id "
            "WHERE d.disabled_at IS NULL AND p.status='active' "
            "AND p.service_options ? 'mqtt' "
            "AND d.metadata ? 'video_cloud_devid' "
            "AND EXISTS(SELECT 1 FROM organization_members m WHERE "
            "m.organization_id=d.organization_id AND m.user_id='" +
            owner_id + "' AND m.role='owner' AND m.disabled_at IS NULL) "
            "ORDER BY d.created_at DESC LIMIT 100) t;")
        for row in rows:
            if not all(re.fullmatch(r'[0-9a-f-]{36}', row[key])
                       for key in ('cloud_id', 'product_id')):
                continue
            device = row.get('device_id', '')
            if not re.fullmatch(r'[A-Za-z0-9_.:-]{3,128}', device):
                continue
            projected = self.sql_rows(
                "SELECT row_to_json(t) FROM (SELECT id,org_id,activated,"
                "auth_revoked_at FROM devices WHERE id='" + device +
                "' AND org_id='" + row['cloud_id'] + "') t;")
            if (len(projected) == 1 and projected[0]['activated']
                    and projected[0]['auth_revoked_at'] is None):
                return row
        raise RuntimeError('no active Dev App Product/device fixture found')

    def ensure_membership(self, identity, cloud_id):
        def current():
            rows = self.account_sql_rows(
                "SELECT row_to_json(t) FROM (SELECT u.id,u.email,m.role "
                "FROM users u JOIN organization_members m ON m.user_id=u.id "
                "WHERE m.disabled_at IS NULL AND m.organization_id='" +
                cloud_id + "' AND u.email='" + identity['email'] + "') t;")
            m.require(len(rows) <= 1, 'duplicate App test membership')
            return rows[0] if rows else None

        token = self.login(self.admin)[0]
        membership = current()
        if not membership or membership.get('role') not in ('admin', 'owner'):
            self.account_http(
                '/admin/brand-clouds/' + cloud_id + '/users', {
                    'email': identity['email'],
                    'password': identity['password'],
                    'display_name': 'R1 App Root acceptance ' +
                                    identity['user_id'][:8],
                    'role': 'admin', 'rotate_password': False,
                    'activation_mode': 'immediate'}, token, (200, 201))
        membership = current()
        m.require(membership and membership.get('id') == identity['user_id']
                  and membership.get('role') in ('admin', 'owner'),
                  'App test membership was not installed')
        return membership['role']

    def app_request(self, identity, device, connection=None):
        context = ssl.create_default_context()
        context.load_cert_chain(identity['chain'], identity['key'])
        owned = connection is None
        if owned:
            connection = http.client.HTTPSConnection(
                PUBLIC_HOST, 443, context=context, timeout=20)
        body = json.dumps({'scope': 'app', 'devid': device,
                           'service': 'mqtt', 'expiry': 900})
        try:
            connection.request('POST', '/request_token', body,
                               {'Content-Type': 'application/json'})
            response = connection.getresponse()
            raw = response.read((1 << 20) + 1)
        except (OSError, ssl.SSLError, http.client.HTTPException):
            if owned:
                connection.close()
            return connection, 0, None
        m.require(len(raw) <= 1 << 20, 'oversized App API response')
        parsed = json.loads(raw) if raw and response.status < 400 else None
        return connection, response.status, parsed

    def validate_app_token(self, identity, device, token):
        claims = token_claims(token['access_token'])
        m.require(token.get('scope') == 'app'
                  and claims.get('scope') == 'app'
                  and claims.get('subject_id') == device
                  and claims.get('actor_id') == identity['user_id']
                  and claims.get('app_certificate_sha256') ==
                  identity['fingerprint']
                  and claims.get('exp', 0) > time.time() + 600
                  and token.get('mqtt', {}).get('username')
                  and token.get('mqtt', {}).get('client_id'),
                  'App token binding differs')
        return claims

    def app_mqtt(self, label, token, device):
        deadline = time.monotonic() + 45
        while True:
            try:
                self.mqtt(token, device, 'roundtrip')
                return
            except RuntimeError as error:
                if (not str(error).startswith('probe failed:')
                        or time.monotonic() >= deadline):
                    raise RuntimeError('App MQTT probe failed (' + label + '): ' +
                                       str(error)) from None
                self.forward('mqtt', NS, 'mqtt-pki', 8883)
                time.sleep(2)

    def hold_mqtt(self, label, token, device):
        deadline = time.monotonic() + 45
        while True:
            try:
                return self.mqtt(token, device, 'hold')
            except RuntimeError as error:
                if (not str(error).startswith('probe failed:')
                        or time.monotonic() >= deadline):
                    raise RuntimeError('App MQTT hold failed (' + label +
                                       '): ' + str(error)) from None
                self.forward('mqtt', NS, 'mqtt-pki', 8883)
                time.sleep(2)

    def turn_secrets(self):
        runtime = self.obj('secret', 'video-cloud-runtime')['data']
        cli = self.obj('secret', 'pkiturn-cli')['data']
        return (base64.b64decode(
                    runtime['VIDEO_CLOUD_TURN_SHARED_SECRET']).decode(),
                base64.b64decode(cli['password']).decode().strip())

    def turn_config(self, identity, device, shared):
        return {
            'redis_addr': 'redis.video-cloud-dev-platform.svc:6379',
            'redis_prefix': 'video_cloud:webrtc',
            'turn_addr': TURN_HOST, 'turn_realm': TURN_REALM,
            'turn_network': 'tcp',
            'turn_shared_secret': shared, 'device_id': device,
            'actor_type': '', 'actor_id': identity['user_id'],
            'app_certificate_sha256': identity['fingerprint'],
            'hold_seconds': 20,
        }

    def app_connection(self, identity):
        context = ssl.create_default_context()
        context.load_cert_chain(identity['chain'], identity['key'])
        connection = http.client.HTTPSConnection(
            PUBLIC_HOST, 443, context=context, timeout=20)
        connection.connect()
        return connection

    def hold_turn(self, config):
        process = m.Process(self.k + [
            '-n', NS, 'exec', '-i', 'pod/' + self.turn_pod,
            '-c', 'coturn-cli-tunnel', '--',
            '/tmp/pkiappacceptance', 'hold'], json.dumps(config))
        self.children.append(process)
        event = process.event(30)
        m.require(event.get('event') == 'allocated'
                  and event.get('username') and event.get('session_id'),
                  'TURN allocation probe did not open')
        return process, event

    def turn_allocations(self, password):
        body = {'cli_addr': '127.0.0.1:' + self.ports['turn-cli'][0],
                'cli_password': password}
        try:
            raw = m.command([self.turn_probe, 'list'], json.dumps(body))
        except RuntimeError as error:
            if str(error) != 'command failed: pkiappacceptance':
                raise
            self.forward_turn_cli()
            body['cli_addr'] = '127.0.0.1:' + self.ports['turn-cli'][0]
            raw = m.command([self.turn_probe, 'list'], json.dumps(body))
        return json.loads(raw)

    def wait_turn_policy(self, password, removed, preserved, timeout=30):
        deadline = time.monotonic() + timeout
        while True:
            usernames = {item['Username'] for item in
                         self.turn_allocations(password)}
            if removed not in usernames and preserved in usernames:
                return
            m.require(time.monotonic() < deadline,
                      'TURN App Root policy enforcement deadline')
            time.sleep(2)

    def root_receipts(self, policy_sha256):
        m.require(re.fullmatch(r'[0-9a-f]{64}', policy_sha256),
                  'invalid App Root policy digest')
        return self.sql_rows(
            "SELECT row_to_json(t) FROM (SELECT consumer_id,"
            "loaded_roots_sha256,acknowledged_at FROM "
            "pki_root_distrust_acknowledgments WHERE environment='dev' "
            "AND domain='app' AND policy_sha256='" + policy_sha256 +
            "' ORDER BY consumer_id,loaded_roots_sha256) t;")

    def wait_root_receipts(self, policy_sha256, loaded_roots_sha256):
        deadline = time.monotonic() + 90
        expected = set(a.CONSUMERS)
        while True:
            rows = self.root_receipts(policy_sha256)
            actual = {row['consumer_id'] for row in rows}
            valid = (actual == expected and len(rows) == len(expected)
                     and all(row['loaded_roots_sha256'] ==
                             loaded_roots_sha256 for row in rows))
            if valid:
                return rows
            m.require(time.monotonic() < deadline,
                      'App Root distrust receipt deadline')
            time.sleep(3)

    @staticmethod
    def root_fingerprints(roots_pem):
        blocks = re.findall(
            r'-----BEGIN CERTIFICATE-----\s+([A-Za-z0-9+/=\s]+?)'
            r'-----END CERTIFICATE-----', roots_pem)
        return {hashlib.sha256(base64.b64decode(
            ''.join(block.split()), validate=True)).hexdigest()
                for block in blocks}

    def consumer_state(self, deployment, container):
        owner = self.obj('deployment', deployment)
        containers = {item['name']: item for item in
                      owner['spec']['template']['spec']['containers']}
        m.require(container in containers,
                  'App trust consumer container changed: ' + deployment)
        mounts = [item for item in containers[container].get('volumeMounts', [])
                  if item['mountPath'] == '/run/pki-state']
        m.require(len(mounts) == 1,
                  'App trust state mount changed: ' + deployment)
        volumes = {item['name']: item for item in
                   owner['spec']['template']['spec']['volumes']}
        volume = volumes.get(mounts[0]['name'], {})
        claim = volume.get('persistentVolumeClaim', {}).get('claimName', '')
        m.require(claim, 'App trust state PVC changed: ' + deployment)
        pvc = self.obj('persistentvolumeclaim', claim)
        raw = self.kube([
            '-n', NS, 'exec', 'deployment/' + deployment,
            '-c', container, '--', 'cat',
            '/run/pki-state/app/root-policy.json'])
        mode = self.kube([
            '-n', NS, 'exec', 'deployment/' + deployment,
            '-c', container, '--', 'stat', '-c', '%a %u',
            '/run/pki-state/app/root-policy.json']).strip()
        state = json.loads(raw)
        m.require(set(state) == {'policy', 'roots_pem'},
                  'App Root trust state schema changed')
        return {
            'deployment': deployment, 'container': container,
            'pvc': claim, 'pvc_uid': pvc['metadata']['uid'],
            'mode_owner': mode, 'sha256': m.digest(raw.encode()),
            'state': state,
        }

    def consumer_pvc_uids(self):
        result = {}
        for deployment, container in (
                ('video-cloud-api-app-pki', 'app'),
                ('mqtt-pki', 'pkibroker'), ('pkiturn', 'pkiturn')):
            owner = self.obj('deployment', deployment)
            containers = {item['name']: item for item in
                          owner['spec']['template']['spec']['containers']}
            mounts = [item for item in
                      containers[container].get('volumeMounts', [])
                      if item['mountPath'] == '/run/pki-state']
            volumes = {item['name']: item for item in
                       owner['spec']['template']['spec']['volumes']}
            m.require(len(mounts) == 1,
                      'App trust state mount changed: ' + deployment)
            claim = volumes[mounts[0]['name']].get(
                'persistentVolumeClaim', {}).get('claimName', '')
            m.require(claim, 'App trust state PVC changed: ' + deployment)
            result[deployment] = {
                'pvc': claim,
                'pvc_uid': self.obj(
                    'persistentvolumeclaim', claim)['metadata']['uid']}
        return result

    def consumer_states(self, policy, old_root, successor_root):
        result = {}
        for deployment, container in (
                ('video-cloud-api-app-pki', 'app'),
                ('mqtt-pki', 'pkibroker'), ('pkiturn', 'pkiturn')):
            record = self.consumer_state(deployment, container)
            state = record['state']
            fingerprints = self.root_fingerprints(state['roots_pem'])
            m.require(state['policy'] == policy
                      and old_root['certificate_fingerprint_sha256']
                      not in fingerprints
                      and successor_root['certificate_fingerprint_sha256']
                      in fingerprints,
                      'App Root trust was not installed: ' + deployment)
            record['root_fingerprints'] = sorted(fingerprints)
            result[deployment] = record
        return result

    def install_post_withdraw_manifest(self, successor_root):
        intermediate = self.api('/issuers/' + m.read(
            Path(self.args.intermediate) /
            'intermediate-ready.json')['issuer_id'])
        m.require(intermediate['status'] == 'active'
                  and intermediate['parent_issuer_id'] ==
                  successor_root['issuer_id'],
                  'successor App intermediate changed')
        desired = a.app_manifest([successor_root, intermediate])
        current = self.obj('configmap', 'pki-app-trust')
        root_trust = current.get('data', {}).get('root-trust.json', '')
        m.require(root_trust, 'persisted App Root policy seed is missing')
        desired['root-trust.json'] = root_trust
        if current['data'] != desired:
            self.observed_patch('configmap', 'pki-app-trust', current, [{
                'op': 'replace', 'path': '/data', 'value': desired}])
            m.write(self.base / 'pki/controller-bootstrap/rollout' /
                    'pki-app-trust-configmap.json', {
                        'apiVersion': 'v1', 'kind': 'ConfigMap',
                        'metadata': {'name': 'pki-app-trust',
                                     'namespace': NS},
                        'data': desired})
            return True
        return False

    def identities(self):
        successor_root = self.app_root('active')
        old_saved = m.read(Path(self.args.old_authority) / 'root-ready.json')
        old_root = self.api('/issuers/' + old_saved['issuer_id'])
        intermediate = m.read(Path(self.args.intermediate) /
                              'intermediate-ready.json')
        m.require(old_root['status'] == 'retiring'
                  and old_root['trust_domain'] == 'app'
                  and old_root['certificate_fingerprint_sha256'] ==
                  old_saved['certificate_fingerprint_sha256'],
                  'reviewed old App Root changed')
        old = self.local_identity(self.args.old_email, 'old-app',
                                  old_root['issuer_id'])
        successor = self.local_identity(
            self.args.successor_email, 'successor-app',
            successor_root['issuer_id'], intermediate['issuer_id'])
        return old_root, successor_root, old, successor

    def prepare(self):
        old_root, successor_root, old, successor = self.identities()
        fixture = self.app_fixture()
        cloud_id, product_id, device = (
            fixture['cloud_id'], fixture['product_id'], fixture['device_id'])
        roles = {label: self.ensure_membership(identity, cloud_id)
                 for label, identity in (('old', old),
                                         ('successor', successor))}
        self.scale_email_worker(0)
        try:
            admissions = {
                label: self.ensure_product_admission(identity, cloud_id,
                                                     product_id)
                for label, identity in (('old', old),
                                        ('successor', successor))}
        finally:
            self.scale_email_worker(1)
        for label, identity in (('old', old), ('successor', successor)):
            connection, status, token = self.app_request(identity, device)
            connection.close()
            m.require(status == 200, 'App token request failed: ' + label)
            self.validate_app_token(identity, device, token)
            self.app_mqtt(label, token, device)
        shared, password = self.turn_secrets()
        held = []
        events = {}
        try:
            for label, identity in (('old', old), ('successor', successor)):
                try:
                    process, event = self.hold_turn(
                        self.turn_config(identity, device, shared))
                except RuntimeError as error:
                    raise RuntimeError('App TURN probe failed (' + label + '): ' +
                                       str(error)) from None
                held.append(process)
                events[label] = event
            usernames = {item['Username'] for item in
                         self.turn_allocations(password)}
            m.require(events['old']['username'] in usernames
                      and events['successor']['username'] in usernames,
                      'TURN allocation inventory omitted an App lineage')
        finally:
            for process in held:
                process.close()
        self.check('app_lineages_ready_for_withdrawal', {
            'old_root_id': old_root['issuer_id'],
            'successor_root_id': successor_root['issuer_id'],
            'old_intermediate_id': old['issuer_id'],
            'successor_intermediate_id': successor['issuer_id'],
            'old_fingerprint': old['fingerprint'],
            'successor_fingerprint': successor['fingerprint'],
            'cloud_id': cloud_id, 'product_id': product_id,
            'device_id': device, 'membership_roles': roles,
            'product_admissions': admissions,
            'public_app_mtls_tokens': 2, 'mqtt_roundtrips': 2,
            'turn_allocations': 2})
        self.device_baseline()
        for role in ('requester', 'approver', 'custodian'):
            self.api('/issuers/search', {'limit': 1}, role=role)
        self.check('unaffected_device_service_before_withdrawal', {
            'device_direct_mtls_and_mqtt': True,
            'service_management_callers': 3})

    def validate_prepare(self):
        source = Path(self.args.prepare).resolve()
        report = m.read(source / 'report.json')
        required = ('r1_acceptance_preflight',
                    'app_lineages_ready_for_withdrawal',
                    'device_baseline_after_app_hierarchy',
                    'unaffected_device_service_before_withdrawal')
        m.require(report.get('environment') == 'dev'
                  and report.get('status') == 'passed'
                  and report.get('foundation_scope') ==
                  'Dev App Root withdrawal at public API, MQTT and TURN boundaries'
                  and all(report.get('checks', {}).get(name, {}).get('status') ==
                          'passed' for name in required),
                  'passed Dev R1 preparation evidence required')
        prepared = report['checks'][
            'app_lineages_ready_for_withdrawal']['evidence']
        m.require(prepared['old_root_id'] ==
                  m.read(Path(self.args.old_authority) /
                         'root-ready.json')['issuer_id']
                  and prepared['successor_root_id'] ==
                  m.read(Path(self.args.authority) /
                         'root-ready.json')['issuer_id']
                  and prepared['old_fingerprint'] ==
                  self.local_identity(
                      self.args.old_email, 'prepare-old-app',
                      prepared['old_root_id'])['fingerprint']
                  and prepared['successor_fingerprint'] ==
                  self.local_identity(
                      self.args.successor_email, 'prepare-successor-app',
                      prepared['successor_root_id'],
                      prepared['successor_intermediate_id'])['fingerprint'],
                  'R1 preparation identities changed')
        self.save('prepare-source.json', {
            'source': str(source),
            'report_sha256': m.digest((source / 'report.json').read_bytes())})
        return prepared

    def withdraw(self):
        prepared = self.validate_prepare()
        old_root, successor_root, old, successor = self.identities()
        fixture = self.app_fixture()
        cloud_id, product_id, device = (
            fixture['cloud_id'], fixture['product_id'], fixture['device_id'])
        m.require((cloud_id, product_id, device) ==
                  (prepared['cloud_id'], prepared['product_id'],
                   prepared['device_id']), 'prepared App fixture changed')
        operation = self.api(
            '/issuers/' + old_root['issuer_id'] + '/operations', {
                'issuer_id': old_root['issuer_id'], 'action': 'revoke',
                'reason': ('Withdraw the superseded Dev App Root after public '
                           'API, MQTT and TURN successor qualification.')},
            key='dev-r1-app-root-withdraw-' + old_root['issuer_id'])
        self.save('withdrawal-operation.json', operation)
        if operation['status'] == 'requested':
            self.approval(operation)
        else:
            m.require(operation['status'] == 'approved',
                      'existing App Root withdrawal operation changed')
        operation_path = '/operations/' + operation['operation_id']

        old_connection = successor_connection = None
        old_mqtt = successor_mqtt = None
        turn_processes = []
        shared, turn_password = self.turn_secrets()
        try:
            old_connection = self.app_connection(old)
            successor_connection = self.app_connection(successor)
            _, old_status, old_token = self.app_request(old, device)
            _, successor_status, successor_token = self.app_request(
                successor, device)
            m.require(old_status == successor_status == 200,
                      'App public baseline changed before withdrawal')
            self.validate_app_token(old, device, old_token)
            self.validate_app_token(successor, device, successor_token)
            self.save('old-app-token.json', old_token)
            self.save('successor-app-token.json', successor_token)
            turn_events = {}
            for label, identity in (('old', old), ('successor', successor)):
                config = self.turn_config(identity, device, shared)
                config['hold_seconds'] = 300
                process, event = self.hold_turn(config)
                turn_processes.append(process)
                turn_events[label] = event
            usernames = {item['Username'] for item in
                         self.turn_allocations(turn_password)}
            m.require(all(event['username'] in usernames
                          for event in turn_events.values()),
                      'held TURN baseline changed before withdrawal')
            old_mqtt, old_opened = self.hold_mqtt(
                'old', old_token, device)
            successor_mqtt, _ = self.hold_mqtt(
                'successor', successor_token, device)

            before = a.dt.datetime.now(a.dt.timezone.utc)
            self.api(operation_path + '/execute', {}, 204)
            policy = self.api('/issuers/' + old_root['issuer_id'] +
                              '/distrust')
            m.require(policy['environment'] == 'dev'
                      and policy['trust_domain'] == 'app'
                      and policy['version'] > 0
                      and re.fullmatch(r'[0-9a-f]{64}',
                                       policy['policy_sha256'])
                      and any(item['issuer_id'] == old_root['issuer_id']
                              and item['operation_id'] ==
                              operation['operation_id']
                              for item in policy['distrusted_roots']),
                      'published App Root distrust policy differs')
            elapsed, age = self.cutoff(old_mqtt, old_opened, before)
            old_mqtt = None
            successor_mqtt.stable(12)

            deadline = time.monotonic() + 45
            states = None
            while states is None:
                try:
                    states = self.consumer_states(
                        policy, old_root, successor_root)
                except RuntimeError:
                    m.require(time.monotonic() < deadline,
                              'App trust installation deadline')
                    time.sleep(3)
            roots_digest = next(iter(states.values()))['state']['roots_pem']
            roots_digest = hashlib.sha256(roots_digest.encode()).hexdigest()
            m.require(all(hashlib.sha256(
                item['state']['roots_pem'].encode()).hexdigest() == roots_digest
                          for item in states.values()),
                      'App consumers installed different Root bundles')
            receipts = self.wait_root_receipts(
                policy['policy_sha256'], roots_digest)

            _, held_old_status, _ = self.app_request(
                old, device, old_connection)
            _, held_successor_status, held_successor_token = self.app_request(
                successor, device, successor_connection)
            _, fresh_old_status, _ = self.app_request(old, device)
            m.require(held_old_status == 0 and fresh_old_status == 0
                      and held_successor_status == 200,
                      'public App mTLS Root enforcement differs')
            self.validate_app_token(
                successor, device, held_successor_token)
            self.mqtt(old_token, device, 'connect', 5)
            self.app_mqtt('successor-after-withdrawal',
                          held_successor_token, device)
            self.wait_turn_policy(
                turn_password, turn_events['old']['username'],
                turn_events['successor']['username'])
            self.api(operation_path + '/revocation-complete', {}, 204)
            m.require(self.api(operation_path)['status'] == 'completed'
                      and self.api('/issuers/' +
                                   old_root['issuer_id'])['status'] == 'revoked',
                      'App Root withdrawal did not complete durably')
            self.check('app_root_withdrawal_enforced', {
                'operation_id': operation['operation_id'],
                'policy_sha256': policy['policy_sha256'],
                'policy_version': policy['version'],
                'receipts': receipts,
                'mqtt_cutoff_seconds': elapsed,
                'mqtt_connection_age_seconds': age,
                'held_old_public_status': held_old_status,
                'fresh_old_public_status': fresh_old_status,
                'successor_public_status': held_successor_status,
                'old_mqtt_code': 5,
                'old_turn_allocation_removed': True,
                'successor_turn_allocation_preserved': True})
        finally:
            for connection in (old_connection, successor_connection):
                if connection:
                    connection.close()
            for process in (old_mqtt, successor_mqtt, *turn_processes):
                if process:
                    process.close()

        before_restart = self.consumer_states(
            policy, old_root, successor_root)
        manifest_updated = self.install_post_withdraw_manifest(successor_root)
        restarted = self.restart_consumers(
            'withdraw-' + policy['policy_sha256'][:16] + '-' +
            m.digest(self.output.name)[:8], successor_root['issuer_id'])
        self.forward_turn_cli()
        self.install_turn_probe()
        after_restart = self.consumer_states(
            policy, old_root, successor_root)
        m.require(all(
            before_restart[name]['pvc_uid'] == after_restart[name]['pvc_uid']
            and before_restart[name]['state'] == after_restart[name]['state']
            and after_restart[name]['mode_owner'] == '600 10001'
            for name in before_restart),
            'App Root state did not survive consumer restart')
        m.require(self.wait_root_receipts(
            policy['policy_sha256'], roots_digest) == receipts,
            'App Root receipts changed after restart')

        _, old_status, _ = self.app_request(old, device)
        _, successor_status, successor_token = self.app_request(
            successor, device)
        m.require(old_status == 0 and successor_status == 200,
                  'post-restart public App trust differs')
        self.validate_app_token(successor, device, successor_token)
        self.mqtt(old_token, device, 'connect', 5)
        self.app_mqtt('successor-after-restart', successor_token, device)
        post_turn = []
        try:
            old_turn, old_event = self.hold_turn(
                self.turn_config(old, device, shared))
            post_turn.append(old_turn)
            successor_turn, successor_event = self.hold_turn(
                self.turn_config(successor, device, shared))
            post_turn.append(successor_turn)
            self.wait_turn_policy(turn_password, old_event['username'],
                                  successor_event['username'])
        finally:
            for process in post_turn:
                process.close()
        self.device_baseline()
        for role in ('requester', 'approver', 'custodian'):
            self.api('/issuers/search', {'limit': 1}, role=role)
        self.check('app_root_withdrawal_restart', {
            'restarted_consumers': restarted,
            'successor_only_manifest_installed': manifest_updated,
            'persistent_state': {name: {
                'pvc': item['pvc'], 'pvc_uid': item['pvc_uid'],
                'state_sha256': item['sha256'],
                'mode_owner': item['mode_owner']}
                for name, item in after_restart.items()},
            'policy_sha256': policy['policy_sha256'],
            'receipts_durable': True,
            'old_public_and_mqtt_denied': True,
            'old_turn_allocation_removed': True,
            'successor_public_mqtt_turn_healthy': True,
            'device_baseline_healthy': True,
            'service_management_callers': 3})

    def recovery_identities(self):
        successor_root = self.app_root('active')
        old_saved = m.read(Path(self.args.old_authority) / 'root-ready.json')
        old_root = self.api('/issuers/' + old_saved['issuer_id'])
        intermediate = m.read(Path(self.args.intermediate) /
                              'intermediate-ready.json')
        m.require(old_root['status'] == 'revoked'
                  and old_root['trust_domain'] == 'app'
                  and old_root['certificate_fingerprint_sha256'] ==
                  old_saved['certificate_fingerprint_sha256'],
                  'recoverable revoked App Root changed')
        old = self.local_identity(self.args.old_email, 'old-app',
                                  old_root['issuer_id'])
        successor = self.local_identity(
            self.args.successor_email, 'successor-app',
            successor_root['issuer_id'], intermediate['issuer_id'])
        return old_root, successor_root, old, successor

    def recover_withdrawal(self):
        source = Path(self.args.resume).resolve()
        report = m.read(source / 'report.json')
        saved_operation = m.read(source / 'withdrawal-operation.json')
        operation = self.api('/operations/' +
                             saved_operation['operation_id'])
        m.require(report.get('environment') == 'dev'
                  and report.get('phase') == 'withdraw'
                  and report.get('status') == 'failed'
                  and report.get('failure') in (
                      'cutoff timing cannot exclude natural lease expiry',
                      'command failed: kubectl')
                  and operation['status'] in (
                      'revocation_pending', 'completed')
                  and operation['request_sha256'] ==
                  saved_operation['request_sha256'],
                  'failed R1 withdrawal is not safely recoverable')
        prepared = self.validate_prepare()
        old_root, successor_root, old, successor = self.recovery_identities()
        m.require(operation['issuer_id'] == old_root['issuer_id'],
                  'R1 withdrawal operation issuer changed')
        fixture = self.app_fixture()
        cloud_id, product_id, device = (
            fixture['cloud_id'], fixture['product_id'], fixture['device_id'])
        m.require((cloud_id, product_id, device) ==
                  (prepared['cloud_id'], prepared['product_id'],
                   prepared['device_id']), 'prepared App fixture changed')
        policy = self.api('/issuers/' + old_root['issuer_id'] + '/distrust')
        m.require(any(item['issuer_id'] == old_root['issuer_id']
                      and item['operation_id'] == operation['operation_id']
                      for item in policy['distrusted_roots']),
                  'recoverable App Root policy changed')
        receipts = self.root_receipts(policy['policy_sha256'])
        m.require(len(receipts) == len(a.CONSUMERS)
                  and {item['consumer_id'] for item in receipts} ==
                  set(a.CONSUMERS)
                  and len({item['loaded_roots_sha256']
                           for item in receipts}) == 1,
                  'recoverable App Root receipts changed')
        roots_digest = receipts[0]['loaded_roots_sha256']
        if operation['status'] == 'revocation_pending':
            self.api('/operations/' + operation['operation_id'] +
                     '/revocation-complete', {}, 204)
        m.require(self.api('/operations/' +
                           operation['operation_id'])['status'] == 'completed',
                  'recovered App Root withdrawal did not complete')

        before_restart = self.consumer_pvc_uids()
        manifest_updated = self.install_post_withdraw_manifest(successor_root)
        restarted = self.restart_consumers(
            'withdraw-' + policy['policy_sha256'][:16] + '-' +
            m.digest(self.output.name)[:8], successor_root['issuer_id'])
        self.forward_turn_cli()
        self.install_turn_probe()
        after_restart = self.consumer_states(
            policy, old_root, successor_root)
        m.require(all(
            before_restart[name]['pvc_uid'] == after_restart[name]['pvc_uid']
            and after_restart[name]['mode_owner'] == '600 10001'
            for name in before_restart),
            'recovered App Root state did not survive restart')
        roots_pem = next(iter(after_restart.values()))['state']['roots_pem']
        m.require(hashlib.sha256(roots_pem.encode()).hexdigest() ==
                  roots_digest
                  and all(item['state']['roots_pem'] == roots_pem
                          for item in after_restart.values()),
                  'recovered App consumers installed different Root bundles')
        m.require(self.wait_root_receipts(
            policy['policy_sha256'], roots_digest) == receipts,
            'recovered App Root receipts changed after restart')

        _, old_status, _ = self.app_request(old, device)
        _, successor_status, successor_token = self.app_request(
            successor, device)
        m.require(old_status == 0 and successor_status == 200,
                  'recovered post-restart public App trust differs')
        self.validate_app_token(successor, device, successor_token)
        self.app_mqtt('successor-after-recovery-restart',
                      successor_token, device)
        shared, turn_password = self.turn_secrets()
        post_turn = []
        try:
            old_turn, old_event = self.hold_turn(
                self.turn_config(old, device, shared))
            post_turn.append(old_turn)
            successor_turn, successor_event = self.hold_turn(
                self.turn_config(successor, device, shared))
            post_turn.append(successor_turn)
            self.wait_turn_policy(turn_password, old_event['username'],
                                  successor_event['username'])
        finally:
            for process in post_turn:
                process.close()
        self.check('app_root_withdrawal_recovered_enforcement', {
            'source': str(source),
            'operation_id': operation['operation_id'],
            'policy_sha256': policy['policy_sha256'],
            'receipts': receipts,
            'old_mqtt_session_closed_before_prior_timing_assertion': True,
            'old_public_handshake_denied': True,
            'old_turn_allocation_removed': True,
            'successor_public_mqtt_turn_healthy': True})
        self.device_baseline()
        for role in ('requester', 'approver', 'custodian'):
            self.api('/issuers/search', {'limit': 1}, role=role)
        self.check('app_root_withdrawal_restart', {
            'restarted_consumers': restarted,
            'successor_only_manifest_installed': manifest_updated,
            'persistent_state': {name: {
                'pvc': item['pvc'], 'pvc_uid': item['pvc_uid'],
                'state_sha256': item['sha256'],
                'mode_owner': item['mode_owner']}
                for name, item in after_restart.items()},
            'policy_sha256': policy['policy_sha256'],
            'operation_status': 'completed',
            'receipts_durable': True,
            'old_public_and_turn_denied': True,
            'successor_public_mqtt_turn_healthy': True,
            'device_baseline_healthy': True,
            'service_management_callers': 3})

    def close(self):
        try:
            if getattr(self, 'turn_pod', ''):
                self.kube([
                    '-n', NS, 'exec', 'pod/' + self.turn_pod,
                    '-c', 'coturn-cli-tunnel', '--', 'rm', '-f',
                    '/tmp/pkiappacceptance'], timeout=20)
        except RuntimeError:
            pass
        super().close()


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--phase', required=True,
                        choices=('prepare', 'withdraw'))
    parser.add_argument('--authority', required=True,
                        help='successor App Root evidence')
    parser.add_argument('--old-authority', required=True)
    parser.add_argument('--intermediate', required=True,
                        help='successor App intermediate evidence')
    parser.add_argument('--database', required=True)
    parser.add_argument('--old-email', required=True)
    parser.add_argument('--successor-email', required=True)
    parser.add_argument('--image', required=True)
    parser.add_argument('--output', required=True)
    parser.add_argument('--prepare', help='passed R1 preparation evidence')
    parser.add_argument('--resume')
    args = parser.parse_args()
    lock = (Path(args.config_root).expanduser() /
            'dev/pki/app-hierarchy-rollout.lock')
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = AppRootAcceptance(args)
    try:
        runner.preflight_acceptance()
        if args.phase == 'prepare':
            runner.prepare()
        elif not args.resume:
            m.require(args.prepare, '--prepare is required for withdrawal')
            runner.withdraw()
        else:
            m.require(args.prepare, '--prepare is required for recovery')
            runner.recover_withdrawal()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'
        runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report)
        runner.close()
        os.close(owner)
        print(json.dumps({'status': runner.report['status'],
                          'report': str(runner.output / 'report.json')}),
              flush=True)


if __name__ == '__main__':
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(
        RuntimeError('interrupted; reconcile R1 App acceptance before retry')))
    signal.signal(signal.SIGINT, lambda *_: (_ for _ in ()).throw(
        RuntimeError('interrupted; reconcile R1 App acceptance before retry')))
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
