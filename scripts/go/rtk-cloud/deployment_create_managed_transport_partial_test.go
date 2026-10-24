package main

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedTransportPartialSourceFixture(t *testing.T) (secretStore, *environmentCreateExecution, string) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, "dev")
	before := managedCheckpointFixture()
	before.Stack = "video-cloud-dev"
	before.PlanSHA256 = strings.Repeat("a", 64)
	before.OwnerToken = strings.Repeat("b", 64)
	a := createFoundationRecoveryArtifact{Plan: createFoundationRecoveryPlan{ParentSHA: before.PlanSHA256, ParentJournalSHA: deploymentplan.Digest(before), Materials: map[string]string{"approved": "original"}}}
	review := deploymentplan.Digest(a.Plan)
	a.Qualification = deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: review})
	}
	write := func(path string, value any) {
		if err := store.write(path, mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join("deployment/create/openbao-recovery", review, "qualification.json"), a)
	write(filepath.Join("deployment/create/openbao-recovery", review, "source-journal.json"), before)
	session := "11111111-1111-4111-8111-111111111111"
	observed := createServiceSessionObserved{Cleanup: environmentBootstrapCleanupBinding{Version: 1, Environment: "dev", SessionID: session, NamespaceUID: "22222222-2222-4222-8222-222222222222", SecretUID: "33333333-3333-4333-8333-333333333333", PolicySHA256: strings.Repeat("d", 64), KubernetesCA: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}}
	write(filepath.Join("deployment/create", before.PlanSHA256, "bootstrap/service/session-renewal", review, "observed.json"), observed)
	e := &environmentCreateExecution{Journal: before}
	e.Journal.Entries = append([]environmentCreateJournalEntry(nil), before.Entries...)
	keys := []string{"openbao-recovery-admission-" + review, "openbao-policy-rtk-dev-service-bootstrap-" + session, "openbao-role-pki-service-bootstrap-dev-qualified", "kubernetes/service-session-original-key-renewal-" + review, "kubernetes/service-custody-reader-renewal-" + review, "kubernetes/service-credential-secrets-renewal-" + review, "openbao-policy-create-" + session + "-cleanup-controller", "openbao-role-pki-controller-dev-qualified", "kubernetes/transport-enrollment-issuer", "kubernetes/transport-server-enrollment", "kubernetes/transport-managed-hosts"}
	for i, key := range keys {
		entry := environmentCreateJournalEntry{Key: key, Phase: "bootstrap", State: "completed", StartedAt: time.Now().Add(-time.Minute), CompletedAt: time.Now(), IntentSHA256: strings.Repeat("e", 64), Receipt: json.RawMessage(`{"original":true}`)}
		if i == 0 {
			entry.IntentSHA256 = deploymentplan.Digest(map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": a.Plan.ParentJournalSHA})
			entry.Receipt = mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": review})
		}
		if i == len(keys)-1 {
			entry.State, entry.CompletedAt, entry.Receipt = "pending", time.Time{}, nil
		}
		e.Journal.Entries = append(e.Journal.Entries, entry)
	}
	return store, e, review
}

func TestManagedTransportPartialSourceRequiresAuthenticatedExactBoundary(t *testing.T) {
	for _, mode := range []string{"original", "no-admission", "wrong-admission", "another-pending", "rewritten-source", "server-uncompleted", "wrong-seed-stage", "after-other-stage"} {
		t.Run(mode, func(t *testing.T) {
			store, e, review := managedTransportPartialSourceFixture(t)
			tail := len(e.Journal.Entries) - 11
			switch mode {
			case "no-admission":
				e.Journal.Entries[tail].Key = "unapproved-admission"
			case "wrong-admission":
				e.Journal.Entries[tail].Receipt = mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": strings.Repeat("f", 64)})
			case "another-pending":
				e.Journal.Entries[tail+5].State = "pending"
			case "rewritten-source":
				e.Journal.Entries[0].Receipt = json.RawMessage(`{"changed":true}`)
			case "server-uncompleted":
				e.Journal.Entries[len(e.Journal.Entries)-2].State = "pending"
			case "wrong-seed-stage":
				e.Journal.Entries[tail+3].Key = "kubernetes/service-session-original-key-renewal-" + strings.Repeat("f", 64)
			case "after-other-stage":
				e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "unknown", State: "pending", IntentSHA256: strings.Repeat("f", 64)})
			}
			_, _, got, err := loadManagedTransportPartialSource(store, e)
			if (err == nil) != (mode == "original") {
				t.Fatal("unsafe managed partial source", mode, err)
			}
			if mode == "original" {
				profile, flag := createRecoveryCheckpointProfile(e.Journal)
				if got != review || profile != "managed-transports-partial-rollout" || flag != "--after-application-enrollment-trust" {
					t.Fatal("wrong scoped recovery selection", profile, flag)
				}
			}
		})
	}
}

