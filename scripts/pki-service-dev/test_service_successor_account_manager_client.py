import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('account_final', Path(__file__).with_name('service_successor_account_manager_client.py'))
mod = importlib.util.module_from_spec(spec); spec.loader.exec_module(mod)


class TransitionTemplateTests(unittest.TestCase):
    def test_only_issuer_and_host_renewal_verification_move_to_successor(self):
        env = [
            {'name': 'PKI_MANAGEMENT_IDENTITY_ROOT_SHA256', 'value': mod.ROOT},
            {'name': 'PKI_MANAGEMENT_ISSUER_SERVER_PKI_ROOT_SHA256', 'value': mod.OLD},
            {'name': 'PKI_MANAGEMENT_CONTROLLER_SERVER_PKI_ROOT_SHA256', 'value': mod.OLD},
            {'name': 'PKI_MANAGEMENT_ACCOUNT_HOST_ROOT_SHA256', 'value': mod.OLD},
            {'name': 'PKI_MANAGEMENT_ACCOUNT_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256', 'value': mod.OLD},
        ]
        owner = {'spec': {'template': {'spec': {'containers': [
            {'name': 'app', 'image': 'api'},
            {'name': 'pkimanagement', 'image': 'ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:' + '1' * 64,
             'env': env},
        ]}}}}
        runner = object.__new__(mod.AccountManagerFinalClient)
        runner.output = Path('/tmp/r2-account-manager-test')
        runner.obj = lambda kind, name, namespace: {
            'immutable': True, 'data': {'root.pem': '-----BEGIN CERTIFICATE-----\na\n-----END CERTIFICATE-----\n'
                                                    '-----BEGIN CERTIFICATE-----\nb\n-----END CERTIFICATE-----\n'}}
        template = runner.transition_template(copy.deepcopy(owner))
        container, values = runner.environment(template)
        self.assertEqual(container['image'], mod.OWNER)
        self.assertEqual(values['PKI_MANAGEMENT_ISSUER_SERVER_PKI_ROOT_SHA256']['value'], mod.ROOT)
        self.assertEqual(values['PKI_MANAGEMENT_ACCOUNT_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256']['value'], mod.ROOT)
        self.assertEqual(values['PKI_MANAGEMENT_CONTROLLER_SERVER_PKI_ROOT_SHA256']['value'], mod.OLD)
        self.assertEqual(values['PKI_MANAGEMENT_ACCOUNT_HOST_ROOT_SHA256']['value'], mod.OLD)


class PreflightStabilityTests(unittest.TestCase):
    def test_transient_503_requires_three_following_successes(self):
        runner = object.__new__(mod.AccountManagerFinalClient)
        runner.report = {}
        runner.save = lambda *_: None
        calls = []
        original, sleep = mod.r.ServiceRun.preflight, mod.time.sleep

        def preflight(_):
            calls.append(None)
            if len(calls) <= 2:
                raise RuntimeError('HTTP /platform/pki/issuers/example/crl: status 503, expected 200')

        try:
            mod.r.ServiceRun.preflight = preflight
            mod.time.sleep = lambda _: None
            runner.preflight_retry()
        finally:
            mod.r.ServiceRun.preflight, mod.time.sleep = original, sleep

        self.assertEqual(len(calls), 5)
        self.assertEqual(runner.report['control_plane_preflight'], {
            'consecutive_successes': 3,
            'attempts': 5,
            'transient_503_recovered': True,
        })

    def test_three_complete_preflights_do_not_share_a_recovery_deadline(self):
        runner = object.__new__(mod.AccountManagerFinalClient)
        runner.report = {}
        runner.save = lambda *_: None
        original, sleep = mod.r.ServiceRun.preflight, mod.time.sleep
        now = [0]

        def preflight(_):
            now[0] += 61

        def monotonic():
            return now[0]

        original_monotonic = mod.time.monotonic
        try:
            mod.r.ServiceRun.preflight = preflight
            mod.time.sleep = lambda _: None
            mod.time.monotonic = monotonic
            runner.preflight_retry()
        finally:
            mod.r.ServiceRun.preflight, mod.time.sleep = original, sleep
            mod.time.monotonic = original_monotonic

        self.assertEqual(runner.report['control_plane_preflight']['attempts'], 3)
        self.assertFalse(runner.report['control_plane_preflight']['transient_503_recovered'])


if __name__ == '__main__':
    unittest.main()
