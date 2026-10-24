package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This is a command-level refusal contract, with an explicit historical native
// inventory boundary. The actual original transport/signer producers precede
// the genuine33-write/readiness interruption. Unavailable external providers
// must yield a complete NO-GO report and never executable recovery authority.
func TestFoundationCLIExecutionContractReportsAllFailuresWithoutReplacingOriginalIdentity(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	appendFile(t, filepath.Join(workspace, "cloud_deploy/architectures/kubernetes/architecture.env"), "DEPLOYMENT_PROTECTION_PROFILE=standard\n")
	t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
	cfg, err := resolveDeploymentConfig(workspace, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	kube, err := yaml.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "Config", "current-context": "original",
		"contexts": []any{map[string]any{"name": "original", "context": map[string]any{"cluster": "original", "user": "original"}}},
		"clusters": []any{map[string]any{"name": "original", "cluster": map[string]any{"server": "https://isolated.invalid:6443", "certificate-authority-data": base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}}},
		"users":    []any{map[string]any{"name": "original", "user": map[string]any{"token": "isolated-original-kubernetes-token"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := foundationExecutionContractFixtureWithInputs(t, 1, foundationExecutionContractInputs{
		provider: providerExecutionContractInputs{cfg: cfg, operator: map[string]string{"LINODE_TOKEN": "isolated-original-linode-token", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD": "isolated-original-admin-password"}, kubeconfig: kube},
		bindings: func(store secretStore, e *environmentCreateExecution) []deploymentplan.Definition {
			// This is the original fresh-generation producer, before its first
			// foundation write. Recovery below never creates a missing credential.
			if err := store.write("runtime/platform-admin", []byte("isolated-original-admin-password\n"), false); err != nil {
				t.Fatal(err)
			}
			if err := ensureMissingRuntimeSecrets(io.Discard, store); err != nil {
				t.Fatal(err)
			}
			var definitions []deploymentplan.Definition
			for _, role := range []string{"management", "openbao"} {
				names := map[string][]string{}
				if role == "openbao" {
					names[role] = []string{"openbao.video-cloud-dev-secrets.svc"}
				} else {
					for _, name := range []string{"account-manager", "pki-controller", "certissuer", "video-cloud-api", "video-cloud-api-app-pki"} {
						names[name] = []string{name + ".video-cloud-dev-platform.svc"}
					}
				}
				transport, err := createEnvironmentTransport(store, e.Journal.PlanSHA256, role, names, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				for name, identity := range transport.Identities {
					secret := "pki-create-" + name + "-transport"
					if role == "openbao" {
						secret = "openbao-transport-tls"
					}
					definitions = append(definitions, environmentCreateSecret(e.Journal.Stack+"-secrets", secret, map[string]string{"ca.crt": transport.CACert, "tls.crt": identity.Certificate, "tls.key": identity.Key}))
				}
			}
			signer, err := ensureEnvironmentCreateAccountSigner(store)
			if err != nil {
				t.Fatal(err)
			}
			definitions = append(definitions, environmentCreateSecret(e.Journal.Stack+"-secrets", "account-manager-jwt", signer))
			// The selected isolated SecretStore already produced its inventory.
			// Preserve it; foundation qualification authenticates its actual bytes.
			var inventory secretInventory
			if err := readExecutionJSON(filepath.Join(store.Root, "inventory.json"), &inventory); err != nil || inventory.SchemaVersion != secretInventorySchemaVersion || inventory.Environment != "dev" {
				t.Fatal("original selected runtime inventory missing", err)
			}
			if err := store.write("deployment/storage/create-allocation.json", mustProviderRecoveryJSON(map[string]string{"source": "isolated original allocation"}), false); err != nil {
				t.Fatal(err)
			}
			return definitions
		},
	})
	var requests []string
	var requestMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestMu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		requestMu.Unlock()
		if r.Method != http.MethodGet {
			t.Error("qualification mutated native provider allocation")
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	t.Setenv("RTK_CLOUD_LINODE_API_ROOT", server.URL)
	t.Setenv("RTK_CLOUD_GODADDY_API_ROOT", server.URL)
	bin := t.TempDir()
	calls := filepath.Join(bin, "native-calls")
	for _, name := range []string{"git", "gh", "docker", "kubectl"} {
		body := "#!/bin/sh\nprintf '%s %s\\n' \"${0##*/}\" \"$*\" >> \"$RTK_FOUNDATION_CLI_CALLS\"\nexit 1\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("RTK_FOUNDATION_CLI_CALLS", calls)
	t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(bin, "kubectl"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	before := createBootstrapStateSnapshot(t, f.store.Root)
	report := filepath.Join(f.store.Root, "deployment/reports/foundation-refusal.json")
	commandErr := runEnvironmentCreateFoundationRecovery([]string{"--environment=dev", "--parent-plan-sha256=" + f.e.Journal.PlanSHA256, "--report=" + report})
	if commandErr == nil {
		t.Fatal("unavailable original foundation was admitted")
	}
	var a createFoundationRecoveryArtifact
	if err := readEnvironmentPreflightJSON(report, &a); err != nil {
		t.Fatal("complete private foundation refusal report missing", err, "command:", commandErr)
	}
	if a.Qualification.Decision != "NO-GO" || len(a.Qualification.Gates) != 6 || a.Plan.ParentSHA != f.e.Journal.PlanSHA256 || a.Plan.FoundationSHA != f.child.SHA256() || a.Plan.FoundationJournalSHA != deploymentplan.Digest(f.journal) {
		t.Fatal("foundation report lost original source or required gates")
	}
	for index, id := range []string{"deployment.foundation.target", "deployment.foundation.release", "deployment.foundation.storage", "deployment.foundation.bootstrap", "deployment.foundation.network", "deployment.foundation.inputs"} {
		gate := a.Qualification.Gates[index]
		if gate.ID != id || !gate.Required || gate.EvidenceSHA256 != a.Qualification.PlanSHA256 || gate.Status != "BLOCKED" {
			t.Fatal("qualification stopped early or fabricated a passing prerequisite", gate)
		}
	}
	requestMu.Lock()
	requestCount := len(requests)
	requestMu.Unlock()
	if requestCount == 0 {
		t.Fatal("foundation command omitted actual provider read adapter")
	}
	if _, err := os.Lstat(filepath.Join(f.store.Root, "deployment/create/foundation-recovery", a.Qualification.PlanSHA256, "qualification.json")); !os.IsNotExist(err) {
		t.Fatal("refused foundation published executable recovery authority")
	}
	after := createBootstrapStateSnapshot(t, f.store.Root)
	for path, value := range before {
		if path != "deployment" && after[path] != value {
			t.Fatal("refused foundation replaced original authority, signer, owner or journal", path)
		}
	}
	raw, _ := json.Marshal(a)
	for _, secret := range []string{"isolated-original-linode-token", "isolated-original-kubernetes-token"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private original credential entered refusal report")
		}
	}
	// A complete refusal report is useful diagnostics, never an execution
	// admission. Reusing its review must stop before any native recheck/write.
	refused := createBootstrapStateSnapshot(t, f.store.Root)
	if err := runEnvironmentCreateFoundationRecovery([]string{"--environment=dev", "--parent-plan-sha256=" + f.e.Journal.PlanSHA256, "--execute", "--confirm=" + cfg.Values["CLOUD_STACK_NAME"], "--review-sha256=" + a.Qualification.PlanSHA256}); err == nil {
		t.Fatal("NO-GO diagnostic review authorized execution")
	}
	assertRecoveryContractReadOnly(t, f.store, refused)
	requestMu.Lock()
	afterExecutionRequests := len(requests)
	requestMu.Unlock()
	if afterExecutionRequests != requestCount {
		t.Fatal("refused review reached native provider execution")
	}
	lock, err := deploymentplan.LockExecutionOwner(filepath.Join(f.store.Root, "deployment/.execution-active"), false)
	if err != nil {
		t.Fatal("refused foundation retained process ownership", err)
	}
	_ = lock.Close()
}
