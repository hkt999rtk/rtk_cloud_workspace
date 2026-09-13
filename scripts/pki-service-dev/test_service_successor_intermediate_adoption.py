import copy
import importlib.util
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location(
    'intermediate_adoption', Path(__file__).with_name('service_successor_intermediate_adoption.py'))
r = importlib.util.module_from_spec(spec); spec.loader.exec_module(r)

ROOT = {'issuer_id': '11111111-1111-4000-8000-000000000001', 'environment': 'dev',
        'trust_domain': 'service', 'kind': 'root', 'status': 'active'}
INTERMEDIATE = {'issuer_id': '22222222-2222-4000-8000-000000000002', 'environment': 'dev',
                'trust_domain': 'service', 'kind': 'intermediate', 'status': 'active',
                'parent_issuer_id': ROOT['issuer_id']}
OLD = {'issuer_id': '33333333-3333-4000-8000-000000000003', 'environment': 'dev',
       'trust_domain': 'service', 'kind': 'intermediate', 'status': 'retiring',
       'parent_issuer_id': '44444444-4444-4000-8000-000000000004'}


class IntermediateAdoptionTests(unittest.TestCase):
    def entry(self, issuer):
        return {'issuer': copy.deepcopy(issuer), 'state_path': '/private/crls/' + issuer['issuer_id'] + '.json'}

    def test_manifest_retains_existing_lineage_and_appends_active_intermediate(self):
        got = r.canonical_manifest([self.entry(ROOT), self.entry(OLD)], ROOT, INTERMEDIATE)
        self.assertEqual([item['issuer']['issuer_id'] for item in got],
                         [ROOT['issuer_id'], OLD['issuer_id'], INTERMEDIATE['issuer_id']])
        self.assertEqual(got[-1]['state_path'], '/private/crls/' + INTERMEDIATE['issuer_id'] + '.json')

    def test_manifest_is_idempotent_and_rejects_changed_state_path(self):
        entries = [self.entry(ROOT), self.entry(INTERMEDIATE)]
        self.assertEqual(r.canonical_manifest(entries, ROOT, INTERMEDIATE), entries)
        entries[-1]['state_path'] = '/different'
        with self.assertRaises(RuntimeError):
            r.canonical_manifest(entries, ROOT, INTERMEDIATE)

    def owner(self, target):
        env = []
        for prefix in target['prefixes']:
            env += [{'name': prefix + '_SERVICE_ROOT_ID', 'value': ROOT['issuer_id']},
                    {'name': prefix + '_SERVICE_ROOT_STATE', 'value': '/private/' + prefix},
                    {'name': prefix + '_SERVICE_ROOTS', 'value': '/roots/' + prefix}]
        volumes = [{'name': volume, 'configMap': {'name': prefix + '-old'}}
                   for volume, prefix in r.c.VOLUMES[(target['name'], target['namespace'])].items()]
        return {'spec': {'template': {'metadata': {}, 'spec': {'containers': [{'name': target['container'], 'env': env}],
                                                         'volumes': volumes}}}}

    def test_template_changes_only_owned_crl_volumes_and_annotation(self):
        replacements = {prefix: prefix + '-new' for _, _, _, prefix in r.c.MANIFESTS}
        for target in r.c.o.TARGETS:
            owner = self.owner(target)
            template = r.intermediate_template(owner, target, replacements, INTERMEDIATE['issuer_id'])
            for volume, prefix in r.c.VOLUMES[(target['name'], target['namespace'])].items():
                found = next(item for item in template['spec']['volumes'] if item['name'] == volume)
                self.assertEqual(found['configMap']['name'], replacements[prefix])
            values = r.c.o.env_map(template['spec']['containers'][0])
            for prefix in target['prefixes']:
                self.assertEqual(values[prefix + '_SERVICE_ROOT_ID'], ROOT['issuer_id'])
            self.assertEqual(template['metadata']['annotations']['rtk.realtek.com/service-intermediate-crl'],
                             INTERMEDIATE['issuer_id'])


if __name__ == '__main__': unittest.main()
