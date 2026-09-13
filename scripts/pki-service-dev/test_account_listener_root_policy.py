import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('account_listener_root_policy', Path(__file__).with_name('account_listener_root_policy.py'))
r = importlib.util.module_from_spec(spec); spec.loader.exec_module(r)

class AccountListenerRootPolicyTests(unittest.TestCase):
    root = {'issuer_id': '00000000-0000-4000-8000-000000000001', 'certificate_fingerprint_sha256': 'a' * 64}
    def owner(self):
        env = [{'name': n, 'value': v} for n, v in {
            r.PREFIX + '_BUNDLE_MANIFEST':'/bundles.json', r.PREFIX + '_SERVER_CRL_MANIFEST':'/crls.json',
            'PKI_MANAGEMENT_ACCOUNT_LISTEN_ADDR':':8443', 'PKI_MANAGEMENT_ACCOUNT_CLIENT_CA':'/run/pki-root/root.pem',
            'PKI_MANAGEMENT_ACCOUNT_CLIENT_ROOT_SHA256':'a'*64, 'PKI_MANAGEMENT_IDENTITY_STATE':'/var/lib/account-pki/private/identity.json', 'KEPT':'true'}.items()]
        return {'spec': {'replicas': 1, 'template': {'spec': {'containers': [{'name':'app'}, {'name':'pkimanagement','image':'old','env':env,'volumeMounts':[{'name':'management-state','mountPath':'/var/lib/account-pki'},{'name':'management-root','mountPath':'/run/pki-root'}]}], 'volumes':[{'name':'management-state','persistentVolumeClaim':{'claimName':'account-manager-service-identity'}},{'name':'management-root','configMap':{'name':'account-manager-service-root'}}]}}}}
    def test_adds_only_policy_settings(self):
        before=self.owner(); result=r.listener_template(before,self.root); env={x['name']:x['value'] for x in result['spec']['containers'][1]['env']}
        self.assertEqual(env[r.PREFIX+'_SERVICE_ROOT_ID'],self.root['issuer_id']); self.assertEqual(env[r.PREFIX+'_SERVICE_ROOT_STATE'],r.STATE); self.assertEqual(env[r.PREFIX+'_SERVICE_ROOTS'],r.ROOTS); self.assertEqual(env['KEPT'],'true')
    def test_rejects_partial_and_preserves_input(self):
        before=self.owner(); original=copy.deepcopy(before); before['spec']['template']['spec']['containers'][1]['env'].append({'name':r.PREFIX+'_SERVICE_ROOT_ID','value':'old'})
        with self.assertRaises(RuntimeError): r.listener_template(before,self.root)
        self.assertNotEqual(before,original)
        clean=self.owner(); original=copy.deepcopy(clean); r.listener_template(clean,self.root); self.assertEqual(clean,original)
    def test_requires_policy_envelope(self):
        self.assertEqual(r.policy_sha({'policy':{'policy_sha256':'b'*64}}),'b'*64)
        with self.assertRaises(RuntimeError): r.policy_sha({})
    def test_controller_authorization_adds_only_account_manager(self):
        owner={'spec':{'replicas':1,'template':{'spec':{'containers':[{'name':'pki-controller','env':[{'name':'PKI_REQUIRED_CONSUMERS_SERVICE','value':'certissuer,factory-enroll,pki-controller'},{'name':'KEPT','value':'true'}]}]}}}}
        result=r.controller_template(owner); env={x['name']:x['value'] for x in result['spec']['containers'][0]['env']}
        self.assertEqual(env['PKI_REQUIRED_CONSUMERS_SERVICE'],r.CONTROLLER_CONSUMERS); self.assertEqual(env['KEPT'],'true')
        self.assertEqual(r.controller_template({'spec':{'replicas':1,'template':result}}),result)

if __name__ == '__main__': unittest.main()
