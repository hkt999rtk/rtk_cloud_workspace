package main

import (
	"context"
	"errors"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestFreshApplicationCRLInitializationBeforeOwnersUsesSelectedImage(t *testing.T) {
	cfg, p, release, runtime, s, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	final, err := finalizeEnvironmentCreateApplicationDefinitions(t.Context(), cfg, p, release, runtime, s, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	var stage string
	var captured []deploymentplan.Definition
	apply := func(_ context.Context, key string, definitions []deploymentplan.Definition) error {
		stage = key
		captured = definitions
		return nil
	}
	if err = initializeFreshApplicationCRLs(t.Context(), cfg, p, release, runtime, s, roots, intermediates, final, apply); err != nil {
		t.Fatal(err)
	}
	if stage != "application-initial-crls" || len(captured) < 6 {
		t.Fatal("missing owner initialization", stage, len(captured))
	}
	for _, d := range captured {
		if d.Object["kind"] != "Job" {
			t.Fatal("unexpected resource")
		}
		c := managedNamedField(environmentPKIPod(d.Object)["containers"], "enroll")
		if c["image"] != p.Image {
			t.Fatal("changed selected image")
		}
	}
	original := p.Image
	p.Image = "ghcr.io/foreign/image@sha256:" + strings.Repeat("a", 64)
	if err = initializeFreshApplicationCRLs(t.Context(), cfg, p, release, runtime, s, roots, intermediates, final, apply); err == nil {
		t.Fatal("foreign selected image admitted")
	}
	p.Image = original
	cfg.Values["PKI_STATE_BACKEND"] = "pvc"
	stage = ""
	if err = initializeFreshApplicationCRLs(t.Context(), cfg, p, release, runtime, s, roots, intermediates, final, apply); err != nil || stage != "" {
		t.Fatal("PVC path changed", err)
	}
}

func TestServiceRecipeUsesSelectedProductionAndStack(t *testing.T) {
	cfg, p, runtime, s := createServiceRecipeFixture(t, "prod")
	cfg.Values["CLOUD_STACK_NAME"] = "custom-prod"
	p.Stack = "custom-prod"
	s.BootstrapCredentialNamespace = p.Stack + "-video-cloud"
	if err := validateEnvironmentCreateServiceCeremony(cfg, p, runtime, s); err != nil {
		t.Fatal("valid selected prod target rejected:", err)
	}
	p.Stack = "foreign-stack"
	if err := validateEnvironmentCreateServiceCeremony(cfg, p, runtime, s); err == nil {
		t.Fatal("foreign stack accepted")
	}
}

func TestFreshApplicationOwnerSequenceWaitsForAllCRLJobs(t *testing.T) {
	cfg, p, release, runtime, s, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	final, err := finalizeEnvironmentCreateApplicationDefinitions(t.Context(), cfg, p, release, runtime, s, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	var stages []string
	apply := func(_ context.Context, key string, _ []deploymentplan.Definition) error {
		stages = append(stages, key)
		return nil
	}
	if err := applyCreateApplicationOwners(t.Context(), cfg, p, release, runtime, s, roots, intermediates, final, apply); err != nil {
		t.Fatal(err)
	}
	if len(stages) != 2 || stages[0] != "application-initial-crls" || stages[1] != "managed-application-owners" {
		t.Fatal("incorrect startup ordering", stages)
	}
	stages = nil
	fail := func(_ context.Context, key string, _ []deploymentplan.Definition) error {
		stages = append(stages, key)
		return errors.New("initialization failed")
	}
	if err := applyCreateApplicationOwners(t.Context(), cfg, p, release, runtime, s, roots, intermediates, final, fail); err == nil || len(stages) != 1 || stages[0] != "application-initial-crls" {
		t.Fatal("workloads started despite failed initialization", stages, err)
	}
}
