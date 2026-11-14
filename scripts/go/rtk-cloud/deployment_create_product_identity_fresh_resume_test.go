package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func freshResumeSelectionFixture(t *testing.T) (environmentCreatePlan, environmentCreatePlan, environmentFreshProductResumeBinding) {
	t.Helper()
	images := map[string]environmentReleaseImage{}
	refs := map[string]string{}
	for role, name := range map[string]string{"LKE_VIDEO_CLOUD_IMAGE": "video-cloud-api", "LKE_EMQX_PKI_IMAGE": "video-cloud-emqx-pki", "LKE_OPENBAO_PKI_IMAGE": "video-cloud-openbao-pki"} {
		ref := "ghcr.io/hkt999rtk/rtk_video_cloud/" + name + "@sha256:" + strings.Repeat("a", 64)
		images[role] = environmentReleaseImage{Reference: ref, SourceRevision: strings.Repeat("b", 40)}
		refs[role] = ref
	}
	original := environmentCreatePlan{Environment: "dev", Stack: "video-cloud-dev", ConfigurationSHA256: strings.Repeat("c", 64), Release: environmentRelease{Images: images}, Bootstrap: environmentCreateBootstrapPlan{Environment: "dev", Stack: "video-cloud-dev", Image: refs["LKE_VIDEO_CLOUD_IMAGE"], Images: refs, ProductIdentities: []environmentCreateProductIdentity{{ID: "Secret/ns/original", Subject: "service:shadow", Claim: "original-claim"}}, Domains: []environmentCreateIssuerPolicy{{Domain: "service", ServiceClientIDs: []string{"service:deployment-bootstrap", "service:shadow"}}}}}
	repair := images["LKE_VIDEO_CLOUD_IMAGE"]
	repair.Reference = strings.Replace(repair.Reference, strings.Repeat("a", 64), strings.Repeat("d", 64), 1)
	selected, _, err := issuerEnrollmentForwardPlan(original, map[string]environmentReleaseImage{"video-cloud-api": repair})
	if err != nil {
		t.Fatal(err)
	}
	return original, selected, environmentFreshProductResumeBinding{SelectedPlanSHA256: deploymentplan.Digest(selected), SessionReviewSHA256: strings.Repeat("e", 64), CustodyStage: "service-custody-reader-renewal-" + strings.Repeat("e", 64), CustodyPlanSHA256: strings.Repeat("f", 64)}
}

func TestFreshProductResumeCustodyRequiresGenuineAcceptedOriginalUID(t *testing.T) {
	store, e, base, backend := freshStageFixture(t)
	review, bindingSHA := strings.Repeat("a", 64), strings.Repeat("b", 64)
	stage := "service-custody-reader-renewal-" + review
	session := "11111111-1111-4111-8111-111111111111"
	body := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-video-cloud", "name": "pki-bootstrap-custody-" + e.Journal.PlanSHA256[:12]}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "custody", "image": "native@sha256:" + strings.Repeat("c", 64), "env": environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": session, "PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/renew-" + bindingSHA[:12] + "/bootstrap.crt"})}}}}}}
	defs := []deploymentplan.Definition{{Object: body, Phase: "bootstrap"}}
	inputs := base.Inputs
	inputs.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, defs)
	child, err := deploymentplan.Compile(inputs, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, child, backend, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
		t.Fatal("actual native custody child", err)
	}
	j, exists, err := child.ReadExecution(store.Root)
	if err != nil || !exists || !j.Accepted {
		t.Fatal("actual accepted child missing", err)
	}
	id := deploymentplan.ID(body)
	q := secondSessionAdmission{Renewal: secondSessionQualification{Candidate: base, PreviousCustodyID: id, PreviousCustodyUID: j.Owners[id], BindingSHA: bindingSHA, RenewedIntent: createServiceSessionIntent{SessionID: session}}}
	binding := environmentFreshProductResumeBinding{SessionReviewSHA256: review, CustodyStage: stage, CustodyPlanSHA256: child.SHA256()}
	if p, err := freshProductResumeCustody(store, e, binding, q); err != nil || p.SHA256() != child.SHA256() {
		t.Fatal("genuine accepted same-UID custody child rejected", err)
	}
	path := filepath.Join(store.Root, "deployment/rollback", child.SHA256(), "journal.json")
	journalBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing-candidate", "foreign-owner", "foreign-plan", "foreign-stage", "incomplete-native", "missing-lease"} {
		t.Run(mode, func(t *testing.T) {
			proof, b := q, binding
			leases := slices.Clone(e.Journal.KubernetesLeases)
			t.Cleanup(func() {
				e.Journal.KubernetesLeases = leases
				if err := os.WriteFile(path, journalBytes, 0600); err != nil {
					t.Fatal(err)
				}
			})
			switch mode {
			case "missing-candidate":
				proof.Renewal.Candidate = nil
			case "foreign-owner":
				proof.Renewal.PreviousCustodyUID = "99999999-9999-4999-8999-999999999999"
			case "foreign-plan":
				b.CustodyPlanSHA256 = strings.Repeat("9", 64)
			case "foreign-stage":
				b.CustodyStage += "-foreign"
			case "incomplete-native":
				interrupted := j
				interrupted.Accepted = false
				raw, _ := json.Marshal(interrupted)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-lease":
				e.Journal.KubernetesLeases = nil
			}
			if _, err := freshProductResumeCustody(store, e, b, proof); err == nil {
				t.Fatal("unproven custody child accepted", mode)
			}
		})
	}
}

