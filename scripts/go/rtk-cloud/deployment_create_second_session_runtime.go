package main

import (
	"context"
	"slices"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Restore the already-admitted second renewal through the existing native
// Service session executor. This constructs no identity, writes no resources,
// and never replaces the first renewal's plan or journal.
func restoreSecondSessionRuntime(ctx context.Context, store secretStore, e *environmentCreateExecution, q secondSessionAdmission, review string, service environmentCreateServiceCeremony, openBaoRoot environmentCreateRegisteredIssuer, selected environmentCreatePlan) (*createServiceSessionRecovery, error) {
	bad := func() (*createServiceSessionRecovery, error) {
		return nil, createProviderRecoveryBlock("second Service session requires its exact original same-key admission")
	}
	if e == nil || !validSecondSessionAdmission(store, e, q) || deploymentplan.Digest(q) != review || e.Check(ctx) != nil {
		return bad()
	}
	bound, err := qualifiedSecondSessionPlan(store, e, &selected, q.Renewal.Candidate)
	if err != nil {
		return nil, err
	}
	first, firstReview, exists, err := loadPartialSessionAdmission(store, e)
	if err != nil || !exists || first.PartialSession == nil || firstReview != q.Renewal.OriginalReview {
		return bad()
	}
	p := first.PartialSession
	r := q.Renewal
	head, headReview, headErr := sessionLineageHead(store, e)
	if headErr != nil || headReview != review && headReview != r.PreviousReview {
		return bad()
	}
	if headReview == r.PreviousReview {
		intent, binding := p.RenewedIntent, p.Recovery.BindingSHA256
		if head != nil {
			intent, binding = head.Renewal.RenewedIntent, head.Renewal.BindingSHA
		}
		if deploymentplan.Digest(intent) != deploymentplan.Digest(r.Intent) || binding != r.PreviousBindingSHA {
			return bad()
		}
	}
	if r.KeySHA != p.KeySHA || r.CSRSHA != p.CSRSHA || r.SourceStage != p.Recovery.SourceStage || r.SourcePlanSHA != p.Recovery.SourcePlanSHA256 || r.SourceJournalSHA != p.Recovery.SourceJournalSHA256 || service.Root.RootFingerprint != r.Intent.RootSHA || service.Intermediate.ID != r.Intent.IssuerID || !environmentPreflightDigest.MatchString(r.PreviousBindingSHA) || !environmentBootstrapOwnerUUID.MatchString(openBaoRoot.ID) || !environmentPreflightDigest.MatchString(openBaoRoot.RootFingerprint) {
		return bad()
	}
	state := r.PreviousState
	proof := &createManagedSessionRecovery{PartialInventory: true, OriginalState: &state, OriginalClientDigests: slices.Clone(r.ClientDigests), OriginalSession: r.Intent.SessionID, OriginalCertificatePath: "/run/pki-bootstrap/renew-" + r.PreviousBindingSHA[:12] + "/bootstrap.crt", OriginalCertificateSHA256: r.PreviousCertSHA, SessionSHA256: deploymentplan.Digest(state), ClientsSHA256: r.ClientsSHA, SourceStage: r.SourceStage, SourcePlanSHA256: r.SourcePlanSHA, SourceJournalSHA256: r.SourceJournalSHA, BindingSHA256: r.BindingSHA, CandidateSHA256: r.Candidate.SHA256()}
	service.BootstrapSessionID = r.Intent.SessionID
	result := &createServiceSessionRecovery{qualifiedPlan: &bound, Ceremony: service, Intent: r.Intent, RenewedIntent: r.RenewedIntent, OriginalKeySHA: r.KeySHA, OriginalCSRSHA: r.CSRSHA, Review: review, RenewalBindingSHA: r.BindingSHA, SourceRenewalStage: r.SourceStage, RenewedCertPath: "/run/pki-bootstrap/renew-" + r.BindingSHA[:12] + "/bootstrap.crt", Renewal: true, SecondSession: true, PostServiceRenewal: true, RenewalCandidate: r.Candidate, OpenBaoRoot: &openBaoRoot, ManagedTransportRecovery: &createManagedTransportRecovery{PartialSessionRenewal: true, SessionRenewal: proof}}
	return result, nil
}

func qualifiedSecondSessionPlan(store secretStore, e *environmentCreateExecution, selected *environmentCreatePlan, candidate *deploymentplan.Plan) (environmentCreatePlan, error) {
	if e == nil || selected == nil || candidate == nil || selected.Environment != store.Environment || selected.Stack != e.Journal.Stack || candidate.Environment != selected.Environment || candidate.Stack != selected.Stack || selected.ConfigurationSHA256 != candidate.ConfigurationSHA256 || deploymentplan.Digest(selected.Release) != candidate.ReleaseSHA256 {
		return environmentCreatePlan{}, createProviderRecoveryBlock("second session requires the exact preflight-selected release and configuration")
	}
	if _, _, err := selectedPartialSeedImage(*selected); err != nil {
		return environmentCreatePlan{}, err
	}
	return *selected, nil
}
