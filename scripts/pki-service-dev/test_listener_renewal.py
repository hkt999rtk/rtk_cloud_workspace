import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('listener_renewal', Path(__file__).with_name('listener_renewal.py'))
r = importlib.util.module_from_spec(spec)
spec.loader.exec_module(r)


class ListenerRenewalTests(unittest.TestCase):
    def test_added_row_requires_one_current_managed_caller_receipt(self):
        old = {'request_id': 'old', 'fingerprint': 'old'}
        identity = {'fingerprint': 'new'}
        issuer = {'issuer_id': 'issuer'}
        added = {'request_id': 'new', 'fingerprint': 'new', 'issuer_id': 'issuer',
                 'status': 'succeeded', 'revoked_at': None, 'caller': 'service:certissuer'}
        self.assertEqual(r.added_row([old], [old, added], identity, issuer, 'client', 'certissuer'), added)
        for rows in ([added], [old, added, added], [old, dict(added, caller='legacy')],
                     [old, dict(added, revoked_at='now')], [old, dict(added, fingerprint='other')]):
            with self.assertRaises(RuntimeError):
                r.added_row([old], rows, identity, issuer, 'client', 'certissuer')


if __name__ == '__main__':
    unittest.main()
