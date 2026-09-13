#!/usr/bin/env python3
"""Prepare the Dev Service v9 authority that adds only service:emqx-pki.

This runner never edits the active intermediate.  It creates the successor in
the ready state and proves activation remains receipt-gated; the separate
activation runner is responsible for listener bundle rollout and CRLs.
"""
import argparse
import base64
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import uuid

spec = importlib.util.spec_from_file_location(
    "service_hierarchy", Path(__file__).with_name("run.py"))
s = importlib.util.module_from_spec(spec)
spec.loader.exec_module(s)
m, NS = s.m, s.NS

SUBJECT = "service:emqx-pki"


class EMQXServiceAuthority(s.ServiceRun):
    def __init__(self, args):
        super().__init__(args)
        self.report["foundation_scope"] = (
            "Dev Service successor intermediate; retain current-root v6-v8 and add only "
            + SUBJECT)
        self.report["emqx_service_authority_runner_sha256"] = m.digest(
            Path(__file__).read_bytes())
        self.save("report.json", self.report)

    def intermediates(self):
        items, cursor = [], ""
        while True:
            page = self.api("/issuers/search", {"limit": 100, "before": cursor})
            items.extend(item for item in page["items"]
                         if item.get("environment") == "dev"
                         and item.get("trust_domain") == "service"
                         and item.get("kind") == "intermediate")
            cursor = page.get("next", "")
            if not cursor:
                return items

    def current_authority(self):
        root = self.active_root()
        live = sorted((item for item in self.intermediates()
                       if item.get("parent_issuer_id") == root["issuer_id"]
                       and item.get("status") in ("active", "retiring")),
                      key=lambda item: item["issuer_version"])
        m.require(len(live) == 3
                  and [item["issuer_version"] for item in live] == [6, 7, 8],
                  "exact current-root Service v6-v8 history required")
        active = [item for item in live if item["status"] == "active"]
        m.require(len(active) == 1 and active[0]["issuer_version"] == 8,
                  "only Service v8 may be active")
        current = active[0]
        m.require(SUBJECT not in current["service_client_ids"]
                  and current["service_client_ids"] == sorted(
                      current["service_client_ids"])
                  and current["server_dns_names"] == sorted(
                      current["server_dns_names"]),
                  "active Service v8 policy changed")
        return root, live, current

    def preflight(self):
        m.require(self.kube(["config", "current-context"]).strip() == self.context,
                  "canonical Dev context mismatch")
        for name in ("pki-controller", "certissuer", "video-cloud-api",
                     "video-cloud-logingester"):
            owner = self.obj("deployment", name)
            m.require(owner["spec"].get("replicas") == 1
                      and owner.get("status", {}).get("readyReplicas") == 1
                      and owner.get("status", {}).get("updatedReplicas") == 1
                      and owner.get("status", {}).get("observedGeneration")
                      == owner["metadata"]["generation"],
                      "required Dev deployment is not ready: " + name)
        build = subprocess.run(["go", "build", "-o", str(self.ceremony), "./cmd/pkiceremony"],
                               cwd=str(m.WORKSPACE / "repos/rtk_video_cloud"),
                               env=dict(os.environ, GOWORK="off"), capture_output=True, timeout=180)
        m.require(build.returncode == 0, "Service ceremony build failed")
        self.forward("am", "video-cloud-dev-account-manager", "account-manager", 80)
        self.accounts = m.read(self.foundation / "accounts.json")
        self.check("emqx_service_authority_preflight", {
            "environment": "dev", "staging_touched": False,
            "required_deployments": ["pki-controller", "certissuer",
                                      "video-cloud-api",
                                      "video-cloud-logingester"]})

    def prepare(self):
        root, predecessors, current = self.current_authority()
        target_ids = sorted(current["service_client_ids"] + [SUBJECT])
        request = {
            "environment": "dev", "trust_domain": "service",
            "kind": "intermediate", "parent_issuer_id": root["issuer_id"],
            "service_client_ids": target_ids,
            "server_dns_names": current["server_dns_names"],
        }
        source = Path(self.args.failed) if self.args.failed else None
        if source:
            prior = m.read(source / "report.json")
            m.require(prior.get("status") == "failed" and
                      prior.get("phase") == "prepare-emqx-service-v9",
                      "failed v9 preparation evidence required")
            saved_request = m.read(source / "intermediate-request.json")
            m.require(saved_request == request,
                      "saved v9 request differs from current policy")
            operation = m.read(source / "intermediate-operation.json")
            self.save("reconciled-from.json", {"source": str(source)})
        else:
            self.save("predecessors.json", predecessors)
            self.save("intermediate-request.json", request)
            operation = self.api("/operations", request,
                                 key="dev-service-emqx-v9-" + uuid.uuid4().hex)
        self.save("intermediate-operation.json", operation)
        self.approval(operation)
        issuer = self.api("/issuers/" + operation["issuer_id"])
        m.require(issuer["status"] == "approved"
                  and issuer["issuer_version"] == 9
                  and issuer["service_client_ids"] == target_ids
                  and issuer["server_dns_names"] == current["server_dns_names"]
                  and issuer["signer_provider"] == "openbao",
                  "Service v9 reservation differs")
        policies = json.loads(self.kube([
            "-n", NS, "exec", "deployment/pki-controller", "--",
            "/app/pkicontroller", "render-openbao-policy", issuer["issuer_id"]]))
        m.require(policies["mount"] == issuer["signer_reference"]
                  and policies["environment"] == "dev"
                  and "/sign/service-client" in policies["service_client_signer_policy"]
                  and "/sign/server" in policies["signer_policy"],
                  "Service v9 signer policy differs")
        self.save("provider-policies.json", policies)
        before = json.loads(self.bao([
            "read", "-format=json", "auth/kubernetes/role/pki-controller-dev"
        ]))["data"]
        self.save("controller-role-before.json", before)
        self.role_policy("pki-controller-dev",
                         "pki-controller-dev-" + issuer["issuer_id"],
                         policies["controller_policy"])
        self.api("/operations/" + operation["operation_id"] + "/provision", {})
        issuer = self.api("/issuers/" + issuer["issuer_id"])
        m.require(issuer["status"] == "provisioning" and issuer["csr_pem"],
                  "Service v9 CSR not durably registered")
        self.save("intermediate-provisioning.json", issuer)
        self.verify_intermediate_custody(issuer)
        source = Path(self.args.authority)
        csr_digest = m.digest(base64.b64decode(
            "".join(issuer["csr_pem"].splitlines()[1:-1])))
        self.ceremony_call([
            "sign", "--issuer", self.output / "intermediate-provisioning.json",
            "--expected-request-sha256", operation["request_sha256"],
            "--expected-csr-sha256", csr_digest,
            "--parent", self.output / "service-root.json",
            "--expected-parent-sha256", root["certificate_fingerprint_sha256"],
            "--key", source / "root-offline-simulation/ca-key.encrypted.pem",
            "--passphrase-file", m.read(source / "passphrase-reference.json")["path"],
            "--out", self.output / "intermediate-signed"])
        self.api("/operations/" + operation["operation_id"] + "/import", {
            "certificate_pem":
                (self.output / "intermediate-signed/certificate.pem").read_text()}, 204)
        issuer = self.api("/issuers/" + issuer["issuer_id"])
        m.require(issuer["status"] == "ready"
                  and issuer["issuer_version"] == 9
                  and issuer["service_client_ids"] == target_ids,
                  "Service v9 import differs")
        self.save("intermediate-ready.json", issuer)
        self.api("/operations/" + operation["operation_id"] + "/activate", {}, 409)
        self.check("emqx_service_v9_ready_gate_closed", {
            "issuer_id": issuer["issuer_id"], "issuer_version": 9,
            "added_subject": SUBJECT,
            "predecessor_versions": [6, 7, 8],
            "activation_without_receipts": 409,
            "private_key_exported": False})


    def resume(self):
        source = Path(self.args.failed)
        prior = m.read(source / "report.json")
        m.require(prior.get("status") == "failed" and
                  prior.get("phase") == "prepare-emqx-service-v9",
                  "failed v9 preparation evidence required")
        root, predecessors, current = self.current_authority()
        target_ids = sorted(current["service_client_ids"] + [SUBJECT])
        request = m.read(source / "intermediate-request.json")
        operation = m.read(source / "intermediate-operation.json")
        m.require(request == {"environment": "dev", "trust_domain": "service",
                              "kind": "intermediate", "parent_issuer_id": root["issuer_id"],
                              "service_client_ids": target_ids,
                              "server_dns_names": current["server_dns_names"]},
                  "saved v9 request differs from current policy")
        issuer = self.api("/issuers/" + operation["issuer_id"])
        m.require(issuer["status"] == "provisioning" and issuer["csr_pem"]
                  and issuer["issuer_version"] == 9
                  and issuer["service_client_ids"] == target_ids,
                  "only the retained v9 provisioning request may resume")
        self.save("reconciled-from.json", {"source": str(source)})
        for name in ("predecessors.json", "intermediate-request.json",
                     "intermediate-operation.json", "provider-policies.json",
                     "controller-role-before.json"):
            self.save(name, m.read(source / name))
        self.save("intermediate-provisioning.json", issuer)
        self.verify_intermediate_custody(issuer)
        csr_digest = m.digest(base64.b64decode(
            "".join(issuer["csr_pem"].splitlines()[1:-1])))
        authority = Path(self.args.authority)
        self.ceremony_call([
            "sign", "--issuer", self.output / "intermediate-provisioning.json",
            "--expected-request-sha256", operation["request_sha256"],
            "--expected-csr-sha256", csr_digest,
            "--parent", self.output / "service-root.json",
            "--expected-parent-sha256", root["certificate_fingerprint_sha256"],
            "--key", authority / "root-offline-simulation/ca-key.encrypted.pem",
            "--passphrase-file", m.read(authority / "passphrase-reference.json")["path"],
            "--out", self.output / "intermediate-signed"])
        self.api("/operations/" + operation["operation_id"] + "/import", {
            "certificate_pem":
                (self.output / "intermediate-signed/certificate.pem").read_text()}, 204)
        issuer = self.api("/issuers/" + issuer["issuer_id"])
        m.require(issuer["status"] == "ready" and issuer["issuer_version"] == 9
                  and issuer["service_client_ids"] == target_ids,
                  "resumed Service v9 import differs")
        self.save("intermediate-ready.json", issuer)
        self.api("/operations/" + operation["operation_id"] + "/activate", {}, 409)
        self.check("emqx_service_v9_ready_gate_closed", {
            "issuer_id": issuer["issuer_id"], "issuer_version": 9,
            "added_subject": SUBJECT, "predecessor_versions": [6, 7, 8],
            "activation_without_receipts": 409,
            "private_key_exported": False, "reconciled": True})

    def activate(self):
        source = Path(self.args.prepared)
        report = m.read(source / "report.json")
        successor = m.read(source / "intermediate-ready.json")
        operation = m.read(source / "intermediate-operation.json")
        root, predecessors, current = self.current_authority()
        m.require(report.get("status") == "passed"
                  and successor["status"] == "ready"
                  and successor["parent_issuer_id"] == root["issuer_id"]
                  and successor["issuer_version"] == 9
                  and current["issuer_version"] == 8
                  and operation["issuer_id"] == successor["issuer_id"],
                  "reviewed v9 authority evidence changed")
        successor = self.api("/issuers/" + successor["issuer_id"])
        m.require(successor["status"] == "ready", "Service v9 is no longer ready")
        refs = [{"issuer_id": item["issuer_id"],
                 "trust_bundle_version": item["trust_bundle_version"]}
                for item in [root] + predecessors + [successor]]
        bundle = "pki-service-bundles-v9-" + successor["issuer_id"][:8]
        self.create({"apiVersion": "v1", "kind": "ConfigMap",
                     "metadata": {"name": bundle, "namespace": NS},
                     "immutable": True,
                     "data": {"issuers.json": json.dumps(refs)}})
        def install(listener):
            owner = self.obj("deployment", listener)
            template = json.loads(json.dumps(owner["spec"]["template"]))
            volumes = [item for item in template["spec"]["volumes"]
                       if item["name"] == "service-bundles"]
            m.require(len(volumes) == 1
                      and volumes[0].get("configMap", {}).get("name")
                      == "pki-service-bundles-r3-6df752e5-overlap",
                      "reviewed Service bundle source changed: " + listener)
            volumes[0]["configMap"]["name"] = bundle
            patch = [{"op": "test", "path": "/metadata/resourceVersion",
                      "value": owner["metadata"]["resourceVersion"]},
                     {"op": "test", "path": "/spec/template",
                      "value": owner["spec"]["template"]},
                     {"op": "replace", "path": "/spec/template",
                      "value": template}]
            self.kube(["-n", NS, "patch", "deployment", listener,
                       "--type=json", "--patch-file=/dev/stdin"],
                      json.dumps(patch))
            self.kube(["-n", NS, "rollout", "status", "deployment/" + listener,
                       "--timeout=300s"], timeout=310)
            self.save(listener + "-v9-template.json", template)
        install("pki-controller")
        self.wait_receipts(successor["issuer_id"], successor["trust_bundle_version"],
                           ["pki-controller"], absent="certissuer")
        self.api("/operations/" + operation["operation_id"] + "/activate", {}, 409)
        install("certissuer")
        receipts = self.wait_receipts(successor["issuer_id"],
                                      successor["trust_bundle_version"],
                                      ["pki-controller", "certissuer"])
        policies = m.read(source / "provider-policies.json")
        self.role_policy("certissuer-pki-dev",
                         "pki-service-server-dev-v9-" + successor["issuer_id"],
                         policies["signer_policy"])
        self.role_policy("certissuer-pki-dev",
                         "pki-service-client-dev-v9-" + successor["issuer_id"],
                         policies["service_client_signer_policy"])
        self.api("/operations/" + operation["operation_id"] + "/activate", {}, 204)
        successor = self.api("/issuers/" + successor["issuer_id"])
        m.require(successor["status"] == "active", "Service v9 activation failed")
        provider = json.loads(self.bao([
            "read", "-format=json", successor["signer_reference"] + "/cert/crl"]))
        record = self.api("/issuers/" + successor["issuer_id"] + "/crl", {
            "crl_pem": provider["data"]["certificate"]})
        m.require(record["issuer_id"] == successor["issuer_id"],
                  "Service v9 CRL publication differs")
        self.save("intermediate-active.json", successor)
        self.save("intermediate-crl.json", record)
        self.check("emqx_service_v9_activated", {
            "issuer_id": successor["issuer_id"], "bundle": bundle,
            "bundle_consumers": receipts, "crl_sha256": record["crl_sha256"],
            "predecessor_versions": [6, 7, 8], "staging_touched": False})

    def recover_activation(self):
        failed = Path(self.args.failed)
        prior = m.read(failed / "report.json")
        source = Path(self.args.prepared)
        successor = m.read(source / "intermediate-ready.json")
        operation = m.read(source / "intermediate-operation.json")
        root, predecessors, current = self.current_authority()
        m.require(prior.get("status") == "failed"
                  and prior.get("phase") in ("activate-emqx-service-v9", "recover-emqx-service-v9")
                  and successor["status"] == "ready"
                  and operation["issuer_id"] == successor["issuer_id"]
                  and current["issuer_version"] == 8,
                  "failed v9 activation evidence changed")
        successor = self.api("/issuers/" + successor["issuer_id"])
        m.require(successor["status"] == "ready", "v9 is not recoverably ready")
        bundle = "pki-service-bundles-v9-" + successor["issuer_id"][:8]
        for listener in ("pki-controller", "certissuer"):
            owner = self.obj("deployment", listener)
            volumes = [item for item in owner["spec"]["template"]["spec"]["volumes"]
                       if item["name"] == "service-bundles"]
            m.require(len(volumes) == 1
                      and volumes[0].get("configMap", {}).get("name") == bundle,
                      "v9 bundle is not installed: " + listener)
        rows = self.kube(["-n", "video-cloud-dev-platform", "exec", "postgresql-0", "--", "psql", "-X", "-U", "postgres", "-d", "video_cloud", "-At", "-c", "SELECT consumer_id FROM pki_bundle_acknowledgments WHERE issuer_id='" + successor["issuer_id"] + "' AND bundle_version='" + successor["trust_bundle_version"] + "' ORDER BY consumer_id;"])
        m.require(rows.splitlines() == ["certissuer", "pki-controller"],
                  "persisted v9 listener receipts differ")
        policies = m.read(source / "provider-policies.json")
        self.role_policy("certissuer-pki-dev",
                         "pki-service-server-dev-v9-" + successor["issuer_id"],
                         policies["signer_policy"])
        self.role_policy("certissuer-pki-dev",
                         "pki-service-client-dev-v9-" + successor["issuer_id"],
                         policies["service_client_signer_policy"])
        self.api("/operations/" + operation["operation_id"] + "/activate", {}, 204)
        successor = self.api("/issuers/" + successor["issuer_id"])
        m.require(successor["status"] == "active", "recovered v9 activation failed")
        provider = json.loads(self.bao([
            "read", "-format=json", successor["signer_reference"] + "/cert/crl"]))
        record = self.api("/issuers/" + successor["issuer_id"] + "/crl", {
            "crl_pem": provider["data"]["certificate"]})
        self.save("intermediate-active.json", successor)
        self.save("intermediate-crl.json", record)
        self.check("emqx_service_v9_activation_recovered", {
            "issuer_id": successor["issuer_id"], "bundle": bundle,
            "bundle_receipts": ["pki-controller", "certissuer"],
            "crl_sha256": record["crl_sha256"],
            "replayed_authority_creation": False})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config-root", default=os.environ.get(
        "RTK_CLOUD_CONFIG_ROOT", str(Path.home() / ".config/rtk_cloud")))
    parser.add_argument("--authority", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--failed")
    parser.add_argument("--prepared")
    parser.add_argument("--activate", action="store_true")
    parser.add_argument("--recover-activate", action="store_true")
    args = parser.parse_args()
    m.require(not (args.activate and args.recover_activate),
              "choose one v9 activation mode")
    args.phase = ("recover-emqx-service-v9" if args.recover_activate else
                  "activate-emqx-service-v9" if args.activate else
                  "prepare-emqx-service-v9")
    m.require(not (args.activate or args.recover_activate) or args.prepared,
              "successful v9 preparation evidence required for activation")
    m.require(not args.recover_activate or args.failed,
              "failed v9 activation evidence required for recovery")
    lock = Path(args.config_root).expanduser() / "dev/pki/service-rollout.lock"
    owner = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
    runner = EMQXServiceAuthority(args)
    try:
        runner.preflight()
        runner.recover_activation() if args.recover_activate else (runner.activate() if args.activate else (runner.resume() if args.failed else runner.prepare()))
        runner.report["status"] = "passed"
    except Exception as error:
        runner.report["status"] = "failed"
        runner.report["failure"] = str(error)
        raise
    finally:
        runner.save("report.json", runner.report)
        runner.close()
        os.close(owner)
        print(json.dumps({"status": runner.report["status"],
                          "report": str(runner.output / "report.json")}), flush=True)


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(1))
    signal.signal(signal.SIGINT, lambda *_: sys.exit(1))
    try:
        main()
    except Exception as error:
        print(json.dumps({"error": str(error)}), file=sys.stderr)
        sys.exit(1)
