package main

// multicloudQualificationSpecs turns the existing cross-service integration
// cases into immutable feature evidence. These cases deliberately do not claim
// the canonical end-to-end workflows: producer preparation is still synthetic
// in CI and the full lifecycle, sharing, and handoff sequences remain staging
// qualifications.
var multicloudQualificationSpecs = []authorizationQualificationSpec{
	{
		TestID:     "INT-AM-CLOUDBOOTSTRAP-002",
		Repository: "rtk_account_manager",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/api", GoTest: "TestIntegrationBillingCloudCreationAcrossRealServices"},
		},
		Assertions: map[string]map[string]string{
			"REQ-MULTICLOUD-LIFECYCLE-001": {
				"signup_commits_unique_owner_cloud":       "PASS",
				"billing_responsibility_initialized_once": "PASS",
				"lost_bootstrap_reply_recovers":           "PASS",
			},
		},
	},
	{
		TestID:     "INT-BILL-MULTICLOUD-ELIGIBILITY-001",
		Repository: "rtk_billing",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/paymentstore", GoTest: "TestOwnershipEligibilityIsReadOnlyAndAcceptsNonnegativeCredit"},
			{Package: "./internal/paymentstore", GoTest: "TestOwnershipEligibilityRetainsBlockersAndRejectsInvalidScope"},
			{Package: "./internal/api", GoTest: "TestOwnershipEligibilityHTTPBoundary"},
		},
		Assertions: map[string]map[string]string{
			"REQ-MULTICLOUD-HANDOFF-001": {
				"negative_balance_rejected":               "PASS",
				"zero_balance_eligible":                   "PASS",
				"positive_balance_eligible":               "PASS",
				"independent_financial_blockers_retained": "PASS",
				"eligibility_read_is_non_mutating":        "PASS",
			},
		},
	},
	{
		TestID:     "INT-BILL-HANDOFF-001",
		Repository: "rtk_billing",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/api", GoTest: "TestHandoffAccountManagerPublicAPIContract"},
		},
		Assertions: map[string]map[string]string{
			"REQ-MULTICLOUD-HANDOFF-001": {
				"global_source_and_target_sessions_used": "PASS",
				"exact_nonnegative_snapshot_confirmed":   "PASS",
				"sole_owner_commit_persisted":            "PASS",
				"responsibility_period_finalized":        "PASS",
				"lost_reply_recovery_is_idempotent":      "PASS",
			},
		},
	},
	{
		TestID:     "INT-BILL-DELETION-001",
		Repository: "rtk_billing",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/api", GoTest: "TestCloudDeletionAccountManagerPublicAPIContract"},
		},
		Assertions: map[string]map[string]string{
			"REQ-MULTICLOUD-LIFECYCLE-001": {
				"global_owner_deletion_api_used":        "PASS",
				"billing_closure_persisted":             "PASS",
				"lost_close_reply_recovers":             "PASS",
				"cancel_and_close_races_are_serialized": "PASS",
			},
		},
	},
}
