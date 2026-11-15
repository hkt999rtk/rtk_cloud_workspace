package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const isolatedResumeChildEnv = "RTK_TEST_ISOLATED_FIRST_RESUME_CHILD"

func runIsolatedOpenBaoFirstResumeSubprocess(t *testing.T, env []string, profile, workspace, environmentRoot, configRoot, parent, proof, checker string) error {
	t.Helper()
	if !isolatedScriptTempPath(workspace) || !isolatedScriptTempPath(environmentRoot) || !strings.Contains(profile, "(deny network-outbound)") || !environmentPreflightDigest.MatchString(parent) || !environmentPreflightDigest.MatchString(proof) || !environmentPreflightDigest.MatchString(checker) {
		return errors.New("isolated same-parent resume inputs unavailable")
	}
	self, err := os.Executable()
	if err != nil || !filepath.IsAbs(self) {
		return errors.New("same checker binary unavailable")
	}
	childEnv, err := isolatedResumeChildEnvironment(t, env, workspace, environmentRoot, configRoot, parent, proof, checker)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(t.Context(), 9*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-p", profile, self, "-test.run", "^TestIsolatedOpenBaoFirstResumeChild$", "-test.timeout=8m", "-test.v")
	cmd.Env = childEnv
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
	log, err := os.CreateTemp("", "rtk-isolated-first-resume-*.log")
	if err != nil {
		return errors.New("isolated resume log unavailable")
	}
	if err := log.Chmod(0600); err != nil {
		_ = log.Close()
		return errors.New("isolated resume log permissions unavailable")
	}
	t.Log("same-parent isolated resume private log:", log.Name())
	cmd.Stdout, cmd.Stderr = log, log
	runErr := cmd.Run()
	closeErr := log.Close()
	if runErr != nil || closeErr != nil || ctx.Err() != nil {
		return errors.New("isolated resume subprocess failed")
	}
	return nil
}

// Bind the child to the same validated fixture environment. TestMain invokes
// `go env GOMODCACHE` and clears CONFIG_ROOT, so provide a narrowly scoped
// private shim and a separately checked copy of the original root.
func isolatedResumeChildEnvironment(t *testing.T, env []string, workspace, environmentRoot, configRoot, parent, proof, checker string) ([]string, error) {
	t.Helper()
	values := make(map[string]string)
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			return nil, errors.New("invalid isolated subprocess environment")
		}
		if _, exists := values[key]; exists {
			return nil, errors.New("ambiguous isolated subprocess environment")
		}
		values[key] = value
		if strings.HasPrefix(key, "RTK_TEST_ISOLATED_RESUME_") || key == isolatedResumeChildEnv {
			return nil, errors.New("ambiguous isolated resume environment")
		}
	}
	cache, root, path := values["GOMODCACHE"], values["RTK_CLOUD_CONFIG_ROOT"], values["PATH"]
	if cache == "" || !filepath.IsAbs(cache) || cache != os.Getenv("GOMODCACHE") || !isolatedScriptTempPath(root) || root != configRoot || path == "" || values["RTK_CLOUD_TEST_MODE"] != "1" {
		return nil, errors.New("isolated original cache or store unavailable")
	}
	dir := t.TempDir()
	if !isolatedScriptTempPath(dir) {
		return nil, errors.New("isolated go shim directory unavailable")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, errors.New("isolated go shim directory unavailable")
	}
	shim := "#!/bin/sh\n[ \"$#\" -eq 2 ] && [ \"$1\" = env ] && [ \"$2\" = GOMODCACHE ] || exit 70\nprintf '%s\\n' \"$GOMODCACHE\"\n"
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(shim), 0700); err != nil {
		return nil, errors.New("isolated go shim unavailable")
	}
	child := make([]string, 0, len(env)+7)
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		if key == isolatedCustodyStopEnv || key == "RTK_FIRST_BAO_READ_ONLY" || key == "PATH" {
			continue
		}
		child = append(child, item)
	}
	child = append(child, "PATH="+dir+":"+path, isolatedResumeChildEnv+"=1", "RTK_TEST_ISOLATED_RESUME_CONFIG_ROOT="+root, "RTK_TEST_ISOLATED_RESUME_WORKSPACE="+workspace, "RTK_TEST_ISOLATED_RESUME_ENV_ROOT="+environmentRoot, "RTK_TEST_ISOLATED_RESUME_PARENT="+parent, "RTK_TEST_ISOLATED_RESUME_PROOF="+proof, "RTK_TEST_ISOLATED_RESUME_CHECKER="+checker)
	return child, nil
}

