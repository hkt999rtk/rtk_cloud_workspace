import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location('api_controller_lifecycle',
                                               Path(__file__).with_name('api_controller_lifecycle.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class APIControllerLifecycleTests(unittest.TestCase):
    def state(self, **changes):
        state = {'subject': m.SUBJECT, 'fingerprint': 'current', 'pending': False,
                 'root_sha256': 'root', 'public_key_sha256': 'current-key', 'state_sha256': 'current-state'}
        state.update(changes)
        return state

    def row(self, fingerprint, **changes):
        row = {'request_id': 'request-' + fingerprint, 'issuer_id': 'issuer', 'subject': m.SUBJECT,
               'caller': m.SUBJECT, 'status': 'succeeded', 'fingerprint': fingerprint, 'revoked_at': None}
        row.update(changes)
        return row

    def test_current_and_stale_selects_only_uninstalled_active_rows(self):
        current, stale = m.current_and_stale([self.row('old'), self.row('current'),
                                              self.row('retired', revoked_at='now')], self.state())
        self.assertEqual(current['fingerprint'], 'current')
        self.assertEqual([row['fingerprint'] for row in stale], ['old'])

    def test_current_and_stale_rejects_missing_or_duplicate_installed_admission(self):
        for rows in ([self.row('old')], [self.row('current'), self.row('current')]):
            with self.assertRaises(RuntimeError):
                m.current_and_stale(rows, self.state())

    def test_replacement_requires_exactly_one_successor_with_new_key(self):
        before = self.state(fingerprint='old', public_key_sha256='old-key', state_sha256='old-state')
        after = self.state(fingerprint='new', public_key_sha256='new-key', state_sha256='new-state')
        old, new = self.row('old'), self.row('new')
        issuer = {'issuer_id': 'issuer'}
        self.assertEqual(m.replacement(before, after, [old], [old, new], issuer), new)
        for rows in ([new], [old, new, new], [old, self.row('new', caller='provisioner')]):
            with self.assertRaises(RuntimeError):
                m.replacement(before, after, [old], rows, issuer)


if __name__ == '__main__':
    unittest.main()
