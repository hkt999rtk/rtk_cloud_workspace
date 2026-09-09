import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('listener_retirement', Path(__file__).with_name('listener_retirement.py'))
r = importlib.util.module_from_spec(spec)
spec.loader.exec_module(r)


class ListenerRetirementTests(unittest.TestCase):
    def test_target_revocation_state_requires_exact_row_and_expected_state(self):
        runner = object.__new__(r.ListenerRetirementRun)
        target = {'listener': 'certissuer', 'kind': 'client', 'fingerprint': 'old'}
        row = {'fingerprint': 'old', 'revoked_at': None}
        runner.full_client_rows = lambda _: [row]
        runner.full_server_rows = lambda _: []
        runner.require_target_revocation_state([target], False)
        row['revoked_at'] = 'now'
        runner.require_target_revocation_state([target], True)
        with self.assertRaises(RuntimeError):
            runner.require_target_revocation_state([target], False)
        runner.full_client_rows = lambda _: [row, dict(row)]
        with self.assertRaises(RuntimeError):
            runner.require_target_revocation_state([target], True)


if __name__ == '__main__':
    unittest.main()
