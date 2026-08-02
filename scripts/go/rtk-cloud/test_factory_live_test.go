package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
