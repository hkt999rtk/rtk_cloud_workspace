import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('api_final', Path(__file__).with_name('service_successor_video_cloud_api_client.py'))
mod = importlib.util.module_from_spec(spec); spec.loader.exec_module(mod)


class VideoCloudAPITransitionTemplateTests(unittest.TestCase):
    def owner(self):
        return {'spec': {'template': {'spec': {'containers': [{'name': 'app', 'image': 'old-image', 'env': [
            {'name': 'VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_STATE', 'value': mod.OLD_STATE},
            {'name': 'VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_ROOT_SHA256', 'value': mod.OLD},
            {'name': 'VIDEO_CLOUD_ACCOUNT_MANAGER_SERVER_PKI_ROOT_SHA256', 'value': mod.OLD},
            {'name': 'VIDEO_CLOUD_ACCOUNT_MANAGER_RENEWAL_SERVER_PKI_ROOT_SHA256', 'value': mod.OLD},
            {'name': 'VIDEO_CLOUD_ACCOUNT_MANAGER_SERVICE_ROOT_ID', 'value': '697e8e86-5af6-4580-8456-7f91d17634f2'},
            {'name': 'VIDEO_CLOUD_ACCOUNT_MANAGER_RENEWAL_SERVICE_ROOT_ID', 'value': '697e8e86-5af6-4580-8456-7f91d17634f2'},
        ]}]}}}}

    def test_transition_moves_only_client_and_renewal_authority(self):
        transition = mod.api_transition_template(copy.deepcopy(self.owner()), Path('/tmp/r2-api-transition'))
        container = transition['spec']['containers'][0]
        values = mod.env_map(container)
        self.assertEqual(container['image'], mod.IMAGE)
        self.assertEqual(values['VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_STATE']['value'], mod.NEW_STATE)
        self.assertEqual(values['VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_ROOT_SHA256']['value'], mod.ROOT)
        self.assertEqual(values['VIDEO_CLOUD_ACCOUNT_MANAGER_RENEWAL_SERVER_PKI_ROOT_SHA256']['value'], mod.ROOT)
        self.assertEqual(values['VIDEO_CLOUD_ACCOUNT_MANAGER_SERVER_PKI_ROOT_SHA256']['value'], mod.OLD)
        self.assertEqual(values['VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_TRANSITION_FROM_STATE']['value'], mod.OLD_STATE)
        self.assertEqual(values['VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_TRANSITION_FROM_ROOT_SHA256']['value'], mod.OLD)

    def test_steady_template_removes_only_one_time_transition_settings(self):
        transition = mod.api_transition_template(copy.deepcopy(self.owner()), Path('/tmp/r2-api-transition'))
        steady = mod.api_steady_template({'spec': {'template': transition}}, Path('/tmp/r2-api-steady'))
        values = mod.env_map(steady['spec']['containers'][0])
        self.assertNotIn('VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_TRANSITION_FROM_STATE', values)
        self.assertNotIn('VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_TRANSITION_FROM_ROOT_SHA256', values)
        self.assertEqual(values['VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_ROOT_SHA256']['value'], mod.ROOT)
        self.assertEqual(values['VIDEO_CLOUD_ACCOUNT_MANAGER_SERVER_PKI_ROOT_SHA256']['value'], mod.OLD)


if __name__ == '__main__':
    unittest.main()
