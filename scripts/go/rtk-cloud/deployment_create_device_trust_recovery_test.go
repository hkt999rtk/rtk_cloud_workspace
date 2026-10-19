package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func deviceTrustCheckpointFixture() environmentCreateJournal {
	id := "80448538-2c59-4861-997f-cca90c3a1fc3"
	receipt := mustProviderRecoveryJSON(map[string]string{"environment": "dev", "issuer_id": id, "signer_reference": "pki-issuers/device/" + id + "/v1", "root_sha256": strings.Repeat("a", 64), "bundle_version": strings.Repeat("b", 64)})
	return environmentCreateJournal{Environment: "dev", Entries: []environmentCreateJournalEntry{
		{Key: "kubernetes/device-root-ready", State: "completed", CompletedAt: time.Now().Add(-time.Hour), Receipt: json.RawMessage(`{"actual_qualified_forward_stage":"original-forward"}`)},
		{Key: "device-root-bootstrap", State: "allocated", IntentSHA256: strings.Repeat("c", 64), Receipt: receipt},
		{Key: "kubernetes/device-root-trust", State: "pending", IntentSHA256: strings.Repeat("d", 64)},
	}}
}

func TestDeviceTrustCheckpointRequiresNativeReadyReceiptAndExactBoundary(t *testing.T) {
	for _, mode := range []string{"ready", "receiptless", "wrong-state", "foreign-env", "wrong-reference", "extra-pending", "duplicate", "trust-receipt", "completed-root", "late-completion"} {
		t.Run(mode, func(t *testing.T) {
			j := deviceTrustCheckpointFixture()
			switch mode {
			case "receiptless":
				j.Entries[1].Receipt = nil
			case "wrong-state":
				j.Entries[1].State = "pending"
			case "foreign-env":
				j.Environment = "staging"
			case "wrong-reference":
				j.Entries[1].Receipt = json.RawMessage(strings.Replace(string(j.Entries[1].Receipt), "/v1", "/v2", 1))
			case "extra-pending":
				j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "unknown", State: "pending", IntentSHA256: strings.Repeat("e", 64)})
			case "duplicate":
				j.Entries = append(j.Entries, j.Entries[2])
			case "trust-receipt":
				j.Entries[2].Receipt = json.RawMessage(`{}`)
			case "completed-root":
				j.Entries[1].State = "completed"
				j.Entries[1].CompletedAt = time.Now()
			case "late-completion":
				j.Entries[2].CompletedAt = time.Now()
			}
			profile, flag := createRecoveryCheckpointProfile(j)
			if mode == "ready" {
				if profile != "device-ready-trust-service-policy" || flag != "--after-application-enrollment-trust" {
					t.Fatal("READY checkpoint not selected", profile, flag)
				}
			} else if profile != "" || flag != "" {
				t.Fatal("unsafe checkpoint selected", profile, flag)
			}
		})
	}
}

