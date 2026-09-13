import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('cutover', Path(__file__).with_name('service_root_authority_cutover.py'))
r = importlib.util.module_from_spec(spec); spec.loader.exec_module(r)

OLD = {'issuer_id': '00000000-0000-4000-8000-000000000001', 'environment': 'dev', 'trust_domain': 'service', 'kind': 'root', 'status': 'retiring'}
NEW = {'issuer_id': '11111111-1111-4000-8000-000000000002', 'environment': 'dev', 'trust_domain': 'service', 'kind': 'root', 'status': 'active'}

class AuthorityCutoverTests(unittest.TestCase):
    def entry(self, issuer=OLD):
        return {'issuer': copy.deepcopy(issuer), 'state_path': '/private/crls/' + issuer['issuer_id'] + '.json'}

    def test_manifest_retains_old_lineage_and_appends_successor(self):
        entries = [self.entry(), self.entry(dict(NEW, issuer_id='22222222-2222-4000-8000-000000000003', kind='intermediate'))]
        got = r.canonical_manifest(entries, OLD, NEW)
        self.assertEqual([row['issuer']['issuer_id'] for row in got], [OLD['issuer_id'], '22222222-2222-4000-8000-000000000003', NEW['issuer_id']])
        self.assertEqual(got[-1]['state_path'], '/private/crls/' + NEW['issuer_id'] + '.json')
        self.assertEqual(got[-1]['issuer']['status'], 'active')

    def test_manifest_rejects_missing_predecessor(self):
        with self.assertRaises(RuntimeError):
            r.canonical_manifest([self.entry(NEW)], OLD, NEW)

    def owner(self, target):
        env = []
        for prefix in target['prefixes']:
            env += [{'name': prefix + '_SERVICE_ROOT_ID', 'value': OLD['issuer_id']},
                    {'name': prefix + '_SERVICE_ROOT_STATE', 'value': '/private/' + prefix},
                    {'name': prefix + '_SERVICE_ROOTS', 'value': '/roots/' + prefix}]
        volumes = [{'name': target['volume'], 'configMap': {'name': target['new_config'].format(suffix=NEW['issuer_id'][:12])}}]
        for volume, prefix in r.VOLUMES[(target['name'], target['namespace'])].items():
            if volume != target['volume']:
                volumes.append({'name': volume, 'configMap': {'name': prefix + '-old'}})
        return {'spec': {'template': {'metadata': {}, 'spec': {'containers': [{'name': target['container'], 'env': env}], 'volumes': volumes}}}}

    def test_template_switches_only_authority_and_crl_mounts(self):
        replacements = {prefix: prefix + '-new' for _, _, _, prefix in r.MANIFESTS}
        for target in r.o.TARGETS:
            owner = self.owner(target)
            template = r.authority_template(owner, target, OLD['issuer_id'], NEW['issuer_id'], replacements)
            env = r.o.env_map(template['spec']['containers'][0])
            for prefix in target['prefixes']:
                self.assertEqual(env[prefix + '_SERVICE_ROOT_ID'], NEW['issuer_id'])
                self.assertEqual(env[prefix + '_SERVICE_ROOT_STATE'], '/private/' + prefix)
            for volume, prefix in r.VOLUMES[(target['name'], target['namespace'])].items():
                actual = next(v for v in template['spec']['volumes'] if v['name'] == volume)
                self.assertEqual(actual['configMap']['name'], replacements[prefix])

if __name__ == '__main__': unittest.main()
