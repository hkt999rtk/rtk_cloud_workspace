import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('openbao_final', Path(__file__).with_name('service_successor_openbao_client.py'))
mod = importlib.util.module_from_spec(spec); spec.loader.exec_module(mod)


class OpenBaoTransitionTemplateTests(unittest.TestCase):
    def owner(self):
        client = {'name': mod.CLIENT, 'image': 'old', 'env': [
            {'name': 'OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_STATE', 'value': mod.OLD_STATE},
            {'name': 'OPENBAO_PKI_SERVICE_CLIENT_ROOT_SHA256', 'value': mod.OLD},
            {'name': 'OPENBAO_PKI_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256', 'value': mod.OLD},
        ]}
        main = {'name': 'openbao', 'image': 'old', 'env': []}
        init = {'name': 'openbao-pki-install', 'image': 'old', 'env': []}
        return {'spec': {'template': {'spec': {'containers': [main, client], 'initContainers': [init], 'volumes': [
            {'name': 'service-root', 'configMap': {'name': mod.OLD_ROOT_CONFIGMAP}},
        ]}}}}

    def test_transition_preserves_host_identity_and_updates_only_client_transport(self):
        before = self.owner()
        transition = mod.transition_template(copy.deepcopy(before), Path('/tmp/r2-openbao-transition'))
        values = mod.env_map(mod.client_container(transition))
        self.assertEqual(values['OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_STATE']['value'], mod.NEW_STATE)
        self.assertEqual(values['OPENBAO_PKI_SERVICE_CLIENT_ROOT_SHA256']['value'], mod.ROOT)
        self.assertEqual(values['OPENBAO_PKI_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256']['value'], mod.ROOT)
        self.assertEqual(values['OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_TRANSITION_FROM_STATE']['value'], mod.OLD_STATE)
        self.assertEqual(transition['spec']['volumes'][0]['configMap']['name'], mod.ROOT_CONFIGMAP)
        self.assertEqual([c['image'] for c in transition['spec']['containers']], [mod.IMAGE, mod.IMAGE])
        self.assertEqual(transition['spec']['initContainers'][0]['image'], mod.IMAGE)

    def test_steady_template_removes_transition_without_moving_host_identity(self):
        transition = mod.transition_template(self.owner(), Path('/tmp/r2-openbao-transition'))
        steady = mod.steady_template({'spec': {'template': transition}}, Path('/tmp/r2-openbao-steady'))
        values = mod.env_map(mod.client_container(steady))
        self.assertNotIn('OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_TRANSITION_FROM_STATE', values)
        self.assertNotIn('OPENBAO_PKI_SERVICE_CLIENT_IDENTITY_TRANSITION_FROM_ROOT_SHA256', values)
        self.assertEqual(values['OPENBAO_PKI_SERVICE_CLIENT_ROOT_SHA256']['value'], mod.ROOT)
        self.assertEqual(values['OPENBAO_PKI_HOST_RENEWAL_SERVER_PKI_ROOT_SHA256']['value'], mod.ROOT)

    def test_replacement_matches_installed_client_leaf(self):
        before = {'fingerprint': 'old', 'public_key_sha256': 'old-key'}
        after = {'fingerprint': 'new', 'public_key_sha256': 'new-key'}
        previous = {'fingerprint': 'old', 'issuer_id': 'old-issuer', 'subject': mod.SUBJECT, 'caller': mod.SUBJECT, 'status': 'succeeded'}
        successor = {'fingerprint': 'new', 'issuer_id': mod.FINAL, 'subject': mod.SUBJECT, 'caller': mod.SUBJECT, 'status': 'succeeded'}
        self.assertEqual(mod.OpenBaoFinalClient.replacement(before, after, [previous], [previous, successor]), successor)


if __name__ == '__main__':
    unittest.main()
