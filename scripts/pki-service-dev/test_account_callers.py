import importlib.util
import json
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('callers', Path(__file__).with_name('account_callers.py'))
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)


class CallerProofTest(unittest.TestCase):
    def test_positive_and_denial_evidence_requires_all_three_results(self):
        for responses, passes in (
            ([(401, {}), (401, {}), (200, {'access_token': 'must-stay-private'})], True),
            ([(200, {'access_token': 'must-stay-private'})], False),
            ([(401, {}), (200, {'access_token': 'must-stay-private'})], False),
            ([(401, {}), (401, {}), (200, {})], False),
            ([(401, {}), (401, {}), (401, {})], False),
        ):
            with self.subTest(responses=responses):
                runner = object.__new__(c.CallerRun)
                runner.app_context, runner.owner_context = object(), object()
                evidence = {}
                runner.check = lambda name, value: evidence.update({name: value})
                with patch.object(c, 'app_request', side_effect=responses) as request:
                    if passes:
                        runner.probe_calls('test-device')
                        self.assertIs(request.call_args_list[-1].args[0], runner.owner_context)
                        self.assertEqual(set(evidence), {'unassigned_device_denied', 'member_without_product_denied', 'assigned_app_token'})
                    else:
                        with self.assertRaises(RuntimeError):
                            runner.probe_calls('test-device')
                        self.assertNotIn('assigned_app_token', evidence)
                self.assertNotIn('must-stay-private', json.dumps(evidence))


if __name__ == '__main__':
    unittest.main()
