import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('broker_final', Path(__file__).with_name('service_successor_pkibroker_client.py'))
mod = importlib.util.module_from_spec(spec); spec.loader.exec_module(mod)


class PkiBrokerTransitionTemplateTests(unittest.TestCase):
    def owner(self):
        broker = {'name': mod.CONTAINER, 'env': [
            {'name': 'PKI_BROKER_IDENTITY_STATE', 'value': mod.OLD_STATE},
            {'name': 'PKI_BROKER_ROOT_SHA256', 'value': mod.OLD},
            {'name': 'PKI_BROKER_IDENTITY_SERVER_PKI_ROOT_SHA256', 'value': mod.OLD},
            {'name': 'PKI_BROKER_IDENTITY_RENEWAL_SERVER_PKI_ROOT_SHA256', 'value': mod.OLD},
        ]}
        mqtt = {'name': 'mqtt', 'env': [{'name': 'MQTT_HOST_IDENTITY_STATE', 'value': '/keep'}]}
        return {'spec': {'template': {'spec': {'containers': [mqtt, broker], 'volumes': [
            {'name': 'pki-service-root', 'configMap': {'name': 'pki-service-host-root'}},
        ]}}}}

    def test_transition_updates_only_broker_client_and_shared_overlap_bundle(self):
        before = self.owner()
        transition = mod.transition_template(copy.deepcopy(before), Path('/tmp/r2-pkibroker-transition'))
        values = mod.env_map(mod.broker_container(transition))
        self.assertEqual(values['PKI_BROKER_IDENTITY_STATE']['value'], mod.NEW_STATE)
        self.assertEqual(values['PKI_BROKER_ROOT_SHA256']['value'], mod.ROOT)
        self.assertEqual(values['PKI_BROKER_IDENTITY_SERVER_PKI_ROOT_SHA256']['value'], mod.OLD)
        self.assertEqual(values['PKI_BROKER_IDENTITY_RENEWAL_SERVER_PKI_ROOT_SHA256']['value'], mod.ROOT)
        self.assertEqual(values['PKI_BROKER_IDENTITY_TRANSITION_FROM_STATE']['value'], mod.OLD_STATE)
        self.assertEqual(mod.broker_container(transition)['image'], mod.IMAGE)
        self.assertEqual(transition['spec']['volumes'][0]['configMap']['name'], mod.ROOT_CONFIGMAP)
        self.assertEqual(transition['spec']['containers'][0], before['spec']['template']['spec']['containers'][0])

    def test_steady_template_removes_only_one_time_transition_settings(self):
        transition = mod.transition_template(self.owner(), Path('/tmp/r2-pkibroker-transition'))
        steady = mod.steady_template({'spec': {'template': transition}}, Path('/tmp/r2-pkibroker-steady'))
        values = mod.env_map(mod.broker_container(steady))
        self.assertNotIn('PKI_BROKER_IDENTITY_TRANSITION_FROM_STATE', values)
        self.assertNotIn('PKI_BROKER_IDENTITY_TRANSITION_FROM_ROOT_SHA256', values)
        self.assertEqual(values['PKI_BROKER_ROOT_SHA256']['value'], mod.ROOT)
        self.assertEqual(values['PKI_BROKER_IDENTITY_SERVER_PKI_ROOT_SHA256']['value'], mod.OLD)

    def test_replacement_matches_the_installed_leaf(self):
        before = {'fingerprint': 'old', 'public_key_sha256': 'old-key'}
        after = {'fingerprint': 'new', 'public_key_sha256': 'new-key'}
        predecessor = {'fingerprint': 'old', 'issuer_id': 'old-issuer', 'subject': mod.SUBJECT, 'caller': mod.SUBJECT, 'status': 'succeeded'}
        successor = {'fingerprint': 'new', 'issuer_id': mod.FINAL, 'subject': mod.SUBJECT, 'caller': mod.SUBJECT, 'status': 'succeeded'}
        self.assertEqual(mod.PkiBrokerFinalClient.replacement(before, after, [predecessor], [predecessor, successor]), successor)


if __name__ == '__main__':
    unittest.main()
