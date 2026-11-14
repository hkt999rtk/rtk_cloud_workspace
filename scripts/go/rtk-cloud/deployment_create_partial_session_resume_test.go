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

func TestPartialSessionJournalRequiresOriginalMonotonicHistory(t *testing.T) {
	now := time.Now().UTC()
	lease := deploymentplan.LeaseOwner{ResourceID: "Lease/ns/original", UID: "original", Holder: "parent"}
	source := environmentCreateJournal{Schema: "original", Environment: "dev", Stack: "dev-stack", PlanSHA256: "parent", ArtifactSHA256: "artifact", OwnerToken: "owner", AdmittedAt: now, KubernetesLeases: []deploymentplan.LeaseOwner{lease}, Entries: []environmentCreateJournalEntry{
		{Key: "old", Phase: "bootstrap", State: "completed", IntentSHA256: "intent-1", StartedAt: now, CompletedAt: now, Receipt: json.RawMessage(`{"ok":true}`)},
		{Key: "pending", Phase: "stage", State: "pending", IntentSHA256: "intent-2", StartedAt: now},
	}}
	current := source
	current.Entries = slices.Clone(source.Entries)
	current.Entries[1].State = "completed"
	current.Entries[1].Receipt = json.RawMessage(`{"plan":"child"}`)
	current.Entries[1].CompletedAt = now.Add(time.Second)
	current.Entries = append(current.Entries, environmentCreateJournalEntry{Key: "renewal", Phase: "bootstrap", State: "pending", IntentSHA256: "intent-3", StartedAt: now})
	if !partialSessionJournalExtends(source, current) {
		t.Fatal("valid continuation rejected")
	}
	for _, mode := range []string{"owner", "missing-prefix", "changed-receipt", "regressed", "changed-intent", "missing-lease", "duplicate", "completed-parent"} {
		t.Run(mode, func(t *testing.T) {
			bad := current
			bad.Entries = slices.Clone(current.Entries)
			switch mode {
			case "owner":
				bad.OwnerToken = "replacement"
			case "missing-prefix":
				bad.Entries = bad.Entries[1:]
			case "changed-receipt":
				bad.Entries[0].Receipt = json.RawMessage(`{"ok":false}`)
			case "regressed":
				bad.Entries[0].State = "pending"
				bad.Entries[0].Receipt = nil
				bad.Entries[0].CompletedAt = time.Time{}
			case "changed-intent":
				bad.Entries[1].IntentSHA256 = "other"
			case "missing-lease":
				bad.KubernetesLeases = nil
			case "duplicate":
				bad.Entries = append(bad.Entries, bad.Entries[0])
			case "completed-parent":
				bad.CompletedAt = now.Add(time.Second)
			}
			if partialSessionJournalExtends(source, bad) {
				t.Fatal("changed source accepted", mode)
			}
		})
	}
}

func TestPartialSessionReceiptFormattingSurvivesJournalRoundTrip(t *testing.T) {
	now := time.Now().UTC()
	for _, state := range []string{"allocated", "completed"} {
		original := environmentCreateJournalEntry{Key: "owner", Phase: "stage", State: state, StartedAt: now, Receipt: json.RawMessage("{\n  \"key\": \"<value>\",\n  \"owner\": \"original\"\n}")}
		if state == "completed" {
			original.CompletedAt = now
		}
		raw, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		var reloaded environmentCreateJournalEntry
		if err = json.Unmarshal(raw, &reloaded); err != nil || !partialSessionEntryAdvances(original, reloaded) {
			t.Fatal("formatted original receipt rejected after JSON persistence", err)
		}
		for _, bad := range []json.RawMessage{json.RawMessage(`{"key":"<value>","owner":"replacement"}`), json.RawMessage(`{"owner":"original","key":"<value>"}`), json.RawMessage(`{"key":`)} {
			reloaded.Receipt = bad
			if partialSessionEntryAdvances(original, reloaded) {
				t.Fatal("changed, reordered or invalid receipt accepted")
			}
		}
	}
}

