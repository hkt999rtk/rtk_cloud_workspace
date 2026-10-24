package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateResidualHistoryResource(subject, session, stack, root string, guarded bool) deploymentplan.Resource {
	name := strings.TrimPrefix(subject, "service:")
	namespace := stack + "-video-cloud"
	if name == "account-manager" {
		namespace = stack + "-account-manager"
	}
	container := deploymentplan.Object{"name": "app", "env": environmentCreateEnv(map[string]string{"PKI_ENVIRONMENT": "dev", "PKI_BOOTSTRAP_SESSION_ID": session})}
	object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": namespace, "name": name}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{container}}}}}
	if name == "account-manager" {
		container["name"] = "pkimanagement"
		environmentPKIPod(object)["containers"] = []any{container, deploymentplan.Object{"name": "account-manager", "env": environmentCreateEnv(map[string]string{"UNRELATED": "unchanged"})}}
	}
	if name == "video-cloud-api" || name == "video-cloud-logingester" {
		var owner environmentCreateApplicationOwner
		for _, value := range environmentCreateApplicationOwners() {
			if value.Name == name {
				owner = value
			}
		}
		service := environmentCreateServiceCeremony{BootstrapSessionID: session, BootstrapCredentialSecret: "service-bootstrap-" + session}
		definition := environmentCreateServiceEnrollmentJob(environmentCreateBootstrapPlan{Stack: stack, Image: "native@sha256:" + strings.Repeat("a", 64)}, namespace, owner.Name+"-client", owner.Claim, service, []any{"/app/serviceidentity-bootstrap", subject}, map[string]string{"PKI_ENVIRONMENT": "dev", "PKI_CLIENT_ROOT_SHA256": root, "PKI_CLIENT_IDENTITY_STATE": owner.ClientState, "PKI_CLIENT_IDENTITY_BOOTSTRAP_CERT": "/run/pki-bootstrap/tls.crt", "PKI_CLIENT_IDENTITY_BOOTSTRAP_KEY": "/run/pki-bootstrap/tls.key"}, false)
		object = definition.Object
		if guarded {
			container = deploymentplan.Map(certIssuerObjectList(environmentPKIPod(object)["containers"])[0])
			container["command"] = []any{"/bin/sh", "-ec", "test -s " + owner.ClientState + " && grep -q '\"current\"' " + owner.ClientState + " && ! grep -q '\"pending\"' " + owner.ClientState + " && /app/serviceidentity-bootstrap " + subject}
		}
	}
	return deploymentplan.Resource{ID: deploymentplan.ID(object), Phase: "bootstrap", Action: "create", UID: "11111111-1111-4111-8111-111111111111", Desired: object}
}