func TestIsolatedOpenBaoFirstResumeChild(t *testing.T) {
	if os.Getenv(isolatedResumeChildEnv) != "1" {
		t.Skip("explicit same-parent isolated resume child required")
	}
	workspace, environmentRoot := os.Getenv("RTK_TEST_ISOLATED_RESUME_WORKSPACE"), os.Getenv("RTK_TEST_ISOLATED_RESUME_ENV_ROOT")
	parent, proof, checker := os.Getenv("RTK_TEST_ISOLATED_RESUME_PARENT"), os.Getenv("RTK_TEST_ISOLATED_RESUME_PROOF"), os.Getenv("RTK_TEST_ISOLATED_RESUME_CHECKER")
	if os.Getenv(isolatedCustodyStopEnv) != "" || os.Getenv("RTK_FIRST_BAO_READ_ONLY") != "" || os.Getenv("RTK_CLOUD_TEST_MODE") != "1" || !isolatedScriptTempPath(workspace) || !isolatedScriptTempPath(environmentRoot) || !environmentPreflightDigest.MatchString(parent) || !environmentPreflightDigest.MatchString(proof) || !environmentPreflightDigest.MatchString(checker) {
		t.Fatal("isolated resume child inputs are not bound to the fixture")
	}
	configRoot := os.Getenv("RTK_TEST_ISOLATED_RESUME_CONFIG_ROOT")
	if !isolatedScriptTempPath(configRoot) || os.Getenv("RTK_CLOUD_CONFIG_ROOT") != "" {
		t.Fatal("original isolated configuration root unavailable")
	}
	if err := os.Setenv("RTK_CLOUD_CONFIG_ROOT", configRoot); err != nil {
		t.Fatal("original isolated configuration root unavailable")
	}
	actualChecker, err := environmentPreflightCheckerSHA256()
	if err != nil || actualChecker != checker {
		t.Fatal("resume child differs from the proof checker")
	}
	cfg, err := resolveDeploymentConfig(workspace, "dev", environmentRoot)
	if err != nil {
		t.Fatal("isolated selected environment unavailable")
	}
	store, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil || !isolatedScriptTempPath(store.Root) {
		t.Fatal("isolated selected operator unavailable")
	}
	checkpoint, err := inspectCreateRecoveryCheckpoint(cfg, store)
	if err != nil || checkpoint.Profile != "openbao-first-foundation" || checkpoint.Journal.PlanSHA256 != parent {
		t.Fatal("original isolated parent checkpoint changed")
	}
	e, err := openCreateExecutionForPublication(t.Context(), store, checkpoint)
	if err != nil {
		t.Fatal("original isolated parent lock unavailable")
	}
	defer e.Close()
	owner := deploymentplan.Digest(e.owner)
	kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil {
		t.Fatal("original isolated Kubernetes identity unavailable")
	}
	backend, err := newEnvironmentKubernetesBackend(string(kube), cfg.Environment)
	if err != nil || backend.Store != store {
		t.Fatal("original isolated Kubernetes backend unavailable")
	}
	backend.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	if err != nil {
		t.Fatal("selected rollout timeout unavailable")
	}
	_, cleanup, err := resumeOpenBaoFirstInitialized(t.Context(), cfg, store, e, backend, operator, proof, io.Discard)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		var blocker *deploymentplan.Blocker
		if errors.As(err, &blocker) {
			t.Log("resume stopped at sanitized blocker:", isolatedResumeBlockerClass(blocker))
			t.Log("resume blocker cause SHA-256:", deploymentplan.Digest(blocker.Cause))
		}
		for _, entry := range e.Journal.Entries {
			key := entry.Key
			if strings.HasPrefix(key, "kubernetes/custody-transport-trust-") {
				key = "kubernetes/custody-transport-trust"
			}
			if (key == "kubernetes/foundation" || key == "kubernetes/foundation-services" || key == "kubernetes/custody-transport-trust" || key == "workload-custody-initialize" || key == "kubernetes/pki-migrate" || key == "kubernetes/pki-grants") && (entry.State == "pending" || entry.State == "allocated" || entry.State == "completed") {
				t.Log("resume original stage:", key, entry.State)
			}
		}
		plan, planErr := readEnvironmentCreateStageInput(store, e, "pki-migrate")
		if planErr == nil && plan != nil {
			journal, exists, readErr := readEnvironmentCreateStageProgress(store, e, "pki-migrate", plan)
			if readErr != nil {
				t.Log("migration child: unreadable")
			} else if !exists {
				t.Log("migration child: not-started")
			} else {
				failed := "other"
				switch {
				case journal.Failed == "":
					failed = "none"
				case strings.HasPrefix(journal.Failed, "Job/"):
					failed = "job"
				case strings.HasPrefix(journal.Failed, "Secret/"):
					failed = "secret"
				case strings.HasPrefix(journal.Failed, "ConfigMap/"):
					failed = "configmap"
				}
				t.Log("migration child: started", "failed-kind", failed, "completed-count", len(journal.Completed), "in-flight", journal.InFlight != "", "acceptance-started", journal.AcceptanceStarted)
			}
		} else {
			t.Log("migration child: original input unavailable")
		}
		t.Fatal("original same-parent bootstrap resume did not complete")
	}
	if e.Journal.PlanSHA256 != parent || deploymentplan.Digest(e.owner) != owner || e.Check(t.Context()) != nil {
		t.Fatal("original execution ownership changed during resume")
	}
	t.Log("original same-parent initialized OpenBao bootstrap resumed; full deployment acceptance is separate")
}

