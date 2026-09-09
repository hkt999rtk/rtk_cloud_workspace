#!/usr/bin/env python3
"""Dev Account Manager caller acceptance; reuse an approved Product and a test App user."""
import argparse
import base64
import datetime as dt
import http.client
import importlib.util
import json
import os
from pathlib import Path
import sqlite3
import ssl
import time
import traceback
import uuid

spec = importlib.util.spec_from_file_location('service', Path(__file__).with_name('run.py'))
s = importlib.util.module_from_spec(spec)
spec.loader.exec_module(s)
m = s.m
AM_NS = 'video-cloud-dev-account-manager'
CLOUD = '5382d0cf-0966-45e9-ad5e-9955f4f6360d'
PRODUCT = '773aa199-16e3-4d59-94c6-5cdb5801c02f'
API_HOST = 'device.video-cloud-dev.realtekconnect.com'
ACCOUNT_HOST = 'account-manager-internal.' + AM_NS + '.svc'
SERVICE_ROOT = '87099089d30f13a7b93035b59c1c0c91bdb05bbe3dab427258c0c48e38144cc2'


def app_request(context, device):
    # Standard TLS verification includes the public server chain and hostname.
    connection = http.client.HTTPSConnection(API_HOST, context=context, timeout=30)
    try:
        connection.request('POST', '/request_token', json.dumps({'scope': 'app', 'devid': device, 'service': 'mqtt'}),
                           {'Content-Type': 'application/json'})
        response = connection.getresponse()
        raw = response.read((1 << 20) + 1)
        m.require(len(raw) <= 1 << 20, 'oversized App response')
        m.require(response.getheader('Content-Type', '').startswith('application/json'),
                  'App request did not reach the JSON API')
        body = json.loads(raw)
        return response.status, body
    finally:
        connection.close()


