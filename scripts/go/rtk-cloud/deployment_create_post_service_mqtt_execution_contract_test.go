package main

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type postServiceMQTTExecutionContract struct {
	*acceptedCoreExecutionContract
	identityReview  string
	original        *deploymentplan.Plan
	originalJournal deploymentplan.Journal
	definitions     []deploymentplan.Definition
}

type coreContractReadinessFailure struct {
	*normalTailE2EBackend
	target string
}

func (b coreContractReadinessFailure) Wait(ctx context.Context, resource deploymentplan.Resource) error {
	if err := b.normalTailE2EBackend.waitObserved(ctx, resource); err != nil {
		return err
	}
	if resource.ID == b.target {
		return errors.New("isolated original native readiness failure")
	}
	b.recordJobCompletion(resource)
	return nil
}

func (f *acceptedCoreExecutionContract) interruptedStage(t *testing.T, key string, defs []deploymentplan.Definition, failed string, historicalRetain ...bool) (*deploymentplan.Plan, deploymentplan.Journal) {
	t.Helper()
	var observed []deploymentplan.Object
	for _, v := range f.native.objects {
		observed = append(observed, v)
	}
	p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: f.cfg.Environment, Stack: f.e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: f.p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(f.p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA(key, defs), CreateParentSHA256: f.e.Journal.PlanSHA256}, defs, observed)
	if err != nil {
		t.Fatal(key, err)
	}
	intent, err := environmentCreateKubernetesStageIntent(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.e.BeginEntry(key, "bootstrap", intent, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.e.Check(ctx) }
	executionErr := p.Execute(context.Background(), coreContractReadinessFailure{f.native, failed}, p.SHA256(), f.store.Root, check, check, check)
	if executionErr == nil {
		t.Fatal("native interruption did not occur", key)
	}
	var j deploymentplan.Journal
	if err := readExecutionJSON(filepath.Join(f.store.Root, "deployment/rollback", p.SHA256(), "journal.json"), &j); err != nil {
		t.Fatal(key, "child evidence:", err, "execution:", executionErr)
	}
	// The old serial producer persisted a retained target UID before Wait;
	// the current executor does so after an immediate Wait. Recreate only that
	// documented historical field from the actual matched native readback.
	if len(historicalRetain) == 1 && historicalRetain[0] && j.Failed == failed && j.Owners[failed] == "" {
		for _, resource := range p.Resources {
			if resource.ID == failed && resource.Action == "retain" {
				actual := f.native.objects[failed]
				if actual == nil || resource.UID == "" || deploymentplan.Map(actual["metadata"])["uid"] != resource.UID || !deploymentplan.Matches(resource.Desired, actual) {
					t.Fatal("historical retained readback changed")
				}
				j.Owners[failed] = resource.UID
				f.write(t, filepath.Join("deployment/rollback", p.SHA256(), "journal.json"), j)
			}
		}
	}
	if j.Failed != failed || j.Owners[failed] == "" || j.InFlight != "" || j.Accepted || j.AcceptanceStarted || f.native.held {
		t.Fatalf("native interruption lost original known write boundary %s: failed=%s owners=%d completed=%d unknown=%s accepted=%v acceptance=%v held=%v error=%v", key, j.Failed, len(j.Owners), len(j.Completed), j.InFlight, j.Accepted, j.AcceptanceStarted, f.native.held, executionErr)
	}
	return p, j
}

