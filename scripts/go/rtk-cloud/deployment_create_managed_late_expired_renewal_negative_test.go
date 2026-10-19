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

func managedLateExpiredRenewalNegativeSessionFixture() (managedSessionState, environmentCreateServiceCeremony, []string, time.Time) {
	now := time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC)
	subjects := []string{"service:account-manager", "service:certissuer", "service:emqx-pki", "service:factory-enroll", "service:openbao", "service:pki-controller", "service:pkibroker", "service:video-cloud-api", "service:video-cloud-logingester"}
	slices.Sort(subjects)
	service := environmentCreateServiceCeremony{BootstrapSessionID: "11111111-1111-4111-8111-111111111111", Intermediate: environmentCreateRegisteredIssuer{ID: "22222222-2222-4222-8222-222222222222"}, Root: environmentCreateRegisteredIssuer{RootFingerprint: strings.Repeat("a", 64)}}
	state := managedSessionState{Session: service.BootstrapSessionID, Issuer: service.Intermediate.ID, Root: service.Root.RootFingerprint, Fingerprint: strings.Repeat("b", 64), Status: "active", Expires: now.Add(-time.Hour), Subjects: subjects}
	return state, service, subjects, now
}

func TestManagedLateExpiredUnusedRejectsInstalledLiveOrChangedAuthority(t *testing.T) {
	original, service, subjects, now := managedLateExpiredRenewalNegativeSessionFixture()
	before := deploymentplan.Digest([]any{original, service, subjects})
	for _, status := range []string{"active", "expired"} {
		current := original
		current.Status = status
		if !managedLateExpiredUnusedSession(current, original, service, subjects, now) {
			t.Fatal("genuine expired unused bookkeeping transition refused", status)
		}
	}
	atExpiry := original
	atExpiry.Expires = now
	if !managedLateExpiredUnusedSession(atExpiry, atExpiry, service, subjects, now) {
		t.Fatal("genuine exact-expiry boundary refused")
	}
	for _, mode := range []string{"current-installed", "saved-installed", "other-live", "negative-other-live", "future-expiry", "zero-expiry", "current-finalized", "saved-finalized", "current-revoked", "current-unknown", "changed-session", "changed-issuer", "changed-root", "changed-fingerprint", "invalid-fingerprint", "changed-expiry", "changed-saved-subject", "extra-subject", "missing-subject", "duplicate-subject", "reordered-subjects", "wrong-expected-roster", "wrong-source-session", "wrong-source-issuer", "wrong-source-root"} {
		t.Run(mode, func(t *testing.T) {
			current, saved := original, original
			current.Subjects = slices.Clone(original.Subjects)
			saved.Subjects = slices.Clone(original.Subjects)
			expected := slices.Clone(subjects)
			ceremony := service
			switch mode {
			case "current-installed":
				current.Installed = []string{subjects[0]}
			case "saved-installed":
				saved.Installed = []string{subjects[0]}
			case "other-live":
				current.OtherActive = 1
			case "negative-other-live":
				current.OtherActive = -1
			case "future-expiry":
				current.Expires = now.Add(time.Nanosecond)
				saved.Expires = current.Expires
			case "zero-expiry":
				current.Expires = time.Time{}
				saved.Expires = current.Expires
			case "current-finalized":
				current.Status = "finalized"
			case "saved-finalized":
				saved.Status = "finalized"
			case "current-revoked":
				current.Status = "revoked"
			case "current-unknown":
				current.Status = ""
			case "changed-session":
				current.Session = "33333333-3333-4333-8333-333333333333"
			case "changed-issuer":
				current.Issuer = "33333333-3333-4333-8333-333333333333"
			case "changed-root":
				current.Root = strings.Repeat("c", 64)
			case "changed-fingerprint":
				current.Fingerprint = strings.Repeat("c", 64)
			case "invalid-fingerprint":
				current.Fingerprint = "unverified"
				saved.Fingerprint = current.Fingerprint
			case "changed-expiry":
				current.Expires = current.Expires.Add(-time.Second)
			case "changed-saved-subject":
				saved.Subjects[0] = "service:foreign"
			case "extra-subject":
				current.Subjects = append(current.Subjects, "service:foreign")
			case "missing-subject":
				current.Subjects = current.Subjects[:8]
			case "duplicate-subject":
				current.Subjects[1] = current.Subjects[0]
			case "reordered-subjects":
				current.Subjects[0], current.Subjects[1] = current.Subjects[1], current.Subjects[0]
			case "wrong-expected-roster":
				expected[0] = "service:foreign"
			case "wrong-source-session":
				ceremony.BootstrapSessionID = "33333333-3333-4333-8333-333333333333"
			case "wrong-source-issuer":
				ceremony.Intermediate.ID = "33333333-3333-4333-8333-333333333333"
			case "wrong-source-root":
				ceremony.Root.RootFingerprint = strings.Repeat("c", 64)
			}
			if managedLateExpiredUnusedSession(current, saved, ceremony, expected, now) {
				t.Fatal("changed, installed, live or unknown authority admitted", mode)
			}
		})
	}
	if deploymentplan.Digest([]any{original, service, subjects}) != before {
		t.Fatal("pure eligibility observation mutated its original authority")
	}
}

