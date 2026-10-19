package main

import (
	"encoding/json"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"testing"
	"time"
)

func managedLatePostMintOwnerFixture(t *testing.T) (*createManagedLateSessionRecovery, *createManagedLatePostMint) {
	t.Helper()
	late, children := managedLateAcceptedCleanupFixture(t)
	cleanup, err := managedLateAcceptedCleanupRBAC(late, children)
	if err != nil {
		t.Fatal(err)
	}
	late.DependencyOwners = map[string]deploymentplan.Resource{}
	var deps []string
	for _, resource := range late.AcceptedCore.Resources {
		if _, ok := cleanup[resource.ID]; ok {
			late.DependencyOwners[resource.ID] = resource
			deps = append(deps, resource.ID)
		}
	}
	slices.Sort(deps)
	controllerID := "Deployment/" + late.AcceptedCore.Stack + "-video-cloud/pki-controller"
	controller := late.Owners[controllerID]
	controller.Dependencies = deps
	late.Owners[controllerID] = controller
	service := late.SourceCeremony
	service.BootstrapSessionID = late.NewSessionID
	service.BootstrapCredentialSecret = "service-bootstrap-" + late.NewSessionID
	expires := time.Now().UTC().Add(8 * time.Hour)
	post := &createManagedLatePostMint{OriginalReview: late.Review, CleanupOwners: cleanup, Ceremony: service, InstallationPolicy: managedLateInstallationPolicy, SessionExpires: expires, LeafNotAfter: expires, Fingerprint: strings.Repeat("a", 64)}
	defs, err := managedLatePostMintDefinitions(late, post)
	if err != nil {
		t.Fatal(err)
	}
	var live []deploymentplan.Object
	for _, owner := range late.Owners {
		body := deploymentplan.Clone(owner.Desired)
		deploymentplan.Map(body["metadata"])["uid"] = owner.UID
		live = append(live, body)
	}
	for _, owner := range cleanup {
		body := deploymentplan.Clone(owner.Desired)
		deploymentplan.Map(body["metadata"])["uid"] = owner.UID
		live = append(live, body)
	}
	inputs := late.AcceptedCore.Inputs
	inputs.BindingsSHA256 = environmentCreateStageBindingsSHA("managed-late-session-owners", defs)
	post.Candidate, err = deploymentplan.Compile(inputs, defs, live)
	if err != nil {
		t.Fatal(err)
	}
	return late, post
}
func TestManagedLatePostMintOwnerCandidateRetainsOnlyAcceptedCleanupSupersession(t *testing.T) {
	late, post := managedLatePostMintOwnerFixture(t)
	before := managedLateSessionSourceDigest(late)
	if err := validateManagedLatePostMintCandidate(late, post, post.Candidate); err != nil {
		t.Fatal(err)
	}
	updates, roles, log := 0, 0, 0
	for _, resource := range post.Candidate.Resources {
		if resource.Action == "update" {
			updates++
		}
		if _, ok := post.CleanupOwners[resource.ID]; ok {
			roles++
			if resource.Action != "retain" || deploymentplan.Digest(resource.Desired) != deploymentplan.Digest(post.CleanupOwners[resource.ID].Desired) {
				t.Fatal("cleanup owner privilege/replay")
			}
		}
		if strings.HasSuffix(resource.ID, "/video-cloud-logingester") {
			log++
			if resource.Action != "retain" {
				t.Fatal("Log changed before exclusive helper")
			}
		}
	}
	if updates != 9 || roles != 4 || log != 1 || managedLateSessionSourceDigest(late) != before {
		t.Fatal("scoped nine SID updates / four genuine RBAC retains / original Log or immutable source changed")
	}
}
func TestManagedLatePostMintOwnerCandidateRejectsUnrelatedChanges(t *testing.T) {
	for _, kind := range []string{"image", "extra env", "replacement UID", "retained Role rollback", "extra resource", "DAG edge", "completed dependency action"} {
		t.Run(kind, func(t *testing.T) {
			late, post := managedLatePostMintOwnerFixture(t)
			var candidate deploymentplan.Plan
			raw, _ := json.Marshal(post.Candidate)
			if json.Unmarshal(raw, &candidate) != nil {
				t.Fatal("isolated candidate")
			}
			index := 0
			for i, r := range candidate.Resources {
				if r.Action == "update" {
					index = i
					break
				}
			}
			r := &candidate.Resources[index]
			switch kind {
			case "image":
				managedNamedField(environmentPKIPod(r.Desired)["containers"], "native")["image"] = "foreign@sha256:" + strings.Repeat("f", 64)
			case "extra env":
				c := managedNamedField(environmentPKIPod(r.Desired)["containers"], "native")
				c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"FOREIGN": "value"}))
			case "replacement UID":
				r.UID = "99999999-9999-4999-8999-999999999999"
			case "retained Role rollback":
				for i, r := range candidate.Resources {
					if strings.HasPrefix(r.ID, "Role/") {
						candidate.Resources[i].Desired = deploymentplan.Clone(late.DependencyOwners[r.ID].Desired)
					}
				}
			case "extra resource":
				candidate.Resources = append(candidate.Resources, candidate.Resources[0])
			case "DAG edge":
				r.Dependencies = append(r.Dependencies, "ConfigMap/foreign/extra")
			case "completed dependency action":
				for i, r := range candidate.Resources {
					if strings.HasPrefix(r.ID, "Role/") {
						candidate.Resources[i].Action = "update"
					}
				}
			}
			if validateManagedLatePostMintCandidate(late, post, &candidate) == nil {
				t.Fatal("unqualified remaining-owner delta accepted")
			}
		})
	}
}
func TestManagedLatePostMintDigestBindsImmutableClockAndSource(t *testing.T) {
	_, post := managedLatePostMintOwnerFixture(t)
	post.SourceSHA = strings.Repeat("a", 64)
	original := managedLatePostMintDigest(post)
	post.Review = strings.Repeat("b", 64)
	if managedLatePostMintDigest(post) != original {
		t.Fatal("assigned review created digest cycle")
	}
	post.NativeDeadline = post.NativeDeadline.Add(time.Minute)
	if managedLatePostMintDigest(post) == original {
		t.Fatal("native cutoff extension escaped qualification")
	}
	post.NativeDeadline = post.NativeDeadline.Add(-time.Minute)
	post.SourceSHA = strings.Repeat("c", 64)
	if managedLatePostMintDigest(post) == original {
		t.Fatal("source replacement escaped qualification")
	}
}

