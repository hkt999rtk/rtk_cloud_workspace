import importlib.util
from pathlib import Path
import unittest
import tempfile
import copy
from types import SimpleNamespace

spec = importlib.util.spec_from_file_location('service_egress', Path(__file__).with_name('egress.py'))
e = importlib.util.module_from_spec(spec)
spec.loader.exec_module(e)


class EgressTests(unittest.TestCase):
    def owner(self, name):
        prefix = 'CERT_ISSUER_SERVICE_CLIENT' if name == 'certissuer' else 'PKI_SERVICE_CLIENT'
        host = 'CERT_ISSUER_HOST_RENEWAL' if name == 'certissuer' else 'PKI_HOST_RENEWAL'
        return {'spec': {'template': {'spec': {'containers': [{'name': name, 'image': 'old', 'env': [
            {'name': prefix + '_ROOT_SHA256', 'value': 'root'},
            {'name': prefix + '_BUNDLE_MANIFEST', 'value': '/bundle'},
            {'name': prefix + '_PKI_CONTROLLER_URL', 'value': 'https://controller'},
            {'name': prefix + '_MANAGEMENT_CA', 'value': '/run/pki-service-consumer/ca.crt'},
            {'name': prefix + '_MANAGEMENT_CERT', 'value': '/run/pki-service-consumer/tls.crt'},
            {'name': prefix + '_MANAGEMENT_KEY', 'value': '/run/pki-service-consumer/tls.key'},
            {'name': host + '_CLIENT_CERT', 'value': '/run/pki-service-consumer/tls.crt'},
            {'name': host + '_CLIENT_KEY', 'value': '/run/pki-service-consumer/tls.key'},
            {'name': 'UNRELATED', 'value': 'retained'}],
            'volumeMounts': [{'name': 'service-consumer', 'mountPath': '/run/pki-service-consumer', 'readOnly': True},
                             {'name': 'retained', 'mountPath': '/retained'}]}],
            'volumes': [{'name': 'service-consumer', 'secret': {'secretName': e.profile(name)['secret']}},
                        {'name': 'retained', 'emptyDir': {}}]}}}}

    def test_managed_template_removes_only_private_bootstrap_transport(self):
        image = 'ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:' + 'a' * 64
        owner = self.owner('certissuer')
        template = e.managed_template(owner, 'certissuer', image)
        container = template['spec']['containers'][0]
        values = e.env_values(container)
        self.assertEqual(values['CERT_ISSUER_SERVICE_CLIENT_IDENTITY_STATE'], e.STATE)
        self.assertEqual(values['CERT_ISSUER_SERVICE_CLIENT_MANAGEMENT_CA'], e.CA_MOUNT)
        for key in ('CERT_ISSUER_SERVICE_CLIENT_MANAGEMENT_CERT', 'CERT_ISSUER_SERVICE_CLIENT_MANAGEMENT_KEY',
                    'CERT_ISSUER_HOST_RENEWAL_CLIENT_CERT', 'CERT_ISSUER_HOST_RENEWAL_CLIENT_KEY'):
            self.assertNotIn(key, values)
        self.assertEqual(values['UNRELATED'], 'retained')
        self.assertEqual(container['image'], image)
        self.assertEqual([item['name'] for item in container['volumeMounts']], ['retained', 'service-managed-egress-ca'])
        self.assertEqual(template['spec']['volumes'][0], {'name': 'retained', 'emptyDir': {}})
        self.assertEqual(template['spec']['volumes'][1]['configMap']['name'], 'pki-service-managed-egress-certissuer-ca')
        self.assertEqual(owner['spec']['template']['spec']['containers'][0]['image'], 'old')

    def test_managed_template_refuses_missing_or_preexisting_transition_state(self):
        owner = self.owner('pki-controller')
        owner['spec']['template']['spec']['containers'][0]['env'] = [item for item in owner['spec']['template']['spec']['containers'][0]['env']
                                                                       if item['name'] != 'PKI_HOST_RENEWAL_CLIENT_KEY']
        with self.assertRaisesRegex(RuntimeError, 'static egress settings incomplete'):
            e.managed_template(owner, 'pki-controller', 'image')
        owner = self.owner('pki-controller')
        owner['spec']['template']['spec']['containers'][0]['env'].append({'name': 'PKI_SERVICE_CLIENT_IDENTITY_STATE', 'value': e.STATE})
        with self.assertRaisesRegex(RuntimeError, 'already configured'):
            e.managed_template(owner, 'pki-controller', 'image')

    def test_pem_removal_is_exact_and_never_accepts_ambiguous_bundle(self):
        cert = '-----BEGIN CERTIFICATE-----\nlegacy\n-----END CERTIFICATE-----'
        other = '-----BEGIN CERTIFICATE-----\nother\n-----END CERTIFICATE-----'
        self.assertEqual(e.remove_pem(cert + '\n' + other + '\n', cert), other + '\n')
        for bundle in (other + '\n', cert + '\n' + cert + '\n', cert + '\n'):
            with self.assertRaises(RuntimeError):
                e.remove_pem(bundle, cert)

    def test_enrollment_template_changes_only_image_and_exact_policy(self):
        owner = self.owner('certissuer')
        owner['spec']['template']['spec']['containers'][0]['env'].append(
            {'name': 'CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN', 'value': '^service-provisioner$'})
        result = e.enrollment_template(owner, 'new-image', '^certissuer$')
        values = e.env_values(result['spec']['containers'][0])
        self.assertEqual(values['CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN'], '^certissuer$')
        self.assertEqual(values['UNRELATED'], 'retained')
        self.assertEqual(result['spec']['containers'][0]['image'], 'new-image')
        self.assertEqual(owner['spec']['template']['spec']['containers'][0]['image'], 'old')

    def test_enrollment_resolves_only_the_listener_local_database_reference(self):
        self.assertEqual(e.enrollment_database_reference('certissuer'), '$CERT_ISSUER_DB_DSN')
        self.assertEqual(e.enrollment_database_reference('pki-controller'), '${PKI_DATABASE_URL:?}')
        with self.assertRaisesRegex(RuntimeError, 'unknown listener'):
            e.enrollment_database_reference('other')

    def test_enrollment_resolves_only_the_listener_local_environment_reference(self):
        self.assertEqual(e.enrollment_environment_reference('certissuer'), '$VIDEO_CLOUD_ENV')
        self.assertEqual(e.enrollment_environment_reference('pki-controller'), '${PKI_ENVIRONMENT:?}')
        with self.assertRaisesRegex(RuntimeError, 'unknown listener'):
            e.enrollment_environment_reference('other')

    def test_legacy_inbound_ca_source_is_separate_from_egress_secret(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'pki/consumers/certissuer/ca.crt'
            e.m.write(path, 'public-ca')
            self.assertEqual(e.legacy_inbound_ca_path(directory, 'certissuer'), path)
            with self.assertRaisesRegex(RuntimeError, 'saved legacy inbound CA'):
                e.legacy_inbound_ca_path(directory, 'pki-controller')

    def test_recovery_upgrades_exact_saved_image_and_refuses_all_runtime_drift(self):
        owner = self.owner('pki-controller')
        owner['metadata'] = {'uid': 'deployment-uid'}
        owner['spec']['template'] = e.managed_template(owner, 'pki-controller', 'old-image')
        saved = copy.deepcopy(owner)
        baseline = {'deployment_uid': 'deployment-uid', 'pvc_uid': 'pvc-uid'}
        result = e.recovery_template(owner, saved, baseline, 'pvc-uid', 'pki-controller', 'new-image')
        self.assertEqual(result['spec']['containers'][0]['image'], 'new-image')
        self.assertEqual(owner, saved)
        for drift in ('image', 'uid', 'pvc', 'env'):
            changed = copy.deepcopy(owner)
            pvc = 'pvc-uid'
            if drift == 'image':
                changed['spec']['template']['spec']['containers'][0]['image'] = 'unrecorded-image'
            elif drift == 'uid':
                changed['metadata']['uid'] = 'replacement'
            elif drift == 'pvc':
                pvc = 'replacement'
            else:
                changed['spec']['template']['spec']['containers'][0]['env'].append({'name': 'DRIFT', 'value': 'true'})
            with self.assertRaisesRegex(RuntimeError, 'runtime drifted'):
                e.recovery_template(changed, saved, baseline, pvc, 'pki-controller', 'new-image')

    def test_recovery_validates_evidence_before_image_mutation(self):
        for drift in (None, 'configmap', 'state', 'trust'):
            with self.subTest(drift=drift), tempfile.TemporaryDirectory() as directory:
                base = Path(directory)
                failed, enrollment = base / 'failed', base / 'enrollment'
                name = 'pki-controller'
                owner = self.owner(name)
                owner['metadata'] = {'uid': 'deployment-uid'}
                owner['spec']['template'] = e.managed_template(owner, name, 'old-image')
                e.m.write(failed / 'report.json', {'status': 'failed', 'phase': 'controller-adopt'})
                e.m.write(failed / ('after-' + e.NS + '-' + name + '-deployment.json'), owner)
                config = {'immutable': True, 'data': {'ca.crt': 'public-ca'}}
                e.m.write(failed / ('create-' + e.profile(name)['configmap'] + '.json'), config)
                e.m.write(enrollment / 'report.json', {'status': 'passed'})
                e.m.write(enrollment / 'baseline.json', {'deployment_uid': 'deployment-uid', 'pvc_uid': 'pvc-uid'})
                e.m.write(enrollment / 'enrolled.json', {'subject': 'service:pki-controller', 'state_sha256': 'saved-hash'})
                if drift == 'trust':
                    e.m.write(failed / ('before-' + e.NS + '-certissuer-runtime-secret.json'), {})
                runner = object.__new__(e.EgressRun)
                runner.args = SimpleNamespace(failed=str(failed), enrollment=str(enrollment), image='new-image')
                runner.report = {}
                runner.selected = lambda: ({}, {})
                runner.deployment = lambda _: copy.deepcopy(owner)
                runner.obj = lambda kind, _: ({'metadata': {'uid': 'pvc-uid'}} if kind == 'persistentvolumeclaim'
                                             else dict(config, data={'ca.crt': 'drift'}) if drift == 'configmap' else config)
                runner.kube = lambda _: '600\n' + ('changed' if drift == 'state' else 'saved-hash') + '  client.json\n'
                events = []
                runner.apply_template = lambda name, old, template, image: events.append(('apply', template['spec']['containers'][0]['image']))
                runner.complete_adopt = lambda *args: events.append(('complete', args[0]))
                if drift:
                    with self.assertRaises(RuntimeError):
                        runner.resume_adopt(name)
                    self.assertEqual(events, [])
                else:
                    runner.resume_adopt(name)
                    self.assertEqual(events, [('apply', 'new-image'), ('complete', name)])

    def test_persistence_keeps_host_policy_and_unrelated_overrides_without_static_keys(self):
        owner = self.owner('certissuer')
        template = e.managed_template(owner, 'certissuer', 'new-image')
        template['spec']['containers'][0]['env'].extend([
            {'name': 'CERT_ISSUER_GATEWAY_CLIENT_CN_PATTERN', 'value': '^service:(certissuer|pki-controller)$'},
            {'name': 'CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN', 'value': '^$'}])
        existing = {'UNRELATED': 'retained', 'CERT_ISSUER_HOST_RENEWAL_CLIENT_KEY': 'old-key',
                    'CERT_ISSUER_SERVICE_CLIENT_MANAGEMENT_CERT': 'old-cert',
                    'CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN': '^pki-controller$'}
        result = e.persisted_settings(existing, template, 'certissuer')
        self.assertEqual(result['UNRELATED'], 'retained')
        self.assertEqual(result['CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN'], '^$')
        self.assertEqual(result['CERT_ISSUER_GATEWAY_CLIENT_CN_PATTERN'], '^service:(certissuer|pki-controller)$')
        self.assertNotIn('CERT_ISSUER_HOST_RENEWAL_CLIENT_KEY', result)
        self.assertNotIn('CERT_ISSUER_SERVICE_CLIENT_MANAGEMENT_CERT', result)


if __name__ == '__main__':
    unittest.main()
