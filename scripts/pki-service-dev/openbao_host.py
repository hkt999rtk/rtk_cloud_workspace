#!/usr/bin/env python3
"""Adopt governed OpenBao TLS in dev, one resumable phase at a time."""
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
import sys
import time
import urllib.parse
import uuid


spec = importlib.util.spec_from_file_location(
    'service_hierarchy', Path(__file__).with_name('run.py'))
h = importlib.util.module_from_spec(spec)
spec.loader.exec_module(h)
m, NS = h.m, h.NS
SECRETS_NS = 'video-cloud-dev-secrets'
PLATFORM_NS = 'video-cloud-dev-platform'

CONSUMERS = ['certissuer', 'pki-controller']
OPENBAO_HOST_NAMES = [
    'openbao.video-cloud-dev-secrets.svc',
    'openbao.video-cloud-dev-secrets.svc.cluster.local']
IMAGE_PATTERN = re.compile(
    r'ghcr\.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:[0-9a-f]{64}')
OPENBAO_IMAGE_PATTERN = re.compile(
    r'ghcr\.io/hkt999rtk/rtk_cloud_dev/openbao-pki@sha256:[0-9a-f]{64}')
OPENBAO_STATE_PVC = 'openbao-pki-identity'
OPENBAO_SERVICE_ROOT = 'openbao-pki-service-root'
OPENBAO_REGISTRY_SECRET = 'openbao-pki-registry'
OPENBAO_BOOTSTRAP_SECRET = 'openbao-pki-bootstrap'
OPENBAO_SEED_POD = 'openbao-pki-bootstrap'
OPENBAO_CLIENT_STATE = '/var/lib/openbao-pki/service/client.json'
OPENBAO_HOST_STATE = '/var/lib/openbao-pki/host/server.json'
OPENBAO_RUNTIME = '/run/openbao-pki/private'


def registry_network_policy():
    return {
        'apiVersion': 'networking.k8s.io/v1',
        'kind': 'NetworkPolicy',
        'metadata': {'name': 'allow-openbao-pki-registry',
                     'namespace': PLATFORM_NS},
        'spec': {
            'podSelector': {'matchLabels': {
                'app.kubernetes.io/name': 'postgresql'}},
            'policyTypes': ['Ingress'],
            'ingress': [{'from': [{
                'namespaceSelector': {'matchLabels': {
                    'kubernetes.io/metadata.name': SECRETS_NS}},
                'podSelector': {'matchLabels': {
                    'app.kubernetes.io/name': 'openbao'}}}],
                'ports': [{'port': 5432, 'protocol': 'TCP'}]}]}}


def managed_openbao_config(data):
    key = 'extraconfig-from-values.hcl'
    source = data.get(key, '')
    cert = '/openbao/tls/tls.crt'
    private_key = '/openbao/tls/tls.key'
    m.require(source.count(cert) == 1 and source.count(private_key) == 1,
              'legacy OpenBao listener paths changed')
    return {key: source.replace(
        cert, OPENBAO_RUNTIME + '/current/chain.pem').replace(
        private_key, OPENBAO_RUNTIME + '/current/key.pem')}


def managed_openbao_template(owner, image, host_root, service_root):
    template = json.loads(json.dumps(owner['spec']['template']))
    pod = template['spec']
    m.require(owner['spec']['replicas'] == 1
              and owner['spec']['updateStrategy']['type'] == 'OnDelete'
              and len(pod.get('containers', [])) == 1
              and not pod.get('initContainers')
              and not pod.get('shareProcessNamespace'),
              'legacy OpenBao StatefulSet topology changed')
    main = pod['containers'][0]
    m.require(main['name'] == 'openbao'
              and main['image'] == 'quay.io/openbao/openbao:2.5.4'
              and main['command'] == ['/bin/sh', '-ec']
              and len(main['args']) == 1,
              'legacy OpenBao container changed')
    start = '/usr/local/bin/docker-entrypoint.sh bao server -config=/tmp/storageconfig.hcl \n'
    m.require(main['args'][0].endswith(start),
              'legacy OpenBao start command changed')
    main['args'][0] = main['args'][0][:-len(start)] + (
        '/usr/local/bin/docker-entrypoint.sh bao server '
        '-config=/tmp/storageconfig.hcl &\n'
        'openbao_pid=$!\n'
        "printf '%s\\n' \"$openbao_pid\" > " +
        OPENBAO_RUNTIME + '/openbao.pid\n'
        'wait "$openbao_pid"\n')
    main['image'] = image
    main['readinessProbe']['exec']['command'][-1] = (
        '[ -f ' + OPENBAO_RUNTIME + '/ready ] && '
        'bao status -tls-skip-verify')
    main['volumeMounts'] = [
        item for item in main['volumeMounts'] if item['name'] != 'openbao-tls']
    main['volumeMounts'].append({
        'name': 'pki-runtime', 'mountPath': '/run/openbao-pki'})
    issuer_name = 'certissuer.' + NS + '.svc'
    settings = {
        'PKI_ENVIRONMENT': 'dev',
        'OPENBAO_PKI_RUNTIME_DIR': OPENBAO_RUNTIME,
        'OPENBAO_PKI_PROCESS_PID_FILE':
            OPENBAO_RUNTIME + '/openbao.pid',
        'OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_STATE': OPENBAO_CLIENT_STATE,
        'OPENBAO_PKI_SERVICE_CLIENT_ROOT_SHA256':
            service_root['certificate_fingerprint_sha256'],
        'OPENBAO_PKI_HOST_IDENTITY_STATE': OPENBAO_HOST_STATE,
        'OPENBAO_PKI_HOST_NAME': OPENBAO_HOST_NAMES[0],
        'OPENBAO_PKI_HOST_DNS_NAMES': ','.join(OPENBAO_HOST_NAMES),
        'OPENBAO_PKI_HOST_ROOT_SHA256':
            host_root['certificate_fingerprint_sha256'],
        'OPENBAO_PKI_HOST_RENEWAL_URL':
            'https://' + issuer_name + ':9443',
        'OPENBAO_PKI_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256':
            service_root['certificate_fingerprint_sha256'],
        'OPENBAO_PKI_HOST_RENEWAL_SERVER_PKI_NAME': issuer_name,
        'OPENBAO_PKI_HOST_RENEWAL_TLS_CA': '/run/service-root/root.pem',
        'OPENBAO_PKI_HOST_RENEWAL_SERVER_PKI_SWEEP_INTERVAL': '10s'}
    env = [{'name': name, 'value': value}
           for name, value in settings.items()]
    env.append({'name': 'PKI_DATABASE_URL', 'valueFrom': {
        'secretKeyRef': {'name': OPENBAO_REGISTRY_SECRET, 'key': 'url'}}})
    mounts = [
        {'name': 'pki-state', 'mountPath': '/var/lib/openbao-pki'},
        {'name': 'pki-runtime', 'mountPath': '/run/openbao-pki'},
        {'name': 'service-root', 'mountPath': '/run/service-root',
         'readOnly': True}]
    security = {
        'allowPrivilegeEscalation': False,
        'capabilities': {'drop': ['ALL']},
        'readOnlyRootFilesystem': True,
        'runAsNonRoot': True}
    worker = {
        'name': 'openbao-pki', 'image': image,
        'imagePullPolicy': 'IfNotPresent', 'env': env,
        'volumeMounts': mounts, 'securityContext': security}
    init = json.loads(json.dumps(worker))
    init['name'] = 'openbao-pki-install'
    init['command'] = ['/bin/sh', '-ec']
    init['args'] = [
        'chmod -R go-rwx /var/lib/openbao-pki/service '
        '/var/lib/openbao-pki/host; '
        'exec /usr/local/bin/openbaopkihost install']
    worker['command'] = ['/usr/local/bin/openbaopkihost']
    pod['initContainers'] = [init]
    pod['containers'].append(worker)
    pod['shareProcessNamespace'] = True
    pod.setdefault('securityContext', {})[
        'fsGroupChangePolicy'] = 'OnRootMismatch'
    m.require(not pod.get('imagePullSecrets'),
              'legacy OpenBao image pull settings changed')
    pod['imagePullSecrets'] = [{'name': 'ghcr-pull'}]
    pod['volumes'] = [
        item for item in pod['volumes'] if item['name'] != 'openbao-tls']
    pod['volumes'].extend([
        {'name': 'pki-state', 'persistentVolumeClaim': {
            'claimName': OPENBAO_STATE_PVC}},
        {'name': 'pki-runtime', 'emptyDir': {}},
        {'name': 'service-root', 'configMap': {
            'name': OPENBAO_SERVICE_ROOT}}])
    template.setdefault('metadata', {}).setdefault('annotations', {})[
        'rtk.cloud/openbao-managed-transport'] = image.split('@')[-1]
    return template


def certissuer_route_template(owner, image, root):
    template = json.loads(json.dumps(owner['spec']['template']))
    containers = template['spec']['containers']
    m.require(len(containers) == 1
              and containers[0]['name'] == 'certissuer',
              'certissuer container changed')
    container = containers[0]
    values = {item['name']: item.get('value')
              for item in container.get('env', [])}
    m.require(not any(values.get(name) for name in (
        'CERT_ISSUER_OPENBAO_HOST_PKI_ROOT_SHA256',
        'CERT_ISSUER_OPENBAO_HOST_DNS_NAMES',
        'CERT_ISSUER_OPENBAO_HOST_CLIENT_CN_PATTERN')),
        'OpenBao host issuance route already configured; reconcile')
    container['image'] = image
    container['env'] = h.with_env(container.get('env', []), {
        'CERT_ISSUER_OPENBAO_HOST_PKI_ROOT_SHA256':
            root['certificate_fingerprint_sha256'],
        'CERT_ISSUER_OPENBAO_HOST_DNS_NAMES':
            ','.join(OPENBAO_HOST_NAMES),
        'CERT_ISSUER_OPENBAO_HOST_CLIENT_CN_PATTERN':
            '^service:openbao$'})
    return template


def provider_crl_manifest(authorities):
    return [{'issuer': issuer,
             'state_path': '/var/lib/pki-host/identity/openbao-tls-crl-' +
                           issuer['issuer_id'] + '.json'}
            for issuer in authorities]