func managedTransportPartialPlanFixture(t *testing.T) (secretStore, *environmentCreateExecution, *deploymentplan.Plan, deploymentplan.Journal) {
	t.Helper()
	store, e, _ := managedTransportPartialSourceFixture(t)
	ns, bao := e.Journal.Stack+"-video-cloud", e.Journal.Stack+"-secrets"
	var defs []deploymentplan.Definition
	var live []deploymentplan.Object
	for _, item := range []struct{ kind, namespace, name, phase string }{{"ConfigMap", bao, "openbao-file", "configuration"}, {"Deployment", ns, "mqtt-pki", "workload"}, {"StatefulSet", bao, "openbao", "workload"}} {
		body := deploymentplan.Object{"apiVersion": "v1", "kind": item.kind, "metadata": deploymentplan.Object{"name": item.name, "namespace": item.namespace}}
		if item.kind == "ConfigMap" {
			body["data"] = deploymentplan.Object{"file.hcl": "original-managed-file-backend"}
		} else {
			body["spec"] = deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"metadata": deploymentplan.Object{}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": item.name, "image": "original-image"}}}}}
		}
		defs = append(defs, deploymentplan.Definition{Phase: item.phase, Object: body})
		actual := deploymentplan.Clone(body)
		if item.kind == "ConfigMap" {
			actual["data"] = deploymentplan.Object{"file.hcl": "prior-bootstrap-file-backend"}
		} else {
			deploymentplan.Map(actual["spec"])["replicas"] = float64(2)
		}
		metadata := deploymentplan.Map(actual["metadata"])
		metadata["uid"] = "original-" + item.name
		metadata["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": e.Journal.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}
		live = append(live, actual)
	}
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}, defs, live)
	if err != nil {
		t.Fatal(err)
	}
	cm, mqtt := "ConfigMap/"+bao+"/openbao-file", "Deployment/"+ns+"/mqtt-pki"
	journal := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Failed: mqtt, Completed: []string{cm}, Owners: map[string]string{cm: "original-openbao-file", mqtt: "original-mqtt-pki"}}
	intent, err := environmentCreateKubernetesStageIntent(plan)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		t.Fatal(err)
	}
	e.Journal.Entries[len(e.Journal.Entries)-1].IntentSHA256 = hash
	e.Journal.KubernetesLeases = []deploymentplan.LeaseOwner{{Holder: plan.SHA256() + ":native-recorded-owner"}}
	return store, e, plan, journal
}

func TestManagedTransportPartialPlanRejectsUnknownWritesOrChangedOriginalPins(t *testing.T) {
	for _, mode := range []string{"original", "no-lease", "wrong-intent", "wrong-owner", "openbao-written", "in-flight", "accepted", "acceptance-started", "wrong-failure", "completed-mqtt"} {
		t.Run(mode, func(t *testing.T) {
			store, e, plan, journal := managedTransportPartialPlanFixture(t)
			switch mode {
			case "no-lease":
				e.Journal.KubernetesLeases = nil
			case "wrong-intent":
				e.Journal.Entries[len(e.Journal.Entries)-1].IntentSHA256 = strings.Repeat("f", 64)
			case "wrong-owner":
				journal.Owners[journal.Failed] = "replacement"
			case "openbao-written":
				journal.Owners["StatefulSet/"+e.Journal.Stack+"-secrets/openbao"] = "unqualified-write"
			case "in-flight":
				journal.InFlight = journal.Failed
			case "accepted":
				journal.Accepted = true
			case "acceptance-started":
				journal.AcceptanceStarted = true
			case "wrong-failure":
				journal.Failed = "StatefulSet/" + e.Journal.Stack + "-secrets/openbao"
			case "completed-mqtt":
				journal.Completed = append(journal.Completed, journal.Failed)
			}
			for name, value := range map[string]any{"plan.json": plan, "journal.json": journal} {
				if err := store.write(filepath.Join("deployment/rollback", plan.SHA256(), name), mustProviderRecoveryJSON(value), false); err != nil {
					t.Fatal(err)
				}
			}
			got, _, err := loadOriginalManagedTransportPartialPlan(store, e)
			if (err == nil) != (mode == "original") {
				t.Fatal("unsafe original partial writes accepted", mode, err)
			}
			if mode == "original" && got.SHA256() != plan.SHA256() {
				t.Fatal("original child identity changed")
			}
		})
	}
}

