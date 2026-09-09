import base64
import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('listener_cleanup', Path(__file__).with_name('listener_cleanup.py'))
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)


class ListenerCleanupTests(unittest.TestCase):
    def test_remove_trust_removes_only_exact_self_ca(self):
        legacy = '-----BEGIN CERTIFICATE-----\nlegacy\n-----END CERTIFICATE-----'
        other = '-----BEGIN CERTIFICATE-----\nother\n-----END CERTIFICATE-----'
        with tempfile.TemporaryDirectory() as directory:
            runner = object.__new__(c.ListenerCleanupRun)
            runner.base = Path(directory)
            c.m.write(runner.base / 'pki/consumers/certissuer/ca.crt', legacy + '\n')
            encoded = base64.b64encode((legacy + '\n' + other + '\n').encode()).decode()
            runner.obj = lambda *_: {'metadata': {'name': 'certissuer-runtime'}, 'data': {'client-ca.crt': encoded}}
            patches = []
            runner.scoped_patch = lambda kind, before, changes: patches.append((kind, before, changes))
            result = runner.remove_trust('certissuer')
            self.assertEqual((result['before_count'], result['after_count']), (2, 1))
            updated = base64.b64decode(patches[0][2][1]['value']).decode()
            self.assertNotIn(legacy, updated)
            self.assertIn(other, updated)

    def test_trust_targets_are_listener_local(self):
        runner = object.__new__(c.ListenerCleanupRun)
        self.assertEqual(runner.trust_target('certissuer'), ('certissuer-runtime', 'client-ca.crt'))
        self.assertEqual(runner.trust_target('pki-controller'), ('pki-controller-tls', 'ca.crt'))


if __name__ == '__main__':
    unittest.main()
