package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const factoryQualificationSchema = "rtk-factory-enroll-qualification/v1"

type factoryQualificationResult struct {
	Schema              string            `json:"schema"`
	RunID               string            `json:"run_id"`
	StartedAt           time.Time         `json:"started_at"`
	EndedAt             time.Time         `json:"ended_at"`
	AccountManagerURL   string            `json:"account_manager_url"`
	FactoryURL          string            `json:"factory_url"`
	DeviceBaseURL       string            `json:"device_base_url"`
	BrandCloudID        string            `json:"brand_cloud_id"`
	DeviceItemProfileID string            `json:"device_item_profile_id"`
	ProductionRunID     string            `json:"production_run_id"`
	DeviceID            string            `json:"device_id"`
	IssuerRequestID     string            `json:"issuer_request_id"`
	TokenHTTPStatus     int               `json:"token_http_status"`
	Steps               map[string]string `json:"steps"`
}

func runTestFactoryLive(args []string) error {
	fs := flag.NewFlagSet("test-factory-live", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace root")
	envRootFlag := fs.String("env-root", "", "LKE staging environment root")
	outDirFlag := fs.String("out-dir", "", "private evidence output directory")
	runIDFlag := fs.String("run-id", "", "run ID")
	deviceURLFlag := fs.String("device-url", "", "device-facing mTLS HTTPS base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	workspace := *workspaceFlag
	if workspace == "" {
		var err error
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required")
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	runID := firstNonEmpty(*runIDFlag, time.Now().UTC().Format("20060102T150405Z")+"-factory")
	outDir := firstNonEmpty(*outDirFlag, filepath.Join(workspace, ".artifacts", "test-runs", runID, "factory-live"))
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return err
	}
	stackValues, err := readEnvFile(filepath.Join(envRoot, "env", "stack.env"))
	if err != nil {
		return err
	}
	stack := firstNonEmpty(stackValues["CLOUD_STACK_NAME"], "video-cloud-staging")
	if stack != "video-cloud-staging" {
		return fmt.Errorf("factory live qualification is restricted to video-cloud-staging, got %s", stack)
	}
	deviceURL := firstNonEmpty(*deviceURLFlag, os.Getenv("VIDEO_CLOUD_DEVICE_BASE_URL"))
	if deviceURL == "" {
		videoHost := strings.TrimSpace(stackValues["VIDEO_CLOUD_DOMAIN"])
		if videoHost == "" {
			return errors.New("--device-url is required when VIDEO_CLOUD_DOMAIN is not configured")
		}
		deviceURL = "https://device." + videoHost
	}
	parsedDeviceURL, err := url.Parse(deviceURL)
	if err != nil || parsedDeviceURL.Scheme != "https" || parsedDeviceURL.Hostname() == "" {
		return errors.New("device URL must be HTTPS")
	}

	portEnv, cleanup, err := startK8SE2EPortForwardsForServices(workspace, envRoot, false)
	if err != nil {
		return err
	}
	defer cleanup()
	accountURL := envListValue(portEnv, "ACCOUNT_MANAGER_BASE_URL")
	factoryURL := firstCSVValue(envListValue(portEnv, "FACTORY_ENROLL_URL"))
	if accountURL == "" || factoryURL == "" {
		return errors.New("K8s port-forward setup did not provide Account Manager and factory endpoints")
	}
	child := exec.Command("go", "run", "./factory_enroll/cmd/rtk-factory-enroll-test", "qualify-staging",
		"--run-id", runID, "--artifact-dir", outDir, "--account-manager-url", accountURL,
		"--factory-url", factoryURL, "--device-url", deviceURL)
	child.Dir = filepath.Join(workspace, "e2e_test")
	child.Env = append(os.Environ(), portEnv...)
	child.Env = append(child.Env, "GOWORK=off", "FACTORY_ENROLL_TEST_ALLOW_LOOPBACK_TUNNEL=1")
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Run(); err != nil {
		return fmt.Errorf("factory production qualification: %w", err)
	}
	var result factoryQualificationResult
	if err := readJSONFile(filepath.Join(outDir, "factory-qualification-results.json"), &result); err != nil {
		return err
	}
	if err := validateFactoryQualificationResult(result, runID); err != nil {
		return err
	}
	kubeconfig, err := ensureK8SKubeconfig(workspace, envRoot, stack)
	if err != nil {
		return err
	}
	if err := writeFactoryRuntimeVerification(kubeconfig, stack, result, outDir); err != nil {
		return err
	}
	return importFactoryQualificationFeatureEvidence(workspace, outDir, result)
}

func envListValue(values []string, key string) string {
	prefix := key + "="
	for i := len(values) - 1; i >= 0; i-- {
		if strings.HasPrefix(values[i], prefix) {
			return strings.TrimPrefix(values[i], prefix)
		}
	}
	return ""
}

func firstCSVValue(raw string) string {
	values := splitCSV(raw)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func validateFactoryQualificationResult(result factoryQualificationResult, runID string) error {
	if result.Schema != factoryQualificationSchema || result.RunID != runID || result.EndedAt.Before(result.StartedAt) {
		return errors.New("factory qualification result identity or timing is invalid")
	}
	if result.BrandCloudID == "" || result.DeviceItemProfileID == "" || result.ProductionRunID == "" ||
		result.DeviceID == "" || result.IssuerRequestID == "" || result.TokenHTTPStatus != 200 {
		return errors.New("factory qualification result is missing a production identity, issuer request, or device token PASS")
	}
	for _, step := range factoryProductionWorkflowSteps() {
		if strings.ToUpper(result.Steps[step]) != "PASS" {
			return fmt.Errorf("factory qualification step %s did not PASS", step)
		}
	}
	if len(result.Steps) != len(factoryProductionWorkflowSteps()) {
		return errors.New("factory qualification result contains missing or unknown workflow steps")
	}
	return nil
}

func factoryProductionWorkflowSteps() []string {
	return []string{"create_device_item_profile", "issue_production_jwt", "generate_device_csr", "enroll_factory_identity", "verify_certissuer_mtls", "validate_device_certificate", "bootstrap_device_token"}
}

func writeFactoryRuntimeVerification(kubeconfig, stack string, result factoryQualificationResult, outDir string) error {
	accountNS := stack + "-account-manager"
	videoNS := stack + "-video-cloud"
	accountDeployment, err := kubectlJSON(kubeconfig, accountNS, "deployment", "account-manager")
	if err != nil {
		return err
	}
	factoryDeployment, err := kubectlJSON(kubeconfig, videoNS, "deployment", "factoryenroll")
	if err != nil {
		return err
	}
	if err := validateDeploymentReady(accountDeployment, "account-manager"); err != nil {
		return err
	}
	if err := validateFactoryDeployment(factoryDeployment); err != nil {
		return err
	}
	for _, item := range []struct {
		namespace, secret string
		keys              []string
	}{
		{accountNS, "account-manager-runtime", []string{"FACTORY_PRODUCTION_JWT_SECRET", "FACTORY_PRODUCTION_JWT_AUDIENCE"}},
		{videoNS, "factoryenroll-runtime", []string{"FACTORY_ENROLL_PRODUCTION_JWT_SECRET", "FACTORY_ENROLL_PRODUCTION_JWT_AUDIENCE"}},
	} {
		actual, err := kubectlSecretKeyNames(kubeconfig, item.namespace, item.secret)
		if err != nil {
			return err
		}
		for _, key := range item.keys {
			if !actual[key] {
				return fmt.Errorf("secret %s/%s is missing key %s", item.namespace, item.secret, key)
			}
		}
	}
	lines := []string{
		"schema=rtk-factory-runtime-verification/v1", "stack=" + stack,
		"account_manager_ready=PASS", "factoryenroll_ready=PASS", "shared_production_jwt_keys_present=PASS",
		"production_jwt_auth_configured=PASS", "certissuer_url_https=PASS", "certissuer_client_mtls_mount=PASS",
		"issuer_request_id_present=PASS", "device_certificate_chain_verified=PASS", "device_mtls_token_http_200=PASS",
		"issuer_request_id=" + result.IssuerRequestID,
	}
	return os.WriteFile(filepath.Join(outDir, "factory-runtime-verification.log"), []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

func kubectlJSON(kubeconfig, namespace, kind, name string) (map[string]any, error) {
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace, "get", kind, name, "-o", "json")
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl get %s/%s in %s: %w", kind, name, namespace, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func kubectlSecretKeyNames(kubeconfig, namespace, name string) (map[string]bool, error) {
	secret, err := kubectlJSON(kubeconfig, namespace, "secret", name)
	if err != nil {
		return nil, err
	}
	data, _ := secret["data"].(map[string]any)
	out := map[string]bool{}
	for key := range data {
		out[key] = true
	}
	return out, nil
}

func validateDeploymentReady(deployment map[string]any, expectedName string) error {
	metadata, _ := deployment["metadata"].(map[string]any)
	if metadata["name"] != expectedName {
		return fmt.Errorf("deployment name mismatch: got %v want %s", metadata["name"], expectedName)
	}
	spec, _ := deployment["spec"].(map[string]any)
	status, _ := deployment["status"].(map[string]any)
	if number(status["readyReplicas"]) < number(spec["replicas"]) || number(spec["replicas"]) < 1 {
		return fmt.Errorf("deployment %s is not fully ready", expectedName)
	}
	return nil
}

func validateFactoryDeployment(deployment map[string]any) error {
	if err := validateDeploymentReady(deployment, "factoryenroll"); err != nil {
		return err
	}
	raw, _ := json.Marshal(deployment)
	text := string(raw)
	for _, marker := range []string{
		"FACTORY_ENROLL_PRODUCTION_JWT_SECRET", "FACTORY_ENROLL_PRODUCTION_JWT_AUDIENCE",
		"https://certissuer.", "FACTORY_ENROLL_CERT_ISSUER_CLIENT_CERT", "FACTORY_ENROLL_CERT_ISSUER_CLIENT_KEY",
		"factoryenroll-certissuer-client", "rtk.realtek.com/runtime-checksum",
	} {
		if !strings.Contains(text, marker) {
			return fmt.Errorf("factoryenroll deployment is missing runtime marker %s", marker)
		}
	}
	return nil
}

func number(value any) float64 {
	number, _ := value.(float64)
	return number
}

func importFactoryQualificationFeatureEvidence(workspace, outDir string, result factoryQualificationResult) error {
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		return err
	}
	inventory, err := loadSpecInventory(workspace)
	if err != nil {
		return err
	}
	refs, err := factoryQualificationEvidenceRefs(outDir)
	if err != nil {
		return err
	}
	if matches := findUnredactedFeatureEvidence(outDir); len(matches) > 0 {
		return fmt.Errorf("unredacted credential-like content found in factory qualification evidence: %s", strings.Join(matches, ", "))
	}
	requirements := catalogRequirementIndex(catalog)
	featureByRequirement := catalogFeatureByRequirement(catalog)
	caseIDs := []string{"E2E-FACTORY-ENROLL-001", "E2E-AUTH-FACTORY-BOOTSTRAP-001"}
	cases := make([]featureCaseEvidenceV2, 0, len(caseIDs))
	for _, testID := range caseIDs {
		tc, ok := catalogCaseByID(catalog.Cases, testID)
		if !ok || tc.Status != "active" {
			return fmt.Errorf("factory qualification Test ID %s is missing or inactive", testID)
		}
		commits := map[string]string{}
		var assertions []featureRequirementAssertion
		for _, requirementID := range tc.Verifies {
			requirement, ok := requirements[requirementID]
			if !ok {
				return fmt.Errorf("unknown factory requirement %s", requirementID)
			}
			if missing := missingFeatureEvidenceTypes(requirement.Evidence, refs); len(missing) > 0 {
				return fmt.Errorf("%s required evidence types missing: %s", requirementID, strings.Join(missing, ", "))
			}
			featureCommits, err := currentFeatureCommits(workspace, featureByRequirement[requirementID])
			if err != nil {
				return err
			}
			for key, value := range featureCommits {
				commits[key] = value
			}
			assertions = append(assertions, featureRequirementAssertion{
				RequirementID: requirementID, Revision: requirement.Revision, SpecSource: requirement.SpecSource,
				Status: "PASS", Assessment: "production JWT factory enrollment and deployed runtime evidence passed",
				Assertions: map[string]string{"production_context": "PASS", "native_flow": "PASS", "runtime_posture": "PASS", "redaction": "PASS"}, Evidence: refs,
			})
		}
		workflows, err := factoryQualificationWorkflowAssertions(tc, inventory, result)
		if err != nil {
			return err
		}
		cases = append(cases, featureCaseEvidenceV2{
			TestID: testID, Status: "PASS", Assessment: "deployed production factory identity became a usable runtime identity",
			Environment: "staging", Target: "", StartedAt: result.StartedAt.UTC().Format(time.RFC3339), CompletedAt: result.EndedAt.UTC().Format(time.RFC3339),
			WorkspaceCommit: commits["workspace"], Commits: commits, Requirements: assertions, Workflows: workflows,
		})
	}
	specCommit, err := currentCanonicalSpecCommit(workspace)
	if err != nil {
		return err
	}
	manifest := featureEvidenceManifestV2{SchemaVersion: featureEvidenceSchemaV3, RunID: result.RunID, GeneratedAt: result.EndedAt.UTC().Format(time.RFC3339), SpecCommit: specCommit, Cases: cases}
	if err := validateFeatureEvidenceManifestV2(manifest, catalog, inventory); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outDir, "feature-evidence.json"), manifest); err != nil {
		return err
	}
	return writeLiveFeatureSummaryArtifacts(outDir, manifest)
}

func factoryQualificationWorkflowAssertions(tc testCatalogCase, inventory specInventory, result factoryQualificationResult) ([]featureWorkflowAssertion, error) {
	var out []featureWorkflowAssertion
	for _, workflow := range inventory.Workflows {
		bound := false
		for _, requirementID := range workflow.RequirementIDs {
			bound = bound || catalogContainsString(tc.Verifies, requirementID)
		}
		if !bound {
			continue
		}
		statuses := map[string]string{}
		details := map[string]map[string]string{}
		switch workflow.ID {
		case "WF-PROV-FACTORY-001":
			for _, step := range factoryProductionWorkflowSteps() {
				statuses[step] = result.Steps[step]
				details[step] = map[string]string{"operation_succeeded": "PASS", "run_identity_correlated": "PASS"}
			}
		case "WF-CONTRACT-AUTH-FACTORY-001":
			statuses = map[string]string{"enroll_factory_device": result.Steps["enroll_factory_identity"], "bootstrap_enrolled_device": result.Steps["bootstrap_device_token"]}
			details = map[string]map[string]string{
				"enroll_factory_device":     {"production_jwt_used": "PASS", "certificate_chain_verified": "PASS"},
				"bootstrap_enrolled_device": {"device_mtls": "PASS", "subject_bound_token_issued": "PASS"},
			}
		default:
			return nil, fmt.Errorf("factory qualification has no evidence adapter for workflow %s", workflow.ID)
		}
		assertion, err := buildWorkflowAssertionWithDetails(workflow, statuses, details)
		if err != nil {
			return nil, err
		}
		out = append(out, assertion)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkflowID < out[j].WorkflowID })
	return out, nil
}

func factoryQualificationEvidenceRefs(outDir string) ([]featureCoverageEvidenceFile, error) {
	names := []string{"factory-qualification-results.json", "factory-qualification-report.md", "factory-qualification-junit.xml", "factory-runtime-verification.log", "factory-runner/factory-enroll-results.json", "factory-runner/factory-enroll-report.md"}
	refs := make([]featureCoverageEvidenceFile, 0, len(names))
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			return nil, fmt.Errorf("read factory qualification evidence %s: %w", name, err)
		}
		sum := sha256.Sum256(raw)
		refs = append(refs, featureCoverageEvidenceFile{Path: name, SHA256: hex.EncodeToString(sum[:]), Type: featureEvidenceType(name)})
	}
	return refs, nil
}