func deviceTrustPartialPlanFixture(t *testing.T) (*deploymentplan.Plan, deploymentplan.Journal) {
	t.Helper()
	stack := "video-cloud-dev"
	ns := stack + "-video-cloud"
	defs := []deploymentplan.Definition{}
	for _, v := range []struct{ kind, name, phase string }{{"ConfigMap", "pki-create-device-trust", "configuration"}, {"Secret", "factoryenroll-runtime", "configuration"}, {"Secret", "video-cloud-runtime", "configuration"}, {"Secret", "video-cloud-workers-runtime", "configuration"}, {"Deployment", "mqtt-pki", "workload"}, {"Deployment", "video-cloud-api-pki", "workload"}} {
		defs = append(defs, deploymentplan.Definition{Phase: v.phase, Object: deploymentplan.Object{"apiVersion": "v1", "kind": v.kind, "metadata": deploymentplan.Object{"name": v.name, "namespace": ns}}})
	}
	for i := range defs {
		if defs[i].Object["kind"] == "Deployment" {
			defs[i].Object["spec"] = deploymentplan.Object{"template": deploymentplan.Object{"metadata": deploymentplan.Object{}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app", "image": "original-image"}}}}}
		}
	}
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: stack, Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	j := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Failed: "Deployment/" + ns + "/mqtt-pki", Owners: map[string]string{}}
	for _, v := range plan.Resources {
		if v.ID == "Deployment/"+ns+"/video-cloud-api-pki" {
			continue
		}
		j.Owners[v.ID] = "uid-" + v.ID
		if v.Phase == "configuration" {
			j.Completed = append(j.Completed, v.ID)
		}
	}
	return plan, j
}

func TestDeviceTrustFailureRejectsUncertainWritesAndWrongOwners(t *testing.T) {
	for _, mode := range []string{"original", "duplicate-completed", "missing-owner", "unwritten-api-owned", "in-flight", "accepted", "acceptance-started", "foreign-failure", "wrong-digest", "extra-resource"} {
		t.Run(mode, func(t *testing.T) {
			p, j := deviceTrustPartialPlanFixture(t)
			switch mode {
			case "duplicate-completed":
				j.Completed[1] = j.Completed[0]
			case "missing-owner":
				delete(j.Owners, j.Completed[0])
			case "unwritten-api-owned":
				j.Owners["Deployment/video-cloud-dev-video-cloud/video-cloud-api-pki"] = "foreign"
			case "in-flight":
				j.InFlight = j.Failed
			case "accepted":
				j.Accepted = true
			case "acceptance-started":
				j.AcceptanceStarted = true
			case "foreign-failure":
				j.Failed = "Deployment/video-cloud-staging-video-cloud/mqtt-pki"
			case "wrong-digest":
				j.PlanSHA256 = strings.Repeat("f", 64)
			case "extra-resource":
				p.Resources = append(p.Resources, p.Resources[0])
				j.PlanSHA256 = p.SHA256()
			}
			if knownDeviceTrustFailure(p, j, "video-cloud-dev") != (mode == "original") {
				t.Fatal("incorrect partial checkpoint decision")
			}
		})
	}
}

func TestBrokerServicePolicyPreservesIdentityAndConfigurationAndIsIdempotent(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			_, p, _, _ := createServiceRecipeFixture(t, environment)
			p.Domains = append(p.Domains, environmentCreateIssuerPolicy{Domain: "device", BundleConsumers: []string{"pkibroker", "video-cloud-api"}})
			c := deploymentplan.Object{"name": "pki-controller", "image": p.Image, "env": environmentCreateEnv(map[string]string{"PKI_REQUIRED_CONSUMERS_SERVICE": "account-manager,certissuer,pki-controller", "PKI_REQUIRED_BUNDLE_CONSUMERS_SERVICE": "account-manager,certissuer,pki-controller", "PKI_SERVICE_CLIENT_IDENTITY_STATE": "/original/private/client.json", "PKI_SERVICE_CLIENT_SERVER_CRL_MANIFEST": "/original/crls.json", "PKI_REQUIRED_CONSUMERS_DEVICE": "pkibroker,video-cloud-api"})}
			d := deploymentplan.Definition{Phase: "workload", Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "pki-controller", "namespace": p.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{c}, "volumes": []any{environmentCreateServiceVolume("state", "persistentVolumeClaim", "original-identity")}}}}}}
			input := []deploymentplan.Definition{d}
			before := deploymentplan.Digest(input)
			corrected, err := configureCreateBrokerServiceConsumer(p, input)
			if err != nil {
				t.Fatal(err)
			}
			cc := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(corrected[0].Object)["containers"])[0])
			env := createRecipeEnv(cc)
			if env["PKI_REQUIRED_CONSUMERS_SERVICE"] != "account-manager,certissuer,pki-controller,pkibroker" {
				t.Fatal("missing explicit broker policy", env)
			}
			cc["env"] = mergeEnvironmentNamedFields(cc["env"], environmentCreateEnv(map[string]string{"PKI_REQUIRED_CONSUMERS_SERVICE": "account-manager,certissuer,pki-controller"}))
			if deploymentplan.Digest(corrected) != before || deploymentplan.Digest(input) != before {
				t.Fatal("policy changed identity, CRLs, bundle threshold or original inputs")
			}
			first, err := configureCreateBrokerServiceConsumer(p, input)
			if err != nil {
				t.Fatal(err)
			}
			second, err := configureCreateBrokerServiceConsumer(p, first)
			if err != nil || deploymentplan.Digest(first) != deploymentplan.Digest(second) {
				t.Fatal("policy is not retry-safe", err)
			}
			malformed := deploymentplan.Clone(d.Object)
			badC := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(malformed)["containers"])[0])
			badC["env"] = environmentCreateEnv(map[string]string{"PKI_REQUIRED_CONSUMERS_SERVICE": "account-manager,account-manager"})
			if _, err := configureCreateBrokerServiceConsumer(p, []deploymentplan.Definition{{Object: malformed}}); err == nil {
				t.Fatal("duplicate policy accepted")
			}
		})
	}
}

