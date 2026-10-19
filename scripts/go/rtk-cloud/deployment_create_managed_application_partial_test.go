package main

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedApplicationPartialFixture(t *testing.T) (secretStore, *environmentCreateExecution, *environmentCreateExecution, environmentCreateRegisteredIssuer) {
	t.Helper()
	store, e, prior := managedTransportExportRetirementFixture(t)
	cert64, key64 := frozenClientIdentity(t, time.Now().Add(time.Hour), x509.ExtKeyUsageServerAuth)
	cert, _ := base64.StdEncoding.DecodeString(cert64)
	key, _ := base64.StdEncoding.DecodeString(key64)
	certificate := string(cert) + string(cert)
	var public struct {
		Root         map[string]any `json:"root"`
		Intermediate map[string]any `json:"intermediate"`
	}
	if readExecutionJSON(filepath.Join(store.Root, "pki/service/issuer.json"), &public) != nil {
		t.Fatal("fixture publication missing")
	}
	root, err := environmentCreateRegisteredIssuerFromMap(public.Root, "")
	if err != nil {
		t.Fatal(err)
	}
	var export deploymentplan.Object
	if json.Unmarshal(e.Journal.Entries[len(e.Journal.Entries)-2].Receipt, &export) != nil {
		t.Fatal("fixture export missing")
	}
	export["certificate_sha256"] = environmentPreflightBytesSHA256([]byte(certificate))
	e.Journal.Entries[len(e.Journal.Entries)-2].Receipt = mustProviderRecoveryJSON(export)
	source := *e
	source.Journal = e.Journal
	source.Journal.Entries = slices.Clone(e.Journal.Entries)
	source.Journal.KubernetesLeases = slices.Clone(e.Journal.KubernetesLeases)
	// The accepted all-retain transport preview is the only prospective stage in
	// the original admitted source. It does not replay any managed host.
	defs, live := []deploymentplan.Definition{}, []deploymentplan.Object{}
	for _, resource := range prior.PostManaged.Plan.Resources {
		actual := deploymentplan.Clone(resource.Desired)
		deploymentplan.Map(actual["metadata"])["uid"] = resource.UID
		defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: resource.Desired, Dependencies: resource.Dependencies})
		live = append(live, actual)
	}
	preview, err := deploymentplan.Compile(prior.Original.Inputs, defs, live)
	if err != nil {
		t.Fatal(err)
	}
	admission := createFoundationRecoveryArtifact{Plan: createFoundationRecoveryPlan{Schema: "rtk-cloud.create-managed-transports-partial-recovery/v1", ParentSHA: e.Journal.PlanSHA256, ParentJournalSHA: deploymentplan.Digest(source.Journal), Materials: map[string]string{"accepted-service-core-managed-partial-native-proof": strings.Repeat("b", 64), "accepted-service-core-managed-partial-forward-plan": preview.SHA256(), "accepted-service-core-managed-partial-original-volume-ledger": strings.Repeat("c", 64)}}}
	review := deploymentplan.Digest(admission.Plan)
	admission.Qualification = deploymentplan.Qualification{Decision: "GO", PlanSHA256: review}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		admission.Qualification.Gates = append(admission.Qualification.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: review})
	}
	write := func(path string, value any) {
		if err := store.write(path, mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join("deployment/create/openbao-recovery", review)
	write(filepath.Join(dir, "qualification.json"), admission)
	write(filepath.Join(dir, "source-journal.json"), source.Journal)
	write(filepath.Join(dir, "openbao-root-trust-plan.json"), preview)
	retirement := &e.Journal.Entries[len(e.Journal.Entries)-1]
	retirement.State = "completed"
	retirement.CompletedAt = time.Now()
	retirement.Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"resource_id": "Deployment/" + e.Journal.Stack + "-video-cloud/pki-export-video-cloud-api-pki-" + e.Journal.PlanSHA256[:12], "uid": export["reader_uid"], "absent": true})
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + review, Phase: "recovery", State: "completed", CompletedAt: time.Now(), IntentSHA256: deploymentplan.Digest(map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(admission), "original_parent_journal_sha256": admission.Plan.ParentJournalSHA}), Receipt: mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": review})})
	appendAccepted := func(stage string, plan *deploymentplan.Plan, uid string) {
		journal := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Accepted: true, AcceptanceStarted: true, Owners: map[string]string{plan.Resources[0].ID: uid}, Completed: []string{plan.Resources[0].ID}}
		intent, err := environmentCreateKubernetesStageIntent(plan)
		if err != nil {
			t.Fatal(err)
		}
		hash, err := environmentCreatePlanSHA256(intent)
		if err != nil {
			t.Fatal(err)
		}
		e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "kubernetes/" + stage, Phase: stage, State: "completed", CompletedAt: time.Now(), IntentSHA256: hash, Receipt: mustProviderRecoveryJSON(deploymentplan.Object{"plan_sha256": plan.SHA256(), "owners": journal.Owners})})
		e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, deploymentplan.LeaseOwner{ResourceID: "Lease/" + e.Journal.Stack + "-video-cloud/rtk-deployment-execution", UID: "genuine-" + stage, Holder: plan.SHA256() + ":owned"})
		write(filepath.Join("deployment/rollback", plan.SHA256(), "plan.json"), plan)
		write(filepath.Join("deployment/rollback", plan.SHA256(), "journal.json"), journal)
	}
	ns := e.Journal.Stack + "-video-cloud"
	single := func(kind, name, phase, action, uid string, body deploymentplan.Object) *deploymentplan.Plan {
		if body == nil {
			body = deploymentplan.Object{"apiVersion": "v1", "kind": kind, "metadata": deploymentplan.Object{"name": name, "namespace": ns}}
		}
		resource := deploymentplan.Resource{ID: kind + "/" + ns + "/" + name, Phase: phase, Action: action, Desired: body, UID: uid}
		if uid != "" {
			resource.Baseline = deploymentplan.Clone(body)
		}
		return &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: prior.Original.Inputs, Resources: []deploymentplan.Resource{resource}}
	}
	apiReader := prior.PostManaged.ExportPlans["kubernetes/server-custody-video-cloud-api-pki"].Resources[0]
	retirePlan := single("Deployment", "pki-export-video-cloud-api-pki-"+e.Journal.PlanSHA256[:12], "retirement", "retire", deploymentplan.String(export["reader_uid"]), apiReader.Desired)
	appendAccepted("retire-pki-export-video-cloud-api-pki-"+e.Journal.PlanSHA256[:12], retirePlan, deploymentplan.String(export["reader_uid"]))
	secret := func(name string) *deploymentplan.Plan {
		definition := environmentCreateSecret(ns, name+"-tls", map[string]string{"tls.crt": certificate, "tls.key": string(key), "ca.crt": root.Certificate})
		plan, err := deploymentplan.Compile(prior.Original.Inputs, []deploymentplan.Definition{definition}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	appendAccepted("server-secret-video-cloud-api-pki", secret("video-cloud-api-pki"), "11111111-1111-4111-8111-111111111111")
	appendAccepted("server-enrollment-video-cloud-api-app-pki", single("Job", "pki-enroll-video-cloud-api-app-pki-server-"+strings.ReplaceAll(prior.PostManaged.Session, "-", "")[:12], "bootstrap", "create", "", nil), "22222222-2222-4222-8222-222222222222")
	appReader := single("Deployment", "pki-export-video-cloud-api-app-pki-"+e.Journal.PlanSHA256[:12], "bootstrap", "create", "", nil)
	readerUID := "33333333-3333-4333-8333-333333333333"
	appendAccepted("server-custody-video-cloud-api-app-pki", appReader, readerUID)
	intent := deploymentplan.Object{"owner": "video-cloud-api-app-pki", "namespace": ns, "claim_uid": "44444444-4444-4444-8444-444444444444", "reader_uid": readerUID, "pod_uid": "55555555-5555-4555-8555-555555555555", "replicaset_uid": "66666666-6666-4666-8666-666666666666", "server_name": "video-cloud-api-app-pki." + ns + ".svc", "root_sha256": root.RootFingerprint}
	appProof := deploymentplan.Clone(intent)
	delete(appProof, "owner")
	delete(appProof, "namespace")
	delete(appProof, "root_sha256")
	appProof["certificate_sha256"] = environmentPreflightBytesSHA256([]byte(certificate))
	appRetire := deploymentplan.Object{"resource_id": appReader.Resources[0].ID, "uid": readerUID}
	receipt := deploymentplan.Clone(appRetire)
	receipt["absent"] = true
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "server-export-video-cloud-api-app-pki", Phase: "bootstrap", State: "completed", CompletedAt: time.Now(), IntentSHA256: activeTransportJournalDigest(intent), Receipt: mustProviderRecoveryJSON(appProof)}, environmentCreateJournalEntry{Key: "retire-pki-export-video-cloud-api-app-pki-" + e.Journal.PlanSHA256[:12], Phase: "bootstrap", State: "completed", CompletedAt: time.Now(), IntentSHA256: activeTransportJournalDigest(appRetire), Receipt: mustProviderRecoveryJSON(receipt)})
	appendAccepted("retire-pki-export-video-cloud-api-app-pki-"+e.Journal.PlanSHA256[:12], single("Deployment", "pki-export-video-cloud-api-app-pki-"+e.Journal.PlanSHA256[:12], "retirement", "retire", readerUID, appReader.Resources[0].Desired), readerUID)
	appendAccepted("server-secret-video-cloud-api-app-pki", secret("video-cloud-api-app-pki"), "77777777-7777-4777-8777-777777777777")
	ids := managedApplicationPartialResourceIDs(ns)
	actions := []string{"create", "create", "create", "create", "update", "update", "create", "update", "create", "create", "create", "create", "retain", "retain", "retain", "retain", "create", "retain", "create", "create", "create", "update", "update", "update", "update", "update"}
	plan := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: prior.Original.Inputs}
	journal := deploymentplan.Journal{Owners: map[string]string{}, Failed: ids[22]}
	for index, id := range ids {
		parts := strings.Split(id, "/")
		body := deploymentplan.Object{"apiVersion": "v1", "kind": parts[0], "metadata": deploymentplan.Object{"name": parts[2], "namespace": ns}}
		phase := "configuration"
		if index < 2 {
			phase = "storage"
		} else if index < 4 {
			phase = "bootstrap"
		} else if index >= 20 {
			phase = "workload"
		}
		resource := deploymentplan.Resource{ID: id, Phase: phase, Action: actions[index], Desired: body}
		uid := fmt.Sprintf("%08d-1111-4111-8111-111111111111", index+1)
		if actions[index] != "create" {
			resource.UID = uid
			resource.Baseline = deploymentplan.Clone(body)
		}
		plan.Resources = append(plan.Resources, resource)
		if index < 23 {
			journal.Owners[id] = uid
		}
		if index < 22 {
			journal.Completed = append(journal.Completed, id)
		}
	}
	journal.PlanSHA256 = plan.SHA256()
	pendingIntent, err := environmentCreateKubernetesStageIntent(plan)
	if err != nil {
		t.Fatal(err)
	}
	pendingSHA, err := environmentCreatePlanSHA256(pendingIntent)
	if err != nil {
		t.Fatal(err)
	}
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "kubernetes/managed-application-owners", Phase: "managed-application-owners", State: "pending", StartedAt: time.Now(), IntentSHA256: pendingSHA})
	e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, deploymentplan.LeaseOwner{ResourceID: "Lease/" + ns + "/rtk-deployment-execution", UID: "genuine-application-lease", Holder: plan.SHA256() + ":owned"})
	write(filepath.Join("deployment/rollback", plan.SHA256(), "plan.json"), plan)
	write(filepath.Join("deployment/rollback", plan.SHA256(), "journal.json"), journal)
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", store.ConfigRoot)
	return store, e, &source, root
}

