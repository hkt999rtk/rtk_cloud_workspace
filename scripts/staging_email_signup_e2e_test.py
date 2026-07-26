#!/usr/bin/env python3

import importlib.util
import pathlib
import unittest


MODULE_PATH = pathlib.Path(__file__).with_name("staging_email_signup_e2e.py")
SPEC = importlib.util.spec_from_file_location("staging_email_signup_e2e", MODULE_PATH)
assert SPEC and SPEC.loader
runner = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runner)


class StagingEmailSignupE2ETest(unittest.TestCase):
    def test_maps_smtp_aliases_to_canonical_runtime_values(self):
        result = runner.canonical_smtp_env({
            "SMTP_SERVER": "mail.example.test",
            "SMTP_PORT": "587",
            "SMTP_EMAIL_ADDR": "no-reply@realtekconnect.com",
            "SMTP_EMAIL_PASSWORD": "secret",
            "SMTP_ENCRYPTION": "STARTTLS",
        }, "https://admin.example.test")
        self.assertEqual(result["AUTH_TOKEN_DELIVERY"], "smtp")
        self.assertEqual(result["SMTP_HOST"], "mail.example.test")
        self.assertEqual(result["SMTP_FROM"], "no-reply@realtekconnect.com")
        self.assertEqual(result["AUTH_TOKEN_BASE_URL"], "https://admin.example.test")
        self.assertNotIn("SMTP_EMAIL_PASSWORD", result)

    def test_rejects_unexpected_sender_and_unsafe_url(self):
        with self.assertRaisesRegex(runner.E2EError, "no-reply"):
            runner.canonical_smtp_env({
                "SMTP_SERVER": "mail.example.test", "SMTP_PORT": "587", "SMTP_EMAIL_ADDR": "other@example.test",
                "SMTP_EMAIL_PASSWORD": "secret", "SMTP_ENCRYPTION": "starttls",
            }, "https://admin.example.test")
        with self.assertRaisesRegex(runner.E2EError, "invalid"):
            runner.public_url("", "Admin Console")
        with self.assertRaisesRegex(runner.E2EError, "starttls"):
            runner.canonical_smtp_env({
                "SMTP_SERVER": "mail.example.test", "SMTP_PORT": "465", "SMTP_EMAIL_ADDR": "no-reply@realtekconnect.com",
                "SMTP_EMAIL_PASSWORD": "secret", "SMTP_ENCRYPTION": "ssl",
            }, "https://admin.example.test")

    def test_generates_a_unique_plus_address(self):
        self.assertEqual(runner.plus_address("imap-test01@example.test", "run-1234"), "imap-test01+run-1234@example.test")
        with self.assertRaisesRegex(runner.E2EError, "plain mailbox"):
            runner.plus_address("imap-test01+existing@example.test", "run-1234")

    def test_run_id_pattern_accepts_generated_timestamp_shape(self):
        self.assertRegex("20260726t182526z-1a2b3c4d", r"^[a-z0-9-]{8,64}$")

    def test_runner_uses_scoped_account_manager_email_deploy(self):
        source = MODULE_PATH.read_text(encoding="utf-8")
        self.assertIn('"account-manager-email-deploy"', source)
        self.assertIn('"--confirm", STACK', source)
        self.assertNotIn('"provision", "--env-root", str(runtime_root), "--deploy"', source)


if __name__ == "__main__":
    unittest.main()
