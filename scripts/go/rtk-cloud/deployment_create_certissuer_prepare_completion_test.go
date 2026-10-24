package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func certIssuerPrepareAcceptedFixture(t *testing.T) (*createManagedCorePartialRecovery, *deploymentplan.Plan, deploymentplan.Journal) {
	t.Helper()
	core := managedCoreInterruptedFixture(t)
	core.CertIssuerPrepare = &createCertIssuerPrepareImageRecovery{Image: environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("6", 64), SourceRevision: strings.Repeat("7", 40)}, SelectionSHA: strings.Repeat("8", 64), ManifestSHA: strings.Repeat("9", 64)}
	defs, err := managedCorePartialDefinitions(core)
	if err != nil {
		t.Fatal(err)
	}
	current := []deploymentplan.Object{}
	for index, resource := range core.PriorForward.Plan.Resources {
		body := resource.Desired
		if index == 15 {
			body = resource.Baseline
		}
		actual := deploymentplan.Clone(body)
		deploymentplan.Map(actual["metadata"])["uid"] = resource.UID
		current = append(current, actual)
	}
	inputs := core.PriorForward.Plan.Inputs
	inputs.BindingsSHA256 = environmentCreateStageBindingsSHA("managed-complete-issuer-forward-test", defs)
	plan, err := deploymentplan.Compile(inputs, defs, current)
	if err != nil {
		t.Fatal(err)
	}
	j := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Accepted: true, AcceptanceStarted: true, Owners: map[string]string{}}
	for _, resource := range plan.Resources {
		j.Completed = append(j.Completed, resource.ID)
		j.Owners[resource.ID] = resource.UID
	}
	return core, plan, j
}

func TestCreateCertIssuerPrepareCompletionRequiresGenuineAcceptedSixteenAndOriginalUIDs(t *testing.T) {
	core, plan, j := certIssuerPrepareAcceptedFixture(t)
	if err := validateCertIssuerPrepareAcceptedPlan(core, plan, j); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"selection-only", "pending", "failed", "inflight", "no-acceptance-start", "missing-owner", "replacement", "wrong-order", "missing-completion", "changed-desired", "changed-image", "changed-private-claim", "forced-account-update", "source-without-prior"} {
		t.Run(mode, func(t *testing.T) {
			var c createManagedCorePartialRecovery
			var p deploymentplan.Plan
			var journal deploymentplan.Journal
			json.Unmarshal(mustProviderRecoveryJSON(core), &c)
			json.Unmarshal(mustProviderRecoveryJSON(plan), &p)
			json.Unmarshal(mustProviderRecoveryJSON(j), &journal)
			switch mode {
			case "selection-only":
				c.CertIssuerPrepare = nil
			case "pending":
				journal.Accepted = false
			case "failed":
				journal.Failed = p.Resources[14].ID
			case "inflight":
				journal.InFlight = p.Resources[14].ID
			case "no-acceptance-start":
				journal.AcceptanceStarted = false
			case "missing-owner":
				delete(journal.Owners, p.Resources[14].ID)
			case "replacement":
				journal.Owners[p.Resources[14].ID] = "44444444-4444-4444-8444-444444444444"
			case "wrong-order":
				slices.Reverse(journal.Completed)
			case "missing-completion":
				journal.Completed = journal.Completed[:15]
			case "changed-desired":
				mqttFoundationPrimary(p.Resources[13].Desired)["env"] = mergeEnvironmentNamedFields(mqttFoundationPrimary(p.Resources[13].Desired)["env"], environmentCreateEnv(map[string]string{"UNQUALIFIED": "true"}))
			case "changed-image":
				mqttFoundationPrimary(p.Resources[14].Desired)["image"] = "foreign"
			case "changed-private-claim":
				deploymentplan.Map(managedNamedField(environmentPKIPod(p.Resources[14].Desired)["volumes"], "state")["persistentVolumeClaim"])["claimName"] = "replacement-state"
			case "forced-account-update":
				p.Resources[15].Action = "update"
			case "source-without-prior":
				c.PriorForward = nil
			}
			journal.PlanSHA256 = p.SHA256()
			if validateCertIssuerPrepareAcceptedPlan(&c, &p, journal) == nil {
				t.Fatal("unqualified or future acceptance selected a role overlay", mode)
			}
		})
	}
}

