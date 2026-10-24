package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateRemainingPreparationFixture(t *testing.T, store secretStore, e *environmentCreateExecution, cfg deploymentConfig) (environmentCreateBootstrapRuntime, managedLateRuntimeBudget, createFoundationRecoveryArtifact) {
	t.Helper()
	remaining, artifact, now := managedLateRemainingNegativeFixture(t)
	remaining.ParentSHA, remaining.SourceSHA, remaining.OwnerSHA = e.Journal.PlanSHA256, deploymentplan.Digest(e.Journal), deploymentplan.Digest(e.owner)
	remaining.SourceLeases = len(e.Journal.KubernetesLeases)
	remaining.Ceremony.CleanupBinding.SessionID = remaining.SessionID
	late := &createManagedLateSessionRecovery{Review: remaining.OriginalReview, ParentSHA: remaining.ParentSHA, SourceSHA: strings.Repeat("3", 64), SourceOwnerSHA: remaining.OwnerSHA, NewSessionID: remaining.SessionID}
	remaining.DescriptorSHA = managedLateSessionSourceDigest(late)
	artifact.Plan.ParentSHA, artifact.Plan.ParentJournalSHA, artifact.Plan.ParentOwnerSHA = remaining.ParentSHA, remaining.SourceSHA, remaining.OwnerSHA
	artifact.Plan.Plan.ConfigurationSHA256 = deploymentplan.Digest(cfg)
	managedLateRemainingNegativeBind(t, remaining, &artifact)
	var err error
	remaining.Authority, err = newManagedLateRemainingAuthority(remaining, artifact, now.Add(90*time.Minute), now, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(artifact)))
	if err != nil {
		t.Fatal(err)
	}
	r := &createServiceSessionRecovery{Review: late.Review, Renewal: true, Ceremony: remaining.Ceremony, ManagedLateSession: late, ManagedLateRemaining: remaining}
	overall, cancel := context.WithDeadline(context.Background(), remaining.Authority.Deadline)
	t.Cleanup(cancel)
	budget := managedLateRuntimeBudget{StartedAt: remaining.Authority.StartedAt, Deadline: remaining.Authority.Deadline, Overall: overall, Limit: managedLateRemainingLimit, RemainingAuthority: remaining.Authority}
	return environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: remaining.ParentSHA, Backend: environmentKubernetesBackend{Store: store}, ServiceSessionRecovery: r}, budget, artifact
}

func TestManagedLateRemainingPreparationExactProfileBindings(t *testing.T) {
	for _, mode := range []string{"genuine", "foreign-config", "foreign-stack", "foreign-store", "foreign-config-root", "foreign-parent", "foreign-runtime-parent", "foreign-backend", "foreign-owner", "foreign-review", "descriptor", "session", "cleanup", "ceremony", "not-renewal", "missing-authority", "changed-authority", "wrong-budget-authority", "old-limit", "post-clock"} {
		t.Run(mode, func(t *testing.T) {
			store := secretStore{Root: "/isolated/dev", ConfigRoot: "/isolated", Environment: "dev"}
			cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"DEPLOYMENT_PROTECTION_PROFILE": "standard", "CLOUD_STACK_NAME": "isolated-stack"}}
			e := &environmentCreateExecution{Store: store, Journal: environmentCreateJournal{Environment: "dev", Stack: "isolated-stack", PlanSHA256: strings.Repeat("a", 64)}, owner: environmentCreateOwner{OwnerToken: "isolated-original"}}
			runtime, budget, _ := managedLateRemainingPreparationFixture(t, store, e, cfg)
			r := runtime.ServiceSessionRecovery
			switch mode {
			case "foreign-config":
				cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] = "foreign"
			case "foreign-stack":
				cfg.Values["CLOUD_STACK_NAME"] = "foreign"
			case "foreign-store":
				store.Root += "/foreign"
			case "foreign-config-root":
				store.ConfigRoot += "/foreign"
			case "foreign-parent":
				e.Journal.PlanSHA256 = strings.Repeat("b", 64)
			case "foreign-runtime-parent":
				runtime.ExecutionSHA256 = strings.Repeat("f", 64)
			case "foreign-backend":
				runtime.Backend.Store.ConfigRoot = "foreign"
			case "foreign-owner":
				e.owner.OwnerToken = "foreign"
			case "foreign-review":
				r.Review = strings.Repeat("a", 64)
			case "descriptor":
				r.ManagedLateSession.SourceSHA = strings.Repeat("a", 64)
			case "session":
				r.Ceremony.BootstrapSessionID = "99999999-9999-4999-8999-999999999999"
			case "cleanup":
				r.Ceremony.CleanupBinding.SessionID = "99999999-9999-4999-8999-999999999999"
			case "ceremony":
				r.Ceremony.BootstrapCredentialSecret = "foreign"
			case "not-renewal":
				r.Renewal = false
			case "missing-authority":
				r.ManagedLateRemaining.Authority = nil
			case "changed-authority":
				r.ManagedLateRemaining.Authority.SourceSHA = strings.Repeat("f", 64)
			case "wrong-budget-authority":
				copy := *budget.RemainingAuthority
				copy.Review = strings.Repeat("f", 64)
				budget.RemainingAuthority = &copy
			case "old-limit":
				budget.Limit = managedLateOverallLimit
			case "post-clock":
				budget.PostMint = true
			}
			if managedLateRemainingPreparationBindings(cfg, store, runtime, budget) != (mode == "genuine") {
				t.Fatal("preparation admitted foreign profile/source/clock or rejected exact scope")
			}
		})
	}
}

