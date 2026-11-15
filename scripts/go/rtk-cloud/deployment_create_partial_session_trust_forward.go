package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type partialSessionTrustForwardProof struct {
	SourceJournal  environmentCreateJournal      `json:"source_journal"`
	OriginalReview string                        `json:"original_review"`
	Forward        *createManagedLateSeedForward `json:"forward"`
}

func partialSessionTrustForwardAlias(f *createManagedLateSeedForward, review string) deploymentplan.Object {
	if f == nil || f.Candidate == nil {
		return nil
	}
	return deploymentplan.Object{"original_failed_plan_sha256": f.FailedPlanSHA, "original_failed_journal_sha256": f.FailedJournalSHA, "actual_qualified_forward_stage": "kubernetes/partial-session-trust-forward-" + review, "actual_qualified_forward_plan_sha256": f.Candidate.SHA256(), "original_qualification_sha256": f.OriginalReview}
}

func partialSessionTrustForwardIntent(parent, review string, p *partialSessionTrustForwardProof) deploymentplan.Object {
	if p == nil || p.Forward == nil || p.Forward.Candidate == nil {
		return nil
	}
	return deploymentplan.Object{"parent": parent, "qualification_sha256": review, "original_review": p.OriginalReview, "failed_plan_sha256": p.Forward.FailedPlanSHA, "candidate_sha256": p.Forward.Candidate.SHA256()}
}

func loadPartialSessionTrustForwardAdmission(store secretStore, e *environmentCreateExecution, original issuerEnrollmentContinuationProof, originalReview string) (issuerEnrollmentContinuationProof, string, bool, error) {
	var q issuerEnrollmentContinuationProof
	if e == nil || e.Store.Root != store.Root {
		return q, "", false, createProviderRecoveryBlock("partial trust forward original execution is unavailable")
	}
	const prefix = "partial-session-trust-forward-"
	var entry *environmentCreateJournalEntry
	for i := range e.Journal.Entries {
		if strings.HasPrefix(e.Journal.Entries[i].Key, prefix) {
			if entry != nil {
				return q, "", false, createProviderRecoveryBlock("partial trust forward admission is ambiguous")
			}
			entry = &e.Journal.Entries[i]
		}
	}
	if entry == nil {
		return q, "", false, nil
	}
	review := strings.TrimPrefix(entry.Key, prefix)
	path, err := store.safePath(filepath.Join("deployment/create/issuer-enrollment-continuation", review, "qualification.json"))
	if !environmentPreflightDigest.MatchString(review) || err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil || readEnvironmentPreflightJSON(path, &q) != nil || q.ParentOwner != deploymentplan.Digest(e.owner) || q.QualifiedAt.IsZero() || !q.ValidUntil.Equal(q.QualifiedAt.Add(time.Hour)) || entry.Phase != "partial-session-trust-forward" || entry.StartedAt.Before(q.QualifiedAt) || !entry.StartedAt.Before(q.ValidUntil) {
		return issuerEnrollmentContinuationProof{}, "", false, createProviderRecoveryBlock("partial trust forward original qualification is unavailable")
	}
	q.QualifiedAt, q.ValidUntil = time.Time{}, time.Time{}
	if deploymentplan.Digest(q) != review || !partialSessionTrustForwardProofMatches(q, original, originalReview, e.Journal) || entry.IntentSHA256 != activeTransportJournalDigest(partialSessionTrustForwardIntent(q.Parent, review, q.PartialTrustForward)) || !partialSessionEntryAdvances(*entry, *entry) {
		return issuerEnrollmentContinuationProof{}, "", false, createProviderRecoveryBlock("partial trust forward qualification or admission changed")
	}
	return q, review, true, nil
}

func beginPartialSessionTrustForward(store secretStore, e *environmentCreateExecution, q issuerEnrollmentContinuationProof, review string, original issuerEnrollmentContinuationProof, originalReview string, now time.Time) error {
	if e == nil || e.Store.Root != store.Root {
		return createProviderRecoveryBlock("partial trust forward original execution is unavailable")
	}
	if _, priorReview, exists, err := loadPartialSessionTrustForwardAdmission(store, e, original, originalReview); err != nil {
		return err
	} else if exists {
		if priorReview != review {
			return createProviderRecoveryBlock("partial trust forward has another original admission")
		}
		return e.Check(context.Background())
	}
	if q.ParentOwner != deploymentplan.Digest(e.owner) || q.QualifiedAt.After(now) || !q.ValidUntil.Equal(q.QualifiedAt.Add(time.Hour)) || !now.Before(q.ValidUntil) || deploymentplan.Digest(e.Journal) != q.ParentJournal {
		return createProviderRecoveryBlock("fresh partial trust forward qualification is required")
	}
	q.QualifiedAt, q.ValidUntil = time.Time{}, time.Time{}
	if deploymentplan.Digest(q) != review || !partialSessionTrustForwardProofMatches(q, original, originalReview, e.Journal) {
		return createProviderRecoveryBlock("partial trust forward qualification changed")
	}
	return e.BeginEntry("partial-session-trust-forward-"+review, "partial-session-trust-forward", mustProviderRecoveryJSON(partialSessionTrustForwardIntent(q.Parent, review, q.PartialTrustForward)), now)
}

