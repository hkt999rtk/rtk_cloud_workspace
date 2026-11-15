package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestFreshProductProgramUsesAdmittedProviderRecoveryGeneration(t *testing.T) {
	f := newProductProtocolFixture(t, false)
	original, _, err := loadEnvironmentCreateAdmittedGeneration(f.store, f.e.Journal.PlanSHA256, f.e.Journal.ArtifactSHA256)
	if err != nil {
		t.Fatal(err)
	}
	next, recovery := providerExecutionContractFork(t, f.e, original, "")
	f.e, f.b.normalTailE2EBackend.e, f.h.Execution = next, next, next
	f.runtime.Execution, f.runtime.ExecutionSHA256, f.runtime.PKIClient.Execution = next, next.Journal.PlanSHA256, next
	f.unscoped, f.runtime.ProductIdentityScope, f.runtime.PKIClient.mutationScope = true, "", ""
	f.l.ParentSHA256, f.l.SourceSHA256, f.l.OwnerSHA256 = next.Journal.PlanSHA256, deploymentplan.Digest(next.Journal), deploymentplan.Digest(next.owner)
	f.subjects, err = environmentCreatePlannedServiceSubjects(f.cfg, f.p)
	if err != nil {
		t.Fatal(err)
	}
	crl, err := readProductCurrentCRL(t.Context(), f.runtime.PKIClient, f.l.Root)
	if err != nil {
		t.Fatal(err)
	}
	service, err := createEnvironmentServiceSession(t.Context(), f.cfg, f.p, f.store, f.runtime, environmentCreateServiceCeremony{Root: f.l.Root, Intermediate: f.next, RootCRLPEM: crl, RootCRLSecret: "pki-service-root-crl"}, f.subjects, f.apply)
	if err != nil {
		t.Fatal("original recovery session", err)
	}
	core, err := f.controls(service, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.apply(t.Context(), "managed-complete-issuer", core); err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("isolated original product enrollment interruption")
	err = executeEnvironmentCreateFreshProductIdentities(t.Context(), f.cfg, f.p, environmentRelease{}, f.store, f.runtime, service, func(context.Context, string, []deploymentplan.Definition) error { return interrupted })
	if !errors.Is(err, interrupted) {
		t.Fatal("program must be genuinely admitted before enrollment interruption", err)
	}
	var active environmentPreflightActive
	activePath := filepath.Join(f.store.Root, "deployment/create/preflight/active.json")
	if err := readEnvironmentPreflightJSON(activePath, &active); err != nil || active.PlanSHA256 != recovery.Plan.OriginalPlanSHA256 || active.PlanSHA256 == next.Journal.PlanSHA256 {
		t.Fatal("fixture must retain the original create pointer and distinct admitted recovery envelope", err)
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	if _, err := readFreshProductProgram(f.store, next); err != nil {
		t.Fatal("original fresh program cannot resolve its admitted provider recovery artifact", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	t.Run("active-pointer-unavailable", func(t *testing.T) {
		raw, err := os.ReadFile(activePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(activePath); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.WriteFile(activePath, raw, 0600); err != nil {
				t.Fatal(err)
			}
		}()
		if _, err := readFreshProductProgram(f.store, next); err != nil {
			t.Fatal("mutable preflight pointer became original generation authority", err)
		}
	})
	t.Run("foreign-admitted-artifact", func(t *testing.T) {
		sha := next.Journal.ArtifactSHA256
		next.Journal.ArtifactSHA256 = deploymentplan.Digest("foreign original qualification")
		defer func() { next.Journal.ArtifactSHA256 = sha }()
		if _, err := readFreshProductProgram(f.store, next); err == nil {
			t.Fatal("foreign recovery admission accepted")
		}
	})
	t.Run("changed-original-artifact", func(t *testing.T) {
		path := filepath.Join(f.store.Root, "deployment/create/recovery", next.Journal.PlanSHA256, "qualification.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		changed := recovery
		changed.Plan.Plan.Stack = "video-cloud-foreign"
		if err := os.WriteFile(path, mustProviderRecoveryJSON(changed), 0600); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
		}()
		if _, err := readFreshProductProgram(f.store, next); err == nil {
			t.Fatal("changed admitted artifact accepted")
		}
	})
	assertRecoveryContractReadOnly(t, f.store, before)
}
