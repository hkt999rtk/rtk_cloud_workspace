#!/usr/bin/env python3
"""Dev Account Manager managed credential adoption; separate private evidence per phase."""
import argparse
import base64
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import secrets
import signal
import sys
from urllib.parse import urlsplit, urlunsplit

spec = importlib.util.spec_from_file_location('managed_hosts', Path(__file__).with_name('hosts.py'))
h = importlib.util.module_from_spec(spec)
spec.loader.exec_module(h)
m, NS, AM_NS = h.m, h.NS, h.AM_NS
ROLE = 'rtk_pki_management_dev'
PVC = 'account-manager-service-identity'
BOOTSTRAP = 'account-manager-pki-provisioner'
SOCKET = '/run/account-pki/private/controller.sock'
STATE = '/var/lib/account-pki/private/identity.json'
JWT_FILES = ['jwt-access.key', 'jwt-access.pub', 'jwt-refresh.key', 'jwt-refresh.pub']


def owner_settings(root):
    issuer, controller = 'certissuer.' + NS + '.svc', 'pki-controller.' + NS + '.svc'
    settings = {'PKI_ENVIRONMENT': 'dev', 'PKI_MANAGEMENT_SOCKET': SOCKET,
                'PKI_MANAGEMENT_IDENTITY_STATE': STATE, 'PKI_MANAGEMENT_IDENTITY_ROOT_SHA256': root,
                'PKI_MANAGEMENT_ISSUER_URL': 'https://' + issuer + ':9443',
                'PKI_MANAGEMENT_CONTROLLER_URL': 'https://' + controller + ':18446',
                'PKI_MANAGEMENT_BOOTSTRAP_CERT': '/run/pki-bootstrap/tls.crt',
                'PKI_MANAGEMENT_BOOTSTRAP_KEY': '/run/pki-bootstrap/tls.key'}
    for prefix, name in [('ISSUER', issuer), ('CONTROLLER', controller)]:
        settings.update({'PKI_MANAGEMENT_' + prefix + '_' + key: value for key, value in {
            'SERVER_PKI_ROOT_SHA256': root, 'SERVER_PKI_NAME': name,
            'TLS_CA': '/run/pki-root/root.pem', 'SERVER_PKI_SWEEP_INTERVAL': '10s'}.items()})
    return settings


def managed_template(owner, app_image, owner_image, root):
    m.require(owner['spec']['replicas'] == 1, 'one dev identity owner required')
    template = json.loads(json.dumps(owner['spec']['template']))
    pod = template['spec']
    m.require(len(pod['containers']) == 1 and pod['containers'][0]['name'] == 'app'
              and not pod.get('initContainers'), 'unexpected Account Manager topology')
    app = pod['containers'][0]
    m.require(not any(e['name'] == 'PKI_CONTROLLER_SOCKET' for e in app['env']), 'already managed; reconcile')
    app['image'] = app_image
    app['env'] = h.h.with_env(app['env'], {'PKI_CONTROLLER_SOCKET': SOCKET, 'PKI_CONTROLLER_CLIENT_CERT': '',
                                         'PKI_CONTROLLER_CLIENT_KEY': '', 'PKI_CONTROLLER_CA': ''})
    app['volumeMounts'].append({'name': 'management-socket', 'mountPath': '/run/account-pki'})
    auth = [v for v in pod['volumes'] if v['name'] == 'pki-auth']
    m.require(len(auth) == 1 and auth[0]['secret']['secretName'] == 'account-manager-pki-auth', 'JWT mount changed')
    auth[0]['secret']['items'] = [{'key': name, 'path': name} for name in JWT_FILES]
    pod['securityContext'] = dict(pod.get('securityContext', {}), runAsUser=10001, runAsGroup=10001,
                                 fsGroup=10001, fsGroupChangePolicy='OnRootMismatch')
    pod['volumes'] += [{'name': 'management-socket', 'emptyDir': {}},
                       {'name': 'management-state', 'persistentVolumeClaim': {'claimName': PVC}},
                       {'name': 'management-bootstrap', 'secret': {'secretName': BOOTSTRAP, 'defaultMode': 0o440}},
                       {'name': 'management-root', 'configMap': {'name': 'account-manager-service-root'}}]
    pod['initContainers'] = [{'name': 'management-socket-directory', 'image': owner_image,
        'command': ['sh', '-c', 'set -eu; umask 077; mkdir /socket/private; chmod 700 /socket/private'],
        'volumeMounts': [{'name': 'management-socket', 'mountPath': '/socket'}]}]
    pod['containers'].append({'name': 'pkimanagement', 'image': owner_image, 'command': ['/app/pkimanagement'],
        'env': [{'name': k, 'value': v} for k, v in owner_settings(root).items()] + [
            {'name': 'PKI_DATABASE_URL', 'valueFrom': {'secretKeyRef': {'name': 'account-manager-pki-verifier', 'key': 'dsn'}}}],
        'volumeMounts': [{'name': 'management-socket', 'mountPath': '/run/account-pki'},
                        {'name': 'management-state', 'mountPath': '/var/lib/account-pki'},
                        {'name': 'management-bootstrap', 'mountPath': '/run/pki-bootstrap', 'readOnly': True},
                        {'name': 'management-root', 'mountPath': '/run/pki-root', 'readOnly': True}],
        'startupProbe': {'exec': {'command': ['test', '-S', SOCKET]}, 'periodSeconds': 5, 'failureThreshold': 60},
        'readinessProbe': {'exec': {'command': ['test', '-S', SOCKET]}, 'periodSeconds': 5},
        'resources': {'requests': {'cpu': '25m', 'memory': '64Mi'}, 'limits': {'memory': '256Mi'}}})
    return template


