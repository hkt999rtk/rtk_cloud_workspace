package main

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestHistoricalUsedMQTTExecutionContractCompletedRetryRefusesDrift(t *testing.T) {
	mqtt, r, _ := newHistoricalUsedMQTTRenewalExecutionContract(t, coreExecutionContractHooks{}, "complete")
	f := mqtt.acceptedCoreExecutionContract
	runtime := f.runtime
	runtime.Backend, runtime.ServiceSessionRecovery = f.backend, r
	runtime.OpenBao.RootTokenPath = filepath.Join(f.store.Root, "openbao/root-token")
	core, _, err := acceptedCreateStage(f.store, f.e, "kubernetes/service-managed-used-renewal-"+r.Review)
	if err != nil {
		t.Fatal(err)
	}
	var owner, trust string
	for _, resource := range core.Resources {
		if resource.Desired["kind"] == "Deployment" && owner == "" {
			owner = resource.ID
		}
		if resource.Desired["kind"] == "ConfigMap" && deploymentplan.Map(resource.Desired["metadata"])["name"] == "pki-create-service-trust" {
			trust = resource.ID
		}
	}
	if owner == "" || trust == "" {
		t.Fatal("completed Core fixture lacks original owner or evolved shared trust")
	}
	for _, mode := range []string{"native-owner", "native-trust-body", "original-csr", "foreign-installed-subject", "duplicate-installed-subject", "foreign-issued-subject", "observed-cleanup-owner", "native-role-scope"} {
		t.Run(mode, func(t *testing.T) {
			var restore func()
			switch mode {
			case "native-owner", "native-trust-body":
				id := owner
				if mode == "native-trust-body" {
					id = trust
				}
				original := deploymentplan.Clone(f.native.objects[id])
				if mode == "native-owner" {
					deploymentplan.Map(f.native.objects[id]["metadata"])["uid"] = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
				} else {
					deploymentplan.Map(f.native.objects[id]["data"])["bundles.json"] = "changed-native-trust"
				}
				f.protocol["objects"] = coreContractNativeInventory(f.native.objects)
				restore = func() {
					f.native.objects[id] = original
					f.protocol["objects"] = coreContractNativeInventory(f.native.objects)
				}
			case "original-csr":
				custody := f.protocol["custody"].(map[string]string)
				original := custody["/run/pki-bootstrap/bootstrap.csr"]
				der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "service:deployment-bootstrap"}}, f.issuerKey)
				if err != nil {
					t.Fatal(err)
				}
				custody["/run/pki-bootstrap/bootstrap.csr"] = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
				restore = func() { custody["/run/pki-bootstrap/bootstrap.csr"] = original }
			case "foreign-installed-subject", "duplicate-installed-subject":
				sessions := f.protocol["historical_sessions"].(map[string]managedSessionState)
				original := sessions[f.session]
				changed := original
				changed.Installed = []string{"service:foreign-workload"}
				if mode == "duplicate-installed-subject" {
					changed.Installed = []string{original.Subjects[0], original.Subjects[0]}
				}
				sessions[f.session] = changed
				restore = func() { sessions[f.session] = original }
			case "foreign-issued-subject":
				original := f.protocol["issued"]
				f.protocol["issued"] = []string{"service:foreign-workload"}
				restore = func() { f.protocol["issued"] = original }
			case "observed-cleanup-owner":
				path := filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-renewal", r.Review, "observed.json")
				original, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var observed createServiceSessionObserved
				if err := json.Unmarshal(original, &observed); err != nil {
					t.Fatal(err)
				}
				observed.Cleanup.SecretUID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
				if err := os.WriteFile(path, mustProviderRecoveryJSON(observed), 0600); err != nil {
					t.Fatal(err)
				}
				restore = func() {
					if err := os.WriteFile(path, original, 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "native-role-scope":
				path := os.Getenv("RTK_HISTORICAL_SESSION_BAO")
				original, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var bao map[string]any
				if err := json.Unmarshal(original, &bao); err != nil {
					t.Fatal(err)
				}
				for _, value := range bao["roles"].(map[string]any) {
					value.(map[string]any)["bound_service_account_names"] = []string{"foreign-service-account"}
				}
				if err := os.WriteFile(path, mustProviderRecoveryJSON(bao), 0600); err != nil {
					t.Fatal(err)
				}
				restore = func() {
					if err := os.WriteFile(path, original, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			defer func() { restore(); f.publish(t) }()
			f.publish(t)
			before := createBootstrapStateSnapshot(t, f.store.Root)
			writes := deploymentplan.Digest(f.native.writes)
			_, err := executeEnvironmentCreateServiceBootstrap(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.store, f.operator, runtime, func(context.Context, string, []deploymentplan.Definition) error {
				t.Fatal("changed completed source reached a native stage")
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatal("completed renewal accepted changed original source", mode, err)
			}
			if writes != deploymentplan.Digest(f.native.writes) || f.native.held {
				t.Fatal("refusal changed native resources or retained an execution lease")
			}
			assertRecoveryContractReadOnly(t, f.store, before)
		})
	}
	if _, resumed, err := resumeAcceptedUsedBootstrapCore(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.store, runtime); err != nil || !resumed {
		t.Fatal("restored exact source no longer resumes", err)
	}
}
