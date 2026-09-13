import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('overlap_adoption', Path(__file__).with_name('service_root_overlap_adoption.py'))
r = importlib.util.module_from_spec(spec); spec.loader.exec_module(r)

class OverlapAdoptionTests(unittest.TestCase):
    def owner(self, target):
        root = '00000000-0000-4000-8000-000000000001'
        env = []
        for prefix in target['prefixes']:
            env.extend([{'name': prefix + '_SERVICE_ROOT_ID', 'value': root},
                        {'name': prefix + '_SERVICE_ROOT_STATE', 'value': '/old/' + prefix},
                        {'name': prefix + '_SERVICE_ROOTS', 'value': '/roots/' + prefix}])
        return {'spec': {'template': {'metadata': {}, 'spec': {'containers': [{'name': target['container'], 'env': env}],
            'volumes': [{'name': target['volume'], 'configMap': {'name': target['old_config']}}] + ([{'name': target['bundle_volume'], 'configMap': {'name': target['old_bundle_prefix'] + 'current'}}] if target.get('bundle_volume') else [])}}}}
    def test_successor_template_updates_only_policy_scope(self):
        old, new = '00000000-0000-4000-8000-000000000001', '11111111-1111-4000-8000-000000000002'
        for target in r.TARGETS:
            template = r.successor_template(self.owner(target), target, old, new, 'ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:' + 'a' * 64)
            env = r.env_map(template['spec']['containers'][0])
            for n, prefix in enumerate(target['prefixes']):
                self.assertEqual(env[prefix + '_SERVICE_ROOT_ID'], old)
                self.assertTrue(env[prefix + '_SERVICE_ROOT_STATE'].endswith(new[:12] + '.json'))
            volume = next(x for x in template['spec']['volumes'] if x['name'] == target['volume'])
            self.assertEqual(volume['configMap']['name'], target['new_config'].format(suffix=new[:12]))
            self.assertEqual(template['metadata']['annotations']['rtk.realtek.com/service-root-policy'], old)
            self.assertEqual(template['metadata']['annotations']['rtk.realtek.com/service-root-overlap'], new)
            if target.get('bundle_volume'):
                bundle = next(x for x in template['spec']['volumes'] if x['name'] == target['bundle_volume'])
                self.assertEqual(bundle['configMap']['name'], target['new_bundle_config'].format(suffix=new[:12]))

if __name__ == '__main__': unittest.main()
