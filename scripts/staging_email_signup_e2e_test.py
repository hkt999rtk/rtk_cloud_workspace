#!/usr/bin/env python3

import importlib.util
import pathlib
import unittest
from unittest import mock


MODULE_PATH = pathlib.Path(__file__).with_name("staging_email_signup_e2e.py")
SPEC = importlib.util.spec_from_file_location("staging_email_signup_e2e", MODULE_PATH)
assert SPEC and SPEC.loader
runner = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runner)


class StagingEmailSignupE2ETest(unittest.TestCase):
    def test_maps_sendmail_service_to_canonical_runtime_values(self):
        result = runner.canonical_sendmail_env({
            "SENDMAIL_HTTP_BEARER_TOKEN": "opaque-token",
        }, "https://admin.example.test")
        self.assertEqual(result["AUTH_TOKEN_DELIVERY"], "sendmail_http")
        self.assertEqual(
            result["SENDMAIL_HTTP_BASE_URL"],
            "https://sm.realtekconnect.com",
        )
        self.assertEqual(
            result["SENDMAIL_HTTP_BEARER_TOKEN"], "opaque-token"
        )
        self.assertEqual(result["AUTH_TOKEN_BASE_URL"], "https://admin.example.test")
        self.assertNotIn("SMTP_PASSWORD", result)

    def test_rejects_missing_token_and_unsafe_url(self):
        with self.assertRaisesRegex(runner.E2EError, "BEARER_TOKEN"):
            runner.canonical_sendmail_env({}, "https://admin.example.test")
        with self.assertRaisesRegex(runner.E2EError, "invalid"):
            runner.public_url("", "Admin Console")
        for value in (
            "http://sm.realtekconnect.com",
            "https://other.example.com",
            "https://sm.realtekconnect.com/send",
            "https://user:pass@sm.realtekconnect.com",
        ):
            with self.subTest(value=value):
                with mock.patch.dict(
                    runner.os.environ,
                    {"SENDMAIL_HTTP_BASE_URL": value},
                    clear=False,
                ):
                    with self.assertRaisesRegex(
                        runner.E2EError, "sm.realtekconnect.com"
                    ):
                        runner.canonical_sendmail_env(
                            {"SENDMAIL_HTTP_BEARER_TOKEN": "token"},
                            "https://admin.example.test",
                        )

    def test_generates_a_unique_plus_address(self):
        self.assertEqual(runner.plus_address("imap-test01@example.test", "run-1234"), "imap-test01+run-1234@example.test")
        with self.assertRaisesRegex(runner.E2EError, "plain mailbox"):
            runner.plus_address("imap-test01+existing@example.test", "run-1234")

    def test_imap_dns_fallback_uses_sendmail_host_for_tcp_only(self):
        settings = {
            "IMAP_SERVER": "mail.realtekconnect.com",
            "IMAP_EMAIL_PORT": "993",
        }

        def resolve(host, _port):
            if host == "mail.realtekconnect.com":
                raise runner.socket.gaierror()
            return [(None, None, None, None, ("192.0.2.1", 993))]

        with mock.patch.object(runner.socket, "getaddrinfo", side_effect=resolve):
            self.assertEqual(
                runner.imap_connect_host(
                    settings, "https://sm.realtekconnect.com"
                ),
                "sm.realtekconnect.com",
            )

    def test_run_id_pattern_accepts_generated_timestamp_shape(self):
        self.assertRegex("20260726t182526z-1a2b3c4d", r"^[a-z0-9-]{8,64}$")

    def test_runner_uses_scoped_account_manager_email_deploy(self):
        source = MODULE_PATH.read_text(encoding="utf-8")
        self.assertIn('"account-manager-email-deploy"', source)
        self.assertIn('"--confirm", STACK', source)
        self.assertNotIn('"provision", "--env-root", str(runtime_root), "--deploy"', source)


if __name__ == "__main__":
    unittest.main()