def provider_verification_template(owner, root, manifest_name, image,
                                   run_name):
    template = json.loads(json.dumps(owner['spec']['template']))
    containers = template['spec']['containers']
    m.require(len(containers) == 1
              and containers[0]['name'] == owner['metadata']['name'],
              'provider client container changed')
    container = containers[0]
    m.require(IMAGE_PATTERN.fullmatch(image or ''),
              'verified dev application image digest required')
    env = {item['name']: item.get('value')
           for item in container.get('env', [])}
    address = urllib.parse.urlparse(env.get('OPENBAO_ADDR', ''))
    m.require(address.scheme == 'https'
              and address.hostname in OPENBAO_HOST_NAMES
              and address.port == 8200
              and env.get('OPENBAO_CACERT') == '/run/openbao-ca/ca.crt'
              and env.get('OPENBAO_SERVER_BUNDLE_MANIFEST') ==
              '/run/openbao-server-bundles/issuers.json',
              'OpenBao provider origin or installed bundle changed')
    volumes = {item['name']: item
               for item in template['spec'].get('volumes', [])}
    mounts = {item['name']: item
              for item in container.get('volumeMounts', [])}
    m.require(volumes.get('host-state', {}).get(
                  'persistentVolumeClaim', {}).get('claimName')
              and mounts.get('host-state', {}).get('mountPath') ==
              '/var/lib/pki-host'
              and 'openbao-server-crls' not in volumes
              and 'openbao-server-crls' not in mounts,
              'provider retained state or CRL manifest mount changed')
    template['spec']['volumes'].append({
        'name': 'openbao-server-crls',
        'configMap': {'name': manifest_name}})
    container.setdefault('volumeMounts', []).append({
        'name': 'openbao-server-crls',
        'mountPath': '/run/openbao-server-crls', 'readOnly': True})
    settings = {
        'OPENBAO_SERVER_PKI_ROOT_SHA256':
            root['certificate_fingerprint_sha256'],
        'OPENBAO_SERVER_PKI_NAME': address.hostname,
        'OPENBAO_SERVER_PKI_SWEEP_INTERVAL': '10s',
        'OPENBAO_SERVER_CRL_MANIFEST':
            '/run/openbao-server-crls/crls.json',
        'OPENBAO_PKI_CONTROLLER_URL':
            'https://pki-controller.' + NS + '.svc:18446',
        'OPENBAO_MANAGEMENT_CA': '/run/pki-host-root/root.pem'}
    for key, value in settings.items():
        m.require(env.get(key) in (None, '', value),
                  'OpenBao provider verification setting changed: ' + key)
    m.require(not env.get('OPENBAO_MANAGEMENT_CERT')
              and not env.get('OPENBAO_MANAGEMENT_KEY'),
              'OpenBao provider verification retained static credentials')
    container['env'] = h.with_env(container.get('env', []), settings)
    container['image'] = image
    template.setdefault('metadata', {}).setdefault('annotations', {})[
        'rtk.cloud/openbao-provider-verification'] = run_name
    return template