func (f *acceptedCoreExecutionContract) admitRecovery(t *testing.T, schema string, materials map[string]string) (string, createFoundationRecoveryArtifact) {
	t.Helper()
	before := f.e.Journal
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		t.Fatal("historical recovery checker identity", err)
	}
	kubeconfigSHA := ""
	if len(f.backend.KubeconfigContent) > 0 {
		kubeconfig, err := readBoundedEnvironmentFile(f.store.KubeconfigPath(), true)
		if err != nil || environmentPreflightBytesSHA256(kubeconfig) != environmentPreflightBytesSHA256(f.backend.KubeconfigContent) {
			t.Fatal("historical recovery selected kubeconfig identity", err)
		}
		kubeconfigSHA = environmentPreflightBytesSHA256(kubeconfig)
	}
	p := createFoundationRecoveryPlan{Schema: schema, ParentSHA: f.e.Journal.PlanSHA256, ParentOwnerSHA: deploymentplan.Digest(f.e.owner), ParentJournalSHA: deploymentplan.Digest(before), CheckerSHA: checker, KubeconfigSHA: kubeconfigSHA, Plan: f.p.Plan, Materials: materials}
	review := deploymentplan.Digest(p)
	q := deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		q.Gates = append(q.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: review})
	}
	now := time.Now().UTC()
	a := createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-openbao-recovery-artifact/v1", Plan: p, Qualification: q, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
	base := filepath.Join("deployment/create/openbao-recovery", review)
	f.write(t, filepath.Join(base, "qualification.json"), a)
	f.write(t, filepath.Join(base, "source-journal.json"), before)
	key := "openbao-recovery-admission-" + review
	intent := map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": p.ParentJournalSHA}
	if err := f.e.BeginEntry(key, "recovery", mustProviderRecoveryJSON(intent), now); err != nil {
		t.Fatal(err)
	}
	receipt := map[string]string{"qualified_recovery_sha256": review}
	if schema == managedCorePartialSchema {
		audit, err := managedCoreOriginalAuditSHA(before)
		if err != nil {
			t.Fatal("historical Core recovery audit identity", err)
		}
		receipt["audit_sha256"] = audit
	}
	if err := f.e.RecordReceipt(key, mustProviderRecoveryJSON(receipt)); err != nil {
		t.Fatal(err)
	}
	if err := f.e.CompleteEntry(key, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return review, a
}

func newPostServiceMQTTExecutionContract(t *testing.T) *postServiceMQTTExecutionContract {
	t.Helper()
	return newPostServiceMQTTExecutionContractFromCore(t, newAcceptedCoreExecutionContract(t), nil)
}

