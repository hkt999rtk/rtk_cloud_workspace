package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Public commands emit private measurements even when they refuse admission.
// Every original byte/mode remains exact; the only allowed additions are the
// command's closed measurement stream and its canonical private directory.
func assertOpenBaoPublicRefusalPreservesOriginals(t *testing.T, store secretStore, before map[string]string) {
	t.Helper()
	after := createBootstrapStateSnapshot(t, store.Root)
	for path, original := range before {
		if after[path] != original {
			t.Fatal("refused public command changed original state", path)
		}
	}
	for path := range after {
		if _, original := before[path]; original {
			continue
		}
		info, err := os.Lstat(filepath.Join(store.Root, path))
		if err != nil {
			t.Fatal(err)
		}
		if path == "deployment/logs" && info.IsDir() && info.Mode().Perm() == 0700 {
			continue
		}
		if filepath.Dir(path) != "deployment/logs" || !strings.HasPrefix(filepath.Base(path), "metrics-") || !strings.HasSuffix(path, ".log") || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			t.Fatal("refused public command wrote non-measurement state", path)
		}
		raw, err := os.ReadFile(filepath.Join(store.Root, path))
		if err != nil {
			t.Fatal(err)
		}
		lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
		for index, line := range lines {
			var measurement deploymentMeasurement
			if json.Unmarshal(line, &measurement) != nil || measurement.StartedAt.IsZero() || index == 0 && measurement.Event != "run-start" || index == len(lines)-1 && measurement.Event != "run-complete" {
				t.Fatal("refusal diagnostic is not a closed native measurement stream", path)
			}
		}
	}
}

func TestOpenBaoPublicCommandRejectsConflictingCheckpointSelections(t *testing.T) {
	for _, tc := range []struct {
		name, cause string
		flags       []string
	}{
		{"two-core", "exactly one original core checkpoint", []string{"--after-service-core-enrollment", "--after-managed-core-order"}},
		{"core-and-session", "accepted Service core is a distinct", []string{"--after-application-enrollment-trust", "--at-service-session"}},
		{"core-and-renewal", "accepted Service core is a distinct", []string{"--after-managed-core-order", "--at-certissuer-server-enrollment"}},
		{"session-and-root", "Service session recovery is a distinct", []string{"--at-service-session", "--at-service-root-trust-admission"}},
		{"enrollment-and-session", "expired bootstrap enrollment is a distinct", []string{"--at-certissuer-server-enrollment", "--at-service-session"}},
		{"root-and-reservation", "ready Root admission and absent reservation", []string{"--at-service-root-trust-rollout", "--recover-initial-reservation"}},
		{"listeners-and-renewal", "accepted listener continuation is a distinct", []string{"--after-pki-listeners", "--renew-initial-transports"}},
		{"two-listeners", "accepted listener continuation is a distinct", []string{"--after-pki-listeners", "--at-pki-listeners"}},
		{"account-and-roles", "exact dev parent and bounded timeout", []string{"--account-schema-migrate", "--after-database-roles"}},
		{"two-database", "exact dev parent and bounded timeout", []string{"--after-database-secrets", "--after-database-roles"}},
		{"foreign-environment", "exact dev parent and bounded timeout", []string{"--environment=staging"}},
		{"unbounded-timeout", "exact dev parent and bounded timeout", []string{"--timeout=91m"}},
		{"zero-timeout", "exact dev parent and bounded timeout", []string{"--timeout=0"}},
		{"foreign-parent", "exact dev parent and bounded timeout", []string{"--parent-plan-sha256=invalid"}},
		{"positional-argument", "exact dev parent and bounded timeout", []string{"unexpected"}},
		{"unknown-option", "flag provided but not defined", []string{"--skip-native-qualification"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--environment=dev", "--parent-plan-sha256=" + strings.Repeat("a", 64), "--workspace=" + filepath.Join(t.TempDir(), "not-created")}, tc.flags...)
			var out, diagnostics bytes.Buffer
			err := runEnvironmentCreateOpenBaoRecoveryContext(t.Context(), args, &out, &diagnostics, true)
			if err == nil || !strings.Contains(err.Error(), tc.cause) || out.Len() != 0 {
				t.Fatal("conflicting checkpoint escaped argument admission", err, out.String())
			}
		})
	}
}