func partialSessionTrustForwardProofMatches(q, original issuerEnrollmentContinuationProof, originalReview string, current environmentCreateJournal) bool {
	p := q.PartialTrustForward
	if p == nil || p.Forward == nil || original.PartialSession == nil || !environmentPreflightDigest.MatchString(originalReview) || p.OriginalReview != originalReview || deploymentplan.Digest(original) != originalReview || q.Parent != original.Parent || q.Parent != current.PlanSHA256 || q.ParentOwner != original.ParentOwner || deploymentplan.Digest(p.SourceJournal) != q.ParentJournal || !partialSessionJournalExtends(p.SourceJournal, current) || !partialSessionProofMatches(original, p.SourceJournal) {
		return false
	}
	f := p.Forward
	if f.SourceSHA != q.ParentJournal || f.SourceOwnerSHA != q.ParentOwner || f.OriginalReview != originalReview || f.OriginalArtifactSHA != originalReview || f.FailedPlanSHA != original.PartialSession.Candidate.SHA256() || deploymentplan.Digest(f.OriginalSource) != deploymentplan.Digest(original.PartialSession.SourceJournal) || validateManagedLateSeedForwardFailedChild(f) != nil || validateManagedLateSeedForwardCandidate(f, f.Candidate) != nil || !environmentBootstrapOwnerUUID.MatchString(f.FailedPodUID) || !environmentPreflightDigest.MatchString(f.NativeEvidence["managed_trust"]) || !environmentPreflightDigest.MatchString(f.NativeEvidence["no_mint"]) || !environmentPreflightDigest.MatchString(f.NativeEvidence["original_state"]) || f.NativeEvidence["clients"] != original.PartialSession.Recovery.ClientsSHA256 || f.NativeEvidence["terminal_pod"] != f.FailedPodUID {
		return false
	}
	return true
}

