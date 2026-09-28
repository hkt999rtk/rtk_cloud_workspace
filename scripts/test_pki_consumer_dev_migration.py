"""Focused safety checks for the dev PKI storage migration renderer."""

import importlib.util
import json
import pathlib
import unittest
from unittest import mock

SCRIPT = pathlib.Path(__file__).with_name("pki-consumer-dev-migration.py")
SPEC = importlib.util.spec_from_file_location("pki_dev_migration", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)
IMAGE = "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + "a" * 64


def deployment(role):
    if role == "api":
        volumes = [{"name": "pki-state", "persistentVolumeClaim": {"claimName": "video-cloud-api-pki-trust"}},
                   {"name": "controller-identity", "persistentVolumeClaim": {"claimName": "video-cloud-api-pki-controller-identity"}}]
        containers = [{"name": "app", "volumeMounts": [{"name": "pki-state"}, {"name": "controller-identity"}]}]
        name = "video-cloud-api-pki"
    else:
        volumes = [{"name": "data", "persistentVolumeClaim": {"claimName": "mqtt-pki-data"}},
                   {"name": "pki-state", "persistentVolumeClaim": {"claimName": "mqtt-pki-trust"}},
                   {"name": "mqtt-host-state", "persistentVolumeClaim": {"claimName": "mqtt-pki-host-identity"}}]
        containers = [{"name": "mqtt", "volumeMounts": [{"name": "data"}, {"name": "mqtt-host-state"}]},
                      {"name": "pkibroker", "volumeMounts": [{"name": "pki-state"}]}]
        name = "mqtt-pki"
    return {"metadata": {"name": name, "namespace": MODULE.NAMESPACE, "resourceVersion": "123"},
            "spec": {"replicas": 0, "template": {"spec": {"volumes": volumes, "containers": containers, "securityContext": {"fsGroup": 1000}}}},
            "status": {"replicas": 0}}


class MigrationSafetyTest(unittest.TestCase):
    def setUp(self):
        plan = json.loads((SCRIPT.parents[1] / "cloud_env/dev/pki-consumer-storage.json").read_text())
        plan["state"] = "migration-required"
        sources = {item["destination_role"]: item["name"] for item in plan["migration_sources"]}
        patcher = mock.patch.object(MODULE, "read_plan", return_value=(plan, sources))
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_jobs_only_mount_exact_old_and_target_claims(self):
        for role, old, target in (("api", "video-cloud-api-pki-trust", "video-cloud-api-pki-controller-identity"),
                                  ("mqtt", "mqtt-pki-data", "mqtt-pki-host-identity")):
            job = MODULE.render_job(role, IMAGE, "reviewed")
            volumes = job["spec"]["template"]["spec"]["volumes"]
            self.assertEqual([x["persistentVolumeClaim"]["claimName"] for x in volumes], [old, target])
            self.assertTrue(volumes[0]["persistentVolumeClaim"]["readOnly"])
            self.assertFalse(job["spec"]["template"]["spec"]["automountServiceAccountToken"])
            self.assertIn("diff -qr", job["spec"]["template"]["spec"]["containers"][0]["args"][0])
            if role == "mqtt":
                self.assertIn("chmod 0600", job["spec"]["template"]["spec"]["containers"][0]["args"][0])

    def test_patch_requires_stopped_pod_and_current_resource_version(self):
        for role in ("api", "mqtt"):
            current = deployment(role)
            patch = MODULE.render_patch(role, current)
            self.assertEqual(patch[:2], [{"op": "test", "path": "/metadata/resourceVersion", "value": "123"},
                                         {"op": "test", "path": "/spec/replicas", "value": 0}])
            self.assertEqual(patch[-1], {"op": "replace", "path": "/spec/replicas", "value": 1})
            if role == "mqtt":
                self.assertIn({"op": "add", "path": "/spec/template/spec/securityContext/fsGroupChangePolicy", "value": "OnRootMismatch"}, patch)
            current["spec"]["replicas"] = 1
            with self.assertRaises(ValueError):
                MODULE.render_patch(role, current)


class CompletedMigrationSafetyTest(unittest.TestCase):
    def test_renderer_rejects_active_dev_plan(self):
        with self.assertRaisesRegex(ValueError, "migration plan is required"):
            MODULE.read_plan()


if __name__ == "__main__":
    unittest.main()
