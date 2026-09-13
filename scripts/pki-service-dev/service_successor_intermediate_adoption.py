#!/usr/bin/env python3
"""Install an active Dev Service intermediate CRL before Service leaf rotation.

The Service Root policy has already been cut over.  This runner extends every
immutable Service CRL manifest with the active intermediate, seeds the matching
private cache on every workload, and only then restarts the individual owners.
It neither changes the Service Root authority nor withdraws any prior lineage.
"""
import argparse
import copy
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import sys
import time

spec = importlib.util.spec_from_file_location(
    'service_root_cutover', Path(__file__).with_name('service_root_authority_cutover.py'))
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)
s, m, NS, AM_NS = c.s, c.m, c.NS, c.AM_NS
UUID = re.compile(r'[0-9a-f-]{36}')


def canonical_manifest(entries, root, intermediate):
    """Retain the current Service manifest and add one active intermediate."""
    result, ids, root_path, installed_path = [], set(), None, None
    for entry in entries:
        issuer, path = entry.get('issuer', {}), entry.get('state_path', '')
        issuer_id = issuer.get('issuer_id', '')
        m.require(issuer.get('environment') == 'dev' and issuer.get('trust_domain') == 'service'
                  and issuer.get('status') in ('active', 'retiring') and UUID.fullmatch(issuer_id)
                  and isinstance(path, str) and path.startswith('/') and issuer_id not in ids,
                  'existing Service CRL manifest is invalid')
        if issuer_id == root['issuer_id']:
            m.require(issuer == root and root_path is None, 'active Service Root CRL entry differs')
            root_path = path
        if issuer_id == intermediate['issuer_id']:
            m.require(issuer == intermediate and installed_path is None,
                      'active Service intermediate CRL entry differs')
            installed_path = path
        result.append(copy.deepcopy(entry)); ids.add(issuer_id)
    m.require(root_path is not None, 'active Service Root is absent from CRL manifest')
    expected_path = root_path.replace(root['issuer_id'], intermediate['issuer_id'])
    m.require(expected_path != root_path, 'Service Root CRL path cannot derive intermediate path')
    if installed_path is None:
        result.append({'issuer': copy.deepcopy(intermediate), 'state_path': expected_path})
    else:
        m.require(installed_path == expected_path, 'active Service intermediate state path differs')
    return result


def intermediate_template(owner, target, replacements, intermediate_id):
    """Patch only CRL ConfigMap volume names on a known Service consumer."""
    template = copy.deepcopy(owner['spec']['template'])
    containers = [item for item in template['spec'].get('containers', [])
                  if item.get('name') == target['container']]
    m.require(len(containers) == 1, 'consumer container ownership changed: ' + target['name'])
    values = c.o.env_map(containers[0])
    for prefix in target['prefixes']:
        m.require(values.get(prefix + '_SERVICE_ROOT_ID') and values.get(prefix + '_SERVICE_ROOT_STATE')
                  and values.get(prefix + '_SERVICE_ROOTS'),
                  'Service Root policy state is incomplete: ' + target['name'] + '/' + prefix)
    volumes = {item['name']: item for item in template['spec'].get('volumes', [])}
    for volume, prefix in c.VOLUMES[(target['name'], target['namespace'])].items():
        current = volumes.get(volume, {}).get('configMap', {}).get('name', '')
        m.require(current == prefix or current.startswith(prefix + '-'),
                  'Service CRL mount ownership changed: ' + target['name'] + '/' + volume)
        volumes[volume]['configMap']['name'] = replacements[prefix]
    annotations = template.setdefault('metadata', {}).setdefault('annotations', {})
    annotations['rtk.realtek.com/service-intermediate-crl'] = intermediate_id
    return template


