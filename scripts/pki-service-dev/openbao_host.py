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
import uuid


spec = importlib.util.spec_from_file_location(
    'service_hierarchy', Path(__file__).with_name('run.py'))
h = importlib.util.module_from_spec(spec)
spec.loader.exec_module(h)
m, NS = h.m, h.NS
SECRETS_NS = 'video-cloud-dev-secrets'

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


class OpenBaoHostRun(h.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
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
        immutable = (
            'issuer_id', 'environment', 'trust_domain', 'kind',
            'parent_issuer_id', 'issuer_version', 'signer_provider',
            'signer_reference', 'certificate_fingerprint_sha256',
            'certificate_pem', 'trust_bundle_version',
            'service_client_ids', 'server_dns_names')
        m.require(all(issuer.get(key) == saved.get(key) for key in immutable)
                  and issuer['status'] == status
                  and issuer['parent_issuer_id'] == root['issuer_id']
                  and issuer['service_client_ids'] == ['service:openbao'],
                  'OpenBao TLS intermediate identity or policy changed')
        operation = m.read(source / 'intermediate-operation.json')
        return root, issuer, operation

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
        m.require(IMAGE_PATTERN.fullmatch(self.args.image or ''),
                  'verified dev application image digest required')
        name = 'pki-openbao-tls-bundles-' + issuer['issuer_id'][:8]
        refs = [{'issuer_id': item['issuer_id'],
                 'trust_bundle_version': item['trust_bundle_version']}
                for item in (root, issuer)]
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
            template = self.intermediate_template(
                owner, self.args.image, name, self.output.name)
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
                  and '/sign/server' in policies['signer_policy'],
                  'saved OpenBao TLS signer policy changed')
        self.role_policy(
            'certissuer-pki-dev',
            'pki-openbao-tls-server-dev-' + issuer['issuer_id'],
            policies['signer_policy'])
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
        self.save('openbao-identity-pvc.json', {
            'name': OPENBAO_STATE_PVC, 'uid': pvc['metadata']['uid'],
            'storage_class': pvc['spec']['storageClassName'],
            'phase': pvc['status']['phase']})
        self.create_in({
            'apiVersion': 'v1', 'kind': 'ConfigMap',
            'metadata': {'name': OPENBAO_SERVICE_ROOT,
                         'namespace': SECRETS_NS},
            'immutable': True,
            'data': {'root.pem': service_root['certificate_pem']}},
            SECRETS_NS)
        database = self.obj('secret', 'pki-controller-database')
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
        request_id = 'dev-openbao-host-' + uuid.uuid4().hex
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
            'private_keys_exported': False})


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get(
        'RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--phase', required=True, choices=[
        'install-root-consumers', 'finish-root-consumers', 'activate-root',
        'install-intermediate-consumers', 'activate-intermediate',
        'configure-certissuer', 'bootstrap-host'])
    parser.add_argument('--authority', required=True)
    parser.add_argument('--image')
    parser.add_argument('--intermediate')
    parser.add_argument('--policy-evidence')
    parser.add_argument('--route')
    parser.add_argument('--service')
    parser.add_argument('--openbao-image')
    parser.add_argument('--failed')
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
         'bootstrap-host': runner.bootstrap_host}[args.phase]()
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