func managedLateExpiredRenewalNegativePinsFixture() *createManagedLateExpiredRenewal {
	sha := strings.Repeat("a", 64)
	state, service, _, _ := managedLateExpiredRenewalNegativeSessionFixture()
	return &createManagedLateExpiredRenewal{Schema: managedLateExpiredRenewalSchema, ParentSHA: sha, SourceSHA: strings.Repeat("b", 64), SourceOwnerSHA: strings.Repeat("c", 64), PriorReview: strings.Repeat("d", 64), PriorDescriptorSHA: strings.Repeat("e", 64), PriorPostSHA: strings.Repeat("f", 64), PriorSession: state, PriorCertificatePath: "/run/pki-bootstrap/renew-old/bootstrap.crt", PriorCertificateSHA: sha, OriginalKeySHA: sha, OriginalCSRSHA: sha, PriorLate: &createManagedLateSessionRecovery{Review: strings.Repeat("d", 64), SourceCeremony: service, OriginalSession: "44444444-4444-4444-8444-444444444444", OriginalEnrollmentSession: "44444444-4444-4444-8444-444444444444", NewSessionID: state.Session, OriginalKeySHA: sha, OriginalCSRSHA: sha}, PriorPost: &createManagedLatePostMint{OriginalReview: strings.Repeat("d", 64), SourceSHA: sha, Ceremony: service, SessionExpires: state.Expires, LeafNotAfter: state.Expires}, PriorOwners: map[string]deploymentplan.Resource{"Deployment/ns/original": {ID: "Deployment/ns/original", UID: "55555555-5555-4555-8555-555555555555", Action: "retain", Desired: deploymentplan.Object{"metadata": deploymentplan.Object{"name": "original"}}}}}
}