func TestManagedTransportForwardKeepsOpenBaoBodiesAndOnlyTwoNewDependencies(t *testing.T) {
	for _, mode := range []string{"original", "changed-openbao", "changed-config", "missing-mqtt", "extra-owner", "extra-dependency"} {
		t.Run(mode, func(t *testing.T) {
			_, _, plan, _ := managedTransportPartialPlanFixture(t)
			var defs []deploymentplan.Definition
			for _, resource := range plan.Resources {
				body := deploymentplan.Clone(resource.Desired)
				if mode == "changed-openbao" && body["kind"] == "StatefulSet" {
					deploymentplan.Map(body["spec"])["replicas"] = float64(2)
				}
				if mode == "changed-config" && body["kind"] == "ConfigMap" {
					deploymentplan.Map(body["data"])["file.hcl"] = "replacement-store"
				}
				if mode == "missing-mqtt" && body["kind"] == "Deployment" {
					continue
				}
				defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: body})
			}
			for _, kind := range []string{"ConfigMap", "Secret"} {
				name := "mqtt-pki-config"
				if kind == "Secret" {
					name = "mqtt-pki-runtime"
				}
				defs = append(defs, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": kind, "metadata": deploymentplan.Object{"name": name, "namespace": plan.Stack + "-video-cloud"}}})
			}
			if mode == "extra-owner" {
				deploymentplan.Map(defs[len(defs)-1].Object["metadata"])["name"] = "unrelated-secret"
			}
			if mode == "extra-dependency" {
				defs = append(defs, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "extra", "namespace": plan.Stack + "-video-cloud"}}})
			}
			err := validateManagedTransportForwardDefinitions(plan, defs)
			if (err == nil) != (mode == "original") {
				t.Fatal("unsafe managed host forward", mode, err)
			}
		})
	}
}

func TestManagedTransportForwardActionsRetainThreeOwnersAndCreateTwoAbsentDependencies(t *testing.T) {
	for _, mode := range []string{"original", "replacement-owner", "existing-config", "existing-runtime", "unrelated-create", "missing-owner"} {
		t.Run(mode, func(t *testing.T) {
			_, _, original, _ := managedTransportPartialPlanFixture(t)
			candidate := *original
			candidate.Resources = slices.Clone(original.Resources)
			for index := range candidate.Resources {
				candidate.Resources[index].Action = "retain"
			}
			candidate.Resources = append(candidate.Resources,
				deploymentplan.Resource{ID: "ConfigMap/" + original.Stack + "-video-cloud/mqtt-pki-config", Action: "create"},
				deploymentplan.Resource{ID: "Secret/" + original.Stack + "-video-cloud/mqtt-pki-runtime", Action: "create"})
			switch mode {
			case "replacement-owner":
				candidate.Resources[0].UID = "replacement"
			case "existing-config":
				candidate.Resources[3].UID = "foreign-config"
				candidate.Resources[3].Action = "update"
			case "existing-runtime":
				candidate.Resources[4].Baseline = deploymentplan.Object{"data": deploymentplan.Object{"credential": "foreign"}}
			case "unrelated-create":
				candidate.Resources[4].ID = "Secret/" + original.Stack + "-video-cloud/unrelated"
			case "missing-owner":
				candidate.Resources = candidate.Resources[1:]
			}
			err := validateManagedTransportForwardActions(original, &candidate)
			if (err == nil) != (mode == "original") {
				t.Fatal("unsafe new create or replaced original owner accepted", mode, err)
			}
		})
	}
}

