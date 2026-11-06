package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestStagingManagedTransportQualificationRequiresAcceptedExactMaterial(t *testing.T) {
	for _, mode := range []string{"valid-before-cutover", "valid-after-cutover", "replaced-owner", "changed-body", "changed-candidate", "changed-ca-file", "unaccepted-material", "missing-reload", "wrong-mounted-key"} {
		t.Run(mode, func(t *testing.T) {
			f := newStagingQualificationFixture(t)
			renewed := map[string]environmentCreateTransport{}
			for _, role := range []string{"management", "openbao"} {
				names := map[string][]string{"openbao": {"openbao." + f.parent.Stack + "-secrets.svc"}}
				if role == "management" {
					names = map[string][]string{}
					for i := 0; i < 7; i++ {
						names[fmt.Sprintf("client-%d", i)] = []string{fmt.Sprintf("client-%d.isolated.svc", i)}
					}
				}
				material, err := createEnvironmentTransport(f.store, f.e.Journal.PlanSHA256, role, names, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				renewed[role] = material
			}
			base := filepath.Join("deployment/create", f.e.Journal.PlanSHA256, "service-authorization-recovery/temporary-transport")
			f.save(t, filepath.Join(base, "candidate.json"), renewed)
			if err := f.store.write(filepath.Join(base, "openbao-ca.crt"), []byte(renewed["openbao"].CACert), false); err != nil {
				t.Fatal(err)
			}
			var defs []deploymentplan.Definition
			add := func(kind, name string) {
				o := deploymentplan.Object{"apiVersion": "v1", "kind": kind, "metadata": deploymentplan.Object{"name": name, "namespace": f.parent.Stack + "-video-cloud"}, "data": deploymentplan.Object{}}
				o, ok := initialTransportOverride(o, renewed)
				if !ok {
					t.Fatal(name)
				}
				defs = append(defs, deploymentplan.Definition{Object: o, Phase: "configuration"})
			}
			for i := 0; i < 7; i++ {
				add("Secret", fmt.Sprintf("pki-create-client-%d-transport", i))
			}
			add("Secret", "openbao-transport-tls")
			add("ConfigMap", "pki-create-transport-trust")
			add("ConfigMap", "pki-openbao-transport-ca")
			p, j := f.stage(t, stagingTransportMaterialStage, defs, true)
			f.stage(t, stagingTransportReloadStage, []deploymentplan.Definition{{Object: stagingTransportQualificationObject("Deployment", f.parent.Stack+"-video-cloud", "pki-controller", deploymentplan.Object{"replicas": float64(1)}), Phase: "workload"}}, true)
			if mode == "valid-after-cutover" {
				f.stage(t, stagingManagedHostsStage, []deploymentplan.Definition{{Object: stagingTransportQualificationObject("StatefulSet", f.parent.Stack+"-secrets", "openbao", deploymentplan.Object{"replicas": float64(1)}), Phase: "workload"}}, true)
			}
			switch mode {
			case "replaced-owner":
				deploymentplan.Map(f.objects[p.Resources[0].ID]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "changed-body":
				deploymentplan.Map(f.objects[p.Resources[0].ID]["data"])["ca.crt"] = "changed"
			case "changed-candidate":
				v := renewed["management"]
				v.CACert = "foreign-ca"
				renewed["management"] = v
				f.save(t, filepath.Join(base, "candidate.json"), renewed)
			case "changed-ca-file":
				if err := f.store.write(filepath.Join(base, "openbao-ca.crt"), []byte("foreign-ca"), true); err != nil {
					t.Fatal(err)
				}
			case "unaccepted-material":
				j.Accepted = false
				f.persistStage(t, p, j)
			case "missing-reload":
				for i := range f.e.Journal.Entries {
					if f.e.Journal.Entries[i].Key == "kubernetes/"+stagingTransportReloadStage {
						f.e.Journal.Entries[i].State = "pending"
					}
				}
				if err := f.e.save(); err != nil {
					t.Fatal(err)
				}
			}
			leaf := renewed["openbao"].Identities["openbao"]
			key := leaf.Key
			if mode == "wrong-mounted-key" {
				key = "replacement-key"
			}
			mounted := fmt.Sprintf("%s  /openbao/transport/tls.crt\n%s  /openbao/transport/ca.crt\n%s  /openbao/transport/tls.key\n", environmentPreflightBytesSHA256([]byte(leaf.Certificate)), environmentPreflightBytesSHA256([]byte(renewed["openbao"].CACert)), environmentPreflightBytesSHA256([]byte(key)))
			if err := os.WriteFile(filepath.Join(f.dir, "mounted"), []byte(mounted), 0600); err != nil {
				t.Fatal(err)
			}
			f.native(t)
			defer f.unchanged(t, p)()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			management, path, err := readStagingManagedHostsTransport(ctx, f.cfg, f.store, f.e, f.b)
			if mode != "valid-before-cutover" && mode != "valid-after-cutover" {
				if err == nil {
					t.Fatal("unverified staging transport accepted", mode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if deploymentplan.Digest(management) != deploymentplan.Digest(renewed["management"]) || path != filepath.Join(f.store.Root, base, "openbao-ca.crt") {
				t.Fatal("original renewed transport lineage changed")
			}
		})
	}
}

func TestStagingManagedOpenBaoQualificationPreservesOriginalOwner(t *testing.T) {
	for _, mode := range []string{"valid", "replaced-controller", "replaced-pod-owner", "changed-body", "changed-receipt", "unaccepted-forward", "changed-forward-owner"} {
		t.Run(mode, func(t *testing.T) {
			f := newStagingQualificationFixture(t)
			ns := f.parent.Stack + "-secrets"
			controller := stagingTransportQualificationObject("StatefulSet", ns, "openbao", deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "openbao", "image": "original-image"}}}}})
			_, foundation := f.stage(t, "foundation", []deploymentplan.Definition{{Object: controller, Phase: "workload"}}, true)
			id := deploymentplan.ID(controller)
			uid := foundation.Owners[id]
			original, _ := f.stage(t, stagingManagedHostsStage, []deploymentplan.Definition{{Object: controller, Phase: "workload"}}, false)
			stage := stagingManagedHostsStage + "-retained-claims-" + original.SHA256()[:12]
			managed := deploymentplan.Clone(controller)
			deploymentplan.Map(managed["spec"])["updateStrategy"] = deploymentplan.Object{"type": "OnDelete"}
			accepted, j := f.stage(t, stage, []deploymentplan.Definition{{Object: managed, Phase: "workload"}}, true)
			receipt := map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": original.SHA256()}
			if mode == "changed-receipt" {
				receipt["original_failed_plan_sha256"] = "foreign"
			}
			if err := f.e.RecordReceipt("kubernetes/"+stagingManagedHostsStage, mustProviderRecoveryJSON(receipt)); err != nil {
				t.Fatal(err)
			}
			if err := f.e.CompleteEntry("kubernetes/"+stagingManagedHostsStage, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			f.objects["Pod/"+ns+"/openbao-0"] = deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "openbao-0", "namespace": ns, "uid": "22222222-2222-4222-8222-222222222222", "resourceVersion": "12", "ownerReferences": []any{deploymentplan.Object{"kind": "StatefulSet", "name": "openbao", "uid": uid, "controller": true}}}}
			switch mode {
			case "replaced-controller":
				deploymentplan.Map(f.objects[id]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "replaced-pod-owner":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(f.objects["Pod/"+ns+"/openbao-0"]["metadata"])["ownerReferences"])[0])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "changed-body":
				deploymentplan.Map(f.objects[id]["spec"])["replicas"] = float64(2)
			case "unaccepted-forward":
				j.Accepted = false
				f.persistStage(t, accepted, j)
			case "changed-forward-owner":
				j.Owners[id] = "99999999-9999-4999-8999-999999999999"
				f.persistStage(t, accepted, j)
			}
			f.native(t)
			defer f.unchanged(t, accepted)()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			err := verifyStagingManagedOpenBao(ctx, f.cfg, f.store, f.e, f.b)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("unverified managed OpenBao accepted", mode)
			}
		})
	}
}

