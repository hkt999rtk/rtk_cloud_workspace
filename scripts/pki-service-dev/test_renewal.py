import importlib.util
from pathlib import Path
import unittest
from unittest.mock import Mock

spec = importlib.util.spec_from_file_location('managed_renewal', Path(__file__).with_name('renewal.py'))
r = importlib.util.module_from_spec(spec)
spec.loader.exec_module(r)


class RenewalTests(unittest.TestCase):
    def test_registry_selection_uses_installed_fingerprint_after_renewal(self):
        runner = object.__new__(r.RenewalRun)
        old = {'fingerprint': 'old', 'status': 'succeeded', 'revoked_at': None}
        new = dict(old, fingerprint='new')
        runner.rows = Mock(return_value=[old, new])
        runner.inspect = Mock(return_value={'fingerprint': 'new', 'pending': False})
        self.assertEqual(runner.issuance(), new)
        runner.inspect.return_value = {'fingerprint': 'old', 'pending': False}
        self.assertEqual(runner.issuance(), old)
        for info in ({'fingerprint': 'unknown', 'pending': False}, {'fingerprint': 'new', 'pending': True}):
            runner.inspect.return_value = info
            with self.assertRaises(RuntimeError): runner.issuance()

    def test_replacement_requires_new_key_and_exactly_one_successful_receipt(self):
        before = {'subject': 'service:account-manager', 'root_sha256': 'root', 'fingerprint': 'old', 'public_key_sha256': 'old-key', 'state_sha256': 'old-state', 'pending': False}
        after = dict(before, fingerprint='new', public_key_sha256='new-key', state_sha256='new-state')
        original = {'fingerprint': 'old', 'status': 'succeeded', 'revoked_at': None, 'caller': 'provisioner'}
        added = dict(original, fingerprint='new', caller='service:account-manager')
        self.assertEqual(r.replacement(before, after, [original], [original, added]), added)
        for field, value in [('pending', True), ('root_sha256', 'wrong'), ('public_key_sha256', 'old-key'), ('fingerprint', 'old')]:
            with self.assertRaises(RuntimeError): r.replacement(before, dict(after, **{field: value}), [original], [original, added])
        for rows in ([added], [original, added, added], [original, dict(added, status='issuing')], [original, dict(added, revoked_at='now')], [original, dict(added, caller='provisioner')]):
            with self.assertRaises(RuntimeError): r.replacement(before, after, [original], rows)


if __name__ == '__main__': unittest.main()
