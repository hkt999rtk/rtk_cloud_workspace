package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Continue only the exact GO produced by the immediately preceding real shell
// preflight in the same synthetic store. Provider, DNS, K8s and CSI are explicit
// test fixtures; this helper never creates an owner, journal, or receipt itself.
func runIsolatedFreshCreateAfterGO(t *testing.T, store secretStore, cfg deploymentConfig, root, workspace, environmentRoot, profile string, env []string, output []byte, preflightErr error, kubeconfig []byte, provider *isolatedFreshProvider) {
	t.Helper()
	if os.Getenv("RTK_TEST_FRESH_CREATE") != "1" {
		return
	}
	if preflightErr != nil || provider == nil || cfg.Environment != "dev" || store.Environment != cfg.Environment || cfg.Values["CLOUD_STACK_NAME"] != "video-cloud-dev" || !isolatedScriptTempPath(workspace) || !isolatedScriptTempPath(environmentRoot) || !isolatedScriptTempPath(store.Root) || !isolatedScriptTempPath(store.ConfigRoot) {
		t.Fatal("fresh create requires the same synthetic dev store and a successful real preflight")
	}
	if !strings.Contains(string(output), "\nGO environment=dev operation=create scope=environment-bootstrap completed=false") || strings.Contains(string(output), "NO-GO environment=dev operation=create") {
		t.Fatal("fresh create requires an actual complete GO from this preflight invocation")
	}
	matches := regexp.MustCompile(`(?m)^plan-sha256: ([a-f0-9]{64})$`).FindAllSubmatch(output, -1)
	if len(matches) != 1 {
		t.Fatal("fresh preflight did not report one exact plan SHA")
	}
	sha := string(matches[0][1])
	active, err := readEnvironmentPreflightActive(store)
	if err != nil || active.Decision != "GO" || active.Environment != cfg.Environment || active.PlanSHA256 != sha {
		t.Fatal("fresh preflight GO does not match the same store's active pointer", err)
	}
	var createActive environmentPreflightActive
	if err = readEnvironmentPreflightJSON(filepath.Join(store.Root, "deployment/create/preflight/active.json"), &createActive); err != nil || deploymentplan.Digest(createActive) != deploymentplan.Digest(active) {
		t.Fatal("fresh create active pointer differs", err)
	}
	dir := filepath.Join(store.Root, "deployment/create/preflight", filepath.FromSlash(active.Artifact))
	if err = validateEnvironmentPreflightDirectories(store, dir); err != nil {
		t.Fatal("unsafe isolated GO artifact directory", err)
	}
	var artifact environmentCreateArtifact
	if err = readEnvironmentPreflightJSON(filepath.Join(dir, "artifact.json"), &artifact); err != nil {
		t.Fatal("isolated GO artifact unavailable", err)
	}
	planSHA, err := environmentCreatePlanSHA256(artifact.Plan)
	if err != nil || artifact.Environment != cfg.Environment || artifact.Stack != cfg.Values["CLOUD_STACK_NAME"] || artifact.Generation != active.Generation || deploymentplan.Digest(artifact) != active.ProofSHA256 || artifact.PlanSHA256 != sha || planSHA != sha || artifact.Qualification.PlanSHA256 != sha || artifact.Qualification.Decision != "GO" || !environmentCreateQualificationGatesValid(artifact.Qualification, environmentCreateRequiredGates) || !time.Now().UTC().Before(artifact.ValidUntil) {
		t.Fatal("fresh GO artifact is incomplete, expired or differs from its reported plan")
	}
	var selected environmentCreatePlan
	if os.Getenv("RTK_TEST_OPENBAO_FIRST") == "1" {
		if decodeEnvironmentJSON(artifact.Plan, &selected) != nil || !selected.OpenBaoFirst || selected.Environment != cfg.Environment || selected.Stack != cfg.Values["CLOUD_STACK_NAME"] {
			t.Fatal("same-GO artifact does not select the isolated OpenBao-first generation")
		}
	}
	if _, err = os.Lstat(filepath.Join(store.Root, "deployment/.execution-active")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fresh synthetic create already has an execution owner")
	}
	if !filepath.IsAbs(root) || profile == "" || !strings.Contains(profile, "(deny network-outbound)") {
		t.Fatal("fresh create sandbox inputs unavailable")
	}
	expectedEnv := map[string]string{"RTK_CLOUD_CONFIG_ROOT": store.ConfigRoot, "RTK_CLOUD_TEST_MODE": "1"}
	for key, want := range expectedEnv {
		count := 0
		for _, item := range env {
			if strings.HasPrefix(item, key+"=") {
				count++
				if item != key+"="+want {
					t.Fatal("fresh create environment changed", key)
				}
			}
		}
		if count != 1 {
			t.Fatal("fresh create synthetic environment missing or ambiguous", key)
		}
	}
	var kubectl string
	for _, item := range env {
		if strings.HasPrefix(item, "RTK_CLOUD_KUBECTL=") {
			if kubectl != "" {
				t.Fatal("ambiguous isolated kubectl")
			}
			kubectl = strings.TrimPrefix(item, "RTK_CLOUD_KUBECTL=")
		}
	}
	if !isolatedScriptTempPath(kubectl) || filepath.Base(kubectl) != "kubectl" {
		t.Fatal("isolated kubectl path is unavailable")
	}
	vars, kubeDir := installIsolatedFreshStatefulKube(t, store, kubeconfig, filepath.Dir(kubectl))
	var controller *isolatedOpenBaoFirstController
	if os.Getenv("RTK_TEST_OPENBAO_FIRST") == "1" {
		controller = newIsolatedOpenBaoFirstController(t, cfg, store, kubeDir)
		for key, value := range installIsolatedOpenBaoFirstKubectl(t, filepath.Dir(kubectl), vars["RTK_FRESH_EXPECTED_KUBECONFIG"], controller) {
			if _, exists := vars[key]; exists {
				t.Fatal("ambiguous isolated OpenBao fixture environment", key)
			}
			vars[key] = value
		}
		host, port, err := net.SplitHostPort(controller.listener.Addr().String())
		if err != nil || host != "127.0.0.1" || port == "" {
			t.Fatal("isolated OpenBao loopback endpoint unavailable", err)
		}
		profile += `(allow network-outbound (remote ip "localhost:` + port + `"))`
	}
	for k, v := range vars {
		for _, item := range env {
			if strings.HasPrefix(item, k+"=") {
				t.Fatal("ambiguous isolated Kubernetes fixture environment", k)
			}
		}
		env = append(env, k+"="+v)
	}
	env, jobPort, jobController := installIsolatedFreshFoundationMigration(t, store, cfg, selected, controller, kubeDir, kubectl, vars["RTK_FRESH_EXPECTED_KUBECONFIG"], env)
	if jobPort != 0 {
		profile += `(allow network-outbound (remote ip "localhost:` + strconv.Itoa(jobPort) + `"))`
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	script := filepath.Join(root, "scripts/deploy-environment.sh")
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-p", profile, "/bin/bash", script, "create", "--environment", cfg.Environment, "--confirm", cfg.Values["CLOUD_STACK_NAME"], "--plan-sha256", sha, "--workspace", workspace, "--environment-root", environmentRoot, "--timeout", "45m")
	cmd.Env = append([]string(nil), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
	log, err := os.CreateTemp("/private/tmp", "rtk-isolated-fresh-create-*.log")
	if err != nil {
		t.Fatal(err)
	}
	if err = log.Chmod(0600); err != nil {
		log.Close()
		t.Fatal("cannot protect isolated create log", err)
	}
	t.Log("same-GO isolated formal create private log:", log.Name(), "plan:", sha)
	var captured bytes.Buffer
	outputWriter := io.MultiWriter(log, &captured)
	cmd.Stdout, cmd.Stderr = outputWriter, outputWriter
	runErr := cmd.Run()
	if os.Getenv(isolatedCustodyStopEnv) == "1" && runErr == nil {
		t.Error("isolated create did not stop at the required before-custody boundary")
	}
	if err = log.Close(); err != nil {
		t.Fatal("cannot preserve isolated create log", err)
	}
	if runErr != nil || ctx.Err() != nil {
		retained := retainIsolatedFreshFailure(t, store, kubeDir)
		if err := os.WriteFile(filepath.Join(retained, "provider-requests.log"), provider.safeRequestLog(), 0600); err != nil {
			t.Fatal("cannot preserve isolated provider observations", err)
		}
		t.Log("isolated create original journal and simulated Kubernetes evidence:", retained)
		if os.Getenv("RTK_TEST_OPENBAO_FIRST_PROOF") == "1" {
			t.Run("same-parent-checkpoint-evidence", func(t *testing.T) {
				runIsolatedOpenBaoFirstCheckpointProof(t, store, cfg, env, provider, vars, kubectl, kubeDir, controller, jobController, profile, workspace, environmentRoot, captured.String())
			})
		}
	}
	if ctx.Err() != nil {
		t.Fatal("isolated formal create timed out; inspect private log", ctx.Err())
	}
	text := captured.String()
	if runErr != nil {
		if !strings.Contains(text, "NO-GO environment=dev operation=create") {
			t.Fatal("isolated create failed without an explicit NO-GO; inspect private log", runErr)
		}
		t.Log("isolated formal create stopped at NO-GO; deployment remains incomplete")
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "[FAIL]") || strings.HasPrefix(line, "[BLOCKED]") || strings.HasPrefix(line, "  cause:") {
				t.Log(line)
			}
		}
		return
	}
	if !strings.Contains(text, "GO environment=dev operation=create") {
		t.Fatal("isolated create returned without a final deployment decision")
	}
	t.Log("isolated create returned GO; separate health acceptance still required")
}

