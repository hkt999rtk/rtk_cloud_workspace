import importlib.util
from pathlib import Path
import unittest
import tempfile
from unittest.mock import Mock

spec = importlib.util.spec_from_file_location('service_retirement', Path(__file__).with_name('retirement.py'))
r = importlib.util.module_from_spec(spec)
spec.loader.exec_module(r)


class RetirementTests(unittest.TestCase):
    def test_rollout_resume_preserves_identity_and_refuses_runtime_drift(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory)
            template = {'spec': {'containers': [{'name': 'certissuer', 'image': 'pinned'}]}}
            for name, value in [('report.json', {'status': 'failed'}),
                                ('after-' + r.NS + '-certissuer-deployment.json', {'spec': {'template': template}}),
                                ('identity-before.json', {'sha256': 'original'})]:
                r.m.write(source / name, value)
            runner = object.__new__(r.RetirementRun)
            runner.args = Mock(resume=directory, image='pinned')
            runner.report = {}
            runner.hierarchy = Mock(return_value=({'issuer_id': 'root'}, {'issuer_id': 'intermediate'}))
            runner.selected = Mock(return_value=(source, {}, {'crl_sha256': 'published'}))
            runner.api = Mock(return_value={'crl_sha256': 'published'})
            runner.obj = Mock(return_value={'spec': {'template': template}})
            runner.unchanged = Mock()
            runner.state_digest = Mock(return_value='original')
            runner.complete_rollout = Mock()
            runner.create = Mock()
            runner.scoped_patch = Mock()
            runner.configure('certissuer', resume=True)
            runner.complete_rollout.assert_called_once()
            runner.create.assert_not_called()
            runner.scoped_patch.assert_not_called()
            runner.complete_rollout.reset_mock()
            runner.state_digest.return_value = 'changed'
            with self.assertRaisesRegex(RuntimeError, 'identity changed'):
                runner.configure('certissuer', resume=True)
            runner.complete_rollout.assert_not_called()
            runner.obj.return_value = {'spec': {'template': {}}}
            with self.assertRaisesRegex(RuntimeError, 'differs from saved'):
                runner.configure('certissuer', resume=True)

    def test_only_replaced_leaf_with_admitted_installed_successor_can_be_selected(self):
        original = {'fingerprint': 'old', 'status': 'succeeded', 'revoked_at': None}
        successor = dict(original, fingerprint='current')
        current = {'fingerprint': 'current', 'pending': False, 'subject': 'service:account-manager'}
        self.assertEqual(r.retirement_target(original, current, [original, successor]), original)
        for changed in (dict(current, fingerprint='old'), dict(current, pending=True), dict(current, subject='service:other')):
            with self.assertRaises(RuntimeError): r.retirement_target(original, changed, [original, successor])
        for rows in ([original], [original, successor, successor], [original, dict(successor, revoked_at='now')], [dict(original, status='issuing'), successor]):
            with self.assertRaises(RuntimeError): r.retirement_target(original, current, rows)


if __name__ == '__main__': unittest.main()
