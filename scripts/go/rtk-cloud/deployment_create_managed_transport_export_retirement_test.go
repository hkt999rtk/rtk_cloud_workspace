package main

import (
	"crypto/x509"
	"encoding/base64"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedTransportExportRetirementFixture(t *testing.T) (secretStore, *environmentCreateExecution, *createManagedTransportPartialRecovery) {
	t.Helper()
	_, _, _, service := createServiceRecipeFixture(t, "dev")
	store, e, partial := managedTransportPostManagedFixture(t)
	source := e.Journal
	source.Entries = slices.Clone(e.Journal.Entries)
	source.KubernetesLeases = slices.Clone(e.Journal.KubernetesLeases)
	defs, live := []deploymentplan.Definition{}, []deploymentplan.Object{}
	for _, resource := range partial.PostManaged.Plan.Resources {
		actual := deploymentplan.Clone(resource.Desired)
		deploymentplan.Map(actual["metadata"])["uid"] = resource.UID
		live = append(live, actual)
		defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: resource.Desired, Dependencies: resource.Dependencies})
	}
	preview, err := deploymentplan.Compile(partial.Original.Inputs, defs, live)
	if err != nil {
		t.Fatal(err)
	}
	volume := environmentCreateVolumeOwner{Namespace: source.Stack + "-video-cloud", PVCName: "video-cloud-api-pki-controller-identity", PVCUID: "44444444-4444-4444-8444-444444444444", PVName: "original-api-private-volume", PVUID: "55555555-5555-4555-8555-555555555555", NativeID: "12345"}
	ledger := environmentCreateResourceLedger{Schema: "rtk-cloud.resource-ledger/v1", Environment: source.Environment, Stack: source.Stack, PlanSHA256: source.PlanSHA256, Provider: map[string]environmentCreateProviderReceipt{}, Kubernetes: map[string]string{}, Volumes: []environmentCreateVolumeOwner{volume}}
	active := managedTransportVolumeLedgerActive{Schema: "rtk-cloud.resource-ledger-active/v1", Environment: source.Environment, Stack: source.Stack, Plan: source.PlanSHA256, LedgerSHA: deploymentplan.Digest(ledger)}
	publication := mustProviderRecoveryJSON(deploymentplan.Object{"root": service.Root.Document, "intermediate": service.Intermediate.Document})
	if err := store.write("pki/service/issuer.json", publication, false); err != nil {
		t.Fatal(err)
	}
	write := func(path string, value any) {
		if err := store.write(path, mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	write(environmentCreateLedgerPath(e), ledger)
	write("deployment/resources/active.json", active)
	a := createFoundationRecoveryArtifact{Plan: createFoundationRecoveryPlan{Schema: "rtk-cloud.create-managed-transports-partial-recovery/v1", ParentSHA: source.PlanSHA256, ParentJournalSHA: deploymentplan.Digest(source), Materials: map[string]string{"accepted-service-core-managed-partial-original-plan": partial.Original.SHA256(), "accepted-service-core-managed-partial-forward-plan": preview.SHA256(), "accepted-service-core-managed-partial-native-proof": deploymentplan.Digest(partial), "accepted-service-core-managed-partial-original-volume-ledger": active.LedgerSHA, "accepted-service-core-managed-published-service": environmentPreflightBytesSHA256(publication)}}}
	review := deploymentplan.Digest(a.Plan)
	a.Qualification = deploymentplan.Qualification{Decision: "GO", PlanSHA256: review}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: review})
	}
	readerKey := "kubernetes/server-custody-video-cloud-api-pki"
	readerID := partial.PostManaged.ExportPlans[readerKey].Resources[0].ID
	readerUID := partial.PostManaged.ExportJournals[readerKey].Owners[readerID]
	pod, replicaSet := "66666666-6666-4666-8666-666666666666", "77777777-7777-4777-8777-777777777777"
	name := "video-cloud-api-pki." + source.Stack + "-video-cloud.svc"
	intent := deploymentplan.Object{"owner": "video-cloud-api-pki", "namespace": source.Stack + "-video-cloud", "claim_uid": volume.PVCUID, "reader_uid": readerUID, "pod_uid": pod, "replicaset_uid": replicaSet, "server_name": name, "root_sha256": service.Root.RootFingerprint}
	proof := deploymentplan.Object{"certificate_sha256": strings.Repeat("c", 64), "claim_uid": volume.PVCUID, "reader_uid": readerUID, "pod_uid": pod, "replicaset_uid": replicaSet, "server_name": name}
	retireIntent := deploymentplan.Object{"resource_id": readerID, "uid": readerUID}
	admission := environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + review, Phase: "recovery", State: "completed", CompletedAt: time.Now(), IntentSHA256: deploymentplan.Digest(map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": a.Plan.ParentJournalSHA}), Receipt: mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": review})}
	export := environmentCreateJournalEntry{Key: "server-export-video-cloud-api-pki", Phase: "bootstrap", State: "completed", CompletedAt: time.Now(), IntentSHA256: activeTransportJournalDigest(intent), Receipt: mustProviderRecoveryJSON(proof)}
	retire := environmentCreateJournalEntry{Key: "retire-pki-export-video-cloud-api-pki-" + source.PlanSHA256[:12], Phase: "bootstrap", State: "pending", StartedAt: time.Now(), IntentSHA256: activeTransportJournalDigest(retireIntent)}
	e.Journal.Entries = append(e.Journal.Entries, admission, export, retire)
	path := filepath.Join("deployment/create/openbao-recovery", review)
	write(filepath.Join(path, "qualification.json"), a)
	write(filepath.Join(path, "source-journal.json"), source)
	write(filepath.Join(path, "openbao-root-trust-plan.json"), preview)
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", store.ConfigRoot)
	return store, e, partial
}