// Never expose arbitrary failure details from a child process. Only exact
// constant product causes are mapped to fixed classifications.
func isolatedResumeBlockerClass(b *deploymentplan.Blocker) string {
	if b == nil {
		return "other"
	}
	if b.ID != "deployment.bootstrap.pki" {
		switch b.ID {
		case "deployment.access", "deployment.access.resource", "deployment.admission", "deployment.drift", "deployment.execution", "deployment.execution.lease", "deployment.execution.cleanup", "deployment.inputs", "deployment.ownership", "deployment.post-deploy", "deployment.post-deploy.workloads", "deployment.recovery", "deployment.runtime.dependency", "deployment.storage.binding", "deployment.create.inputs", "deployment.create.stage":
			return b.ID
		default:
			return "other"
		}
	}
	switch b.Cause {
	case "stage changed its qualified original input or definitions":
		return "original-stage-inputs-changed"
	case "custody trust publication requires the original admitted create executor":
		return "custody-trust-executor"
	case "custody trust requires the original bootstrap generation reference":
		return "custody-trust-generation"
	case "custody trust must use the selected generation's original OpenBao foundation authority":
		return "custody-trust-authority"
	case "recorded custody trust does not belong to the original environment and foundation authority":
		return "custody-trust-recorded-authority"
	case "original stage input scope unavailable", "original immutable stage input changed", "original compiled stage input unavailable", "original compiled stage input changed", "stage input is outside selected original execution":
		return "original-stage-input-unavailable"
	case "original stage progress unavailable", "child execution has no original parent request", "original stage request changed", "native stage receipt has no child journal", "native stage lease has no child journal", "original child journal has no recorded native lease", "original parent receipt differs from accepted child", "accepted stage parent receipt changed":
		return "original-stage-journal-unavailable"
	case "custody initialization requires its original admitted create owner and workload inventory":
		return "custody-owner"
	case "recorded custody initialization differs from the original selected scope":
		return "custody-intent"
	case "original custody completion receipt is inconsistent":
		return "custody-receipt"
	case "original OpenBao custody configuration credential is unavailable":
		return "custody-credential"
	case "original OpenBao custody mount inventory cannot be read":
		return "custody-mount-inventory"
	case "selected custody mount already exists with another storage type; replacement is prohibited":
		return "custody-mount-type"
	case "completed generation custody mount is missing; no identity is regenerated":
		return "custody-mount-missing"
	case "native custody configuration readback differs from its exact original mount, policy or workload role; no completion is recorded":
		return "custody-configuration-readback"
	case "custody inventory requires its original qualified environment and create generation":
		return "custody-generation"
	case "workload storage inventory requires its selected environment intent":
		return "custody-environment"
	case "custody product owners differ from maintained environment intent":
		return "custody-product-owners"
	case "custody product owner differs from its exact maintained identity recipe":
		return "custody-product-recipe"
	case "native custody workload has conflicting namespace or service-account owners":
		return "custody-workload-conflict"
	case "native custody storage alias is duplicated":
		return "custody-alias-conflict"
	case "original MQTT custody server is outside the frozen DNS policy":
		return "custody-mqtt-dns"
	case "maintained product identity has no native workload custody owner":
		return "custody-workload-owner"
	default:
		return "bootstrap-other"
	}
}