func TestManagedApplicationPartialAuthenticatesOnlyActualCompletedExportAndPartialApplicationSuffix(t *testing.T) {
	for _, mode := range []string{"original", "prefix", "metadata", "admission", "extra-lease", "lease-holder", "api-retire-owner", "completed-api-retire-changed-intent", "server-child-receipt", "partial-intent", "extra-entry"} {
		t.Run(mode, func(t *testing.T) {
			store, e, source, _ := managedApplicationPartialFixture(t)
			start := len(source.Journal.Entries)
			switch mode {
			case "prefix":
				e.Journal.Entries[0].IntentSHA256 = strings.Repeat("0", 64)
			case "metadata":
				e.Journal.OriginalJournalSHA256 = strings.Repeat("0", 64)
			case "admission":
				e.Journal.Entries[start].IntentSHA256 = strings.Repeat("0", 64)
			case "extra-lease":
				e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1])
			case "lease-holder":
				e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1].Holder = strings.Repeat("0", 64) + ":foreign"
			case "api-retire-owner":
				e.Journal.Entries[start-1].Receipt = []byte(`{"absent":false}`)
			case "completed-api-retire-changed-intent":
				e.Journal.Entries[start-1].IntentSHA256 = strings.Repeat("0", 64)
			case "server-child-receipt":
				e.Journal.Entries[start+3].Receipt = []byte(`{"plan_sha256":"foreign","owners":{}}`)
			case "partial-intent":
				e.Journal.Entries[len(e.Journal.Entries)-1].IntentSHA256 = strings.Repeat("0", 64)
			case "extra-entry":
				e.Journal.Entries = append(e.Journal.Entries, e.Journal.Entries[len(e.Journal.Entries)-1])
			}
			historical, _, partial, err := loadManagedApplicationPartialSource(store, e)
			if (err == nil) != (mode == "original") {
				t.Fatal("unqualified application source accepted", mode, err)
			}
			if mode == "original" {
				if deploymentplan.Digest(historical.Journal) != deploymentplan.Digest(source.Journal) || len(partial.AcceptedPlans) != 6 || len(partial.Journal.Completed) != 22 {
					t.Fatal("genuine completed ancestry lost")
				}
				profile, flag := createRecoveryCheckpointProfile(e.Journal)
				if profile != "managed-application-partial-rollout" || flag != "--after-application-enrollment-trust" {
					t.Fatal("wrong profile", profile, flag)
				}
			}
		})
	}
}