// A saved snapshot must be the admitted image-only forward of the original
// generation; its own hash cannot authorize changed declarations or settings.
func TestFreshProductResumeSelectionRequiresAdmittedImageOnlyForward(t *testing.T) {
	original, selected, binding := freshResumeSelectionFixture(t)
	release := deploymentplan.Digest(selected.Release)
	if err := verifyFreshProductResumeSelection(original, selected, binding, release); err != nil {
		t.Fatal("same-generation admitted repair was rejected", err)
	}
	for _, mode := range []string{"snapshot-hash", "unadmitted-release", "configuration", "product", "policy", "bootstrap-image", "unrelated-bootstrap", "foreign-repository"} {
		t.Run(mode, func(t *testing.T) {
			var changed environmentCreatePlan
			if json.Unmarshal(mustProviderRecoveryJSON(selected), &changed) != nil {
				t.Fatal("copy selected fixture")
			}
			b, admitted := binding, release
			switch mode {
			case "snapshot-hash":
				b.SelectedPlanSHA256 = strings.Repeat("9", 64)
			case "unadmitted-release":
				admitted = deploymentplan.Digest(original.Release)
			case "configuration":
				changed.ConfigurationSHA256 = strings.Repeat("9", 64)
			case "product":
				changed.Bootstrap.ProductIdentities[0].Claim = "another-owner"
			case "policy":
				changed.Bootstrap.Domains[0].ServiceClientIDs = append(changed.Bootstrap.Domains[0].ServiceClientIDs, "service:foreign")
			case "bootstrap-image":
				changed.Bootstrap.Image = original.Bootstrap.Image
			case "unrelated-bootstrap":
				changed.Bootstrap.AppRoutingMode = "foreign"
			case "foreign-repository":
				image := changed.Release.Images["LKE_VIDEO_CLOUD_IMAGE"]
				image.Reference = strings.Replace(image.Reference, "hkt999rtk/rtk_video_cloud", "foreign/repository", 1)
				changed.Release.Images["LKE_VIDEO_CLOUD_IMAGE"] = image
				changed.Bootstrap.Image, changed.Bootstrap.Images["LKE_VIDEO_CLOUD_IMAGE"] = image.Reference, image.Reference
				admitted = deploymentplan.Digest(changed.Release)
			}
			if mode != "snapshot-hash" {
				b.SelectedPlanSHA256 = deploymentplan.Digest(changed)
			}
			if err := verifyFreshProductResumeSelection(original, changed, b, admitted); err == nil {
				t.Fatal("saved snapshot authorized a changed original input", mode)
			}
		})
	}
}

func TestFreshProductResumeSelectionRetainsAlreadyAdmittedOriginalPlan(t *testing.T) {
	original, _, binding := freshResumeSelectionFixture(t)
	binding.SelectedPlanSHA256 = deploymentplan.Digest(original)
	if err := verifyFreshProductResumeSelection(original, original, binding, deploymentplan.Digest(original.Release)); err != nil {
		t.Fatal("unchanged admitted original plan required an unnecessary image forward", err)
	}
}