func TestManagedLatePostMintQualificationDoesNotStartInstallationClock(t *testing.T) {
	_, post := managedLatePostMintOwnerFixture(t)
	pin := managedLatePostMintBudgetPin(post)
	post.Review = strings.Repeat("f", 64)
	if managedLatePostMintBudgetPin(post) != pin || !post.NativeStartedAt.IsZero() || !post.NativeDeadline.IsZero() || post.InstallationClock != nil {
		t.Fatal("qualification allocated an installation clock")
	}
	post.SessionExpires = post.SessionExpires.Add(time.Minute)
	if managedLatePostMintBudgetPin(post) == pin {
		t.Fatal("different genuine native expiry escaped qualified policy")
	}
}

func TestManagedLatePostMintReviewRoutesOnlyCompletedLiteralAppTail(t *testing.T) {
	now := time.Now().UTC().Add(-time.Hour)
	review, appReview := strings.Repeat("a", 64), strings.Repeat("b", 64)
	journal := environmentCreateJournal{Entries: make([]environmentCreateJournalEntry, 370)}
	for i := range journal.Entries {
		journal.Entries[i] = environmentCreateJournalEntry{Key: "authenticated-original-" + strings.Repeat("x", i%3+1), State: "completed", StartedAt: now, CompletedAt: now.Add(time.Second), Receipt: []byte(`{"genuine":"original"}`)}
	}
	journal.Entries[361].Key = "kubernetes/service-session-original-key-renewal-" + review
	journal.Entries[362].Key = "openbao-recovery-admission-" + strings.Repeat("c", 64)
	journal.Entries[366].Key = "kubernetes/service-cleanup-binding-renewal-" + review
	if got := managedLatePostMintReview(journal); got != review {
		t.Fatal("original370 routing changed", got)
	}
	key := "app-root-crl-renewal-" + appReview
	for _, suffix := range []string{"", "/native-import", "/native-runtime-video-cloud-api-app-pki", "/native-runtime-mqtt-pki"} {
		journal.Entries = append(journal.Entries, environmentCreateJournalEntry{Key: key + suffix, Phase: "bootstrap", State: "completed", StartedAt: now.Add(time.Minute), CompletedAt: now.Add(2 * time.Minute), Receipt: []byte(`{"genuine":"App"}`)})
	}
	before := deploymentplan.Digest(journal)
	if got := managedLatePostMintReview(journal); got != review || deploymentplan.Digest(journal) != before {
		t.Fatal("completed App routing changed actual source", got)
	}
	for _, mode := range []string{"pending", "phase", "missing-time", "missing-receipt", "foreign-target", "bad-review", "unknown-tail"} {
		t.Run(mode, func(t *testing.T) {
			changed := journal
			changed.Entries = slices.Clone(journal.Entries)
			switch mode {
			case "pending":
				changed.Entries[373].State = "pending"
			case "phase":
				changed.Entries[370].Phase = "recovery"
			case "missing-time":
				changed.Entries[371].StartedAt = time.Time{}
			case "missing-receipt":
				changed.Entries[372].Receipt = nil
			case "foreign-target":
				changed.Entries[373].Key = key + "/native-runtime-foreign"
			case "bad-review":
				changed.Entries[370].Key = "app-root-crl-renewal-bad"
			case "unknown-tail":
				changed.Entries = append(changed.Entries, environmentCreateJournalEntry{Key: "unknown", State: "completed", StartedAt: now, CompletedAt: now.Add(time.Second), Receipt: []byte(`{}`)})
			}
			if managedLatePostMintReview(changed) != "" {
				t.Fatal("unknown/incomplete App tail routed to installation", mode)
			}
		})
	}
}

func TestManagedLatePostMintParsingRemovesOnlyActualAdjustedAdmission(t *testing.T) {
	e := &environmentCreateExecution{Journal: environmentCreateJournal{PlanSHA256: strings.Repeat("a", 64), Entries: []environmentCreateJournalEntry{{Key: "original"}, {Key: "openbao-recovery-admission-" + strings.Repeat("b", 64)}, {Key: "actual-owner-child"}}}}
	before := deploymentplan.Digest(e.Journal)
	parsed, err := managedLatePostMintRemoveAdmission(e, 1)
	if err != nil || len(parsed.Journal.Entries) != 2 || parsed.Journal.Entries[0].Key != "original" || parsed.Journal.Entries[1].Key != "actual-owner-child" || deploymentplan.Digest(e.Journal) != before {
		t.Fatal("adjusted parsing rewrote actual journal", err)
	}
	for _, index := range []int{-1, 0, 2, 3} {
		if _, err := managedLatePostMintRemoveAdmission(e, index); err == nil {
			t.Fatal("foreign position removed", index)
		}
	}
	if _, err := managedLatePostMintRemoveAdmission(nil, 0); err == nil {
		t.Fatal("nil source")
	}
}
