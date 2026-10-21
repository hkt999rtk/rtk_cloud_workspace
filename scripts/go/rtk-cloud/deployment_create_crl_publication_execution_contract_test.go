package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// These tests begin at the immutable completion boundary: the synthetic native
// protocol has already produced its four receipts. The production publisher must
// authenticate them against the held owner, then commit matching private files.
func TestCRLExecutionContractAdapterPublication(t *testing.T) {
	for _, domain := range []string{"mqtt", "openbao_tls"} {
		for _, mode := range []string{"complete", "pending-child", "wrong-native-evidence", "wrong-top-receipt", "wrong-source", "wrong-intent", "foreign-lease", "missing-lease", "reordered-child", "early-child", "late-admission", "extra-tail", "changed-owner", "mirror-conflict"} {
			t.Run(domain+"/"+mode, func(t *testing.T) {
				f := newCRLExecutorFixture(t)
				e := f.execution
				source := e.Journal
				root, key, snapshot, now := rootCRLFixture(t)
				root.Domain = domain
				if domain == "openbao_tls" {
					root.ID = "734487b8-abcf-40be-a813-8b52c64f7954"
					snapshot.Prior.IssuerID = root.ID
				}
				request, err := buildCreateRootCRLRequestForDomain(root, snapshot, domain, now)
				if err != nil {
					t.Fatal(err)
				}
				signed := signedRootCRLFixture(t, root, key, request)
				mqtt := createMQTTRootCRLArtifact{Schema: createMQTTRootCRLSchema + "/artifact", Plan: createMQTTRootCRLPlan{Schema: createMQTTRootCRLSchema, Parent: source.PlanSHA256, SourceOwnerSHA: deploymentplan.Digest(e.owner), SourceJournalSHA: deploymentplan.Digest(source), Configuration: f.cfg, ConfigurationSHA: deploymentplan.Digest(f.cfg), Candidate: "deployment/repairs/mqtt-contract", Root: root, Snapshot: snapshot, SnapshotSHA: deploymentplan.Digest(snapshot), Request: request, Signed: signed}, QualifiedAt: now.Add(-time.Minute), ValidUntil: now.Add(14 * time.Minute)}
				openbao := createOpenBaoTLSRootCRLArtifact{Schema: createOpenBaoTLSRootCRLSchema + "/artifact", Plan: createOpenBaoTLSRootCRLPlan{Schema: createOpenBaoTLSRootCRLSchema, Parent: source.PlanSHA256, SourceOwnerSHA: deploymentplan.Digest(e.owner), SourceJournalSHA: deploymentplan.Digest(source), Configuration: f.cfg, ConfigurationSHA: deploymentplan.Digest(f.cfg), Candidate: "deployment/repairs/openbao-contract", Root: root, Snapshot: snapshot, SnapshotSHA: deploymentplan.Digest(snapshot), Request: request, Signed: signed}, QualifiedAt: now.Add(-time.Minute), ValidUntil: now.Add(14 * time.Minute)}
				names := []string{"video-cloud-api", "video-cloud-logingester"}
				if domain == "openbao_tls" {
					names = []string{"certissuer", "pki-controller"}
				}
				targets := []createRootCRLStateTarget{}
				evidence := map[string]string{"native-publication": deploymentplan.Digest(signed)}
				for _, name := range names {
					target := createRootCRLStateTarget{ID: "Deployment/" + source.Stack + "-video-cloud/" + name, UID: "original-" + name, Desired: deploymentplan.Object{"metadata": deploymentplan.Object{"name": name}}, Container: name, Paths: []string{"/synthetic/original-cache"}}
					targets = append(targets, target)
					evidence[target.ID+":state"] = deploymentplan.Digest(map[string]string{"crl": signed.Digest, "owner": target.UID})
					evidence[target.ID+":owner"] = deploymentplan.Digest(target)
					evidence[target.ID+":native-ack"] = deploymentplan.Digest(name + ":" + signed.Digest)
				}
				mqtt.Plan.Consumers = targets
				openbao.Plan.Consumers = targets
				plan := any(mqtt.Plan)
				prefix := "mqtt-root-crl-renewal-"
				review := deploymentplan.Digest(mqtt.Plan)
				base := mqttRootCRLBase(review)
				pointer := "mqtt-root-crl-renewal.json"
				importIntent := any(mqttRootCRLImportIntent(mqtt.Plan))
				intents := []any{}
				if domain == "openbao_tls" {
					plan = openbao.Plan
					prefix = "openbao-tls-root-crl-renewal-"
					review = deploymentplan.Digest(openbao.Plan)
					base = openBaoTLSRootCRLBase(review)
					pointer = "openbao-tls-root-crl-renewal.json"
					importIntent = openBaoTLSRootCRLImportIntent(openbao.Plan)
				}
				top := prefix + review
				intents = append(intents, plan, importIntent)
				for _, target := range targets {
					if domain == "mqtt" {
						intents = append(intents, mqttRootCRLRuntimeIntent(mqtt.Plan, target))
					} else {
						intents = append(intents, openBaoTLSRootCRLRuntimeIntent(openbao.Plan, target))
					}
				}
				// A lease fixture records an actual admitted scoped owner; publication itself
				// must never acquire another lease or append another journal entry.
				lease := deploymentplan.LeaseOwner{ResourceID: "Lease/" + source.Stack + "-video-cloud/rtk-deployment-execution", UID: "11111111-1111-4111-8111-111111111111", Holder: rootCRLLeasePlan(f.cfg, review).SHA256() + ":isolated-original-owner"}
				if err := e.RecordKubernetesLease(lease); err != nil {
					t.Fatal(err)
				}
				keys := []string{top, top + "/native-import", top + "/native-runtime-" + names[0], top + "/native-runtime-" + names[1]}
				for i, k := range keys {
					if err := rootCRLStepBegin(e, k, intents[i]); err != nil {
						t.Fatal(err)
					}
					if i == 0 {
						continue
					}
					var receipt any = map[string]any{"native_crl": signed, "request_sha256": deploymentplan.Digest(request)}
					if i > 1 {
						if domain == "mqtt" {
							receipt = mqttRootCRLEvidenceForTarget(evidence, targets[i-2])
						} else {
							receipt = openBaoTLSRootCRLEvidenceForTarget(evidence, targets[i-2])
						}
					}
					if err := rootCRLStepComplete(e, k, receipt); err != nil {
						t.Fatal(err)
					}
				}
				var receipt any = createMQTTRootCRLReceipt{createMQTTRootCRLSchema, source.PlanSHA256, review, mqtt.Plan.Candidate + "/signed/revocations.pem", signed.Digest, deploymentplan.Digest(evidence), true}
				if domain == "openbao_tls" {
					receipt = createOpenBaoTLSRootCRLReceipt{createOpenBaoTLSRootCRLSchema, source.PlanSHA256, review, openbao.Plan.Candidate + "/signed/revocations.pem", signed.Digest, deploymentplan.Digest(evidence), true}
				}
				if err := rootCRLStepComplete(e, top, receipt); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "pending-child":
					e.Journal.Entries[3].State = "pending"
					e.Journal.Entries[3].Receipt = nil
					e.Journal.Entries[3].CompletedAt = time.Time{}
				case "wrong-native-evidence":
					evidence[targets[0].ID+":native-ack"] = strings.Repeat("f", 64)
				case "wrong-top-receipt":
					var r map[string]any
					json.Unmarshal(e.Journal.Entries[0].Receipt, &r)
					r["NativeEvidenceSHA"] = strings.Repeat("f", 64)
					e.Journal.Entries[0].Receipt = mustProviderRecoveryJSON(r)
				case "wrong-source":
					source.OwnerToken = "foreign-owner"
				case "wrong-intent":
					e.Journal.Entries[1].IntentSHA256 = strings.Repeat("f", 64)
				case "foreign-lease":
					e.Journal.KubernetesLeases[0].Holder = "foreign"
				case "missing-lease":
					e.Journal.KubernetesLeases = nil
				case "reordered-child":
					e.Journal.Entries[2], e.Journal.Entries[3] = e.Journal.Entries[3], e.Journal.Entries[2]
				case "early-child":
					e.Journal.Entries[2].StartedAt = now.Add(-time.Hour)
				case "late-admission":
					e.Journal.Entries[0].StartedAt = now.Add(time.Hour)
				case "extra-tail":
					e.Journal.Entries = append(e.Journal.Entries, e.Journal.Entries[3])
				case "changed-owner":
					e.Journal.OwnerToken = "foreign-owner"
				case "mirror-conflict":
					if err := f.store.write(filepath.Join(base, "completed.json"), []byte(`{"preserve":"original conflict"}`), false); err != nil {
						t.Fatal(err)
					}
				}
				if err := e.save(); err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				publish := func() error {
					if domain == "mqtt" {
						return publishCreateMQTTRootCRLCompletion(context.Background(), f.store, e, mqtt, source, evidence, &out)
					}
					return publishCreateOpenBaoTLSRootCRLCompletion(context.Background(), f.store, e, openbao, source, evidence, &out)
				}
				before := deploymentplan.Digest(e.Journal)
				err = publish()
				pointerPath := filepath.Join("deployment/create/material", e.owner.PlanSHA256, pointer)
				if mode != "complete" {
					if err == nil {
						t.Fatal("tampered native completion accepted")
					}
					if _, readErr := f.store.read(pointerPath); !os.IsNotExist(readErr) {
						t.Fatal("incomplete publication exposed active pointer", readErr)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				first, err := f.store.read(pointerPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := publish(); err != nil {
					t.Fatal("exact pointer retry", err)
				}
				again, _ := f.store.read(pointerPath)
				if first != again || deploymentplan.Digest(e.Journal) != before {
					t.Fatal("publication retry rewrote evidence or appended execution")
				}
				mirror, _ := f.store.read(filepath.Join(base, "completed.json"))
				if mirror != first {
					t.Fatal("active pointer differs from immutable native completion")
				}
				for _, rel := range []string{pointerPath, filepath.Join(base, "completed.json"), filepath.Join(base, "final-journal.json")} {
					info, err := os.Stat(filepath.Join(f.store.Root, rel))
					if err != nil || info.Mode().Perm() != 0600 {
						t.Fatal("completion is not private", rel, err)
					}
				}
			})
		}
	}
}