func TestOpenBaoACKMembershipAcceptsOnlyPersistedEquivalentReceipt(t *testing.T) {
	now := time.Now().UTC()
	owner := environmentCreateOwner{Environment: "dev", Stack: "stack", PlanSHA256: "parent", OwnerToken: "owner"}
	source := environmentCreateJournal{Schema: environmentCreateJournalSchema, Environment: owner.Environment, Stack: owner.Stack, PlanSHA256: owner.PlanSHA256, OwnerToken: owner.OwnerToken, Entries: []environmentCreateJournalEntry{{Key: "original", Phase: "stage", State: "completed", IntentSHA256: "intent", StartedAt: now, CompletedAt: now, Receipt: json.RawMessage("{\n \"key\": \"<value>\"\n}")}}}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded environmentCreateJournal
	if err = json.Unmarshal(raw, &reloaded); err != nil || !createOpenBaoACKCompletionMembership(source, reloaded, owner) {
		t.Fatal("equivalent receipt was rejected", err)
	}
	reloaded.Entries[0].Receipt = json.RawMessage(`{"key":"replacement"}`)
	if createOpenBaoACKCompletionMembership(source, reloaded, owner) {
		t.Fatal("changed receipt was accepted")
	}
}

func TestPartialSessionAllowsOnlyNaturalExpiryTransition(t *testing.T) {
	now := time.Now().UTC()
	saved := managedSessionState{Session: "original", Issuer: "issuer", Root: "root", Fingerprint: "fingerprint", Status: "active", Expires: now.Add(-time.Hour), Subjects: []string{"one", "two"}, Installed: []string{"one"}}
	expired := saved
	expired.Status = "expired"
	if !partialSessionOriginalStatePreserved(saved, expired, now) || !partialSessionOriginalStatePreserved(saved, saved, now) {
		t.Fatal("unchanged expired original session rejected")
	}
	for _, mode := range []string{"reversed", "issuer", "fingerprint", "expiry", "installed", "competing", "sealed"} {
		t.Run(mode, func(t *testing.T) {
			before, after := saved, expired
			switch mode {
			case "reversed":
				before, after = expired, saved
			case "issuer":
				after.Issuer = "other"
			case "fingerprint":
				after.Fingerprint = "other"
			case "expiry":
				after.Expires = after.Expires.Add(time.Second)
			case "installed":
				after.Installed = []string{"two"}
			case "competing":
				after.OtherActive = 1
			case "sealed":
				after.Status = "sealed"
			}
			if partialSessionOriginalStatePreserved(before, after, now) {
				t.Fatal("changed original session accepted", mode)
			}
		})
	}
}

