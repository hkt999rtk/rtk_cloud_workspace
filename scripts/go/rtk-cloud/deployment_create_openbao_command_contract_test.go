package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This fixture reaches the public recovery command from a genuinely executed
// isolated foundation and original local identities. External providers report
// that their resources are unavailable, so qualification must publish NO-GO
// without admitting or mutating the previously accepted execution.
func openBaoCommandContractFixture(t *testing.T) (*initializedAuditContractFixture, string, string, *[]string, *sync.Mutex) {
	t.Helper()
	workspace := writeDeploymentFixture(t, "dev", "lke")
	appendFile(t, filepath.Join(workspace, "cloud_deploy/architectures/kubernetes/architecture.env"), "DEPLOYMENT_PROTECTION_PROFILE=standard\n")
	cfg, err := resolveDeploymentConfig(workspace, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	kube := map[string]any{
		"apiVersion": "v1", "kind": "Config", "current-context": "original",
		"contexts": []any{map[string]any{"name": "original", "context": map[string]any{"cluster": "original", "user": "original"}}},
		"clusters": []any{map[string]any{"name": "original", "cluster": map[string]any{"server": "https://isolated.invalid:6443", "certificate-authority-data": base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}}},
		"users":    []any{map[string]any{"name": "original", "user": map[string]any{"token": "synthetic-original-token"}}},
	}
	raw, err := yaml.Marshal(kube)
	if err != nil {
		t.Fatal(err)
	}
	f := newInitializedAuditContractFixture(t, initializedAuditContractInputs{cfg: cfg, operator: map[string]string{"LINODE_TOKEN": "isolated-provider-token", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD": "isolated-original-admin-password"}, kubeconfig: raw})
	for name, value := range map[string]string{"init.json": `{"root_token":"isolated-original-root","unseal_keys_b64":["isolated-original-share"]}`, "root-token": "isolated-original-root", "unseal-key": "isolated-original-share"} {
		if err := f.store.write(filepath.Join("openbao", name), []byte(value), false); err != nil {
			t.Fatal(err)
		}
	}
	f.pending(t, "openbao-audit-enable")
	var requests []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"reason":"isolated original provider unavailable"}]}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("RTK_CLOUD_LINODE_API_ROOT", server.URL)
	t.Setenv("RTK_CLOUD_GODADDY_API_ROOT", server.URL)
	bin := t.TempDir()
	log := filepath.Join(bin, "external-calls")
	for _, name := range []string{"git", "docker", "gh", "kubectl"} {
		path := filepath.Join(bin, name)
		body := "#!/bin/sh\nprintf '%s %s\\n' \"${0##*/}\" \"$*\" >> \"$RTK_OPENBAO_COMMAND_CALLS\"\nexit 1\n"
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("RTK_OPENBAO_COMMAND_CALLS", log)
	t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(bin, "kubectl"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return f, workspace, log, &requests, &mu
}

func TestOpenBaoCommandQualificationReportsAllGatesAndPreservesOriginalState(t *testing.T) {
	f, workspace, log, requests, mu := openBaoCommandContractFixture(t)
	before := createBootstrapStateSnapshot(t, f.store.Root)
	report := filepath.Join(f.store.Root, "deployment/reports/isolated-openbao-qualification.json")
	var out, diagnostics bytes.Buffer
	err := runEnvironmentCreateOpenBaoRecoveryContext(context.Background(), []string{
		"--workspace=" + workspace, "--environment=dev", "--parent-plan-sha256=" + f.sha, "--report=" + report,
	}, &out, &diagnostics, true)
	if err == nil || !strings.Contains(out.String(), "NO-GO environment=dev") {
		t.Fatalf("unavailable original external resources were not reported as NO-GO: err=%v output=%s diagnostics=%s", err, out.String(), diagnostics.String())
	}
	var a createFoundationRecoveryArtifact
	if err := readEnvironmentPreflightJSON(report, &a); err != nil {
		t.Fatal("complete refusal report unavailable", err)
	}
	if a.Qualification.Decision != "NO-GO" || len(a.Qualification.Gates) != 6 || a.Plan.ParentSHA != f.sha || a.Plan.FoundationSHA != f.child.SHA256() || a.Plan.ParentJournalSHA != deploymentplan.Digest(f.parent.Journal) {
		t.Fatal("report lost its original provenance or complete gate results")
	}
	for _, gate := range a.Qualification.Gates {
		if !gate.Required || gate.EvidenceSHA256 != a.Qualification.PlanSHA256 {
			t.Fatal("gate is not bound to this qualification")
		}
		if gate.ID == "deployment.openbao.inputs" && (gate.Status != "BLOCKED" || !strings.Contains(gate.Cause, "deployment.release.source")) {
			// This final check repeats source qualification only after all
			// original input hashes and identities have matched.
			t.Fatal("original input recheck did not reach the deliberately unavailable source adapter", gate.Cause)
		}
	}
	if _, err := os.Lstat(filepath.Join(f.store.Root, "deployment/create/openbao-recovery", a.Qualification.PlanSHA256, "qualification.json")); !os.IsNotExist(err) {
		t.Fatal("NO-GO published an executable qualification")
	}
	after := createBootstrapStateSnapshot(t, f.store.Root)
	for path, value := range before {
		if path == "deployment" {
			continue
		}
		if after[path] != value {
			t.Fatal("qualification changed an original identity, owner or journal", path)
		}
	}
	mu.Lock()
	calls := append([]string(nil), (*requests)...)
	mu.Unlock()
	if len(calls) == 0 {
		t.Fatal("qualification did not reach the real provider read adapter")
	}
	for _, call := range calls {
		if !strings.HasPrefix(call, "GET ") {
			t.Fatal("qualification mutated provider resources", call)
		}
	}
	raw, err := os.ReadFile(log)
	if err != nil || !bytes.Contains(raw, []byte("kubectl")) {
		t.Fatal("qualification omitted Kubernetes availability check", err)
	}
	for _, forbidden := range []string{" apply ", " create ", " patch ", " delete ", "operator init", "isolated-original-root", "isolated-provider-token", "synthetic-original-token", "isolated-original-admin-password"} {
		if bytes.Contains(raw, []byte(forbidden)) || strings.Contains(out.String()+diagnostics.String(), forbidden) {
			t.Fatal("read-only qualification mutated resources or exposed private inputs", forbidden)
		}
	}
	lock, err := deploymentplan.LockExecutionOwner(filepath.Join(f.store.Root, "deployment/.execution-active"), false)
	if err != nil {
		t.Fatal("qualification retained the process lock", err)
	}
	_ = lock.Close()
}

func TestOpenBaoCommandRejectsUnreviewedExecutionAndChangedOriginalInputs(t *testing.T) {
	for _, mode := range []string{"unreviewed-execution", "changed-operator", "changed-configuration", "missing-identity", "missing-runtime", "foreign-runtime-inventory", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f, workspace, _, requests, mu := openBaoCommandContractFixture(t)
			args := []string{"--workspace=" + workspace, "--environment=dev", "--parent-plan-sha256=" + f.sha}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "unreviewed-execution":
				args = append(args, "--execute", "--confirm=video-cloud-dev", "--review-sha256="+strings.Repeat("a", 64))
			case "changed-operator":
				if err := f.store.write("operator/env/LINODE_TOKEN", []byte("different-isolated-provider"), true); err != nil {
					t.Fatal(err)
				}
			case "changed-configuration":
				appendFile(t, filepath.Join(workspace, "cloud_env/dev/environment.env"), "CLOUD_DNS_ROOT_DOMAIN=changed.test\n")
			case "missing-identity":
				if err := os.Remove(filepath.Join(f.store.Root, "pki/account-manager-jwt/access.key")); err != nil {
					t.Fatal(err)
				}
			case "missing-runtime":
				if err := os.Remove(filepath.Join(f.store.Root, "runtime/postgres")); err != nil {
					t.Fatal(err)
				}
			case "foreign-runtime-inventory":
				var inventory secretInventory
				if err := readExecutionJSON(filepath.Join(f.store.Root, "inventory.json"), &inventory); err != nil {
					t.Fatal(err)
				}
				inventory.Environment = "staging"
				f.write(t, "inventory.json", inventory)
			case "cancelled":
				cancel()
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			var out, diagnostics bytes.Buffer
			if err := runEnvironmentCreateOpenBaoRecoveryContext(ctx, args, &out, &diagnostics, true); err == nil {
				t.Fatal("unqualified execution was admitted")
			}
			after := createBootstrapStateSnapshot(t, f.store.Root)
			for path, value := range before {
				if after[path] != value {
					t.Fatal("refused execution changed original state", path)
				}
			}
			mu.Lock()
			none := reflect.DeepEqual(*requests, []string(nil))
			mu.Unlock()
			if !none {
				t.Fatal("unqualified execution reached the provider")
			}
			if mode == "missing-identity" {
				if _, err := os.Lstat(filepath.Join(f.store.Root, "pki/account-manager-jwt/access.key")); !os.IsNotExist(err) {
					t.Fatal("missing original signer was regenerated")
				}
			}
			if mode == "missing-runtime" {
				if _, err := os.Lstat(filepath.Join(f.store.Root, "runtime/postgres")); !os.IsNotExist(err) {
					t.Fatal("missing recorded runtime credential was regenerated")
				}
			}
		})
	}
}
