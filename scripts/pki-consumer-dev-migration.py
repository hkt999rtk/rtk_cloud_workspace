#!/usr/bin/env python3
"""Render dev PKI three-claim migration Jobs or guarded Deployment patches; never apply."""

import argparse
import json
import pathlib
import re

WORKSPACE = pathlib.Path(__file__).resolve().parents[1]
NAMESPACE = "video-cloud-dev-video-cloud"
IMAGE = re.compile(r"ghcr\.io/hkt999rtk/[a-z0-9_/-]+@sha256:[0-9a-f]{64}\Z")


def read_plan():
    plan = json.loads((WORKSPACE / "cloud_env/dev/pki-consumer-storage.json").read_text())
    if plan["environment"] != "dev" or plan["state"] != "migration-required" or len(plan["claims"]) != 3:
        raise ValueError("dev three-claim migration plan is required")
    sources = {item["destination_role"]: item["name"] for item in plan["migration_sources"]}
    if sources != {"api_trust": "video-cloud-api-pki-trust", "mqtt_data": "mqtt-pki-data"}:
        raise ValueError("unreviewed legacy source claims")
    return plan, sources


def render_job(role, image, suffix):
    plan, sources = read_plan()
    if not IMAGE.fullmatch(image):
        raise ValueError("migration Job requires a digest-pinned selected dev image")
    if not re.fullmatch(r"[a-z0-9-]{1,24}", suffix):
        raise ValueError("invalid migration suffix")
    roles = plan["role_claims"]
    if role == "api":
        source, target = sources["api_trust"], roles["api_identity"]
        uid, group = 10001, 10001
        script = """set -eu
mkdir -p /target/controller-identity /target/trust
cp -a /target/private /target/controller-identity/
find /source -mindepth 1 -maxdepth 1 ! -name lost+found -exec cp -a '{}' /target/trust/ ';'
diff -qr /target/private /target/controller-identity/private
diff -qr --exclude=lost+found /source /target/trust
test -s /target/controller-identity/private/identity.json
test -s /target/trust/root-trust.json
"""
    elif role == "mqtt":
        source, target = sources["mqtt_data"], roles["mqtt_identity"]
        uid, group = 1000, 1000
        script = """set -eu
mkdir -p /target/host-identity /target/emqx-data
cp -a /target/identity /target/seed /target/host-identity/
find /source -mindepth 1 -maxdepth 1 ! -name lost+found -exec cp -a '{}' /target/emqx-data/ ';'
diff -qr /target/identity /target/host-identity/identity
diff -qr /target/seed /target/host-identity/seed
diff -qr --exclude=lost+found /source /target/emqx-data
chmod 0700 /target/host-identity/identity /target/host-identity/seed
find /target/host-identity/identity -type f -exec chmod 0600 '{}' ';'
test -s /target/host-identity/identity/state.json
test -s /target/emqx-data/cluster.uuid
"""
    else:
        raise ValueError("role must be api or mqtt")
    return {"apiVersion": "batch/v1", "kind": "Job",
            "metadata": {"name": f"pki-{role}-storage-migrate-{suffix}", "namespace": NAMESPACE,
                         "labels": {"app.kubernetes.io/part-of": "rtk-cloud", "rtk.cloud/storage-migration": "three-claim"}},
            "spec": {"backoffLimit": 0, "template": {"metadata": {"labels": {"rtk.cloud/storage-migration": "three-claim"}},
                  "spec": {"restartPolicy": "Never", "automountServiceAccountToken": False,
                           "imagePullSecrets": [{"name": "ghcr-pull"}],
                           "securityContext": {"runAsUser": uid, "fsGroup": group},
                           "containers": [{"name": "copy", "image": image,
                                           "command": ["/bin/sh", "-ec"], "args": [script],
                                           "volumeMounts": [{"name": "source", "mountPath": "/source", "readOnly": True},
                                                            {"name": "target", "mountPath": "/target"}]}],
                           "volumes": [{"name": "source", "persistentVolumeClaim": {"claimName": source, "readOnly": True}},
                                       {"name": "target", "persistentVolumeClaim": {"claimName": target}}]}}}}