func TestManagedTransportPartialExportRetirementRequiresOriginalCompletedExportAndUnwrittenRetirement(t *testing.T) {
	for _, mode := range []string{"original", "changed-admission", "changed-source-prefix", "changed-metadata", "added-lease", "retirement-child", "retirement-completed", "retirement-receipt", "wrong-retire-intent", "wrong-export-intent", "wrong-export-root", "missing-export", "changed-volume-pointer", "changed-publication", "wrong-reader", "wrong-claim", "malformed-certificate-pin"} {
		t.Run(mode, func(t *testing.T) {
			store, e, _ := managedTransportExportRetirementFixture(t)
			end := len(e.Journal.Entries)
			switch mode {
			case "changed-admission":
				e.Journal.Entries[end-3].IntentSHA256 = strings.Repeat("0", 64)
			case "changed-source-prefix":
				e.Journal.Entries[0].IntentSHA256 = strings.Repeat("0", 64)
			case "changed-metadata":
				e.Journal.OriginalJournalSHA256 = strings.Repeat("0", 64)
			case "added-lease":
				e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1])
			case "retirement-child":
				e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "kubernetes/" + e.Journal.Entries[end-1].Key, Phase: "retirement", State: "pending", IntentSHA256: strings.Repeat("0", 64)})
			case "retirement-completed":
				e.Journal.Entries[end-1].State = "completed"
				e.Journal.Entries[end-1].CompletedAt = time.Now()
			case "retirement-receipt":
				e.Journal.Entries[end-1].Receipt = []byte(`{"absent":true}`)
			case "wrong-retire-intent":
				e.Journal.Entries[end-1].IntentSHA256 = strings.Repeat("0", 64)
			case "wrong-export-intent", "wrong-export-root":
				e.Journal.Entries[end-2].IntentSHA256 = strings.Repeat("0", 64)
			case "missing-export":
				e.Journal.Entries[end-2].State = "pending"
			case "changed-volume-pointer":
				if err := store.write("deployment/resources/active.json", mustProviderRecoveryJSON(managedTransportVolumeLedgerActive{}), true); err != nil {
					t.Fatal(err)
				}
			case "changed-publication":
				if err := store.write("pki/service/issuer.json", []byte(`{"root":{}}`), true); err != nil {
					t.Fatal(err)
				}
			case "wrong-reader", "wrong-claim", "malformed-certificate-pin":
				var proof deploymentplan.Object
				if decodeEnvironmentJSON(e.Journal.Entries[end-2].Receipt, &proof) != nil {
					t.Fatal("fixture proof invalid")
				}
				field := map[string]string{"wrong-reader": "reader_uid", "wrong-claim": "claim_uid", "malformed-certificate-pin": "certificate_sha256"}[mode]
				proof[field] = "foreign"
				e.Journal.Entries[end-2].Receipt = mustProviderRecoveryJSON(proof)
			}
			base, prior, accepted, post, err := loadManagedTransportPostManaged(store, e)
			if (err == nil) != (mode == "original") {
				t.Fatal("unqualified export retirement source accepted", mode, err)
			}
			if mode == "original" {
				if base == nil || prior == nil || accepted == nil || post == nil || post.ExportRetire == nil || !environmentPreflightDigest.MatchString(post.ExportRetire.ExportSHA) || !environmentPreflightDigest.MatchString(post.ExportRetire.RetireSHA) || len(post.ExportPlans) != 2 {
					t.Fatal("original accepted export/retirement proof lost")
				}
				profile, flag := createRecoveryCheckpointProfile(e.Journal)
				if profile != "managed-transports-api-export-retirement" || flag != "--after-application-enrollment-trust" {
					t.Fatal("wrong retirement selector", profile, flag)
				}
			}
		})
	}
}

