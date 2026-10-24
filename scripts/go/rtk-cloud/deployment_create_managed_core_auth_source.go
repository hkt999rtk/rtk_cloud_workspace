package main

import (
	"context"
	"path/filepath"
	"slices"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Read-only policy authentication can follow the one genuinely qualified
// admission appended to its captured source. It never allocates, closes or
// rewrites that admission, and cannot admit a Kubernetes write suffix.
func managedCoreAuthenticationSource(ctx context.Context, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery) (*environmentCreateExecution, error) {
	if e == nil || r == nil || r.ManagedCorePartial == nil || e.Check(ctx) != nil {
		return nil, managedCoreObservationBlock("native-auth-exclusive-source")
	}
	if managedCoreObservationSource(store, e, r.ManagedCorePartial) == nil {
		return e, nil
	}
	review := r.ManagedCorePartial.ForwardReview
	if !environmentPreflightDigest.MatchString(review) {
		return nil, managedCoreObservationBlock("native-auth-fresh-review")
	}
	base := filepath.Join(store.Root, "deployment/create/openbao-recovery", review)
	var artifact createFoundationRecoveryArtifact
	var captured environmentCreateJournal
	var owner environmentCreateOwner
	var preview deploymentplan.Plan
	if readExecutionJSON(filepath.Join(base, "qualification.json"), &artifact) != nil || readExecutionJSON(filepath.Join(base, "source-journal.json"), &captured) != nil || readExecutionJSON(filepath.Join(base, "source-owner.json"), &owner) != nil || readExecutionJSON(filepath.Join(base, managedCorePartialForwardPlanFilename), &preview) != nil {
		return nil, managedCoreObservationBlock("native-auth-private-admission-proof")
	}
	kubeconfig, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || artifact.Plan.KubeconfigSHA != environmentPreflightBytesSHA256(kubeconfig) {
		return nil, managedCoreObservationBlock("native-auth-original-kubeconfig")
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return nil, managedCoreObservationBlock("native-auth-checker")
	}
	current := e.Journal
	if len(current.Entries) >= len(captured.Entries)+2 && len(current.KubernetesLeases) >= len(captured.KubernetesLeases)+1 {
		frame := *e
		frame.Journal = current
		frame.Journal.Entries = slices.Clone(current.Entries[:len(captured.Entries)+2])
		frame.Journal.KubernetesLeases = slices.Clone(current.KubernetesLeases[:len(captured.KubernetesLeases)+1])
		if managedCorePartialNativeCheckpoint(store, &frame) {
			current.Entries = slices.Clone(current.Entries)
			copy(current.Entries[:len(captured.Entries)], captured.Entries)
			current.KubernetesLeases = slices.Clone(current.KubernetesLeases)
			copy(current.KubernetesLeases[:len(captured.KubernetesLeases)], captured.KubernetesLeases)
		}
	}
	if err := validateManagedCoreAuthenticationAdmission(current, captured, e.owner, owner, r, artifact, &preview, checker, time.Now().UTC()); err != nil {
		return nil, err
	}
	source := *e
	source.Journal = captured
	if err := managedCoreObservationSource(store, &source, r.ManagedCorePartial); err != nil {
		return nil, err
	}
	return &source, nil
}

func validateManagedCoreAuthenticationAdmission(current, captured environmentCreateJournal, currentOwner, capturedOwner environmentCreateOwner, r *createServiceSessionRecovery, artifact createFoundationRecoveryArtifact, preview *deploymentplan.Plan, checker string, now time.Time) error {
	bad := func(stage string) error { return managedCoreObservationBlock("native-auth-admission-" + stage) }
	if r == nil || r.ManagedCorePartial == nil || preview == nil {
		return bad("typed-profile")
	}
	core := r.ManagedCorePartial
	review := core.ForwardReview
	sourceCore := *core
	sourceCore.ForwardReview = ""
	sourceCore.CertIssuerPrepare = nil
	if core.Action != managedCoreRestoreOnly || r.Review != core.OriginalRepairReview || !environmentPreflightDigest.MatchString(review) || deploymentplan.Digest(artifact.Plan) != review || artifact.Schema != "rtk-cloud.create-openbao-recovery-artifact/v1" || artifact.Plan.Schema != managedCorePartialSchema || artifact.Plan.ParentSHA != current.PlanSHA256 || artifact.Plan.ParentJournalSHA != deploymentplan.Digest(captured) || artifact.Plan.ParentJournalSHA != core.SourceSHA || artifact.Plan.ParentOwnerSHA != deploymentplan.Digest(currentOwner) || deploymentplan.Digest(currentOwner) != deploymentplan.Digest(capturedOwner) || capturedOwner.Schema != environmentCreateOwnerSchema || capturedOwner.OwnerToken != current.OwnerToken || capturedOwner.PlanSHA256 != current.PlanSHA256 || artifact.Plan.CheckerSHA != checker || !environmentPreflightDigest.MatchString(checker) || captured.OwnerToken != current.OwnerToken || artifact.QualifiedAt.After(now) || !artifact.ValidUntil.Equal(artifact.QualifiedAt.Add(15*time.Minute)) || artifact.Qualification.PlanSHA256 != review || artifact.Qualification.Decision != "GO" || len(artifact.Qualification.Gates) != 6 {
		return bad("qualification")
	}
	for index, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		gate := artifact.Qualification.Gates[index]
		if gate.ID != id || !gate.Required || gate.Status != "PASS" || gate.EvidenceSHA256 != review {
			return bad("six-complete-gates")
		}
	}
	materials := artifact.Plan.Materials
	if materials["accepted-service-core-complete-issuer-partial-action"] != deploymentplan.Digest(managedCoreRestoreOnly) || materials["accepted-service-core-managed-complete-observation-action"] != deploymentplan.Digest(managedCoreRestoreOnly) || materials["accepted-service-core-complete-issuer-partial-original-review"] != core.OriginalRepairReview || materials["accepted-service-core-complete-issuer-partial-source"] != deploymentplan.Digest(sourceCore) || materials["accepted-service-core-complete-issuer-partial-source-journal"] != deploymentplan.Digest(core.Journal) || materials["accepted-service-core-complete-issuer-partial-source-plan"] != core.Plan.SHA256() || materials["accepted-service-core-complete-issuer-partial-forward-plan"] != preview.SHA256() || validateManagedCorePartialCandidate(core, preview) != nil {
		return bad("exact-restore-scope")
	}
	if core.PriorForward != nil {
		if validateManagedCoreCertIssuerPrepare(core.CertIssuerPrepare) != nil {
			return bad("formal-certissuer-image")
		}
		expected := map[string]string{}
		addManagedCoreCertIssuerPrepareMaterials(expected, core.CertIssuerPrepare)
		for key, value := range expected {
			if materials[key] != value {
				return bad("qualified-certissuer-image")
			}
		}
	} else if core.CertIssuerPrepare != nil {
		return bad("foreign-certissuer-image")
	}
	if core.PriorAdmission != nil {
		if materials["accepted-service-core-complete-issuer-partial-prior-admission"] != deploymentplan.Digest(core.PriorAdmission) || core.PriorForward == nil && (core.PriorAdmission.SourceSHA != core.SourceSHA || core.PriorAdmission.CandidateSHA != preview.SHA256()) {
			return bad("exact-prior-admission")
		}
	} else if materials["accepted-service-core-complete-issuer-partial-prior-admission"] != "" {
		return bad("foreign-prior-admission")
	}
	if len(current.Entries) < len(captured.Entries)+1 || len(current.KubernetesLeases) < len(captured.KubernetesLeases) || len(captured.Entries) == 0 {
		return bad("one-admission-suffix")
	}
	prefix := current
	prefix.Entries = slices.Clone(current.Entries[:len(captured.Entries)])
	prefix.KubernetesLeases = slices.Clone(current.KubernetesLeases[:len(captured.KubernetesLeases)])
	// The source loader owns the exact known prior-admission transition. Only
	// its two-field native receipt can move; no other intent/time/owner/lease can.
	if !managedCorePartialPrefixMatches(captured, prefix, core) && !managedCorePartialAliasPrefixMatches(captured, prefix, core, "kubernetes/managed-complete-issuer-forward-"+review, preview.SHA256()) || prefix.Entries[len(prefix.Entries)-1].CompletedAt.After(now) {
		return bad("unchanged-source-prefix")
	}
	entry := current.Entries[len(captured.Entries)]
	intent := map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(artifact), "original_parent_journal_sha256": artifact.Plan.ParentJournalSHA}
	if entry.Key != "openbao-recovery-admission-"+review || entry.Phase != "recovery" || entry.IntentSHA256 != activeTransportJournalDigest(intent) || entry.StartedAt.IsZero() || entry.StartedAt.Before(artifact.QualifiedAt) || !entry.StartedAt.Before(artifact.ValidUntil) || entry.StartedAt.After(now) {
		return bad("genuine-fresh-entry")
	}
	switch entry.State {
	case "pending":
		if len(current.Entries) != len(captured.Entries)+1 || len(current.KubernetesLeases) != len(captured.KubernetesLeases) || !now.Before(artifact.ValidUntil) || !entry.CompletedAt.IsZero() || len(entry.Receipt) != 0 {
			return bad("pending-entry")
		}
	case "completed":
		audit, err := managedCoreOriginalAuditSHA(captured)
		if err != nil || !managedCoreAdmissionReceipt(entry, review, audit) || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(now) {
			return bad("completed-native-proof")
		}
	default:
		return bad("entry-state")
	}
	return nil
}