func TestStagingPrepareTransportPreservesUsableOriginalMaterial(t *testing.T) {
	for _, mode := range []string{"valid", "missing-ca", "changed-ca", "unbounded", "wrong-forward-lineage", "missing-signer"} {
		t.Run(mode, func(t *testing.T) {
			f := newStagingQualificationFixture(t)
			management := environmentCreateTransport{}
			for _, role := range []string{"management", "openbao"} {
				m, err := createEnvironmentTransport(f.store, f.e.Journal.PlanSHA256, role, map[string][]string{role: {role + ".isolated.svc"}}, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				if role == "management" {
					management = m
				}
			}
			if _, err := ensureEnvironmentCreateAccountSigner(f.store); err != nil {
				t.Fatal(err)
			}
			f.save(t, "inventory.json", secretInventory{SchemaVersion: secretInventorySchemaVersion, Environment: "staging"})
			f.save(t, "deployment/storage/create-allocation.json", map[string]string{"source": "original isolated allocation"})
			f.stage(t, "transport-client-enrollment", []deploymentplan.Definition{{Object: stagingTransportQualificationObject("Job", f.parent.Stack+"-video-cloud", "original-transport", deploymentplan.Object{}), Phase: "bootstrap"}}, true)
			operation := environmentCreateIssuerOperation{ID: "11111111-1111-4111-8111-111111111111", IssuerID: "22222222-2222-4222-8222-222222222222", Status: "requested"}
			for _, key := range []string{"pki-reserve-mqtt-root", "pki-authorize-" + operation.ID, "pki-provision-" + operation.ID, "pki-import-" + operation.ID} {
				if err := f.e.BeginEntry(key, "bootstrap", mustProviderRecoveryJSON(map[string]string{"source": "original"}), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				if err := f.e.RecordReceipt(key, mustProviderRecoveryJSON(operation)); err != nil {
					t.Fatal(err)
				}
				if err := f.e.CompleteEntry(key, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			original, _ := f.stage(t, stagingMQTTRootStage, []deploymentplan.Definition{{Object: stagingTransportQualificationObject("ConfigMap", f.parent.Stack+"-video-cloud", "original-root-trust", deploymentplan.Object{}), Phase: "configuration"}}, false)
			for _, prefix := range []string{"mqtt-root-trust-rwo-", "mqtt-root-trust-rwo-spread-"} {
				stage := prefix + original.SHA256()[:12]
				if mode == "wrong-forward-lineage" && prefix == "mqtt-root-trust-rwo-spread-" {
					stage = prefix + "ffffffffffff"
				}
				if err := f.e.BeginEntry("kubernetes/"+stage, stage, mustProviderRecoveryJSON(map[string]string{"original": original.SHA256()}), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			ca := filepath.Join(f.store.Root, "deployment/create/material", f.e.Journal.PlanSHA256, "openbao-transport/ca.crt")
			switch mode {
			case "missing-ca":
				if err := os.Remove(ca); err != nil {
					t.Fatal(err)
				}
			case "changed-ca":
				if err := os.WriteFile(ca, []byte(management.CACert), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-signer":
				if err := os.Remove(filepath.Join(f.store.Root, "pki/account-manager-jwt/access.key")); err != nil {
					t.Fatal(err)
				}
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			ctx := t.Context()
			if mode != "unbounded" {
				bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				ctx = bounded
			}
			got, path, err := prepareStagingMQTTTransport(ctx, f.cfg, f.store, &f.parent, f.e, f.b)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				if deploymentplan.Digest(got) != deploymentplan.Digest(management) || path != ca {
					t.Fatal("usable original transport was replaced")
				}
			} else if err == nil {
				t.Fatal("unqualified original transport accepted", mode)
			}
			assertRecoveryContractReadOnly(t, f.store, before)
		})
	}
}

func TestStagingAcceptedEnrollmentForwardRequiresExactReceiptLineage(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-failed-job", "wrong-failed-uid", "wrong-journal", "unaccepted-forward"} {
		t.Run(mode, func(t *testing.T) {
			f := newStagingQualificationFixture(t)
			job := stagingQualificationJob(f.parent.Stack+"-account-manager", "original-enrollment", "account-manager-service-state")
			original, j := f.stage(t, stagingCoreEnrollmentStage, []deploymentplan.Definition{{Object: job, Phase: "bootstrap"}}, false)
			id := original.Resources[0].ID
			uid := "44444444-4444-4444-8444-444444444444"
			j.Failed = id
			j.Owners[id] = uid
			f.persistStage(t, original, j)
			stage := stagingCoreEnrollmentStage + "-rwo-" + original.SHA256()[:12]
			deploymentplan.Map(job["metadata"])["name"] = "original-enrollment-rwo"
			accepted, aj := f.stage(t, stage, []deploymentplan.Definition{{Object: job, Phase: "bootstrap"}}, true)
			receipt := map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": original.SHA256(), "original_failed_journal_sha256": deploymentplan.Digest(j), "original_failed_job": id, "original_failed_job_uid": uid}
			switch mode {
			case "wrong-failed-job":
				receipt["original_failed_job"] = "Job/foreign/original"
			case "wrong-failed-uid":
				receipt["original_failed_job_uid"] = "99999999-9999-4999-8999-999999999999"
			case "wrong-journal":
				receipt["original_failed_journal_sha256"] = deploymentplan.Digest("foreign")
			case "unaccepted-forward":
				aj.Accepted = false
				f.persistStage(t, accepted, aj)
			}
			entry := *createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/"+stagingCoreEnrollmentStage)
			entry.Receipt = mustProviderRecoveryJSON(receipt)
			before := createBootstrapStateSnapshot(t, f.store.Root)
			got, completed, err := acceptedStagingCoreForward(f.store, f.e, entry, original, j)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				if got.SHA256() != accepted.SHA256() || deploymentplan.Digest(completed) != deploymentplan.Digest(aj) {
					t.Fatal("forward accepted another lineage")
				}
			} else if err == nil {
				t.Fatal("invalid receipt lineage accepted", mode)
			}
			assertRecoveryContractReadOnly(t, f.store, before)
		})
	}
}

func TestStagingExpiredTransportRenewalRejectsChangedMaterialOwners(t *testing.T) {
	for _, mode := range []string{"replaced-owner", "changed-body", "changed-candidate-key", "missing-projection"} {
		t.Run(mode, func(t *testing.T) {
			f := newStagingQualificationFixture(t)
			ns := f.parent.Stack + "-secrets"
			original := map[string]environmentCreateTransport{}
			for _, role := range []string{"management", "openbao"} {
				names := map[string][]string{"openbao": {"openbao." + ns + ".svc"}}
				if role == "management" {
					names = map[string][]string{}
					for i := 0; i < 7; i++ {
						names[fmt.Sprintf("client-%d", i)] = []string{fmt.Sprintf("client-%d.isolated.svc", i)}
					}
				}
				m, err := createEnvironmentTransport(f.store, f.e.Journal.PlanSHA256, role, names, time.Now().UTC().Add(-5*time.Hour))
				if err != nil {
					t.Fatal(err)
				}
				original[role] = m
			}
			controller := stagingTransportQualificationObject("StatefulSet", ns, "openbao", deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "openbao", "image": "original-image"}}}}})
			defs := []deploymentplan.Definition{{Object: controller, Phase: "workload"}}
			add := func(kind, name string) {
				if mode == "missing-projection" && name == "pki-create-client-6-transport" {
					return
				}
				o := deploymentplan.Object{"apiVersion": "v1", "kind": kind, "metadata": deploymentplan.Object{"name": name, "namespace": ns}, "data": deploymentplan.Object{}}
				o, ok := initialTransportOverride(o, original)
				if !ok {
					t.Fatal(name)
				}
				defs = append(defs, deploymentplan.Definition{Object: o, Phase: "configuration"})
			}
			for i := 0; i < 7; i++ {
				add("Secret", fmt.Sprintf("pki-create-client-%d-transport", i))
			}
			add("Secret", "openbao-transport-tls")
			add("ConfigMap", "pki-create-transport-trust")
			add("ConfigMap", "pki-openbao-transport-ca")
			foundation, j := f.stage(t, "foundation", defs, true)
			uid := j.Owners[deploymentplan.ID(controller)]
			podUID := "22222222-2222-4222-8222-222222222222"
			f.objects["Pod/"+ns+"/openbao-0"] = deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "openbao-0", "namespace": ns, "uid": podUID, "resourceVersion": "12", "ownerReferences": []any{deploymentplan.Object{"kind": "StatefulSet", "name": "openbao", "uid": uid, "controller": true}}}, "spec": deploymentplan.Clone(environmentPKIPod(controller))}
			init := mustProviderRecoveryJSON(map[string]any{"root_token": "isolated-root-token", "unseal_keys_b64": []string{"isolated-unseal-key"}})
			for path, data := range map[string][]byte{"openbao/init.json": init, "openbao/root-token": []byte("isolated-root-token"), "openbao/unseal-key": []byte("isolated-unseal-key")} {
				if err := f.store.write(path, data, false); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.e.BeginEntry("openbao-initialize", "bootstrap", mustProviderRecoveryJSON(map[string]string{"original": "initializer"}), time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if err := f.e.RecordReceipt("openbao-initialize", mustProviderRecoveryJSON(map[string]string{"init_sha256": environmentPreflightBytesSHA256(init), "pod_uid": podUID, "statefulset_uid": uid, "root_token_ref": "openbao/root-token", "unseal_ref": "openbao/unseal-key"})); err != nil {
				t.Fatal(err)
			}
			if err := f.e.CompleteEntry("openbao-initialize", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			f.stage(t, "transport-client-enrollment", []deploymentplan.Definition{{Object: stagingQualificationJob(f.parent.Stack+"-video-cloud", "original-enrollment", "original-claim"), Phase: "bootstrap"}}, true)
			operation := environmentCreateIssuerOperation{ID: "11111111-1111-4111-8111-111111111111", IssuerID: "22222222-2222-4222-8222-222222222222", Status: "requested"}
			for _, key := range []string{"pki-reserve-mqtt-root", "pki-authorize-" + operation.ID, "pki-provision-" + operation.ID, "pki-import-" + operation.ID} {
				if err := f.e.BeginEntry(key, "bootstrap", mustProviderRecoveryJSON(map[string]string{"source": "original"}), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				if err := f.e.RecordReceipt(key, mustProviderRecoveryJSON(operation)); err != nil {
					t.Fatal(err)
				}
				if err := f.e.CompleteEntry(key, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			failed, _ := f.stage(t, stagingMQTTRootStage, []deploymentplan.Definition{{Object: stagingTransportQualificationObject("ConfigMap", ns, "original-root-trust", deploymentplan.Object{}), Phase: "configuration"}}, false)
			for _, prefix := range []string{"mqtt-root-trust-rwo-", "mqtt-root-trust-rwo-spread-"} {
				stage := prefix + failed.SHA256()[:12]
				if err := f.e.BeginEntry("kubernetes/"+stage, stage, mustProviderRecoveryJSON(map[string]string{"original": failed.SHA256()}), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			base := filepath.Join("deployment/create", f.e.Journal.PlanSHA256, "service-authorization-recovery/temporary-transport")
			switch mode {
			case "replaced-owner":
				deploymentplan.Map(f.objects["Secret/"+ns+"/openbao-transport-tls"]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "changed-body":
				deploymentplan.Map(f.objects["Secret/"+ns+"/openbao-transport-tls"]["data"])["ca.crt"] = "unreviewed-authority"
			case "changed-candidate-key":
				candidate := map[string]environmentCreateTransport{}
				for role, material := range original {
					renewed, _, err := renewInitialTransportMaterial(material, time.Now().UTC())
					if err != nil {
						t.Fatal(err)
					}
					candidate[role] = renewed
				}
				m := candidate["openbao"]
				leaf := m.Identities["openbao"]
				leaf.Key = "different-serving-key"
				m.Identities["openbao"] = leaf
				candidate["openbao"] = m
				f.save(t, filepath.Join(base, "candidate.json"), candidate)
			}
			leaf := original["openbao"].Identities["openbao"]
			mounted := fmt.Sprintf("%s /openbao/transport/tls.crt\n%s /openbao/transport/ca.crt\n%s /openbao/transport/tls.key\n", environmentPreflightBytesSHA256([]byte(leaf.Certificate)), environmentPreflightBytesSHA256([]byte(original["openbao"].CACert)), environmentPreflightBytesSHA256([]byte(leaf.Key)))
			if err := os.WriteFile(filepath.Join(f.dir, "mounted"), []byte(mounted), 0600); err != nil {
				t.Fatal(err)
			}
			f.native(t)
			defer f.unchanged(t, foundation)()
			before := createBootstrapStateSnapshot(t, f.store.Root)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			_, _, err := prepareStagingMQTTTransport(ctx, f.cfg, f.store, &f.parent, f.e, f.b)
			if err == nil {
				t.Fatal("expired transport recovery accepted changed material", mode)
			}
			after := createBootstrapStateSnapshot(t, f.store.Root)
			for path, value := range before {
				if after[path] != value {
					t.Fatal("recovery replaced original material", path)
				}
			}
			for path := range after {
				if _, ok := before[path]; !ok && !strings.HasPrefix(filepath.ToSlash(path), filepath.ToSlash(base)+"/") {
					info, statErr := os.Stat(filepath.Join(f.store.Root, path))
					if statErr != nil || !info.IsDir() || path != base && !strings.HasPrefix(filepath.ToSlash(base), filepath.ToSlash(path)+"/") {
						t.Fatal("recovery wrote outside its candidate directory", path)
					}
				}
			}
			var candidate map[string]environmentCreateTransport
			if err := readExecutionJSON(filepath.Join(f.store.Root, base, "candidate.json"), &candidate); err != nil {
				t.Fatal("renewal did not reach candidate owner fencing", err)
			}
			if mode != "changed-candidate-key" {
				for role, old := range original {
					next := candidate[role]
					ca, err := parseFirstCertificatePEM([]byte(next.CACert))
					if err != nil || !ca.NotAfter.After(time.Now().Add(20*time.Hour)) {
						t.Fatal("candidate did not extend temporary authority", err)
					}
					for name, oldLeaf := range old.Identities {
						if next.Identities[name].Key != oldLeaf.Key {
							t.Fatal("renewal changed original serving key", role, name)
						}
					}
				}
			}
		})
	}
}