func TestCreateCertIssuerPrepareCompletionMembershipPinsFinalPrefixAndAllowsOwnerArchiveCompletion(t *testing.T) {
	at := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	final := environmentCreateJournal{Schema: environmentCreateJournalSchema, Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: strings.Repeat("a", 64), OwnerToken: "isolated-original-owner", Entries: []environmentCreateJournalEntry{{Key: "genuine-completed-forward", Phase: "bootstrap", State: "completed", StartedAt: at, CompletedAt: at.Add(time.Second), IntentSHA256: strings.Repeat("b", 64), Receipt: mustProviderRecoveryJSON(map[string]string{"observed": "synthetic-isolated"})}}, KubernetesLeases: []deploymentplan.LeaseOwner{{ResourceID: "Lease/video-cloud-dev-video-cloud/rtk-deployment-execution", UID: "11111111-1111-4111-8111-111111111111", Holder: "genuine-recorded-child"}}}
	proof := &createCertIssuerPrepareCompletion{FinalEntryCount: len(final.Entries), FinalEntriesSHA: deploymentplan.Digest(final.Entries), FinalJournalSHA: deploymentplan.Digest(final), CompletedAt: at.Add(time.Second)}
	current := final
	current.Entries = append(slices.Clone(final.Entries), environmentCreateJournalEntry{Key: "later-native-stage"})
	current.KubernetesLeases = append(slices.Clone(final.KubernetesLeases), deploymentplan.LeaseOwner{UID: "later-owned-lease"})
	current.CompletedAt = at.Add(time.Minute)
	if !certIssuerPrepareCompletionMembership(final, current, proof) {
		t.Fatal("genuine later append/archive completion rejected")
	}
	for _, mode := range []string{"empty-prefix-sha", "changed-prefix-sha", "changed-final-sha", "changed-count", "changed-receipt", "changed-intent", "changed-start", "changed-owner", "changed-old-lease", "deleted-prefix", "completed-before-proof", "changed-provider-metadata"} {
		t.Run(mode, func(t *testing.T) {
			var changed environmentCreateJournal
			json.Unmarshal(mustProviderRecoveryJSON(current), &changed)
			p := *proof
			switch mode {
			case "empty-prefix-sha":
				p.FinalEntriesSHA = ""
			case "changed-prefix-sha":
				p.FinalEntriesSHA = strings.Repeat("0", 64)
			case "changed-final-sha":
				p.FinalJournalSHA = strings.Repeat("0", 64)
			case "changed-count":
				p.FinalEntryCount++
			case "changed-receipt":
				changed.Entries[0].Receipt = mustProviderRecoveryJSON(map[string]string{"replacement": "receipt"})
			case "changed-intent":
				changed.Entries[0].IntentSHA256 = strings.Repeat("0", 64)
			case "changed-start":
				changed.Entries[0].StartedAt = changed.Entries[0].StartedAt.Add(time.Second)
			case "changed-owner":
				changed.OwnerToken = "replacement"
			case "changed-old-lease":
				changed.KubernetesLeases[0].UID = "replacement"
			case "deleted-prefix":
				changed.Entries = nil
			case "completed-before-proof":
				changed.CompletedAt = at
			case "changed-provider-metadata":
				changed.ProviderMaterialPlanSHA256 = strings.Repeat("0", 64)
			}
			if certIssuerPrepareCompletionMembership(final, changed, &p) {
				t.Fatal("changed completed source membership accepted", mode)
			}
		})
	}
}

