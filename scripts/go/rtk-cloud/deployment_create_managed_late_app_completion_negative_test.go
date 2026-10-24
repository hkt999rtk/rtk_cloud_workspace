package main

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func appCompletionNegativeFixture(t *testing.T) (createAppRootCRLArtifact, environmentCreateJournal, environmentCreateJournal, environmentCreateJournal, map[string]string, environmentCreateOwner) {
	t.Helper()
	a, small, oldFull := appRootCRLNegativeTailFixture(t)
	source := managedLateAppTransportSourceFixture().Journal
	root, key, snapshot, now := rootCRLFixture(t)
	root.ID, root.Domain = managedLateAppRootID, "app"
	snapshot.Prior.IssuerID = root.ID
	request, err := buildManagedLateAppRootCRLRequest(root, snapshot, now.Add(-90*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for i := range source.Entries {
		source.Entries[i].StartedAt = now.Add(-2 * time.Hour)
		source.Entries[i].CompletedAt = source.Entries[i].StartedAt.Add(time.Second)
	}
	owner := environmentCreateOwner{Schema: environmentCreateOwnerSchema, Environment: source.Environment, Stack: source.Stack, PlanSHA256: source.PlanSHA256, OwnerToken: source.OwnerToken, Journal: "deployment/create/isolated-App-completion/journal.json", StartedAt: now.Add(-3 * time.Hour)}
	a.Plan.Root, a.Plan.Snapshot, a.Plan.Request = root, snapshot, request
	a.Plan.Signed = signedRootCRLFixture(t, root, key, request)
	a.Plan.SourceJournalSHA = deploymentplan.Digest(source)
	a.Plan.SourceOwnerSHA = deploymentplan.Digest(owner)
	a.Plan.ConfigurationSHA = deploymentplan.Digest(a.Plan.Configuration)
	a.Plan.QualifiedAt = now.Add(-time.Hour)
	a.QualifiedAt, a.ValidUntil = a.Plan.QualifiedAt, a.Plan.QualifiedAt.Add(15*time.Minute)
	review := deploymentplan.Digest(a.Plan)
	a.Qualification.PlanSHA256 = review
	for i := range a.Qualification.Gates {
		a.Qualification.Gates[i].EvidenceSHA256 = review
	}
	evidence := map[string]string{}
	for _, entry := range oldFull.Entries[len(small.Entries)+2:] {
		var fields map[string]string
		if decodeEnvironmentJSON(entry.Receipt, &fields) != nil {
			t.Fatal("isolated native evidence")
		}
		for k, v := range fields {
			evidence[k] = v
		}
	}
	captured := source
	captured.Entries = slices.Clone(source.Entries)
	captured.KubernetesLeases = []deploymentplan.LeaseOwner{{ResourceID: "Lease/video-cloud-dev-video-cloud/rtk-deployment-execution", UID: "77777777-7777-4777-8777-777777777777", Holder: rootCRLLeasePlan(a.Plan.Configuration, review).SHA256() + ":original-App-owner"}}
	scope := "app-root-crl-renewal-" + review
	captured.Entries = append(captured.Entries,
		environmentCreateJournalEntry{Key: scope, Phase: "bootstrap", State: "pending", StartedAt: now.Add(-59 * time.Minute), IntentSHA256: activeTransportJournalDigest(a.Plan)},
		environmentCreateJournalEntry{Key: scope + "/native-import", Phase: "bootstrap", State: "completed", StartedAt: now.Add(-58 * time.Minute), CompletedAt: now.Add(-47 * time.Minute), IntentSHA256: activeTransportJournalDigest(appRootCRLImportIntent(a.Plan)), Receipt: mustProviderRecoveryJSON(deploymentplan.Object{"native_crl": a.Plan.Signed, "request_sha256": deploymentplan.Digest(a.Plan.Request), "operator_binding_sha256": deploymentplan.Digest(a.Plan.Operator)})},
		environmentCreateJournalEntry{Key: scope + "/native-runtime-video-cloud-api-app-pki", Phase: "bootstrap", State: "pending", StartedAt: now.Add(-46 * time.Minute), IntentSHA256: activeTransportJournalDigest(appRootCRLRuntimeIntent(a.Plan, a.Plan.Consumers[0]))})
	final := captured
	final.Entries = slices.Clone(captured.Entries)
	final.Entries[372].State, final.Entries[372].CompletedAt = "completed", now.Add(-5*time.Minute)
	final.Entries[372].Receipt = mustProviderRecoveryJSON(managedLateAppCompletionEvidenceForTarget(a, evidence, a.Plan.Consumers[0]))
	final.Entries = append(final.Entries, environmentCreateJournalEntry{Key: scope + "/native-runtime-mqtt-pki", Phase: "bootstrap", State: "completed", StartedAt: now.Add(-4 * time.Minute), CompletedAt: now.Add(-3 * time.Minute), IntentSHA256: activeTransportJournalDigest(appRootCRLRuntimeIntent(a.Plan, a.Plan.Consumers[1])), Receipt: mustProviderRecoveryJSON(managedLateAppCompletionEvidenceForTarget(a, evidence, a.Plan.Consumers[1]))})
	final.Entries[370].State, final.Entries[370].CompletedAt = "completed", now.Add(-2*time.Minute)
	final.Entries[370].Receipt = mustProviderRecoveryJSON(appRootCRLReceipt(a, evidence))
	return a, source, captured, final, evidence, owner
}

func cloneAppCompletionNegativeJournal(t *testing.T, j environmentCreateJournal) environmentCreateJournal {
	t.Helper()
	var out environmentCreateJournal
	if json.Unmarshal(mustProviderRecoveryJSON(j), &out) != nil {
		t.Fatal("isolated journal clone")
	}
	return out
}

func TestManagedLateAppCompletionNegativeSourceRequiresActualImportedOnlyPrefix(t *testing.T) {
	a, source, captured, final, _, _ := appCompletionNegativeFixture(t)
	if time.Now().UTC().Before(a.ValidUntil) || !managedLateAppCompletionSourceValid(a, source, captured) || !managedLateAppCompletionSourceValid(a, source, final) {
		t.Fatal("genuine original admission/import rejected after historical GO expiry")
	}
	for _, mode := range []string{"pending-import", "allocated-import", "missing-import", "missing-import-receipt", "changed-import-record", "changed-import-request", "changed-import-principal", "changed-top-intent", "reset-top-start", "late-original-admission", "zero-original-start", "zero-import-completion", "future-import-completion", "API-before-import", "API-with-invented-receipt", "unknown-API", "unknown-tail", "duplicate-tail", "second-lease", "missing-original-lease", "foreign-holder", "replacement-lease-UID", "changed-source", "changed-owner-token", "changed-metadata", "wrong-root", "wrong-domain", "prior-number", "successor-number", "failed-original-gate"} {
		t.Run(mode, func(t *testing.T) {
			current := cloneAppCompletionNegativeJournal(t, captured)
			var original createAppRootCRLArtifact
			if json.Unmarshal(mustProviderRecoveryJSON(a), &original) != nil {
				t.Fatal("isolated artifact")
			}
			switch mode {
			case "pending-import", "allocated-import":
				current.Entries[371].State = strings.TrimSuffix(mode, "-import")
				current.Entries[371].CompletedAt = time.Time{}
			case "missing-import":
				current.Entries = append(current.Entries[:371], current.Entries[372:]...)
			case "missing-import-receipt":
				current.Entries[371].Receipt = nil
			case "changed-import-record", "changed-import-request", "changed-import-principal":
				receipt := deploymentplan.Object{"native_crl": a.Plan.Signed, "request_sha256": deploymentplan.Digest(a.Plan.Request), "operator_binding_sha256": deploymentplan.Digest(a.Plan.Operator)}
				if mode == "changed-import-record" {
					bad := a.Plan.Signed
					bad.Digest = strings.Repeat("f", 64)
					receipt["native_crl"] = bad
				}
				if mode == "changed-import-request" {
					receipt["request_sha256"] = strings.Repeat("f", 64)
				}
				if mode == "changed-import-principal" {
					receipt["operator_binding_sha256"] = strings.Repeat("f", 64)
				}
				current.Entries[371].Receipt = mustProviderRecoveryJSON(receipt)
			case "changed-top-intent":
				current.Entries[370].IntentSHA256 = strings.Repeat("f", 64)
			case "reset-top-start":
				current.Entries[370].StartedAt = time.Now().UTC().Add(-time.Minute)
			case "late-original-admission":
				current.Entries[370].StartedAt = a.ValidUntil
			case "zero-original-start":
				current.Entries[370].StartedAt = time.Time{}
			case "zero-import-completion":
				current.Entries[371].CompletedAt = time.Time{}
			case "future-import-completion":
				current.Entries[371].CompletedAt = time.Now().UTC().Add(time.Minute)
			case "API-before-import":
				current.Entries[372].StartedAt = current.Entries[371].CompletedAt.Add(-time.Second)
			case "API-with-invented-receipt":
				current.Entries[372].Receipt = mustProviderRecoveryJSON(map[string]string{"invented": strings.Repeat("f", 64)})
			case "unknown-API":
				current.Entries[372].Key += "-foreign"
			case "unknown-tail":
				current.Entries = append(current.Entries, environmentCreateJournalEntry{Key: "unknown-completion-stage", State: "pending"})
			case "duplicate-tail":
				current.Entries = append(current.Entries, current.Entries[372], current.Entries[372])
			case "second-lease":
				current.KubernetesLeases = append(current.KubernetesLeases, current.KubernetesLeases[0])
			case "missing-original-lease":
				current.KubernetesLeases = nil
			case "foreign-holder":
				current.KubernetesLeases[0].Holder = "foreign-holder"
			case "replacement-lease-UID":
				current.KubernetesLeases[0].UID = "replacement"
			case "changed-source":
				current.Entries[0].Receipt = mustProviderRecoveryJSON(map[string]string{"changed": "source"})
			case "changed-owner-token":
				current.OwnerToken = "foreign-owner"
			case "changed-metadata":
				current.OriginalJournalSHA256 = strings.Repeat("f", 64)
			case "wrong-root":
				original.Plan.Root.ID = "99999999-9999-4999-8999-999999999999"
			case "wrong-domain":
				original.Plan.Root.Domain = "mqtt"
			case "prior-number":
				original.Plan.Snapshot.Prior.Number = "2"
			case "successor-number":
				original.Plan.Signed.Number = "3"
			case "failed-original-gate":
				original.Qualification.Gates[0].Status = "FAIL"
			}
			if managedLateAppCompletionSourceValid(original, source, current) {
				t.Fatal("unqualified completion-only source admitted", mode)
			}
		})
	}
}

func TestManagedLateAppCompletionNegativeMembershipFreezesImportLeaseAndOldStarts(t *testing.T) {
	a, source, captured, final, evidence, _ := appCompletionNegativeFixture(t)
	if !managedLateAppCompletionMembership(a, source, captured, captured, evidence) || !managedLateAppCompletionMembership(a, source, captured, final, evidence) {
		t.Fatal("genuine monotonic API/Broker/top closure refused")
	}
	for _, mode := range []string{"changed-import-start", "changed-import-completion", "changed-import-raw-order", "changed-API-start", "changed-API-phase", "changed-API-intent", "changed-top-phase", "changed-top-start", "added-parent-admission", "added-lease", "changed-lease-UID", "changed-metadata", "changed-source", "missing-native-root-ACK", "missing-native-ICA-ACK", "missing-cache", "wrong-owner-evidence", "extra-native-evidence", "changed-completed-API", "regressed-completion", "missing-final-broker", "wrong-broker-receipt", "wrong-top-receipt"} {
		t.Run(mode, func(t *testing.T) {
			before := cloneAppCompletionNegativeJournal(t, captured)
			current := cloneAppCompletionNegativeJournal(t, final)
			observed := map[string]string{}
			for k, v := range evidence {
				observed[k] = v
			}
			switch mode {
			case "changed-import-start":
				current.Entries[371].StartedAt = current.Entries[371].StartedAt.Add(time.Second)
			case "changed-import-completion":
				current.Entries[371].CompletedAt = current.Entries[371].CompletedAt.Add(time.Second)
			case "changed-import-raw-order":
				current.Entries[371].Receipt = json.RawMessage(`{"request_sha256":"` + deploymentplan.Digest(a.Plan.Request) + `","operator_binding_sha256":"` + deploymentplan.Digest(a.Plan.Operator) + `","native_crl":` + string(mustProviderRecoveryJSON(a.Plan.Signed)) + `}`)
			case "changed-API-start":
				current.Entries[372].StartedAt = current.Entries[372].StartedAt.Add(time.Second)
			case "changed-API-phase":
				current.Entries[372].Phase = "another-phase"
			case "changed-API-intent":
				current.Entries[372].IntentSHA256 = strings.Repeat("f", 64)
			case "changed-top-phase":
				current.Entries[370].Phase = "recovery"
			case "changed-top-start":
				current.Entries[370].StartedAt = current.Entries[370].StartedAt.Add(time.Second)
			case "added-parent-admission":
				current.Entries = append(current.Entries, environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + strings.Repeat("f", 64), State: "pending"})
			case "added-lease":
				current.KubernetesLeases = append(current.KubernetesLeases, current.KubernetesLeases[0])
			case "changed-lease-UID":
				current.KubernetesLeases[0].UID = "99999999-9999-4999-8999-999999999999"
			case "changed-metadata":
				current.ProviderMaterialPlanSHA256 = strings.Repeat("f", 64)
			case "changed-source":
				current.Entries[0].IntentSHA256 = strings.Repeat("f", 64)
			case "missing-native-root-ACK":
				delete(observed, "native-ACKs-"+a.Plan.Root.ID)
			case "missing-native-ICA-ACK":
				delete(observed, "native-ACKs-"+a.Plan.Intermediate.ID)
			case "missing-cache":
				delete(observed, "cache-"+a.Plan.Consumers[0].ID+"-"+filepath.Base(a.Plan.Consumers[0].Paths[0]))
			case "wrong-owner-evidence":
				observed["consumer-owner-"+a.Plan.Consumers[0].ID] = strings.Repeat("f", 64)
			case "extra-native-evidence":
				observed["invented-authority"] = strings.Repeat("f", 64)
			case "changed-completed-API":
				before.Entries[372] = final.Entries[372]
				current.Entries[372].CompletedAt = current.Entries[372].CompletedAt.Add(time.Second)
			case "regressed-completion":
				before = cloneAppCompletionNegativeJournal(t, final)
				current = cloneAppCompletionNegativeJournal(t, captured)
			case "missing-final-broker":
				current.Entries = current.Entries[:373]
			case "wrong-broker-receipt":
				current.Entries[373].Receipt = mustProviderRecoveryJSON(managedLateAppCompletionEvidenceForTarget(a, evidence, a.Plan.Consumers[0]))
			case "wrong-top-receipt":
				receipt := appRootCRLReceipt(a, evidence)
				receipt.EvidenceSHA = strings.Repeat("f", 64)
				current.Entries[370].Receipt = mustProviderRecoveryJSON(receipt)
			}
			if managedLateAppCompletionMembership(a, source, before, current, observed) {
				t.Fatal("completion changed genuine old history or invented evidence", mode)
			}
		})
	}
}

func appCompletionNegativeArtifact(t *testing.T, original createAppRootCRLArtifact, source, captured environmentCreateJournal, evidence map[string]string, owner environmentCreateOwner) managedLateAppCompletionArtifact {
	t.Helper()
	lease := captured.KubernetesLeases[len(source.KubernetesLeases)]
	leasePlan := rootCRLLeasePlan(original.Plan.Configuration, deploymentplan.Digest(original.Plan))
	actual := deploymentplan.Object{"apiVersion": "coordination.k8s.io/v1", "kind": "Lease", "metadata": deploymentplan.Object{"name": "rtk-deployment-execution", "namespace": source.Stack + "-video-cloud", "uid": lease.UID, "resourceVersion": "37", "labels": deploymentplan.Object{"rtk.realtek.com/stack": source.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}, "annotations": deploymentplan.Object{"rtk.realtek.com/plan-sha256": leasePlan.SHA256(), "rtk.realtek.com/environment": source.Environment}}, "spec": deploymentplan.Object{"holderIdentity": lease.Holder}}
	at := time.Now().UTC().Add(-time.Minute)
	p := managedLateAppCompletionPlan{Schema: managedLateAppCompletionSchema, Parent: original.Plan.Parent, OriginalReview: deploymentplan.Digest(original.Plan), OriginalArtifactSHA: deploymentplan.Digest(original), SourceJournalSHA: deploymentplan.Digest(captured), OwnerSHA: deploymentplan.Digest(owner), ConfigurationSHA: original.Plan.ConfigurationSHA, KubeconfigSHA: original.Plan.KubeconfigSHA, CheckerSHA: strings.Repeat("f", 64), NativeEvidence: evidence, Lease: lease, LeaseObject: actual, QualifiedAt: at}
	a := managedLateAppCompletionArtifact{Schema: managedLateAppCompletionSchema + "/artifact", Plan: p, QualifiedAt: at, ValidUntil: at.Add(15 * time.Minute)}
	bindAppCompletionNegativeGates(&a)
	return a
}
func bindAppCompletionNegativeGates(a *managedLateAppCompletionArtifact) {
	review := deploymentplan.Digest(a.Plan)
	a.Qualification = deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}
	for _, id := range managedLateAppCompletionGates {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: review})
	}
}

