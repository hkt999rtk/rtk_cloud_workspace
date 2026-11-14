package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestPartialTrustForwardPreservesAdmittedSessionAndFailedJob(t *testing.T) {
	now := time.Now().UTC()
	f := managedLateSeedForwardFixture(t)
	digest := strings.Repeat("a", 64)
	parent := f.OriginalSource.PlanSHA256
	intent := createServiceSessionIntent{SessionID: "33333333-3333-4333-8333-333333333333", IssuerID: "44444444-4444-4444-8444-444444444444", RootSHA: digest, Subjects: []string{"service:certissuer"}, Namespaces: []string{f.FailedPlan.Stack + "-video-cloud"}}
	renewed := intent
	renewed.SessionID = sessionRenewalUUID(parent, digest, intent.SessionID)
	container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(f.FailedPlan.Resources[0].Desired)["containers"])[0])
	container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": renewed.SessionID, "PKI_BOOTSTRAP_ISSUER_ID": intent.IssuerID, "PKI_BOOTSTRAP_ROOT_SHA256": intent.RootSHA, "PKI_BOOTSTRAP_SUBJECTS": strings.Join(intent.Subjects, ","), "PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/renew-" + digest[:12] + "/bootstrap.crt"}))
	f.FailedPlanSHA = f.FailedPlan.SHA256()
	f.FailedJournal.PlanSHA256 = f.FailedPlanSHA
	f.FailedJournalSHA = deploymentplan.Digest(f.FailedJournal)
	source := environmentCreateJournal{Schema: "v1", Environment: "dev", Stack: f.FailedPlan.Stack, PlanSHA256: parent, OwnerToken: "owner", AdmittedAt: now, Entries: []environmentCreateJournalEntry{{Key: "kubernetes/service-session", Phase: "service-session", State: "completed", IntentSHA256: digest, StartedAt: now, CompletedAt: now, Receipt: json.RawMessage(`{"plan_sha256":"` + digest + `"}`)}}}
	state := &managedSessionState{Session: intent.SessionID, Issuer: intent.IssuerID, Root: intent.RootSHA, Fingerprint: digest, Status: "active", Expires: now.Add(-time.Minute), Subjects: intent.Subjects}
	recovery := createManagedSessionRecovery{PartialInventory: true, OriginalState: state, OriginalSession: intent.SessionID, OriginalCertificatePath: "/run/pki-bootstrap/bootstrap.crt", OriginalCertificateSHA256: digest, SessionSHA256: digest, ClientsSHA256: digest, SourceStage: "kubernetes/service-session", SourcePlanSHA256: digest, SourceJournalSHA256: digest, BindingSHA256: digest, CandidateSHA256: f.FailedPlanSHA}
	original := issuerEnrollmentContinuationProof{Parent: parent, ParentJournal: deploymentplan.Digest(source), ParentOwner: digest, Release: f.FailedPlan.ReleaseSHA256, PartialSession: &partialSessionContinuationProof{SourceJournal: source, Recovery: recovery, Intent: intent, RenewedIntent: renewed, KeySHA: digest, CSRSHA: digest, Candidate: f.FailedPlan}}
	oldReview := deploymentplan.Digest(original)
	current := source
	current.Entries = append(slices.Clone(source.Entries), environmentCreateJournalEntry{Key: "kubernetes/service-session-original-key-renewal-" + oldReview, Phase: "seed", State: "pending", IntentSHA256: digest, StartedAt: now})
	f.OriginalSource, f.OriginalReview, f.OriginalArtifactSHA = source, oldReview, oldReview
	f.SourceSHA, f.SourceOwnerSHA = deploymentplan.Digest(current), original.ParentOwner
	f.NativeEvidence = map[string]string{"managed_trust": digest, "terminal_pod": f.FailedPodUID, "no_mint": digest, "original_state": digest, "clients": digest}
	f.Candidate = managedLateSeedForwardFixtureCandidate(t, f)
	forward := issuerEnrollmentContinuationProof{Parent: parent, ParentJournal: deploymentplan.Digest(current), ParentOwner: original.ParentOwner, PartialTrustForward: &partialSessionTrustForwardProof{SourceJournal: current, OriginalReview: oldReview, Forward: f}}
	if !partialSessionTrustForwardProofMatches(forward, original, oldReview, current) {
		t.Fatal("original trust-only forward rejected")
	}
	bad := forward
	copy := *forward.PartialTrustForward
	bad.PartialTrustForward = &copy
	copy.OriginalReview = strings.Repeat("b", 64)
	if partialSessionTrustForwardProofMatches(bad, original, oldReview, current) {
		t.Fatal("foreign original admission accepted")
	}
	alias := partialSessionTrustForwardAlias(f, digest)
	if alias["original_failed_plan_sha256"] != f.FailedPlanSHA || alias["actual_qualified_forward_plan_sha256"] != f.Candidate.SHA256() {
		t.Fatal("forward receipt does not bind both Job generations")
	}
}
