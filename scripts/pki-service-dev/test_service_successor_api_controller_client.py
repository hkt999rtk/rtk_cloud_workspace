import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('api_controller_final', Path(__file__).with_name('service_successor_api_controller_client.py'))
mod = importlib.util.module_from_spec(spec); spec.loader.exec_module(mod)


class APIControllerTransitionTemplateTests(unittest.TestCase):
    def owner(self):
        return {'spec': {'template': {'spec': {'containers': [{'name': 'app', 'image': 'old-image', 'env': [
            {'name': 'VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE', 'value': mod.OLD_STATE},
            {'name': 'VIDEO_CLOUD_CONTROLLER_IDENTITY_ROOT_SHA256', 'value': mod.OLD},
            {'name': 'VIDEO_CLOUD_CONTROLLER_IDENTITY_SERVER_PKI_ROOT_SHA256', 'value': mod.OLD},
            {'name': 'VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_SERVER_PKI_ROOT_SHA256', 'value': mod.OLD},
        ]}]}}}}

    def test_transition_keeps_the_predecessor_controller_server_pin(self):
        template = mod.transition_template(copy.deepcopy(self.owner()), Path('/tmp/r2-api-controller-transition'))
        container = template['spec']['containers'][0]
        values = mod.env_map(container)
        self.assertEqual(container['image'], mod.IMAGE)
        self.assertEqual(values['VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE']['value'], mod.NEW_STATE)
        self.assertEqual(values['VIDEO_CLOUD_CONTROLLER_IDENTITY_ROOT_SHA256']['value'], mod.ROOT)
        self.assertEqual(values['VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_SERVER_PKI_ROOT_SHA256']['value'], mod.ROOT)
        self.assertEqual(values['VIDEO_CLOUD_CONTROLLER_IDENTITY_SERVER_PKI_ROOT_SHA256']['value'], mod.OLD)

    def test_steady_template_removes_only_one_time_transition_settings(self):
        transition = mod.transition_template(copy.deepcopy(self.owner()), Path('/tmp/r2-api-controller-transition'))
        steady = mod.steady_template({'spec': {'template': transition}}, Path('/tmp/r2-api-controller-steady'))
        values = mod.env_map(steady['spec']['containers'][0])
        self.assertNotIn('VIDEO_CLOUD_CONTROLLER_IDENTITY_TRANSITION_FROM_STATE', values)
        self.assertNotIn('VIDEO_CLOUD_CONTROLLER_IDENTITY_TRANSITION_FROM_ROOT_SHA256', values)
        self.assertEqual(values['VIDEO_CLOUD_CONTROLLER_IDENTITY_ROOT_SHA256']['value'], mod.ROOT)
        self.assertEqual(values['VIDEO_CLOUD_CONTROLLER_IDENTITY_SERVER_PKI_ROOT_SHA256']['value'], mod.OLD)


if __name__ == '__main__':
    unittest.main()
