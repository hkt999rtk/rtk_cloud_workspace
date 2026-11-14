package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const freshProductSessionSuccessorSchema = "rtk-cloud.fresh-product-session-successor/v1"

type freshProductSessionSuccessor struct {
	Schema                                                                                                string `json:"schema"`
	ParentSHA256, OwnerSHA256, Generation, ProgramSHA256, ScopeSHA256, InstalledSHA256, OutputsSHA256     string
	SourceReview, CurrentReview, OriginalSessionID, SessionID, RootSHA256, IssuerID, KeySHA256, CSRSHA256 string
	CustodyPlanSHA256, CredentialPlanSHA256, CleanupBindingSHA256                                         string
}

func freshProductSuccessorPath(parent, review string) string {
	return filepath.Join("deployment/create", parent, "bootstrap/product-identities/session-successor", review+".json")
}
func freshProductSuccessorControlStage(review string) string {
	return "fresh-product-session-successor-" + review[:16] + "-cleanup"
}
func freshProductSuccessorBlock() error { return freshProductResumeBlock("fresh-session-successor") }

// Original programme/output files remain literal source authority. This builds
// a private view only after the separately admitted native head has completed.
func expectedFreshProductSessionSuccessor(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, head secondSessionAdmission, review string) (freshProductSessionSuccessor, *environmentProductIdentitySession, error) {
	var zero freshProductSessionSuccessor
	bad := func() (freshProductSessionSuccessor, *environmentProductIdentitySession, error) {
		return zero, nil, freshProductSuccessorBlock()
	}
	if e == nil || e.Store != store || l == nil || l.FreshResume == nil || !environmentPreflightDigest.MatchString(review) || deploymentplan.Digest(head) != review {
		return bad()
	}
	programme, err := readFreshProductProgram(store, e)
	if err != nil {
		return zero, nil, err
	}
	if productIdentityScope(&programme.Lifecycle) != productIdentityScope(l) {
		return bad()
	}
	sourceReview := l.FreshResume.SessionReviewSHA256
	source, err := freshProductResumeSourceAdmission(store, e, sourceReview)
	if err != nil {
		return zero, nil, err
	}
	if sourceReview == review {
		return bad()
	}
	current, currentReview, err := sessionLineageHead(store, e)
	if err != nil || current == nil || currentReview != review || deploymentplan.Digest(*current) != review {
		return bad()
	}
	done, err := completedServiceSessionBatch(store, e, head, review)
	if err != nil || !done {
		return bad()
	}
	programmeEntry := createOpenBaoACKCompletionEntry(e.Journal, "product-identities/fresh")
	savedEntry := createOpenBaoACKCompletionEntry(head.SourceJournal, "product-identities/fresh")
	if programmeEntry == nil || savedEntry == nil || deploymentplan.Digest(*programmeEntry) != deploymentplan.Digest(*savedEntry) {
		return bad()
	}
	old, err := readProductInstalled(store, l)
	if err != nil {
		return zero, nil, err
	}
	from, to := source.Renewal, head.Renewal
	if from.RenewedIntent.SessionID != old.Ceremony.BootstrapSessionID || from.KeySHA != to.KeySHA || from.CSRSHA != to.CSRSHA || to.RenewedIntent.RootSHA != old.Ceremony.Root.RootFingerprint || to.RenewedIntent.IssuerID != old.Ceremony.Intermediate.ID || !slices.Equal(from.RenewedIntent.Subjects, to.RenewedIntent.Subjects) || !slices.Equal(from.RenewedIntent.Namespaces, to.RenewedIntent.Namespaces) || programme.SelectedPlan == nil || to.Candidate == nil || to.Candidate.ConfigurationSHA256 != programme.SelectedPlan.ConfigurationSHA256 || to.Candidate.ReleaseSHA256 != deploymentplan.Digest(programme.SelectedPlan.Release) {
		return bad()
	}
	var effective environmentProductIdentitySession
	if json.Unmarshal(mustProviderRecoveryJSON(old), &effective) != nil {
		return bad()
	}
	// The admission/ancestry above proves this target; the original birth SID is
	// retained separately and never written back into the original installed file.
	anchor := old.Ceremony
	anchor.BootstrapSessionID = to.RenewedIntent.SessionID
	ceremony, err := freshProductResumeCurrentCeremony(store, e, head, review, anchor)
	if err != nil {
		return zero, nil, err
	}
	effective.Ceremony = ceremony
	if effective.EnrollmentSessionID == "" {
		effective.EnrollmentSessionID = old.Ceremony.BootstrapSessionID
	}
	custody, custodyJournal, err := acceptedCreateStage(store, e, "kubernetes/service-custody-reader-renewal-"+review)
	if err != nil || custody == nil {
		return bad()
	}
	credentials, credentialsJournal, err := acceptedCreateStage(store, e, "kubernetes/service-credential-secrets-renewal-"+review)
	if err != nil || credentials == nil {
		return bad()
	}
	for _, stage := range []struct {
		name    string
		plan    *deploymentplan.Plan
		journal deploymentplan.Journal
	}{{"service-custody-reader-renewal-" + review, custody, custodyJournal}, {"service-credential-secrets-renewal-" + review, credentials, credentialsJournal}} {
		checked, exists, err := readEnvironmentCreateStageProgress(store, e, stage.name, stage.plan)
		if err != nil || !exists || deploymentplan.Digest(checked) != deploymentplan.Digest(stage.journal) {
			return bad()
		}
	}
	programmeRaw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, freshProductProgramPath(e.Journal.PlanSHA256)), true)
	if err != nil {
		return zero, nil, err
	}
	installedRaw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, productIdentitySessionPath(l)), true)
	if err != nil {
		return zero, nil, err
	}
	b := freshProductSessionSuccessor{Schema: freshProductSessionSuccessorSchema, ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(e.owner), Generation: e.Journal.PlanSHA256, ProgramSHA256: environmentPreflightBytesSHA256(programmeRaw), ScopeSHA256: productIdentityScope(l), InstalledSHA256: environmentPreflightBytesSHA256(installedRaw), OutputsSHA256: deploymentplan.Digest(old.Outputs), SourceReview: sourceReview, CurrentReview: review, OriginalSessionID: old.Ceremony.BootstrapSessionID, SessionID: ceremony.BootstrapSessionID, RootSHA256: ceremony.Root.RootFingerprint, IssuerID: ceremony.Intermediate.ID, KeySHA256: to.KeySHA, CSRSHA256: to.CSRSHA, CustodyPlanSHA256: custody.SHA256(), CredentialPlanSHA256: credentials.SHA256(), CleanupBindingSHA256: deploymentplan.Digest(ceremony.CleanupBinding)}
	return b, &effective, e.Check(context.Background())
}