func TestManagedLateExpiredRenewalPinsKeepEveryHistoricalAuthority(t *testing.T) {
	original := managedLateExpiredRenewalNegativePinsFixture()
	before := deploymentplan.Digest(original)
	sourcePins := managedLateExpiredRenewalSourcePins(original)
	if sourcePins == "" || managedLateExpiredRenewalSourcePins(nil) != "" {
		t.Fatal("missing source pin was treated as authority")
	}
	for _, mode := range []string{"schema", "parent", "source", "owner", "prior-review", "prior-descriptor", "prior-post", "prior-session", "prior-installed", "prior-certificate-path", "prior-certificate-bytes", "original-key", "original-csr", "old-late-review", "old-enrollment-session", "old-late-key", "old-post-expiry", "old-owner-uid", "old-owner-body"} {
		t.Run(mode, func(t *testing.T) {
			var changed createManagedLateExpiredRenewal
			if json.Unmarshal(mustProviderRecoveryJSON(original), &changed) != nil {
				t.Fatal("isolated source clone")
			}
			switch mode {
			case "schema":
				changed.Schema = "foreign"
			case "parent":
				changed.ParentSHA = strings.Repeat("1", 64)
			case "source":
				changed.SourceSHA = strings.Repeat("1", 64)
			case "owner":
				changed.SourceOwnerSHA = strings.Repeat("1", 64)
			case "prior-review":
				changed.PriorReview = strings.Repeat("1", 64)
			case "prior-descriptor":
				changed.PriorDescriptorSHA = strings.Repeat("1", 64)
			case "prior-post":
				changed.PriorPostSHA = strings.Repeat("1", 64)
			case "prior-session":
				changed.PriorSession.Session = "66666666-6666-4666-8666-666666666666"
			case "prior-installed":
				changed.PriorSession.Installed = []string{changed.PriorSession.Subjects[0]}
			case "prior-certificate-path":
				changed.PriorCertificatePath = "/run/pki-bootstrap/foreign/bootstrap.crt"
			case "prior-certificate-bytes":
				changed.PriorCertificateSHA = strings.Repeat("1", 64)
			case "original-key":
				changed.OriginalKeySHA = strings.Repeat("1", 64)
			case "original-csr":
				changed.OriginalCSRSHA = strings.Repeat("1", 64)
			case "old-late-review":
				changed.PriorLate.Review = strings.Repeat("1", 64)
			case "old-enrollment-session":
				changed.PriorLate.OriginalEnrollmentSession = "66666666-6666-4666-8666-666666666666"
			case "old-late-key":
				changed.PriorLate.OriginalKeySHA = strings.Repeat("1", 64)
			case "old-post-expiry":
				changed.PriorPost.SessionExpires = changed.PriorPost.SessionExpires.Add(time.Second)
			case "old-owner-uid":
				v := changed.PriorOwners["Deployment/ns/original"]
				v.UID = "66666666-6666-4666-8666-666666666666"
				changed.PriorOwners[v.ID] = v
			case "old-owner-body":
				deploymentplan.Map(changed.PriorOwners["Deployment/ns/original"].Desired["metadata"])["name"] = "foreign"
			}
			if managedLateExpiredRenewalSourcePins(&changed) == sourcePins {
				t.Fatal("original authority change was normalized away", mode)
			}
		})
	}
	runtime := *original
	runtime.Review = strings.Repeat("9", 64)
	runtime.Candidate = &deploymentplan.Plan{}
	runtime.NativeEvidence = map[string]string{"read-only": "observation"}
	if managedLateExpiredRenewalSourcePins(&runtime) != sourcePins || managedLateExpiredRenewalDigest(&runtime) == managedLateExpiredRenewalDigest(original) {
		t.Fatal("source/runtime pin separation lost candidate or native evidence binding")
	}
	reviewOnly := *original
	reviewOnly.Review = strings.Repeat("9", 64)
	if managedLateExpiredRenewalDigest(&reviewOnly) != managedLateExpiredRenewalDigest(original) {
		t.Fatal("assigned review introduced a circular descriptor pin")
	}
	effective := &createManagedLateSessionRecovery{ExpiredRenewal: original}
	effectiveSHA := managedLateSessionSourceDigest(effective)
	assigned := *effective
	assigned.Review = strings.Repeat("9", 64)
	assignedSource := *original
	assignedSource.Review = assigned.Review
	assigned.ExpiredRenewal = &assignedSource
	if managedLateSessionSourceDigest(&assigned) != effectiveSHA {
		t.Fatal("fresh archive assignment changed the already-qualified effective Late digest")
	}
	assignedPrior := *assignedSource.PriorLate
	assignedPrior.Review = strings.Repeat("8", 64)
	assignedSource.PriorLate = &assignedPrior
	if managedLateSessionSourceDigest(&assigned) == effectiveSHA {
		t.Fatal("archive normalization also erased the original admitted Late review")
	}
	if deploymentplan.Digest(original) != before {
		t.Fatal("source digest changed original Late history")
	}
}