func TestManagedApplicationPartialRejectsOtherWorkloadProgressAndNewOwners(t *testing.T) {
	store, e, _, _ := managedApplicationPartialFixture(t)
	_, _, partial, err := loadManagedApplicationPartialSource(store, e)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"original", "accepted", "acceptance-started", "inflight", "other-failure", "more-completed", "foreign-written-owner", "unwritten-owner", "other-resource", "changed-original-uid", "wrong-action"} {
		t.Run(mode, func(t *testing.T) {
			var plan deploymentplan.Plan
			var j deploymentplan.Journal
			if json.Unmarshal(mustProviderRecoveryJSON(partial.Plan), &plan) != nil || json.Unmarshal(mustProviderRecoveryJSON(partial.Journal), &j) != nil {
				t.Fatal("fixture cloning failed")
			}
			switch mode {
			case "accepted":
				j.Accepted = true
			case "acceptance-started":
				j.AcceptanceStarted = true
			case "inflight":
				j.InFlight = j.Failed
			case "other-failure":
				j.Failed = plan.Resources[23].ID
			case "more-completed":
				j.Completed = append(j.Completed, j.Failed)
			case "foreign-written-owner":
				j.Owners[j.Failed] = "foreign"
			case "unwritten-owner":
				j.Owners[plan.Resources[23].ID] = plan.Resources[23].UID
			case "other-resource":
				plan.Resources[0].ID = "PersistentVolumeClaim/foreign/foreign"
			case "changed-original-uid":
				plan.Resources[22].UID = "88888888-8888-4888-8888-888888888888"
			case "wrong-action":
				plan.Resources[0].Action = "update"
			}
			j.PlanSHA256 = plan.SHA256()
			if (validateManagedApplicationPartialJournal(&plan, j) == nil) != (mode == "original") {
				t.Fatal("changed partial application progress accepted", mode)
			}
		})
	}
}