// Retain only allowlisted synthetic stage and resource status metadata. Never
// copy raw plans, receipts, material, credentials or Kubernetes object bodies.
func retainIsolatedFreshFailure(t *testing.T, store secretStore, kubeDir string) string {
	t.Helper()
	if !isolatedScriptTempPath(store.Root) || !isolatedScriptTempPath(kubeDir) {
		t.Fatal("refusing to retain non-isolated state")
	}
	destination, err := os.MkdirTemp("/private/tmp", "rtk-isolated-create-evidence-")
	if err != nil {
		t.Fatal("cannot retain isolated evidence", err)
	}
	var journals []map[string]any
	root := filepath.Join(store.Root, "deployment", "create", "executions")
	if _, statErr := os.Stat(root); statErr == nil {
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || entry.Name() != "journal.json" {
				return nil
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
				return errors.New("invalid isolated journal")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var raw map[string]any
			if json.Unmarshal(data, &raw) != nil {
				return errors.New("invalid isolated journal JSON")
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			row := map[string]any{"path": rel}
			for _, key := range []string{"schema", "environment", "stack", "plan_sha256", "admitted_at", "completed_at", "failed", "in_flight", "accepted", "acceptance_started", "completed"} {
				if v, ok := raw[key]; ok {
					row[key] = v
				}
			}
			var entries []map[string]any
			for _, item := range certIssuerObjectList(raw["entries"]) {
				entry := map[string]any{}
				source := deploymentplan.Map(item)
				for _, key := range []string{"key", "phase", "state", "started_at", "completed_at"} {
					if value, ok := source[key]; ok {
						entry[key] = value
					}
				}
				entries = append(entries, entry)
			}
			row["entries"] = entries
			journals = append(journals, row)
			return nil
		})
		if err != nil {
			t.Fatal("cannot summarize isolated execution journal", err)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("cannot inspect isolated execution journals", statErr)
	}
	var objects []map[string]any
	files, err := filepath.Glob(filepath.Join(kubeDir, "*_*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
			t.Fatal("invalid isolated Kubernetes metadata")
		}
		data, err := os.ReadFile(path)
		var raw map[string]any
		if err != nil || json.Unmarshal(data, &raw) != nil {
			t.Fatal("invalid isolated Kubernetes object")
		}
		metadata := deploymentplan.Map(raw["metadata"])
		status := deploymentplan.Map(raw["status"])
		row := map[string]any{"kind": raw["kind"], "name": metadata["name"], "namespace": metadata["namespace"], "uid": metadata["uid"], "phase": status["phase"]}
		var conditions []map[string]any
		for _, condition := range certIssuerObjectList(status["conditions"]) {
			value := deploymentplan.Map(condition)
			conditions = append(conditions, map[string]any{"type": value["type"], "status": value["status"]})
		}
		row["conditions"] = conditions
		objects = append(objects, row)
	}
	for name, value := range map[string]any{"journals.json": journals, "kubernetes-status.json": objects} {
		data, err := json.Marshal(value)
		if err != nil || os.WriteFile(filepath.Join(destination, name), data, 0600) != nil {
			t.Fatal("cannot retain sanitized isolated status")
		}
	}
	if calls, err := os.ReadFile(filepath.Join(kubeDir, "safe-calls.log")); err == nil {
		allowedActions := map[string]bool{"get": true, "create": true, "replace": true, "delete": true, "wait": true, "rollout": true, "auth": true, "other": true}
		allowedKinds := map[string]bool{"other": true, "namespace": true, "Namespace": true, "namespaces": true, "pod": true, "pods": true, "deployment": true, "Deployment": true, "statefulset": true, "StatefulSet": true, "clusterrole": true, "ClusterRole": true, "configmap": true, "ConfigMap": true, "storageclass": true, "StorageClass": true, "validatingwebhookconfiguration": true, environmentNamespacedInventoryKinds: true, environmentClusterInventoryKinds: true}
		if len(calls) > 1<<20 {
			t.Fatal("isolated Kubernetes call summary exceeds its bound")
		}
		for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
			parts := strings.Fields(line)
			if len(parts) != 3 || !allowedActions[parts[0]] || !allowedKinds[parts[1]] {
				t.Fatal("isolated Kubernetes call summary contains unexpected fields")
			}
			status, err := strconv.Atoi(parts[2])
			if err != nil || status < 0 || status > 255 {
				t.Fatal("isolated Kubernetes call status is invalid")
			}
		}
		if err := os.WriteFile(filepath.Join(destination, "kubernetes-calls.log"), calls, 0600); err != nil {
			t.Fatal("cannot retain isolated Kubernetes call summary", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cannot inspect isolated Kubernetes call summary", err)
	}
	return destination
}

func TestRetainIsolatedFreshFailureKeepsPrivateEvidence(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	if err := store.write("deployment/create/executions/test/journal.json", []byte(`{"entries":[{"key":"stage","state":"pending","receipt":{"key":"PRIVATE_SENTINEL"}}],"owner_token":"PRIVATE_SENTINEL"}`), false); err != nil {
		t.Fatal(err)
	}
	kubeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(kubeDir, "Secret_ns_name.json"), []byte(`{"kind":"Secret","metadata":{"name":"name","namespace":"ns","uid":"owner"},"data":{"key":"PRIVATE_SENTINEL"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kubeDir, "safe-calls.log"), []byte("get namespace 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	retained := retainIsolatedFreshFailure(t, store, kubeDir)
	t.Cleanup(func() { _ = os.RemoveAll(retained) })
	info, err := os.Stat(retained)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("retained synthetic root must be private", err)
	}
	for path, expected := range map[string]string{"journals.json": "stage", "kubernetes-status.json": "owner", "kubernetes-calls.log": "get namespace 0"} {
		path = filepath.Join(retained, path)
		data, err := os.ReadFile(path)
		info, statErr := os.Stat(path)
		if err != nil || statErr != nil || !strings.Contains(string(data), expected) || strings.Contains(string(data), "PRIVATE_SENTINEL") || info.Mode().Perm() != 0600 {
			t.Fatal("private isolated evidence does not match", err, statErr)
		}
	}
}