func managedLateExpiredRenewalNegativeJournal() environmentCreateJournal {
	now := time.Date(2026, 10, 14, 11, 34, 0, 0, time.UTC)
	review, app := strings.Repeat("a", 64), strings.Repeat("b", 64)
	j := environmentCreateJournal{Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: strings.Repeat("c", 64), Entries: make([]environmentCreateJournalEntry, 370)}
	for i := range j.Entries {
		j.Entries[i] = environmentCreateJournalEntry{Key: "isolated-completed-original", State: "completed", StartedAt: now.Add(-time.Hour), CompletedAt: now.Add(-time.Minute), Receipt: json.RawMessage(`{"original":true}`)}
	}
	j.Entries[361].Key = "kubernetes/service-session-original-key-renewal-" + review
	j.Entries[362].Key = "openbao-recovery-admission-" + strings.Repeat("d", 64)
	j.Entries[366].Key = "kubernetes/service-cleanup-binding-renewal-" + review
	for _, suffix := range []string{"", "/native-import", "/native-runtime-video-cloud-api-app-pki", "/native-runtime-mqtt-pki"} {
		j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "app-root-crl-renewal-" + app + suffix, Phase: "bootstrap", State: "completed", StartedAt: now.Add(-time.Minute), CompletedAt: now, Receipt: json.RawMessage(`{"isolated_app":true}`)})
	}
	return j
}

func TestManagedLateExpiredRenewalSourceCannotTurnUnknownOutcomeIntoUnused(t *testing.T) {
	for _, mode := range []string{"missing-source", "wrong-store", "short-history", "pending-app", "final-parent", "install-admission", "pending-mint", "accepted-owner", "log-outcome", "clock-marker"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}}
			e := &environmentCreateExecution{Store: store, Journal: managedLateExpiredRenewalNegativeJournal()}
			before := deploymentplan.Digest(e.Journal)
			switch mode {
			case "missing-source":
				e = nil
			case "wrong-store":
				e.Store.Root = filepath.Join(store.Root, "foreign")
			case "short-history":
				e.Journal.Entries = e.Journal.Entries[:370]
			case "pending-app":
				e.Journal.Entries[373].State = "pending"
			case "final-parent":
				e.Journal.CompletedAt = time.Now().UTC()
			case "install-admission":
				e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + strings.Repeat("e", 64), State: "pending"})
			case "pending-mint":
				e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "kubernetes/service-session-original-key-renewal-" + strings.Repeat("e", 64), State: "pending"})
			case "accepted-owner":
				e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "kubernetes/managed-late-session-owners-" + strings.Repeat("e", 64), State: "completed"})
			case "log-outcome":
				e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "kubernetes/managed-late-log-existing-ack-" + strings.Repeat("e", 64), State: "completed"})
			case "clock-marker":
				if err := writeEnvironmentCreateDerivedPrivateFile(store, managedLateInstallationClockPath(e.Journal.PlanSHA256), []byte(`{}`)); err != nil {
					t.Fatal(err)
				}
			}
			if e != nil {
				before = deploymentplan.Digest(e.Journal)
			}
			r, f, err := loadManagedLateExpiredRenewalSource(cfg, store, e)
			if err == nil || r != nil || f != nil {
				t.Fatal("unknown, admitted, clocked or incomplete source became an unused authority", mode)
			}
			if mode == "clock-marker" && !strings.Contains(err.Error(), "installation-clock") {
				t.Fatal("existing clock did not block before historical source loading", err)
			}
			if e != nil && deploymentplan.Digest(e.Journal) != before {
				t.Fatal("source refusal rewrote historical outcomes")
			}
		})
	}
}