func TestManagedTransportPartialCredentialTrustUsesOnlyExactLaterAcceptedIssuer(t *testing.T) {
	for _, mode := range []string{"original", "later-not-accepted", "wrong-intent", "wrong-later-owner", "wrong-baseline", "unrelated-prior-stage", "unrelated-resource", "reordered-stages", "wrong-inventory"} {
		t.Run(mode, func(t *testing.T) {
			store, e, review := managedTransportPartialSourceFixture(t)
			priorKey := "kubernetes/service-credential-secrets-renewal-" + review
			id := "ConfigMap/" + e.Journal.Stack + "-account-manager/pki-create-service-trust"
			owner := "original-service-trust"
			old := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-create-service-trust", "namespace": e.Journal.Stack + "-account-manager"}, "data": deploymentplan.Object{"client-roots.pem": "original-full-trust"}}
			baseline := deploymentplan.Clone(old)
			deploymentplan.Map(baseline["metadata"])["uid"] = owner
			deploymentplan.Map(baseline["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": e.Journal.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}
			next := deploymentplan.Clone(old)
			next["data"] = deploymentplan.Object{"client-roots.pem": "accepted-enrollment-trust"}
			defs := []deploymentplan.Definition{{Phase: "configuration", Object: next}}
			for index := 0; index < 15; index++ {
				defs = append(defs, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "source-" + string(rune('a'+index)), "namespace": e.Journal.Stack + "-video-cloud"}}})
			}
			plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: e.Journal.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}, defs, []deploymentplan.Object{baseline})
			if err != nil {
				t.Fatal(err)
			}
			resource := deploymentplan.Resource{ID: id, Action: "update", UID: owner, Desired: deploymentplan.Clone(old)}
			journal := deploymentplan.Journal{Accepted: true, Owners: map[string]string{}}
			for _, item := range plan.Resources {
				journal.Completed = append(journal.Completed, item.ID)
				journal.Owners[item.ID] = "accepted-" + item.ID
				if item.ID == id {
					journal.Owners[item.ID] = owner
				}
			}
			switch mode {
			case "later-not-accepted":
				journal.Accepted = false
			case "wrong-later-owner":
				journal.Owners[id] = "replacement"
			case "wrong-baseline":
				resource.Desired["data"] = deploymentplan.Object{"client-roots.pem": "unrecorded-earlier-trust"}
			case "unrelated-prior-stage":
				priorKey = "kubernetes/service-enrollment-issuer"
			case "unrelated-resource":
				resource.ID = "ConfigMap/" + e.Journal.Stack + "-video-cloud/pki-create-service-trust"
			case "reordered-stages":
				for index := range e.Journal.Entries {
					if e.Journal.Entries[index].Key == "kubernetes/transport-enrollment-issuer" {
						e.Journal.Entries[0], e.Journal.Entries[index] = e.Journal.Entries[index], e.Journal.Entries[0]
						break
					}
				}
			case "wrong-inventory":
				plan.Resources = plan.Resources[1:]
				delete(journal.Owners, journal.Completed[0])
				journal.Completed = journal.Completed[1:]
			}
			journal.PlanSHA256 = plan.SHA256()
			intent, err := environmentCreateKubernetesStageIntent(plan)
			if err != nil {
				t.Fatal(err)
			}
			hash, err := environmentCreatePlanSHA256(intent)
			if err != nil {
				t.Fatal(err)
			}
			for index := range e.Journal.Entries {
				entry := &e.Journal.Entries[index]
				if entry.Key == "kubernetes/transport-enrollment-issuer" {
					entry.IntentSHA256 = hash
					entry.Receipt = mustProviderRecoveryJSON(map[string]string{"plan_sha256": plan.SHA256()})
					if mode == "wrong-intent" {
						entry.IntentSHA256 = strings.Repeat("f", 64)
					}
				}
			}
			for name, value := range map[string]any{"plan.json": plan, "journal.json": journal} {
				if err := store.write(filepath.Join("deployment/rollback", plan.SHA256(), name), mustProviderRecoveryJSON(value), false); err != nil {
					t.Fatal(err)
				}
			}
			materials := map[string]string{}
			expected, err := managedTransportCredentialTrustSupersession(store, e, priorKey, resource, owner, materials)
			if (err == nil) != (mode == "original") {
				t.Fatal("unqualified accepted-stage supersession", mode, err)
			}
			if mode == "original" && (!deploymentplan.Matches(next, expected) || materials["accepted-service-core-managed-partial-source-plan-kubernetes/transport-enrollment-issuer"] != plan.SHA256()) {
				t.Fatal("later accepted recipe or material binding absent")
			}
		})
	}
}

