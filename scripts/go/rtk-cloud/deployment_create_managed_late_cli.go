package main

// Private CLI seams; no deployment or native allocation has executed these.
import (
	"context"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateSessionACKCheckpointReview(j environmentCreateJournal) string {
	if len(j.Entries) < 8 || !j.CompletedAt.IsZero() {
		return ""
	}
	last := j.Entries[len(j.Entries)-1]
	const prefix = "openbao-tls-root-crl-renewal-"
	const suffix = "/native-runtime-pki-controller"
	if last.State != "completed" || last.CompletedAt.IsZero() || !strings.HasPrefix(last.Key, prefix) || !strings.HasSuffix(last.Key, suffix) {
		return ""
	}
	review := strings.TrimSuffix(strings.TrimPrefix(last.Key, prefix), suffix)
	if !environmentPreflightDigest.MatchString(review) {
		return ""
	}
	for index, expectedSuffix := range []string{"", "/native-import", "/native-runtime-certissuer", suffix} {
		entry := j.Entries[len(j.Entries)-4+index]
		if entry.Key != prefix+review+expectedSuffix || entry.State != "completed" || entry.CompletedAt.IsZero() {
			return ""
		}
	}
	// Selection supplies no authority. The immutable typed constructor must
	// authenticate every source, candidate, native receipt and original owner.
	return review
}

func loadManagedLatePreparedSession(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan) (*createServiceSessionRecovery, error) {
	r, late, err := loadManagedLateSessionSource(cfg, store, e, p, managedLateSessionACKReady)
	if err != nil {
		return nil, err
	}
	r.ManagedLateSession = late
	if err := prepareManagedLateSessionSeed(ctx, cfg, store, e, b, p, r, late); err != nil {
		return nil, err
	}
	r.Candidate = late.OwnerCandidate
	return r, nil
}

func admitManagedLateSessionCandidates(ctx context.Context, b environmentKubernetesBackend, late *createManagedLateSessionRecovery) error {
	if late == nil || validateManagedLateSessionOwnerCandidate(late, late.OwnerCandidate) != nil || validateManagedLateLogCandidates(late) != nil {
		return environmentCreateBootstrapBlocker("late exact qualified candidates required")
	}
	// Restore and retirement have separately authenticated future baselines.
	// Only candidates against the actual current native source admit here.
	for _, candidate := range []*deploymentplan.Plan{late.SeedCandidate, late.CustodyCandidate, late.OwnerCandidate, late.LogStopCandidate, late.LogHelperCandidate} {
		if candidate == nil {
			return environmentCreateBootstrapBlocker("late current candidate missing")
		}
		if err := candidate.Admit(ctx, b); err != nil {
			return err
		}
	}
	return nil
}

// Post-mint recovery admits only remaining Log candidates. Its separately
// qualified effective owner candidate is admitted by the calling profile.
func admitManagedLatePostMintCandidates(ctx context.Context, b environmentKubernetesBackend, late *createManagedLateSessionRecovery) error {
	if late == nil || validateManagedLateLogCandidates(late) != nil {
		return environmentCreateBootstrapBlocker("late post-mint exact Log candidates required")
	}
	for _, candidate := range []*deploymentplan.Plan{late.LogStopCandidate, late.LogHelperCandidate} {
		if candidate == nil {
			return environmentCreateBootstrapBlocker("late post-mint current Log candidate missing")
		}
		if err := candidate.Admit(ctx, b); err != nil {
			return err
		}
	}
	return nil
}