func TestFreshProductResumePrefixPermitsOnlyBoundContinuationCompletion(t *testing.T) {
	now := time.Now().UTC().Add(-time.Minute)
	parent, selectedRelease := strings.Repeat("a", 64), strings.Repeat("b", 64)
	original := environmentCreateJournalEntry{Key: issuerEnrollmentContinuationPrefix + strings.Repeat("c", 64), Phase: "continuation", State: "pending", IntentSHA256: strings.Repeat("d", 64), StartedAt: now}
	source := environmentCreateJournal{Schema: environmentCreateJournalSchema, Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: parent, Entries: []environmentCreateJournalEntry{{Key: "original", Phase: "bootstrap", State: "completed", IntentSHA256: "intent", StartedAt: now, CompletedAt: now, Receipt: json.RawMessage(`{"original":true}`)}, original}, KubernetesLeases: []deploymentplan.LeaseOwner{{ResourceID: "Lease/ns/a", UID: "owner-a"}, {ResourceID: "Lease/ns/b", UID: "owner-b"}}}
	sha := deploymentplan.Digest(source)
	for _, state := range []string{"pending", "allocated", "completed"} {
		current := source
		current.Entries = slices.Clone(source.Entries)
		entry := &current.Entries[1]
		entry.State = state
		if state != "pending" {
			// This selected release deliberately differs from the original
			// continuation's qualification release, as in the actual handoff.
			entry.Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"accepted": true, "original_parent": parent, "release_sha256": selectedRelease})
		}
		if state == "completed" {
			entry.CompletedAt = now.Add(time.Second)
		}
		got, err := normalizeFreshProductResumePrefix(current, original, parent, selectedRelease, sha)
		if err != nil || deploymentplan.Digest(got) != sha || current.Entries[1].State != state {
			t.Fatal("valid original continuation advance changed its source", state, err)
		}
		if state != "completed" {
			continue
		}
		for _, mode := range []string{"accepted", "parent", "release", "unknown-receipt", "intent", "started", "other-entry", "lease-order", "lease-missing", "source-hash"} {
			t.Run(mode, func(t *testing.T) {
				bad := current
				bad.Entries, bad.KubernetesLeases = slices.Clone(current.Entries), slices.Clone(current.KubernetesLeases)
				wantSHA := sha
				receipt := jsonRawObject(bad.Entries[1].Receipt)
				switch mode {
				case "accepted":
					receipt["accepted"] = false
				case "parent":
					receipt["original_parent"] = strings.Repeat("9", 64)
				case "release":
					receipt["release_sha256"] = strings.Repeat("9", 64)
				case "unknown-receipt":
					receipt["forged"] = true
				case "intent":
					bad.Entries[1].IntentSHA256 = "foreign"
				case "started":
					bad.Entries[1].StartedAt = now.Add(time.Second)
				case "other-entry":
					bad.Entries[0].Receipt = json.RawMessage(`{"original":false}`)
				case "lease-order":
					slices.Reverse(bad.KubernetesLeases)
				case "lease-missing":
					bad.KubernetesLeases = bad.KubernetesLeases[:1]
				case "source-hash":
					wantSHA = strings.Repeat("9", 64)
				}
				bad.Entries[1].Receipt = mustProviderRecoveryJSON(receipt)
				if _, err := normalizeFreshProductResumePrefix(bad, original, parent, selectedRelease, wantSHA); err == nil {
					t.Fatal("unrelated source advancement was normalized", mode)
				}
			})
		}
	}
}

func TestFreshProductResumeRejectsAdmittedSessionWithoutNativeCompletion(t *testing.T) {
	f := newServiceSessionNativeMintFixture(t)
	before := createBootstrapStateSnapshot(t, f.store.Root)
	if _, err := freshProductResumeCurrentCeremony(f.store, f.e, f.q, f.review, f.service); err == nil {
		t.Fatal("admission alone authorized a current product ceremony")
	}
	assertRecoveryContractReadOnly(t, f.store, before)
}