func TestPartialSessionProofBindsCandidateAndSource(t *testing.T) {
	now := time.Now().UTC()
	digest := strings.Repeat("a", 64)
	source := environmentCreateJournal{Schema: "v1", Environment: "dev", Stack: "stack", PlanSHA256: digest, OwnerToken: "owner", AdmittedAt: now, Entries: []environmentCreateJournalEntry{{Key: "kubernetes/service-session", Phase: "service-session", State: "completed", IntentSHA256: digest, StartedAt: now, CompletedAt: now, Receipt: json.RawMessage(`{"plan_sha256":"` + digest + `"}`)}}}
	intent := createServiceSessionIntent{SessionID: "33333333-3333-4333-8333-333333333333", IssuerID: "44444444-4444-4444-8444-444444444444", RootSHA: digest, Subjects: []string{"service:certissuer"}, Namespaces: []string{"stack-video-cloud"}}
	renewed := intent
	renewed.SessionID = sessionRenewalUUID(digest, digest, intent.SessionID)
	env := map[string]string{"PKI_BOOTSTRAP_SESSION_ID": renewed.SessionID, "PKI_BOOTSTRAP_ISSUER_ID": renewed.IssuerID, "PKI_BOOTSTRAP_ROOT_SHA256": renewed.RootSHA, "PKI_BOOTSTRAP_SUBJECTS": strings.Join(renewed.Subjects, ","), "PKI_BOOTSTRAP_KEY_PATH": "/run/pki-bootstrap/bootstrap.key", "PKI_BOOTSTRAP_CSR_PATH": "/run/pki-bootstrap/bootstrap.csr", "PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/renew-" + digest[:12] + "/bootstrap.crt"}
	job := deploymentplan.Object{"kind": "Job", "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"env": environmentCreateEnv(env)}}}}}}
	candidate := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: source.Environment, Stack: source.Stack, ReleaseSHA256: digest}, Resources: []deploymentplan.Resource{{ID: "Job/ns/renew", Action: "create", Desired: job}}}
	originalState := &managedSessionState{Session: intent.SessionID, Issuer: intent.IssuerID, Root: intent.RootSHA, Fingerprint: digest, Status: "active", Expires: now.Add(-time.Minute), Subjects: intent.Subjects}
	recovery := createManagedSessionRecovery{PartialInventory: true, OriginalState: originalState, OriginalSession: intent.SessionID, OriginalCertificatePath: "/run/pki-bootstrap/bootstrap.crt", OriginalCertificateSHA256: digest, SessionSHA256: digest, ClientsSHA256: digest, SourceStage: "kubernetes/service-session", SourcePlanSHA256: digest, SourceJournalSHA256: digest, BindingSHA256: digest, CandidateSHA256: candidate.SHA256()}
	proof := issuerEnrollmentContinuationProof{Parent: digest, ParentJournal: deploymentplan.Digest(source), ParentOwner: deploymentplan.Digest(environmentCreateOwner{}), Release: digest, PartialSession: &partialSessionContinuationProof{SourceJournal: source, Recovery: recovery, Intent: intent, RenewedIntent: renewed, KeySHA: digest, CSRSHA: digest, Candidate: candidate}}
	if !partialSessionProofMatches(proof, source) {
		t.Fatal("original bound proof rejected")
	}
	review := deploymentplan.Digest(proof)
	qualified := proof
	qualified.QualifiedAt, qualified.ValidUntil = now, now.Add(time.Hour)
	admission := environmentCreateJournalEntry{Key: "partial-session-renewal-" + review, Phase: "partial-session-renewal", State: "pending", StartedAt: now, IntentSHA256: activeTransportJournalDigest(partialSessionAdmissionIntent(digest, review, proof.PartialSession))}
	withAdmission := source
	withAdmission.Entries = append(slices.Clone(source.Entries), admission)
	if !partialSessionAdmissionValid(qualified, review, admission, withAdmission) {
		t.Fatal("valid originally admitted continuation rejected")
	}
	store := makeIsolatedTestSecretStore(t, "dev")
	path := filepath.Join("deployment/create/issuer-enrollment-continuation", review, "qualification.json")
	if err := store.write(path, mustProviderRecoveryJSON(qualified), false); err != nil {
		t.Fatal(err)
	}
	existing := &environmentCreateExecution{Store: store, Journal: withAdmission}
	loaded, loadedReview, ok, err := loadPartialSessionAdmission(store, existing)
	if err != nil || !ok || loadedReview != review || deploymentplan.Digest(loaded) != review {
		t.Fatal("original qualification not loaded", err)
	}
	foreign := *existing
	foreign.owner.OwnerToken = "foreign"
	if _, _, _, err := loadPartialSessionAdmission(store, &foreign); err == nil {
		t.Fatal("changed execution owner accepted")
	}
	for _, mode := range []string{"late", "intent", "review", "qualification", "phase"} {
		t.Run("admission-"+mode, func(t *testing.T) {
			bad, entry, sha := qualified, admission, review
			switch mode {
			case "late":
				entry.StartedAt = qualified.ValidUntil
			case "intent":
				entry.IntentSHA256 = strings.Repeat("b", 64)
			case "review":
				sha = strings.Repeat("b", 64)
			case "qualification":
				bad.ValidUntil = now.Add(2 * time.Hour)
			case "phase":
				entry.Phase = "foreign"
			}
			if partialSessionAdmissionValid(bad, sha, entry, withAdmission) {
				t.Fatal("invalid continuation admission accepted", mode)
			}
		})
	}
	old := *originalState
	old.Status, old.OtherActive = "expired", 1
	next := managedSessionState{Session: renewed.SessionID, Issuer: renewed.IssuerID, Root: renewed.RootSHA, Fingerprint: digest, Status: "active", Expires: now.Add(time.Hour), Subjects: renewed.Subjects}
	if !partialSessionSuccessorStatesValid(proof.PartialSession, old, next, now) {
		t.Fatal("valid original and successor states rejected")
	}
	for _, mode := range []string{"old-fingerprint", "old-installations", "multiple-active", "new-session", "new-expired", "new-installation"} {
		t.Run(mode, func(t *testing.T) {
			prior, successor := old, next
			switch mode {
			case "old-fingerprint":
				prior.Fingerprint = "other"
			case "old-installations":
				prior.Installed = []string{"foreign"}
			case "multiple-active":
				prior.OtherActive = 2
			case "new-session":
				successor.Session = "other"
			case "new-expired":
				successor.Expires = now.Add(-time.Second)
			case "new-installation":
				successor.Installed = []string{"foreign"}
			}
			if partialSessionSuccessorStatesValid(proof.PartialSession, prior, successor, now) {
				t.Fatal("changed session lineage accepted", mode)
			}
		})
	}
	resumed := &createServiceSessionRecovery{Ceremony: environmentCreateServiceCeremony{BootstrapSessionID: intent.SessionID, BootstrapClaim: intent.Claim, BootstrapCredentialNamespaces: intent.Namespaces, Intermediate: environmentCreateRegisteredIssuer{ID: intent.IssuerID}, Root: environmentCreateRegisteredIssuer{RootFingerprint: intent.RootSHA}}, ManagedTransportRecovery: &createManagedTransportRecovery{}}
	if err := restorePartialSessionProof(proof, deploymentplan.Digest(proof), source, resumed); err != nil || resumed.RenewedIntent.SessionID != renewed.SessionID || resumed.RenewalCandidate.SHA256() != candidate.SHA256() {
		t.Fatal("qualified original continuation not restored", err)
	}
	resumed.Ceremony.Intermediate.ID = "replacement"
	if err := restorePartialSessionProof(proof, deploymentplan.Digest(proof), source, resumed); err == nil {
		t.Fatal("foreign Service lineage accepted")
	}
	for _, mode := range []string{"journal", "key", "candidate", "renewed-subject", "changed-owner"} {
		t.Run(mode, func(t *testing.T) {
			bad := proof
			part := *proof.PartialSession
			bad.PartialSession = &part
			current := source
			switch mode {
			case "journal":
				bad.ParentJournal = strings.Repeat("b", 64)
			case "key":
				part.KeySHA = "missing"
			case "candidate":
				part.Candidate = &deploymentplan.Plan{Inputs: candidate.Inputs}
			case "renewed-subject":
				part.RenewedIntent.Subjects = []string{"foreign"}
			case "changed-owner":
				current.OwnerToken = "foreign"
			}
			if partialSessionProofMatches(bad, current) {
				t.Fatal("changed proof accepted", mode)
			}
		})
	}
	execution := &environmentCreateExecution{Journal: source}
	runtime := &createServiceSessionRecovery{Intent: intent, RenewedIntent: renewed, OriginalKeySHA: digest, OriginalCSRSHA: digest, RenewalCandidate: candidate, ManagedTransportRecovery: &createManagedTransportRecovery{PartialSessionRenewal: true, SessionRenewal: &recovery}}
	frozen, err := makePartialSessionProof(proof, execution, runtime)
	if err != nil {
		t.Fatal(err)
	}
	execution.Journal.Entries[0].Key = "mutated"
	candidate.Resources[0].ID = "mutated"
	originalState.Fingerprint = "mutated"
	if frozen.SourceJournal.Entries[0].Key != "kubernetes/service-session" || frozen.Candidate.Resources[0].ID != "Job/ns/renew" || frozen.Recovery.OriginalState.Fingerprint != digest {
		t.Fatal("qualification aliases mutable execution state")
	}
}

