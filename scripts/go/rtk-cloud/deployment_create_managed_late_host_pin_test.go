package main

import (
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateOriginalHostPinFixture() (*createServiceSessionRecovery, *createFoundationRecoveryArtifact, string, string) {
	pin := strings.Repeat("4", 64)
	admission := &createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-foundation-recovery-artifact/v1", Plan: createFoundationRecoveryPlan{
		Schema:    "rtk-cloud.create-managed-application-trust-openbao-ack-recovery/v1",
		ParentSHA: strings.Repeat("7", 64), Materials: map[string]string{"accepted-service-core-managed-application-current-servers": pin},
	}}
	review := deploymentplan.Digest(admission.Plan)
	r := &createServiceSessionRecovery{Review: review, ManagedCorePartial: &createManagedCorePartialRecovery{OriginalRepairReview: review, ArtifactSHA: deploymentplan.Digest(admission)}, ManagedTransportRecovery: &createManagedTransportRecovery{Partial: &createManagedTransportPartialRecovery{}}}
	return r, admission, review, pin
}

func TestManagedLateOriginalHostPinHydratesOnlyAuthenticatedOriginalAdmission(t *testing.T) {
	r, admission, review, pin := managedLateOriginalHostPinFixture()
	if r.ManagedTransportRecovery.Partial.ServerSHA != "" {
		t.Fatal("fixture must model pure historical derivation's unpopulated pin")
	}
	if err := hydrateManagedLateOriginalHostServerPin(r, admission, review); err != nil {
		t.Fatal(err)
	}
	if r.ManagedTransportRecovery.Partial.ServerSHA != pin {
		t.Fatal("did not retain original admitted host pin")
	}
	before := deploymentplan.Digest(r)
	if err := hydrateManagedLateOriginalHostServerPin(r, admission, review); err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Digest(r) != before {
		t.Fatal("idempotent hydration changed identity")
	}
}

func TestManagedLateOriginalHostPinRejectsChangedAuthorityWithoutMutation(t *testing.T) {
	tests := map[string]func(*createServiceSessionRecovery, *createFoundationRecoveryArtifact, *string){
		"wrong-review-argument": func(r *createServiceSessionRecovery, a *createFoundationRecoveryArtifact, review *string) {
			*review = strings.Repeat("9", 64)
		},
		"wrong-runtime-review": func(r *createServiceSessionRecovery, a *createFoundationRecoveryArtifact, review *string) {
			r.Review = strings.Repeat("9", 64)
		},
		"wrong-original-review": func(r *createServiceSessionRecovery, a *createFoundationRecoveryArtifact, review *string) {
			r.ManagedCorePartial.OriginalRepairReview = strings.Repeat("9", 64)
		},
		"wrong-artifact": func(r *createServiceSessionRecovery, a *createFoundationRecoveryArtifact, review *string) {
			r.ManagedCorePartial.ArtifactSHA = strings.Repeat("9", 64)
		},
		"changed-qualified-artifact": func(r *createServiceSessionRecovery, a *createFoundationRecoveryArtifact, review *string) {
			a.Qualification.Decision = "NO-GO"
		},
		"different-admitted-plan": func(r *createServiceSessionRecovery, a *createFoundationRecoveryArtifact, review *string) {
			a.Plan.ParentSHA = strings.Repeat("9", 64)
			r.ManagedCorePartial.ArtifactSHA = deploymentplan.Digest(a)
		},
		"wrong-schema-even-rebound": func(r *createServiceSessionRecovery, a *createFoundationRecoveryArtifact, review *string) {
			a.Plan.Schema = "rtk-cloud.create-managed-other-recovery/v1"
			*review = deploymentplan.Digest(a.Plan)
			r.Review = *review
			r.ManagedCorePartial.OriginalRepairReview = *review
			r.ManagedCorePartial.ArtifactSHA = deploymentplan.Digest(a)
		},
		"empty-pin-even-rebound": func(r *createServiceSessionRecovery, a *createFoundationRecoveryArtifact, review *string) {
			a.Plan.Materials["accepted-service-core-managed-application-current-servers"] = ""
			*review = deploymentplan.Digest(a.Plan)
			r.Review = *review
			r.ManagedCorePartial.OriginalRepairReview = *review
			r.ManagedCorePartial.ArtifactSHA = deploymentplan.Digest(a)
		},
		"invalid-pin-even-rebound": func(r *createServiceSessionRecovery, a *createFoundationRecoveryArtifact, review *string) {
			a.Plan.Materials["accepted-service-core-managed-application-current-servers"] = "private-not-a-digest"
			*review = deploymentplan.Digest(a.Plan)
			r.Review = *review
			r.ManagedCorePartial.OriginalRepairReview = *review
			r.ManagedCorePartial.ArtifactSHA = deploymentplan.Digest(a)
		},
		"populated-mismatch": func(r *createServiceSessionRecovery, a *createFoundationRecoveryArtifact, review *string) {
			r.ManagedTransportRecovery.Partial.ServerSHA = strings.Repeat("9", 64)
		},
		"missing-core": func(r *createServiceSessionRecovery, a *createFoundationRecoveryArtifact, review *string) {
			r.ManagedCorePartial = nil
		},
		"missing-transport": func(r *createServiceSessionRecovery, a *createFoundationRecoveryArtifact, review *string) {
			r.ManagedTransportRecovery = nil
		},
		"missing-partial": func(r *createServiceSessionRecovery, a *createFoundationRecoveryArtifact, review *string) {
			r.ManagedTransportRecovery.Partial = nil
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			r, a, review, _ := managedLateOriginalHostPinFixture()
			mutate(r, a, &review)
			before := deploymentplan.Digest(r)
			if err := hydrateManagedLateOriginalHostServerPin(r, a, review); err == nil {
				t.Fatal("accepted changed original authority")
			}
			if deploymentplan.Digest(r) != before {
				t.Fatal("rejection changed original recovery identity")
			}
		})
	}
	t.Run("nil-source", func(t *testing.T) {
		_, a, review, _ := managedLateOriginalHostPinFixture()
		if hydrateManagedLateOriginalHostServerPin(nil, a, review) == nil {
			t.Fatal("accepted nil source")
		}
	})
	t.Run("nil-admission", func(t *testing.T) {
		r, _, review, _ := managedLateOriginalHostPinFixture()
		if hydrateManagedLateOriginalHostServerPin(r, nil, review) == nil {
			t.Fatal("accepted nil admission")
		}
	})
}