func TestManagedLateResidualInstalledHistoryRequiresNativeACKAndAcceptedSubject(t *testing.T) {
	state, observed, current, subjects, now := managedHistoricalSeedFixture(t)
	state.OtherActive = 0
	r, _, _ := managedLateResidualAuditFixture(t)
	approval := r.ResidualCleanup.Approval
	for i, ns := range []string{"video-cloud-dev-video-cloud", "video-cloud-dev-account-manager", "video-cloud-dev-secrets"} {
		approval.Objects[i] = createManagedLateResidualCleanupObject{ResourceID: "Secret/" + ns + "/service-bootstrap-" + state.Session, Namespace: ns, SessionID: state.Session, UID: observed.UIDs[ns]}
	}
	approval.InventorySHA = deploymentplan.Digest(approval.Objects)
	recovery := &createServiceSessionRecovery{ManagedLateSession: &createManagedLateSessionRecovery{}, ManagedLateRemaining: r, StaleSeeds: []createServiceSessionObserved{observed}}
	r.StaleSeedsSHA = deploymentplan.Digest(recovery.StaleSeeds)
	accepted := map[string]bool{"service:account-manager": true, "service:certissuer": true, "service:pki-controller": true, "service:video-cloud-api": true, "service:video-cloud-logingester": true}
	for _, mode := range []string{"core2", "core3", "application5", "missing-subject-proof", "no-ack", "duplicate-ack", "unaccepted-native-ack", "foreign-fingerprint", "foreign-expiry", "foreign-issuer", "foreign-root", "foreign-allowlist", "active", "other-active", "changed-source", "foreign-observation", "missing-approved-uid", "saved-ack-regression", "duplicate-source", "missing-residual"} {
		t.Run(mode, func(t *testing.T) {
			var copy struct {
				Recovery createServiceSessionRecovery
				State    managedSessionState
				Observed createServiceSessionObserved
			}
			if err := json.Unmarshal(mustProviderRecoveryJSON(map[string]any{"Recovery": recovery, "State": state, "Observed": observed}), &copy); err != nil {
				t.Fatal(err)
			}
			copy.State.Installed = []string{"service:certissuer", "service:account-manager"}
			allowed := cloneBoolMapForResidualHistory(accepted)
			switch mode {
			case "core3":
				copy.State.Installed = append(copy.State.Installed, "service:pki-controller")
			case "application5":
				copy.State.Installed = append(copy.State.Installed, "service:pki-controller", "service:video-cloud-api", "service:video-cloud-logingester")
			case "missing-subject-proof":
				delete(allowed, "service:certissuer")
			case "no-ack":
				copy.State.Installed = nil
			case "duplicate-ack":
				copy.State.Installed = append(copy.State.Installed, copy.State.Installed[0])
			case "unaccepted-native-ack":
				copy.State.Installed = append(copy.State.Installed, "service:pkibroker")
			case "foreign-fingerprint":
				copy.State.Fingerprint = strings.Repeat("f", 64)
			case "foreign-expiry":
				copy.State.Expires = copy.State.Expires.Add(-time.Second)
			case "foreign-issuer":
				copy.State.Issuer = "99999999-1111-4111-8111-111111111111"
			case "foreign-root":
				copy.State.Root = strings.Repeat("f", 64)
			case "foreign-allowlist":
				copy.State.Subjects = slices.Clone(subjects[:8])
			case "active":
				copy.State.Status = "active"
			case "other-active":
				copy.State.OtherActive = 1
			case "changed-source":
				copy.Recovery.ManagedLateRemaining.StaleSeedsSHA = strings.Repeat("f", 64)
			case "foreign-observation":
				copy.Observed.Reader = "foreign"
			case "missing-approved-uid":
				copy.Recovery.ManagedLateRemaining.ResidualCleanup.Approval.Objects[0].UID = "99999999-1111-4111-8111-111111111111"
				copy.Recovery.ManagedLateRemaining.ResidualCleanup.Approval.InventorySHA = deploymentplan.Digest(copy.Recovery.ManagedLateRemaining.ResidualCleanup.Approval.Objects)
			case "saved-ack-regression":
				var saved managedSessionState
				_ = json.Unmarshal(copy.Observed.Session, &saved)
				saved.Installed = []string{"service:pki-controller"}
				copy.Observed.Session = mustProviderRecoveryJSON(saved)
				copy.Recovery.StaleSeeds[0] = copy.Observed
				copy.Recovery.ManagedLateRemaining.StaleSeedsSHA = deploymentplan.Digest(copy.Recovery.StaleSeeds)
			case "duplicate-source":
				copy.Recovery.StaleSeeds = append(copy.Recovery.StaleSeeds, copy.Observed)
				copy.Recovery.ManagedLateRemaining.StaleSeedsSHA = deploymentplan.Digest(copy.Recovery.StaleSeeds)
			case "missing-residual":
				copy.Recovery.ManagedLateRemaining.ResidualCleanup = nil
			}
			before := deploymentplan.Digest(copy)
			got := managedLateResidualInstalledHistoryMatches(&copy.Recovery, copy.Observed, current, copy.State, subjects, allowed, now)
			if got != (mode == "core2" || mode == "core3" || mode == "application5") || before != deploymentplan.Digest(copy) {
				t.Fatal("source, owner or actual native ACK history accepted incorrectly", mode, got)
			}
		})
	}
}

func cloneBoolMapForResidualHistory(source map[string]bool) map[string]bool {
	out := map[string]bool{}
	for key, value := range source {
		out[key] = value
	}
	return out
}

