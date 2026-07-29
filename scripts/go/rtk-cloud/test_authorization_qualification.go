package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type authorizationQualificationSpec struct {
	TestID     string
	Repository string
	Package    string
	GoTest     string
	Assertions map[string]map[string]string
	Workflows  map[string]map[string]string
}

var authorizationQualificationSpecs = []authorizationQualificationSpec{
	{
		TestID: "INT-AM-AUTHZ-BOUNDARY-001", Repository: "rtk_account_manager", Package: "./internal/api", GoTest: "TestIntegrationVideoCloudRuntimeScopeDoesNotGrantProductRole",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTHZ-BOUNDARY-001": {
				"runtime_admin_scope_rejected": "PASS",
				"product_user_route_rejected":  "PASS",
				"platform_acl_route_rejected":  "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-AUTHZ-MATRIX-001", Repository: "rtk_account_manager", Package: "./internal/api", GoTest: "TestIntegrationAuthorizationAndTenancyMatrix",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTHZ-ROUTES-001": {
				"declared_role_access":       "PASS",
				"cross_tenant_denial":        "PASS",
				"non_disclosing_not_found":   "PASS",
				"disabled_subject_rejection": "PASS",
			},
			"REQ-CONTRACT-AUTHZ-COMPAT-001": {
				"member_device_lifecycle":       "PASS",
				"platform_admin_compatibility":  "PASS",
				"ordinary_user_platform_denial": "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-AUTHZ-SOURCE-001", Repository: "rtk_account_manager", Package: "./internal/store", GoTest: "TestACLExternalGroupMappingCreatesScopedAssignment",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTHZ-SOURCE-001": {
				"explicit_external_group_mapping": "PASS",
				"persisted_role_assignment":       "PASS",
				"organization_scope_enforced":     "PASS",
				"unmapped_permission_denied":      "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-AUTHZ-MODEL-001", Repository: "rtk_account_manager", Package: "./internal/store", GoTest: "TestACLRoleAssignmentsAuthorizeInsideScopeOnly",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTHZ-MODEL-001": {
				"explicit_assignment_required": "PASS",
				"organization_scope_enforced":  "PASS",
				"read_only_write_denied":       "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-AUTHZ-CATALOG-001", Repository: "rtk_account_manager", Package: "./internal/store", GoTest: "TestACLSeedPermissionCatalogAndSystemRoles",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTHZ-CATALOG-001": {
				"permission_catalog_seeded": "PASS",
				"stable_role_names_seeded":  "PASS",
			},
			"REQ-CONTRACT-AUTHZ-DEFAULTS-001": {
				"declared_positive_grants": "PASS",
				"undeclared_write_denied":  "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-AUTHZ-PROVIDER-001", Repository: "rtk_account_manager", Package: "./internal/api", GoTest: "TestIntegrationChipsetProviderACLRefreshVisibilityAndAudit",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTHZ-PROVIDER-001": {
				"read_permission_independent":    "PASS",
				"edit_permission_independent":    "PASS",
				"publish_permission_independent": "PASS",
				"provider_audit_recorded":        "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-OTA-OPERATOR-001", Repository: "rtk_video_cloud", Package: "./internal/httpapi", GoTest: "TestSKUOTAOperatorAndDeviceAuthenticationBoundaries",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTHZ-OTA-OPERATOR-001": {
				"operator_credential_required": "PASS",
				"brand_context_required":       "PASS",
				"signed_brand_context_used":    "PASS",
			},
			"REQ-CONTRACT-AUTHZ-OTA-DEVICE-001": {
				"device_credential_required":   "PASS",
				"non_device_scope_rejected":    "PASS",
				"token_subject_is_device_id":   "PASS",
				"request_body_cannot_override": "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-OTA-ARTIFACT-001", Repository: "rtk_video_cloud", Package: "./internal/skuota", GoTest: "TestArtifactTokenIsDeploymentScopedAndExpires",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTHZ-OTA-ARTIFACT-001": {
				"foreign_device_rejected":   "PASS",
				"deployment_scope_enforced": "PASS",
				"expired_grant_rejected":    "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-OTA-TENANT-001", Repository: "rtk_video_cloud", Package: "./internal/skuota", GoTest: "TestTenantIsolationAndEventIdempotency",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTHZ-OTA-TENANT-001": {
				"foreign_brand_release_denied": "PASS",
				"foreign_state_not_returned":   "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-BRANDPROFILE-001", Repository: "rtk_account_manager", Package: "./internal/api", GoTest: "TestIntegrationPlatformAdminDeviceItemProfileLifecycle",
		Assertions: map[string]map[string]string{
			"REQ-CA-BRAND-PROFILE-001": {
				"inventory_metadata_preserved":    "PASS",
				"service_options_explicit":        "PASS",
				"category_derived_acl_rejected":   "PASS",
				"invalid_service_option_rejected": "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-BRANDIDENT-001", Repository: "rtk_account_manager", Package: "./internal/api", GoTest: "TestIntegrationAppEndUserClaimCreatesMultiBrandBindings",
		Assertions: map[string]map[string]string{
			"REQ-CA-BRAND-IDENTITY-001": {
				"global_app_subject_created":   "PASS",
				"claim_creates_brand_link":     "PASS",
				"claim_creates_device_binding": "PASS",
			},
			"REQ-CA-BRAND-PRIVACY-001": {
				"current_brand_only":         "PASS",
				"foreign_brand_not_returned": "PASS",
				"direct_email_not_returned":  "PASS",
			},
		},
		Workflows: map[string]map[string]string{
			"WF-CA-BRAND-IDENTITY-001": {
				"login_app_end_user":       "PASS",
				"resolve_app_device_claim": "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-BRANDADMIN-001", Repository: "rtk_account_manager", Package: "./internal/api", GoTest: "TestIntegrationPlatformAdminBrandCloudLifecycle",
		Assertions: map[string]map[string]string{
			"REQ-CA-BRAND-ADMIN-AUTH-001": {
				"platform_admin_allowed": "PASS",
				"ordinary_user_rejected": "PASS",
				"brand_scope_preserved":  "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-BRANDUSER-001", Repository: "rtk_account_manager", Package: "./internal/api", GoTest: "TestIntegrationPlatformAdminCreatesActiveBrandCloudUser",
		Assertions: map[string]map[string]string{
			"REQ-CA-BRAND-USER-PROVISION-001": {
				"brand_identity_isolated": "PASS",
				"verified_state_created":  "PASS",
				"password_not_reused":     "PASS",
				"lifecycle_is_idempotent": "PASS",
			},
			"REQ-CA-BRAND-AUDIT-001": {
				"lifecycle_events_recorded": "PASS",
				"platform_actor_attributed": "PASS",
				"brand_subject_attributed":  "PASS",
			},
		},
		Workflows: map[string]map[string]string{
			"WF-CA-AUDIT-001": {
				"create_audited_brand_cloud": "PASS",
				"read_brand_cloud_audit":     "PASS",
			},
			"WF-CA-BRAND-001": {
				"create_brand_cloud":  "PASS",
				"create_brand_owner":  "PASS",
				"disable_brand_owner": "PASS",
				"enable_brand_owner":  "PASS",
				"delete_brand_owner":  "PASS",
			},
		},
	},
	{
		TestID: "INT-CA-BRANDBFF-001", Repository: "rtk_cloud_admin", Package: "./internal/app", GoTest: "TestPlatformAdminBrandCloudsProxyRequiresUpstreamToken",
		Assertions: map[string]map[string]string{
			"REQ-CA-BRAND-SOURCE-001": {
				"account_manager_results_returned": "PASS",
				"upstream_authority_required":      "PASS",
				"local_fallback_rejected":          "PASS",
			},
			"REQ-CA-BRAND-BFF-001": {
				"bearer_token_forwarded":   "PASS",
				"request_fields_forwarded": "PASS",
				"upstream_failure_safe":    "PASS",
			},
		},
	},
}

type goTestJSONEvent struct {
	Action string `json:"Action"`
	Test   string `json:"Test"`
}

type authorizationQualificationResult struct {
	TestID      string `json:"test_id"`
	Selector    string `json:"selector"`
	Status      string `json:"status"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
	DurationMS  int64  `json:"duration_ms"`
}

func runAuthorizationQualification(workspace, outputDir, runID string) error {
	if strings.TrimSpace(runID) == "" {
		runID = time.Now().UTC().Format("20060102T150405Z") + "-account-authz"
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		return err
	}
	caseByID := map[string]testCatalogCase{}
	for _, tc := range catalog.Cases {
		caseByID[tc.ID] = tc
	}
	results := make([]authorizationQualificationResult, 0, len(authorizationQualificationSpecs))
	for _, spec := range authorizationQualificationSpecs {
		tc, ok := caseByID[spec.TestID]
		if !ok || tc.Status != "active" || tc.Layer != "integration" {
			return fmt.Errorf("authorization qualification case %s is missing or is not an active integration case", spec.TestID)
		}
		if tc.Selector != spec.GoTest {
			return fmt.Errorf("authorization qualification case %s selector=%q, want %q", spec.TestID, tc.Selector, spec.GoTest)
		}
		if err := validateAuthorizationQualificationAssertions(tc, spec); err != nil {
			return err
		}
		started := time.Now().UTC()
		command := exec.Command("go", "test", "-json", "-count=1", spec.Package, "-run", "^"+spec.GoTest+"$")
		command.Dir = filepath.Join(workspace, "repos", spec.Repository)
		command.Env = append(os.Environ(), "GOWORK=off")
		output, commandErr := command.CombinedOutput()
		completed := time.Now().UTC()
		passed, skipped, parseErr := qualificationGoTestStatus(output, spec.GoTest)
		if parseErr != nil {
			return fmt.Errorf("%s result: %w", spec.TestID, parseErr)
		}
		if commandErr != nil {
			return fmt.Errorf("%s failed: %w", spec.TestID, commandErr)
		}
		if skipped || !passed {
			return fmt.Errorf("%s did not execute to PASS (skipped=%t)", spec.TestID, skipped)
		}
		results = append(results, authorizationQualificationResult{
			TestID: spec.TestID, Selector: spec.GoTest, Status: "PASS",
			StartedAt: started.Format(time.RFC3339), CompletedAt: completed.Format(time.RFC3339),
			DurationMS: completed.Sub(started).Milliseconds(),
		})
	}
	resultsPath := filepath.Join(outputDir, "results.json")
	if err := writeJSON(resultsPath, map[string]any{
		"schema_version": "rtk-account-authorization-qualification/v1",
		"run_id":         runID,
		"status":         "PASS",
		"cases":          results,
	}); err != nil {
		return err
	}
	junitPath := filepath.Join(outputDir, "junit.xml")
	if err := os.WriteFile(junitPath, []byte(renderAuthorizationQualificationJUnit(results)), 0o644); err != nil {
		return err
	}
	refs, err := qualificationEvidenceRefs(outputDir, resultsPath, junitPath)
	if err != nil {
		return err
	}
	workspaceCommit, err := gitOutput(workspace, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	specCommit, err := currentCanonicalSpecCommit(workspace)
	if err != nil {
		return err
	}
	requirements := catalogRequirementIndex(catalog)
	features := catalogFeatureByRequirement(catalog)
	inventory, err := loadSpecInventory(workspace)
	if err != nil {
		return err
	}
	workflows := map[string]specWorkflow{}
	for _, workflow := range inventory.Workflows {
		workflows[workflow.ID] = workflow
	}
	resultByID := map[string]authorizationQualificationResult{}
	for _, result := range results {
		resultByID[result.TestID] = result
	}
	manifest := featureEvidenceManifestV2{
		SchemaVersion: featureEvidenceSchemaV3,
		RunID:         runID,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		SpecCommit:    specCommit,
	}
	for _, spec := range authorizationQualificationSpecs {
		result := resultByID[spec.TestID]
		assertions := make([]featureRequirementAssertion, 0, len(spec.Assertions))
		workflowAssertions := make([]featureWorkflowAssertion, 0, len(spec.Workflows))
		requirementIDs := make([]string, 0, len(spec.Assertions))
		for requirementID := range spec.Assertions {
			requirementIDs = append(requirementIDs, requirementID)
		}
		sort.Strings(requirementIDs)
		for _, requirementID := range requirementIDs {
			requirement, ok := requirements[requirementID]
			if !ok {
				return fmt.Errorf("%s references unknown requirement %s", spec.TestID, requirementID)
			}
			assertions = append(assertions, featureRequirementAssertion{
				RequirementID: requirementID,
				Revision:      requirement.Revision,
				SpecSource:    requirement.SpecSource,
				Status:        "PASS",
				Assessment:    "explicit targeted integration assertions passed",
				Assertions:    spec.Assertions[requirementID],
				Evidence:      refs,
			})
		}
		workflowIDs := make([]string, 0, len(spec.Workflows))
		for workflowID := range spec.Workflows {
			workflowIDs = append(workflowIDs, workflowID)
		}
		sort.Strings(workflowIDs)
		for _, workflowID := range workflowIDs {
			workflow, ok := workflows[workflowID]
			if !ok {
				return fmt.Errorf("%s references unknown workflow %s", spec.TestID, workflowID)
			}
			assertion, err := buildWorkflowAssertion(workflow, spec.Workflows[workflowID])
			if err != nil {
				return fmt.Errorf("%s: %w", spec.TestID, err)
			}
			workflowAssertions = append(workflowAssertions, assertion)
		}
		var caseFeature testCatalogFeature
		for _, requirementID := range requirementIDs {
			feature, ok := features[requirementID]
			if !ok {
				return fmt.Errorf("%s requirement %s has no canonical feature", spec.TestID, requirementID)
			}
			if caseFeature.ID != "" && caseFeature.ID != feature.ID {
				return fmt.Errorf("%s spans features %s and %s", spec.TestID, caseFeature.ID, feature.ID)
			}
			caseFeature = feature
		}
		commits, err := currentFeatureCommits(workspace, caseFeature)
		if err != nil {
			return err
		}
		manifest.Cases = append(manifest.Cases, featureCaseEvidenceV2{
			TestID: spec.TestID, Status: "PASS", Assessment: "targeted integration test executed and passed",
			Environment: "ci", StartedAt: result.StartedAt, CompletedAt: result.CompletedAt,
			WorkspaceCommit: strings.TrimSpace(workspaceCommit), Commits: commits, Requirements: assertions, Workflows: workflowAssertions,
		})
	}
	return writeJSON(filepath.Join(outputDir, "feature-evidence.json"), manifest)
}

func validateAuthorizationQualificationAssertions(tc testCatalogCase, spec authorizationQualificationSpec) error {
	mapped := map[string]bool{}
	for _, requirementID := range tc.Verifies {
		mapped[requirementID] = true
		if len(spec.Assertions[requirementID]) == 0 {
			return fmt.Errorf("%s has no explicit assertions for %s", tc.ID, requirementID)
		}
	}
	for requirementID := range spec.Assertions {
		if !mapped[requirementID] {
			return fmt.Errorf("%s emits an assertion for unmapped requirement %s", tc.ID, requirementID)
		}
	}
	return nil
}

func qualificationGoTestStatus(output []byte, testName string) (passed, skipped bool, err error) {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		var event goTestJSONEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Test != testName {
			continue
		}
		switch event.Action {
		case "pass":
			passed = true
		case "skip":
			skipped = true
		}
	}
	if err := scanner.Err(); err != nil {
		return false, false, err
	}
	if !passed && !skipped {
		return false, false, fmt.Errorf("go test JSON has no terminal event for %s", testName)
	}
	return passed, skipped, nil
}

func qualificationEvidenceRefs(outputDir string, paths ...string) ([]featureCoverageEvidenceFile, error) {
	refs := make([]featureCoverageEvidenceFile, 0, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(raw)
		refs = append(refs, featureCoverageEvidenceFile{
			Path: filepath.ToSlash(filepath.Base(path)), SHA256: hex.EncodeToString(sum[:]), Type: featureEvidenceType(path),
		})
	}
	return refs, nil
}

func renderAuthorizationQualificationJUnit(results []authorizationQualificationResult) string {
	var totalMS int64
	var cases []string
	for _, result := range results {
		totalMS += result.DurationMS
		cases = append(cases, fmt.Sprintf(
			`  <testcase classname="account-manager.authorization" name="%s" time="%.3f"/>`,
			xmlEscape(result.TestID+" "+result.Selector), float64(result.DurationMS)/1000,
		))
	}
	return fmt.Sprintf(
		"<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<testsuite name=\"account-authorization-qualification\" tests=\"%d\" failures=\"0\" skipped=\"0\" time=\"%.3f\">\n%s\n</testsuite>\n",
		len(results), float64(totalMS)/1000, strings.Join(cases, "\n"),
	)
}

func xmlEscape(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(value)
}