func TestDeviceTrustForwardUsesActualReceiptAndPreservesReadyIdentity(t *testing.T) {
	cfg, store, c, _, files := recoveryPreflightFixture(t)
	e := &environmentCreateExecution{Store: store, Journal: c.Journal}
	// No real environment is touched; absence of an acquired owner must fail
	// before any fabricated completion could be persisted.
	r := &createServiceSessionRecovery{Review: strings.Repeat("f", 64), DeviceTrustRecovery: &createDeviceTrustRecovery{Root: environmentCreateRegisteredIssuer{ID: "80448538-2c59-4861-997f-cca90c3a1fc3"}}}
	calls := 0
	err := executeDeviceTrustForward(context.Background(), environmentCreateBootstrapRuntime{Execution: e, ServiceSessionRecovery: r}, func(context.Context, string, []deploymentplan.Definition) error { calls++; return nil })
	if err == nil || calls != 0 {
		t.Fatal("unqualified forward stage wrote completion", cfg.Environment)
	}
	assertRecoveryOriginalFiles(t, store, files)
}

func TestDeviceTrustRegistryDeduplicatesOnlyIdenticalAuthenticatedReservationAliases(t *testing.T) {
	device := "80448538-2c59-4861-997f-cca90c3a1fc3"
	native := "9a6c0a78-9599-4fb9-8b0f-1bbad15618c9"
	original := environmentCreateIssuerOperation{ID: "08af5057-8918-4215-aee7-5633d0f6995f", IssuerID: "5874046d-4088-4a40-a27c-04f7fd9b0ef0", Digest: strings.Repeat("a", 64), Status: "requested"}
	for _, mode := range []string{"identical", "changed-request", "changed-issuer", "native-alias", "receiptless"} {
		t.Run(mode, func(t *testing.T) {
			alias := original
			if mode == "changed-request" {
				alias.Digest = strings.Repeat("b", 64)
			}
			if mode == "changed-issuer" {
				alias.IssuerID = device
			}
			if mode == "native-alias" {
				alias.ID = native
				alias.IssuerID = device
			}
			raw, _ := json.Marshal(original)
			forward, _ := json.Marshal(alias)
			if mode == "receiptless" {
				forward = nil
			}
			j := environmentCreateJournal{Entries: []environmentCreateJournalEntry{{Key: "pki-reserve-service-root", State: "completed", Receipt: raw}, {Key: "pki-reserve-service-root-recovery-original", State: "completed", Receipt: forward}}}
			issuers, ops, err := deviceTrustRegistryIdentities(j, device, native)
			if mode == "identical" {
				if err != nil || len(issuers) != 2 || len(ops) != 2 || ops[native] != device || ops[original.ID] != original.IssuerID {
					t.Fatal("original aliases were counted as new registry objects", err)
				}
			} else if err == nil {
				t.Fatal("changed native receipt accepted")
			}
		})
	}
}