class OpenBaoHostRun(h.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.probe_pods = {}
        self.report['foundation_scope'] = (
            'Dev-only OpenBao TLS host and actual provider clients')
        self.report['openbao_host_runner_sha256'] = m.digest(
            Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def openbao_root(self, status):
        source = Path(self.args.authority)
        saved = m.read(source / 'root-ready.json')
        root = self.api('/issuers/' + saved['issuer_id'])
        m.require(root['environment'] == 'dev'
                  and root['trust_domain'] == 'openbao_tls'
                  and root['kind'] == 'root'
                  and root['status'] == status
                  and root['certificate_fingerprint_sha256'] ==
                  saved['certificate_fingerprint_sha256']
                  and root['trust_bundle_version'] ==
                  saved['trust_bundle_version'],
                  'saved OpenBao TLS Root changed')
        self.save('openbao-tls-root.json', root)
        return root

    def scoped_patch(self, name, before, template):
        self.save('before-' + name + '-deployment.json', before)
        patches = [
            {'op': 'test', 'path': '/metadata/resourceVersion',
             'value': before['metadata']['resourceVersion']},
            {'op': 'replace', 'path': '/spec/template', 'value': template}]
        changed = json.loads(self.kube([
            '-n', NS, 'patch', 'deployment', name, '--type=json',
            '--patch-file=/dev/stdin', '-o', 'json'], json.dumps(patches)))
        desired = {'apiVersion': changed['apiVersion'], 'kind': changed['kind'],
                   'metadata': {key: changed['metadata'][key]
                                for key in ('name', 'namespace', 'labels',
                                            'annotations')
                                if key in changed['metadata']},
                   'spec': changed['spec']}
        self.save('after-' + name + '-deployment.json', desired)
        m.write(self.base / 'pki/controller-bootstrap/rollout' /
                (name + '-deployment.json'), desired)

    def staged_client_template(self, owner, image, ca_configmap,
                               manifest_configmap, root):
        template = json.loads(json.dumps(owner['spec']['template']))
        pod = template['spec']
        m.require(len(pod['containers']) == 1,
                  'provider Deployment topology changed')
        container = pod['containers'][0]
        m.require(container['name'] == owner['metadata']['name'],
                  'provider container name changed')
        volumes = {item['name']: item for item in pod.get('volumes', [])}
        m.require(volumes.get('openbao-ca', {}).get('configMap', {}).get(
                  'name') == 'pki-openbao-transport-ca'
                  and 'openbao-server-bundles' not in volumes,
                  'OpenBao staged trust source changed; reconcile')
        mounts = {item['name']: item
                  for item in container.get('volumeMounts', [])}
        m.require(mounts.get('openbao-ca', {}).get('mountPath') ==
                  '/run/openbao-ca'
                  and any(item['name'] == 'host-root'
                          and item['mountPath'] == '/run/pki-host-root'
                          for item in container.get('volumeMounts', [])),
                  'provider trust mounts changed')
        volumes['openbao-ca']['configMap']['name'] = ca_configmap
        pod['volumes'].append({
            'name': 'openbao-server-bundles',
            'configMap': {'name': manifest_configmap}})
        container.setdefault('volumeMounts', []).append({
            'name': 'openbao-server-bundles',
            'mountPath': '/run/openbao-server-bundles', 'readOnly': True})
        container['image'] = image
        container['env'] = h.with_env(container.get('env', []), {
            'OPENBAO_SERVER_BUNDLE_MANIFEST':
                '/run/openbao-server-bundles/issuers.json',
            'OPENBAO_SERVER_BUNDLE_ROOT_SHA256':
                root['certificate_fingerprint_sha256'],
            'OPENBAO_SERVER_BUNDLE_PKI_CONTROLLER_URL':
                'https://pki-controller.' + NS + '.svc:18446',
            'OPENBAO_SERVER_BUNDLE_MANAGEMENT_CA':
                '/run/pki-host-root/root.pem'})
        if owner['metadata']['name'] == 'pki-controller':
            container['env'] = h.with_env(container['env'], {
                'PKI_REQUIRED_CONSUMERS_OPENBAO_TLS':
                    ','.join(CONSUMERS),
                'PKI_REQUIRED_BUNDLE_CONSUMERS_OPENBAO_TLS':
                    ','.join(CONSUMERS)})
        template.setdefault('metadata', {}).setdefault('annotations', {})[
            'rtk.cloud/openbao-root-staging'] = self.output.name
        return template

    def recovery_client_template(self, owner, image, ca_configmap,
                                 manifest_configmap, root):
        pod = owner['spec']['template']['spec']
        volumes = {item['name']: item for item in pod.get('volumes', [])}
        source = volumes.get('openbao-ca', {}).get('configMap', {}).get('name')
        if source == 'pki-openbao-transport-ca':
            return self.staged_client_template(
                owner, image, ca_configmap, manifest_configmap, root)
        m.require(source == ca_configmap,
                  'OpenBao recovery trust source changed')
        template = json.loads(json.dumps(owner['spec']['template']))
        template['spec']['containers'][0]['image'] = image
        if owner['metadata']['name'] == 'pki-controller':
            container = template['spec']['containers'][0]
            container['env'] = h.with_env(container.get('env', []), {
                'PKI_REQUIRED_CONSUMERS_OPENBAO_TLS':
                    ','.join(CONSUMERS),
                'PKI_REQUIRED_BUNDLE_CONSUMERS_OPENBAO_TLS':
                    ','.join(CONSUMERS)})
        candidate = {'metadata': owner['metadata'],
                     'spec': {'template': template}}
        self.verify_staged_client(candidate, image, ca_configmap,
                                  manifest_configmap, root)
        template.setdefault('metadata', {}).setdefault('annotations', {})[
            'rtk.cloud/openbao-root-staging'] = self.output.name
        return template

    def verify_staged_client(self, owner, image, ca_configmap,
                             manifest_configmap, root):
        pod = owner['spec']['template']['spec']
        m.require(len(pod['containers']) == 1,
                  'provider Deployment topology changed')
        container = pod['containers'][0]
        m.require(container['name'] == owner['metadata']['name']
                  and container['image'] == image,
                  'installed provider image changed')
        volumes = {item['name']: item for item in pod.get('volumes', [])}
        m.require(volumes.get('openbao-ca', {}).get('configMap', {}).get(
                  'name') == ca_configmap
                  and volumes.get('openbao-server-bundles', {}).get(
                      'configMap', {}).get('name') == manifest_configmap,
                  'installed OpenBao trust sources changed')
        mounts = {item['name']: item
                  for item in container.get('volumeMounts', [])}
        m.require(mounts.get('openbao-ca', {}).get('mountPath') ==
                  '/run/openbao-ca'
                  and mounts.get('openbao-server-bundles', {}).get(
                      'mountPath') == '/run/openbao-server-bundles'
                  and mounts.get('host-root', {}).get('mountPath') ==
                  '/run/pki-host-root',
                  'installed OpenBao trust mounts changed')
        env = {item['name']: item.get('value')
               for item in container.get('env', [])}
        expected = {
            'OPENBAO_SERVER_BUNDLE_MANIFEST':
                '/run/openbao-server-bundles/issuers.json',
            'OPENBAO_SERVER_BUNDLE_ROOT_SHA256':
                root['certificate_fingerprint_sha256'],
            'OPENBAO_SERVER_BUNDLE_PKI_CONTROLLER_URL':
                'https://pki-controller.' + NS + '.svc:18446',
            'OPENBAO_SERVER_BUNDLE_MANAGEMENT_CA':
                '/run/pki-host-root/root.pem'}
        m.require(all(env.get(key) == value for key, value in expected.items()),
                  'installed OpenBao bundle settings changed')
        if owner['metadata']['name'] == 'pki-controller':
            m.require(
                env.get('PKI_REQUIRED_CONSUMERS_OPENBAO_TLS') ==
                ','.join(CONSUMERS)
                and env.get(
                    'PKI_REQUIRED_BUNDLE_CONSUMERS_OPENBAO_TLS') ==
                ','.join(CONSUMERS), 'OpenBao consumer gates changed')

    def root_configmaps(self, root):
        legacy = self.obj('configmap', 'pki-openbao-transport-ca')
        m.require(set(legacy.get('data', {})) == {'ca.crt'},
                  'legacy OpenBao public CA source changed')
        suffix = root['issuer_id'][:8]
        ca_name = 'pki-openbao-transport-ca-' + suffix
        manifest_name = 'pki-openbao-tls-bundles-' + suffix
        refs = [{'issuer_id': root['issuer_id'],
                 'trust_bundle_version': root['trust_bundle_version']}]
        return ca_name, manifest_name, {
            ca_name: {'ca.crt': h.append_pem(
                legacy['data']['ca.crt'], root['certificate_pem'])},
            manifest_name: {'issuers.json': json.dumps(refs)}}

    def ensure_root_configmaps(self, root):
        ca_name, manifest_name, expected = self.root_configmaps(root)
        for name in (ca_name, manifest_name):
            raw = self.kube(['-n', NS, 'get', 'configmap', name,
                             '--ignore-not-found', '-o', 'json'])
            if not raw.strip():
                self.create({'apiVersion': 'v1', 'kind': 'ConfigMap',
                             'metadata': {'name': name, 'namespace': NS},
                             'immutable': True, 'data': expected[name]})
                continue
            current = json.loads(raw)
            m.require(current.get('immutable') is True
                      and current.get('data') == expected[name],
                      'installed OpenBao Root ConfigMap changed: ' + name)
        return ca_name, manifest_name

    def require_activation_blocked(self, operation):
        # The operation requester owns activation. Approval accounts only supply
        # the two distinct approvals recorded during authority preparation.
        self.api('/operations/' + operation['operation_id'] + '/activate', {},
                 409)

    def require_activation_policy_absent(self, operation):
        self.api('/operations/' + operation['operation_id'] + '/activate', {},
                 403)

    def install_root_consumers(self):
        root = self.openbao_root('ready')
        m.require(IMAGE_PATTERN.fullmatch(self.args.image or ''),
                  'verified dev application image digest required')
        operation = m.read(Path(self.args.authority) / 'root-operation.json')
        self.require_activation_policy_absent(operation)
        ca_configmap, manifest_configmap = self.ensure_root_configmaps(root)
        for name in reversed(CONSUMERS):
            owner = self.obj('deployment', name)
            template = self.staged_client_template(
                owner, self.args.image, ca_configmap,
                manifest_configmap, root)
            self.scoped_patch(name, owner, template)
            self.kube(['-n', NS, 'rollout', 'status',
                       'deployment/' + name, '--timeout=300s'], timeout=310)
            if name == 'pki-controller':
                self.require_activation_blocked(operation)
        receipts = self.wait_receipts(root['issuer_id'],
                                     root['trust_bundle_version'], CONSUMERS)
        self.check('openbao_tls_root_installed_by_actual_clients', {
            'issuer_id': root['issuer_id'], 'consumers': receipts,
            'activation_without_policy_denied': True,
            'activation_without_receipts_denied': True,
            'ca_configmap': ca_configmap,
            'manifest_configmap': manifest_configmap,
            'existing_listener_preserved': True,
            'image': self.args.image})

    def finish_root_consumers(self):
        failed = m.read(Path(self.args.failed) / 'report.json')
        m.require(failed['status'] == 'failed'
                  and failed['phase'] == 'install-root-consumers',
                  'failed OpenBao Root consumer phase required')
        root = m.read(Path(self.args.authority) / 'root-ready.json')
        m.require(root['environment'] == 'dev'
                  and root['trust_domain'] == 'openbao_tls'
                  and root['kind'] == 'root'
                  and root['status'] == 'ready',
                  'saved OpenBao TLS Root is not ready')
        m.require(IMAGE_PATTERN.fullmatch(self.args.image or ''),
                  'verified dev application image digest required')
        operation = m.read(Path(self.args.authority) / 'root-operation.json')
        ca_configmap, manifest_configmap = self.ensure_root_configmaps(root)
        for name in reversed(CONSUMERS):
            owner = self.obj('deployment', name)
            template = self.recovery_client_template(
                owner, self.args.image, ca_configmap,
                manifest_configmap, root)
            if template != owner['spec']['template']:
                self.scoped_patch(name, owner, template)
            self.kube(['-n', NS, 'rollout', 'status',
                       'deployment/' + name, '--timeout=300s'], timeout=310)
            self.verify_staged_client(self.obj('deployment', name),
                                      self.args.image, ca_configmap,
                                      manifest_configmap, root)
            if name == 'pki-controller':
                root = self.openbao_root('ready')
                self.require_activation_blocked(operation)
        receipts = self.wait_receipts(root['issuer_id'],
                                     root['trust_bundle_version'], CONSUMERS)
        self.check('openbao_tls_root_consumer_recovery', {
            'issuer_id': root['issuer_id'], 'consumers': receipts,
            'reconciled_from': str(Path(self.args.failed)),
            'activation_without_receipts_denied': True,
            'existing_listener_preserved': True,
            'image': self.args.image})

    def recovery_preflight(self):
        m.require(self.kube(['config', 'current-context']).strip() ==
                  self.context, 'canonical dev context mismatch')
        namespace = self.obj('namespace', NS)
        m.require(namespace['metadata']['name'] == NS, 'wrong namespace')
        account = self.obj('deployment', 'account-manager',
                           'video-cloud-dev-account-manager')
        m.require(account.get('status', {}).get('readyReplicas', 0) ==
                  account['spec']['replicas'] > 0,
                  'Account Manager unavailable during recovery')
        self.forward('am', 'video-cloud-dev-account-manager',
                     'account-manager', 80)
        self.accounts = m.read(self.foundation / 'accounts.json')
        identities = {name: self.login(credentials)[1]
                      for name, credentials in self.accounts.items()}
        m.require(len(set(identities.values())) == 3,
                  'approval accounts are not distinct')
        self.check('recovery_preflight', {
            'context': self.context, 'namespace': NS,
            'account_manager_ready': True,
            'controller_may_be_unavailable': True})

    def activate_root(self):
        root = self.openbao_root('ready')
        operation = m.read(Path(self.args.authority) / 'root-operation.json')
        receipts = self.wait_receipts(root['issuer_id'],
                                     root['trust_bundle_version'], CONSUMERS)
        self.api('/operations/' + operation['operation_id'] + '/activate', {},
                 204)
        root = self.api('/issuers/' + root['issuer_id'])
        m.require(root['status'] == 'active',
                  'OpenBao TLS Root did not activate')
        self.save('openbao-tls-root-active.json', root)
        now = dt.datetime.now(dt.timezone.utc).replace(microsecond=0)
        request = {
            'issuer_id': root['issuer_id'],
            'issuer_fingerprint_sha256':
                root['certificate_fingerprint_sha256'],
            'crl_number': '1', 'this_update': m.stamp(now),
            'next_update': m.stamp(now + dt.timedelta(days=1)),
            'revocations': []}
        self.save('root-crl-request.json', request)
        expected = self.ceremony_call([
            'crl-digest', self.output / 'root-crl-request.json']).strip()
        authority = Path(self.args.authority)
        self.ceremony_call([
            'crl', '--issuer', self.output / 'openbao-tls-root-active.json',
            '--crl-request', self.output / 'root-crl-request.json',
            '--expected-request-sha256', expected,
            '--key', authority / 'root-offline-simulation/ca-key.encrypted.pem',
            '--passphrase-file', m.read(
                authority / 'passphrase-reference.json')['path'],
            '--out', self.output / 'root-crl'])
        raw = (self.output / 'root-crl/revocations.pem').read_text()
        record = self.api('/issuers/' + root['issuer_id'] + '/crl', {
            'crl_pem': raw}, role='approver')
        self.save('openbao-tls-root-crl.json', record)
        self.check('openbao_tls_root_active_with_crl', {
            'issuer_id': root['issuer_id'], 'consumers': receipts,
            'crl_sha256': record['crl_sha256'],
            'existing_listener_preserved': True})

    def ready_intermediate(self, status='ready'):
        root = self.openbao_root('active')
        source = Path(self.args.intermediate)
        saved = m.read(source / 'intermediate-ready.json')
        issuer = self.api('/issuers/' + saved['issuer_id'])
        server_only = getattr(self.args, 'server_only', False)
        expected_ids = [] if server_only else ['service:openbao']
        immutable = (
            'issuer_id', 'environment', 'trust_domain', 'kind',
            'parent_issuer_id', 'issuer_version', 'signer_provider',
            'signer_reference', 'certificate_fingerprint_sha256',
            'certificate_pem', 'trust_bundle_version',
            'service_client_ids', 'server_dns_names')
        m.require(all(issuer.get(key) == saved.get(key) for key in immutable)
                  and issuer['status'] == status
                  and issuer['parent_issuer_id'] == root['issuer_id']
                  and issuer.get('service_client_ids', []) == expected_ids
                  and (not server_only or issuer['issuer_version'] == 2),
                  'OpenBao TLS intermediate identity or policy changed')
        if server_only:
            predecessor = m.read(source / 'intermediate-v1.json')
            current = self.api('/issuers/' + predecessor['issuer_id'])
            expected_status = 'retiring' if status == 'active' else 'active'
            m.require(all(current.get(key) == predecessor.get(key)
                          for key in immutable)
                      and current['status'] == expected_status
                      and predecessor['issuer_version'] == 1
                      and predecessor['service_client_ids'] ==
                      ['service:openbao']
                      and predecessor['server_dns_names'] ==
                      OPENBAO_HOST_NAMES,
                      'OpenBao TLS v1 predecessor changed')
        operation = m.read(source / 'intermediate-operation.json')
        return root, issuer, operation

    def intermediate_bundle_issuers(self, root, issuer):
        items = [root]
        if getattr(self.args, 'server_only', False):
            items.append(m.read(Path(self.args.intermediate) /
                                'intermediate-v1.json'))
        items.append(issuer)
        return items

    def intermediate_template(self, owner, image, manifest_name, run_name):
        template = json.loads(json.dumps(owner['spec']['template']))
        container = template['spec']['containers'][0]
        m.require(container['name'] == owner['metadata']['name'],
                  'provider container changed')
        m.require(container['image'] == image,
                  'verified provider image changed')
        volumes = {item['name']: item for item in template['spec']['volumes']}
        m.require('openbao-server-bundles' in volumes,
                  'OpenBao manifest mount missing')
        volumes['openbao-server-bundles']['configMap']['name'] = manifest_name
        template.setdefault('metadata', {}).setdefault('annotations', {})[
            'rtk.cloud/openbao-intermediate-staging'] = run_name
        return template

    def install_intermediate_consumers(self):
        root, issuer, operation = self.ready_intermediate()
        server_only = getattr(self.args, 'server_only', False)
        if not server_only:
            m.require(IMAGE_PATTERN.fullmatch(self.args.image or ''),
                      'verified dev application image digest required')
        name = 'pki-openbao-tls-bundles-' + issuer['issuer_id'][:8]
        refs = [{'issuer_id': item['issuer_id'],
                 'trust_bundle_version': item['trust_bundle_version']}
                for item in self.intermediate_bundle_issuers(root, issuer)]
        raw = self.kube(['-n', NS, 'get', 'configmap', name,
                         '--ignore-not-found', '-o', 'json'])
        expected = {'issuers.json': json.dumps(refs)}
        if not raw.strip():
            self.create({'apiVersion': 'v1', 'kind': 'ConfigMap',
                         'metadata': {'name': name, 'namespace': NS},
                         'immutable': True, 'data': expected})
        else:
            current = json.loads(raw)
            m.require(current.get('immutable') is True
                      and current.get('data') == expected,
                      'OpenBao intermediate manifest changed')
        for consumer in reversed(CONSUMERS):
            owner = self.obj('deployment', consumer)
            image = (owner['spec']['template']['spec']['containers'][0]
                     ['image'] if server_only else self.args.image)
            m.require(IMAGE_PATTERN.fullmatch(image or ''),
                      'verified dev application image digest required')
            template = self.intermediate_template(
                owner, image, name, self.output.name)
            self.scoped_patch(consumer, owner, template)
            self.kube(['-n', NS, 'rollout', 'status',
                       'deployment/' + consumer, '--timeout=300s'], timeout=310)
            if consumer == 'pki-controller':
                self.api('/operations/' + operation['operation_id'] +
                         '/activate', {}, 409)
        receipts = self.wait_receipts(
            issuer['issuer_id'], issuer['trust_bundle_version'], CONSUMERS)
        self.check('openbao_tls_intermediate_installed_by_actual_clients', {
            'issuer_id': issuer['issuer_id'], 'consumers': receipts,
            'manifest_configmap': name,
            'missing_certissuer_activation_denied': True,
            'existing_listener_preserved': True})

    def activate_intermediate(self):
        _, issuer, operation = self.ready_intermediate()
        receipts = self.wait_receipts(
            issuer['issuer_id'], issuer['trust_bundle_version'], CONSUMERS)
        self.api('/operations/' + operation['operation_id'] + '/activate', {},
                 204)
        issuer = self.api('/issuers/' + issuer['issuer_id'])
        m.require(issuer['status'] == 'active',
                  'OpenBao TLS intermediate did not activate')
        if getattr(self.args, 'server_only', False):
            predecessor = m.read(Path(self.args.intermediate) /
                                 'intermediate-v1.json')
            old = self.api('/issuers/' + predecessor['issuer_id'])
            m.require(old['status'] == 'retiring'
                      and old['certificate_fingerprint_sha256'] ==
                      predecessor['certificate_fingerprint_sha256']
                      and old['trust_bundle_version'] ==
                      predecessor['trust_bundle_version'],
                      'OpenBao TLS v1 did not enter retiring state')
            self.save('openbao-tls-intermediate-v1-retiring.json', old)
        self.save('openbao-tls-intermediate-active.json', issuer)
        provider = json.loads(self.bao([
            'read', '-format=json', issuer['signer_reference'] + '/cert/crl']))
        record = self.api('/issuers/' + issuer['issuer_id'] + '/crl', {
            'crl_pem': provider['data']['certificate']})
        self.save('openbao-tls-intermediate-crl.json', record)
        self.check('openbao_tls_intermediate_active_with_crl', {
            'issuer_id': issuer['issuer_id'], 'consumers': receipts,
            'crl_sha256': record['crl_sha256'],
            'existing_listener_preserved': True})

    def configure_certissuer(self):
        root, issuer, _ = self.ready_intermediate('active')
        m.require(IMAGE_PATTERN.fullmatch(self.args.image or ''),
                  'verified dev application image digest required')
        policies = m.read(Path(self.args.policy_evidence) /
                          'provider-policies.json')
        m.require(policies['issuer_id'] == issuer['issuer_id']
                  and policies['mount'] == issuer['signer_reference']
                  and '/sign/server' in policies['signer_policy']
                  and (not getattr(self.args, 'server_only', False)
                       or not policies.get('service_client_signer_policy')),
                  'saved OpenBao TLS signer policy changed')
        self.role_policy(
            'certissuer-pki-dev',
            'pki-openbao-tls-server-dev-' + issuer['issuer_id'],
            policies['signer_policy'])
        if getattr(self.args, 'server_only', False):
            self.check('openbao_tls_server_only_signer_enabled', {
                'issuer_id': issuer['issuer_id'],
                'server_dns_names': issuer['server_dns_names'],
                'service_client_ids': issuer.get('service_client_ids', []),
                'existing_route_preserved': True})
            return
        owner = self.obj('deployment', 'certissuer')
        template = certissuer_route_template(
            owner, self.args.image, root)
        template.setdefault('metadata', {}).setdefault('annotations', {})[
            'rtk.cloud/openbao-host-issuer-rollout'] = self.output.name
        self.scoped_patch('certissuer', owner, template)
        self.create({
            'apiVersion': 'networking.k8s.io/v1',
            'kind': 'NetworkPolicy',
            'metadata': {'name': 'allow-openbao-host-renewal',
                         'namespace': NS},
            'spec': {
                'podSelector': {'matchLabels': {
                    'app.kubernetes.io/name': 'certissuer'}},
                'policyTypes': ['Ingress'],
                'ingress': [{'from': [{
                    'namespaceSelector': {'matchLabels': {
                        'kubernetes.io/metadata.name':
                            'video-cloud-dev-secrets'}},
                    'podSelector': {'matchLabels': {
                        'app.kubernetes.io/name': 'openbao'}}}],
                    'ports': [{'port': 9443, 'protocol': 'TCP'}]}]}})
        self.kube(['-n', NS, 'rollout', 'status',
                   'deployment/certissuer', '--timeout=300s'], timeout=310)
        settings = {
            'CERT_ISSUER_OPENBAO_HOST_PKI_ROOT_SHA256':
                root['certificate_fingerprint_sha256'],
            'CERT_ISSUER_OPENBAO_HOST_DNS_NAMES':
                ','.join(OPENBAO_HOST_NAMES),
            'CERT_ISSUER_OPENBAO_HOST_CLIENT_CN_PATTERN':
                '^service:openbao$'}
        m.write(self.base / 'operator/env/PKI_CERTISSUER_IMAGE',
                self.args.image + '\n')
        path = (self.base / 'pki/controller-bootstrap/rollout' /
                'certissuer-service-settings.json')
        m.write(path, dict(m.read(path), **settings))
        self.check('openbao_host_named_issuance_route_enabled', {
            'issuer_id': issuer['issuer_id'],
            'dns_names': OPENBAO_HOST_NAMES,
            'caller_pattern': '^service:openbao$',
            'image': self.args.image,
            'cross_namespace_ingress': 'openbao pods only'})

    def sql(self, query):
        return self.kube([
            '-n', 'video-cloud-dev-platform', 'exec', '-i',
            'postgresql-0', '--', 'psql', '-X', '-v', 'ON_ERROR_STOP=1',
            '-U', 'postgres', '-d', 'video_cloud', '-At'], query).strip()

    def create_in(self, obj, namespace, persist=True):
        m.require(obj['metadata'].get('namespace') == namespace,
                  'object namespace differs')
        self.kube(['-n', namespace, 'create', '-f', '-'], json.dumps(obj))
        if persist:
            m.write(self.base / 'pki/controller-bootstrap/rollout' /
                    (obj['metadata']['name'] + '-' +
                     obj['kind'].lower() + '.json'), obj)

    def ensure_registry_network_policy(self):
        expected = registry_network_policy()
        name = expected['metadata']['name']
        raw = self.kube(['-n', PLATFORM_NS, 'get', 'networkpolicy', name,
                         '--ignore-not-found', '-o', 'json'])
        if raw.strip():
            current = json.loads(raw)
            m.require(current.get('spec') == expected['spec'],
                      'OpenBao registry NetworkPolicy changed')
        else:
            self.create_in(expected, PLATFORM_NS)
        m.write(self.base / 'pki/controller-bootstrap/rollout' /
                (name + '-networkpolicy.json'), expected)

    def delete_exact(self, kind, name, namespace, obj):
        options = {'apiVersion': 'v1', 'kind': 'DeleteOptions',
                   'preconditions': {
                       'uid': obj['metadata']['uid']}}
        self.kube(['delete', '--raw', '/api/v1/namespaces/' + namespace +
                   '/' + kind + '/' + name, '-f', '-'], json.dumps(options))

    def patch_in(self, kind, name, namespace, before, patches):
        self.save('before-' + name + '-' + kind + '.json', before)
        changes = [{'op': 'test', 'path': '/metadata/resourceVersion',
                    'value': before['metadata']['resourceVersion']}]
        changes.extend(patches)
        return json.loads(self.kube([
            '-n', namespace, 'patch', kind, name, '--type=json',
            '--patch-file=/dev/stdin', '-o', 'json'], json.dumps(changes)))

    def openbao_pod(self):
        pods = json.loads(self.kube([
            '-n', SECRETS_NS, 'get', 'pods',
            '-l', 'app.kubernetes.io/instance=openbao,'
                  'app.kubernetes.io/name=openbao,component=server',
            '-o', 'json']))['items']
        pods = [pod for pod in pods
                if not pod['metadata'].get('deletionTimestamp')]
        m.require(len(pods) == 1, 'unexpected live OpenBao pod count')
        return pods[0]

    def wait_openbao_replacement(self, previous_uid):
        deadline = time.monotonic() + 120
        while True:
            raw = self.kube([
                '-n', SECRETS_NS, 'get', 'pod', 'openbao-0',
                '--ignore-not-found', '-o', 'json'])
            if raw.strip():
                pod = json.loads(raw)
                if pod['metadata']['uid'] != previous_uid:
                    return pod
            m.require(time.monotonic() < deadline,
                      'OpenBao replacement pod deadline')
            time.sleep(2)

    def wait_openbao(self):
        owner = self.obj('statefulset', 'openbao', SECRETS_NS)
        pod = self.openbao_pod()
        statuses = {item['name']: item for item in
                    pod['status'].get('containerStatuses', [])}
        init = {item['name']: item for item in
                pod['status'].get('initContainerStatuses', [])}
        m.require(all(statuses.get(name, {}).get('ready')
                      for name in ('openbao', 'openbao-pki'))
                  and init.get('openbao-pki-install', {}).get(
                      'state', {}).get('terminated', {}).get('exitCode') == 0
                  and owner.get('status', {}).get('readyReplicas') == 1
                  and owner.get('status', {}).get('updatedReplicas') == 1
                  and pod['metadata'].get('labels', {}).get(
                      'controller-revision-hash') ==
                  owner.get('status', {}).get('updateRevision'),
                  'managed OpenBao containers are not ready')
        return pod

    def unseal_openbao(self):
        path = self.base / 'openbao/unseal-key'
        info = os.lstat(path)
        m.require(not path.is_symlink() and info.st_mode & 0o077 == 0,
                  'dev OpenBao unseal key permissions changed')
        key = path.read_text().strip()
        m.require(re.fullmatch(r'[A-Za-z0-9+/=_-]{32,256}', key) is not None,
                  'invalid dev OpenBao unseal key')
        self.kube([
            '-n', SECRETS_NS, 'exec', '-i', 'pod/openbao-0',
            '-c', 'openbao', '--', 'sh', '-ec',
            'read -r key; BAO_SKIP_VERIFY=true '
            'bao operator unseal "$key" >/dev/null'], key + '\n')

    def openbao_peer(self, label, expected):
        self.forward(label, SECRETS_NS, 'openbao', 8200)
        peer = json.loads(m.command([
            self.probe, 'tls-peer', self.output / 'openbao-tls-root.pem',
            OPENBAO_HOST_NAMES[0], self.ports[label][0]]))
        m.require(peer['peer_sha256'] == expected['fingerprint'],
                  'OpenBao serves another registered certificate')
        return peer

    def install_host_probe(self, pod=None):
        pod = pod or self.openbao_pod()
        uid, name = pod['metadata']['uid'], pod['metadata']['name']
        if uid not in self.probe_pods:
            binary = self.output / 'pki-dev-probe-linux'
            if not binary.exists():
                result = m.subprocess.run([
                    'go', 'build', '-trimpath', '-ldflags=-s -w', '-o',
                    str(binary), './pki-dev-probe'],
                    cwd=m.WORKSPACE / 'scripts/go',
                    env=dict(os.environ, GOOS='linux', GOARCH='amd64',
                             CGO_ENABLED='0', GOWORK='off'),
                    capture_output=True, timeout=180)
                m.require(result.returncode == 0,
                          'OpenBao public state probe build failed')
            remote = '/run/openbao-pki/pki-dev-probe'
            self.kube([
                '-n', SECRETS_NS, 'exec', '-i', name,
                '-c', 'openbao-pki', '--', 'sh', '-ec',
                'umask 077; base64 -d > ' + remote +
                ' && chmod 700 ' + remote],
                base64.b64encode(binary.read_bytes()).decode())
            self.probe_pods[uid] = remote
        return name, self.probe_pods[uid]

    def inspect_host_state(self, pod=None):
        name, probe = self.install_host_probe(pod)
        return json.loads(self.kube([
            '-n', SECRETS_NS, 'exec', name, '-c', 'openbao-pki', '--',
            probe, 'service-state', OPENBAO_HOST_STATE]))

    def server_rows(self):
        raw = self.sql(
            "SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,caller,"
            "status,fingerprint,dns_names,certificate_pem,issued_at,revoked_at "
            "FROM pki_server_issuances WHERE environment='dev' "
            "AND domain='openbao_tls' ORDER BY issued_at,request_id) t;")
        return [row for row in (json.loads(line) for line in raw.splitlines()
                                if line)
                if row['dns_names'] == OPENBAO_HOST_NAMES]

    def current_host(self, allowed_issuers, pod=None):
        pod = pod or self.openbao_pod()
        state = self.inspect_host_state(pod)
        rows = self.server_rows()
        selected = [row for row in rows
                    if row['fingerprint'] == state['fingerprint']]
        m.require(not state['pending']
                  and state['subject'] == OPENBAO_HOST_NAMES[0]
                  and len(selected) == 1
                  and selected[0]['issuer_id'] in allowed_issuers
                  and selected[0]['caller'] == 'service:openbao'
                  and selected[0]['status'] == 'succeeded'
                  and selected[0]['revoked_at'] is None,
                  'current OpenBao host is not registry-admitted')
        m.write(self.output / 'openbao-tls-root.pem',
                self.openbao_root('active')['certificate_pem'])
        peer = self.openbao_peer('openbao-current', selected[0])
        return {'pod_uid': pod['metadata']['uid'], 'state': state,
                'row': selected[0], 'rows': rows,
                'peer_sha256': peer['peer_sha256'],
                'pvc_uid': self.obj('persistentvolumeclaim',
                                    OPENBAO_STATE_PVC,
                                    SECRETS_NS)['metadata']['uid']}

    def lifecycle_prerequisites(self):
        adoption = m.read(Path(self.args.adoption) / 'report.json')
        signer = m.read(Path(self.args.signer) / 'report.json')
        m.require(adoption['status'] == 'passed'
                  and adoption['phase'] == 'adopt-host'
                  and adoption['checks']['openbao_managed_host_adopted'][
                      'status'] == 'passed',
                  'successful managed OpenBao adoption required')
        m.require(signer['status'] == 'passed'
                  and signer['phase'] == 'configure-certissuer'
                  and signer['checks'][
                      'openbao_tls_server_only_signer_enabled'][
                      'status'] == 'passed',
                  'successful server-only signer evidence required')
        root, issuer, _ = self.ready_intermediate('active')
        predecessor = m.read(Path(self.args.intermediate) /
                             'intermediate-v1.json')
        pod = self.wait_openbao()
        owner = self.obj('statefulset', 'openbao', SECRETS_NS)
        containers = {item['name']: item for item in owner['spec'][
            'template']['spec']['containers']}
        worker = containers.get('openbao-pki', {})
        env = {item['name']: item for item in worker.get('env', [])}
        m.require(owner['spec']['replicas'] == 1
                  and owner['spec']['updateStrategy']['type'] == 'OnDelete'
                  and env.get('OPENBAO_PKI_HOST_IDENTITY_STATE', {}).get(
                      'value') == OPENBAO_HOST_STATE
                  and 'OPENBAO_PKI_SEED_CERT' not in env,
                  'managed OpenBao host ownership changed')
        return root, predecessor, issuer, owner, pod

    def renew_host(self):
        _, predecessor, issuer, owner, pod = self.lifecycle_prerequisites()
        before = self.current_host(
            {predecessor['issuer_id'], issuer['issuer_id']}, pod)
        m.require(before['row']['issuer_id'] == predecessor['issuer_id'],
                  'OpenBao predecessor is not the installed v1 leaf')
        self.save('baseline.json', before)
        processes = self.kube([
            '-n', SECRETS_NS, 'exec', pod['metadata']['name'],
            '-c', 'openbao-pki', '--', 'sh', '-ec',
            "for f in /proc/[0-9]*/comm; do "
            "[ \"$(cat \"$f\")\" = openbaopkihost ] && "
            "basename \"$(dirname \"$f\")\"; done"]).splitlines()
        m.require(len(processes) == 1 and processes[0].isdigit(),
                  'expected one OpenBao TLS identity owner process')
        intent = {'pod_uid': pod['metadata']['uid'],
                  'previous_fingerprint': before['state']['fingerprint'],
                  'target_issuer_id': issuer['issuer_id'],
                  'at': m.stamp(dt.datetime.now(dt.timezone.utc))}
        self.save('renewal-intent.json', intent)
        self.kube(['-n', SECRETS_NS, 'exec', pod['metadata']['name'],
                   '-c', 'openbao-pki', '--', 'kill', '-HUP', processes[0]])
        deadline = time.monotonic() + 180
        while True:
            try:
                state = self.inspect_host_state(pod)
            except RuntimeError:
                state = before['state']
            if (not state['pending']
                    and state['fingerprint'] !=
                    before['state']['fingerprint']):
                break
            m.require(time.monotonic() < deadline,
                      'OpenBao host renewal deadline; do not signal again')
            time.sleep(2)
        rows = self.server_rows()
        old_ids = {row['request_id'] for row in before['rows']}
        added = [row for row in rows if row['request_id'] not in old_ids]
        m.require(len(rows) == len(before['rows']) + 1
                  and len(added) == 1
                  and added[0]['fingerprint'] == state['fingerprint']
                  and added[0]['issuer_id'] == issuer['issuer_id']
                  and added[0]['caller'] == 'service:openbao'
                  and added[0]['status'] == 'succeeded'
                  and added[0]['revoked_at'] is None
                  and state['public_key_sha256'] !=
                  before['state']['public_key_sha256'],
                  'OpenBao host replacement registry evidence differs')
        renewed = self.current_host({issuer['issuer_id']}, pod)
        self.save('renewed.json', renewed)
        self.delete_exact('pods', pod['metadata']['name'], SECRETS_NS, pod)
        self.wait_openbao_replacement(pod['metadata']['uid'])
        self.kube(['-n', SECRETS_NS, 'wait',
                   '--for=jsonpath={.status.phase}=Running',
                   'pod/openbao-0', '--timeout=300s'], timeout=310)
        self.unseal_openbao()
        self.kube(['-n', SECRETS_NS, 'wait', '--for=condition=Ready',
                   'pod/openbao-0', '--timeout=120s'], timeout=130)
        restarted_pod = self.wait_openbao()
        restarted = self.current_host({issuer['issuer_id']}, restarted_pod)
        m.require(restarted['state'] == renewed['state']
                  and restarted['row'] == renewed['row']
                  and restarted['pvc_uid'] == renewed['pvc_uid']
                  and restarted_pod['metadata']['uid'] !=
                  pod['metadata']['uid']
                  and self.obj('statefulset', 'openbao', SECRETS_NS)[
                      'metadata']['uid'] == owner['metadata']['uid'],
                  'OpenBao successor changed across retained restart')
        self.check('openbao_host_renewed_on_server_only_v2', {
            'predecessor_fingerprint': before['state']['fingerprint'],
            'successor_fingerprint': renewed['state']['fingerprint'],
            'successor_issuer_id': issuer['issuer_id'],
            'new_owner_key': True, 'private_key_exported': False,
            'served_successor': True,
            'retained_restart': True})

    def retire_host_predecessor(self):
        _, predecessor, issuer, _, pod = self.lifecycle_prerequisites()
        renewal = Path(self.args.renewal)
        report = m.read(renewal / 'report.json')
        m.require(report['status'] == 'passed'
                  and report['phase'] == 'renew-host'
                  and report['checks'][
                      'openbao_host_renewed_on_server_only_v2'][
                      'status'] == 'passed',
                  'successful OpenBao v2 renewal evidence required')
        baseline = m.read(renewal / 'baseline.json')
        renewed = m.read(renewal / 'renewed.json')
        current = self.current_host({issuer['issuer_id']}, pod)
        m.require(current['state'] == renewed['state']
                  and current['pvc_uid'] == renewed['pvc_uid'],
                  'installed OpenBao v2 successor changed')
        rows = self.server_rows()
        old = [row for row in rows if row['fingerprint'] ==
               baseline['state']['fingerprint']]
        successor = [row for row in rows if row['fingerprint'] ==
                     renewed['state']['fingerprint']]
        m.require(len(old) == len(successor) == 1
                  and old[0]['issuer_id'] == predecessor['issuer_id']
                  and successor[0]['issuer_id'] == issuer['issuer_id']
                  and old[0]['revoked_at'] is None
                  and successor[0]['revoked_at'] is None,
                  'OpenBao retirement targets changed')
        previous = self.api('/issuers/' + predecessor['issuer_id'] + '/crl')
        self.save('target.json', old[0])
        self.save('previous-crl.json', previous)
        body = {
            'certificate_sha256': old[0]['fingerprint'],
            'reason': 'Replaced by server-only OpenBao TLS v2 renewal ' +
                      successor[0]['request_id']}
        self.save('revocation-request.json', body)
        revoked = self.api('/issuers/' + predecessor['issuer_id'] +
                           '/revoke-server', body)
        m.require(self.api('/issuers/' + predecessor['issuer_id'] +
                           '/revoke-server', body) == revoked,
                  'OpenBao v1 server revocation replay changed')
        self.save('revocation.json', revoked)
        denied = [row for row in self.server_rows()
                  if row['fingerprint'] == old[0]['fingerprint']]
        m.require(len(denied) == 1 and denied[0]['revoked_at'] is not None
                  and self.api('/issuers/' + predecessor['issuer_id'] +
                               '/crl') == previous,
                  'OpenBao v1 registry denial differs')
        publish = {'certificate_sha256': old[0]['fingerprint']}
        published = self.api('/issuers/' + predecessor['issuer_id'] +
                             '/publish-server-revocation', publish)
        m.require(published['crl_sha256'] != previous['crl_sha256']
                  and self.api('/issuers/' + predecessor['issuer_id'] +
                               '/publish-server-revocation', publish) ==
                  published,
                  'OpenBao v1 CRL publication differs')
        self.save('published-crl.json', published)
        receipts = self.wait_receipts(
            predecessor['issuer_id'], published['crl_sha256'], CONSUMERS,
            kind='crl')
        entries = json.loads(m.command(
            [self.probe, 'crl'], published['crl_pem'])) or []
        m.write(self.output / 'target.pem', old[0]['certificate_pem'])
        serial = m.command([
            self.openssl, 'x509', '-in', self.output / 'target.pem',
            '-noout', '-serial']).strip().split('=')[1]
        m.require(any(int(item['serial_hex'], 16) == int(serial, 16)
                      for item in entries),
                  'published OpenBao v1 CRL omits predecessor')
        finalized = self.api('/issuers/' + predecessor['issuer_id'] +
                             '/finalize-server-revocation', publish)
        m.require(finalized['crl_sha256'] == published['crl_sha256']
                  and self.api('/issuers/' + predecessor['issuer_id'] +
                               '/finalize-server-revocation', publish) ==
                  finalized,
                  'OpenBao v1 finalization differs')
        self.save('finalized.json', finalized)
        policy = ('pki-openbao-tls-server-dev-' +
                  predecessor['issuer_id'])
        self.role_policy('certissuer-pki-dev', policy)
        self.current_host({issuer['issuer_id']})
        self.check('openbao_v1_leaf_revocation_published', {
            'predecessor_fingerprint': old[0]['fingerprint'],
            'successor_fingerprint': successor[0]['fingerprint'],
            'crl_sha256': published['crl_sha256'],
            'consumers': receipts,
            'registry_denial': True,
            'crl_contains_predecessor': True,
            'finalization_replay_identical': True,
            'v1_certissuer_signer_removed': True,
            'v2_successor_served': True})

    def enable_provider_verification(self):
        root, predecessor, issuer, _, _ = self.lifecycle_prerequisites()
        source = Path(self.args.retirement)
        report = m.read(source / 'report.json')
        m.require(report['status'] == 'failed'
                  and report['phase'] == 'retire-host'
                  and report['failure'] == 'consumer receipt deadline',
                  'failed OpenBao v1 receipt evidence required')
        target = m.read(source / 'target.json')
        published = m.read(source / 'published-crl.json')
        rows = [row for row in self.server_rows()
                if row['fingerprint'] == target['fingerprint']]
        m.require(len(rows) == 1 and rows[0]['revoked_at'] is not None
                  and rows[0]['issuer_id'] == predecessor['issuer_id']
                  and self.api('/issuers/' + predecessor['issuer_id'] +
                               '/crl') == published,
                  'published OpenBao v1 revocation changed')
        m.require(IMAGE_PATTERN.fullmatch(self.args.image or ''),
                  'verified dev application image digest required')
        authorities = [root,
                       self.api('/issuers/' + predecessor['issuer_id']),
                       issuer]
        manifest = provider_crl_manifest(authorities)
        manifest_name = ('pki-openbao-tls-crls-identity-' +
                         issuer['issuer_id'][:8])
        raw = self.kube(['-n', NS, 'get', 'configmap', manifest_name,
                         '--ignore-not-found', '-o', 'json'])
        expected = {'crls.json': json.dumps(manifest)}
        if raw.strip():
            current_manifest = json.loads(raw)
            m.require(current_manifest.get('immutable') is True
                      and current_manifest.get('data') == expected,
                      'OpenBao provider CRL manifest changed')
        else:
            self.create({'apiVersion': 'v1', 'kind': 'ConfigMap',
                         'metadata': {'name': manifest_name,
                                      'namespace': NS},
                         'immutable': True, 'data': expected})
        for name in CONSUMERS:
            owner = self.obj('deployment', name)
            template = provider_verification_template(
                owner, root, manifest_name, self.args.image,
                self.output.name)
            self.scoped_patch(name, owner, template)
            self.kube(['-n', NS, 'rollout', 'status',
                       'deployment/' + name, '--timeout=300s'], timeout=310)
        receipts = {}
        for item in (root, predecessor, issuer):
            crl = self.api('/issuers/' + item['issuer_id'] + '/crl')
            receipts[item['issuer_id']] = self.wait_receipts(
                item['issuer_id'], crl['crl_sha256'], CONSUMERS, kind='crl')
        body = {'certificate_sha256': target['fingerprint']}
        finalized = self.api('/issuers/' + predecessor['issuer_id'] +
                             '/finalize-server-revocation', body)
        m.require(finalized['crl_sha256'] == published['crl_sha256']
                  and self.api('/issuers/' + predecessor['issuer_id'] +
                               '/finalize-server-revocation', body) ==
                  finalized,
                  'OpenBao v1 finalization differs after client recovery')
        self.save('finalized.json', finalized)
        policy = ('pki-openbao-tls-server-dev-' +
                  predecessor['issuer_id'])
        self.role_policy('certissuer-pki-dev', policy)
        current = self.current_host({issuer['issuer_id']})
        self.check('openbao_actual_clients_verify_registry_and_crls', {
            'root_sha256': root['certificate_fingerprint_sha256'],
            'server_name_by_consumer': {
                name: urllib.parse.urlparse(next(
                    item['value'] for item in self.obj(
                        'deployment', name)['spec']['template']['spec'][
                            'containers'][0]['env']
                    if item['name'] == 'OPENBAO_ADDR')).hostname
                for name in CONSUMERS},
            'crl_consumers': receipts,
            'managed_service_identities': True,
            'static_management_credentials': False,
            'private_crl_state': '/var/lib/pki-host/openbao-crls',
            'image': self.args.image,
            'v1_finalized': True,
            'v1_certissuer_signer_removed': True,
            'v2_successor_sha256': current['state']['fingerprint']})

    def recover_bootstrap_resources(self, request_id, image, bootstrap,
                                    database):
        pod = self.obj('pod', OPENBAO_SEED_POD, SECRETS_NS)
        env = {item['name']: item.get('value') for item in
               pod['spec']['containers'][0].get('env', [])}
        m.require(pod.get('status', {}).get('phase') == 'Failed'
                  and pod['metadata'].get('labels', {}).get(
                      'rtk.cloud/purpose') == 'pki-bootstrap'
                  and pod['spec']['containers'][0]['image'] == image
                  and env.get('OPENBAO_PKI_BOOTSTRAP_REQUEST_ID') == request_id,
                  'failed OpenBao bootstrap pod changed')
        bootstrap_secret = self.obj(
            'secret', OPENBAO_BOOTSTRAP_SECRET, SECRETS_NS)
        registry_secret = self.obj(
            'secret', OPENBAO_REGISTRY_SECRET, SECRETS_NS)
        m.require(bootstrap_secret.get('data') == bootstrap['data']
                  and registry_secret.get('data', {}).get('url') ==
                  database['data']['url'],
                  'failed OpenBao bootstrap secrets changed')
        for name, obj in ((OPENBAO_BOOTSTRAP_SECRET, bootstrap_secret),
                          (OPENBAO_REGISTRY_SECRET, registry_secret)):
            self.delete_exact('secrets', name, SECRETS_NS, obj)
        self.delete_exact('pods', OPENBAO_SEED_POD, SECRETS_NS, pod)

    def patch_provisioner(self, expected, replacement):
        owner = self.obj('deployment', 'certissuer')
        template = json.loads(json.dumps(owner['spec']['template']))
        container = template['spec']['containers'][0]
        values = {item['name']: item.get('value')
                  for item in container.get('env', [])}
        name = 'CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN'
        m.require(values.get(name) == expected,
                  'certissuer provisioner policy changed')
        container['env'] = h.with_env(container['env'], {name: replacement})
        self.scoped_patch('certissuer', owner, template)

    def service_v5(self):
        source = Path(self.args.service)
        report = m.read(source / 'report.json')
        root = m.read(source / 'service-root.json')
        saved = m.read(source / 'intermediate-v5-active.json')
        current = self.api('/issuers/' + saved['issuer_id'])
        m.require(report['status'] == 'passed'
                  and current == saved and current['status'] == 'active'
                  and current['parent_issuer_id'] == root['issuer_id']
                  and 'service:openbao' in current['service_client_ids'],
                  'active Service v5 evidence changed')
        self.save('service-root.json', root)
        self.save('service-v5.json', current)
        return root, current

    def bootstrap_rows(self):
        clients = self.sql(
            "SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,"
            "subject,caller,status,fingerprint,issued_at,revoked_at FROM "
            "pki_service_client_issuances WHERE environment='dev' AND "
            "subject='service:openbao' ORDER BY issued_at,request_id) t;")
        servers = self.sql(
            "SELECT row_to_json(t) FROM (SELECT request_id,issuer_id,caller,"
            "status,fingerprint,dns_names,issued_at,revoked_at FROM "
            "pki_server_issuances WHERE environment='dev' AND "
            "domain='openbao_tls' ORDER BY issued_at,request_id) t;")
        return ([json.loads(line) for line in clients.splitlines() if line],
                [json.loads(line) for line in servers.splitlines() if line])

    def bootstrap_host(self):
        route = m.read(Path(self.args.route) / 'report.json')
        m.require(route['status'] == 'passed'
                  and route['phase'] == 'configure-certissuer',
                  'successful OpenBao host route evidence required')
        root, issuer, _ = self.ready_intermediate('active')
        service_root, service_v5 = self.service_v5()
        m.require(OPENBAO_IMAGE_PATTERN.fullmatch(
                  self.args.openbao_image or ''),
                  'verified dev OpenBao image digest required')
        clients, servers = self.bootstrap_rows()
        m.require(not clients and not servers,
                  'OpenBao managed identity already exists; reconcile')
        failed = None
        request_id = 'dev-openbao-host-' + uuid.uuid4().hex
        if self.args.failed:
            failed = Path(self.args.failed)
            failed_report = m.read(failed / 'report.json')
            public = m.read(failed / 'bootstrap-pod-public.json')
            m.require(failed_report['status'] == 'failed'
                      and failed_report['phase'] == 'bootstrap-host'
                      and public['image'] == self.args.openbao_image
                      and public['state_paths'] == [
                          OPENBAO_CLIENT_STATE, OPENBAO_HOST_STATE]
                      and public['request_id'].startswith(
                          'dev-openbao-host-'),
                      'failed OpenBao bootstrap evidence changed')
            request_id = public['request_id']
        else:
            self.create_in({
                'apiVersion': 'v1', 'kind': 'PersistentVolumeClaim',
                'metadata': {'name': OPENBAO_STATE_PVC,
                             'namespace': SECRETS_NS},
                'spec': {'accessModes': ['ReadWriteOnce'],
                         'storageClassName': 'linode-block-storage-retain',
                         'resources': {'requests': {'storage': '10Gi'}}}},
                SECRETS_NS)
            self.kube(['-n', SECRETS_NS, 'wait',
                       '--for=jsonpath={.status.phase}=Bound',
                       'persistentvolumeclaim/' + OPENBAO_STATE_PVC,
                       '--timeout=300s'], timeout=310)
        pvc = self.obj('persistentvolumeclaim', OPENBAO_STATE_PVC,
                       SECRETS_NS)
        if failed:
            saved_pvc = m.read(failed / 'openbao-identity-pvc.json')
            m.require(saved_pvc['uid'] == pvc['metadata']['uid'],
                      'retained OpenBao identity PVC changed')
        m.require(pvc['spec']['storageClassName'] ==
                  'linode-block-storage-retain'
                  and pvc['status']['phase'] == 'Bound',
                  'OpenBao identity PVC is not retained and bound')
        self.save('openbao-identity-pvc.json', {
            'name': OPENBAO_STATE_PVC, 'uid': pvc['metadata']['uid'],
            'storage_class': pvc['spec']['storageClassName'],
            'phase': pvc['status']['phase']})
        root_config = {
            'apiVersion': 'v1', 'kind': 'ConfigMap',
            'metadata': {'name': OPENBAO_SERVICE_ROOT,
                         'namespace': SECRETS_NS},
            'immutable': True,
            'data': {'root.pem': service_root['certificate_pem']}}
        if failed:
            current_root = self.obj(
                'configmap', OPENBAO_SERVICE_ROOT, SECRETS_NS)
            m.require(current_root.get('immutable') is True
                      and current_root.get('data') == root_config['data'],
                      'OpenBao Service Root ConfigMap changed')
        else:
            self.create_in(root_config, SECRETS_NS)
        database = self.obj('secret', 'pki-controller-database')
        source = self.base / 'pki/consumers/certissuer'
        m.require(all((source / name).is_file()
                      for name in ('ca.crt', 'tls.crt', 'tls.key')),
                  'certissuer bootstrap identity missing')
        bootstrap = {
            'apiVersion': 'v1', 'kind': 'Secret',
            'metadata': {'name': OPENBAO_BOOTSTRAP_SECRET,
                         'namespace': SECRETS_NS},
            'type': 'Opaque', 'data': {
                name: base64.b64encode((source / name).read_bytes()).decode()
                for name in ('tls.crt', 'tls.key')}}
        self.ensure_registry_network_policy()
        if failed:
            self.recover_bootstrap_resources(
                request_id, self.args.openbao_image, bootstrap, database)
        self.create_in({
            'apiVersion': 'v1', 'kind': 'Secret',
            'metadata': {'name': OPENBAO_REGISTRY_SECRET,
                         'namespace': SECRETS_NS},
            'type': 'Opaque', 'data': {'url': database['data']['url']}},
            SECRETS_NS, persist=False)
        self.save('openbao-registry-secret.json', {
            'name': OPENBAO_REGISTRY_SECRET,
            'source_uid': database['metadata']['uid'],
            'url_sha256': m.digest(base64.b64decode(database['data']['url']))})
        self.create_in(bootstrap, SECRETS_NS, persist=False)
        tls = self.obj('secret', 'certissuer-runtime')
        original_ca = tls['data']['client-ca.crt']
        combined = h.append_pem(
            base64.b64decode(original_ca).decode(),
            (source / 'ca.crt').read_text())
        self.observed_patch('secret', 'certissuer-runtime', tls, [{
            'op': 'test', 'path': '/data/client-ca.crt',
            'value': original_ca}, {
            'op': 'replace', 'path': '/data/client-ca.crt',
            'value': base64.b64encode(combined.encode()).decode()}])
        certissuer_open = False
        pod_created = False
        try:
            self.patch_provisioner('^$', '^certissuer$')
            certissuer_open = True
            self.kube(['-n', NS, 'rollout', 'status',
                       'deployment/certissuer', '--timeout=300s'], timeout=310)
            issuer_name = 'certissuer.' + NS + '.svc'
            settings = {
                'PKI_ENVIRONMENT': 'dev',
                'PKI_DATABASE_URL': None,
                'OPENBAO_PKI_RUNTIME_DIR': '/run/openbao-pki',
                'OPENBAO_PKI_PROCESS_PID_FILE':
                    '/run/openbao-pki/openbao.pid',
                'OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_STATE':
                    OPENBAO_CLIENT_STATE,
                'OPENBAO_PKI_SERVICE_CLIENT_ROOT_SHA256':
                    service_root['certificate_fingerprint_sha256'],
                'OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_BOOTSTRAP_CERT':
                    '/run/bootstrap/tls.crt',
                'OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_BOOTSTRAP_KEY':
                    '/run/bootstrap/tls.key',
                'OPENBAO_PKI_HOST_IDENTITY_STATE': OPENBAO_HOST_STATE,
                'OPENBAO_PKI_HOST_NAME': OPENBAO_HOST_NAMES[0],
                'OPENBAO_PKI_HOST_DNS_NAMES': ','.join(OPENBAO_HOST_NAMES),
                'OPENBAO_PKI_HOST_ROOT_SHA256':
                    root['certificate_fingerprint_sha256'],
                'OPENBAO_PKI_HOST_RENEWAL_URL':
                    'https://' + issuer_name + ':9443',
                'OPENBAO_PKI_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256':
                    service_root['certificate_fingerprint_sha256'],
                'OPENBAO_PKI_HOST_RENEWAL_SERVER_PKI_NAME': issuer_name,
                'OPENBAO_PKI_HOST_RENEWAL_TLS_CA':
                    '/run/service-root/root.pem',
                'OPENBAO_PKI_HOST_RENEWAL_SERVER_PKI_SWEEP_INTERVAL': '10s',
                'OPENBAO_PKI_BOOTSTRAP_REQUEST_ID': request_id}
            env = [{'name': name, 'value': value}
                   for name, value in settings.items() if value is not None]
            env.append({'name': 'PKI_DATABASE_URL', 'valueFrom': {
                'secretKeyRef': {'name': OPENBAO_REGISTRY_SECRET,
                                 'key': 'url'}}})
            pod = {
                'apiVersion': 'v1', 'kind': 'Pod',
                'metadata': {'name': OPENBAO_SEED_POD,
                             'namespace': SECRETS_NS,
                             'labels': {
                                 'app.kubernetes.io/name': 'openbao',
                                 'rtk.cloud/purpose': 'pki-bootstrap'}},
                'spec': {
                    'automountServiceAccountToken': False,
                    'restartPolicy': 'Never',
                    'imagePullSecrets': [{'name': 'ghcr-pull'}],
                    'securityContext': {
                        'runAsUser': 100, 'runAsGroup': 1000,
                        'fsGroup': 1000,
                        'fsGroupChangePolicy': 'OnRootMismatch'},
                    'containers': [{
                        'name': 'bootstrap',
                        'image': self.args.openbao_image,
                        'command': ['/usr/local/bin/openbaopkihost'],
                        'args': ['bootstrap'], 'env': env,
                        'volumeMounts': [
                            {'name': 'state',
                             'mountPath': '/var/lib/openbao-pki'},
                            {'name': 'runtime',
                             'mountPath': '/run/openbao-pki'},
                            {'name': 'service-root',
                             'mountPath': '/run/service-root',
                             'readOnly': True},
                            {'name': 'bootstrap',
                             'mountPath': '/run/bootstrap',
                             'readOnly': True}],
                        'securityContext': {
                            'allowPrivilegeEscalation': False,
                            'capabilities': {'drop': ['ALL']},
                            'readOnlyRootFilesystem': True,
                            'runAsNonRoot': True}}],
                    'volumes': [
                        {'name': 'state', 'persistentVolumeClaim': {
                            'claimName': OPENBAO_STATE_PVC}},
                        {'name': 'runtime', 'emptyDir': {}},
                        {'name': 'service-root', 'configMap': {
                            'name': OPENBAO_SERVICE_ROOT}},
                        {'name': 'bootstrap', 'secret': {
                            'secretName': OPENBAO_BOOTSTRAP_SECRET,
                            'defaultMode': 0o440}}]}}
            self.save('bootstrap-pod-public.json', {
                'metadata': pod['metadata'],
                'image': self.args.openbao_image,
                'request_id': request_id,
                'state_paths': [OPENBAO_CLIENT_STATE, OPENBAO_HOST_STATE]})
            self.create_in(pod, SECRETS_NS, persist=False)
            pod_created = True
            self.kube(['-n', SECRETS_NS, 'wait',
                       '--for=jsonpath={.status.phase}=Succeeded',
                       'pod/' + OPENBAO_SEED_POD, '--timeout=300s'],
                      timeout=310)
        finally:
            if certissuer_open:
                self.patch_provisioner('^certissuer$', '^$')
            current_tls = self.obj('secret', 'certissuer-runtime')
            m.require(current_tls['data']['client-ca.crt'] ==
                      base64.b64encode(combined.encode()).decode(),
                      'certissuer bootstrap CA changed')
            self.observed_patch('secret', 'certissuer-runtime', current_tls, [{
                'op': 'replace', 'path': '/data/client-ca.crt',
                'value': original_ca}])
            self.kube(['-n', NS, 'rollout', 'status',
                       'deployment/certissuer', '--timeout=300s'], timeout=310)
        clients, servers = self.bootstrap_rows()
        m.require(len(clients) == 1 and len(servers) == 1
                  and clients[0]['issuer_id'] == service_v5['issuer_id']
                  and clients[0]['caller'] == 'certissuer'
                  and clients[0]['status'] == 'succeeded'
                  and clients[0]['revoked_at'] is None
                  and servers[0]['issuer_id'] == issuer['issuer_id']
                  and servers[0]['caller'] == 'service:openbao'
                  and servers[0]['dns_names'] == OPENBAO_HOST_NAMES
                  and servers[0]['status'] == 'succeeded'
                  and servers[0]['revoked_at'] is None,
                  'OpenBao bootstrap registry rows differ')
        self.save('service-client-issuance.json', clients[0])
        self.save('server-issuance.json', servers[0])
        if pod_created:
            pod = self.obj('pod', OPENBAO_SEED_POD, SECRETS_NS)
            options = {'apiVersion': 'v1', 'kind': 'DeleteOptions',
                       'preconditions': {
                           'uid': pod['metadata']['uid'],
                           'resourceVersion':
                               pod['metadata']['resourceVersion']}}
            self.kube(['delete', '--raw', '/api/v1/namespaces/' +
                       SECRETS_NS + '/pods/' + OPENBAO_SEED_POD,
                       '-f', '-'], json.dumps(options))
        secret = self.obj('secret', OPENBAO_BOOTSTRAP_SECRET, SECRETS_NS)
        self.kube(['-n', SECRETS_NS, 'delete', 'secret',
                   OPENBAO_BOOTSTRAP_SECRET, '--wait=true'])
        self.check('openbao_retained_identities_bootstrapped', {
            'service_issuer_id': service_v5['issuer_id'],
            'server_issuer_id': issuer['issuer_id'],
            'service_client_sha256': clients[0]['fingerprint'],
            'server_sha256': servers[0]['fingerprint'],
            'pvc_uid': pvc['metadata']['uid'],
            'temporary_bootstrap_secret_uid': secret['metadata']['uid'],
            'temporary_bootstrap_secret_deleted': True,
            'temporary_provisioner_closed': True,
            'request_id': request_id,
            'reconciled_from': str(failed) if failed else '',
            'private_keys_exported': False})

    def adopt_host(self):
        source = Path(self.args.bootstrap)
        report = m.read(source / 'report.json')
        client = m.read(source / 'service-client-issuance.json')
        server = m.read(source / 'server-issuance.json')
        m.require(report['status'] == 'passed'
                  and report['phase'] == 'bootstrap-host'
                  and client['subject'] == 'service:openbao'
                  and server['caller'] == 'service:openbao'
                  and server['dns_names'] == OPENBAO_HOST_NAMES,
                  'successful OpenBao bootstrap evidence required')
        root, issuer, _ = self.ready_intermediate('active')
        service_root, service_v5 = self.service_v5()
        m.require(client['issuer_id'] == service_v5['issuer_id']
                  and server['issuer_id'] == issuer['issuer_id'],
                  'OpenBao bootstrap lineage changed')
        m.require(OPENBAO_IMAGE_PATTERN.fullmatch(
                  self.args.openbao_image or ''),
                  'verified dev OpenBao image digest required')
        clients, servers = self.bootstrap_rows()
        m.require(clients == [client] and servers == [server],
                  'OpenBao bootstrap registry rows changed')
        pvc = self.obj('persistentvolumeclaim', OPENBAO_STATE_PVC,
                       SECRETS_NS)
        saved_pvc = m.read(source / 'openbao-identity-pvc.json')
        m.require(pvc['metadata']['uid'] == saved_pvc['uid']
                  and pvc['status']['phase'] == 'Bound',
                  'retained OpenBao identity PVC changed')
        m.require(not self.kube([
            '-n', SECRETS_NS, 'get', 'secret', OPENBAO_BOOTSTRAP_SECRET,
            '--ignore-not-found', '-o', 'name']).strip(),
            'temporary OpenBao bootstrap Secret remains')
        self.obj('secret', OPENBAO_REGISTRY_SECRET, SECRETS_NS)
        service_root_config = self.obj(
            'configmap', OPENBAO_SERVICE_ROOT, SECRETS_NS)
        m.require(service_root_config.get('immutable') is True
                  and service_root_config.get('data', {}).get('root.pem') ==
                  service_root['certificate_pem'],
                  'OpenBao Service Root source changed')
        m.write(self.output / 'openbao-tls-root.pem',
                root['certificate_pem'])

        rollout = self.base / 'pki/controller-bootstrap/rollout'
        config_path = rollout / 'openbao-managed-configmap.json'
        statefulset_path = rollout / 'openbao-managed-statefulset.json'
        config = self.obj('configmap', 'openbao-config', SECRETS_NS)
        owner = self.obj('statefulset', 'openbao', SECRETS_NS)
        if config_path.is_file() or statefulset_path.is_file():
            m.require(config_path.is_file() and statefulset_path.is_file(),
                      'partial saved OpenBao adoption state')
            desired_config = m.read(config_path)
            desired_owner = m.read(statefulset_path)
            m.require(desired_config['metadata']['name'] == 'openbao-config'
                      and desired_owner['metadata']['name'] == 'openbao',
                      'saved OpenBao adoption objects changed')
            if config.get('data') != desired_config['data']:
                m.require(managed_openbao_config(config['data']) ==
                          desired_config['data'],
                          'OpenBao listener differs from saved adoption')
            if owner['spec']['template'] != desired_owner['spec']['template']:
                m.require(managed_openbao_template(
                    owner, self.args.openbao_image, root, service_root) ==
                    desired_owner['spec']['template'],
                    'OpenBao StatefulSet differs from saved adoption')
        else:
            desired_config = {
                'apiVersion': config['apiVersion'], 'kind': config['kind'],
                'metadata': {'name': 'openbao-config',
                             'namespace': SECRETS_NS,
                             'labels': config['metadata'].get('labels', {}),
                             'annotations': config['metadata'].get(
                                 'annotations', {})},
                'data': managed_openbao_config(config['data'])}
            desired_owner = {
                'apiVersion': owner['apiVersion'], 'kind': owner['kind'],
                'metadata': {'name': 'openbao', 'namespace': SECRETS_NS,
                             'labels': owner['metadata'].get('labels', {}),
                             'annotations': owner['metadata'].get(
                                 'annotations', {})},
                'spec': dict(owner['spec'], template=managed_openbao_template(
                    owner, self.args.openbao_image, root, service_root))}
            m.write(config_path, desired_config)
            m.write(statefulset_path, desired_owner)
        self.save('desired-openbao-configmap.json', desired_config)
        self.save('desired-openbao-statefulset.json', desired_owner)
        if config['data'] != desired_config['data']:
            self.patch_in('configmap', 'openbao-config', SECRETS_NS,
                          config, [{'op': 'replace', 'path': '/data',
                                    'value': desired_config['data']}])
        if owner['spec']['template'] != desired_owner['spec']['template']:
            self.patch_in('statefulset', 'openbao', SECRETS_NS, owner, [{
                'op': 'replace', 'path': '/spec/template',
                'value': desired_owner['spec']['template']}])

        installed_config = self.obj('configmap', 'openbao-config', SECRETS_NS)
        installed_owner = self.obj('statefulset', 'openbao', SECRETS_NS)
        m.require(installed_config['data'] == desired_config['data']
                  and installed_owner['spec']['template'].get(
                      'metadata', {}).get('annotations', {}).get(
                          'rtk.cloud/openbao-managed-transport') ==
                  self.args.openbao_image.split('@')[-1],
                  'installed OpenBao adoption objects differ')
        # Retain API-defaulted container fields as the exact recovery target.
        desired_owner['spec']['template'] = installed_owner['spec']['template']
        m.write(statefulset_path, desired_owner)
        self.save('desired-openbao-statefulset.json', desired_owner)

        old = self.openbao_pod()
        self.delete_exact('pods', old['metadata']['name'], SECRETS_NS, old)
        self.wait_openbao_replacement(old['metadata']['uid'])
        self.kube(['-n', SECRETS_NS, 'wait',
                   '--for=jsonpath={.status.phase}=Running',
                   'pod/openbao-0', '--timeout=300s'], timeout=310)
        self.unseal_openbao()
        self.kube(['-n', SECRETS_NS, 'wait', '--for=condition=Ready',
                   'pod/openbao-0', '--timeout=120s'], timeout=130)
        first = self.wait_openbao()
        peer = self.openbao_peer('openbao-peer-first', server)
        state = self.kube([
            '-n', SECRETS_NS, 'exec', 'pod/' + first['metadata']['name'],
            '-c', 'openbao-pki', '--', 'sha256sum',
            OPENBAO_CLIENT_STATE, OPENBAO_HOST_STATE]).strip()
        runtime = self.kube([
            '-n', SECRETS_NS, 'exec', 'pod/' + first['metadata']['name'],
            '-c', 'openbao-pki', '--', 'sh', '-ec',
            'test -s ' + OPENBAO_RUNTIME + '/ready; test -s ' +
            OPENBAO_RUNTIME + '/openbao.pid; kill -0 "$(cat ' +
            OPENBAO_RUNTIME + '/openbao.pid)"; stat -c "%a %u" ' +
            OPENBAO_RUNTIME]).strip()
        m.require(runtime in ('700 100', '2700 100'),
                  'OpenBao private runtime permissions changed')

        self.delete_exact('pods', first['metadata']['name'], SECRETS_NS, first)
        self.wait_openbao_replacement(first['metadata']['uid'])
        self.kube(['-n', SECRETS_NS, 'wait',
                   '--for=jsonpath={.status.phase}=Running',
                   'pod/openbao-0', '--timeout=300s'], timeout=310)
        self.unseal_openbao()
        self.kube(['-n', SECRETS_NS, 'wait', '--for=condition=Ready',
                   'pod/openbao-0', '--timeout=120s'], timeout=130)
        second = self.wait_openbao()
        peer_after = self.openbao_peer('openbao-peer-second', server)
        state_after = self.kube([
            '-n', SECRETS_NS, 'exec', 'pod/' + second['metadata']['name'],
            '-c', 'openbao-pki', '--', 'sha256sum',
            OPENBAO_CLIENT_STATE, OPENBAO_HOST_STATE]).strip()
        m.require(second['metadata']['uid'] != first['metadata']['uid']
                  and state_after == state
                  and peer_after['peer_sha256'] == peer['peer_sha256']
                  and self.obj('persistentvolumeclaim', OPENBAO_STATE_PVC,
                               SECRETS_NS)['metadata']['uid'] ==
                  pvc['metadata']['uid'],
                  'OpenBao seed-free restart changed retained identity')
        live = self.obj('statefulset', 'openbao', SECRETS_NS)
        m.require(live['spec']['template'] == desired_owner['spec']['template']
                  and 'openbao-tls' not in json.dumps(
                      live['spec']['template']),
                  'legacy OpenBao TLS mount remains')
        raw_legacy = self.kube([
            '-n', SECRETS_NS, 'get', 'secret', 'openbao-tls',
            '--ignore-not-found', '-o', 'json'])
        if raw_legacy.strip():
            self.delete_exact('secrets', 'openbao-tls', SECRETS_NS,
                              json.loads(raw_legacy))
        m.require(not self.kube([
            '-n', SECRETS_NS, 'get', 'secret', 'openbao-tls',
            '--ignore-not-found', '-o', 'name']).strip(),
            'legacy OpenBao TLS Secret remains')
        self.check('openbao_managed_host_adopted', {
            'image': self.args.openbao_image,
            'server_sha256': server['fingerprint'],
            'service_client_sha256': client['fingerprint'],
            'pvc_uid': pvc['metadata']['uid'],
            'seed_free_restart': True,
            'openbao_pid_shared': True,
            'runtime_mode_owner': runtime,
            'legacy_tls_secret_deleted': True,
            'private_keys_exported': False})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--phase', required=True, choices=[
        'install-root-consumers', 'finish-root-consumers', 'activate-root',
        'install-intermediate-consumers', 'activate-intermediate',
        'configure-certissuer', 'bootstrap-host', 'adopt-host',
        'renew-host', 'retire-host', 'enable-provider-verification'])
    parser.add_argument('--authority', required=True)
    parser.add_argument('--image')
    parser.add_argument('--intermediate')
    parser.add_argument('--policy-evidence')
    parser.add_argument('--route')
    parser.add_argument('--service')
    parser.add_argument('--openbao-image')
    parser.add_argument('--bootstrap')
    parser.add_argument('--adoption')
    parser.add_argument('--signer')
    parser.add_argument('--renewal')
    parser.add_argument('--retirement')
    parser.add_argument('--failed')
    parser.add_argument('--server-only', action='store_true')
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    m.require(args.phase != 'finish-root-consumers' or args.failed,
              'failed Root consumer evidence required')
    m.require('intermediate' not in args.phase or args.intermediate,
              'ready OpenBao TLS intermediate evidence required')
    m.require(args.phase != 'configure-certissuer'
              or (args.intermediate and args.policy_evidence and args.image),
              'intermediate, signer policy and image required')
    m.require(args.phase != 'bootstrap-host'
              or (args.intermediate and args.route and args.service
                  and args.openbao_image),
              'active authorities, route and OpenBao image required')
    m.require(args.phase != 'adopt-host'
              or (args.intermediate and args.service and args.bootstrap
                  and args.openbao_image),
              'active authorities, bootstrap and OpenBao image required')
    m.require(args.phase != 'renew-host'
              or (args.server_only and args.intermediate and args.adoption
                  and args.signer),
              'server-only issuer, adoption and signer evidence required')
    m.require(args.phase != 'retire-host'
              or (args.server_only and args.intermediate and args.adoption
                  and args.signer and args.renewal),
              'server-only issuer, adoption, signer and renewal required')
    m.require(args.phase != 'enable-provider-verification'
              or (args.server_only and args.intermediate and args.adoption
                  and args.signer and args.retirement and args.image),
              'server-only issuer and failed retirement evidence required')
    lock = (Path(args.config_root).expanduser() /
            'dev/pki/openbao-host-rollout.lock')
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = OpenBaoHostRun(args)
    try:
        if args.phase == 'finish-root-consumers':
            runner.recovery_preflight()
        else:
            runner.preflight()
        {'install-root-consumers': runner.install_root_consumers,
         'finish-root-consumers': runner.finish_root_consumers,
         'activate-root': runner.activate_root,
         'install-intermediate-consumers':
             runner.install_intermediate_consumers,
         'activate-intermediate': runner.activate_intermediate,
         'configure-certissuer': runner.configure_certissuer,
         'bootstrap-host': runner.bootstrap_host,
         'adopt-host': runner.adopt_host,
         'renew-host': runner.renew_host,
         'retire-host': runner.retire_host_predecessor,
         'enable-provider-verification':
             runner.enable_provider_verification}[args.phase]()
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
    def interrupted(*_):
        raise RuntimeError('interrupted; reconcile saved OpenBao host evidence')
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        main()
    except Exception as error:
        print(json.dumps({'error': str(error)}), file=sys.stderr)
        sys.exit(1)