def render_management(base):
    desired = m.read(base / 'pki/controller-bootstrap/rollout/account-manager-deployment.json')
    m.require(desired['metadata']['namespace'] == AM_NS and desired['metadata']['name'] == 'account-manager'
              and desired['spec']['replicas'] == 1 and desired['spec']['strategy']['type'] == 'Recreate', 'persisted management scope differs')
    containers = desired['spec']['template']['spec']['containers']
    m.require([c['name'] for c in containers] == ['app', 'pkimanagement'], 'persisted management owners differ')
    image = (base / 'operator/env/PKI_ACCOUNT_MANAGER_API_IMAGE').read_text().strip()
    m.require(h.h.re.fullmatch(r'ghcr\.io/hkt999rtk/rtk_cloud_dev/account-manager@sha256:[0-9a-f]{64}', image), 'persisted management image not pinned')
    containers[0]['image'] = image
    owner_pin = base / 'operator/env/PKI_ACCOUNT_MANAGER_OWNER_IMAGE'
    if owner_pin.exists():
        owner_image = owner_pin.read_text().strip()
        m.require(h.h.re.fullmatch(r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}', owner_image), 'persisted identity owner image not pinned')
        containers[1]['image'] = owner_image
    return desired


class ManagementRun(h.HostRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['management_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.report['foundation_scope'] = 'Dev managed Account Manager controller credential adoption'

    def create(self, obj):
        ns, name = obj['metadata']['namespace'], obj['metadata']['name']
        m.require(ns in (NS, AM_NS), 'unexpected creation namespace')
        self.save('create-' + name + '.json', obj)
        created = json.loads(self.kube(['-n', ns, 'create', '-f', '-', '-o', 'json'], json.dumps(obj)))
        self.save('created-' + name + '.json', created)
        m.write(self.base / 'pki/controller-bootstrap/rollout' / (name + '-' + obj['kind'].lower() + '.json'), obj)
        return created

    def sql(self, query):
        return self.kube(['-n', 'video-cloud-dev-platform', 'exec', '-i', 'postgresql-0', '--',
                         'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'video_cloud', '-At'], query).strip()

    def issuance(self):
        raw = self.sql("SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,subject,status,fingerprint,certificate_pem,issued_at,revoked_at "
                       "FROM pki_service_client_issuances WHERE environment='dev' AND subject='service:account-manager') t;")
        rows = [json.loads(line) for line in raw.splitlines() if line]
        m.require(len(rows) == 1 and rows[0]['status'] == 'succeeded' and rows[0]['revoked_at'] is None,
                  'expected exactly one current managed issuance; reconcile')
        return rows[0]

    def prepare_management(self):
        root, issuer = self.hierarchy()
        m.require(self.sql("SELECT count(*) FROM pki_service_client_issuances WHERE environment='dev' AND subject='service:account-manager';") == '0',
                  'Account Manager issuance already exists; reconcile')
        owner = self.obj('deployment', 'account-manager', AM_NS)
        m.require(len(owner['spec']['template']['spec']['containers']) == 1, 'Account Manager already has owner')
        self.save('account-manager-before.json', owner)
        # Dedicated verifier login; existing NOLOGIN grants are retained, not reset globally.
        m.require(self.sql("SELECT NOT(rolsuper OR rolcanlogin OR rolcreatedb OR rolcreaterole OR rolbypassrls) FROM pg_roles WHERE rolname='rtk_pki_verifier';") == 't',
                  'verifier group not safe')
        password = secrets.token_hex(32)
        self.save('database-password', password)
        self.sql("BEGIN; CREATE ROLE " + ROLE + " LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS "
                 "CONNECTION LIMIT 12 PASSWORD '" + password + "' IN ROLE rtk_pki_verifier; COMMIT;")
        self.verify_database_role()
        controller = self.obj('deployment', 'pki-controller')['spec']['template']['spec']['containers'][0]
        setting = next(e for e in controller['env'] if e['name'] == 'PKI_DATABASE_URL')
        if 'value' in setting:
            dsn = setting['value']
        else:
            ref = setting['valueFrom']['secretKeyRef']
            dsn = base64.b64decode(self.obj('secret', ref['name'])['data'][ref['key']]).decode()
        parsed = urlsplit(dsn)
        m.require(parsed.scheme in ('postgres', 'postgresql') and parsed.path == '/video_cloud'
                  and parsed.hostname in ('postgresql.video-cloud-dev-platform.svc', 'postgresql.video-cloud-dev-platform.svc.cluster.local'), 'unexpected registry database')
        dsn = urlunsplit((parsed.scheme, ROLE + ':' + password + '@' + parsed.hostname + ':' + str(parsed.port or 5432), parsed.path, parsed.query, ''))
        self.create({'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': 'account-manager-pki-verifier', 'namespace': AM_NS},
                     'type': 'Opaque', 'data': {'dsn': base64.b64encode(dsn.encode()).decode()}})
        self.create({'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': 'account-manager-service-root', 'namespace': AM_NS},
                     'immutable': True, 'data': {'root.pem': root['certificate_pem']}})
        self.create({'apiVersion': 'v1', 'kind': 'PersistentVolumeClaim', 'metadata': {'name': PVC, 'namespace': AM_NS},
                     'spec': {'accessModes': ['ReadWriteOnce'], 'storageClassName': 'linode-block-storage-retain', 'resources': {'requests': {'storage': '10Gi'}}}})
        for name in ('bootstrap-ca', 'bootstrap'):
            m.command([self.openssl, 'genpkey', '-algorithm', 'EC', '-pkeyopt', 'ec_paramgen_curve:P-256', '-out', self.output / (name + '.key')])
        m.command([self.openssl, 'req', '-new', '-x509', '-key', self.output / 'bootstrap-ca.key', '-days', '2',
                   '-subj', '/CN=dev-account-manager-bootstrap', '-addext', 'basicConstraints=critical,CA:TRUE,pathlen:0',
                   '-addext', 'keyUsage=critical,keyCertSign,cRLSign', '-out', self.output / 'bootstrap-ca.crt'])
        m.command([self.openssl, 'req', '-new', '-key', self.output / 'bootstrap.key', '-subj', '/CN=dev-account-manager-provisioner', '-out', self.output / 'bootstrap.csr'])
        self.save('bootstrap.ext', 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=clientAuth\n')
        m.command([self.openssl, 'x509', '-req', '-in', self.output / 'bootstrap.csr', '-CA', self.output / 'bootstrap-ca.crt',
                   '-CAkey', self.output / 'bootstrap-ca.key', '-set_serial', '1', '-days', '1', '-extfile', self.output / 'bootstrap.ext', '-out', self.output / 'bootstrap.crt'])
        self.create({'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': BOOTSTRAP, 'namespace': AM_NS}, 'type': 'Opaque',
                     'data': {key: base64.b64encode((self.output / name).read_bytes()).decode() for key, name in [('tls.crt', 'bootstrap.crt'), ('tls.key', 'bootstrap.key')]}})
        policy = m.read(Path(self.args.intermediate) / 'provider-policies.json')
        m.require(policy['issuer_id'] == issuer['issuer_id'], 'provider policy scope changed')
        self.role_policy('certissuer-pki-dev', 'pki-service-client-dev-' + issuer['issuer_id'], policy['service_client_signer_policy'])
        self.patch_trust('certissuer-runtime', 'client-ca.crt', (self.output / 'bootstrap-ca.crt').read_text())
        before = self.obj('deployment', 'certissuer')
        previous = next((e.get('value') for e in before['spec']['template']['spec']['containers'][0]['env']
                         if e['name'] == 'CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN'), '^service-provisioner$')
        self.save('previous-provisioner.json', {'pattern': previous})
        self.rollout('certissuer', {'CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN': '^(?:' + previous + '|dev-account-manager-provisioner)$'})
        self.device_baseline()
        self.check('management_prepared', {'verifier_role': ROLE, 'pvc': PVC, 'temporary_bootstrap': True})

    def verify_database_role(self):
        query = "SELECT NOT(rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls) AND rolcanlogin FROM pg_roles WHERE rolname='" + ROLE + "';"
        m.require(self.sql(query) == 't', 'verifier login is privileged')
        m.require(self.sql("SELECT bool_and(NOT has_table_privilege('" + ROLE + "',c.oid,'INSERT,UPDATE,DELETE,TRUNCATE,TRIGGER') "
                           "AND NOT has_any_column_privilege('" + ROLE + "',c.oid,'INSERT,UPDATE')) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace "
                           "WHERE n.nspname='public' AND c.relkind='r' AND c.relname LIKE 'pki_%';") == 't', 'verifier has PKI write permissions')
        m.require(self.sql("SELECT NOT has_schema_privilege('" + ROLE + "','public','CREATE') AND has_column_privilege('" + ROLE + "','pki_service_client_issuances','fingerprint','SELECT') "
                           "AND has_column_privilege('" + ROLE + "','pki_server_issuances','fingerprint','SELECT') AND has_table_privilege('" + ROLE + "','pki_crls','SELECT');") == 't', 'verifier grants missing or schema writable')
        self.check('management_verifier_grants', {'login_role': ROLE, 'pki_writes_and_schema_create_denied': True})

    def preparation(self):
        source = Path(self.args.preparation)
        m.require(m.read(source / 'report.json')['status'] == 'passed', 'successful management preparation required')
        return source

    def worker_images(self):
        objects = json.loads(self.kube(['-n', AM_NS, 'get', 'deployments', '-o', 'json']))['items']
        return {o['metadata']['name']: {c['name']: c['image'] for c in o['spec']['template']['spec']['containers']}
                for o in objects if o['metadata']['name'] != 'account-manager'}

    def adopt(self):
        source = self.preparation()
        root, _ = self.hierarchy()
        current = self.obj('deployment', 'account-manager', AM_NS)
        saved = m.read(source / 'account-manager-before.json')
        m.require(current['metadata']['uid'] == saved['metadata']['uid'] and current['spec'] == saved['spec'],
                  'Account Manager changed since preparation')
        ref = m.read(source / ('created-' + PVC + '.json'))
        m.require(self.obj('persistentvolumeclaim', PVC, AM_NS)['metadata']['uid'] == ref['metadata']['uid'], 'prepared identity PVC replaced')
        image = self.args.image
        m.require(h.h.re.fullmatch(r'ghcr\.io/hkt999rtk/rtk_cloud_dev/account-manager@sha256:[0-9a-f]{64}', image or ''), 'verified dev Account Manager image required')
        owner_image = self.obj('deployment', 'certissuer')['spec']['template']['spec']['containers'][0]['image']
        self.save('worker-images.json', self.worker_images())
        template = managed_template(current, image, owner_image, root['certificate_fingerprint_sha256'])
        self.scoped_patch('deployment', current, [{'op': 'replace', 'path': '/spec/strategy', 'value': {'type': 'Recreate'}},
                                                {'op': 'replace', 'path': '/spec/template', 'value': template}])
        # scoped_patch uses metadata namespace. Save the API-only image separately from unchanged worker images.
        m.write(self.base / 'operator/env/PKI_ACCOUNT_MANAGER_API_IMAGE', image)
        self.kube(['-n', AM_NS, 'rollout', 'status', 'deployment/account-manager', '--timeout=360s'], timeout=370)
        self.forward('am', AM_NS, 'account-manager', 80)
        self.verify_identity()
        self.device_baseline()
        self.check('management_adopted', {'bootstrap_removal': 'next phase', 'managed_key_exported': False})

    def resume_adopt(self):
        prepared = self.preparation()
        old = Path(self.args.adoption)
        prior = m.read(old / 'report.json')
        m.require(prior['status'] == 'failed' and prior['phase'] == 'adopt', 'failed adoption evidence required')
        self.hierarchy()
        filename = 'after-' + AM_NS + '-account-manager-deployment.json'
        saved = m.read(old / filename)
        current = self.obj('deployment', 'account-manager', AM_NS)
        ref = m.read(prepared / ('created-' + PVC + '.json'))
        m.require(current['spec']['template'] == saved['spec']['template'] and current['spec']['strategy']['type'] == 'Recreate'
                  and self.obj('persistentvolumeclaim', PVC, AM_NS)['metadata']['uid'] == ref['metadata']['uid'], 'managed owner changed during recovery')
        self.report['reconciled_from'] = str(old)
        self.save(filename, saved)
        self.save('worker-images.json', m.read(old / 'worker-images.json'))
        self.verify_identity()
        self.device_baseline()
        self.check('management_adopted', {'bootstrap_removal': 'next phase', 'managed_key_exported': False,
                                        'recovery_mutations': False})

    def state_digest(self, name='account-manager'):
        raw = self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--', 'sh', '-c',
                         'stat -c %a /var/lib/account-pki/private; stat -c %a ' + STATE + '; sha256sum ' + STATE])
        return h.private_state_digest(raw)

    def verify_identity(self):
        row = self.issuance()
        m.require(row['issuer_id'] == m.read(Path(self.args.intermediate) / 'intermediate-ready.json')['issuer_id'], 'unexpected managed issuer')
        self.save('managed-issuance.json', row)
        self.save('managed-state.json', {'sha256': self.state_digest()})
        for role in ('requester', 'approver', 'custodian'):
            self.api('/issuers/search', {'limit': 1}, role=role)
        self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'app', '--', 'sh', '-c',
                   'test -S ' + SOCKET + ' && test ! -e /var/lib/account-pki/private/identity.json && test ! -e /run/pki-auth/account-manager.key'])
        self.kube(['-n', AM_NS, 'exec', 'deployment/account-manager', '-c', 'pkimanagement', '--',
                   'test', '!', '-e', '/run/pki-auth/jwt-access.key'])
        self.check('managed_human_api', {'service_subject': row['subject'], 'fingerprint': row['fingerprint'], 'human_roles': 3,
                                       'api_mounts_no_managed_or_static_controller_key': True, 'owner_mounts_no_human_assertion_key': True})

    def verify_bootstrap_denied(self):
        source = self.preparation()
        response = json.loads(m.command([self.probe, 'tls-denial', self.output / 'root.pem', source / 'bootstrap.crt',
                                         source / 'bootstrap.key', 'certissuer.' + NS + '.svc', self.ports['issuer'][0],
                                         '/v1/certificates/service-client/issue']))
        m.require(response.get('status') == 0 and response.get('remote_certificate_rejected') is True,
                  'removed bootstrap certificate was not explicitly rejected')
        self.check('bootstrap_tls_denied', {'remote_certificate_rejected': True})

    def seal(self):
        source = self.preparation()
        adopted = Path(self.args.adoption)
        m.require(m.read(adopted / 'report.json')['status'] == 'passed', 'successful adoption required')
        self.hierarchy()
        self.verify_identity()
        before_hash = m.read(adopted / 'managed-state.json')['sha256']
        m.require(self.state_digest() == before_hash and self.issuance() == m.read(adopted / 'managed-issuance.json'), 'managed identity changed; reconcile')
        current = self.obj('deployment', 'account-manager', AM_NS)
        expected = m.read(adopted / ('after-' + AM_NS + '-account-manager-deployment.json'))
        m.require(current['spec']['template'] == expected['spec']['template'], 'managed deployment changed')
        template = json.loads(json.dumps(current['spec']['template']))
        pod = template['spec']; owner = next(c for c in pod['containers'] if c['name'] == 'pkimanagement')
        owner['env'] = [e for e in owner['env'] if not e['name'].startswith('PKI_MANAGEMENT_BOOTSTRAP_')]
        owner['volumeMounts'] = [v for v in owner['volumeMounts'] if v['name'] != 'management-bootstrap']
        pod['volumes'] = [v for v in pod['volumes'] if v['name'] != 'management-bootstrap']
        self.scoped_patch('deployment', current, [{'op': 'replace', 'path': '/spec/template', 'value': template}])
        self.kube(['-n', AM_NS, 'rollout', 'status', 'deployment/account-manager', '--timeout=300s'], timeout=310)
        self.forward('am', AM_NS, 'account-manager', 80)
        self.verify_identity()
        m.require(self.state_digest() == before_hash, 'managed state changed on bootstrap-free restart')
        secret = self.obj('secret', 'certissuer-runtime')
        trusted = base64.b64decode(secret['data']['client-ca.crt']).decode()
        ca = (source / 'bootstrap-ca.crt').read_text()
        m.require(trusted.count(ca.strip()) == 1, 'temporary issuer trust changed')
        trusted = trusted.replace(ca.strip(), '').strip() + '\n'
        self.scoped_patch('secret', secret, [{'op': 'replace', 'path': '/data/client-ca.crt', 'value': base64.b64encode(trusted.encode()).decode()}])
        self.rollout('certissuer', {'CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN': m.read(source / 'previous-provisioner.json')['pattern']})
        self.verify_bootstrap_denied()
        self.remove_bootstrap_secret(source)
        self.verify_identity()
        self.device_baseline()
        self.check('management_bootstrap_removed', {'bootstrap_free_restart': True, 'state_unchanged': True, 'temporary_trust_and_secret_removed': True})

    def remove_bootstrap_secret(self, source):
        original = m.read(source / ('created-' + BOOTSTRAP + '.json'))
        live = self.obj('secret', BOOTSTRAP, AM_NS)
        m.require(live['metadata']['uid'] == original['metadata']['uid'], 'bootstrap Secret owner changed')
        options = {'apiVersion': 'v1', 'kind': 'DeleteOptions', 'preconditions': {k: live['metadata'][k] for k in ('uid', 'resourceVersion')}}
        self.kube(['delete', '--raw', '/api/v1/namespaces/' + AM_NS + '/secrets/' + BOOTSTRAP, '-f', '-'], json.dumps(options))
        (self.base / 'pki/controller-bootstrap/rollout' / (BOOTSTRAP + '-secret.json')).unlink()

    def resume_seal(self):
        source = self.preparation()
        adopted, old = Path(self.args.adoption), Path(self.args.sealing)
        prior = m.read(old / 'report.json')
        m.require(prior['status'] == 'failed' and prior['phase'] == 'seal', 'failed sealing evidence required')
        self.hierarchy()
        saved = m.read(old / ('after-' + AM_NS + '-account-manager-deployment.json'))
        current = self.obj('deployment', 'account-manager', AM_NS)
        ref = m.read(source / ('created-' + PVC + '.json'))
        m.require(current['spec']['template'] == saved['spec']['template']
                  and self.obj('persistentvolumeclaim', PVC, AM_NS)['metadata']['uid'] == ref['metadata']['uid'], 'managed owner changed after sealing failure')
        issuer = self.obj('deployment', 'certissuer')['spec']['template']['spec']['containers'][0]
        pattern = next(e['value'] for e in issuer['env'] if e['name'] == 'CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN')
        ca = (source / 'bootstrap-ca.crt').read_text().strip()
        trust = base64.b64decode(self.obj('secret', 'certissuer-runtime')['data']['client-ca.crt']).decode()
        m.require(pattern == m.read(source / 'previous-provisioner.json')['pattern'] and ca not in trust,
                  'temporary issuer trust not yet removed; reconcile')
        state = m.read(adopted / 'managed-state.json')['sha256']
        m.require(self.state_digest() == state and self.issuance() == m.read(adopted / 'managed-issuance.json'), 'managed identity changed')
        self.report['reconciled_from'] = str(old)
        self.rollout('account-manager', ns=AM_NS)
        self.verify_identity()
        m.require(self.state_digest() == state, 'managed state changed across bootstrap-free restart')
        self.verify_bootstrap_denied()
        self.remove_bootstrap_secret(source)
        self.device_baseline()
        self.check('management_bootstrap_removed', {'bootstrap_free_restart': True, 'state_unchanged': True,
                                                   'temporary_trust_and_secret_removed': True})

    def verify(self):
        self.preparation()
        _, issuer = self.hierarchy()
        self.verify_server_signer(issuer, service_client=True)
        self.verify_bootstrap_denied()
        self.verify_identity()
        self.verify_database_role()
        adopted = Path(self.args.adoption)
        m.require(m.read(adopted / 'report.json')['status'] == 'passed', 'successful adoption evidence required')
        m.require(self.worker_images() == m.read(adopted / 'worker-images.json'), 'Account Manager worker images changed')
        current = self.obj('deployment', 'account-manager', AM_NS)
        saved = render_management(self.base)
        image = (self.base / 'operator/env/PKI_ACCOUNT_MANAGER_API_IMAGE').read_text().strip()
        m.require(current['spec']['template'] == saved['spec']['template'] and current['spec']['strategy']['type'] == 'Recreate'
                  and current['spec']['template']['spec']['containers'][0]['image'] == image, 'persisted Account Manager differs from runtime')
        owner = next(c for c in current['spec']['template']['spec']['containers'] if c['name'] == 'pkimanagement')
        m.require(not any(e['name'].startswith('PKI_MANAGEMENT_BOOTSTRAP_') for e in owner['env']), 'bootstrap still configured')
        ref = m.read(Path(self.args.preparation) / ('created-' + PVC + '.json'))
        m.require(self.obj('persistentvolumeclaim', PVC, AM_NS)['metadata']['uid'] == ref['metadata']['uid'], 'identity PVC replaced')
        self.device_baseline()
        self.check('management_final_audit', {'private_identity_persistent': True, 'bootstrap_absent': True,
                    'natural_renewal_revocation_and_crl_receipts': 'not yet qualified'})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--phase', choices=['prepare', 'adopt', 'resume-adopt', 'seal', 'resume-seal', 'verify'], required=True)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    for arg in ('authority', 'intermediate', 'output'):
        parser.add_argument('--' + arg, required=True)
    for arg in ('preparation', 'adoption', 'sealing', 'image'):
        parser.add_argument('--' + arg)
    args = parser.parse_args()
    m.require(args.phase == 'prepare' or args.preparation, 'management preparation evidence required')
    m.require(args.phase not in ('resume-adopt', 'seal', 'resume-seal', 'verify') or args.adoption, 'adoption evidence required')
    m.require(args.phase != 'resume-seal' or args.sealing, 'failed sealing evidence required')
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = ManagementRun(args)
    try:
        runner.preflight()
        {'prepare': runner.prepare_management, 'adopt': runner.adopt, 'resume-adopt': runner.resume_adopt, 'seal': runner.seal, 'resume-seal': runner.resume_seal, 'verify': runner.verify}[args.phase]()
        runner.report['status'] = 'passed'
    except Exception as error:
        runner.report['status'] = 'failed'; runner.report['failure'] = str(error)
        raise
    finally:
        runner.save('report.json', runner.report); runner.close(); os.close(fd)
        print(json.dumps({'phase': args.phase, 'status': runner.report['status'], 'report': str(runner.output / 'report.json')}), flush=True)


if __name__ == '__main__':
    def interrupted(*_):
        raise RuntimeError('interrupted; reconcile saved state before replay')
    signal.signal(signal.SIGTERM, interrupted); signal.signal(signal.SIGINT, interrupted)
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr); sys.exit(1)