func TestIsolatedResumeBlockerClassRejectsDynamicData(t *testing.T) {
	if got := isolatedResumeBlockerClass(&deploymentplan.Blocker{ID: "deployment.bootstrap.pki", Cause: "original OpenBao custody mount inventory cannot be read"}); got != "custody-mount-inventory" {
		t.Fatalf("known classification = %q", got)
	}
	for _, b := range []*deploymentplan.Blocker{nil, {ID: "foreign", Cause: "original OpenBao custody mount inventory cannot be read"}, {ID: "deployment.bootstrap.pki", Cause: "original OpenBao custody mount inventory cannot be read secret-sentinel"}, {ID: "deployment.bootstrap.pki", Cause: "secret-sentinel"}} {
		got := isolatedResumeBlockerClass(b)
		if got != "other" && got != "bootstrap-other" {
			t.Fatal("dynamic cause was exposed")
		}
	}
}

// Exercise the real package TestMain, including its go lookup and clearing of
// CONFIG_ROOT, without needing a deployment or any external process.
func TestIsolatedResumeEnvironmentUsesOriginalCacheAndStore(t *testing.T) {
	root := t.TempDir()
	cache := os.Getenv("GOMODCACHE")
	env := []string{"PATH=/usr/bin:/bin", "GOMODCACHE=" + cache, "RTK_CLOUD_CONFIG_ROOT=" + root, "RTK_CLOUD_TEST_MODE=1", "GOPROXY=off", "TMPDIR=" + os.TempDir(), "RTK_RESUME_ENV_PROBE=1", isolatedCustodyStopEnv + "=1", "RTK_FIRST_BAO_READ_ONLY=1"}
	child, err := isolatedResumeChildEnvironment(t, env, root, root, root, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run", "^TestIsolatedResumeEnvironmentProbe$", "-test.v")
	cmd.Env = child
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("real TestMain rejected isolated child: %s: %v", out, err)
	}
	for _, bad := range [][]string{
		append(append([]string{}, env...), "GOMODCACHE="+cache),
		append(append([]string{}, env...), "RTK_TEST_ISOLATED_RESUME_CONFIG_ROOT="+root),
		{"PATH=/usr/bin:/bin", "GOMODCACHE=/foreign", "RTK_CLOUD_CONFIG_ROOT=" + root, "RTK_CLOUD_TEST_MODE=1"},
		{"PATH=/usr/bin:/bin", "GOMODCACHE=" + cache, "RTK_CLOUD_CONFIG_ROOT=" + t.TempDir(), "RTK_CLOUD_TEST_MODE=1"},
	} {
		if _, err := isolatedResumeChildEnvironment(t, bad, root, root, root, "", "", ""); err == nil {
			t.Fatal("foreign or ambiguous child environment accepted")
		}
	}
}

func TestIsolatedResumeEnvironmentProbe(t *testing.T) {
	if os.Getenv("RTK_RESUME_ENV_PROBE") != "1" {
		t.Skip("isolated environment probe only")
	}
	if os.Getenv("RTK_CLOUD_CONFIG_ROOT") != "" || !isolatedScriptTempPath(os.Getenv("RTK_TEST_ISOLATED_RESUME_CONFIG_ROOT")) || os.Getenv(isolatedCustodyStopEnv) != "" || os.Getenv("RTK_FIRST_BAO_READ_ONLY") != "" {
		t.Fatal("TestMain did not preserve the original restricted child inputs")
	}
	out, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil || strings.TrimSpace(string(out)) != os.Getenv("GOMODCACHE") {
		t.Fatal("original cache unavailable")
	}
	if err := exec.Command("go", "version").Run(); err == nil {
		t.Fatal("isolated go shim accepted another operation")
	}
}