// Every selected profile is exercised through the public command against the
// actual pending audit checkpoint. Selection never manufactures later history.
func TestOpenBaoPublicCommandRequiresActualSelectedCheckpoint(t *testing.T) {
	for _, tc := range []struct{ flag, cause string }{
		{"after-migration", "audit must be the sole pending original step"},
		{"after-database-roles", "audit must be the sole pending original step"},
		{"after-database-secrets", "audit must be the sole pending original step"},
		{"at-account-admission", "audit must be the sole pending original step"},
		{"after-account-startup", "only the original admission"},
		{"account-schema-migrate", "only the original admission"},
		{"after-account-bootstrap", "initial listener recovery permits only"},
		{"at-pki-listeners", "initial listener recovery permits only"},
		{"after-pki-listeners", "initial listener recovery permits only"},
		{"renew-initial-transports", "initial listener recovery permits only"},
		{"recover-initial-reservation", "initial reservation recovery permits only"},
		{"at-service-root-trust-admission", "Service Root trust recovery permits only"},
		{"at-service-root-trust-rollout", "partial Service Root trust recovery permits only"},
		{"at-service-session", "Service session recovery permits only"},
		{"at-certissuer-server-enrollment", "expired bootstrap recovery permits only"},
		{"after-bootstrap-renewal-permissions", "bootstrap permissions recovery permits only"},
		{"after-service-core-enrollment", "accepted Service core permits no"},
		{"after-managed-core-order", "managed core order recovery permits only"},
		{"after-application-enrollment-trust", "application trust recovery permits only"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			f, workspace, _, requests, mu := openBaoCommandContractFixture(t)
			before := createBootstrapStateSnapshot(t, f.store.Root)
			var out, diagnostics bytes.Buffer
			err := runEnvironmentCreateOpenBaoRecoveryContext(t.Context(), []string{"--workspace=" + workspace, "--environment=dev", "--parent-plan-sha256=" + f.sha, "--" + tc.flag}, &out, &diagnostics, true)
			if err == nil || !strings.Contains(err.Error(), tc.cause) {
				t.Fatal("wrong checkpoint was not precisely refused", err)
			}
			assertOpenBaoPublicRefusalPreservesOriginals(t, f.store, before)
			mu.Lock()
			count := len(*requests)
			mu.Unlock()
			if count != 0 {
				t.Fatal("unselected checkpoint reached external provider")
			}
			lock, err := deploymentplan.LockExecutionOwner(filepath.Join(f.store.Root, "deployment/.execution-active"), false)
			if err != nil {
				t.Fatal("refused selected checkpoint retained execution lock", err)
			}
			_ = lock.Close()
		})
	}
}