func TestManagedApplicationPartialPureHistoryPreservesExportsAfterLegitimateLedgerRecapture(t *testing.T) {
	store, e, source, root := managedApplicationPartialFixture(t)
	// Operational ledger metadata can have legitimately advanced after six
	// accepted child stages. It is not replaced with an invented old snapshot.
	var ledger environmentCreateResourceLedger
	if readExecutionJSON(filepath.Join(store.Root, environmentCreateLedgerPath(source)), &ledger) != nil {
		t.Fatal("fixture ledger missing")
	}
	ledger.UpdatedAt = time.Now().Add(time.Minute)
	active := managedTransportVolumeLedgerActive{Schema: "rtk-cloud.resource-ledger-active/v1", Environment: ledger.Environment, Stack: ledger.Stack, Plan: ledger.PlanSHA256, LedgerSHA: deploymentplan.Digest(ledger)}
	if store.write(environmentCreateLedgerPath(source), mustProviderRecoveryJSON(ledger), true) != nil || store.write("deployment/resources/active.json", mustProviderRecoveryJSON(active), true) != nil {
		t.Fatal("fixture recapture failed")
	}
	if _, _, _, _, err := loadManagedTransportExportRetirementSource(store, source, false); err == nil {
		t.Fatal("old live ledger was silently rebound")
	}
	base, prior, accepted, post, err := loadManagedTransportExportRetirementSource(store, source, true)
	if err != nil {
		t.Fatal("immutable historical source depended on newer live ledger", err)
	}
	_, _, partial, err := loadManagedApplicationPartialSource(store, e)
	if err != nil {
		t.Fatal(err)
	}
	original, journal, err := loadOriginalManagedTransportPartialPlan(store, base)
	if err != nil {
		t.Fatal(err)
	}
	partial.Historical = &createManagedTransportPartialRecovery{Original: original, Journal: journal, PriorForward: prior, AcceptedForward: accepted, PostManaged: post}
	if err := validateManagedApplicationPartialExports(store, e, source, partial, root); err != nil {
		t.Fatal("genuine retained exports rejected", err)
	}
	if len(partial.Exports) != 2 || !managedApplicationPartialNativeCheckpoint(store, e) {
		t.Fatal("pure source lost exact serving Secret and retired reader proof")
	}
	before := deploymentplan.Digest(e.Journal)
	if _, _, _, _, err := loadManagedTransportExportRetirementSource(store, source, true); err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Digest(e.Journal) != before {
		t.Fatal("historical derivation rewrote current receipts")
	}
}