func TestPartialSessionAdmissionSurvivesRetryWithoutResetting(t *testing.T) {
	store, execution, _, _ := freshStageFixture(t)
	now := time.Now().UTC()
	digest := strings.Repeat("a", 64)
	if err := execution.BeginEntry("kubernetes/service-session", "service-session", json.RawMessage(`{"source":"original"}`), now); err != nil {
		t.Fatal(err)
	}
	if err := execution.RecordReceipt("kubernetes/service-session", json.RawMessage(`{"plan_sha256":"`+digest+`"}`)); err != nil {
		t.Fatal(err)
	}
	if err := execution.CompleteEntry("kubernetes/service-session", now); err != nil {
		t.Fatal(err)
	}
	source := execution.Journal
	intent := createServiceSessionIntent{SessionID: "33333333-3333-4333-8333-333333333333", IssuerID: "44444444-4444-4444-8444-444444444444", RootSHA: digest, Subjects: []string{"service:certissuer"}, Namespaces: []string{source.Stack + "-video-cloud"}}
	renewed := intent
	renewed.SessionID = sessionRenewalUUID(source.PlanSHA256, digest, intent.SessionID)
	env := map[string]string{"PKI_BOOTSTRAP_SESSION_ID": renewed.SessionID, "PKI_BOOTSTRAP_ISSUER_ID": renewed.IssuerID, "PKI_BOOTSTRAP_ROOT_SHA256": renewed.RootSHA, "PKI_BOOTSTRAP_SUBJECTS": strings.Join(renewed.Subjects, ","), "PKI_BOOTSTRAP_KEY_PATH": "/run/pki-bootstrap/bootstrap.key", "PKI_BOOTSTRAP_CSR_PATH": "/run/pki-bootstrap/bootstrap.csr", "PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/renew-" + digest[:12] + "/bootstrap.crt"}
	job := deploymentplan.Object{"kind": "Job", "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"env": environmentCreateEnv(env)}}}}}}
	candidate := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: source.Environment, Stack: source.Stack, ReleaseSHA256: digest}, Resources: []deploymentplan.Resource{{ID: "Job/ns/renew", Action: "create", Desired: job}}}
	state := &managedSessionState{Session: intent.SessionID, Issuer: intent.IssuerID, Root: intent.RootSHA, Fingerprint: digest, Status: "active", Expires: now.Add(-time.Minute), Subjects: intent.Subjects}
	recovery := createManagedSessionRecovery{PartialInventory: true, OriginalState: state, OriginalSession: intent.SessionID, OriginalCertificatePath: "/run/pki-bootstrap/bootstrap.crt", OriginalCertificateSHA256: digest, SessionSHA256: digest, ClientsSHA256: digest, SourceStage: "kubernetes/service-session", SourcePlanSHA256: digest, SourceJournalSHA256: digest, BindingSHA256: digest, CandidateSHA256: candidate.SHA256()}
	proof := issuerEnrollmentContinuationProof{Parent: source.PlanSHA256, ParentJournal: deploymentplan.Digest(source), ParentOwner: deploymentplan.Digest(execution.owner), Release: digest, PartialSession: &partialSessionContinuationProof{SourceJournal: source, Recovery: recovery, Intent: intent, RenewedIntent: renewed, KeySHA: digest, CSRSHA: digest, Candidate: candidate}}
	review := deploymentplan.Digest(proof)
	proof.QualifiedAt, proof.ValidUntil = now, now.Add(time.Hour)
	if err := store.write(filepath.Join("deployment/create/issuer-enrollment-continuation", review, "qualification.json"), mustProviderRecoveryJSON(proof), false); err != nil {
		t.Fatal(err)
	}
	if err := beginPartialSessionAdmission(store, execution, proof, review, now); err != nil {
		t.Fatal(err)
	}
	runtime := environmentCreateBootstrapRuntime{Execution: execution, ServiceSessionRecovery: &createServiceSessionRecovery{Review: review, RenewedIntent: renewed, ManagedTransportRecovery: &createManagedTransportRecovery{PartialSessionRenewal: true, SessionRenewal: &recovery}}}
	cfg := deploymentConfig{Environment: "dev"}
	if !partialSessionAllowsPendingOpenBao(cfg, runtime, "policy", "rtk-dev-service-bootstrap-"+renewed.SessionID) || !partialSessionAllowsPendingOpenBao(cfg, runtime, "policy", "create-"+renewed.SessionID+"-cleanup-controller") || !partialSessionAllowsPendingOpenBao(cfg, runtime, "role", "pki-service-bootstrap-dev") {
		t.Fatal("admitted renewal cannot reconcile its own pending OpenBao effects")
	}
	if partialSessionAllowsPendingOpenBao(cfg, runtime, "policy", "foreign-policy") || partialSessionAllowsPendingOpenBao(cfg, runtime, "role", "foreign-role") {
		t.Fatal("admitted renewal could reconcile an unrelated OpenBao effect")
	}
	first := *createOpenBaoACKCompletionEntry(execution.Journal, "partial-session-renewal-"+review)
	if err := beginPartialSessionAdmission(store, execution, proof, review, now.Add(2*time.Hour)); err != nil {
		t.Fatal("admitted operation could not resume", err)
	}
	second := createOpenBaoACKCompletionEntry(execution.Journal, first.Key)
	if second == nil || deploymentplan.Digest(first) != deploymentplan.Digest(*second) {
		t.Fatal("retry changed original admission")
	}
	if err := beginPartialSessionAdmission(store, execution, proof, strings.Repeat("b", 64), now); err == nil {
		t.Fatal("different qualification was accepted")
	}
}
