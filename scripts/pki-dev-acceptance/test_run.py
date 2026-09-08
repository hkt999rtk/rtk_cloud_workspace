import importlib.util
import io
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('acceptance', Path(__file__).with_name('run.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class AcceptanceTests(unittest.TestCase):
    def test_device_gate_survives_domain_policy_adoption(self):
        legacy = {'PKI_REQUIRED_CONSUMERS': 'video-cloud-api,pkibroker'}
        domains = {'PKI_REQUIRED_CONSUMERS_DEVICE': 'video-cloud-api,pkibroker',
                   'PKI_REQUIRED_CONSUMERS_SERVICE': 'pki-controller,certissuer'}
        self.assertEqual(m.device_consumers(legacy), m.CONSUMERS)
        self.assertEqual(m.device_consumers(domains), m.CONSUMERS)
        with self.assertRaisesRegex(RuntimeError, 'mixed consumer'):
            m.device_consumers(dict(legacy, **domains))
        self.assertNotEqual(m.device_consumers({'PKI_REQUIRED_CONSUMERS_SERVICE': 'video-cloud-api,pkibroker'}), m.CONSUMERS)

    def test_go_rfc3339_fraction_precision_on_python_39(self):
        for fraction in ('1', '12', '123', '1234', '12345', '123456', '123456789'):
            parsed = m.parse_time('2026-09-08T09:00:00.' + fraction + 'Z')
            self.assertEqual(parsed.microsecond, int(fraction[:6].ljust(6, '0')))
            self.assertEqual(parsed.utcoffset(), m.dt.timedelta(0))
        self.assertEqual(m.parse_time('2026-09-08T09:00:00Z').microsecond, 0)

    def test_expected_plaintext_http_denial_is_not_a_json_error(self):
        runner = object.__new__(m.Acceptance)
        runner.ports = {'am': ('12345', None)}
        error = m.urllib.error.HTTPError('http://127.0.0.1', 409, 'Conflict', {}, io.BytesIO(b'consumer unavailable\n'))
        with patch.object(m.urllib.request, 'build_opener') as opener:
            opener.return_value.open.side_effect = error
            self.assertIsNone(runner.http('/platform/pki/operations/fixture/activate', {}, expected=409))

    def test_resume_refuses_previously_attempted_signing(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / 'run'
            output.mkdir()
            previous = {'status': 'failed', 'environment': 'dev', 'context': 'dev-fixture'}
            m.write(output / 'report.json', previous)
            (output / 'signed').mkdir()
            with self.assertRaisesRegex(RuntimeError, 'signing already attempted'):
                m.Acceptance(directory, 'dev-fixture', output, resume=True)
            self.assertEqual(m.read(output / 'report.json'), previous)

    def test_private_write_replaces_symlink_without_touching_target(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'original'
            target.write_text('preserve')
            link = Path(directory) / 'result'
            link.symlink_to(target)
            m.write(link, {'secret': 'fixture'})
            self.assertEqual(target.read_text(), 'preserve')
            self.assertFalse(link.is_symlink())
            self.assertEqual(link.stat().st_mode & 0o777, 0o600)

    def test_capture_failure_does_not_expose_credentials(self):
        with self.assertRaisesRegex(RuntimeError, '^command failed:') as error:
            m.command(['python3', '-c', 'import sys; print("private-token"); sys.exit(1)'])
        self.assertNotIn('private-token', str(error.exception))

    def test_receipt_identity_cannot_inject_sql(self):
        runner = object.__new__(m.Acceptance)
        for issuer, digest in [("x'; DROP TABLE x;--", 'a'*64), ('123e4567-e89b-12d3-a456-426614174000', "' OR TRUE")]:
            with self.assertRaises((ValueError, RuntimeError)):
                runner.receipts(issuer, digest)

    def test_missing_broker_receipt_gate_checks_absence_on_every_poll(self):
        runner = object.__new__(m.Acceptance)
        with patch.object(runner, 'receipts', side_effect=[[], ['pkibroker']]), patch.object(m.time, 'sleep'):
            with self.assertRaisesRegex(RuntimeError, 'unexpected receipt'):
                runner.wait_receipts('issuer', 'digest', ['video-cloud-api'], absent='pkibroker')

    def test_positive_startup_wait_records_transient_denial(self):
        runner = object.__new__(m.Acceptance)
        with patch.object(runner, 'mqtt', side_effect=[RuntimeError('MQTT authorization result differs: 5'), None]), patch.object(m.time, 'sleep'):
            self.assertEqual(runner.wait_positive_mqtt({}, 'device'), 2)

    def test_positive_startup_wait_does_not_hide_protocol_failure(self):
        runner = object.__new__(m.Acceptance)
        with patch.object(runner, 'mqtt', side_effect=RuntimeError('invalid publication')):
            with self.assertRaisesRegex(RuntimeError, 'invalid publication'):
                runner.wait_positive_mqtt({}, 'device')

    def test_process_preserves_back_to_back_buffered_events(self):
        process = m.Process(['python3', '-c', 'print("{}", flush=True); print("{}", flush=True)'])
        try:
            self.assertEqual(process.event(), {})
            self.assertEqual(process.event(), {})
            with self.assertRaisesRegex(RuntimeError, 'ended'):
                process.event()
        finally:
            process.close()

    def test_natural_lease_expiry_cannot_pass_cutoff(self):
        runner = object.__new__(m.Acceptance)
        class Session:
            def event(self, timeout):
                return {'event': 'closed', 'at': '2026-09-08T01:01:00Z'}
        before = m.dt.datetime.fromisoformat('2026-09-08T01:00:55+00:00')
        with self.assertRaisesRegex(RuntimeError, 'natural lease'):
            runner.cutoff(Session(), {'at': '2026-09-08T01:00:00Z'}, before)


if __name__ == '__main__':
    unittest.main()