func TestManagedTransportPartialOriginalCustodyRequiresServiceAliasAdmissionAndNonemptyRawPins(t *testing.T) {
	for _, mode := range []string{"original", "missing-key-domain", "missing-csr-domain", "wrong-root-domain", "no-alias", "pending-alias", "wrong-original-plan", "wrong-alias-stage", "wrong-admission", "changed-key-proof", "changed-csr-proof", "other-renewed-session"} {
		t.Run(mode, func(t *testing.T) {
			store, e, _ := managedTransportPartialSourceFixture(t)
			before := e.Journal
			before.Entries = slices.Clone(e.Journal.Entries[:len(e.Journal.Entries)-11])
			keyHash, csrHash := strings.Repeat("c", 64), strings.Repeat("d", 64)
			materials := map[string]string{"original-service-session-plan": strings.Repeat("e", 64), "original-service-session-custody-bootstrap.key": keyHash, "original-service-session-custody-bootstrap.csr": csrHash}
			switch mode {
			case "missing-key-domain":
				delete(materials, "original-service-session-custody-bootstrap.key")
			case "missing-csr-domain":
				delete(materials, "original-service-session-custody-bootstrap.csr")
			case "wrong-root-domain":
				delete(materials, "original-service-session-custody-bootstrap.key")
				materials["original-service-root-custody-key"] = keyHash
			}
			a := createFoundationRecoveryArtifact{Plan: createFoundationRecoveryPlan{ParentSHA: before.PlanSHA256, ParentJournalSHA: deploymentplan.Digest(before), Materials: materials}}
			review := deploymentplan.Digest(a.Plan)
			a.Qualification = deploymentplan.Qualification{Decision: "GO", PlanSHA256: review}
			for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
				a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: review})
			}
			for name, value := range map[string]any{"qualification.json": a, "source-journal.json": before} {
				if err := store.write(filepath.Join("deployment/create/openbao-recovery", review, name), mustProviderRecoveryJSON(value), false); err != nil {
					t.Fatal(err)
				}
			}
			admission := environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + review, State: "completed", CompletedAt: time.Now(), IntentSHA256: deploymentplan.Digest(map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": a.Plan.ParentJournalSHA}), Receipt: mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": review})}
			alias := environmentCreateJournalEntry{Key: "kubernetes/service-issuer-server-enrollment", State: "completed", CompletedAt: time.Now(), Receipt: mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/service-issuer-server-enrollment-renewal-" + review, "original_failed_plan_sha256": strings.Repeat("e", 64), "renewed_session_id": "22222222-2222-4222-8222-222222222222"})}
			current := "33333333-3333-4333-8333-333333333333"
			proof := managedTransportPreservedClients{Key: keyHash, CSR: csrHash, Clients: strings.Repeat("f", 64), Original: "44444444-4444-4444-8444-444444444444", Renewed: current}
			switch mode {
			case "no-alias":
				alias.Key = "unrelated-stage"
			case "pending-alias":
				alias.State = "pending"
			case "wrong-original-plan":
				alias.Receipt = mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/service-issuer-server-enrollment-renewal-" + review, "original_failed_plan_sha256": strings.Repeat("f", 64), "renewed_session_id": "22222222-2222-4222-8222-222222222222"})
			case "wrong-alias-stage":
				alias.Receipt = mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/mqtt-root-trust-renewal-" + review, "original_failed_plan_sha256": strings.Repeat("e", 64), "renewed_session_id": "22222222-2222-4222-8222-222222222222"})
			case "wrong-admission":
				admission.IntentSHA256 = strings.Repeat("f", 64)
			case "changed-key-proof":
				proof.Key = strings.Repeat("a", 64)
			case "changed-csr-proof":
				proof.CSR = strings.Repeat("b", 64)
			case "other-renewed-session":
				proof.Renewed = proof.Original
			}
			e.Journal.Entries = append(e.Journal.Entries, admission, alias)
			bound := map[string]string{}
			pins, err := loadManagedTransportOriginalCustodyPins(store, e, proof, current, bound)
			if (err == nil) != (mode == "original") {
				t.Fatal("unqualified or empty original custody pin accepted", mode, err)
			}
			if mode == "original" && (pins["bootstrap.key"] != keyHash || pins["bootstrap.csr"] != csrHash || !environmentPreflightDigest.MatchString(bound["accepted-service-core-managed-partial-original-custody-admission"])) {
				t.Fatal("original admitted Service key/CSR pins absent")
			}
		})
	}
}

