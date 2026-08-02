package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func installFactoryTestCommand(t *testing.T, name, body string) string {
	t.Helper()
	bin := t.TempDir()
	path := filepath.Join(bin, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return path
}

func initFactoryTestRepo(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "factory-test@example.invalid"}, {"config", "user.name", "Factory Test"},
	} {
		if output, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-qm", "fixture"}} {
		if output, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	commit, err := gitOutput(path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(commit)
}

func validFactoryQualificationResult() factoryQualificationResult {
	started := time.Date(2026, 8, 2, 1, 2, 3, 0, time.UTC)
	steps := map[string]string{}
	for _, step := range factoryProductionWorkflowSteps() {
		steps[step] = "PASS"
	}
	return factoryQualificationResult{
		Schema: factoryQualificationSchema, RunID: "factory-live-001", StartedAt: started, EndedAt: started.Add(time.Minute),
		AccountManagerURL: "http://127.0.0.1:18081", FactoryURL: "http://127.0.0.1:18443",
		DeviceBaseURL: "https://device.video-cloud-staging.realtekconnect.com",
		BrandCloudID:  "brand-001", DeviceItemProfileID: "profile-001", ProductionRunID: "production-001",
		DeviceID: "pk-device-001", IssuerRequestID: "issuer-001", TokenHTTPStatus: 200, Steps: steps,
	}
}

func TestValidateFactoryQualificationResultRequiresEveryCanonicalStep(t *testing.T) {
	result := validFactoryQualificationResult()
	if err := validateFactoryQualificationResult(result, result.RunID); err != nil {
		t.Fatal(err)
	}
	delete(result.Steps, "verify_certissuer_mtls")
	if err := validateFactoryQualificationResult(result, result.RunID); err == nil || !strings.Contains(err.Error(), "verify_certissuer_mtls") {
		t.Fatalf("missing-step error = %v", err)
	}
}

func TestValidateFactoryDeploymentRequiresProductionJWTAndMTLSPosture(t *testing.T) {
	var deployment map[string]any
	if err := json.Unmarshal([]byte(`{
  "metadata":{"name":"factoryenroll","annotations":{"rtk.realtek.com/runtime-checksum":"checksum"}},
  "spec":{"replicas":1,"template":{"spec":{
    "containers":[{"env":[
      {"name":"FACTORY_ENROLL_PRODUCTION_JWT_SECRET"},
      {"name":"FACTORY_ENROLL_PRODUCTION_JWT_AUDIENCE"},
      {"name":"FACTORY_ENROLL_CERT_ISSUER_CLIENT_CERT"},
      {"name":"FACTORY_ENROLL_CERT_ISSUER_CLIENT_KEY"},
      {"name":"FACTORY_ENROLL_CERT_ISSUER_URL","value":"https://certissuer.stack.svc:9443"}
    ]}],
    "volumes":[{"name":"factoryenroll-certissuer-client"}]
  }}},
  "status":{"readyReplicas":1}
}`), &deployment); err != nil {
		t.Fatal(err)
	}
	if err := validateFactoryDeployment(deployment); err != nil {
		t.Fatal(err)
	}
	rawTemplate := deployment["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	rawTemplate["volumes"] = []any{}
	if err := validateFactoryDeployment(deployment); err == nil || !strings.Contains(err.Error(), "factoryenroll-certissuer-client") {
		t.Fatalf("missing volume error = %v", err)
	}
}

func TestDeploymentChecksumPatchCarriesOnlyDigestAndFactoryEnvRefs(t *testing.T) {
	patch := deploymentChecksumPatch("digest-value", []map[string]any{{
		"name":      "FACTORY_ENROLL_PRODUCTION_JWT_SECRET",
		"valueFrom": map[string]any{"secretKeyRef": map[string]string{"name": "factoryenroll-runtime", "key": "FACTORY_ENROLL_PRODUCTION_JWT_SECRET"}},
	}})
	raw, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, marker := range []string{"digest-value", "factoryenroll-runtime", "FACTORY_ENROLL_PRODUCTION_JWT_SECRET"} {
		if !strings.Contains(text, marker) {
			t.Fatalf("patch missing %q: %s", marker, text)
		}
	}
	if strings.Contains(text, "production-jwt-secret-marker") {
		t.Fatal("deployment patch must not carry the signing secret")
	}
}

func TestImportFactoryQualificationWritesCrossFeatureEvidence(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outDir, "factory-runner"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"factory-qualification-results.json":         `{"schema":"rtk-factory-enroll-qualification/v1"}`,
		"factory-qualification-report.md":            "# Factory qualification PASS\n",
		"factory-qualification-junit.xml":            `<testsuite tests="1"><testcase name="factory"/></testsuite>`,
		"factory-runtime-verification.log":           "production_jwt_auth_configured=PASS\ncertissuer_client_mtls_mount=PASS\n",
		"factory-runner/factory-enroll-results.json": `{"summary":{"successes":1}}`,
		"factory-runner/factory-enroll-report.md":    "# Factory enrollment PASS\n",
	} {
		if err := os.WriteFile(filepath.Join(outDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result := validFactoryQualificationResult()
	if err := importFactoryQualificationFeatureEvidence(workspace, outDir, result); err != nil {
		t.Fatal(err)
	}
	var manifest featureEvidenceManifestV2
	if err := readJSONFile(filepath.Join(outDir, "feature-evidence.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Cases) != 2 {
		t.Fatalf("cases = %d, want 2", len(manifest.Cases))
	}
	if len(manifest.Cases[0].Workflows)+len(manifest.Cases[1].Workflows) != 2 {
		t.Fatalf("workflow assertions = %+v", manifest.Cases)
	}
}

func TestConfigureFactoryProductionJWTRuntimePatchesAndRollsOutBothServices(t *testing.T) {
	envRoot := t.TempDir()
	tokenPath := filepath.Join(envRoot, "state", "openbao", "root-token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("test-openbao-root-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "kubectl.log")
	t.Setenv("FACTORY_KUBECTL_LOG", logPath)
	installFactoryTestCommand(t, "kubectl", `printf '%s\n' "$*" >> "$FACTORY_KUBECTL_LOG"
cat >/dev/null || true`)

	if err := configureFactoryProductionJWTRuntime("/tmp/factory-kubeconfig", envRoot, "video-cloud-staging"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(raw)
	for _, marker := range []string{
		"exec -i openbao-0", "patch secret account-manager-runtime", "patch secret factoryenroll-runtime",
		"patch deployment account-manager", "patch deployment factoryenroll",
		"rollout status deployment/account-manager", "rollout status deployment/factoryenroll",
	} {
		if !strings.Contains(logText, marker) {
			t.Fatalf("kubectl log missing %q:\n%s", marker, logText)
		}
	}
	if strings.Contains(logText, "test-openbao-root-token") {
		t.Fatal("OpenBao root token leaked into command arguments")
	}
}

func TestWriteFactoryRuntimeVerificationChecksDeploymentsSecretsAndSourceImages(t *testing.T) {
	workspace := t.TempDir()
	accountCommit := initFactoryTestRepo(t, filepath.Join(workspace, "repos", "rtk_account_manager"))
	videoCommit := initFactoryTestRepo(t, filepath.Join(workspace, "repos", "rtk_video_cloud"))
	short := func(value string) string {
		if len(value) > 12 {
			return value[:12]
		}
		return value
	}
	accountImage := "ghcr.io/test/account-manager:sha-" + short(accountCommit)
	videoImage := "ghcr.io/test/video-cloud:sha-" + short(videoCommit)
	script := fmt.Sprintf(`case "$*" in
  *"get deployment account-manager -o json"*) printf '%%s\n' '{"metadata":{"name":"account-manager"},"spec":{"replicas":1,"template":{"spec":{"containers":[{"image":"%s"}]}}},"status":{"readyReplicas":1}}' ;;
  *"get deployment factoryenroll -o json"*) printf '%%s\n' '{"metadata":{"name":"factoryenroll"},"spec":{"replicas":1,"template":{"spec":{"containers":[{"image":"%s"}]}}},"status":{"readyReplicas":1},"runtime_markers":["FACTORY_ENROLL_PRODUCTION_JWT_SECRET","FACTORY_ENROLL_PRODUCTION_JWT_AUDIENCE","https://certissuer.stack.svc:9443","FACTORY_ENROLL_CERT_ISSUER_CLIENT_CERT","FACTORY_ENROLL_CERT_ISSUER_CLIENT_KEY","factoryenroll-certissuer-client","rtk.realtek.com/runtime-checksum"]}' ;;
  *"get deployment certissuer -o json"*) printf '%%s\n' '{"metadata":{"name":"certissuer"},"spec":{"replicas":1,"template":{"spec":{"containers":[{"image":"%s"}]}}},"status":{"readyReplicas":1}}' ;;
  *"get secret account-manager-runtime -o json"*) printf '%%s\n' '{"data":{"FACTORY_PRODUCTION_JWT_SECRET":"eA==","FACTORY_PRODUCTION_JWT_AUDIENCE":"eA=="}}' ;;
  *"get secret factoryenroll-runtime -o json"*) printf '%%s\n' '{"data":{"FACTORY_ENROLL_PRODUCTION_JWT_SECRET":"eA==","FACTORY_ENROLL_PRODUCTION_JWT_AUDIENCE":"eA=="}}' ;;
  *) exit 7 ;;
esac`, accountImage, videoImage, videoImage)
	installFactoryTestCommand(t, "kubectl", script)
	outDir := t.TempDir()
	result := validFactoryQualificationResult()
	if err := writeFactoryRuntimeVerification(workspace, "/tmp/factory-kubeconfig", "video-cloud-staging", result, outDir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "factory-runtime-verification.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"shared_production_jwt_keys_present=PASS", "source_commit_images_matched=PASS", "issuer_request_id=issuer-001"} {
		if !strings.Contains(string(raw), marker) {
			t.Fatalf("runtime verification missing %q: %s", marker, raw)
		}
	}
}

func TestFactoryQualificationValidationRejectsInvalidIdentityTimingAndSteps(t *testing.T) {
	valid := validFactoryQualificationResult()
	tests := []struct {
		name   string
		mutate func(*factoryQualificationResult)
	}{
		{"schema", func(result *factoryQualificationResult) { result.Schema = "wrong" }},
		{"run id", func(result *factoryQualificationResult) { result.RunID = "wrong" }},
		{"timing", func(result *factoryQualificationResult) { result.EndedAt = result.StartedAt.Add(-time.Second) }},
		{"identity", func(result *factoryQualificationResult) { result.DeviceID = "" }},
		{"token status", func(result *factoryQualificationResult) { result.TokenHTTPStatus = 401 }},
		{"failed step", func(result *factoryQualificationResult) { result.Steps["generate_device_csr"] = "FAIL" }},
		{"unknown step", func(result *factoryQualificationResult) { result.Steps["unknown"] = "PASS" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := valid
			result.Steps = map[string]string{}
			for key, value := range valid.Steps {
				result.Steps[key] = value
			}
			test.mutate(&result)
			if err := validateFactoryQualificationResult(result, valid.RunID); err == nil {
				t.Fatal("invalid factory result was accepted")
			}
		})
	}
}

func TestFactoryQualificationSmallHelpersAndFailureBranches(t *testing.T) {
	if got := envListValue([]string{"A=old", "B=value", "A=new"}, "A"); got != "new" {
		t.Fatalf("envListValue = %q", got)
	}
	if got := envListValue(nil, "missing"); got != "" {
		t.Fatalf("missing envListValue = %q", got)
	}
	if got := firstCSVValue(" first, second "); got != "first" || firstCSVValue("") != "" {
		t.Fatalf("firstCSVValue = %q", got)
	}
	patch := deploymentChecksumPatch("checksum", nil)
	raw, _ := json.Marshal(patch)
	if strings.Contains(string(raw), "containers") {
		t.Fatalf("annotation-only checksum patch unexpectedly contains containers: %s", raw)
	}
	if number(float64(3)) != 3 || number("3") != 0 {
		t.Fatal("number conversion did not preserve its strict JSON-number contract")
	}
	ready := map[string]any{"metadata": map[string]any{"name": "service"}, "spec": map[string]any{"replicas": float64(2)}, "status": map[string]any{"readyReplicas": float64(1)}}
	if err := validateDeploymentReady(ready, "wrong"); err == nil || !strings.Contains(err.Error(), "name mismatch") {
		t.Fatalf("name mismatch error = %v", err)
	}
	if err := validateDeploymentReady(ready, "service"); err == nil || !strings.Contains(err.Error(), "not fully ready") {
		t.Fatalf("readiness error = %v", err)
	}
	if err := runTestFactoryLive([]string{"--unknown"}); err == nil {
		t.Fatal("unknown factory-live flag was accepted")
	}
	if err := runTestFactoryLive([]string{"--workspace", t.TempDir()}); err == nil || !strings.Contains(err.Error(), "--env-root is required") {
		t.Fatalf("missing env-root error = %v", err)
	}
	if _, err := factoryQualificationEvidenceRefs(t.TempDir()); err == nil {
		t.Fatal("missing factory evidence files were accepted")
	}
}