// This adapter reads only actual child-executor native objects and the exact
// original provider allocations. Missing initializer/volume evidence remains a
// real NO-GO; it does not promote the bounded fixture to a deployable source.
func installOpenBaoPublicOriginalReads(t *testing.T, f *initializedAuditContractFixture, mode string) (string, *[]string, *sync.Mutex) {
	t.Helper()
	if f.native == nil || len(f.native.objects) != 33 {
		t.Fatal("actual original native child inventory unavailable")
	}
	objects := map[string]deploymentplan.Object{}
	for id, object := range f.native.objects {
		objects[id] = deploymentplan.Clone(object)
	}
	selected := f.child.Resources[0].ID
	switch mode {
	case "foreign-lease":
		objects["Lease/"+f.cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud/rtk-deployment-execution"] = deploymentplan.Object{"apiVersion": "coordination.k8s.io/v1", "kind": "Lease", "metadata": deploymentplan.Object{"namespace": f.cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud", "name": "rtk-deployment-execution", "uid": "foreign-original-lease"}}
	case "foreign-owner":
		deploymentplan.Map(objects[selected]["metadata"])["uid"] = "foreign-original-owner"
	case "foreign-body":
		deploymentplan.Map(objects[selected]["data"])["fixture"] = "foreign-original-body"
	}
	var calls []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer isolated-provider-token" {
			http.Error(w, "unexpected original provider request", 401)
			return
		}
		for _, resource := range f.artifact.Plan.Plan.Provider.Resources {
			receipt := f.artifact.Plan.Receipts[resource.Key]
			path := resource.Path + "/" + url.PathEscape(receipt.NativeID)
			if resource.Kind == "bucket" {
				path = resource.Path + "/" + url.PathEscape(resource.Region) + "/" + url.PathEscape(resource.Name)
			}
			if r.URL.Path != path {
				continue
			}
			id, _ := strconv.Atoi(receipt.NativeID)
			actual := map[string]any{"id": id, "label": resource.Name, "region": resource.Region, "status": "running", "ipv4": []string{"203.0.113.42", "192.168.1.42"}, "s3_endpoint": "https://us-sea-1.linodeobjects.com"}
			if mode == "foreign-provider" {
				actual["label"] = "foreign-original-provider"
			}
			_ = json.NewEncoder(w).Encode(actual)
			return
		}
		http.Error(w, "unselected provider path", 404)
	}))
	t.Cleanup(server.Close)
	t.Setenv("RTK_CLOUD_LINODE_API_ROOT", server.URL)
	dir := t.TempDir()
	native, log := filepath.Join(dir, "native.json"), filepath.Join(dir, "reads.jsonl")
	if err := os.WriteFile(native, mustProviderRecoveryJSON(objects), 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/usr/bin/env python3
import json,os,sys
a=sys.argv[1:]
with open(os.environ['RTK_OPENBAO_PUBLIC_READS'],'a') as out:out.write(json.dumps(a)+'\n')
if 'get' not in a:sys.exit('isolated public adapter permits only selected reads')
i=a.index('get');kind=a[i+1]
if kind in ['nodes','node']:
 print(json.dumps({'apiVersion':'v1','kind':'NodeList','items':[]}));sys.exit()
if i+2>=len(a) or a[i+2].startswith('-'):sys.exit('unselected resource listing')
ns=a[a.index('--namespace')+1] if '--namespace' in a else ''
key=kind+'/'+ns+'/'+a[i+2]
with open(os.environ['RTK_OPENBAO_PUBLIC_NATIVE']) as source:objects=json.load(source)
if key in objects:print(json.dumps(objects[key]))
elif '--ignore-not-found=true' not in a:sys.exit('missing selected original resource')
`
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_OPENBAO_PUBLIC_NATIVE", native)
	t.Setenv("RTK_OPENBAO_PUBLIC_READS", log)
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	return log, &calls, &mu
}

func TestOpenBaoPublicCommandRechecksActualOriginalOwnersBeforeAdmission(t *testing.T) {
	for _, tc := range []struct{ mode, cause string }{
		{"retained-originals", "original initialized Pod, StatefulSet or key response identity changed"},
		{"foreign-provider", "original provider allocation changed"},
		{"foreign-lease", "previous child lease has not been released"},
		{"foreign-owner", "original accepted foundation owner changed"},
		{"foreign-body", "original foundation drift exceeds"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			f, workspace, _, _, _ := openBaoCommandContractFixture(t)
			log, calls, mu := installOpenBaoPublicOriginalReads(t, f, tc.mode)
			before := createBootstrapStateSnapshot(t, f.store.Root)
			writes := deploymentplan.Digest(f.native.writes)
			report := filepath.Join(f.store.Root, "deployment/reports/public-originals.json")
			args := []string{"--workspace=" + workspace, "--environment=dev", "--parent-plan-sha256=" + f.sha, "--report=" + report}
			var out, diagnostics bytes.Buffer
			err := runEnvironmentCreateOpenBaoRecoveryContext(context.Background(), args, &out, &diagnostics, true)
			if err == nil {
				t.Fatal("incomplete original initialization was admitted")
			}
			var result createFoundationRecoveryArtifact
			if err := readEnvironmentPreflightJSON(report, &result); err != nil {
				t.Fatal("public qualification omitted refusal report", err)
			}
			if result.Qualification.Decision != "NO-GO" || len(result.Qualification.Gates) != 6 || result.Qualification.Gates[0].ID != "deployment.openbao.target" || !strings.Contains(result.Qualification.Gates[0].Cause, tc.cause) {
				t.Fatal("native original gate did not reach exact refusal", result.Qualification)
			}
			afterQualification := createBootstrapStateSnapshot(t, f.store.Root)
			for path, original := range before {
				if got := afterQualification[path]; got != original {
					t.Fatal("qualification changed original evidence", path)
				}
			}
			if deploymentplan.Digest(f.native.writes) != writes {
				t.Fatal("read-only qualification wrote native objects")
			}
			mu.Lock()
			count := len(*calls)
			mu.Unlock()
			if count == 0 {
				t.Fatal("public target omitted original provider verification")
			}
			if tc.mode == "retained-originals" {
				raw, err := os.ReadFile(log)
				if err != nil {
					t.Fatal(err)
				}
				for _, resource := range f.child.Resources {
					parts := strings.Split(resource.ID, "/")
					if !bytes.Contains(raw, []byte(`"`+parts[2]+`"`)) {
						t.Fatal("original native child was not actually read", resource.ID)
					}
				}
				sha := result.Qualification.PlanSHA256
				qualification := filepath.Join("deployment/create/openbao-recovery", sha, "qualification.json")
				for _, persist := range []bool{false, true} {
					if persist {
						f.write(t, qualification, result)
					}
					beforeExecution := createBootstrapStateSnapshot(t, f.store.Root)
					out.Reset()
					diagnostics.Reset()
					err := runEnvironmentCreateOpenBaoRecoveryContext(t.Context(), append(slices.Clone(args), "--execute", "--confirm="+f.cfg.Values["CLOUD_STACK_NAME"], "--review-sha256="+sha), &out, &diagnostics, true)
					if err == nil || !strings.Contains(err.Error(), "fresh complete initialized OpenBao recovery and exact confirmation required") {
						t.Fatal("unqualified report became executable", persist, err)
					}
					assertOpenBaoPublicRefusalPreservesOriginals(t, f.store, beforeExecution)
					mu.Lock()
					afterCount := len(*calls)
					mu.Unlock()
					if afterCount != count {
						t.Fatal("NO-GO execution reached provider recheck")
					}
				}
			}
		})
	}
}