// A completed successor must render its own immutable cleanup map while the
// original generation's global map remains a different retained owner.
func TestFreshProductResumeProjectsCompletedCleanupWithoutRewritingObservation(t *testing.T) {
	f := newBootstrapCleanupLineageFixture(t)
	path := filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-renewal", f.review, "observed.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var observed createServiceSessionObserved
	if err := decodeEnvironmentJSON(raw, &observed); err != nil || observed.Cleanup.ConfigMapName != "" {
		t.Fatal("fixture must preserve the original unnamed observation", err)
	}
	// Cleanup's fixture retains the full registry state. Persist the exact
	// session SELECT projection written by createEnvironmentServiceSession.
	native := jsonRawObject(observed.Session)
	delete(native, "installed_subjects")
	delete(native, "other_active")
	observed.Session = mustProviderRecoveryJSON(native)
	raw = mustProviderRecoveryJSON(observed)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	if !validSecondSessionAdmission(f.store, f.e, f.q) || deploymentplan.Digest(f.q) != f.review {
		t.Fatal("fixture lacks original admitted current session")
	}
	if done, err := completedServiceSessionBatch(f.store, f.e, f.q, f.review); err != nil || !done {
		t.Fatal("fixture lacks genuine native session completion", err)
	}
	if _, _, err := managedTransportPostAcceptedStage(f.store, f.e, "kubernetes/service-credential-secrets-renewal-"+f.review); err != nil {
		t.Fatal("fixture lacks accepted current credential child", err)
	}
	p := f.parent.Bootstrap
	p.ProductIdentities = []environmentCreateProductIdentity{{Subject: "service:fixture-product"}}
	current, err := remainingFreshProductServiceCeremony(f.store, p, f.runtime, f.service)
	if err != nil {
		t.Fatal("completed original session handoff", err)
	}
	want := observed.Cleanup
	want.ConfigMapName = "pki-bootstrap-cleanup-binding-" + observed.Cleanup.SessionID[:12]
	if deploymentplan.Digest(current.CleanupBinding) != deploymentplan.Digest(want) || current.BootstrapSessionID != f.service.BootstrapSessionID || current.BootstrapCredentialSecret != f.service.BootstrapCredentialSecret || deploymentplan.Digest(current.CredentialUIDs) != deploymentplan.Digest(f.service.CredentialUIDs) || !slices.Equal(current.BootstrapCredentialNamespaces, f.service.BootstrapCredentialNamespaces) || current.CustodyReader != f.service.CustodyReader || deploymentplan.Digest(current.Root) != deploymentplan.Digest(f.service.Root) || deploymentplan.Digest(current.Intermediate) != deploymentplan.Digest(f.service.Intermediate) {
		t.Fatal("projection changed the authenticated session, credential, namespace or public issuer")
	}
	ns := f.service.BootstrapCredentialNamespace
	env := map[string]string{"CLOUD_ENV_NAME": f.store.Environment, "CLOUD_STACK_NAME": f.parent.Stack}
	controller := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": ns, "name": "pki-controller"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"serviceAccountName": "pki-controller", "automountServiceAccountToken": false, "containers": []any{deploymentplan.Object{"name": "pki-controller", "image": f.parent.Bootstrap.Image}}}}}}
	var original createServiceSessionObserved
	if err := readExecutionJSON(filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-observed.json"), &original); err != nil {
		t.Fatal(err)
	}
	oldDefinitions, err := applyEnvironmentBootstrapCleanup(env, deploymentplan.Clone(controller), original.Cleanup)
	if err != nil {
		t.Fatal(err)
	}
	oldMap := deploymentplan.Clone(oldDefinitions[0].Object)
	oldMapID, oldMapSHA := deploymentplan.ID(oldMap), deploymentplan.Digest(oldMap)
	if oldMapID != "ConfigMap/"+ns+"/pki-bootstrap-cleanup-binding" || oldMap["immutable"] != true {
		t.Fatal("fixture lacks the original immutable global cleanup owner")
	}
	definitions, err := applyEnvironmentBootstrapCleanup(env, controller, current.CleanupBinding)
	if err != nil {
		t.Fatal(err)
	}
	if deploymentplan.ID(definitions[0].Object) != "ConfigMap/"+ns+"/"+want.ConfigMapName {
		t.Fatal("current cleanup projection selected the original map")
	}
	mounted := false
	for _, item := range certIssuerObjectList(environmentPKIPod(controller)["volumes"]) {
		volume := deploymentplan.Map(item)
		if volume["name"] == "bootstrap-cleanup" {
			mounted = deploymentplan.Map(volume["configMap"])["name"] == want.ConfigMapName
		}
	}
	if !mounted {
		t.Fatal("controller does not mount its current session cleanup projection")
	}
	definitions = append(definitions, deploymentplan.Definition{Phase: "workload", Object: controller})
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: f.store.Environment, Stack: f.parent.Stack, Operation: "scoped", ConfigurationSHA256: f.parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(f.parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA("managed-complete-issuer", definitions)}, definitions, []deploymentplan.Object{oldMap})
	if err != nil {
		t.Fatal("remaining issuer compilation attempted to replace original immutable cleanup map", err)
	}
	for _, resource := range plan.Resources {
		if resource.ID == oldMapID {
			t.Fatal("new issuer stage owns the original cleanup map")
		}
	}
	if deploymentplan.Digest(oldMap) != oldMapSHA {
		t.Fatal("original cleanup owner was mutated")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("projection rewrote its admitted observation", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	for _, mode := range []string{"foreign-map", "foreign-session", "foreign-credential"} {
		t.Run(mode, func(t *testing.T) {
			var changed createServiceSessionObserved
			if err := decodeEnvironmentJSON(raw, &changed); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "foreign-map":
				changed.Cleanup.ConfigMapName = "pki-bootstrap-cleanup-binding-foreign"
			case "foreign-session":
				changed.Cleanup.SessionID = "99999999-9999-4999-8999-999999999999"
			case "foreign-credential":
				changed.Cleanup.SecretUID = "99999999-9999-4999-8999-999999999999"
			}
			if err := os.WriteFile(path, mustProviderRecoveryJSON(changed), 0600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			})
			if _, err := remainingFreshProductServiceCeremony(f.store, p, f.runtime, f.service); err == nil {
				t.Fatal("foreign cleanup binding was projected", mode)
			}
		})
	}
}

