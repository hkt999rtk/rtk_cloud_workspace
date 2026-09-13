import importlib.util
from pathlib import Path
import unittest
spec=importlib.util.spec_from_file_location('activate',Path(__file__).with_name('service_root_activate.py'))
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)
class ActivationTests(unittest.TestCase):
 def test_receipt_query_is_exact_successor_scope(self):
  root={'issuer_id':'00000000-0000-4000-8000-000000000001','trust_bundle_version':'a'*64}
  query=r.receipt_query(root)
  self.assertIn(root['issuer_id'],query);self.assertIn(root['trust_bundle_version'],query);self.assertIn('ORDER BY consumer_id',query);self.assertEqual(sorted(r.REQUIRED), ['certissuer','pki-controller'])
if __name__=='__main__':unittest.main()