func TestManagedTransportPartialExportRetirementRejectsShortOrRepeatedSuffix(t *testing.T) {
	for length := 0; length < 10; length++ {
		journal := environmentCreateJournal{PlanSHA256: strings.Repeat("a", 64), Entries: make([]environmentCreateJournalEntry, length)}
		if managedTransportExportRetirementReview(journal) != "" {
			t.Fatal("short journal selected retirement continuation")
		}
	}
	store, e, _ := managedTransportExportRetirementFixture(t)
	e.Journal.Entries = append(e.Journal.Entries, e.Journal.Entries[len(e.Journal.Entries)-3:]...)
	if _, _, _, _, err := loadManagedTransportPostManaged(store, e); err == nil {
		t.Fatal("repeated export retirement suffix accepted")
	}
}

func TestManagedTransportPartialExportRetirementNativeMaterialRequiresExactCertificateKeyAndFiveFields(t *testing.T) {
	certEncoded, keyEncoded := frozenClientIdentity(t, time.Now().Add(time.Hour), x509.ExtKeyUsageServerAuth)
	certificate, _ := base64.StdEncoding.DecodeString(certEncoded)
	privateKey, _ := base64.StdEncoding.DecodeString(keyEncoded)
	_, otherKeyEncoded := frozenClientIdentity(t, time.Now().Add(time.Hour), x509.ExtKeyUsageServerAuth)
	otherKey, _ := base64.StdEncoding.DecodeString(otherKeyEncoded)
	for _, mode := range []string{"original", "different-certificate", "different-key", "wrong-environment", "wrong-root", "wrong-name", "unknown-field", "multiple-objects", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			material := environmentCreateOwnedServerMaterial{Certificate: string(certificate) + string(certificate), Key: string(privateKey), Root: strings.Repeat("a", 64), Name: "video-cloud-api-pki.video-cloud-dev-video-cloud.svc", Environment: "dev"}
			pins := &createManagedTransportExportRetire{CertificateSHA: environmentPreflightBytesSHA256([]byte(material.Certificate)), RootSHA: material.Root, ServerName: material.Name}
			switch mode {
			case "different-certificate":
				material.Certificate += "private-marker"
			case "different-key":
				material.Key = string(otherKey)
			case "wrong-environment":
				material.Environment = "staging"
			case "wrong-root":
				material.Root = strings.Repeat("b", 64)
			case "wrong-name":
				material.Name = "foreign"
			}
			raw := mustProviderRecoveryJSON(material)
			if mode == "unknown-field" {
				var body deploymentplan.Object
				if decodeEnvironmentJSON(raw, &body) != nil {
					t.Fatal("fixture decode failed")
				}
				body["foreign"] = "private-marker"
				raw = mustProviderRecoveryJSON(body)
			}
			if mode == "multiple-objects" {
				raw = append(raw, raw...)
			}
			if mode == "oversized" {
				raw = []byte(strings.Repeat("private-marker", 256<<10))
			}
			digest, err := validateManagedTransportRetainedExportMaterial(raw, "dev", pins)
			if (err == nil) != (mode == "original") || mode == "original" && !environmentPreflightDigest.MatchString(digest) {
				t.Fatal("unqualified native retained material accepted", mode, err)
			}
			if err != nil && strings.Contains(err.Error(), "private-marker") {
				t.Fatal("native private output leaked")
			}
		})
	}
}
