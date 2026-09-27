#!/usr/bin/env python3
"""Validate environment-owned PKI consumer PVC plans; never apply resources."""

import argparse
import json
import pathlib
import os
import ssl
import re
import subprocess
import sys
import urllib.request
import urllib.error

ROLES = ("api_trust", "api_identity", "mqtt_data", "mqtt_trust", "mqtt_identity")
NAME = re.compile(r"[a-z0-9](?:[-a-z0-9]*[a-z0-9])?\Z")
SIZE = re.compile(r"([1-9][0-9]*)Gi\Z")


def load_plan(root, environment):
    path = root / "cloud_env" / environment / "pki-consumer-storage.json"
    data = json.loads(path.read_text())
    expected = {"schema_version", "environment", "state", "storage_class", "claims", "role_claims"}
    if environment == "dev":
        expected.add("migration_sources")
    if set(data) != expected:
        raise ValueError("PKI storage plan has unexpected or missing fields")
    if data["schema_version"] != 1 or data["environment"] != environment:
        raise ValueError("PKI storage plan version/environment mismatch")
    if data["state"] not in ("migration-required", "plan-only"):
        raise ValueError("PKI storage plan state must be migration-required or plan-only")
    if data["storage_class"] != "linode-block-storage-retain":
        raise ValueError("PKI storage class must retain data")
    claims = data["claims"]
    if not isinstance(claims, list) or not claims:
        raise ValueError("PKI storage plan requires claims")
    names = set()
    total = 0
    for claim in claims:
        if set(claim) != {"name", "request"} or not isinstance(claim["name"], str) or not NAME.fullmatch(claim["name"]):
            raise ValueError("invalid PKI claim name")
        if claim["name"] in names:
            raise ValueError("duplicate PKI claim name")
        names.add(claim["name"])
        match = SIZE.fullmatch(str(claim["request"]))
        if not match:
            raise ValueError("PKI claim request must be a positive Gi quantity")
        gib = int(match.group(1))
        if gib < 10:
            raise ValueError("new Linode PKI claims must request at least 10 Gi")
        total += gib
    roles = data["role_claims"]
    if not isinstance(roles, dict) or set(roles) != set(ROLES) or set(roles.values()) != names:
        raise ValueError("every PKI role and claim must be mapped")
    if roles["mqtt_trust"] == roles["mqtt_identity"] or roles["mqtt_trust"] == roles["mqtt_data"]:
        raise ValueError("MQTT trust shared with broker must be isolated from private identity and data")
    if roles["api_trust"] in (roles["mqtt_trust"], roles["mqtt_data"], roles["mqtt_identity"]):
        raise ValueError("API and MQTT must not share a claim")
    if roles["api_identity"] != roles["api_trust"] and roles["api_identity"] in (roles["mqtt_trust"], roles["mqtt_data"], roles["mqtt_identity"]):
        raise ValueError("API identity must not share a MQTT claim")
    if len(claims) != 3:
        raise ValueError("PKI consumer layout must use exactly three claims")
    if environment == "dev":
        sources = data["migration_sources"]
        if not isinstance(sources, list) or len(sources) != 2 or {item.get("destination_role") for item in sources} != {"api_trust", "mqtt_data"} or any(set(item) != {"name", "destination_role"} or item["name"] in names for item in sources):
            raise ValueError("dev requires exact legacy trust and data migration sources")
    return data, total


def _provider_count(token, path):
    total = 0
    page = 1
    while True:
        request = urllib.request.Request(
            f"https://api.linode.com/v4/{path}?page_size=500&page={page}",
            headers={"Authorization": "Bearer " + token})
        with urllib.request.urlopen(request, timeout=25, context=ssl.create_default_context(cafile="/etc/ssl/cert.pem")) as response:
            payload = json.load(response)
        total += len(payload["data"])
        if page >= payload.get("pages", 1):
            return total
        page += 1