func TestManagedTransportPartialHostVolumesRequireOriginalLedgerAndNativeBinding(t *testing.T) {
	for _, name := range []string{"mqtt-pki-data", "mqtt-pki-trust", "mqtt-pki-host-identity", "openbao-pki-identity", "data-openbao-0", "audit-openbao-0"} {
		t.Run(name, func(t *testing.T) {
			for _, mode := range []string{"original", "missing-ledger", "duplicate-ledger", "conflicting-owner", "changed-pvc-uid", "changed-pv-uid", "changed-volume-name", "changed-claim-ref", "changed-native-id", "foreign-driver", "unbound-pvc", "unbound-pv", "terminating-pvc", "missing-pv"} {
				t.Run(mode, func(t *testing.T) {
					namespace := "video-cloud-dev-video-cloud"
					if strings.Contains(name, "openbao") {
						namespace = "video-cloud-dev-secrets"
					}
					owner := environmentCreateVolumeOwner{NativeID: "123", PVName: "original-pv", PVUID: "original-pv-uid", PVCName: name, PVCUID: "original-pvc-uid", Namespace: namespace}
					id := "PersistentVolumeClaim/" + namespace + "/" + name
					ledger := environmentCreateResourceLedger{Kubernetes: map[string]string{}, Volumes: []environmentCreateVolumeOwner{owner}}
					claim := deploymentplan.Object{"kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"namespace": namespace, "name": name, "uid": owner.PVCUID}, "spec": deploymentplan.Object{"volumeName": owner.PVName}, "status": deploymentplan.Object{"phase": "Bound"}}
					volume := deploymentplan.Object{"kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": owner.PVName, "uid": owner.PVUID}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"namespace": namespace, "name": name, "uid": owner.PVCUID}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": "123-pvc-original"}}, "status": deploymentplan.Object{"phase": "Bound"}}
					switch mode {
					case "missing-ledger":
						ledger.Volumes = nil
					case "duplicate-ledger":
						ledger.Volumes = append(ledger.Volumes, owner)
					case "conflicting-owner":
						ledger.Kubernetes[id] = "replacement"
					case "changed-pvc-uid":
						deploymentplan.Map(claim["metadata"])["uid"] = "replacement"
					case "changed-pv-uid":
						deploymentplan.Map(volume["metadata"])["uid"] = "replacement"
					case "changed-volume-name":
						deploymentplan.Map(claim["spec"])["volumeName"] = "replacement-pv"
					case "changed-claim-ref":
						deploymentplan.Map(deploymentplan.Map(volume["spec"])["claimRef"])["uid"] = "replacement"
					case "changed-native-id":
						deploymentplan.Map(deploymentplan.Map(volume["spec"])["csi"])["volumeHandle"] = "456-pvc-original"
					case "foreign-driver":
						deploymentplan.Map(deploymentplan.Map(volume["spec"])["csi"])["driver"] = "foreign-driver"
					case "unbound-pvc":
						deploymentplan.Map(claim["status"])["phase"] = "Pending"
					case "unbound-pv":
						deploymentplan.Map(volume["status"])["phase"] = "Released"
					case "terminating-pvc":
						deploymentplan.Map(claim["metadata"])["deletionTimestamp"] = "2026-10-13T00:00:00Z"
					case "missing-pv":
						volume = nil
					}
					got, err := managedTransportRecordedHostVolume(ledger, id)
					if err == nil {
						err = validateManagedTransportHostVolume(got, claim, volume)
					}
					if (err == nil) != (mode == "original") {
						t.Fatal("unrecorded or replaced original storage accepted", mode, err)
					}
					if mode == "original" && got != owner {
						t.Fatal("original volume custody changed")
					}
				})
			}
		})
	}
}

