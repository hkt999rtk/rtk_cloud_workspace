package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestProviderCLIExecutionContractReportsQualificationWithoutReplayingOriginal(t *testing.T) {
	for _, mode := range []string{"original", "completed-canaries", "wrong-confirmation", "wrong-review", "storage-key-execute", "storage-key-review", "storage-key-wrong-stack"} {
		t.Run(mode, func(t *testing.T) {
			workspace := writeDeploymentFixture(t, "dev", "lke")
			appendFile(t, filepath.Join(workspace, "cloud_deploy/architectures/kubernetes/architecture.env"), "DEPLOYMENT_PROTECTION_PROFILE=standard\n")
			t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
			cfg, err := resolveDeploymentConfig(workspace, "dev", "")
			if err != nil {
				t.Fatal(err)
			}
			operator := map[string]string{"LINODE_TOKEN": "isolated-provider-token"}
			cfg, store, execution, _, plan := providerExecutionContractFixture(t, providerExecutionContractInputs{cfg: cfg, operator: operator})
			if mode == "completed-canaries" {
				execution, _ = providerExecutionContractFork(t, execution, plan, "")
				providerExecutionContractCanaries(t, cfg, execution)
			}
			if err := execution.Close(); err != nil {
				t.Fatal(err)
			}
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method+" "+r.URL.Path)
				if r.Method != http.MethodGet {
					t.Error("provider qualification mutated a native allocation")
				}
				http.Error(w, "isolated unavailable original allocation", http.StatusNotFound)
			}))
			t.Cleanup(server.Close)
			t.Setenv("RTK_CLOUD_LINODE_API_ROOT", server.URL)
			t.Setenv("RTK_CLOUD_GODADDY_API_ROOT", server.URL)
			bin := t.TempDir()
			calls := filepath.Join(bin, "calls")
			for _, name := range []string{"git", "docker", "gh", "kubectl"} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nprintf '%s %s\\n' \"${0##*/}\" \"$*\" >> \"$RTK_PROVIDER_CLI_CALLS\"\nexit 1\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(bin, "kubectl"))
			t.Setenv("RTK_PROVIDER_CLI_CALLS", calls)
			report := filepath.Join(store.Root, "deployment/reports/provider-qualification.json")
			args := []string{"--environment=dev", "--original-plan-sha256=" + execution.Journal.PlanSHA256, "--report=" + report}
			qualification := mode == "original" || mode == "completed-canaries"
			switch mode {
			case "completed-canaries":
				args = append(args, "--after-storage-canary")
			case "wrong-confirmation":
				args = append(args, "--execute", "--confirm=foreign", "--review-sha256="+deploymentplan.Digest("foreign"))
			case "wrong-review":
				args = append(args, "--execute", "--confirm="+cfg.Values["CLOUD_STACK_NAME"], "--review-sha256="+deploymentplan.Digest("foreign"))
			case "storage-key-execute":
				args = append(args, "--establish-storage-keys", "--execute", "--confirm="+cfg.Values["CLOUD_STACK_NAME"])
			case "storage-key-review":
				args = append(args, "--establish-storage-keys", "--confirm="+cfg.Values["CLOUD_STACK_NAME"], "--review-sha256="+deploymentplan.Digest("unreviewed"))
			case "storage-key-wrong-stack":
				args = append(args, "--establish-storage-keys", "--confirm=foreign")
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			if err := runEnvironmentCreateProviderRecovery(args); err == nil {
				t.Fatal("unavailable or unreviewed recovery was admitted")
			}
			after := createBootstrapStateSnapshot(t, store.Root)
			if qualification {
				var artifact environmentCreateProviderRecoveryArtifact
				if err := readEnvironmentPreflightJSON(report, &artifact); err != nil {
					t.Fatal("qualification omitted complete private report", err)
				}
				if artifact.Qualification.Decision != "NO-GO" || len(artifact.Qualification.Gates) != 6 || artifact.Plan.OriginalPlanSHA256 != execution.Journal.PlanSHA256 {
					t.Fatal("qualification report omitted original source or required checks")
				}
				for i, id := range []string{"deployment.recovery.target", "deployment.recovery.release", "deployment.recovery.storage", "deployment.recovery.bootstrap", "deployment.recovery.network", "deployment.recovery.inputs"} {
					gate := artifact.Qualification.Gates[i]
					if gate.ID != id || !gate.Required || gate.EvidenceSHA256 != artifact.Qualification.PlanSHA256 || gate.Status != "BLOCKED" {
						t.Fatal("qualification stopped at the first failure or lost gate evidence", gate)
					}
				}
				raw, _ := json.Marshal(artifact)
				if strings.Contains(string(raw), operator["LINODE_TOKEN"]) {
					t.Fatal("private credential entered qualification report")
				}
				relative, _ := filepath.Rel(store.Root, report)
				delete(after, relative)
				if len(requests) == 0 {
					t.Fatal("qualification omitted original provider inspection")
				}
			} else if len(requests) != 0 {
				t.Fatal("invalid execution review reached a native provider")
			}
			// The command may prepare its private output directory before it
			// rejects a review. Empty directories convey no execution authority.
			for path := range after {
				if _, existed := before[path]; existed {
					continue
				}
				parent := filepath.Dir(path)
				allowed := path == "deployment/create/recovery" || parent == "deployment/create/recovery" && environmentPreflightDigest.MatchString(filepath.Base(path)) || qualification && path == "deployment/reports"
				info, err := os.Lstat(filepath.Join(store.Root, path))
				if allowed && err == nil && info.IsDir() && info.Mode().Perm() == 0700 {
					delete(after, path)
				}
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("refused recovery changed original state or persisted executable qualification")
			}
			lock, err := deploymentplan.LockExecutionOwner(filepath.Join(store.Root, "deployment/.execution-active"), false)
			if err != nil {
				t.Fatal("refused command leaked the original process lock", err)
			}
			_ = lock.Close()
		})
	}
}