def _check_provider_capacity(root, environment, additional):
    if additional == 0:
        return
    account = root / "cloud_env" / environment / "runtime" / "adapters" / "lke" / "account.env"
    limits = [line.split("=", 1)[1].strip() for line in account.read_text().splitlines()
              if line.startswith("LKE_ACTIVE_SERVICE_LIMIT=")]
    if len(limits) != 1 or not limits[0].isdigit() or int(limits[0]) < 1:
        raise ValueError("confirmed LKE_ACTIVE_SERVICE_LIMIT is required before new PKI PVCs")
    config_base = pathlib.Path(os.environ.get("RTK_CLOUD_CONFIG_ROOT", pathlib.Path.home() / ".config" / "rtk_cloud"))
    token = (config_base / environment / "operator" / "env" / "LINODE_TOKEN").read_text().strip()
    if not token:
        raise ValueError("environment-local Linode token is unavailable")
    instances = _provider_count(token, "linode/instances")
    volumes = _provider_count(token, "volumes")
    balancers = _provider_count(token, "nodebalancers")
    current = instances + volumes + balancers
    projected = current + additional
    limit = int(limits[0])
    print(f"provider: current={current} instances={instances} volumes={volumes} nodebalancers={balancers} new_pki_volumes={additional} projected={projected} confirmed_limit={limit}")
    if projected > limit:
        raise ValueError("PKI PVC projection exceeds confirmed active-service limit")


def _kubectl_items(kubeconfig, resource):
    result = subprocess.run(["kubectl", "--kubeconfig", str(kubeconfig), "get", resource, "-A", "-o", "json"],
                            capture_output=True, text=True, check=True, timeout=30)
    return json.loads(result.stdout)["items"]