func TestDeviceTrustForwardRestartsOnlyOwnedBrokerAfterControllerAndRetainsCompletedConfiguration(t *testing.T) {
	plan, j := deviceTrustPartialPlanFixture(t)
	controller := deploymentplan.Definition{Phase: "workload", Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "pki-controller", "namespace": plan.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "pki-controller", "image": "original-image"}}}}}}}
	var defs []deploymentplan.Definition
	var live []deploymentplan.Object
	for _, r := range plan.Resources {
		defs = append(defs, deploymentplan.Definition{Phase: r.Phase, Object: deploymentplan.Clone(r.Desired), Retain: strings.HasPrefix(r.ID, "ConfigMap/") || strings.HasPrefix(r.ID, "Secret/")})
		if uid := j.Owners[r.ID]; uid != "" {
			o := deploymentplan.Clone(r.Desired)
			deploymentplan.Map(o["metadata"])["uid"] = uid
			live = append(live, o)
		}
	}
	oldController := deploymentplan.Clone(controller.Object)
	deploymentplan.Map(oldController["metadata"])["uid"] = "original-controller-uid"
	deploymentplan.Map(oldController["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": plan.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}
	live = append(live, oldController)
	before := deploymentplan.Digest(defs)
	forward := deviceTrustForwardDefinitions(defs, controller, j)
	candidate, err := deploymentplan.Compile(plan.Inputs, forward, live)
	if err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Digest(defs) != before {
		t.Fatal("original failed definitions changed")
	}
	controllerIndex, brokerIndex, apiIndex := -1, -1, -1
	for n, r := range candidate.Resources {
		switch r.ID {
		case j.Failed:
			brokerIndex = n
			if r.Action != "update" || r.UID != j.Owners[r.ID] || len(r.Dependencies) != 1 || r.Dependencies[0] != deploymentplan.ID(controller.Object) {
				t.Fatal("broker does not retain its owner or wait for policy")
			}
		case "Deployment/" + plan.Stack + "-video-cloud/video-cloud-api-pki":
			apiIndex = n
			if r.Action != "create" || len(r.Dependencies) != 1 {
				t.Fatal("original unwritten API is not a dependency-bound create")
			}
		case deploymentplan.ID(controller.Object):
			controllerIndex = n
		default:
			if r.Action != "retain" || r.UID != j.Owners[r.ID] {
				t.Fatal("completed configuration is rewritten", r.ID)
			}
		}
	}
	if controllerIndex < 0 || brokerIndex < controllerIndex || apiIndex < controllerIndex {
		t.Fatal("compiler did not order Controller before broker/API")
	}
}