class IntermediateAdoption(s.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = ('Dev active Service intermediate CRL adoption before leaf rotation; '
                                           'Service Root authority and predecessor withdrawal are unchanged')
        self.report['service_successor_intermediate_adoption_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def api(self, path, body=None, *args, **kwargs):
        if body is not None:
            return super().api(path, body, *args, **kwargs)
        for attempt in range(3):
            try:
                return super().api(path, None, *args, **kwargs)
            except RuntimeError as error:
                if 'status 503' not in str(error) or attempt == 2:
                    raise
                time.sleep(attempt + 1)

    def evidence(self, directory, names, kind, status):
        directory = Path(directory)
        path = next((directory / name for name in names if (directory / name).is_file()), None)
        m.require(path is not None, 'reviewed Service issuer evidence is missing')
        saved = m.read(path)
        current = self.api('/issuers/' + saved['issuer_id'])
        m.require(current == saved and current['environment'] == 'dev' and current['trust_domain'] == 'service'
                  and current['kind'] == kind and current['status'] == status,
                  'reviewed Service issuer evidence changed')
        return current

    def preflight(self, root, intermediate):
        m.require(self.kube(['config', 'current-context']).strip() == self.context, 'canonical Dev context mismatch')
        m.require(intermediate['parent_issuer_id'] == root['issuer_id'],
                  'active Service intermediate is not under the active Service Root')
        record = self.api('/issuers/' + intermediate['issuer_id'] + '/crl')
        m.require(record.get('issuer_id') == intermediate['issuer_id']
                  and re.fullmatch(r'[0-9a-f]{64}', record.get('crl_sha256', ''))
                  and isinstance(record.get('crl_pem'), str), 'active Service intermediate CRL is missing')
        self.intermediate_crl = record
        for target in c.o.TARGETS:
            owner = self.obj('deployment', target['name'], target['namespace'])
            m.require(owner['spec'].get('replicas') == 1 and owner.get('status', {}).get('readyReplicas') == 1
                      and owner.get('status', {}).get('updatedReplicas') == 1
                      and owner.get('status', {}).get('observedGeneration') == owner['metadata'].get('generation'),
                      'required Dev deployment is not ready: ' + target['name'])
        self.check('service_successor_intermediate_crl_preflight', {
            'service_root_id': root['issuer_id'], 'intermediate_id': intermediate['issuer_id'],
            'intermediate_crl_sha256': record['crl_sha256'], 'staging_touched': False})

    def create_manifests(self, root, intermediate):
        replacements, evidence = {}, {}
        for live_prefix, namespace, key, output_prefix in c.MANIFESTS:
            names = set()
            for (name, ns), volumes in c.VOLUMES.items():
                if ns != namespace:
                    continue
                for volume, prefix in volumes.items():
                    if prefix == live_prefix:
                        owner = self.obj('deployment', name, ns)
                        names.add(next(item['configMap']['name'] for item in owner['spec']['template']['spec']['volumes']
                                       if item['name'] == volume))
            m.require(len(names) == 1, 'Service CRL manifest source is ambiguous: ' + live_prefix)
            source_name = names.pop()
            source = self.obj('configmap', source_name, namespace)
            entries = json.loads(source.get('data', {}).get(key, ''))
            desired = canonical_manifest(entries, root, intermediate)
            raw = json.dumps(desired, separators=(',', ':')) + '\n'
            name = c.manifest_name(output_prefix, raw)
            existing = self.kube(['-n', namespace, 'get', 'configmap', name, '-o', 'json', '--ignore-not-found'])
            if existing:
                saved = json.loads(existing)
                m.require(saved.get('immutable') is True and saved.get('data', {}).get(key) == raw,
                          'immutable Service intermediate CRL manifest differs: ' + name)
            else:
                obj = {'apiVersion': 'v1', 'kind': 'ConfigMap',
                       'metadata': {'name': name, 'namespace': namespace}, 'immutable': True, 'data': {key: raw}}
                self.kube(['-n', namespace, 'create', '-f', '-'], json.dumps(obj))
                self.save('create-' + namespace + '-' + name + '.json', obj)
            replacements[live_prefix] = name
            evidence[live_prefix] = {'source': source_name, 'replacement': name,
                                     'sha256': hashlib.sha256(raw.encode()).hexdigest(),
                                     'entries': len(desired), 'manifest': desired}
        return replacements, evidence

    def seed_intermediate_crls(self, intermediate, record, manifests):
        state = c.crl_state(intermediate, record)
        encoded = __import__('base64').b64encode(state.encode()).decode()
        state_digest = hashlib.sha256(state.encode()).hexdigest()
        paths = {}
        for target in c.o.TARGETS:
            for _, prefix in c.VOLUMES[(target['name'], target['namespace'])].items():
                matches = [entry['state_path'] for entry in manifests[prefix]['manifest']
                           if entry['issuer']['issuer_id'] == intermediate['issuer_id']]
                m.require(len(matches) == 1, 'active Service intermediate CRL path is missing: ' + prefix)
                path = matches[0]
                # Do not emit the cache contents: the state contains public CRL
                # material, and a digest is enough to prove exact replay safety.
                command = ('if test -e ' + path + '; then true; else umask 077; mkdir -p ' +
                           str(Path(path).parent) + '; printf %s ' + encoded + ' | base64 -d > ' + path +
                           '; chmod 600 ' + path + '; fi; test "$(sha256sum ' + path +
                           ' | cut -d \" \" -f1)" = "' + state_digest + '"')
                self.kube(['-n', target['namespace'], 'exec', 'deployment/' + target['name'], '-c',
                           target['container'], '--', 'sh', '-c', command])
                paths.setdefault(target['consumer'], []).append(path)
        self.check('service_successor_intermediate_crl_seeded', {
            'intermediate_id': intermediate['issuer_id'], 'crl_sha256': record['crl_sha256'],
            'state_paths': paths, 'staging_touched': False})

    def rollout(self, target, replacements, intermediate):
        owner = self.obj('deployment', target['name'], target['namespace'])
        template = intermediate_template(owner, target, replacements, intermediate['issuer_id'])
        if template != owner['spec']['template']:
            patch = [{'op': 'test', 'path': '/metadata/resourceVersion', 'value': owner['metadata']['resourceVersion']},
                     {'op': 'test', 'path': '/spec/template', 'value': owner['spec']['template']},
                     {'op': 'replace', 'path': '/spec/template', 'value': template}]
            self.kube(['-n', target['namespace'], 'patch', 'deployment', target['name'], '--type=json',
                       '--patch-file=/dev/stdin'], json.dumps(patch))
        self.kube(['-n', target['namespace'], 'rollout', 'status', 'deployment/' + target['name'], '--timeout=300s'],
                  timeout=310)
        self.save(target['name'] + '-template.json', template)

    def verify(self, root, intermediate, replacements, manifests):
        state = c.crl_state(intermediate, self.intermediate_crl)
        state_digest = hashlib.sha256(state.encode()).hexdigest()
        m.require(self.api('/issuers/' + intermediate['issuer_id'] + '/crl') == self.intermediate_crl,
                  'active Service intermediate CRL changed during adoption')
        for target in c.o.TARGETS:
            owner = self.obj('deployment', target['name'], target['namespace'])
            expected = intermediate_template(owner, target, replacements, intermediate['issuer_id'])
            m.require(expected == owner['spec']['template'], 'post-adoption template drift: ' + target['name'])
            for _, prefix in c.VOLUMES[(target['name'], target['namespace'])].items():
                paths = [entry['state_path'] for entry in manifests[prefix]['manifest']
                         if entry['issuer']['issuer_id'] == intermediate['issuer_id']]
                m.require(len(paths) == 1, 'active Service intermediate CRL path is missing after rollout')
                self.kube(['-n', target['namespace'], 'exec', 'deployment/' + target['name'], '-c',
                           target['container'], '--', 'sh', '-c',
                           'test "$(sha256sum ' + paths[0] + ' | cut -d " " -f1)" = "' + state_digest + '"'])
        self.check('service_successor_intermediate_crl_adopted', {
            'service_root_id': root['issuer_id'], 'intermediate_id': intermediate['issuer_id'],
            'crl_manifests': replacements, 'predecessor_withdrawal': 'not_attempted', 'staging_touched': False})

    def go(self):
        self.forward('am', AM_NS, 'account-manager', 80)
        self.accounts = m.read(self.foundation / 'accounts.json')
        root = self.evidence(self.args.authority, ('successor-active.json', 'service-root.json'), 'root', 'active')
        intermediate = self.evidence(self.args.intermediate, ('intermediate-active.json',), 'intermediate', 'active')
        self.preflight(root, intermediate)
        replacements, manifests = self.create_manifests(root, intermediate)
        self.seed_intermediate_crls(intermediate, self.intermediate_crl, manifests)
        for target in c.o.TARGETS:
            self.rollout(target, replacements, intermediate)
        self.verify(root, intermediate, replacements, manifests)
        self.save('manifests.json', manifests)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT',
                        str(Path.home() / '.config/rtk_cloud')))
    parser.add_argument('--authority', required=True)
    parser.add_argument('--intermediate', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args(); args.phase = 'adopt-service-successor-intermediate-crl'
    lock = Path(args.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600); fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = IntermediateAdoption(args)
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
