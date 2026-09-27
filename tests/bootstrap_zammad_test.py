"""Safety and repeatability checks for the private Zammad bootstrap tool."""

import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1] / "scripts/bootstrap-zammad.py"
SPEC = importlib.util.spec_from_file_location("bootstrap_zammad", SCRIPT)
bootstrap = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(bootstrap)


class FakeAPI:
    def __init__(self, *, existing=False, incompatible=False):
        self.calls = []
        self.groups = [{"name": "RTK Support", "id": 2}] if existing else []
        self.attributes = []
        if existing:
            self.attributes = [
                {"name": name, "object": "Ticket", "data_type": "input"}
                for name in ("rtk_cloud_uuid", "rtk_category")
            ]
        if incompatible:
            self.attributes = [{"name": "rtk_cloud_uuid", "object": "User", "data_type": "input"}]

    def call(self, method, path, **kwargs):
        self.calls.append((method, path))
        if (method, path) == ("GET", "/api/v1/groups"):
            return 200, self.groups
        if (method, path) == ("POST", "/api/v1/groups"):
            self.groups = [{"name": "RTK Support", "id": 2}]
            return 201, self.groups[0]
        if (method, path) == ("GET", "/api/v1/object_manager_attributes"):
            return 200, self.attributes
        if (method, path) == ("POST", "/api/v1/object_manager_attributes"):
            self.attributes.append(kwargs["payload"])
            return 201, kwargs["payload"]
        if (method, path) == ("POST", "/api/v1/object_manager_attributes_execute_migrations"):
            return 200, {}
        raise AssertionError((method, path))


class BootstrapZammadTests(unittest.TestCase):
    def test_new_instance_creates_group_fields_and_one_migration(self):
        api = FakeAPI()
        group_id, migrated = bootstrap.ensure_group_and_fields(api, "operator-password")
        self.assertEqual(group_id, 2)
        self.assertTrue(migrated)
        self.assertEqual(api.calls.count(("POST", "/api/v1/object_manager_attributes")), 2)
        self.assertEqual(api.calls.count(("POST", "/api/v1/object_manager_attributes_execute_migrations")), 1)

    def test_existing_instance_does_not_repeat_migration(self):
        api = FakeAPI(existing=True)
        group_id, migrated = bootstrap.ensure_group_and_fields(api, "operator-password")
        self.assertEqual((group_id, migrated), (2, False))
        self.assertTrue(all(method == "GET" for method, _ in api.calls))

    def test_incompatible_cloud_field_fails_before_any_change(self):
        api = FakeAPI(incompatible=True)
        with self.assertRaisesRegex(RuntimeError, "incompatible"):
            bootstrap.ensure_group_and_fields(api, "operator-password")
        self.assertTrue(all(method == "GET" for method, _ in api.calls))

    def test_secret_files_are_private_and_never_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "runtime" / "zammad-integration-token"
            bootstrap.create_private_file(path, "opaque-token")
            self.assertEqual(bootstrap.private_file(path), "opaque-token")
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            with self.assertRaises(FileExistsError):
                bootstrap.create_private_file(path, "replacement")
            os.chmod(path, 0o644)
            with self.assertRaisesRegex(RuntimeError, "permissive"):
                bootstrap.private_file(path)

    def test_inventory_rejects_public_ingress(self):
        claims = {name: {"metadata": {"name": name}, "status": {"phase": "Bound"}} for name in bootstrap.PVC_NAMES}
        deployments = {name: {"metadata": {"name": name}, "status": {"readyReplicas": 1}} for name in bootstrap.APP_DEPLOYMENTS}
        payloads = {
            "pvc": {"items": list(claims.values())},
            "deployment": {"items": list(deployments.values())},
            "ingress": {"items": [{"metadata": {"name": "public"}}]},
        }
        with patch.object(bootstrap, "kubectl"), patch.object(bootstrap, "cluster_json", side_effect=lambda _, __, kind: payloads[kind]):
            with self.assertRaisesRegex(RuntimeError, "Ingress"):
                bootstrap.ready_inventory(Path("/nonexistent"), "video-cloud-test-support")

    def test_unassigned_owner_must_be_inactive_system_user(self):
        class OwnerAPI:
            def __init__(self, user):
                self.user = user

            def call(self, *_args, **_kwargs):
                return 200, self.user

        bootstrap.verify_unassigned_owner(OwnerAPI({"id": 1, "login": "-", "active": False}), "password")
        with self.assertRaisesRegex(RuntimeError, "not the inactive"):
            bootstrap.verify_unassigned_owner(OwnerAPI({"id": 1, "login": "agent", "active": True}), "password")

    def test_unexpected_active_admin_blocks_bootstrap(self):
        class AdminAPI:
            def __init__(self, logins):
                self.logins = logins

            def call(self, _method, path, **_kwargs):
                if path == "/api/v1/roles":
                    return 200, [{"id": 1, "name": "Admin"}]
                if path == "/api/v1/users":
                    return 200, [{"login": login, "active": True, "role_ids": [1]} for login in self.logins]
                raise AssertionError(path)

        bootstrap.verify_admin_accounts(AdminAPI(["rtk-zammad-operator"]), "password")
        with self.assertRaisesRegex(RuntimeError, "unexpected active"):
            bootstrap.verify_admin_accounts(AdminAPI(["rtk-zammad-operator", "legacy-admin"]), "password")


if __name__ == "__main__":
    unittest.main()