// A later full-history fixture may select its genuine Core and MQTT authority
// before these source records are frozen; existing focused callers retain the
// original small Core/MQTT model.
func newPostServiceMQTTExecutionContractFromCore(t *testing.T, f *acceptedCoreExecutionContract, selectedRoot *environmentCreateRegisteredIssuer) *postServiceMQTTExecutionContract {
	t.Helper()
	p := f.p.Plan.Bootstrap
	apps, err := renderEnvironmentCreateApplicationEnrollment(f.cfg, p, f.runtime, f.service)
	if err != nil {
		t.Fatal(err)
	}
	failed := ""
	for _, d := range apps {
		if strings.HasPrefix(deploymentplan.ID(d.Object), "Job/"+p.Stack+"-video-cloud/pki-enroll-factoryenroll-client-") {
			failed = deploymentplan.ID(d.Object)
		}
	}
	originalApp, j := f.interruptedStage(t, "kubernetes/application-client-enrollment", apps, failed)
	if !knownApplicationEnrollmentTrustFailure(originalApp, j, p.Stack) {
		t.Fatalf("original application interruption differs: completed=%d owners=%d failed=%s", len(j.Completed), len(j.Owners), j.Failed)
	}
	review, _ := f.admitRecovery(t, "rtk-cloud.create-application-trust-recovery/v1", map[string]string{"accepted-service-core-application-plan": originalApp.SHA256()})
	core, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), f.cfg, p, f.runtime, f.service)
	if err != nil {
		t.Fatal(err)
	}
	trustKey := "kubernetes/service-managed-core-trust-recovery-" + review
	identityPlan, identityJournal := f.interruptedStage(t, trustKey, core, "Deployment/"+p.Stack+"-account-manager/account-manager")
	if len(identityPlan.Resources) != 15 || len(identityJournal.Owners) != 15 {
		t.Fatal("maintained Core repair inventory changed", len(identityPlan.Resources), len(identityJournal.Owners))
	}
	forwardKey := "kubernetes/service-managed-core-identity-recovery-" + review
	f.stage(t, forwardKey, core)
	if err := f.e.RecordReceipt(trustKey, mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": forwardKey, "original_failed_plan_sha256": identityPlan.SHA256()})); err != nil {
		t.Fatal(err)
	}
	if err := f.e.CompleteEntry(trustKey, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	appKey := "kubernetes/application-client-enrollment-trust-recovery-" + review
	f.stage(t, appKey, apps)
	if err := f.e.RecordReceipt("kubernetes/application-client-enrollment", mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": appKey, "original_failed_plan_sha256": originalApp.SHA256(), "renewed_session_id": f.session})); err != nil {
		t.Fatal(err)
	}
	if err := f.e.CompleteEntry("kubernetes/application-client-enrollment", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return finishHistoricalPostServiceMQTT(t, f, review, selectedRoot)
}

// Continue from an already executed Core identity/App repair. This keeps the
// full historical fixture from replaying or substituting those accepted stages.
func finishHistoricalPostServiceMQTT(t *testing.T, f *acceptedCoreExecutionContract, review string, selectedRoot *environmentCreateRegisteredIssuer) *postServiceMQTTExecutionContract {
	t.Helper()
	p := f.p.Plan.Bootstrap
	for _, entry := range rtkSecretCatalog() {
		path := filepath.Join("runtime", entry.ID)
		if _, err := readBoundedEnvironmentFile(filepath.Join(f.store.Root, path), true); err != nil {
			if err := f.store.write(path, []byte("isolated-test-value-"+entry.ID+"\n"), false); err != nil {
				t.Fatal(err)
			}
		}
	}
	definitions, err := renderEnvironmentCreateApplicationOwners(context.Background(), f.cfg, p, f.p.Plan.Release, f.store, f.operator, f.service)
	if err != nil {
		t.Fatal(err)
	}
	root := f.service.Root
	root.Domain = "mqtt"
	root.ID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	root.Status = "ready"
	root.Document = deploymentplan.Clone(root.Document)
	root.Document["issuer_id"], root.Document["trust_domain"], root.Document["status"] = root.ID, root.Domain, root.Status
	if selectedRoot != nil {
		root = *selectedRoot
		if root.Environment != f.cfg.Environment || root.Domain != "mqtt" || root.Kind != "root" || root.Status != "ready" {
			t.Fatal("selected original MQTT authority has a different native scope")
		}
	}
	definitions, err = configureEnvironmentCreateMQTTConsumers(p, f.service, root, nil, definitions)
	if err != nil {
		t.Fatal(err)
	}
	definitions = environmentCreateApplicationDefinitionsFor(definitions, "video-cloud-api", "video-cloud-logingester")
	var mqttDefinitions []deploymentplan.Definition
	for _, definition := range definitions {
		// The historical MQTT cohort owns only API/log runtime inputs. The
		// separate Factory runtime Secret is outside that API/log cohort.
		if deploymentplan.ID(definition.Object) != "Secret/"+p.Stack+"-video-cloud/factoryenroll-runtime" {
			mqttDefinitions = append(mqttDefinitions, definition)
		}
	}
	definitions = mqttDefinitions
	if len(definitions) != 5 {
		t.Fatal("maintained MQTT native cohort changed", len(definitions))
	}
	historical := make([]deploymentplan.Definition, len(definitions))
	for i, d := range definitions {
		historical[i] = d
		// This accepted-history reader supports the original bootstrap-phase
		// consumer-before-configuration ordering. Preserve those frozen phases;
		// the maintained recovery candidate uses prerequisite-first phases.
		if d.Object["kind"] == "Deployment" {
			historical[i].Phase = "bootstrap"
			historical[i].Dependencies = nil
		}
	}
	original, j := f.interruptedStage(t, "kubernetes/mqtt-root-trust", historical, "Deployment/"+p.Stack+"-video-cloud/video-cloud-api")
	if len(j.Completed) != 0 || len(j.Owners) != 1 {
		t.Fatal("original MQTT order did not stop before four unwritten prerequisites", len(j.Completed), len(j.Owners))
	}
	return &postServiceMQTTExecutionContract{f, review, original, j, definitions}
}

func TestPostServiceMQTTExecutionContractReadsAcceptedRepairAndOriginalInterruption(t *testing.T) {
	f := newPostServiceMQTTExecutionContract(t)
	before := createBootstrapStateSnapshot(t, f.store.Root)
	r := &createServiceSessionRecovery{}
	review, a, err := loadPostServiceMQTTSource(f.store, f.e, r)
	if err != nil {
		t.Fatal("complete original MQTT interrupted history rejected", err)
	}
	if review != f.identityReview || deploymentplan.Digest(a.Plan) != review || !r.PostServiceResume || r.MQTTPendingPlan.SHA256() != f.original.SHA256() || deploymentplan.Digest(r.MQTTPendingJournal) != deploymentplan.Digest(f.originalJournal) || r.CompletedManagedStage != "kubernetes/service-managed-core-identity-recovery-"+review || r.ApplicationStage != "kubernetes/application-client-enrollment-trust-recovery-"+review {
		t.Fatal("MQTT source lost original accepted repair or interruption")
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil || a.Plan.CheckerSHA != checker || a.Plan.KubeconfigSHA != environmentPreflightBytesSHA256(f.backend.KubeconfigContent) {
		t.Fatal("historical recovery admission did not bind its selected checker and kubeconfig", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
}

func TestHistoricalManagedCoreAdmissionBindsOriginalAudit(t *testing.T) {
	f := newAcceptedCoreExecutionContract(t)
	expected := deploymentplan.Digest("isolated original native audit")
	f.record(t, "openbao-audit-enable", map[string]string{"backend": "file"}, map[string]string{"audit_sha256": expected})
	audit, err := managedCoreOriginalAuditSHA(f.e.Journal)
	if err != nil || audit != expected {
		t.Fatal(err)
	}
	review, _ := f.admitRecovery(t, managedCorePartialSchema, map[string]string{"isolated": "managed Core audit receipt"})
	entry := f.e.Journal.Entries[len(f.e.Journal.Entries)-1]
	var receipt map[string]string
	if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || len(receipt) != 2 || receipt["qualified_recovery_sha256"] != review || receipt["audit_sha256"] != audit {
		t.Fatal("historical managed Core admission receipt lost the original audit identity")
	}
}

func (f *postServiceMQTTExecutionContract) interruptedForward(t *testing.T) (string, *deploymentplan.Plan, deploymentplan.Journal) {
	t.Helper()
	var defs []deploymentplan.Definition
	var observed []deploymentplan.Object
	for _, v := range f.native.objects {
		observed = append(observed, v)
	}
	for _, definition := range f.definitions {
		d := definition
		d.Object = deploymentplan.Clone(d.Object)
		// Preserve serial readiness of this historical recovery producer. All
		// three prerequisites are applied before the first consumer is waited.
		if d.Object["kind"] == "Deployment" {
			d.Phase = "migration"
			d.Dependencies = nil
		}
		if d.Object["kind"] == "Secret" {
			data := deploymentplan.Map(d.Object["data"])
			if data == nil {
				data = deploymentplan.Object{}
			}
			for k, v := range deploymentplan.Map(d.Object["stringData"]) {
				data[k] = base64.StdEncoding.EncodeToString([]byte(deploymentplan.String(v)))
			}
			d.Object["data"] = data
			delete(d.Object, "stringData")
		}
		defs = append(defs, d)
	}
	candidate, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: f.cfg.Environment, Stack: f.e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: f.p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(f.p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("mqtt-prerequisites-forward", defs), CreateParentSHA256: f.e.Journal.PlanSHA256}, defs, observed)
	if err != nil {
		t.Fatal(err)
	}
	review, _ := f.admitRecovery(t, "rtk-cloud.create-mqtt-trust-order-recovery/v1", map[string]string{"accepted-service-core-mqtt-forward-plan": candidate.SHA256()})
	f.write(t, filepath.Join("deployment/create/openbao-recovery", review, "mqtt-root-trust-plan.json"), candidate)
	current, j := f.interruptedStage(t, "kubernetes/mqtt-root-trust-order-recovery-"+review, defs, f.originalJournal.Failed, true)
	if len(j.Completed) != 3 || len(j.Owners) != 4 {
		t.Fatal("MQTT forward lost three accepted prerequisites and one waiting API owner", len(j.Completed), len(j.Owners))
	}
	return review, current, j
}

func TestPostServiceMQTTExecutionContractFollowsAdmittedForwardWithoutReplayingWrites(t *testing.T) {
	f := newPostServiceMQTTExecutionContract(t)
	forward, current, j := f.interruptedForward(t)
	before := createBootstrapStateSnapshot(t, f.store.Root)
	writes := deploymentplan.Digest(f.native.writes)
	r := &createServiceSessionRecovery{}
	review, _, err := loadPostServiceMQTTSource(f.store, f.e, r)
	if err != nil {
		t.Fatal("authenticated interrupted forward rejected", err)
	}
	if review != f.identityReview || r.UsedRenewalReview != forward || r.MQTTForwardKey != "kubernetes/mqtt-root-trust-order-recovery-"+forward || r.OriginalMQTTPlan.SHA256() != f.original.SHA256() || r.MQTTPendingPlan.SHA256() != current.SHA256() || deploymentplan.Digest(r.MQTTPendingJournal) != deploymentplan.Digest(j) || len(r.UsedRenewalHistory) != 1 || r.UsedRenewalHistory[0] != forward {
		t.Fatal("forward changed the original failure ancestry or adopted a different attempt")
	}
	if deploymentplan.Digest(f.native.writes) != writes {
		t.Fatal("history read replayed native writes")
	}
	assertRecoveryContractReadOnly(t, f.store, before)
}

func TestPostServiceMQTTExecutionContractRejectsAlteredForwardHistory(t *testing.T) {
	for _, mutation := range []string{"source-journal", "admission", "candidate", "unknown-write", "replaced-owner"} {
		t.Run(mutation, func(t *testing.T) {
			f := newPostServiceMQTTExecutionContract(t)
			review, current, j := f.interruptedForward(t)
			base := filepath.Join("deployment/create/openbao-recovery", review)
			switch mutation {
			case "source-journal":
				var before environmentCreateJournal
				if err := readExecutionJSON(filepath.Join(f.store.Root, base, "source-journal.json"), &before); err != nil {
					t.Fatal(err)
				}
				before.OwnerToken = "another-original-owner"
				f.write(t, filepath.Join(base, "source-journal.json"), before)
			case "admission":
				for i := range f.e.Journal.Entries {
					if f.e.Journal.Entries[i].Key == "openbao-recovery-admission-"+review {
						f.e.Journal.Entries[i].IntentSHA256 = deploymentplan.Digest("different qualified source")
					}
				}
			case "candidate":
				var candidate deploymentplan.Plan
				if err := readExecutionJSON(filepath.Join(f.store.Root, base, "mqtt-root-trust-plan.json"), &candidate); err != nil {
					t.Fatal(err)
				}
				candidate.Resources[0].Desired["foreign"] = true
				f.write(t, filepath.Join(base, "mqtt-root-trust-plan.json"), candidate)
			case "unknown-write":
				j.InFlight = j.Failed
				f.write(t, filepath.Join("deployment/rollback", current.SHA256(), "journal.json"), j)
			case "replaced-owner":
				delete(j.Owners, j.Failed)
				f.write(t, filepath.Join("deployment/rollback", current.SHA256(), "journal.json"), j)
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			if _, _, err := loadPostServiceMQTTSource(f.store, f.e, &createServiceSessionRecovery{}); err == nil {
				t.Fatal("altered forward history accepted", mutation)
			}
			assertRecoveryContractReadOnly(t, f.store, before)
		})
	}
}