func TestManagedLateRemainingPreparationRequiresHeldFenceAndGenuinePureHistory(t *testing.T) {
	store, e, cfg, _, _ := managedLateSourceWriteFixture(t)
	cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] = "standard"
	invalidateManagedLateSourceReuse(e)
	for len(e.Journal.Entries) < 388 {
		e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "isolated-unproved-stage", State: "completed"})
	}
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	runtime, budget, artifact := managedLateRemainingPreparationFixture(t, store, e, cfg)
	r := runtime.ServiceSessionRecovery.ManagedLateRemaining
	managedLateSourceReuseNegativeWrite(t, store, filepath.Join("deployment/create/openbao-recovery", r.Review, "qualification.json"), mustProviderRecoveryJSON(artifact))
	managedLateSourceReuseNegativeWrite(t, store, managedLateRemainingAuthorityPath(r.ParentSHA), mustProviderRecoveryJSON(r.Authority))
	ctx := context.WithValue(budget.Overall, managedLateBudgetKey{}, budget)
	if !managedLateRemainingPreparationBindings(cfg, store, runtime, budget) {
		t.Fatal("isolated exact profile fixture refused")
	}
	if _, err := readManagedLateRemainingAuthority(store, r); err != nil {
		t.Fatal("isolated immutable authority refused", err)
	}
	before := deploymentplan.Digest([]any{e.Journal, e.owner, runtime.ServiceSessionRecovery})
	if err := verifyManagedLateRemainingPreparationSource(ctx, cfg, store, runtime); err == nil {
		t.Fatal("profile and clock alone replaced genuine pure runtime ancestry")
	}
	if deploymentplan.Digest([]any{e.Journal, e.owner, runtime.ServiceSessionRecovery}) != before {
		t.Fatal("preparation rewrote original source or selected session")
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if err := verifyManagedLateRemainingPreparationSource(ctx, cfg, store, runtime); err == nil || !strings.Contains(err.Error(), "execution-fence") {
		t.Fatal("preparation accepted a released original fence", err)
	}
}

func TestManagedLateRemaining120PolicyIsFiniteAndIndependent(t *testing.T) {
	if managedLateRemainingLimit != 120*time.Minute || managedLateNativeLimit != 50*time.Minute || managedLateOverallLimit != 90*time.Minute {
		t.Fatal("remaining policy changed an ordinary native or PostMint limit")
	}
	r, a, now := managedLateRemainingNegativeFixture(t)
	r.NotBefore = now.Add(-40 * time.Minute)
	managedLateRemainingNegativeBind(t, r, &a)
	authority, err := newManagedLateRemainingAuthority(r, a, now.Add(90*time.Minute), now, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a)))
	if err != nil || !authority.Deadline.Equal(r.NotBefore.Add(120*time.Minute)) || authority.PolicySHA != managedLateRemainingPolicySHA() {
		t.Fatal("remaining policy lost signed leaf120 origin", err)
	}
	for _, mode := range []string{"missing-policy", "wrong-policy", "extended-origin", "caller-over90"} {
		t.Run(mode, func(t *testing.T) {
			copy := *authority
			switch mode {
			case "missing-policy":
				copy.PolicySHA = ""
			case "wrong-policy":
				copy.PolicySHA = strings.Repeat("f", 64)
			case "extended-origin":
				copy.Deadline = copy.Deadline.Add(time.Minute)
			case "caller-over90":
				copy.OverallDeadline = copy.AdmittedAt.Add(91 * time.Minute)
			}
			if managedLateRemainingAuthorityValid(&copy) {
				t.Fatal("changed immutable remaining policy accepted")
			}
		})
	}
	delete(a.Plan.Materials, managedLateRemainingMaterial+"policy")
	r.Review = deploymentplan.Digest(a.Plan)
	a.Qualification.PlanSHA256 = r.Review
	for i := range a.Qualification.Gates {
		a.Qualification.Gates[i].EvidenceSHA256 = r.Review
	}
	if _, err := newManagedLateRemainingAuthority(r, a, now.Add(90*time.Minute), now, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a))); err == nil {
		t.Fatal("fresh GO without the120 policy material admitted")
	}
}