func TestManagedLateResidualHistoricalResourceBindsExactOriginalRecipe(t *testing.T) {
	for _, subject := range []string{"service:account-manager", "service:certissuer", "service:pki-controller", "service:video-cloud-api", "service:video-cloud-logingester"} {
		for _, guarded := range []bool{false, true} {
			for _, mode := range []string{"original", "foreign-session", "foreign-uid", "foreign-kind", "foreign-env", "foreign-command", "foreign-bootstrap", "writable-bootstrap", "foreign-claim", "foreign-root"} {
				t.Run(fmt.Sprint(subject, guarded, mode), func(t *testing.T) {
					sid, root := "9427de3f-845b-4111-8111-111111111111", strings.Repeat("a", 64)
					resource := managedLateResidualHistoryResource(subject, sid, "video-cloud-dev", root, guarded)
					container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(resource.Desired)["containers"])[0])
					isJob := resource.Desired["kind"] == "Job"
					if !isJob && mode != "original" && mode != "foreign-session" && mode != "foreign-uid" && mode != "foreign-kind" && mode != "foreign-env" {
						return
					}
					switch mode {
					case "foreign-session":
						sid = "99999999-1111-4111-8111-111111111111"
					case "foreign-uid":
						resource.UID = "invalid"
					case "foreign-kind":
						resource.Desired["kind"] = "StatefulSet"
					case "foreign-env":
						container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_ENVIRONMENT": "production"}))
					case "foreign-command":
						container["command"] = []any{"/bin/sh", "-ec", "/app/serviceidentity-bootstrap " + subject}
					case "foreign-bootstrap":
						for _, raw := range certIssuerObjectList(environmentPKIPod(resource.Desired)["volumes"]) {
							v := deploymentplan.Map(raw)
							if v["name"] == "bootstrap" {
								deploymentplan.Map(v["secret"])["secretName"] = "foreign"
							}
						}
					case "writable-bootstrap":
						for _, raw := range certIssuerObjectList(container["volumeMounts"]) {
							m := deploymentplan.Map(raw)
							if m["name"] == "bootstrap" {
								m["readOnly"] = false
							}
						}
					case "foreign-claim":
						for _, raw := range certIssuerObjectList(environmentPKIPod(resource.Desired)["volumes"]) {
							v := deploymentplan.Map(raw)
							if v["name"] == "service-state" {
								deploymentplan.Map(v["persistentVolumeClaim"])["claimName"] = "foreign"
							}
						}
					case "foreign-root":
						container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_CLIENT_ROOT_SHA256": strings.Repeat("f", 64)}))
					}
					got := managedLateResidualHistoricalResourceSubject(resource, sid, "video-cloud-dev", "dev", root)
					if (got == subject) != (mode == "original") {
						t.Fatal("original accepted recipe changed", got)
					}
				})
			}
		}
	}
}

func TestManagedLateResidualHistoricalSubjectsRequireAcceptedParentAndChild(t *testing.T) {
	for _, mode := range []string{"accepted", "wrong-intent", "wrong-child-owner", "unaccepted-child", "pending-parent", "outside-source400"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			sid, root := "9427de3f-845b-4111-8111-111111111111", strings.Repeat("a", 64)
			resource := managedLateResidualHistoryResource("service:video-cloud-api", sid, "video-cloud-dev", root, true)
			plan := &deploymentplan.Plan{Version: 1, Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: root, ReleaseSHA256: root, BindingsSHA256: root}, Resources: []deploymentplan.Resource{resource}}
			child := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Accepted: true, AcceptanceStarted: true, Completed: []string{resource.ID}, Owners: map[string]string{resource.ID: resource.UID}}
			intent, err := environmentCreateKubernetesStageIntent(plan)
			if err != nil {
				t.Fatal(err)
			}
			sha, err := environmentCreatePlanSHA256(intent)
			if err != nil {
				t.Fatal(err)
			}
			entry := environmentCreateJournalEntry{Key: "kubernetes/original-enrollment", Phase: "original-enrollment", State: "completed", StartedAt: time.Now().Add(-2 * time.Hour), CompletedAt: time.Now().Add(-time.Hour), IntentSHA256: sha, Receipt: mustProviderRecoveryJSON(map[string]any{"plan_sha256": plan.SHA256(), "owners": child.Owners})}
			e := &environmentCreateExecution{Store: store, Journal: environmentCreateJournal{Environment: "dev", Stack: "video-cloud-dev", Entries: make([]environmentCreateJournalEntry, 400)}}
			e.Journal.Entries[10] = entry
			switch mode {
			case "wrong-intent":
				e.Journal.Entries[10].IntentSHA256 = strings.Repeat("f", 64)
			case "wrong-child-owner":
				child.Owners[resource.ID] = "99999999-1111-4111-8111-111111111111"
			case "unaccepted-child":
				child.Accepted = false
			case "pending-parent":
				e.Journal.Entries[10].State = "pending"
			case "outside-source400":
				e.Journal.Entries[10] = environmentCreateJournalEntry{}
				e.Journal.Entries = append(e.Journal.Entries, entry)
			}
			for name, value := range map[string]any{"plan.json": plan, "journal.json": child} {
				if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/rollback", plan.SHA256(), name), mustProviderRecoveryJSON(value)); err != nil {
					t.Fatal(err)
				}
			}
			got, err := managedLateResidualAcceptedHistoricalSubjects(store, e, sid, environmentCreateServiceCeremony{Root: environmentCreateRegisteredIssuer{RootFingerprint: root}})
			if (err == nil && got["service:video-cloud-api"]) != (mode == "accepted") {
				t.Fatal("unaccepted parent/child subject proof accepted", mode, err, got)
			}
		})
	}
}
