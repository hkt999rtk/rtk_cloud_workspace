#!/usr/bin/env python3
"""Move Dev Service Root-policy consumers to an activated successor authority.

The successor CRL is added to immutable, workload-specific manifests before any
consumer changes its policy authority.  Old root/intermediate entries stay in
place until the later withdrawal ceremony has proven connection cutoff.
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

spec = importlib.util.spec_from_file_location('service_overlap', Path(__file__).with_name('service_root_overlap_adoption.py'))
o = importlib.util.module_from_spec(spec)
spec.loader.exec_module(o)
s, m, NS, AM_NS = o.s, o.m, o.NS, o.AM_NS

# Every Service CRL mount has a private state path.  A replacement is immutable
# and has a content address in its name, so no process observes a changed
# manifest through a projected ConfigMap without its reviewed restart.
MANIFESTS = (
    ('pki-service-client-crls', NS, 'crls.json', 'pki-service-client-crls'),
    ('factoryenroll-service-crls', NS, 'crls.json', 'factoryenroll-service-crls'),
    ('factoryenroll-account-manager-crls', NS, 'crls.json', 'factoryenroll-account-manager-crls'),
    ('video-cloud-api-account-manager-crls', NS, 'crls.json', 'video-cloud-api-account-manager-crls'),
    ('account-manager-service-crls', AM_NS, 'crls.json', 'account-manager-service-crls'),
    ('account-manager-egress-service-crls', AM_NS, 'crls.json', 'account-manager-egress-service-crls'),
)
VOLUMES = {
    ('pki-controller', NS): {'service-crls': 'pki-service-client-crls'},
    ('certissuer', NS): {'service-crls': 'pki-service-client-crls'},
    ('factoryenroll', NS): {'service-crls': 'factoryenroll-service-crls',
                            'account-manager-crls': 'factoryenroll-account-manager-crls'},
    ('video-cloud-api', NS): {'account-manager-crls': 'video-cloud-api-account-manager-crls'},
    ('account-manager', AM_NS): {'account-service-crls': 'account-manager-service-crls',
                                 'management-egress-service-crls': 'account-manager-egress-service-crls'},
}
UUID = re.compile(r'[0-9a-f-]{36}')


def canonical_manifest(entries, predecessor, successor):
    """Copy a Service CRL manifest and append exactly one successor root."""
    result, ids, predecessor_path = [], set(), None
    for entry in entries:
        issuer = entry.get('issuer', {})
        issuer_id, path = issuer.get('issuer_id', ''), entry.get('state_path', '')
        m.require(issuer.get('environment') == 'dev' and issuer.get('trust_domain') == 'service'
                  and issuer.get('status') in ('active', 'retiring') and UUID.fullmatch(issuer_id)
                  and isinstance(path, str) and path.startswith('/') and issuer_id not in ids,
                  'existing Service CRL manifest is invalid')
        if issuer_id == predecessor['issuer_id']:
            m.require(issuer.get('kind') == 'root' and predecessor_path is None,
                      'predecessor Service Root CRL entry differs')
            predecessor_path = path
        result.append(copy.deepcopy(entry)); ids.add(issuer_id)
    m.require(predecessor_path is not None, 'predecessor Service Root is absent from CRL manifest')
    successor_path = predecessor_path.replace(predecessor['issuer_id'], successor['issuer_id'])
    m.require(successor_path != predecessor_path and successor['issuer_id'] not in ids,
              'successor Service Root CRL entry already differs')
    result.append({'issuer': copy.deepcopy(successor), 'state_path': successor_path})
    return result


def manifest_name(prefix, raw):
    return prefix + '-' + hashlib.sha256(raw.encode()).hexdigest()[:12]


def crl_state(successor, record):
    m.require(record.get('issuer_id') == successor['issuer_id']
              and re.fullmatch(r'[0-9a-f]{64}', record.get('crl_sha256', ''))
              and isinstance(record.get('crl_pem'), str), 'successor Service Root CRL differs')
    return json.dumps({'issuer_fingerprint': successor['certificate_fingerprint_sha256'], 'crl': record},
                      separators=(',', ':'))


def authority_template(owner, target, predecessor_id, successor_id, replacements):
    """Patch a single known owner after every replacement is already created."""
    template = copy.deepcopy(owner['spec']['template'])
    containers = [c for c in template['spec'].get('containers', []) if c.get('name') == target['container']]
    m.require(len(containers) == 1, 'consumer container ownership changed: ' + target['name'])
    container = containers[0]
    values = o.env_map(container)
    updates = {}
    for prefix in target['prefixes']:
        current = values.get(prefix + '_SERVICE_ROOT_ID')
        m.require(current in (predecessor_id, successor_id) and values.get(prefix + '_SERVICE_ROOT_STATE')
                  and values.get(prefix + '_SERVICE_ROOTS'), 'Service Root overlap state differs: ' + target['name'] + '/' + prefix)
        updates[prefix + '_SERVICE_ROOT_ID'] = successor_id
    container['env'] = o.with_env(container.get('env', []), updates)
    expected = VOLUMES[(target['name'], target['namespace'])]
    volumes = {v['name']: v for v in template['spec'].get('volumes', [])}
    for volume, prefix in expected.items():
        actual = volumes.get(volume, {}).get('configMap', {}).get('name', '')
        m.require(actual == prefix or actual.startswith(prefix + '-'),
                  'Service CRL mount ownership changed: ' + target['name'] + '/' + volume)
        volumes[volume]['configMap']['name'] = replacements[prefix]
    annotations = template.setdefault('metadata', {}).setdefault('annotations', {})
    annotations['rtk.realtek.com/service-root-policy'] = successor_id
    annotations['rtk.realtek.com/service-root-overlap'] = successor_id
    return template


class Cutover(s.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report['foundation_scope'] = 'Dev Service Root successor authority cutover; predecessor withdrawal is not attempted'
        self.report['service_root_authority_cutover_runner_sha256'] = m.digest(Path(__file__).read_bytes())
        self.save('report.json', self.report)

    def evidence(self, directory, names):
        directory = Path(directory)
        path = next((directory / name for name in names if (directory / name).is_file()), None)
        m.require(path is not None, 'reviewed Service Root evidence is missing')
        saved = m.read(path)
        current = self.api('/issuers/' + saved['issuer_id'])
        m.require(current['issuer_id'] == saved['issuer_id']
                  and current['certificate_fingerprint_sha256'] == saved['certificate_fingerprint_sha256']
                  and current['environment'] == 'dev' and current['trust_domain'] == 'service'
                  and current['kind'] == 'root', 'reviewed Service Root evidence changed')
        return current

    def preflight(self, predecessor, successor):
        m.require(self.kube(['config', 'current-context']).strip() == self.context, 'canonical Dev context mismatch')
        m.require(predecessor['status'] == 'retiring' and successor['status'] == 'active',
                  'Service Root transition state differs')
        crl = self.api('/issuers/' + successor['issuer_id'] + '/crl')
        m.require(crl.get('issuer_id') == successor['issuer_id'] and re.fullmatch(r'[0-9a-f]{64}', crl.get('crl_sha256', '')),
                  'active successor Service Root CRL is missing')
        self.successor_crl = crl
        for target in o.TARGETS:
            owner = self.obj('deployment', target['name'], target['namespace'])
            m.require(owner['spec'].get('replicas') == 1 and owner.get('status', {}).get('readyReplicas') == 1
                      and owner.get('status', {}).get('updatedReplicas') == 1
                      and owner.get('status', {}).get('observedGeneration') == owner['metadata'].get('generation'),
                      'required Dev deployment is not ready: ' + target['name'])
        self.check('service_root_authority_cutover_preflight', {
            'predecessor_root_id': predecessor['issuer_id'], 'successor_root_id': successor['issuer_id'],
            'successor_root_crl_sha256': crl['crl_sha256'], 'staging_touched': False})

    def create_manifests(self, predecessor, successor):
        replacements, evidence = {}, {}
        for live_prefix, namespace, key, output_prefix in MANIFESTS:
            # Locate a compatible current ConfigMap by the owned prefix.  The
            # caller deployment identifies the effective name; only a known
            # canonical source may supply the replacement content.
            names = set()
            for (name, ns), volumes in VOLUMES.items():
                if ns != namespace:
                    continue
                for volume, prefix in volumes.items():
                    if prefix == live_prefix:
                        owner = self.obj('deployment', name, ns)
                        names.add(next(v['configMap']['name'] for v in owner['spec']['template']['spec']['volumes'] if v['name'] == volume))
            m.require(len(names) == 1, 'Service CRL manifest source is ambiguous: ' + live_prefix)
            source_name = names.pop()
            source = self.obj('configmap', source_name, namespace)
            entries = json.loads(source.get('data', {}).get(key, ''))
            desired = canonical_manifest(entries, predecessor, successor)
            raw = json.dumps(desired, separators=(',', ':')) + '\n'
            name = manifest_name(output_prefix, raw)
            existing = self.kube(['-n', namespace, 'get', 'configmap', name, '-o', 'json', '--ignore-not-found'])
            if existing:
                saved = json.loads(existing)
                m.require(saved.get('immutable') is True and saved.get('data', {}).get(key) == raw,
                          'immutable successor Service CRL manifest differs: ' + name)
            else:
                obj = {'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': name, 'namespace': namespace},
                       'immutable': True, 'data': {key: raw}}
                self.kube(['-n', namespace, 'create', '-f', '-'], json.dumps(obj))
                self.save('create-' + namespace + '-' + name + '.json', obj)
            replacements[live_prefix] = name
            evidence[live_prefix] = {'source': source_name, 'replacement': name,
                                     'sha256': hashlib.sha256(raw.encode()).hexdigest(), 'entries': len(desired),
                                     'manifest': desired}
        return replacements, evidence

    def seed_successor_crls(self, successor, record, manifests):
        state = crl_state(successor, record)
        encoded = __import__('base64').b64encode(state.encode()).decode()
        paths = {}
        for target in o.TARGETS:
            for _, prefix in VOLUMES[(target['name'], target['namespace'])].items():
                entries = manifests[prefix]['manifest']
                match = [entry['state_path'] for entry in entries
                         if entry['issuer']['issuer_id'] == successor['issuer_id']]
                m.require(len(match) == 1, 'successor Service Root CRL path is missing: ' + prefix)
                path = match[0]
                # This is private workload state, not a projected ConfigMap.
                # An existing state is accepted only when it exactly matches the
                # signed successor CRL record; a different cached authority is
                # never overwritten by this transition.
                command = ('if test -e ' + path + '; then cat ' + path + '; else '
                           'umask 077; mkdir -p ' + str(Path(path).parent) + '; printf %s ' + encoded +
                           ' | base64 -d > ' + path + '; chmod 600 ' + path + '; cat ' + path + '; fi')
                actual = self.kube(['-n', target['namespace'], 'exec', 'deployment/' + target['name'], '-c',
                                    target['container'], '--', 'sh', '-c', command])
                m.require(actual == state, 'successor Service Root CRL state differs: ' + target['name'] + '/' + path)
                paths.setdefault(target['consumer'], []).append(path)
        self.check('service_root_successor_crl_seeded', {
            'successor_root_id': successor['issuer_id'], 'crl_sha256': record['crl_sha256'],
            'state_paths': paths, 'staging_touched': False})

    def rollout(self, target, predecessor, successor, replacements):
        owner = self.obj('deployment', target['name'], target['namespace'])
        template = authority_template(owner, target, predecessor['issuer_id'], successor['issuer_id'], replacements)
        if template != owner['spec']['template']:
            patch = [{'op': 'test', 'path': '/metadata/resourceVersion', 'value': owner['metadata']['resourceVersion']},
                     {'op': 'test', 'path': '/spec/template', 'value': owner['spec']['template']},
                     {'op': 'replace', 'path': '/spec/template', 'value': template}]
            self.kube(['-n', target['namespace'], 'patch', 'deployment', target['name'], '--type=json', '--patch-file=/dev/stdin'], json.dumps(patch))
        self.kube(['-n', target['namespace'], 'rollout', 'status', 'deployment/' + target['name'], '--timeout=300s'], timeout=310)
        self.save(target['name'] + '-template.json', template)

    def verify(self, successor, replacements):
        for target in o.TARGETS:
            owner = self.obj('deployment', target['name'], target['namespace'])
            template = authority_template(owner, target, successor['issuer_id'], successor['issuer_id'], replacements)
            m.require(template == owner['spec']['template'], 'post-cutover template drift: ' + target['name'])
        self.check('service_root_authority_cutover_complete', {
            'successor_root_id': successor['issuer_id'], 'crl_manifests': replacements,
            'predecessor_withdrawal': 'not_attempted', 'staging_touched': False})

    def go(self):
        m.require(self.kube(['config', 'current-context']).strip() == self.context, 'canonical Dev context mismatch')
        self.forward('am', AM_NS, 'account-manager', 80)
        self.accounts = m.read(self.foundation / 'accounts.json')
        predecessor = self.evidence(self.args.predecessor, ('service-root.json', 'root-ready.json'))
        successor = self.evidence(self.args.successor, ('successor-active.json', 'service-root.json', 'root-ready.json'))
        self.preflight(predecessor, successor)
        replacements, manifests = self.create_manifests(predecessor, successor)
        self.seed_successor_crls(successor, self.successor_crl, manifests)
        for target in o.TARGETS:
            self.rollout(target, predecessor, successor, replacements)
        self.verify(successor, replacements)
        self.save('manifests.json', manifests)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--config-root', default=os.environ.get('RTK_CLOUD_CONFIG_ROOT', str(Path.home() / '.config/rtk_cloud')))
    p.add_argument('--predecessor', required=True)
    p.add_argument('--successor', required=True)
    p.add_argument('--output', required=True)
    a = p.parse_args(); a.phase = 'cutover-service-root-authority'
    lock = Path(a.config_root).expanduser() / 'dev/pki/service-rollout.lock'
    fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600); fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    r = Cutover(a)
    try:
        r.go(); r.report['status'] = 'passed'
    except Exception as e:
        r.report['status'], r.report['failure'] = 'failed', str(e); raise
    finally:
        r.save('report.json', r.report); r.close(); os.close(fd)
        print(json.dumps({'status': r.report['status'], 'report': str(r.output / 'report.json')}), flush=True)

if __name__ == '__main__':
    try: main()
    except Exception as e: print(json.dumps({'error': str(e)}), file=sys.stderr); sys.exit(1)
