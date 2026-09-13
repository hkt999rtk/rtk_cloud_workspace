import importlib.util
from pathlib import Path
import unittest
spec=importlib.util.spec_from_file_location('service_root_overlap',Path(__file__).with_name('service_root_overlap.py'))
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)
class OverlapTests(unittest.TestCase):
 def root(self,identifier): return {'issuer_id':identifier,'environment':'dev','trust_domain':'service','kind':'root','certificate_pem':'-----BEGIN CERTIFICATE-----\nX\n-----END CERTIFICATE-----\n'}
 def test_overlap_configs_are_immutable_and_scoped(self):
  old,new=self.root('00000000-0000-4000-8000-000000000001'),self.root('00000000-0000-4000-8000-000000000002')
  configs=r.overlap_configs(old,new);self.assertEqual([x['metadata']['namespace'] for x in configs],[r.NS,r.NS,r.AM_NS]);self.assertTrue(all(x['immutable'] for x in configs));self.assertTrue(all('00000000-0000-4000-8000-000000000001' not in x['metadata']['name'] for x in configs));self.assertIn(old['certificate_pem'].strip(),configs[0]['data']['roots.pem']);self.assertIn(new['certificate_pem'].strip(),configs[0]['data']['roots.pem'])
if __name__=='__main__':unittest.main()