func TestCreateCertIssuerPrepareReleaseOverlayPreservesBusinessAndPreviouslyVerifiedOpenBaoRole(t *testing.T) {
	oldAPI := environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("1", 64), SourceRevision: strings.Repeat("2", 40)}
	captured := environmentRelease{Schema: "rtk-cloud.release/v1", Environment: "dev", Stack: "video-cloud-dev", WorkspaceRevision: strings.Repeat("3", 40), Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": oldAPI, "LKE_OPENBAO_PKI_IMAGE": {Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("4", 64)}, "LKE_TURN_IMAGE": {Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-turn@sha256:" + strings.Repeat("5", 64)}}}
	var caller environmentRelease
	json.Unmarshal(mustProviderRecoveryJSON(captured), &caller)
	caller.Images["LKE_OPENBAO_PKI_IMAGE"] = environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("6", 64)}
	image := environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("7", 64), SourceRevision: strings.Repeat("8", 40)}
	proof := &createCertIssuerPrepareCompletion{OriginalReleaseSHA: deploymentplan.Digest(captured), Recovery: createCertIssuerPrepareImageRecovery{Image: image}}
	before := deploymentplan.Digest(caller)
	if validateCertIssuerPrepareOriginalRelease(caller, captured, proof) != nil {
		t.Fatal("verified previous OpenBao overlay was compared as a new parent release")
	}
	effective := certIssuerPrepareReleaseOverlay(caller, image)
	if deploymentplan.Digest(caller) != before || effective.Images["LKE_VIDEO_CLOUD_IMAGE"] != oldAPI || effective.Images["LKE_CERTISSUER_IMAGE"] != image || effective.Images["LKE_OPENBAO_PKI_IMAGE"] != caller.Images["LKE_OPENBAO_PKI_IMAGE"] {
		t.Fatal("role-only overlay changed original business/provider image")
	}
	delete(effective.Images, "LKE_CERTISSUER_IMAGE")
	if deploymentplan.Digest(effective) != before {
		t.Fatal("unexpected release mutation")
	}
	for _, mode := range []string{"business-api", "turn", "workspace", "foreign-role", "foreign-certissuer", "source-digest"} {
		t.Run(mode, func(t *testing.T) {
			var changed environmentRelease
			json.Unmarshal(mustProviderRecoveryJSON(caller), &changed)
			p := *proof
			switch mode {
			case "business-api":
				changed.Images["LKE_VIDEO_CLOUD_IMAGE"] = image
			case "turn":
				changed.Images["LKE_TURN_IMAGE"] = image
			case "workspace":
				changed.WorkspaceRevision = strings.Repeat("0", 40)
			case "foreign-role":
				changed.Images["UNQUALIFIED_IMAGE"] = image
			case "foreign-certissuer":
				changed.Images["LKE_CERTISSUER_IMAGE"] = oldAPI
			case "source-digest":
				p.OriginalReleaseSHA = strings.Repeat("0", 64)
			}
			if validateCertIssuerPrepareOriginalRelease(changed, captured, &p) == nil {
				t.Fatal("foreign release was admitted by role overlay", mode)
			}
		})
	}
}