def _audit_dev_cleanup(kubeconfig, plan):
    legacy = {item["name"] for item in plan["migration_sources"]}
    roles = plan["role_claims"]
    deployments = _kubectl_items(kubeconfig, "deployments")
    wanted = {"video-cloud-api-pki": {"pki-state": (roles["api_trust"], "trust"),
                                      "controller-identity": (roles["api_identity"], "controller-identity")},
              "mqtt-pki": {"data": (roles["mqtt_data"], "emqx-data"),
                           "mqtt-host-state": (roles["mqtt_identity"], "host-identity"),
                           "pki-state": (roles["mqtt_trust"], "")}}
    for name, expected in wanted.items():
        matches = [item for item in deployments if item["metadata"]["namespace"] == "video-cloud-dev-video-cloud" and item["metadata"]["name"] == name]
        if len(matches) != 1:
            raise ValueError(f"dev {name} Deployment missing")
        item = matches[0]
        if item["spec"].get("replicas") != 1 or item.get("status", {}).get("readyReplicas") != 1:
            raise ValueError(f"dev {name} is not ready on the new layout")
        spec = item["spec"]["template"]["spec"]
        volumes = {v["name"]: v.get("persistentVolumeClaim", {}).get("claimName") for v in spec["volumes"]}
        mounts = {m["name"]: m.get("subPath", "") for c in spec["containers"] for m in c.get("volumeMounts", [])}
        if any(volumes.get(vol) != claim or mounts.get(vol) != subpath for vol, (claim, subpath) in expected.items()):
            raise ValueError(f"dev {name} PVC mounts do not match selected plan")
    refs = []
    for resource in ("pods", "deployments", "statefulsets", "jobs"):
        for item in _kubectl_items(kubeconfig, resource):
            spec = item.get("spec", {})
            if resource != "pods":
                spec = spec.get("template", {}).get("spec", {})
            for volume in spec.get("volumes", []):
                if volume.get("persistentVolumeClaim", {}).get("claimName") in legacy and item["metadata"].get("namespace") == "video-cloud-dev-video-cloud":
                    refs.append(f"{resource}/{item['metadata']['name']}")
    if refs:
        raise ValueError("legacy PKI PVCs are still referenced by " + ", ".join(sorted(set(refs))))
    for name in sorted(legacy):
        print(f"CLEANUP_CANDIDATE {name}: new consumers Ready and no current Pod/workload reference")
    print("Read-only audit passed; verify backup/rollback evidence and retained PV/volume IDs before deletion")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--environment", required=True, choices=("dev", "staging", "prod"))
    parser.add_argument("--workspace", type=pathlib.Path, default=pathlib.Path(__file__).resolve().parents[1])
    parser.add_argument("--render", action="store_true", help="print review-only PVC JSON; never applies it")
    parser.add_argument("--live", action="store_true", help="read selected environment PVCs and report reuse/new claims")
    parser.add_argument("--cleanup-audit", action="store_true", help="read-only dev legacy-PVC reference and new-layout audit")
    args = parser.parse_args()
    try:
        plan, total = load_plan(args.workspace, args.environment)
    except (OSError, json.JSONDecodeError, ValueError) as exc:
        parser.error(str(exc))
    if args.render and (args.live or args.cleanup_audit):
        parser.error("--render and live/audit checks are separate review steps")
    if args.cleanup_audit and args.environment != "dev":
        parser.error("legacy cleanup audit applies only to dev")
    if args.live or args.cleanup_audit:
        config_base = pathlib.Path(os.environ.get("RTK_CLOUD_CONFIG_ROOT", pathlib.Path.home() / ".config" / "rtk_cloud"))
        kubeconfig = config_base / args.environment / "kube" / "kubeconfig.yaml"
        namespace = f"video-cloud-{args.environment}-video-cloud"
        result = subprocess.run(["kubectl", "--kubeconfig", str(kubeconfig), "-n", namespace, "get", "pvc", "-o", "json"], capture_output=True, text=True, check=True, timeout=30)
        current = {item["metadata"]["name"]: item for item in json.loads(result.stdout)["items"]}
        if args.cleanup_audit:
            try:
                _audit_dev_cleanup(kubeconfig, plan)
            except (OSError, ValueError, subprocess.CalledProcessError) as exc:
                parser.error(str(exc))
            return
        missing = 0
        for claim in plan["claims"]:
            found = current.get(claim["name"])
            if found is None:
                print(f"NEW {claim['name']} {claim['request']}")
                missing += 1
                continue
            spec = found["spec"]
            if spec.get("storageClassName") != plan["storage_class"] or spec.get("resources", {}).get("requests", {}).get("storage") != claim["request"] or found.get("status", {}).get("phase") != "Bound":
                raise ValueError(f"existing PVC {claim['name']} differs from selected plan or is not Bound")
            print(f"REUSE {claim['name']}")
        if args.environment == "dev":
            for source in plan["migration_sources"]:
                found = current.get(source["name"])
                if found is None or found.get("status", {}).get("phase") != "Bound":
                    raise ValueError(f"legacy migration source {source['name']} is missing or not Bound")
                print(f"MIGRATE {source['name']} -> {plan['role_claims'][source['destination_role']]}")
        print(f"{args.environment}: existing={len(plan['claims'])-missing} new={missing}; read-only; state={plan['state']}")
        try:
            _check_provider_capacity(args.workspace, args.environment, missing)
        except (OSError, ValueError, urllib.error.URLError) as exc:
            parser.error(str(exc))
        return
    if args.render:
        namespace = f"video-cloud-{args.environment}-video-cloud"
        items = [{"apiVersion": "v1", "kind": "PersistentVolumeClaim",
                  "metadata": {"name": claim["name"], "namespace": namespace},
                  "spec": {"storageClassName": plan["storage_class"], "accessModes": ["ReadWriteOnce"],
                           "resources": {"requests": {"storage": claim["request"]}}}}
                 for claim in plan["claims"]]
        json.dump({"apiVersion": "v1", "kind": "List", "items": items}, sys.stdout, indent=2)
        print()
    else:
        print(f"{args.environment}: {plan['state']}; {len(plan['claims'])} claims; {total} Gi requested; no resources created")
        for claim in plan["claims"]:
            print(f"  {claim['name']}: {claim['request']}")


if __name__ == "__main__":
    main()