func TestFreshProductResumeExecutionContextBoundaries(t *testing.T) {
	f := newProductProtocolFixture(t, false)
	selected := environmentCreatePlan{Environment: f.cfg.Environment, Stack: f.p.Stack, ConfigurationSHA256: deploymentplan.Digest(f.cfg), Bootstrap: f.p}
	if err := freshProductResumeSelectedInputs(f.cfg, f.p, environmentRelease{}, selected); err != nil {
		t.Fatal("same original product context", err)
	}
	for _, boundary := range []string{"selected-configuration", "selected-bootstrap", "selected-release"} {
		t.Run(boundary, func(t *testing.T) {
			var changed environmentCreatePlan
			if json.Unmarshal(mustProviderRecoveryJSON(selected), &changed) != nil {
				t.Fatal("copy selected inputs")
			}
			switch boundary {
			case "selected-configuration":
				changed.ConfigurationSHA256 = strings.Repeat("f", 64)
			case "selected-bootstrap":
				changed.Bootstrap.Image = "isolated-context-drift"
			case "selected-release":
				changed.Release.Environment = "foreign"
			}
			if err := freshProductResumeSelectedInputs(f.cfg, f.p, environmentRelease{}, changed); err == nil || !strings.Contains(err.Error(), boundary) {
				t.Fatal("selected input drift lacks exact safe boundary", err)
			}
		})
	}
	service := environmentCreateServiceCeremony{Root: f.l.Root, Intermediate: f.next}
	if err := freshProductResumeCeremonyMetadata(service, service); err != nil {
		t.Fatal("same original public lineage", err)
	}
	for _, boundary := range []string{"service-root-metadata", "service-intermediate-metadata"} {
		t.Run(boundary, func(t *testing.T) {
			var changed environmentCreateServiceCeremony
			if json.Unmarshal(mustProviderRecoveryJSON(service), &changed) != nil {
				t.Fatal("copy ceremony")
			}
			if boundary == "service-root-metadata" {
				changed.Root.Document["isolated_observation"] = "changed"
			} else {
				changed.Intermediate.Document["isolated_observation"] = "changed"
			}
			if err := freshProductResumeCeremonyMetadata(changed, service); err == nil || !strings.Contains(err.Error(), boundary) {
				t.Fatal("metadata drift lacks exact safe boundary", err)
			}
		})
	}
}