func TestCreateCertIssuerPrepareCompletionPointerRejectsHalfPublicationAndPrivatePathChanges(t *testing.T) {
	for _, mode := range []string{"absent", "pointer-only", "mirror-only", "final-only", "changed-mirror", "parent", "schema", "symlink", "permissions"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := prepareCertIssuerPublicationFixture(t)
			review := strings.Repeat("a", 64)
			proof := createCertIssuerPrepareCompletion{Schema: createCertIssuerPrepareCompletionSchema, Parent: f.parent, Review: review}
			write := func(relative string, value any) {
				t.Helper()
				if err := f.store.write(relative, mustProviderRecoveryJSON(value), true); err != nil {
					t.Fatal(err)
				}
			}
			pointer := createCertIssuerPrepareCompletionPath(f.parent)
			base := createCertIssuerPrepareCompletionBase(review)
			if mode == "mirror-only" || mode == "final-only" {
				owner := environmentCreateOwner{Schema: environmentCreateOwnerSchema, PlanSHA256: f.parent, Journal: "deployment/create/executions/" + f.parent + "/execution-isolated/journal.json"}
				write("deployment/.execution-active/owner.json", owner)
				write(owner.Journal, environmentCreateJournal{Entries: []environmentCreateJournalEntry{{Key: "kubernetes/managed-complete-issuer-forward-" + review, State: "completed"}}})
				name := "completed.json"
				if mode == "final-only" {
					name = "final-journal.json"
				}
				write(filepath.Join(base, name), proof)
			} else if mode != "absent" {
				write(pointer, proof)
				if mode != "pointer-only" {
					mirror := proof
					if mode == "changed-mirror" {
						mirror.Review = strings.Repeat("b", 64)
					}
					write(filepath.Join(base, "completed.json"), mirror)
				}
				if mode == "parent" {
					proof.Parent = strings.Repeat("b", 64)
					write(pointer, proof)
				}
				if mode == "schema" {
					proof.Schema = "foreign"
					write(pointer, proof)
				}
				if mode == "permissions" {
					if err := os.Chmod(filepath.Join(f.store.Root, pointer), 0644); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "symlink" {
					path := filepath.Join(f.store.Root, pointer)
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(filepath.Join(f.store.Root, base, "completed.json"), path); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := createOpenBaoACKStoreSnapshot(t, f.store)
			got, err := readCreateCertIssuerPrepareCompletion(f.store, f.parent)
			if mode == "absent" {
				if err != nil || got != nil {
					t.Fatal("unexecuted selection became overlay", err)
				}
			} else if err == nil {
				t.Fatal("half or changed pointer silently admitted", mode)
			}
			if !reflect.DeepEqual(before, createOpenBaoACKStoreSnapshot(t, f.store)) {
				t.Fatal("completion reader changed retained state")
			}
		})
	}
}

func TestCreateCertIssuerPreparePublisherCannotRunBeforeNativeAcceptanceOrWithoutExclusiveFence(t *testing.T) {
	f, _ := prepareCertIssuerPublicationFixture(t)
	core := managedCoreInterruptedFixture(t)
	e := &environmentCreateExecution{Store: f.store}
	runtime := environmentCreateBootstrapRuntime{Execution: e, ServiceSessionRecovery: &createServiceSessionRecovery{ManagedCorePartial: core}}
	before := createOpenBaoACKStoreSnapshot(t, f.store)
	if publishCreateCertIssuerPrepareCompletionWithCommand(context.Background(), f.cfg, runtime, f.command(t)) == nil {
		t.Fatal("selected-only or unfenced future acceptance published")
	}
	if !reflect.DeepEqual(before, createOpenBaoACKStoreSnapshot(t, f.store)) || f.calls != 0 {
		t.Fatal("failed future/fence guard produced evidence or invoked publication")
	}
}

func TestCreateCertIssuerPrepareInventoryRequiresExactOriginalNativeOwnerAndRoleImage(t *testing.T) {
	for _, mode := range []string{"original", "sid-only", "uid", "image", "missing", "duplicate", "extra-container", "ambiguous-name", "terminating", "unrelated-matching-image", "changed-plan", "changed-child-journal"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := prepareCertIssuerPublicationFixture(t)
			core, plan, j := certIssuerPrepareAcceptedFixture(t)
			review := strings.Repeat("b", 64)
			proof := createCertIssuerPrepareCompletion{Schema: createCertIssuerPrepareCompletionSchema, Stack: "video-cloud-dev", Parent: f.parent, Review: review, Recovery: *core.CertIssuerPrepare, AcceptedPlanSHA: plan.SHA256(), AcceptedJournalSHA: deploymentplan.Digest(j)}
			write := func(relative string, value any) {
				t.Helper()
				if err := f.store.write(relative, mustProviderRecoveryJSON(value), true); err != nil {
					t.Fatal(err)
				}
			}
			write(createCertIssuerPrepareCompletionPath(f.parent), proof)
			write(filepath.Join(createCertIssuerPrepareCompletionBase(review), "completed.json"), proof)
			write(filepath.Join("deployment/rollback", plan.SHA256(), "plan.json"), plan)
			write(filepath.Join("deployment/rollback", plan.SHA256(), "journal.json"), j)
			original := plan.Resources[14]
			current := deploymentplan.Clone(original.Desired)
			deploymentplan.Map(current["metadata"])["uid"] = original.UID
			live := []deploymentplan.Object{current}
			switch mode {
			case "sid-only":
				fields, _ := environmentPKINamedFields(mqttFoundationPrimary(current)["env"])
				fields["PKI_BOOTSTRAP_SESSION_ID"]["value"] = "11111111-1111-4111-8111-111111111111"
			case "uid":
				deploymentplan.Map(current["metadata"])["uid"] = "44444444-4444-4444-8444-444444444444"
			case "image":
				mqttFoundationPrimary(current)["image"] = "foreign"
			case "missing":
				live = nil
			case "duplicate":
				live = append(live, deploymentplan.Clone(current))
			case "extra-container":
				pod := environmentPKIPod(current)
				pod["containers"] = append(certIssuerObjectList(pod["containers"]), deploymentplan.Object{"name": "unqualified", "image": proof.Recovery.Image.Reference})
			case "ambiguous-name":
				pod := environmentPKIPod(current)
				pod["containers"] = append(certIssuerObjectList(pod["containers"]), deploymentplan.Clone(mqttFoundationPrimary(current)))
			case "terminating":
				deploymentplan.Map(current["metadata"])["deletionTimestamp"] = "2026-01-01T10:00:00Z"
			case "unrelated-matching-image":
				deploymentplan.Map(current["metadata"])["name"] = "unrelated-certissuer"
			case "changed-plan":
				mqttFoundationPrimary(plan.Resources[14].Desired)["image"] = "foreign"
				write(filepath.Join("deployment/rollback", proof.AcceptedPlanSHA, "plan.json"), plan)
			case "changed-child-journal":
				j.Accepted = false
				write(filepath.Join("deployment/rollback", proof.AcceptedPlanSHA, "journal.json"), j)
			}
			err := validateCreateCertIssuerPrepareInventory(f.store, f.parent, live)
			if (err == nil) != (mode == "original" || mode == "sid-only") {
				t.Fatal("unqualified current native role selected", mode, err)
			}
		})
	}
	f, _ := prepareCertIssuerPublicationFixture(t)
	if validateCreateCertIssuerPrepareInventory(f.store, f.parent, nil) != nil {
		t.Fatal("unexecuted image selection requires an overlay")
	}
}
