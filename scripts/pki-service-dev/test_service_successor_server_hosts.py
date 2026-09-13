import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('server_hosts', Path(__file__).with_name('service_successor_server_hosts.py'))
mod = importlib.util.module_from_spec(spec); spec.loader.exec_module(mod)


def owner(target):
    prefix = target['prefix']
    env = [
        {'name': prefix + '_HOST_IDENTITY_STATE', 'value': target['old_state']},
        {'name': prefix + '_HOST_ROOT_SHA256', 'value': mod.OLD},
        {'name': prefix + '_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256', 'value': mod.ROOT},
    ]
    if target.get('listener_client_root'):
        env.append({'name': target['listener_client_root'], 'value': mod.OLD})
    return {'spec': {'template': {'metadata': {}, 'spec': {'containers': [{
        'name': target['container'], 'image': mod.IMAGE, 'env': [
            *env,
        ]
    }]}}}}


class ServerTransitionTemplateTests(unittest.TestCase):
    def test_existing_successor_gets_one_signal_and_missing_state_bootstraps(self):
        self.assertEqual(mod.renewal_action({'fingerprint': 'a' * 64}), 'SIGUSR2')
        self.assertEqual(mod.renewal_action(None), 'startup_bootstrap')

    def test_transition_is_distinct_and_steady_removes_only_bootstrap(self):
        for target in mod.TARGETS.values():
            with self.subTest(target=target['deployment']):
                initial = owner(target)
                transition = mod.transition_template(copy.deepcopy(initial), target, Path('/tmp/r2'))
                env = mod.environment(transition['spec']['containers'][0])
                prefix = target['prefix']
                self.assertEqual(env[prefix + '_HOST_IDENTITY_STATE']['value'], target['new_state'])
                self.assertEqual(env[prefix + '_HOST_ROOT_SHA256']['value'], mod.ROOT)
                self.assertEqual(env[prefix + '_HOST_IDENTITY_TRANSITION_FROM_STATE']['value'], target['old_state'])
                self.assertEqual(env[prefix + '_HOST_IDENTITY_TRANSITION_FROM_ROOT_SHA256']['value'], mod.OLD)
                if target.get('listener_client_root'):
                    self.assertEqual(env[target['listener_client_root']]['value'], mod.ROOT)
                steady = mod.steady_template({'spec': {'template': transition}}, target, Path('/tmp/r2'))
                steady_env = mod.environment(steady['spec']['containers'][0])
                self.assertNotIn(prefix + '_HOST_IDENTITY_TRANSITION_FROM_STATE', steady_env)
                self.assertNotIn(prefix + '_HOST_IDENTITY_TRANSITION_FROM_ROOT_SHA256', steady_env)
                self.assertEqual(steady_env[prefix + '_HOST_IDENTITY_STATE']['value'], target['new_state'])

    def test_rejects_an_unreviewed_image(self):
        target = mod.TARGETS['pki-controller']
        initial = owner(target)
        initial['spec']['template']['spec']['containers'][0]['image'] = 'other'
        with self.assertRaises(RuntimeError):
            mod.transition_template(initial, target, Path('/tmp/r2'))


if __name__ == '__main__':
    unittest.main()