// Qualify a new Job for the same original session only when the original
// admitted Job definitively failed before signing and the original key, CSR,
// and managed OpenBao trust owner remain unchanged.
func preparePartialSessionTrustForward(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, q issuerEnrollmentContinuationProof, review string, r *createServiceSessionRecovery) (*createManagedLateSeedForward, error) {
	bad := func(stage string) (*createManagedLateSeedForward, error) {
		return nil, createProviderRecoveryBlock("partial session trust forward requires exact terminal no-mint evidence: " + stage)
	}
	if e == nil || r == nil || r.OpenBaoRoot == nil || q.PartialSession == nil || r.Review != review || deploymentplan.Digest(q) != review || !partialSessionProofMatches(q, e.Journal) {
		return bad("original qualification")
	}
	p := q.PartialSession
	stage := "service-session-original-key-renewal-" + review
	journal, exists, err := readEnvironmentCreateStageProgress(store, e, stage, p.Candidate)
	if err != nil || !exists || len(p.Candidate.Resources) != 1 {
		return bad("original child journal")
	}
	resource := p.Candidate.Resources[0]
	uid := journal.Owners[resource.ID]
	f := &createManagedLateSeedForward{Schema: managedLateSeedForwardSchema, OriginalReview: review, SourceSHA: deploymentplan.Digest(e.Journal), SourceOwnerSHA: deploymentplan.Digest(e.owner), OriginalArtifactSHA: deploymentplan.Digest(q), OriginalSource: p.SourceJournal, FailedPlan: p.Candidate, FailedJournal: journal, FailedPlanSHA: p.Candidate.SHA256(), FailedJournalSHA: deploymentplan.Digest(journal), FailedJobUID: uid}
	if validateManagedLateSeedForwardFailedChild(f) != nil {
		return bad("failed Job record")
	}
	trustDigest, err := verifyPartialSessionOpenBaoTrust(ctx, store, e, b, *r.OpenBaoRoot)
	if err != nil {
		return nil, err
	}
	owners, err := completedInitialTransportSuccessorResources(ctx, store, e, b)
	if err != nil {
		return nil, err
	}
	trustID := "ConfigMap/" + e.Journal.Stack + "-video-cloud/pki-create-openbao-tls-consumer-trust"
	for _, owner := range owners {
		if owner.ID == trustID {
			f.TrustOwner = deploymentplan.Resource{ID: owner.ID, UID: owner.UID, Desired: owner.Desired}
		}
	}
	definitions, err := managedLateSeedForwardDefinitions(f)
	if err != nil {
		return nil, err
	}
	job, err := b.Read(ctx, resource.ID)
	if err != nil || job == nil {
		return bad("failed Job owner")
	}
	namespace := e.Journal.Stack + "-video-cloud"
	name := deploymentplan.String(deploymentplan.Map(resource.Desired["metadata"])["name"])
	raw, err := b.command(ctx, nil, "--namespace", namespace, "get", "pods", "--selector", "job-name="+name, "-o", "json")
	var pods struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if err != nil || json.Unmarshal(raw, &pods) != nil || len(pods.Items) != 1 {
		return bad("original terminal Pod")
	}
	f.FailedPodUID = deploymentplan.String(deploymentplan.Map(pods.Items[0]["metadata"])["uid"])
	container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(resource.Desired)["containers"])[0])
	logs, err := b.command(ctx, nil, "--namespace", namespace, "logs", "pod/"+deploymentplan.String(deploymentplan.Map(pods.Items[0]["metadata"])["name"]), "--container", deploymentplan.String(container["name"]))
	if err != nil || validateManagedLateSeedForwardTerminal(f, job, pods.Items[0], logs) != nil {
		return bad("terminal TLS outcome")
	}
	lease, err := b.Read(ctx, "Lease/"+namespace+"/rtk-deployment-execution")
	if err != nil || lease != nil {
		return bad("original Job lease")
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: b}
	if verifyManagedSeedOriginalCustody(ctx, runtime, r, &p.Recovery) != nil {
		return bad("original custody")
	}
	path := r.RenewedCertPath
	if path != "/run/pki-bootstrap/renew-"+p.Recovery.BindingSHA256[:12]+"/bootstrap.crt" {
		return bad("qualified output path")
	}
	dir := filepath.Dir(path)
	guard := "test -d /run/pki-bootstrap && test ! -L /run/pki-bootstrap && test -d " + dir + " && test ! -L " + dir + " && test ! -e " + path + " && test ! -L " + path
	if _, err := b.command(ctx, nil, "--namespace", namespace, "exec", "deployment/"+r.Ceremony.CustodyReader, "--container", "custody", "--", "/bin/sh", "-ec", guard); err != nil {
		return bad("successor certificate absence")
	}
	query, err := managedLateSeedForwardNoMintQuery(cfg.Environment, p.RenewedIntent.SessionID, mqttFoundationEnv(container, "PKI_BOOTSTRAP_DEPLOYMENT_ID"))
	if err != nil {
		return nil, err
	}
	counts, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	if err != nil || !managedLateSeedForwardNoMintCountsValid(counts) {
		return bad("native session and issuance absence")
	}
	old, rawState, err := readManagedSessionState(ctx, cfg, runtime, p.Intent.SessionID)
	if err != nil || !managedSessionMayRenew(old, r.Ceremony, p.Intent.Subjects, time.Now().UTC()) || p.Recovery.OriginalState == nil || !partialSessionOriginalStatePreserved(*p.Recovery.OriginalState, old, time.Now().UTC()) {
		return bad("original session state")
	}
	rows, err := readManagedSessionClientRows(ctx, cfg, runtime)
	if err != nil || !partialSessionClientsPreserved(rows, r.Ceremony, p.Intent.Subjects, old, &p.Recovery, time.Now().UTC()) || deploymentplan.Digest(rows) != p.Recovery.ClientsSHA256 {
		return bad("original client inventory")
	}
	inputs := p.Candidate.Inputs
	inputs.BindingsSHA256 = environmentCreateStageBindingsSHA("managed-late-seed-trust-forward", definitions)
	live, err := b.Inventory(ctx, e.Journal.Stack)
	if err != nil {
		return nil, err
	}
	f.Candidate, err = deploymentplan.Compile(inputs, definitions, live)
	if err != nil || validateManagedLateSeedForwardCandidate(f, f.Candidate) != nil {
		return bad("trust-only forward candidate")
	}
	f.NativeEvidence = map[string]string{"managed_trust": trustDigest, "terminal_pod": f.FailedPodUID, "no_mint": environmentPreflightBytesSHA256(counts), "original_state": environmentPreflightBytesSHA256(rawState), "clients": deploymentplan.Digest(rows)}
	if strings.TrimSpace(string(logs)) == "" {
		return bad("terminal diagnostic")
	}
	return f, e.Check(ctx)
}