class CallerRun(m.Acceptance):
    def prepare(self, args):
        self.started_at = m.stamp()
        self.report['checks'] = {}
        self.report['foundation_scope'] = 'Factory enrollment and public App API over managed Account Manager callers'
        self.report['runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.report['not_qualified'] += ['caller renewal/retirement', 'held connections', 'trust outage']
        m.require(self.kube(['config', 'current-context']).strip() == self.context, 'wrong dev context')
        deployments = {}
        for name, ns in [('account-manager', AM_NS), ('factoryenroll', m.NS), ('video-cloud-api', m.NS)]:
            obj = self.obj('deployment', name, ns)
            m.require(obj['status'].get('readyReplicas') == obj['spec']['replicas'] == 1
                      and obj['status'].get('observedGeneration') == obj['metadata']['generation'],
                      'caller deployment not ready: ' + name)
            deployments[name] = {c['name']: c['image'] for c in obj['spec']['template']['spec']['containers']}
            if name != 'account-manager':
                env = {e['name']: e.get('value') for c in obj['spec']['template']['spec']['containers'] for e in c.get('env', [])}
                prefix = 'VIDEO_CLOUD_ACCOUNT_MANAGER_' if name == 'video-cloud-api' else 'FACTORY_ENROLL_ACCOUNT_MANAGER_'
                m.require(env.get(prefix + ('INTERNAL_URL' if name == 'video-cloud-api' else 'URL')) == 'https://' + ACCOUNT_HOST + ':8443'
                          and env.get(prefix + 'SERVER_PKI_NAME') == ACCOUNT_HOST
                          and env.get(prefix + 'SERVER_PKI_ROOT_SHA256') == SERVICE_ROOT,
                          'managed Account Manager destination differs: ' + name)
                identity_prefix = prefix if name == 'video-cloud-api' else 'FACTORY_ENROLL_SERVICE_'
                m.require(env.get(identity_prefix + 'IDENTITY_STATE') and env.get(identity_prefix + 'IDENTITY_ROOT_SHA256') == SERVICE_ROOT,
                          'managed caller identity missing: ' + name)
                m.require(not env.get(prefix + 'MANAGEMENT_CERT') and not env.get(prefix + 'MANAGEMENT_KEY')
                          and not env.get(identity_prefix + 'IDENTITY_BOOTSTRAP_CERT')
                          and not env.get(identity_prefix + 'IDENTITY_BOOTSTRAP_KEY'),
                          'static caller credential is configured: ' + name)
        self.forward('am', AM_NS, 'account-manager', 80)
        self.forward('factory', m.NS, 'factoryenroll', 80)
        self.accounts = m.read(self.foundation / 'accounts.json')
        secret = self.obj('secret', 'account-manager-runtime', AM_NS)['data']
        self.admin = {name: base64.b64decode(secret[key]).decode() for name, key in (
            ('email', 'ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL'),
            ('password', 'ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD'))}
        with sqlite3.connect(Path(args.database).resolve().as_uri() + '?mode=ro', uri=True) as db:
            db.row_factory = sqlite3.Row
            rows = db.execute('SELECT * FROM users WHERE email = ?', (args.email,)).fetchall()
        m.require(len(rows) == 1 and rows[0]['brand_cloud_id'] == CLOUD, 'one test user in the governed dev Cloud required')
        self.user = dict(rows[0])
        m.require(args.email.startswith('pki-service-caller-') and args.email.endswith('@dev.invalid'),
                  'dedicated dev caller test user required')
        credentials = json.loads(self.user['app_credentials_json'])
        certificate = json.loads(self.user['app_certificate_json'])
        self.save('app-key.pem', credentials['private_key_pem'])
        self.save('app-chain.pem', certificate['certificate_chain_pem'])
        self.app_context = ssl.create_default_context()
        self.app_context.load_cert_chain(self.output / 'app-chain.pem', self.output / 'app-key.pem')
        owner = self.http('/auth/login', self.accounts['requester'])
        cloud = self.http('/developer/brand-clouds/' + CLOUD, token=owner['tokens']['access_token'])
        m.require(cloud['brand_cloud']['role'] == 'owner', 'rehearsal requester must own the Cloud')
        if args.owner_identity:
            self.owner_context = ssl.create_default_context()
            self.owner_context.load_cert_chain(args.owner_identity / 'owner-chain.pem', args.owner_identity / 'owner-key.pem')
        else:
            m.require(owner['app_certificate']['status'] == 'csr_required',
                      'owner already has an App certificate; provide its retained --owner-identity')
            csr = self.key('owner', 'app-user:' + owner['user']['id'])
            self.save('owner-issuance-intent.json', {'user_id': owner['user']['id'], 'csr_sha256': m.digest(csr)})
            issued = self.http('/auth/login', dict(self.accounts['requester'], app_csr_pem=csr))
            self.save('owner-certificate.json', issued['app_certificate'])
            self.save('owner-chain.pem', issued['app_certificate']['certificate_chain_pem'])
            self.owner_context = ssl.create_default_context()
            self.owner_context.load_cert_chain(self.output / 'owner-chain.pem', self.output / 'owner-key.pem')
        self.check('preflight', {'deployments': deployments, 'user_id': self.user['user_id'],
                                 'cloud': CLOUD, 'product': PRODUCT, 'public_origin': 'https://' + API_HOST})

    def restart_caller(self, name):
        ns, container, paths = {
            'video-cloud-api': (m.NS, 'app', ['/var/lib/video-cloud-api-pki/private/identity.json']),
            'factoryenroll': (m.NS, 'factoryenroll', ['/state/identity/client.json']),
            'account-manager': (AM_NS, 'pkimanagement', ['/var/lib/account-pki/private/identity.json',
                                                       '/var/lib/account-pki/private/account-listener-state.json']),
        }[name]
        owner = self.obj('deployment', name, ns)
        selector = ','.join(k + '=' + v for k, v in owner['spec']['selector']['matchLabels'].items())
        def pod():
            pods = json.loads(self.kube(['-n', ns, 'get', 'pods', '-l', selector, '-o', 'json']))['items']
            return [p for p in pods if not p['metadata'].get('deletionTimestamp')]
        def hashes(current):
            return self.kube(['-n', ns, 'exec', current['metadata']['name'], '-c', container, '--', 'sha256sum'] + paths).strip()
        pods = pod()
        m.require(len(pods) == 1, 'one identity owner required before restart')
        before, state = pods[0], hashes(pods[0])
        intent = {'deployment': name, 'pod_uid': before['metadata']['uid'], 'state_hashes': state}
        self.save(name + '-restart-intent.json', intent)
        options = {'apiVersion': 'v1', 'kind': 'DeleteOptions',
                   'preconditions': {k: before['metadata'][k] for k in ('uid', 'resourceVersion')}}
        self.kube(['delete', '--raw', '/api/v1/namespaces/' + ns + '/pods/' + before['metadata']['name'], '-f', '-'], json.dumps(options))
        deadline = time.monotonic() + 180
        while time.monotonic() < deadline:
            pods = pod()
            if len(pods) == 1 and pods[0]['metadata']['uid'] != before['metadata']['uid'] and all(
                    c.get('ready') for c in pods[0]['status'].get('containerStatuses', [])) and pods[0]['status'].get('containerStatuses'):
                break
            time.sleep(2)
        else:
            raise RuntimeError('identity owner restart deadline: ' + name)
        m.require(hashes(pods[0]) == state, 'managed identity changed across restart')
        m.require(self.obj('deployment', name, ns)['spec'] == owner['spec'], 'deployment changed during restart')
        if name == 'account-manager':
            self.forward('am', AM_NS, name, 80)
        if name == 'factoryenroll':
            self.forward('factory', m.NS, name, 80)
        self.check(name + '_restart', dict(intent, replacement_pod_uid=pods[0]['metadata']['uid']))

    def replay_factory(self, fixture):
        request = m.read(fixture / 'enroll-request.json')
        original = m.read(fixture / 'enrolled.json')
        run = m.read(fixture / 'production-run.json')
        result = self.http('/factory/enroll', request, run['factory_jwt'], service='factory')
        m.require(result['certificate_pem'] == original['certificate_pem'], 'enrollment replay issued another certificate')
        self.check('factory_exact_replay', {'device': request['devid'], 'same_certificate': True})

    def logs(self):
        raw = self.kube(['-n', AM_NS, 'logs', 'deployment/account-manager', '-c', 'app', '--since-time=' + self.started_at])
        records = []
        for line in raw.splitlines():
            try:
                row = json.loads(line)
            except ValueError:
                continue
            path = row.get('path', '')
            if path == '/v1/internal/app-token-authorizations' or path.startswith('/v1/internal/factory-enrollments/'):
                records.append({key: row.get(key) for key in ('ts', 'path', 'status', 'remote_addr')})
        m.require(any(r['path'] == '/v1/internal/app-token-authorizations' and r['status'] == 200
                      and r['remote_addr'] == '127.0.0.1' for r in records), 'no successful loopback Account Manager authorization log')
        m.require(any(r['path'] == '/v1/internal/factory-enrollments/reserve' and r['status'] == 200
                      and r['remote_addr'] == '127.0.0.1' for r in records), 'no successful loopback factory reservation log')
        self.check('account_manager_internal_requests', {'records': records})

    def enroll(self):
        issuers = self.api('/issuers/search', {'limit': 100})['items']
        issuers = [i for i in issuers if i.get('device_item_profile_id') == PRODUCT and i['status'] == 'active']
        m.require(len(issuers) == 1 and issuers[0]['environment'] == 'dev'
                  and issuers[0]['trust_domain'] == 'device', 'one active governed dev Product issuer required')
        admin = self.login(self.admin)[0]
        profiles = self.http('/admin/brand-clouds/' + CLOUD + '/device-item-profiles', token=admin)
        profiles = [p for p in profiles['device_item_profiles'] if p['id'] == PRODUCT]
        m.require(len(profiles) == 1 and profiles[0]['status'] == 'active', 'active dev Product profile required')
        profile = profiles[0]
        now = dt.datetime.now(dt.timezone.utc)
        batch = 'dev-pki-service-' + uuid.uuid4().hex
        request = {'factory_id': 'dev-pki-rehearsal', 'batch_id': batch, 'allowed_quantity': 1,
                   'valid_from': m.stamp(now - dt.timedelta(minutes=1)),
                   'valid_until': m.stamp(now + dt.timedelta(hours=24))}
        self.save('production-run-request.json', request)
        run = self.http('/admin/brand-clouds/' + CLOUD + '/device-item-profiles/' + PRODUCT + '/production-runs',
                        request, admin, 201)
        self.save('production-run.json', run)
        device = 'pki-service-' + uuid.uuid4().hex
        body = {'request_id': uuid.uuid4().hex, 'devid': device, 'csr_pem': self.key('device', device),
                'serial_number': batch, 'production_run_id': run['production_run']['id'], 'brand_cloud_id': CLOUD,
                'device_item_profile_id': PRODUCT, 'factory_id': request['factory_id'], 'batch_id': batch,
                'entitlement_state': 'active'}
        body.update({key: profile[key] for key in ('profile_key', 'ca_profile', 'issuer_profile', 'service_options')})
        self.save('enroll-request.json', body)
        result = self.http('/factory/enroll', body, run['factory_jwt'], service='factory')
        self.save('enrolled.json', result)
        m.require(result['certificate_bundle']['issuance']['issuer_id'] == issuers[0]['issuer_id'], 'wrong Product issuer')
        self.check('factory_enrollment', {'device': device, 'issuer_id': issuers[0]['issuer_id'],
                                           'production_run_id': run['production_run']['id']})
        return device

    def bind(self, device):
        admin = self.login(self.admin)[0]
        request = {'organization_id': CLOUD, 'video_cloud_devid': device, 'device_item_profile_id': PRODUCT,
                   'activity_id': device, 'clip_public_key': 'dev-caller-mqtt-only', 'service_options': ['mqtt'],
                   'expires_at': m.stamp(dt.datetime.now(dt.timezone.utc) + dt.timedelta(hours=1))}
        self.save('bind-request.json', request)
        claim = self.http('/admin/device-claim-tokens', request, admin, 201)
        m.require(claim['device_claim_token']['video_cloud_devid'] == device
                  and claim['device_claim_token']['organization_id'] == CLOUD, 'claim does not match fixture')
        self.save('claim.json', claim)
        result = self.http('/orgs/' + CLOUD + '/devices/claim/resolve',
                           {'claim_token': claim['claim_token'], 'device_name': device}, self.token(), 201)
        self.save('bound.json', result)
        self.check('app_device_binding', {'device': device})

    def probe_calls(self, device):
        denied, denial = app_request(self.app_context, 'pki-service-unassigned-' + uuid.uuid4().hex)
        m.require(denied == 401 and not denial.get('access_token'), 'unassigned Device was not denied')
        self.check('unassigned_device_denied', {'http_status': denied})
        member_status, member_result = app_request(self.app_context, device)
        m.require(member_status == 401 and not member_result.get('access_token'), 'member without Product admission was allowed')
        self.check('member_without_product_denied', {'http_status': member_status, 'device': device})
        status, result = app_request(self.owner_context, device)
        m.require(status == 200 and result.get('access_token'), 'assigned App token was not issued: HTTP ' + str(status))
        # Tokens remain private; reports contain only the observed result.
        self.check('assigned_app_token', {'http_status': status, 'device': device, 'token_present': True})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default='~/.config/rtk_cloud')
    parser.add_argument('--output', required=True)
    parser.add_argument('--database', required=True)
    parser.add_argument('--email', required=True)
    parser.add_argument('--fixture', type=Path, help='Reuse a previously enrolled and bound caller fixture; no enrollment/binding mutation')
    parser.add_argument('--owner-identity', type=Path, help='Retained owner App identity directory; never rotate it implicitly')
    parser.add_argument('--restart', choices=('video-cloud-api', 'factoryenroll', 'account-manager'),
                        help='Restart one existing identity owner, verify unchanged state, then repeat caller checks')
    args = parser.parse_args()
    m.require(not args.restart or (args.fixture and args.owner_identity),
              '--restart requires an enrolled/bound fixture and retained owner identity')
    os.umask(0o077)
    runner = CallerRun(args.config_root, 'lke649805-ctx', args.output)
    try:
        runner.prepare(args)
        if args.fixture:
            request = m.read(args.fixture / 'enroll-request.json')
            m.require(request['brand_cloud_id'] == CLOUD and request['device_item_profile_id'] == PRODUCT,
                      'fixture Product/Cloud differs')
            device = request['devid']
            m.require((args.fixture / 'enrolled.json').is_file(), 'successful enrollment response required')
        else:
            device = runner.enroll()
            runner.bind(device)
        if args.restart:
            runner.restart_caller(args.restart)
        if args.fixture:
            runner.replay_factory(args.fixture)
        runner.probe_calls(device)
        runner.logs()
        runner.report['status'] = 'passed'
    except BaseException as error:
        runner.report['status'] = 'failed'
        runner.report['failure'] = str(error) if isinstance(error, RuntimeError) else type(error).__name__
        frame = traceback.extract_tb(error.__traceback__)[-1]
        runner.report['failure_location'] = Path(frame.filename).name + ':' + str(frame.lineno)
        print(json.dumps({'status': 'failed', 'failure': runner.report['failure']}), flush=True)
    finally:
        runner.save('report.json', runner.report)
        runner.close()
    return 0 if runner.report['status'] == 'passed' else 1


if __name__ == '__main__':
    raise SystemExit(main())
