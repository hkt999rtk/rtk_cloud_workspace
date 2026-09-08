import base64
import datetime as dt
import http.server
import importlib.util
from pathlib import Path
import threading
import tempfile
import json
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location('service_crl', Path(__file__).with_name('crl.py'))
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)


class CRLTests(unittest.TestCase):
    def test_refresh_preserves_revocations_and_increments_number(self):
        pem = '-----BEGIN X509 CRL-----\n' + base64.b64encode(b'fixture').decode() + '\n-----END X509 CRL-----\n'
        old = {'issuer_id': 'root', 'crl_number': '9', 'crl_pem': pem, 'crl_sha256': c.der_digest(pem), 'this_update': '2026-09-08T00:00:00Z'}
        issuer = {'issuer_id': 'root', 'certificate_fingerprint_sha256': 'pin'}
        entries = [{'serial_hex': 'abc', 'reason_code': 1, 'revoked_at': old['this_update']}]
        now = dt.datetime(2026, 9, 8, 1, tzinfo=dt.timezone.utc)
        result = c.refresh_request(issuer, old, entries, now)
        self.assertEqual(result['crl_number'], '10')
        self.assertEqual(result['revocations'], entries)
        self.assertEqual(result['next_update'], '2026-09-11T01:00:00Z')
        with self.assertRaises(RuntimeError): c.refresh_request(issuer, dict(old, crl_sha256='changed'), entries, now)
        with self.assertRaises(RuntimeError): c.refresh_request(issuer, old, entries, now - dt.timedelta(days=1))

    def test_recovery_accepts_only_saved_prior_or_exact_desired(self):
        prior = {'issuer_id': 'root', 'crl_number': '1', 'crl_sha256': 'old'}
        desired = dict(prior, crl_number='2', crl_sha256='new')
        self.assertEqual(c.publication_state(prior, prior, desired), 'pending')
        self.assertEqual(c.publication_state(desired, prior, desired), 'published')
        for current in (dict(desired, crl_sha256='concurrent'), dict(desired, issuer_id='other'), dict(desired, crl_number='3')):
            with self.assertRaises(RuntimeError): c.publication_state(current, prior, desired)
        with self.assertRaises(RuntimeError): c.publication_state(prior, prior, dict(desired, crl_number='0'))

    def test_response_loss_forwards_once_and_client_observes_eof(self):
        requests = []
        class Upstream(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_): pass
            def do_POST(self):
                requests.append((self.path, self.rfile.read(int(self.headers['Content-Length'])), self.headers['Authorization']))
                self.send_response(200); self.end_headers(); self.wfile.write(b'{"published":true}')
        server = http.server.HTTPServer(('127.0.0.1', 0), Upstream)
        thread = threading.Thread(target=server.handle_request); thread.start()
        try:
            result = c.drop_response(server.server_port, '/v1/platform/pki/issuers/test/crl', b'{"crl_pem":"fixture"}', 'fixture-token')
            self.assertEqual(result, {'forwarded': 1, 'response_lost': True, 'upstream_status': 200})
            self.assertEqual(requests, [('/v1/platform/pki/issuers/test/crl', b'{"crl_pem":"fixture"}', 'Bearer fixture-token')])
        finally:
            thread.join(5); server.server_close()

    def test_altered_signed_manifest_fails_before_publication(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory)
            issuer = {'issuer_id': 'root', 'kind': 'root', 'certificate_fingerprint_sha256': 'pin'}
            desired = {'issuer_id': 'root', 'crl_number': '2', 'crl_sha256': 'signed', 'this_update': 'now', 'next_update': 'later'}
            request = dict(desired, issuer_fingerprint_sha256='pin', revocations=[])
            request.pop('crl_sha256')
            c.m.write(source / 'issuer.json', issuer)
            c.m.write(source / 'previous.json', {'crl_number': '1', 'crl_pem': 'previous'})
            c.m.write(source / 'signed/revocations.pem', 'signed')
            c.m.write(source / 'request.json', request)
            c.m.write(source / 'request-digest.json', {'sha256': 'expected'})
            manifest = {'request': request, 'request_sha256': 'expected', 'crl_sha256': 'tampered'}
            c.m.write(source / 'signed/public-manifest.json', manifest)
            runner = object.__new__(c.CRLRun)
            runner.inspect_crl = Mock(return_value=desired)
            runner.ceremony_call = Mock(return_value='expected')
            runner.probe = 'fixture'
            runner.save = Mock()
            with patch.object(c.m, 'command', return_value='[]'):
                with self.assertRaisesRegex(RuntimeError, 'signed request changed'):
                    runner.artifact(source, issuer)
            runner.save.assert_not_called()
            manifest['crl_sha256'] = 'signed'
            c.m.write(source / 'signed/public-manifest.json', manifest)
            c.m.write(source / 'desired.json', dict(desired, crl_sha256='changed'))
            with patch.object(c.m, 'command', return_value='[]'):
                with self.assertRaisesRegex(RuntimeError, 'saved signed artifact changed'):
                    runner.artifact(source, issuer)
            runner.save.assert_not_called()

    def test_uncertain_provider_rotation_recovers_without_rotating_again(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / 'failed'
            intermediate = Path(directory) / 'intermediate'
            output = Path(directory) / 'recovery'
            issuer = {'issuer_id': 'selected'}
            mount = 'pki-issuers/service/selected/v1'
            previous = {'crl_sha256': 'before'}
            for name, value in [('issuer.json', issuer), ('previous.json', previous), ('rotation-intent.json', {'mount': mount}), ('provider-before.pem', 'before')]:
                c.m.write(source / name, value)
            c.m.write(intermediate / 'provider-policies.json', {'mount': mount})
            runner = object.__new__(c.CRLRun)
            runner.args = Mock(source=str(source), intermediate=str(intermediate))
            runner.output = output
            runner.hierarchy = Mock(return_value=({}, issuer))
            runner.api = Mock(return_value=previous)
            runner.kube = Mock(return_value='jwt')
            runner.save = lambda name, value: c.m.write(output / name, value)
            runner.artifact, runner.check = Mock(), Mock()
            for current in ('before', 'after'):
                def bao(args, body='', token=None):
                    if 'auth/kubernetes/login' in args: return '{"auth":{"client_token":"token"}}'
                    if mount + '/cert/crl' in args: return json.dumps({'data': {'certificate': current}})
                    if 'auth/token/revoke' in args: return ''
                    self.fail('unexpected provider call: ' + repr(args))
                runner.bao = Mock(side_effect=bao)
                with patch.object(c, 'der_digest', side_effect=lambda pem: pem):
                    if current == 'before':
                        with self.assertRaisesRegex(RuntimeError, 'no blind rotation retry'):
                            runner.prepare_intermediate(True)
                        runner.artifact.assert_not_called()
                    else:
                        runner.prepare_intermediate(True)
                        self.assertEqual((output / 'signed/revocations.pem').read_text(), 'after')
                self.assertEqual(runner.bao.call_args.args[0], ['write', 'auth/token/revoke', '-'])

    def test_crl_maintenance_uses_owner_inspection_after_renewal(self):
        runner = object.__new__(c.CRLRun)
        row = {'fingerprint': 'current', 'status': 'succeeded', 'revoked_at': None}
        runner.rows = Mock(return_value=[dict(row, fingerprint='old'), row])
        with patch.object(c.r.RenewalRun, 'inspect', return_value={'fingerprint': 'current', 'pending': False}):
            self.assertEqual(runner.issuance(), row)

    def test_server_state_dispatch_does_not_inspect_account_manager(self):
        runner = object.__new__(c.CRLRun)
        with patch.object(c.h.h.HostRun, 'state_digest', return_value='server') as host, patch.object(c.h.ManagementRun, 'state_digest', return_value='client') as client:
            self.assertEqual(runner.state_digest('certissuer'), 'server')
            host.assert_called_once_with(runner, 'certissuer'); client.assert_not_called()
            self.assertEqual(runner.state_digest(), 'client')


if __name__ == '__main__': unittest.main()
