import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location(
    'finalize', Path(__file__).with_name('service_root_finalize.py'))
f = importlib.util.module_from_spec(spec)
spec.loader.exec_module(f)


class ServiceRootFinalizeTest(unittest.TestCase):
    def test_reconciled_settings_uses_live_values(self):
        self.assertEqual(f.reconciled_settings({'ROOT': 'old', 'GATE': 'old'},
                                               {'ROOT': 'new', 'GATE': 'five'}),
                         {'ROOT': 'new', 'GATE': 'five'})

    def test_reconciled_settings_rejects_absent_setting(self):
        with self.assertRaisesRegex(RuntimeError, 'absent from live'):
            f.reconciled_settings({'ROOT': 'old'}, {})


if __name__ == '__main__':
    unittest.main()
