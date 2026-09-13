import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location(
    'managed', Path(__file__).with_name('service_mqtt_controller_identity.py'))
x = importlib.util.module_from_spec(spec)
spec.loader.exec_module(x)


class ManagedControllerTemplateTest(unittest.TestCase):
    def test_removes_static_key_and_preserves_other_settings(self):
        env = [
            {'name': 'KEEP', 'value': 'yes'},
            {'name': 'VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_STATE',
             'value': '/private/state.json'},
            {'name': 'VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_ROOT_SHA256',
             'value': x.ROOT},
        ] + [{'name': name, 'value': '/private/key'} for name in x.STATIC_SETTINGS]
        owner = {'spec': {'template': {'metadata': {}, 'spec': {
            'containers': [{'name': 'app', 'image': 'old', 'env': env,
                            'volumeMounts': [
                                {'name': 'mqtt-pki-management'},
                                {'name': 'keep'}]}],
            'volumes': [{'name': 'mqtt-pki-management'}, {'name': 'keep'}]}}}}
        image = ('ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:' +
                 'a' * 64)
        result = x.managed_template(owner, image)
        app = result['spec']['containers'][0]
        self.assertEqual(app['image'], image)
        self.assertEqual({entry['name'] for entry in app['env']}, {
            'KEEP', 'VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_STATE',
            'VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_ROOT_SHA256'})
        self.assertEqual([item['name'] for item in app['volumeMounts']], ['keep'])
        self.assertEqual([item['name'] for item in result['spec']['volumes']], ['keep'])


if __name__ == '__main__':
    unittest.main()