func TestManagedLateAppCompletionNegativeFreshArtifactCannotChangeAuthorityOrSelector(t *testing.T) {
	original, source, captured, _, evidence, owner := appCompletionNegativeFixture(t)
	valid := appCompletionNegativeArtifact(t, original, source, captured, evidence, owner)
	if !managedLateAppCompletionArtifactValid(valid, original, source, captured, owner) {
		t.Fatal("genuine isolated completion artifact refused")
	}
	for _, mode := range []string{"schema", "original-review", "original-artifact", "parent", "source", "owner", "configuration", "kubeconfig", "checker-shape", "future-qualification", "different-qualification-time", "extended-freshness", "missing-gate", "failed-gate", "foreign-gate", "lease-UID", "lease-holder", "lease-object-holder", "lease-object-plan", "lease-object-owner", "missing-lease-object", "missing-native-ACK"} {
		t.Run(mode, func(t *testing.T) {
			var a managedLateAppCompletionArtifact
			if json.Unmarshal(mustProviderRecoveryJSON(valid), &a) != nil {
				t.Fatal("isolated artifact")
			}
			switch mode {
			case "schema":
				a.Plan.Schema = "foreign-completion-schema"
			case "original-review":
				a.Plan.OriginalReview = strings.Repeat("e", 64)
			case "original-artifact":
				a.Plan.OriginalArtifactSHA = strings.Repeat("e", 64)
			case "parent":
				a.Plan.Parent = strings.Repeat("e", 64)
			case "source":
				a.Plan.SourceJournalSHA = strings.Repeat("e", 64)
			case "owner":
				a.Plan.OwnerSHA = strings.Repeat("e", 64)
			case "configuration":
				a.Plan.ConfigurationSHA = strings.Repeat("e", 64)
			case "kubeconfig":
				a.Plan.KubeconfigSHA = strings.Repeat("e", 64)
			case "checker-shape":
				a.Plan.CheckerSHA = "unqualified-checker"
			case "future-qualification":
				a.Plan.QualifiedAt = time.Now().UTC().Add(time.Minute)
				a.QualifiedAt = a.Plan.QualifiedAt
				a.ValidUntil = a.QualifiedAt.Add(15 * time.Minute)
			case "different-qualification-time":
				a.Plan.QualifiedAt = a.Plan.QualifiedAt.Add(time.Second)
			case "extended-freshness":
				a.ValidUntil = a.ValidUntil.Add(time.Second)
			case "lease-UID":
				a.Plan.Lease.UID = "99999999-9999-4999-8999-999999999999"
			case "lease-holder":
				a.Plan.Lease.Holder = "foreign-owner"
			case "lease-object-holder":
				deploymentplan.Map(a.Plan.LeaseObject["spec"])["holderIdentity"] = "foreign-owner"
			case "lease-object-plan":
				deploymentplan.Map(deploymentplan.Map(a.Plan.LeaseObject["metadata"])["annotations"])["rtk.realtek.com/plan-sha256"] = strings.Repeat("e", 64)
			case "lease-object-owner":
				deploymentplan.Map(a.Plan.LeaseObject["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "missing-lease-object":
				a.Plan.LeaseObject = nil
			case "missing-native-ACK":
				delete(a.Plan.NativeEvidence, "native-ACKs-"+original.Plan.Root.ID)
			}
			bindAppCompletionNegativeGates(&a)
			if mode == "missing-gate" {
				a.Qualification.Gates = a.Qualification.Gates[:5]
			}
			if mode == "failed-gate" {
				a.Qualification.Gates[0].Status = "FAIL"
			}
			if mode == "foreign-gate" {
				a.Qualification.Gates[0].ID = "foreign-required-gate"
			}
			if managedLateAppCompletionArtifactValid(a, original, source, captured, owner) {
				t.Fatal("self-consistent but foreign fresh authority admitted", mode)
			}
		})
	}
	store := makeIsolatedTestSecretStore(t, "dev")
	review := deploymentplan.Digest(valid.Plan)
	base := managedLateAppCompletionBase(valid.Plan.OriginalReview, review)
	for name, value := range map[string]any{"qualification.json": valid, "source-journal.json": captured, "source-owner.json": owner} {
		lateSeedForwardWrite(t, store, filepath.Join(base, name), value)
	}
	if _, _, err := loadManagedLateAppCompletionArtifact(store, valid.Plan.OriginalReview, review, original, source, owner); err != nil {
		t.Fatal("correct selected artifact refused", err)
	}
	swapped := valid
	swapped.Plan.CheckerSHA = strings.Repeat("e", 64)
	bindAppCompletionNegativeGates(&swapped)
	if !managedLateAppCompletionArtifactValid(swapped, original, source, captured, owner) {
		t.Fatal("swapped object must be independently self-valid to test path authority")
	}
	path := filepath.Join(base, "qualification.json")
	if err := store.write(path, mustProviderRecoveryJSON(swapped), true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadManagedLateAppCompletionArtifact(store, valid.Plan.OriginalReview, review, original, source, owner); err == nil {
		t.Fatal("another self-valid checker artifact accepted from requested review path")
	}
}

func TestManagedLateAppCompletionNegativeDurableProofCannotRewriteSixGOObservation(t *testing.T) {
	original, source, captured, final, evidence, owner := appCompletionNegativeFixture(t)
	a := appCompletionNegativeArtifact(t, original, source, captured, evidence, owner)
	started := a.QualifiedAt.Add(10 * time.Second)
	final.Entries[372].CompletedAt = started.Add(5 * time.Second)
	final.Entries[373].StartedAt = started.Add(10 * time.Second)
	final.Entries[373].CompletedAt = started.Add(15 * time.Second)
	final.Entries[370].CompletedAt = started.Add(20 * time.Second)
	review := deploymentplan.Digest(a.Plan)
	proof := managedLateAppCompletionExecution{Schema: managedLateAppCompletionSchema + "/completion", Review: review, OriginalReview: a.Plan.OriginalReview, SourceSHA: a.Plan.SourceJournalSHA, FinalSHA: deploymentplan.Digest(final), EvidenceSHA: deploymentplan.Digest(evidence), StartedAt: started, CompletedAt: started.Add(25 * time.Second)}
	completion := createAppRootCRLCompletion{Schema: createAppRootCRLSchema + "/completion", Parent: original.Plan.Parent, Review: a.Plan.OriginalReview, SourceSHA: deploymentplan.Digest(source), FinalSHA: proof.FinalSHA, EvidenceSHA: proof.EvidenceSHA, SourceEntryCount: len(source.Entries), FinalEntryCount: len(final.Entries), Completed: true, NativeEvidence: evidence, CompletionReview: review}
	store := makeIsolatedTestSecretStore(t, "dev")
	base := managedLateAppCompletionBase(a.Plan.OriginalReview, review)
	for name, value := range map[string]any{"qualification.json": a, "source-journal.json": captured, "source-owner.json": owner, "completed.json": proof} {
		lateSeedForwardWrite(t, store, filepath.Join(base, name), value)
	}
	if err := validateManagedLateAppCompletionProof(store, original, source, completion, final, owner); err != nil {
		t.Fatal("genuine selected completion proof refused", err)
	}
	for _, mode := range []string{"different-cache-observation", "different-ACK-observation", "restarted-fresh-authority", "closure-before-fresh-authority", "wrong-old-review", "missing-fresh-reference"} {
		t.Run(mode, func(t *testing.T) {
			current := cloneAppCompletionNegativeJournal(t, final)
			changedProof := proof
			changedCompletion := completion
			actual := map[string]string{}
			for k, v := range evidence {
				actual[k] = v
			}
			switch mode {
			case "different-cache-observation":
				actual["cache-"+original.Plan.Consumers[0].ID+"-"+filepath.Base(original.Plan.Consumers[0].Paths[0])] = strings.Repeat("e", 64)
			case "different-ACK-observation":
				actual["native-ACKs-"+original.Plan.Root.ID] = strings.Repeat("e", 64)
			case "restarted-fresh-authority":
				changedProof.StartedAt = a.ValidUntil
			case "closure-before-fresh-authority":
				current.Entries[372].CompletedAt = changedProof.StartedAt.Add(-time.Second)
			case "wrong-old-review":
				changedCompletion.Review = strings.Repeat("e", 64)
			case "missing-fresh-reference":
				changedCompletion.CompletionReview = ""
			}
			if mode == "different-cache-observation" || mode == "different-ACK-observation" {
				for i, target := range original.Plan.Consumers {
					current.Entries[372+i].Receipt = mustProviderRecoveryJSON(managedLateAppCompletionEvidenceForTarget(original, actual, target))
				}
				current.Entries[370].Receipt = mustProviderRecoveryJSON(appRootCRLReceipt(original, actual))
				if !managedLateAppCompletionMembership(original, source, captured, current, actual) {
					t.Fatal("forged local proof must remain structurally self-consistent to test genuine sixGO pin")
				}
			}
			changedProof.FinalSHA, changedProof.EvidenceSHA = deploymentplan.Digest(current), deploymentplan.Digest(actual)
			changedCompletion.FinalSHA, changedCompletion.EvidenceSHA, changedCompletion.NativeEvidence = changedProof.FinalSHA, changedProof.EvidenceSHA, actual
			if err := store.write(filepath.Join(base, "completed.json"), mustProviderRecoveryJSON(changedProof), true); err != nil {
				t.Fatal(err)
			}
			if err := validateManagedLateAppCompletionProof(store, original, source, changedCompletion, current, owner); err == nil {
				t.Fatal("self-consistent local proof changed the actual fresh observation or authority", mode)
			}
		})
	}
}