func TestDeviceTrustForwardFailureDoesNotCompleteOriginalAndRetryRetainsNativeRoot(t *testing.T) {
	cfg, store, operator, artifact := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	rootID := "80448538-2c59-4861-997f-cca90c3a1fc3"
	rootReceipt := mustProviderRecoveryJSON(map[string]string{"issuer_id": rootID, "root_sha256": strings.Repeat("a", 64)})
	if err = e.BeginEntry("device-root-bootstrap", "bootstrap", json.RawMessage(`{"issuer_id":"`+rootID+`"}`), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err = e.RecordReceipt("device-root-bootstrap", rootReceipt); err != nil {
		t.Fatal(err)
	}
	if err = e.BeginEntry("kubernetes/device-root-trust", "bootstrap", json.RawMessage(`{"original":"trust"}`), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	plan, _ := deviceTrustPartialPlanFixture(t)
	r := &createServiceSessionRecovery{Review: strings.Repeat("f", 64), DeviceTrustRecovery: &createDeviceTrustRecovery{Root: environmentCreateRegisteredIssuer{ID: rootID}, Original: plan}}
	runtime := environmentCreateBootstrapRuntime{Execution: e, ServiceSessionRecovery: r}
	before, err := os.ReadFile(e.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("isolated rollout failure")
	err = executeDeviceTrustForward(context.Background(), runtime, func(context.Context, string, []deploymentplan.Definition) error { return failure })
	after, readErr := os.ReadFile(e.JournalPath)
	if !errors.Is(err, failure) || readErr != nil || !bytes.Equal(before, after) {
		t.Fatal("failed forward fabricated original completion", err, readErr)
	}
	err = executeDeviceTrustForward(context.Background(), runtime, func(_ context.Context, stage string, _ []deploymentplan.Definition) error {
		key := "kubernetes/" + stage
		if err := e.BeginEntry(key, "bootstrap", json.RawMessage(`{"qualified":"forward"}`), time.Now().UTC()); err != nil {
			return err
		}
		if err := e.RecordReceipt(key, json.RawMessage(`{"actual_accepted_fixture":true}`)); err != nil {
			return err
		}
		return e.CompleteEntry(key, time.Now().UTC())
	})
	if err != nil {
		t.Fatal(err)
	}
	root, trust, forward := false, false, false
	for _, entry := range e.Journal.Entries {
		if entry.Key == "device-root-bootstrap" {
			root = entry.State == "allocated" && bytes.Equal(entry.Receipt, rootReceipt)
		}
		if entry.Key == "kubernetes/device-root-trust" {
			var receipt map[string]string
			trust = entry.State == "completed" && !entry.CompletedAt.IsZero() && json.Unmarshal(entry.Receipt, &receipt) == nil && receipt["original_root_issuer_id"] == rootID && receipt["original_failed_plan_sha256"] == plan.SHA256() && receipt["actual_qualified_forward_stage"] == "kubernetes/device-root-trust-policy-forward-"+r.Review
		}
		if entry.Key == "kubernetes/device-root-trust-policy-forward-"+r.Review {
			forward = entry.State == "completed"
		}
	}
	if !root || !trust || !forward {
		t.Fatal("retry lost native Root or actual forward completion", root, trust, forward, store.Environment)
	}
}

func TestDeviceTrustSignerRequiresOriginalCertificateAndActualNativeKeyBinding(t *testing.T) {
	_, _, root := createDeviceRecipeFixture(t, "dev")
	for _, mode := range []string{"original", "missing-key", "nil-key", "missing-issuer", "wrong-certificate", "wrong-chain", "no-certificate-signing", "no-crl-signing"} {
		t.Run(mode, func(t *testing.T) {
			data := map[string]any{"certificate": root.Certificate, "ca_chain": []string{root.Certificate}, "issuer_id": "00000000-0000-0000-0000-000000000001", "key_id": "00000000-0000-0000-0000-000000000003", "usage": "read-only,issuing-certificates,crl-signing"}
			switch mode {
			case "missing-key":
				delete(data, "key_id")
			case "nil-key":
				data["key_id"] = "00000000-0000-0000-0000-000000000000"
			case "missing-issuer":
				delete(data, "issuer_id")
			case "wrong-certificate":
				data["certificate"] = "not a CA certificate"
			case "wrong-chain":
				data["ca_chain"] = []string{root.Certificate, root.Certificate}
			case "no-certificate-signing":
				data["usage"] = "read-only,crl-signing"
			case "no-crl-signing":
				data["usage"] = "read-only,issuing-certificates"
			}
			raw, _ := json.Marshal(map[string]any{"data": data})
			sha, err := deviceTrustSignerObservation(raw, root)
			if mode == "original" {
				if err != nil || !environmentPreflightDigest.MatchString(sha) {
					t.Fatal("actual original signer rejected", err)
				}
			} else if err == nil {
				t.Fatal("unverified provider signing binding accepted")
			}
		})
	}
}

func TestDeviceTrustCheckpointAllowsOnlyCompletedAdmissionTail(t *testing.T) {
	for _, mode := range []string{"valid", "pending", "foreign-review", "missing-audit", "unknown-stage"} {
		t.Run(mode, func(t *testing.T) {
			j := deviceTrustCheckpointFixture()
			review := strings.Repeat("e", 64)
			e := environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + review, State: "completed", IntentSHA256: strings.Repeat("f", 64), CompletedAt: time.Now(), Receipt: mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": review, "audit_sha256": strings.Repeat("a", 64)})}
			switch mode {
			case "pending":
				e.State = "pending"
			case "foreign-review":
				e.Key = "openbao-recovery-admission-" + strings.Repeat("b", 64)
			case "missing-audit":
				e.Receipt = mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": review})
			case "unknown-stage":
				e.Key = "kubernetes/unknown-completed"
			}
			j.Entries = append(j.Entries, e)
			if isDeviceTrustCheckpoint(j) != (mode == "valid") {
				t.Fatal("unsafe admission boundary", mode)
			}
		})
	}
}

func TestDeviceTrustOriginalPlanUsesTrustIntentAfterAdmission(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	plan, child := deviceTrustPartialPlanFixture(t)
	j := deviceTrustCheckpointFixture()
	j.Stack = plan.Stack
	intent, err := environmentCreateKubernetesStageIntent(plan)
	if err != nil {
		t.Fatal(err)
	}
	sha, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		t.Fatal(err)
	}
	j.Entries[2].IntentSHA256 = sha
	review := strings.Repeat("e", 64)
	j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + review, State: "completed", IntentSHA256: strings.Repeat("f", 64), CompletedAt: time.Now(), Receipt: mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": review, "audit_sha256": strings.Repeat("a", 64)})})
	j.KubernetesLeases = []deploymentplan.LeaseOwner{{Holder: plan.SHA256() + ":original"}}
	for name, value := range map[string]any{"plan.json": plan, "journal.json": child} {
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/rollback", plan.SHA256(), name), mustProviderRecoveryJSON(value)); err != nil {
			t.Fatal(err)
		}
	}
	got, _, err := loadOriginalDeviceTrustPlan(store, &environmentCreateExecution{Journal: j})
	if err != nil || got.SHA256() != plan.SHA256() {
		t.Fatal("admission tail replaced original trust intent", err)
	}
	j.Entries[2].IntentSHA256 = strings.Repeat("0", 64)
	if _, _, err := loadOriginalDeviceTrustPlan(store, &environmentCreateExecution{Journal: j}); err == nil {
		t.Fatal("foreign original intent accepted")
	}
}

func TestDeviceTrustPartialForwardRejectsOtherNativeOutcomes(t *testing.T) {
	for _, mode := range []string{"broker-only", "uncertain", "api-written", "wrong-uid", "config-rewritten", "controller-failed", "missing-completion", "accepted"} {
		t.Run(mode, func(t *testing.T) {
			plan, j := deviceTrustPartialPlanFixture(t)
			for i := range plan.Resources {
				v := &plan.Resources[i]
				if v.ID != "Deployment/"+plan.Stack+"-video-cloud/video-cloud-api-pki" {
					v.UID = j.Owners[v.ID]
					if v.ID == j.Failed {
						v.Action = "update"
					} else {
						v.Action = "retain"
					}
				}
			}
			controller := plan.Resources[4]
			controller.ID = "Deployment/" + plan.Stack + "-video-cloud/pki-controller"
			controller.Desired = deploymentplan.Clone(controller.Desired)
			deploymentplan.Map(controller.Desired["metadata"])["name"] = "pki-controller"
			controller.UID = "controller-uid"
			controller.Action = "update"
			plan.Resources = append(plan.Resources, controller)
			j.Owners[controller.ID] = controller.UID
			j.Completed = append(j.Completed, controller.ID)
			switch mode {
			case "uncertain":
				j.InFlight = j.Failed
			case "api-written":
				j.Owners["Deployment/"+plan.Stack+"-video-cloud/video-cloud-api-pki"] = "unexpected-uid"
			case "wrong-uid":
				j.Owners[j.Failed] = "different-uid"
			case "config-rewritten":
				plan.Resources[0].Action = "update"
			case "controller-failed":
				j.Failed = controller.ID
			case "missing-completion":
				j.Completed = j.Completed[:4]
			case "accepted":
				j.Accepted = true
			}
			j.PlanSHA256 = plan.SHA256()
			if knownDeviceTrustForwardFailure(plan, j, plan.Stack) != (mode == "broker-only") {
				t.Fatal("unsupported native outcome", mode)
			}
		})
	}
}

func TestDeviceTrustCheckpointAdmitsOnlyBoundPendingForwardShape(t *testing.T) {
	j := deviceTrustCheckpointFixture()
	review := strings.Repeat("e", 64)
	j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + review, State: "completed", IntentSHA256: strings.Repeat("f", 64), CompletedAt: time.Now(), Receipt: mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": review, "audit_sha256": strings.Repeat("a", 64)})}, environmentCreateJournalEntry{Key: "kubernetes/device-root-trust-policy-forward-" + review, State: "pending", IntentSHA256: strings.Repeat("b", 64)})
	if !isDeviceTrustCheckpoint(j) {
		t.Fatal("native forward failure not selected")
	}
	if loadDeviceTrustPartialForwards(makeIsolatedTestSecretStore(t, "dev"), &environmentCreateExecution{Journal: j}, &createDeviceTrustRecovery{}) == nil {
		t.Fatal("unqualified forward accepted")
	}
	j.Entries[len(j.Entries)-1].Receipt = json.RawMessage(`{"uncertain":true}`)
	if isDeviceTrustCheckpoint(j) {
		t.Fatal("uncertain forward receipt accepted")
	}
}

func TestDeviceTrustHistoricalRecipeRestoresOnlyMissingPublicCA(t *testing.T) {
	plan, j := deviceTrustPartialPlanFixture(t)
	for i := range plan.Resources {
		if plan.Resources[i].ID == j.Failed {
			c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(plan.Resources[i].Desired)["containers"])[0])
			c["name"] = "pkibroker"
			c["env"] = environmentCreateEnv(map[string]string{"PKI_BROKER_IDENTITY_STATE": "/original/client.json"})
		}
	}
	var defs []deploymentplan.Definition
	for _, v := range plan.Resources {
		defs = append(defs, deploymentplan.Definition{Object: deploymentplan.Clone(v.Desired)})
	}
	for _, d := range defs {
		if deploymentplan.ID(d.Object) == j.Failed {
			c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["containers"])[0])
			c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BROKER_MANAGEMENT_CA": environmentCreateServiceTrust + "/root.pem"}))
		}
	}
	originalSHA := deploymentplan.Digest(plan.Resources)
	restoreOriginalDeviceTrustCA(defs, plan)
	for i, d := range defs {
		if deploymentplan.Digest(d.Object) != deploymentplan.Digest(plan.Resources[i].Desired) {
			t.Fatal("historical reconstruction changed other intent")
		}
	}
	if deploymentplan.Digest(plan.Resources) != originalSHA {
		t.Fatal("original admitted plan mutated")
	}
	controller := deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "pki-controller", "namespace": plan.Stack + "-video-cloud"}}}
	forward := deviceTrustForwardDefinitions(defs, controller, j)
	for _, d := range forward {
		if deploymentplan.ID(d.Object) == j.Failed {
			c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["containers"])[0])
			if mqttFoundationEnv(c, "PKI_BROKER_MANAGEMENT_CA") != environmentCreateServiceTrust+"/root.pem" || mqttFoundationEnv(c, "PKI_BROKER_IDENTITY_STATE") != "/original/client.json" {
				t.Fatal("forward CA omitted or identity changed")
			}
		}
	}
}

