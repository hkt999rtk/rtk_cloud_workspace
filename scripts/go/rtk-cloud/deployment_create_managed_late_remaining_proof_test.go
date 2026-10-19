package main

import (
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedLateRemainingACKProofPreservesImmediatePriorAndSourceNormalizer(t *testing.T) {
	late := &createManagedLateSessionRecovery{Review: strings.Repeat("a", 64), SourceSHA: strings.Repeat("b", 64), OriginalSession: "11111111-1111-4111-8111-111111111111", OriginalEnrollmentSession: "22222222-2222-4222-8222-222222222222", NewSessionID: "33333333-3333-4333-8333-333333333333", ExpiredRenewal: &createManagedLateExpiredRenewal{Review: strings.Repeat("a", 64)}}
	plan := &deploymentplan.Plan{Version: deploymentplan.Version}
	before := deploymentplan.Digest(late)
	proof := managedLateACKSourceProof(late, plan)
	if proof["original_session_id"] != late.OriginalSession || proof["original_session_id"] == late.OriginalEnrollmentSession || proof["late_descriptor_sha256"] != managedLateSessionSourceDigest(late) || proof["late_owners_plan_sha256"] != plan.SHA256() || deploymentplan.Digest(late) != before {
		t.Fatal("e2 proof lost its immediate expired e1 or changed original history")
	}
	late.ExpiredRenewal.Review = strings.Repeat("c", 64)
	if managedLateACKSourceProof(late, plan)["late_descriptor_sha256"] != proof["late_descriptor_sha256"] {
		t.Fatal("runtime outer review changed the qualified source descriptor digest")
	}
	late.ExpiredRenewal = nil
	late.OriginalSession = late.OriginalEnrollmentSession
	if managedLateACKSourceProof(late, plan)["original_session_id"] != late.OriginalEnrollmentSession {
		t.Fatal("ordinary original-generation ACK field changed")
	}
}
