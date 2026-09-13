import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('service_root_successor', Path(__file__).with_name('service_root_successor.py'))
r = importlib.util.module_from_spec(spec); spec.loader.exec_module(r)

class ServiceRootSuccessorTests(unittest.TestCase):
    def root(self, identifier='00000000-0000-4000-8000-000000000001', status='active'):
        return {'issuer_id': identifier, 'environment': 'dev', 'trust_domain': 'service', 'kind': 'root', 'status': status}
    def test_requires_exactly_one_active_service_root(self):
        self.assertEqual(r.current_root([self.root()])['issuer_id'], self.root()['issuer_id'])
        with self.assertRaises(RuntimeError): r.current_root([])
        with self.assertRaises(RuntimeError): r.current_root([self.root(), self.root('00000000-0000-4000-8000-000000000002')])
        with self.assertRaises(RuntimeError): r.current_root([self.root(status='retiring')])

if __name__ == '__main__': unittest.main()