func TestManagedLateExpiredRenewalKeepsOriginalEnrollmentAndOnlyProspectiveRoleTarget(t *testing.T) {
	const original = "11111111-1111-4111-8111-111111111111"
	const expired = "22222222-2222-4222-8222-222222222222"
	const next = "33333333-3333-4333-8333-333333333333"
	late := &createManagedLateSessionRecovery{OriginalSession: expired, OriginalEnrollmentSession: original, NewSessionID: next, AcceptedCore: &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Stack: "video-cloud-dev"}}, ExpiredRenewal: &createManagedLateExpiredRenewal{Schema: managedLateExpiredRenewalSchema}}
	if managedLateOriginalWorkloadSession(late) != original {
		t.Fatal("retained enrolled workloads were incorrectly moved to the unused temporary authority")
	}
	ordinary := *late
	ordinary.ExpiredRenewal = nil
	if managedLateOriginalWorkloadSession(&ordinary) != expired || managedLateOriginalWorkloadSession(nil) != "" {
		t.Fatal("new profile changed ordinary workload session selection")
	}
	body := deploymentplan.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": deploymentplan.Object{"name": "bootstrap-cleanup", "namespace": "video-cloud-dev-video-cloud", "labels": deploymentplan.Object{"rtk.cloud/stack": "video-cloud-dev"}}, "rules": []any{deploymentplan.Object{"apiGroups": []any{""}, "resources": []any{"secrets"}, "resourceNames": []any{"service-bootstrap-" + expired}, "verbs": []any{"get", "delete"}}}}
	source := deploymentplan.Resource{ID: "Role/video-cloud-dev-video-cloud/bootstrap-cleanup", UID: "44444444-4444-4444-8444-444444444444", Action: "retain", Phase: "configuration", Desired: body, Baseline: deploymentplan.Clone(body)}
	before := deploymentplan.Digest(source)
	projected := managedLateExpiredOwnerDependency(late, source)
	expected := source
	expected.Desired = deploymentplan.Clone(body)
	rule := deploymentplan.Map(certIssuerObjectList(expected.Desired["rules"])[0])
	rule["resourceNames"] = []any{"service-bootstrap-" + next}
	if deploymentplan.Digest(projected) != deploymentplan.Digest(expected) || deploymentplan.Digest(source) != before {
		t.Fatal("prospective Role changed more than its exact Secret target or rewrote accepted source")
	}
	for _, mode := range []string{"extra-rule", "foreign-resource", "foreign-old-session", "extra-verb", "missing-rule", "unknown-rule-field"} {
		t.Run(mode, func(t *testing.T) {
			changed := source
			changed.Desired = deploymentplan.Clone(body)
			rule := deploymentplan.Map(certIssuerObjectList(changed.Desired["rules"])[0])
			switch mode {
			case "extra-rule":
				changed.Desired["rules"] = append(certIssuerObjectList(changed.Desired["rules"]), deploymentplan.Object{"verbs": []any{"*"}})
			case "foreign-resource":
				rule["resources"] = []any{"configmaps"}
			case "foreign-old-session":
				rule["resourceNames"] = []any{"service-bootstrap-" + original}
			case "extra-verb":
				rule["verbs"] = []any{"get", "delete", "list"}
			case "missing-rule":
				delete(changed.Desired, "rules")
			case "unknown-rule-field":
				rule["nonResourceURLs"] = []any{"/foreign"}
			}
			snapshot := deploymentplan.Digest(changed)
			if got := managedLateExpiredOwnerDependency(late, changed); got.ID != "" {
				t.Fatal("foreign Role policy was projected as the sanctioned future target", mode)
			}
			if deploymentplan.Digest(changed) != snapshot {
				t.Fatal("refused Role mutation altered original policy")
			}
		})
	}
	unrelated := source
	unrelated.ID = "Role/video-cloud-dev-video-cloud/foreign"
	if deploymentplan.Digest(managedLateExpiredOwnerDependency(late, unrelated)) != deploymentplan.Digest(unrelated) || deploymentplan.Digest(managedLateExpiredOwnerDependency(&ordinary, source)) != before {
		t.Fatal("prospective repair changed a foreign dependency or ordinary profile")
	}
}