func TestManagedTransportPartialVolumeLedgerRequiresMatchingActivePointer(t *testing.T) {
	for _, mode := range []string{"original", "foreign-plan", "foreign-environment", "foreign-stack", "empty-digest", "changed-ledger"} {
		t.Run(mode, func(t *testing.T) {
			ledger := environmentCreateResourceLedger{Schema: "rtk-cloud.resource-ledger/v1", Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: strings.Repeat("a", 64), Volumes: []environmentCreateVolumeOwner{{NativeID: "123", PVName: "original-pv", PVUID: "original-pv-uid", PVCName: "data-openbao-0", PVCUID: "original-pvc-uid", Namespace: "video-cloud-dev-secrets"}}}
			active := managedTransportVolumeLedgerActive{Schema: "rtk-cloud.resource-ledger-active/v1", Environment: ledger.Environment, Stack: ledger.Stack, Plan: ledger.PlanSHA256, LedgerSHA: deploymentplan.Digest(ledger)}
			switch mode {
			case "foreign-plan":
				active.Plan = strings.Repeat("b", 64)
			case "foreign-environment":
				active.Environment = "staging"
			case "foreign-stack":
				active.Stack = "foreign-stack"
			case "empty-digest":
				active.LedgerSHA = ""
			case "changed-ledger":
				ledger.Volumes[0].PVCUID = "replacement"
			}
			if err := validateManagedTransportVolumeLedger(ledger, active); (err == nil) != (mode == "original") {
				t.Fatal("unauthenticated original volume ledger accepted", mode, err)
			}
		})
	}
}

func TestManagedTransportPartialFoundationOverrideRequiresExactRecordedConfigurationTransition(t *testing.T) {
	for _, mode := range []string{"original", "no-partial", "other-resource", "replacement-owner", "changed-foundation", "changed-source-baseline", "configuration-not-completed", "wrong-failure", "accepted-original", "in-flight"} {
		t.Run(mode, func(t *testing.T) {
			_, _, plan, journal := managedTransportPartialPlanFixture(t)
			partial := &createManagedTransportPartialRecovery{Original: plan, Journal: journal}
			var source deploymentplan.Resource
			for _, resource := range plan.Resources {
				if resource.ID == "ConfigMap/"+plan.Stack+"-secrets/openbao-file" {
					source = resource
				}
			}
			id, owner, expected := source.ID, source.UID, deploymentplan.Clone(source.Baseline)
			switch mode {
			case "no-partial":
				partial = nil
			case "other-resource":
				id = "ConfigMap/" + plan.Stack + "-secrets/unrelated"
			case "replacement-owner":
				owner = "replacement"
			case "changed-foundation":
				expected["data"] = deploymentplan.Object{"file.hcl": "unqualified-earlier-foundation"}
			case "changed-source-baseline":
				for index := range plan.Resources {
					if plan.Resources[index].ID == id {
						plan.Resources[index].Baseline["data"] = deploymentplan.Object{"file.hcl": "unqualified-source-baseline"}
					}
				}
				partial.Journal.PlanSHA256 = plan.SHA256()
			case "configuration-not-completed":
				partial.Journal.Completed = nil
			case "wrong-failure":
				partial.Journal.Failed = "StatefulSet/" + plan.Stack + "-secrets/openbao"
			case "accepted-original":
				partial.Journal.Accepted = true
			case "in-flight":
				partial.Journal.InFlight = id
			}
			desired, changed, err := managedTransportPartialFoundationOverride(partial, id, owner, expected)
			wantOverride := mode == "original"
			wantPass := wantOverride || mode == "no-partial" || mode == "other-resource"
			if (err == nil) != wantPass || changed != wantOverride {
				t.Fatal("unqualified foundation override", mode, changed, err)
			}
			if wantOverride && deploymentplan.Digest(desired) != deploymentplan.Digest(source.Desired) || wantPass && !wantOverride && deploymentplan.Digest(desired) != deploymentplan.Digest(expected) {
				t.Fatal("recorded desired or ordinary foundation recipe changed")
			}
		})
	}
}