def render_patch(role, deployment):
    plan, sources = read_plan()
    expected_name = {"api": "video-cloud-api-pki", "mqtt": "mqtt-pki"}.get(role)
    if not expected_name or deployment.get("metadata", {}).get("name") != expected_name or deployment["metadata"].get("namespace") != NAMESPACE:
        raise ValueError("wrong dev PKI Deployment")
    if deployment["spec"].get("replicas") != 0 or deployment.get("status", {}).get("replicas", 0) != 0:
        raise ValueError("PKI Deployment must be fully stopped before patch rendering")
    spec = deployment["spec"]["template"]["spec"]
    volumes = spec["volumes"]
    containers = spec["containers"]
    roles = plan["role_claims"]
    if role == "api":
        changes = (("pki-state", sources["api_trust"], roles["api_trust"], "trust", "app"),
                   ("controller-identity", roles["api_identity"], roles["api_identity"], "controller-identity", "app"))
    else:
        changes = (("data", sources["mqtt_data"], roles["mqtt_data"], "emqx-data", "mqtt"),
                   ("mqtt-host-state", roles["mqtt_identity"], roles["mqtt_identity"], "host-identity", "mqtt"))
    patch = [{"op": "test", "path": "/metadata/resourceVersion", "value": deployment["metadata"]["resourceVersion"]},
             {"op": "test", "path": "/spec/replicas", "value": 0}]
    for name, old, new, subpath, container_name in changes:
        vi = next((i for i, item in enumerate(volumes) if item["name"] == name), None)
        ci = next((i for i, item in enumerate(containers) if item["name"] == container_name), None)
        if vi is None or ci is None or volumes[vi].get("persistentVolumeClaim", {}).get("claimName") != old:
            raise ValueError("unexpected source PVC in Deployment")
        mi = next((i for i, item in enumerate(containers[ci].get("volumeMounts", [])) if item["name"] == name), None)
        if mi is None or "subPath" in containers[ci]["volumeMounts"][mi]:
            raise ValueError("unexpected existing PKI mount subPath")
        path = f"/spec/template/spec/volumes/{vi}/persistentVolumeClaim/claimName"
        patch += [{"op": "test", "path": path, "value": old},
                  {"op": "replace", "path": path, "value": new},
                  {"op": "add", "path": f"/spec/template/spec/containers/{ci}/volumeMounts/{mi}/subPath", "value": subpath}]
    if role == "mqtt":
        security = spec.get("securityContext", {})
        if security.get("fsGroup") != 1000 or security.get("fsGroupChangePolicy") not in (None, "OnRootMismatch"):
            raise ValueError("unexpected MQTT fsGroup policy")
        if security.get("fsGroupChangePolicy") is None:
            patch.append({"op": "add", "path": "/spec/template/spec/securityContext/fsGroupChangePolicy", "value": "OnRootMismatch"})
    patch.append({"op": "replace", "path": "/spec/replicas", "value": 1})
    return patch


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument("--render-job", choices=("api", "mqtt"))
    group.add_argument("--render-patch", choices=("api", "mqtt"))
    parser.add_argument("--image", help="exact current image digest, required for Job")
    parser.add_argument("--suffix", help="unique reviewed Job suffix, required for Job")
    parser.add_argument("--deployment-json", type=pathlib.Path, help="fresh stopped Deployment JSON, required for patch")
    args = parser.parse_args()
    try:
        if args.render_job:
            if not args.image or not args.suffix:
                parser.error("--image and --suffix are required for a migration Job")
            result = render_job(args.render_job, args.image, args.suffix)
        else:
            if not args.deployment_json:
                parser.error("--deployment-json is required for a guarded patch")
            result = render_patch(args.render_patch, json.loads(args.deployment_json.read_text()))
    except (OSError, ValueError, KeyError) as exc:
        parser.error(str(exc))
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