func managedCoreOriginalAuditSHA(j environmentCreateJournal) (string, error) {
	found, hash := 0, ""
	for _, entry := range j.Entries {
		if entry.Key == "openbao-audit-enable" {
			var receipt struct {
				Audit  string `json:"audit_sha256"`
				Review string `json:"qualified_recovery_sha256,omitempty"`
			}
			if entry.State != "completed" || entry.CompletedAt.IsZero() || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || !environmentPreflightDigest.MatchString(receipt.Audit) {
				return "", managedCoreObservationBlock("original-native-audit-proof")
			}
			found++
			hash = receipt.Audit
		}
	}
	if found != 1 {
		return "", managedCoreObservationBlock("original-native-audit-count")
	}
	return hash, nil
}

func managedCoreAdmissionReceipt(entry environmentCreateJournalEntry, review, audit string) bool {
	var receipt struct {
		Audit  string `json:"audit_sha256"`
		Review string `json:"qualified_recovery_sha256"`
	}
	return decodeEnvironmentJSON(entry.Receipt, &receipt) == nil && receipt.Audit == audit && receipt.Review == review
}

func managedCoreReadyControllerAuthentication(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery) (string, string, string, error) {
	bad := func() (string, string, string, error) {
		return "", "", "", managedCoreObservationBlock("genuine-ready-controller-policy")
	}
	if r == nil || r.ManagedCorePartial == nil || r.ManagedCorePartial.PriorForward == nil || e == nil || e.Check(ctx) != nil {
		return bad()
	}
	if _, err := managedCoreAuthenticationSource(ctx, store, e, r); err != nil {
		return "", "", "", err
	}
	plan, _, err := managedCorePartialCurrentPlan(r.ManagedCorePartial)
	if err != nil {
		return bad()
	}
	actual, err := b.Read(ctx, plan.Resources[13].ID)
	if err != nil {
		return bad()
	}
	ready, err := managedCoreReadyControllerResource(r.ManagedCorePartial, actual)
	if err != nil {
		return bad()
	}
	resource := plan.Resources[13]
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = b.Wait(bounded, ready)
	cancel()
	if err != nil {
		return bad()
	}
	name, role, policy, err := verifyServiceSessionRecordedAuthenticationNative(ctx, cfg, store, e, b, r)
	if err != nil {
		return "", "", "", err
	}
	current, err := b.Read(ctx, resource.ID)
	if err != nil || current == nil || deploymentplan.Digest(deploymentplan.Projection(current)) != deploymentplan.Digest(deploymentplan.Projection(actual)) {
		return bad()
	}
	return name, role, policy, e.Check(ctx)
}

func managedCoreReadyControllerResource(core *createManagedCorePartialRecovery, actual deploymentplan.Object) (deploymentplan.Resource, error) {
	var empty deploymentplan.Resource
	if core == nil || core.PriorForward == nil {
		return empty, managedCoreObservationBlock("typed-completed-controller")
	}
	plan, journal, err := managedCorePartialCurrentPlan(core)
	if err != nil || len(journal.Completed) != 14 || len(journal.Owners) != 15 || journal.Completed[13] != plan.Resources[13].ID || validateManagedCoreCurrentOwner(core, 13, actual) != nil {
		return empty, managedCoreObservationBlock("completed-current-controller")
	}
	resource := plan.Resources[13]
	resource.Action, resource.UID = "retain", journal.Owners[resource.ID]
	return resource, nil
}