func readFreshProductSessionSuccessor(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle) (*freshProductSessionSuccessor, error) {
	if l == nil || l.FreshResume == nil {
		return nil, nil
	}
	head, review, err := sessionLineageHead(store, e)
	if err != nil {
		return nil, err
	}
	if head == nil {
		return nil, freshProductSuccessorBlock()
	}
	if review == l.FreshResume.SessionReviewSHA256 {
		return nil, nil
	}
	path, err := store.safePath(freshProductSuccessorPath(e.Journal.PlanSHA256, review))
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, freshProductSuccessorBlock()
	} else if err != nil {
		return nil, err
	}
	var saved freshProductSessionSuccessor
	if readEnvironmentPreflightJSON(path, &saved) != nil {
		return nil, freshProductSuccessorBlock()
	}
	expected, _, err := expectedFreshProductSessionSuccessor(store, e, l, *head, review)
	if err != nil {
		return nil, err
	}
	if deploymentplan.Digest(saved) != deploymentplan.Digest(expected) {
		return nil, freshProductSuccessorBlock()
	}
	return &saved, nil
}

func readEffectiveFreshProductInstalled(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle) (*environmentProductIdentitySession, error) {
	original, err := readProductInstalled(store, l)
	if err != nil {
		return nil, err
	}
	binding, err := readFreshProductSessionSuccessor(store, e, l)
	if err != nil {
		return nil, err
	}
	if binding == nil {
		return original, nil
	}
	head, err := loadSecondSessionAdmission(store, e, binding.CurrentReview)
	if err != nil {
		return nil, err
	}
	_, effective, err := expectedFreshProductSessionSuccessor(store, e, l, head, binding.CurrentReview)
	return effective, err
}