func TestDeviceBrokerTrustPreparationBindsOriginalClaimAndKeepsKeys(t *testing.T) {
	image := "docker.io/library/alpine@sha256:" + strings.Repeat("a", 64)
	for _, mode := range []string{"original", "foreign-claim", "foreign-state", "fsGroup", "mutable-image"} {
		t.Run(mode, func(t *testing.T) {
			c := deploymentplan.Object{"name": "pkibroker", "env": environmentCreateEnv(map[string]string{"PKI_BROKER_IDENTITY_STATE": environmentCreateBrokerIdentity}), "volumeMounts": []any{environmentCreateServiceMount("service-identity", "/run/pki-state", false)}}
			pod := deploymentplan.Object{"containers": []any{c}, "securityContext": deploymentplan.Object{"runAsUser": float64(10001)}, "volumes": []any{environmentCreateServiceVolume("service-identity", "persistentVolumeClaim", "mqtt-pki-trust")}}
			obj := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "mqtt-pki"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": pod}}}
			selectedImage := image
			switch mode {
			case "foreign-claim":
				deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(pod["volumes"])[0])["persistentVolumeClaim"])["claimName"] = "foreign"
			case "foreign-state":
				c["env"] = environmentCreateEnv(map[string]string{"PKI_BROKER_IDENTITY_STATE": "/foreign/key.json"})
			case "fsGroup":
				deploymentplan.Map(pod["securityContext"])["fsGroup"] = float64(10001)
			case "mutable-image":
				selectedImage = "alpine:latest"
			}
			before := deploymentplan.Digest(c)
			err := prepareDeviceBrokerTrustDirectory([]deploymentplan.Definition{{Object: obj}}, selectedImage)
			if mode != "original" {
				if err == nil {
					t.Fatal("foreign permission preparation accepted", mode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			init := deploymentplan.Map(certIssuerObjectList(pod["initContainers"])[0])
			if init["image"] != image || deploymentplan.Digest(init["command"]) != deploymentplan.Digest([]any{"/bin/sh", "-ec", coreIdentityPreparation}) || deploymentplan.Digest(init["args"]) != deploymentplan.Digest([]any{"identity-permissions", "/identity-volume/broker-identity/private/client.json"}) || deploymentplan.Digest(c) != before || deploymentplan.Map(pod["securityContext"])["fsGroup"] != nil {
				t.Fatal("preparation changed original identity or permission boundary")
			}
		})
	}
}

func TestDeviceTrustForwardAcceptsOnlyExactOwnedAPIFailure(t *testing.T) {
	for _, mode := range []string{"api", "missing-owner", "broker-not-completed", "uncertain", "foreign-uid"} {
		t.Run(mode, func(t *testing.T) {
			plan, j := deviceTrustPartialPlanFixture(t)
			for i := range plan.Resources {
				v := &plan.Resources[i]
				if v.ID != "Deployment/"+plan.Stack+"-video-cloud/video-cloud-api-pki" {
					v.UID = j.Owners[v.ID]
					v.Action = "retain"
				}
			}
			controller := plan.Resources[4]
			controller.ID = "Deployment/" + plan.Stack + "-video-cloud/pki-controller"
			controller.Desired = deploymentplan.Clone(controller.Desired)
			deploymentplan.Map(controller.Desired["metadata"])["name"] = "pki-controller"
			controller.UID = "controller-uid"
			controller.Action = "retain"
			plan.Resources = append(plan.Resources, controller)
			j.Owners[controller.ID] = controller.UID
			j.Completed = append(j.Completed, controller.ID, j.Failed)
			j.Failed = "Deployment/" + plan.Stack + "-video-cloud/video-cloud-api-pki"
			j.Owners[j.Failed] = "actual-api-uid"
			switch mode {
			case "missing-owner":
				delete(j.Owners, j.Failed)
			case "broker-not-completed":
				j.Completed = j.Completed[:5]
			case "uncertain":
				j.InFlight = j.Failed
			case "foreign-uid":
				plan.Resources[4].UID = "foreign-broker-uid"
			}
			j.PlanSHA256 = plan.SHA256()
			if knownDeviceTrustForwardFailure(plan, j, plan.Stack) != (mode == "api") {
				t.Fatal("unexpected API failure classification", mode)
			}
		})
	}
}

func TestIncomingBootstrapManifestBindsOnlyOriginalPublicConfigMapKey(t *testing.T) {
	for _, domain := range []string{"device", "app"} {
		t.Run(domain, func(t *testing.T) {
			c := deploymentplan.Object{"name": "app", "env": environmentCreateEnv(map[string]string{"VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST": "/run/pki-" + domain + "/bundles.json", "VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE": "/original/private/identity.json"})}
			obj := deploymentplan.Object{"kind": "Deployment", "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{c}}}}}
			if err := projectIncomingBootstrapManifest(obj, domain); err != nil {
				t.Fatal(err)
			}
			pod := environmentPKIPod(obj)
			volume := deploymentplan.Map(certIssuerObjectList(pod["volumes"])[0])
			source := deploymentplan.Map(volume["configMap"])
			mount := deploymentplan.Map(certIssuerObjectList(c["volumeMounts"])[0])
			if source["name"] != "pki-create-"+domain+"-trust" || deploymentplan.Digest(source["items"]) != deploymentplan.Digest([]any{deploymentplan.Object{"key": "bundles.json", "path": "bundles.json"}}) || mount["readOnly"] != true || mount["subPath"] != "bundles.json" || mount["mountPath"] != "/run/pki-"+domain+"-manifest/bundles.json" || mqttFoundationEnv(c, "VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE") != "/original/private/identity.json" {
				t.Fatal("manifest copied credentials or changed original Root source")
			}
			before := deploymentplan.Digest(obj)
			if err := projectIncomingBootstrapManifest(obj, domain); err != nil || deploymentplan.Digest(obj) != before {
				t.Fatal("projection is not idempotent", err)
			}
		})
	}
}
