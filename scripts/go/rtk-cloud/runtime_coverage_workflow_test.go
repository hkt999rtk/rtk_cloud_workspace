package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCloudAdminE2EInitializesCanonicalRequirementSource(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, ".github", "workflows", "cloud-admin-e2e.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)
	for _, required := range []string{
		"'repos/rtk_cloud_contracts_doc'",
		"CI_RUNNER_GITHUB_WORK_KEY",
		`core.sshCommand "ssh -i ~/.ssh/id_ed25519_github_work -o IdentitiesOnly=yes"`,
		"repos/rtk_cloud_admin \\",
		"repos/rtk_cloud_contracts_doc",
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("Cloud Admin E2E workflow is missing canonical requirement source %q", required)
		}
	}
}

func TestRuntimeCoverageWorkflowKeepsSharedClusterGuardrails(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, ".github", "workflows", "go-runtime-coverage-nightly.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)
	for _, required := range []string{
		"mode:",
		"options: [preflight, run]",
		"video-cloud-staging-lke",
		"group: staging-mutating-tests",
		"RUNTIME_COVERAGE_NIGHTLY_ENABLED",
		"RUNTIME_COVERAGE_SHARED_CLUSTER: \"1\"",
		"runs-on: ubuntu-24.04",
		"RUNTIME_COVERAGE_RUNNER_LABEL: ubuntu-24.04",
		"RUNTIME_COVERAGE_FEATURE_WORKFLOW: workflow-local-live",
		"lfs: true",
		"LKE_POSTGRES_STORAGE_MODE: emptydir",
		`server_ca="$(awk -F= '$1 == "RUNTIME_COVERAGE_SERVER_CA"`,
		`sudo cp "$server_ca"`,
		"RUNTIME_COVERAGE_PLANNED_PVCS: \"5\"",
		"RUNTIME_COVERAGE_PLANNED_LOAD_BALANCERS: \"0\"",
		"RUNTIME_COVERAGE_PLANNED_GENERATORS: \"0\"",
		`get secret video-cloud-runtime -o json`,
		`.data.AWS_ACCESS_KEY_ID | @base64d`,
		`deployment_env VIDEO_CLOUD_BLOB_ENDPOINT`,
		`deployment_env VIDEO_CLOUD_BLOB_REGION`,
		`deployment_env VIDEO_CLOUD_BLOB_BUCKET`,
		`deployment_env VIDEO_CLOUD_WEBRTC_TURN_URLS`,
		`.data.VIDEO_CLOUD_TURN_SHARED_SECRET | @base64d`,
		`RUNTIME_COVERAGE_SHARED_TURN_HOST=$turn_public_host`,
		`LKE_TURN_SHARED=$turn_shared_secret`,
		`VIDEO_CLOUD_WEBRTC_TURN_URLS=$turn_urls`,
		`RUNTIME_COVERAGE_STORAGE_SOURCE_NAMESPACE=$staging_video_namespace`,
		"VIDEO_CLOUD_BLOB_PREFIX=runtime-coverage/%s",
		"--preflight --plan --apply --deploy --artifacts",
		"Prepare run-scoped Clip credentials",
		"exec deployment/video-cloud-api -- /app/admin-token --ttl 1h",
		"VIDEO_CLOUD_LOAD_CLIP_USER_PRIVATE_KEY=$user_private",
		"VIDEO_CLOUD_LOAD_CLIP_SERVER_PUBLIC_KEY=$server_public",
		"--metadata-file",
		"--provenance=false",
		"--build-context runtime_coverage_helper=tests/runtime-coverage",
		"pinned_image=\"$image@$digest\"",
		"${env_key}_DIGEST=$digest",
		"lke-image-manifest.json",
		"runtime-coverage-k8s.sh verify",
		"runtime-coverage-k8s.sh endpoints",
		"feature-endpoints.env",
		"CLOUD_DNS_ROOT_DOMAIN=$RUNTIME_COVERAGE_STACK.invalid",
		`video_cloud_api_base_url="https://video.$RUNTIME_COVERAGE_STACK.invalid:18443"`,
		`printf 'VIDEO_CLOUD_API_BASE_URL=%s\n' "$video_cloud_api_base_url"`,
		`grep -Fxq "VIDEO_CLOUD_API_BASE_URL=$video_cloud_api_base_url"`,
		"runtime-coverage-k8s.sh cleanup",
		"RUNTIME_COVERAGE_DEPLOYED=1",
		"RUNTIME_COVERAGE_PREPARED=1",
		"--device-count 12",
		"--device-mix light=10,camera=2",
		`export HOME100K_ENV_ROOT="$RUNTIME_ENV_ROOT"`,
		"Aggregate runtime feature evidence",
		`test-feature-coverage "$action"`,
		"test-spec-inventory check",
		`--evidence ".artifacts/test-runs/$RUNTIME_COVERAGE_RUN_ID"`,
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("runtime workflow missing %q", required)
		}
	}
	preflightRaw, err := os.ReadFile(filepath.Join(workspace, "scripts", "ci", "runtime-coverage-preflight.sh"))
	if err != nil {
		t.Fatal(err)
	}
	preflight := string(preflightRaw)
	for _, required := range []string{
		"testsrc2_1080p_2s.h264",
		"oid sha256:",
		"sha256sum",
		"H264 fixture is not materialized from Git LFS",
	} {
		if !strings.Contains(preflight, required) {
			t.Fatalf("runtime preflight missing H264 fixture guard %q", required)
		}
	}
	for _, forbidden := range []string{
		"--dns",
		"cloud_env/staging/lke\n",
		"jarvis-macos",
		"secrets.VIDEO_CLOUD_ADMIN_TOKEN",
		"secrets.CLIP_USER_PRIVATE_KEY_PATH",
		"secrets.CLIP_SERVER_PUBLIC_KEY_PATH",
		"secrets.LINODE_OBJ_ACCESS_KEY_ID",
		"secrets.LINODE_OBJ_SECRET_ACCESS_KEY",
		"secrets.LINODE_OBJ_ENDPOINT",
		"secrets.LINODE_OBJ_BUCKET",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("runtime workflow contains forbidden value %q", forbidden)
		}
	}
	feature, err := os.ReadFile(filepath.Join(workspace, ".github", "workflows", "feature-qualification.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(feature), "group: staging-mutating-tests") {
		t.Fatal("feature qualification does not share the staging mutation lock")
	}
	dockerfiles, err := filepath.Glob(filepath.Join(workspace, "tests", "runtime-coverage", "Dockerfile.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dockerfiles) != 5 {
		t.Fatalf("runtime coverage Dockerfiles = %d, want 5", len(dockerfiles))
	}
	for _, dockerfile := range dockerfiles {
		raw, err := os.ReadFile(dockerfile)
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		if !strings.Contains(body, "runtime_coverage_helper") ||
			!strings.Contains(body, "-covermode=atomic") ||
			!strings.Contains(body, "-coverpkg=./...") {
			t.Fatalf("%s does not inject the atomic runtime coverage flush helper", dockerfile)
		}
	}
	accountDockerfile, err := os.ReadFile(filepath.Join(workspace, "tests", "runtime-coverage", "Dockerfile.account-manager"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"go build -trimpath -o /out/rtk-account-manager-outbox-worker ./cmd/outbox-worker",
		"COPY --from=builder /out/rtk-account-manager-outbox-worker /app/rtk-account-manager-outbox-worker",
	} {
		if !strings.Contains(string(accountDockerfile), want) {
			t.Fatalf("runtime Account Manager image missing %q", want)
		}
	}
	videoDockerfile, err := os.ReadFile(filepath.Join(workspace, "tests", "runtime-coverage", "Dockerfile.video-cloud"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(videoDockerfile), "turnregistrar") {
		t.Fatal("runtime Video Cloud image must include the run-scoped TURN registrar")
	}
	onboardingStart := strings.Index(workflow, "- name: Run onboarding and runtime log checks")
	canaryStart := strings.Index(workflow, "- name: Run all feature canaries")
	uiStart := strings.Index(workflow, "- name: Run desktop and mobile deployed UI smoke")
	lifecycleStart := strings.Index(workflow, "- name: Run cross-service lifecycle checks")
	aggregateStart := strings.Index(workflow, "- name: Aggregate runtime feature evidence")
	if onboardingStart < 0 || canaryStart < 0 || uiStart < 0 || lifecycleStart < 0 || aggregateStart < 0 {
		t.Fatal("runtime workflow lifecycle steps are missing")
	}
	if !(onboardingStart < canaryStart && canaryStart < uiStart && uiStart < lifecycleStart && lifecycleStart < aggregateStart) {
		t.Fatal("destructive lifecycle qualification must run after feature canaries and deployed UI smoke")
	}
	onboarding := workflow[onboardingStart:canaryStart]
	if !strings.Contains(onboarding, "tunnel-start") ||
		!strings.Contains(onboarding, "$ACCOUNT_MANAGER_BASE_URL/v1/health") ||
		!strings.Contains(onboarding, "$VIDEO_CLOUD_BASE_URL/version") ||
		!strings.Contains(onboarding, "CLOUD_STAGING_E2E_VIDEO_CLOUD_TOKEN_BASE_URL_OVERRIDE") ||
		!strings.Contains(onboarding, "CLOUD_STAGING_E2E_FACTORY_ENROLL_PORT=18444") ||
		!strings.Contains(onboarding, "CLOUD_STAGING_E2E_MQTT_PORT=18884") ||
		!strings.Contains(onboarding, "CLOUD_BIND_DEVICES_CLAIM_EVIDENCE_COUNT=1") ||
		!strings.Contains(onboarding, "staging-e2e-test") ||
		!strings.Contains(onboarding, "--steps data,mqtt,runtime-logs,billing-log,billing-db ") {
		t.Fatal("onboarding must use the isolated mTLS tunnel for token issuance without colliding with test-live factory/MQTT forwarding")
	}
	if strings.Contains(onboarding, "--steps lifecycle") || strings.Contains(onboarding, "billing-db,lifecycle") {
		t.Fatal("onboarding must not mutate device eligibility before feature canaries")
	}
	if strings.Contains(onboarding, "-- test-live") {
		t.Fatal("partial onboarding must not publish the complete live case before lifecycle evidence exists")
	}
	lifecycle := workflow[lifecycleStart:aggregateStart]
	for _, expected := range []string{
		"tunnel-start",
		"CLOUD_STAGING_E2E_VIDEO_CLOUD_TOKEN_BASE_URL_OVERRIDE",
		"CLOUD_STAGING_E2E_FACTORY_ENROLL_PORT=18444",
		"CLOUD_STAGING_E2E_MQTT_PORT=18884",
		"--skip-remove --skip-provision",
		"--steps lifecycle",
		`mv "$live_dir/summary.json" "$live_dir/onboarding-summary.json"`,
		`mv "$live_dir/TEST_REPORT.md" "$live_dir/ONBOARDING_TEST_REPORT.md"`,
		`--out-dir "$live_dir"`,
	} {
		if !strings.Contains(lifecycle, expected) {
			t.Fatalf("terminal lifecycle qualification missing %q", expected)
		}
	}
	if strings.Contains(onboarding, "HOME100K_REFRESH_APP_CERT") {
		t.Fatal("onboarding must reuse the matching app key and certificate created by data setup")
	}
	if !strings.Contains(workflow[canaryStart:uiStart], "tunnel-start") {
		t.Fatal("feature canaries require the shared HTTPS/MQTT tunnel")
	}
	if !strings.Contains(workflow, "FEATURE_QUALIFICATION_MODE: ${{ vars.FEATURE_QUALIFICATION_MODE || 'observe' }}") {
		t.Fatal("runtime runner must receive the repository feature qualification mode")
	}
	if !strings.Contains(workflow, "coverage_mode=main") ||
		!strings.Contains(workflow, `if [ "${{ github.event_name }}" = "schedule" ]`) ||
		!strings.Contains(workflow, "coverage_mode=scheduled") ||
		!strings.Contains(workflow, `--mode "$coverage_mode"`) {
		t.Fatal("runtime feature aggregate must enforce scheduled requirements only for true schedule events")
	}
	if !strings.Contains(workflow, "go-version: \"1.26.3\"") {
		t.Fatal("runtime runner must satisfy the Cloud Admin Go toolchain requirement")
	}
	deterministicStart := strings.Index(workflow, "- name: Run deterministic feature evidence")
	loginStart := strings.Index(workflow, "- name: Log in to GHCR")
	if deterministicStart < 0 || loginStart < 0 || deterministicStart > loginStart {
		t.Fatal("runtime deterministic evidence must run after preflight and before deployment work")
	}
	deterministic := workflow[deterministicStart:loginStart]
	for _, expected := range []string{
		"test-e2e",
		"test-ui",
		"--full --desktop --mobile --install",
		"--output-dir \"$evidence_root/ui\"",
		"postgres:16-alpine",
		"trap cleanup_qualification_postgres EXIT",
		"--publish 127.0.0.1::5432",
		"qualification_postgres_ready=false",
		"qualification_postgres_ready=true",
		`if [[ "$qualification_postgres_ready" != true ]]`,
		"qualification postgres status={{.State.Status}} exit={{.State.ExitCode}}",
		"TEST_DATABASE_URL=\"postgres://postgres:postgres@127.0.0.1:${qualification_port}/postgres?sslmode=disable\"",
		"--qualification-only",
		"--qualification-only \\\n            --install \\",
		"--qualification-output-dir \"$evidence_root/authorization\"",
		"cleanup_qualification_postgres\n          trap - EXIT",
	} {
		if !strings.Contains(deterministic, expected) {
			t.Fatalf("runtime deterministic evidence is missing %q", expected)
		}
	}
	ui := workflow[uiStart:]
	for _, expected := range []string{
		"svc/account-manager 18081:80",
		"select email, password, brand_cloud_id",
		"role in ('member', 'owner', 'admin')",
		"case when role = 'member'",
		"customer_fixture",
		"E2E_BRAND_CLOUD_ID",
		"svc/video-cloud-prometheus 18091:9090",
		"test-platform-live",
		"--platform-session \"$E2E_PLATFORM_SESSION_ID\"",
		"--customer-session \"$E2E_CUSTOMER_SESSION_ID\"",
	} {
		if !strings.Contains(ui, expected) {
			t.Fatalf("runtime UI smoke must reuse its run-scoped customer identity: missing %q", expected)
		}
	}
	for _, forbidden := range []string{"/v1/auth/register", "customer_register_payload", "customer_register_response"} {
		if strings.Contains(ui, forbidden) {
			t.Fatalf("runtime UI smoke must not re-register its existing run-scoped customer: found %q", forbidden)
		}
	}
}

func TestWorkspaceBaselineRunsAndCanEnforceDeterministicFeatureEvidence(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, ".github", "workflows", "workspace-test-baseline.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)
	for _, required := range []string{
		"FEATURE_EVIDENCE_RUN_ID:",
		"Run deterministic product UI evidence",
		"--full --desktop --mobile --install",
		"FEATURE_QUALIFICATION_MODE",
		"action=check",
		"test-spec-inventory check",
		"test-spec-impact",
		"Qualify canonical authorization requirements",
		"--qualification-only",
		"--qualification-only \\\n            --install \\",
		"--qualification-output-dir",
		"TEST_DATABASE_URL:",
		`export FEATURE_QUALIFICATION_MODE="$qualification"`,
		`--evidence ".artifacts/test-runs/$FEATURE_EVIDENCE_RUN_ID"`,
		`.artifacts/test-runs/${{ env.FEATURE_EVIDENCE_RUN_ID }}/**`,
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("workspace baseline missing feature governance wiring %q", required)
		}
	}
}

func TestK8SE2ETokenBaseURLSupportsMTLSTunnelOverride(t *testing.T) {
	t.Setenv("CLOUD_STAGING_E2E_VIDEO_CLOUD_TOKEN_BASE_URL_OVERRIDE", "")
	if got := k8sE2ETokenBaseURL("18080"); got != "http://127.0.0.1:18080" {
		t.Fatalf("default token base URL = %q", got)
	}
	t.Setenv("CLOUD_STAGING_E2E_VIDEO_CLOUD_TOKEN_BASE_URL_OVERRIDE", "https://device.coverage.invalid:18443")
	if got := k8sE2ETokenBaseURL("18080"); got != "https://device.coverage.invalid:18443" {
		t.Fatalf("overridden token base URL = %q", got)
	}
}

func TestRuntimeCoverageFlushHelperWritesMetadataAndCountersOnSIGTERM(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	helper, err := os.ReadFile(filepath.Join(workspace, "tests", "runtime-coverage", "coverage_flush.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "coverage_flush.go"), helper, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main
import (
	"os"
	"time"
)
func main() {
	_ = os.WriteFile(os.Getenv("READY_FILE"), []byte("ready"), 0o600)
	for {
		covered()
		time.Sleep(10 * time.Millisecond)
	}
}
func covered() int { return 1 }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module runtimeflush\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "runtimeflush")
	build := exec.Command("go", "build", "-cover", "-covermode=atomic", "-coverpkg=./...", "-o", binary, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build runtime flush fixture: %v\n%s", err, output)
	}
	coverageDir := filepath.Join(dir, "coverage")
	if err := os.Mkdir(coverageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary)
	readyFile := filepath.Join(dir, "ready")
	command.Env = append(os.Environ(), "GOCOVERDIR="+coverageDir, "READY_FILE="+readyFile)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 100 && !exists(readyFile); attempt++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !exists(readyFile) {
		_ = command.Process.Kill()
		t.Fatal("coverage fixture did not become ready")
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("coverage fixture exit: %v", err)
	}
	meta, err := filepath.Glob(filepath.Join(coverageDir, "covmeta.*"))
	if err != nil {
		t.Fatal(err)
	}
	counters, err := filepath.Glob(filepath.Join(coverageDir, "covcounters.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(meta) == 0 || len(counters) == 0 {
		t.Fatalf("coverage flush files: meta=%v counters=%v", meta, counters)
	}
}

func TestRuntimeCoveragePreflightWrongConfirmationIsBlocked(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(t.TempDir(), "preflight.json")
	command := exec.Command("bash", filepath.Join(workspace, "scripts", "ci", "runtime-coverage-preflight.sh"))
	command.Env = append(os.Environ(),
		"GITHUB_WORKSPACE="+workspace,
		"RUNTIME_COVERAGE_RUN_ID=unit-preflight",
		"RUNTIME_COVERAGE_MODE=run",
		"RUNTIME_COVERAGE_CONFIRM=wrong-stack",
		"CLOUD_STAGING_LKE_CLUSTER_LABEL=video-cloud-staging-lke",
		"RUNTIME_COVERAGE_RUNNER_LABEL=ubuntu-24.04",
		"RUNTIME_COVERAGE_RUNNER_OS=Linux",
		"RUNTIME_COVERAGE_RUNNER_ARCH=X64",
		"GITHUB_ACTIONS=true",
		"RUNNER_OS=Linux",
		"RUNNER_ARCH=X64",
		"RUNTIME_COVERAGE_PREFLIGHT_REPORT="+report,
	)
	if err := command.Run(); err == nil {
		t.Fatal("preflight accepted a wrong confirmation")
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Status   string   `json:"status"`
		Failures []string `json:"failures"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Status != "BLOCKED" || !strings.Contains(strings.Join(parsed.Failures, "\n"), "requires confirmation") {
		t.Fatalf("preflight report = %#v", parsed)
	}
}

func TestRuntimeCoveragePreflightWrongRunnerArchitectureIsBlocked(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(t.TempDir(), "preflight.json")
	command := exec.Command("bash", filepath.Join(workspace, "scripts", "ci", "runtime-coverage-preflight.sh"))
	command.Env = append(os.Environ(),
		"GITHUB_WORKSPACE="+workspace,
		"RUNTIME_COVERAGE_RUN_ID=unit-preflight",
		"RUNTIME_COVERAGE_MODE=preflight",
		"CLOUD_STAGING_LKE_CLUSTER_LABEL=video-cloud-staging-lke",
		"RUNTIME_COVERAGE_RUNNER_LABEL=ubuntu-24.04",
		"RUNTIME_COVERAGE_RUNNER_OS=Linux",
		"RUNTIME_COVERAGE_RUNNER_ARCH=X64",
		"GITHUB_ACTIONS=true",
		"RUNNER_OS=Linux",
		"RUNNER_ARCH=ARM64",
		"RUNTIME_COVERAGE_PREFLIGHT_REPORT="+report,
	)
	if err := command.Run(); err == nil {
		t.Fatal("preflight accepted a wrong runner architecture")
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Status string `json:"status"`
		Runner struct {
			Label        string `json:"label"`
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		} `json:"runner"`
		Failures []string `json:"failures"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Status != "BLOCKED" ||
		parsed.Runner.Label != "ubuntu-24.04" ||
		parsed.Runner.OS != "Linux" ||
		parsed.Runner.Architecture != "ARM64" ||
		!strings.Contains(strings.Join(parsed.Failures, "\n"), "runner architecture must be X64") {
		t.Fatalf("preflight report = %#v", parsed)
	}
}

func TestRuntimeCoveragePreflightRequiresSharedStagingStorageSource(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(t.TempDir(), "preflight.json")
	command := exec.Command("bash", filepath.Join(workspace, "scripts", "ci", "runtime-coverage-preflight.sh"))
	command.Env = append(os.Environ(),
		"GITHUB_WORKSPACE="+workspace,
		"RUNTIME_COVERAGE_RUN_ID=unit-preflight",
		"RUNTIME_COVERAGE_MODE=preflight",
		"CLOUD_STAGING_LKE_CLUSTER_LABEL=video-cloud-staging-lke",
		"RUNTIME_COVERAGE_RUNNER_LABEL=ubuntu-24.04",
		"RUNTIME_COVERAGE_PREFLIGHT_REPORT="+report,
		"RUNTIME_COVERAGE_STORAGE_SOURCE_NAMESPACE=unexpected",
	)
	if err := command.Run(); err == nil {
		t.Fatal("preflight accepted object storage outside shared staging")
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Failures []string `json:"failures"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(parsed.Failures, "\n"), "object storage must be sourced from the shared staging") {
		t.Fatalf("preflight failures = %#v", parsed.Failures)
	}
}

func TestRuntimeCleanupWritesResidualAndStagingAnchorReport(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "scripts", "ci", "runtime-coverage-k8s.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, required := range []string{
		"cleanup-report.json",
		"deployment-anchors.json",
		"feature-endpoints.json",
		"runtime-coverage-turnregistrar",
		"allow-runtime-coverage-turnregistrar",
		"port: 18190",
		"tunnel_start",
		"tunnel_stop",
		"ingress.log",
		"type: ClusterIP",
		"18443:443 18883:8883",
		"18090:80",
		`map \$http_upgrade \$connection_upgrade`,
		`proxy_set_header Upgrade \$http_upgrade`,
		`proxy_set_header Connection \$connection_upgrade`,
		"claim=\"${module}-runtime-coverage\"",
		"group_by(.) | max_by(length)[0]",
		"nodeSelector:{\"kubernetes.io/hostname\":$node_name}",
		`test "${GOCOVERDIR:-}" = /coverage && test -w "$GOCOVERDIR"`,
		"VIDEO_CLOUD_LOAD_STORAGE_NAMESPACE=$video_namespace",
		"running pod image IDs do not match the expected digest",
		"residual_namespaces",
		"residual_pvcs",
		"residual_pods",
		"residual_services",
		"deleted_cloud_volumes",
		"residual_retained_pvs",
		"residual_cloud_volumes",
		"persistentVolumeReclaimPolicy == \"Retain\"",
		"https://api.linode.com/v4/volumes/$volume_id",
		"staging deployment UID or image changed",
		"rtk-cloud-run-id=$run_id",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("cleanup gate missing %q", required)
		}
	}
}

func TestRuntimeSnapshotHandlesKubernetesPayloadBeyondArgumentLimit(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	deployments := filepath.Join(root, "deployments.json")
	pods := filepath.Join(root, "pods.json")
	if err := os.WriteFile(deployments, []byte(`{"items":[{"metadata":{"namespace":"video-cloud-staging-video-cloud","name":"api","uid":"deployment-uid"},"spec":{"template":{"spec":{"containers":[{"image":"example/api:main"}]}}}}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	podItems := make([]map[string]any, 0, 18000)
	for index := 0; index < cap(podItems); index++ {
		podItems = append(podItems, map[string]any{
			"status": map[string]any{
				"containerStatuses": []map[string]string{{
					"imageID": fmt.Sprintf("docker-pullable://example/api@sha256:%064x", index),
				}},
			},
		})
	}
	podJSON, err := json.Marshal(map[string]any{"items": podItems})
	if err != nil {
		t.Fatal(err)
	}
	if len(podJSON) < 2*1024*1024 {
		t.Fatalf("fixture must exceed a typical ARG_MAX, got %d bytes", len(podJSON))
	}
	if err := os.WriteFile(pods, podJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	kubectl := filepath.Join(bin, "kubectl")
	fakeKubectl := `#!/usr/bin/env bash
case " $* " in
  *" get deployments "*) cat "$FAKE_DEPLOYMENTS" ;;
  *" get pods "*) cat "$FAKE_PODS" ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(kubectl, []byte(fakeKubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	kubeconfig := filepath.Join(root, "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", filepath.Join(workspace, "scripts", "ci", "runtime-coverage-k8s.sh"), "snapshot")
	command.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GITHUB_WORKSPACE="+root,
		"RUNTIME_COVERAGE_RUN_ID=runtime-large-snapshot",
		"RUNTIME_COVERAGE_STACK=coverage-large-snapshot",
		"KUBECONFIG="+kubeconfig,
		"FAKE_DEPLOYMENTS="+deployments,
		"FAKE_PODS="+pods,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("snapshot failed: %v\n%s", err, output)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".artifacts", "runtime-coverage", "runtime-large-snapshot", "staging-before.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Deployments  []any    `json:"deployments"`
		ImageDigests []string `json:"image_digests"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Deployments) != 1 || len(snapshot.ImageDigests) != len(podItems) {
		t.Fatalf("snapshot counts = deployments:%d digests:%d", len(snapshot.Deployments), len(snapshot.ImageDigests))
	}
}

func TestRuntimeDeploymentAnchorRejectsPodDigestMismatch(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	kubectl := filepath.Join(bin, "kubectl")
	fakeKubectl := `#!/usr/bin/env bash
case " $* " in
  *" rollout status "*) exit 0 ;;
  *" get deployment/"*)
    printf '{"spec":{"selector":{"matchLabels":{"app":"runtime"}},"template":{"spec":{"containers":[{"name":"app","image":"%s"}]}}}}\n' "$FAKE_IMAGE"
    ;;
  *" get pods "*)
    printf '{"items":[{"status":{"containerStatuses":[{"name":"app","image":"ghcr.io/example/runtime@sha256:normalized","imageID":"docker-pullable://example/runtime@sha256:wrong"}]}}]}\n'
    ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(kubectl, []byte(fakeKubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	kubeconfig := filepath.Join(root, "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "output")
	image := "ghcr.io/example/runtime:test"
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	command := exec.Command("bash", filepath.Join(workspace, "scripts", "ci", "runtime-coverage-k8s.sh"), "verify")
	command.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GITHUB_WORKSPACE="+workspace,
		"RUNTIME_COVERAGE_RUN_ID=runtime-digest-mismatch",
		"RUNTIME_COVERAGE_STACK=coverage-digest-mismatch",
		"RUNTIME_COVERAGE_OUTPUT_ROOT="+output,
		"KUBECONFIG="+kubeconfig,
		"FAKE_IMAGE="+image,
		"LKE_ACCOUNT_MANAGER_IMAGE="+image,
		"LKE_ACCOUNT_MANAGER_IMAGE_DIGEST="+digest,
		"LKE_CLOUD_ADMIN_IMAGE="+image,
		"LKE_CLOUD_ADMIN_IMAGE_DIGEST="+digest,
		"LKE_FRONTEND_IMAGE="+image,
		"LKE_FRONTEND_IMAGE_DIGEST="+digest,
		"LKE_CLOUD_LOGGER_IMAGE="+image,
		"LKE_CLOUD_LOGGER_IMAGE_DIGEST="+digest,
		"LKE_VIDEO_CLOUD_IMAGE="+image,
		"LKE_VIDEO_CLOUD_IMAGE_DIGEST="+digest,
	)
	if outputBytes, err := command.CombinedOutput(); err == nil {
		t.Fatalf("digest mismatch passed unexpectedly:\n%s", outputBytes)
	}
	raw, err := os.ReadFile(filepath.Join(output, "deployment-anchors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Status      string   `json:"status"`
		Deployments []any    `json:"deployments"`
		Errors      []string `json:"errors"`
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "FAIL" || len(report.Deployments) != 5 ||
		!strings.Contains(strings.Join(report.Errors, "\n"), "running pod image IDs do not match the expected digest") {
		t.Fatalf("deployment anchor report = %#v", report)
	}
}

func TestRuntimeFeatureEndpointsAreRunScopedAndDoNotRequireDNS(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	kubectl := filepath.Join(bin, "kubectl")
	fakeKubectl := `#!/usr/bin/env bash
case " $* " in
  *" get service/"*)
    printf '{"status":{"loadBalancer":{"ingress":[{"ip":"203.0.113.10"}]}}}\n'
    ;;
  *)
    cat >/dev/null || true
    ;;
esac
`
	if err := os.WriteFile(kubectl, []byte(fakeKubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"curl", "timeout"} {
		path := filepath.Join(bin, command)
		if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	kubeconfig := filepath.Join(root, "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeEnvRoot := filepath.Join(root, "runtime-env")
	if err := os.MkdirAll(filepath.Join(runtimeEnvRoot, "state", "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(runtimeEnvRoot, "state", "secrets", "device-client-ca-bundle.pem"),
		[]byte("test client CA bundle\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "output")
	command := exec.Command("bash", filepath.Join(workspace, "scripts", "ci", "runtime-coverage-k8s.sh"), "endpoints")
	command.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GITHUB_WORKSPACE="+workspace,
		"RUNTIME_COVERAGE_RUN_ID=runtime-endpoints",
		"RUNTIME_COVERAGE_STACK=coverage-endpoints",
		"RUNTIME_COVERAGE_OUTPUT_ROOT="+output,
		"RUNTIME_ENV_ROOT="+runtimeEnvRoot,
		"KUBECONFIG="+kubeconfig,
	)
	if outputBytes, err := command.CombinedOutput(); err != nil {
		t.Fatalf("endpoint setup failed: %v\n%s", err, outputBytes)
	}
	envRaw, err := os.ReadFile(filepath.Join(output, "feature-endpoints.env"))
	if err != nil {
		t.Fatal(err)
	}
	envText := string(envRaw)
	for _, expected := range []string{
		"HOME100K_ACCOUNT_MANAGER_BASE_URL=https://account.coverage-endpoints.invalid:18443",
		"HOME100K_VIDEO_CLOUD_PUBLIC_BASE_URL=https://video.coverage-endpoints.invalid:18443",
		"HOME100K_VIDEO_CLOUD_TOKEN_BASE_URL=https://device.video.coverage-endpoints.invalid:18443",
		"HOME100K_MQTT_ADDR=127.0.0.1:18883",
		"HOME100K_GENERATOR_HOSTS_OVERRIDE_IP=127.0.0.1",
		"HOME100K_CLOUD_LOGGER_ENDPOINT=http://127.0.0.1:18090",
		"RUNTIME_COVERAGE_HOSTNAMES=account.coverage-endpoints.invalid,video.coverage-endpoints.invalid,device.video.coverage-endpoints.invalid",
		"RUNTIME_COVERAGE_SERVER_CA=" + filepath.Join(runtimeEnvRoot, "state", "secrets", "runtime-coverage-server-ca.crt"),
		"VIDEO_CLOUD_LOAD_STORAGE_NAMESPACE=coverage-endpoints-video-cloud",
	} {
		if !strings.Contains(envText, expected+"\n") {
			t.Fatalf("feature endpoint env missing %q:\n%s", expected, envText)
		}
	}
	if strings.Contains(envText, "video-cloud-staging.realtekconnect.com") {
		t.Fatalf("feature endpoint env leaked shared staging endpoint:\n%s", envText)
	}
	reportRaw, err := os.ReadFile(filepath.Join(output, "feature-endpoints.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Status     string `json:"status"`
		DNSCreated bool   `json:"dns_created"`
		Endpoints  []any  `json:"endpoints"`
	}
	if err := json.Unmarshal(reportRaw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "PASS" || report.DNSCreated || len(report.Endpoints) != 2 {
		t.Fatalf("feature endpoint report = %#v", report)
	}
}