func TestManagedApplicationPartialRetainedSecretsRequireExactExportCertificateAndOriginalKey(t *testing.T) {
	store, e, source, root := managedApplicationPartialFixture(t)
	_, _, partial, err := loadManagedApplicationPartialSource(store, e)
	if err != nil {
		t.Fatal(err)
	}
	base, prior, accepted, post, err := loadManagedTransportExportRetirementSource(store, source, true)
	if err != nil {
		t.Fatal(err)
	}
	original, j, err := loadOriginalManagedTransportPartialPlan(store, base)
	if err != nil {
		t.Fatal(err)
	}
	partial.Historical = &createManagedTransportPartialRecovery{Original: original, Journal: j, PriorForward: prior, AcceptedForward: accepted, PostManaged: post}
	_, otherKey64 := frozenClientIdentity(t, time.Now().Add(time.Hour), x509.ExtKeyUsageServerAuth)
	for _, mode := range []string{"original", "wrong-certificate", "different-private-key", "wrong-ca", "extra-private-field", "foreign-secret", "different-reader", "retirement-replaced-owner", "changed-export-root"} {
		t.Run(mode, func(t *testing.T) {
			var cloned createManagedApplicationPartialRecovery
			if json.Unmarshal(mustProviderRecoveryJSON(partial), &cloned) != nil {
				t.Fatal("fixture clone failed")
			}
			current := *e
			current.Journal = e.Journal
			current.Journal.Entries = slices.Clone(e.Journal.Entries)
			resource := &cloned.AcceptedPlans["kubernetes/server-secret-video-cloud-api-app-pki"].Resources[0]
			data := deploymentplan.Map(resource.Desired["data"])
			switch mode {
			case "wrong-certificate":
				data["tls.crt"] = base64.StdEncoding.EncodeToString([]byte("private-marker"))
			case "different-private-key":
				data["tls.key"] = otherKey64
			case "wrong-ca":
				data["ca.crt"] = base64.StdEncoding.EncodeToString([]byte("foreign"))
			case "extra-private-field":
				data["private-marker"] = "foreign"
			case "foreign-secret":
				resource.ID = "Secret/foreign/foreign"
			case "different-reader", "changed-export-root":
				at := len(source.Journal.Entries) + 5
				var proof deploymentplan.Object
				if json.Unmarshal(current.Journal.Entries[at].Receipt, &proof) != nil {
					t.Fatal("proof missing")
				}
				if mode == "different-reader" {
					proof["reader_uid"] = "88888888-8888-4888-8888-888888888888"
				} else {
					current.Journal.Entries[at].IntentSHA256 = strings.Repeat("0", 64)
				}
				current.Journal.Entries[at].Receipt = mustProviderRecoveryJSON(proof)
			case "retirement-replaced-owner":
				cloned.AcceptedPlans["kubernetes/retire-pki-export-video-cloud-api-app-pki-"+e.Journal.PlanSHA256[:12]].Resources[0].UID = "88888888-8888-4888-8888-888888888888"
			}
			err := validateManagedApplicationPartialExports(store, &current, source, &cloned, root)
			if (err == nil) != (mode == "original") {
				t.Fatal("unqualified retained serving material accepted", mode, err)
			}
			if err != nil && strings.Contains(err.Error(), "private-marker") {
				t.Fatal("private native bytes leaked")
			}
		})
	}
}

func TestManagedApplicationPartialRejectsShortAndRepeatedJournals(t *testing.T) {
	for length := 0; length < 14; length++ {
		if managedApplicationPartialReview(environmentCreateJournal{PlanSHA256: strings.Repeat("a", 64), Entries: make([]environmentCreateJournalEntry, length)}) != "" {
			t.Fatal("short journal selected")
		}
	}
	_, e, _, _ := managedApplicationPartialFixture(t)
	e.Journal.Entries = append(e.Journal.Entries, e.Journal.Entries[len(e.Journal.Entries)-10:]...)
	if managedApplicationPartialReview(e.Journal) != "" {
		t.Fatal("repeated bootstrap/exports admitted")
	}
}
