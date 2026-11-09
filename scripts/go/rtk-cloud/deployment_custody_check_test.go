package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCustodyHealthContextUsesSelectedImageAndOneFrozenEnvironment(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, store, _, _ := createStateFixture(t, environment)
			cfg.Values["PKI_STATE_BACKEND"] = "openbao"
			image := "ghcr.io/fixture/native@sha256:" + strings.Repeat("a", 64)
			if err := store.write("operator/env/LKE_PKI_CONTROLLER_IMAGE", []byte(image), true); err != nil {
				t.Fatal(err)
			}
			kube, err := yaml.Marshal(frozenKubernetesFixture(t))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.write("kube/kubeconfig.yaml", kube, true); err != nil {
				t.Fatal(err)
			}
			runtime := newEnvironmentCheckRuntime(t.Context(), cfg, store, nil, "")
			if runtime.custodyError != nil || runtime.custodyReader == nil || runtime.custodyReader.image != image || runtime.custodyReader.store.Root != store.Root {
				t.Fatal("health did not consume selected environment image/store", runtime.custodyError)
			}
			if runtime.frozenBackend == nil || !bytes.Equal(runtime.frozenBackend.KubeconfigContent, runtime.custodyReader.backend.KubeconfigContent) {
				t.Fatal("ordinary health reads and custody helper can use different Kubernetes identities")
			}
			original := append([]byte(nil), runtime.frozenBackend.KubeconfigContent...)
			if err := store.write("kube/kubeconfig.yaml", []byte("foreign-replacement-canary"), true); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(runtime.frozenBackend.KubeconfigContent, original) || !bytes.Equal(runtime.custodyReader.backend.KubeconfigContent, original) {
				t.Fatal("checker switched to replacement canonical authentication")
			}
		})
	}
}

func TestCustodyAccountRegistrationHealthUsesCommittedCachesAndActualPublicReload(t *testing.T) {
	f := newRegistrationPublicHealthFixture(t)
	operator, err := f.store.readOperator()
	if err != nil {
		t.Fatal(err)
	}
	f.store = makeIsolatedTestSecretStore(t, "dev")
	for key, value := range operator {
		if err := f.store.write("operator/env/"+key, []byte(value), false); err != nil {
			t.Fatal(err)
		}
	}
	f.cfg.Values["PKI_STATE_BACKEND"], f.cfg.Values["PKI_STATE_KV_MOUNT"], f.cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	f.cfg.Values["DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS"], f.cfg.Values["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"] = "30", "300"
	generation := strings.Repeat("a", 64)
	ns := f.cfg.Values["CLOUD_STACK_NAME"] + "-account-manager"
	ca, err := kubernetesSecretBytes(f.listener, "client-ca.crt")
	if err != nil {
		t.Fatal(err)
	}
	root, err := parseFirstCertificatePEM(ca)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, transport := createServiceRecipeFixture(t, "dev")
	_, _, _, next := createServiceRecipeFixture(t, "dev")
	managed := next.Root
	managed.Domain, managed.Status = "openbao_tls", "active"
	binding := &environmentCustodyBinding{Generation: generation, ServiceRootSHA256: environmentPreflightBytesSHA256(root.Raw), OpenBaoRootSHA256: managed.RootFingerprint, Owners: []environmentCustodyOwnerBinding{{ID: "account-manager", Namespace: ns, ServiceAccount: "account-manager", StorageAliases: []string{environmentCreateServiceOwnerClaim("account-manager")}, Clients: []environmentCustodyClientBinding{{Subject: "service:account-manager", StatePath: environmentCreateServiceOwnerIdentityPath("account-manager", "client.json")}}}}}
	trust, err := renderEnvironmentCustodyTrust(f.cfg, generation, transport.Root.Certificate, &managed, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.write(filepath.Join("deployment", "create", "material", generation, "openbao-transport", "ca.crt"), []byte(transport.Root.Certificate), false); err != nil {
		t.Fatal(err)
	}
	if err := f.store.write(filepath.Join("deployment", "create", generation, "custody", "transport.json"), []byte(deploymentplan.String(deploymentplan.Map(trust[0].Object["data"])["transport.json"])), false); err != nil {
		t.Fatal(err)
	}
	completed := environmentIdentityBindings{Version: 1, Environment: "dev", Stack: f.cfg.Values["CLOUD_STACK_NAME"], Owners: map[string]string{deploymentplan.ID(f.account): "account-uid"}, Custody: binding}
	if err := f.store.write(filepath.Join("deployment", "create", generation, "bootstrap", "bindings.json"), mustProviderRecoveryJSON(completed), false); err != nil {
		t.Fatal(err)
	}
	projected, err := projectEnvironmentCustodyResources(f.cfg.Values, "dev", binding, "", []deploymentplan.Definition{{Object: f.account}})
	if err != nil {
		t.Fatal(err)
	}
	f.account = createRecipeObject(t, projected, deploymentplan.ID(f.account))
	f.pod["spec"] = deploymentplan.Clone(environmentPKIPod(f.account))
	f.objects[deploymentplan.ID(f.account)], f.objects[deploymentplan.ID(f.pod)] = f.account, f.pod
	fixture := t.TempDir()
	installCustodyInspectionProcessFixture(t, fixture, "public-success")
	states := map[string]json.RawMessage{}
	for _, raw := range f.states {
		var state environmentCustodyCRLState
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		states[state.CRL.IssuerID] = raw
	}
	encoded, err := json.Marshal(states)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "public-states.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	image := "ghcr.io/fixture/native@sha256:" + strings.Repeat("d", 64)
	reader := &maintenanceRuntime{cfg: f.cfg, store: f.store, backend: environmentKubernetesBackend{Store: f.store, KubeconfigContent: []byte("isolated-account-reader"), RolloutTimeout: time.Minute}, image: image, readOverride: f.read}
	reader.commandOverride = func(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "get" && args[1] == "pods" {
			return json.Marshal(deploymentplan.Object{"items": []any{f.pod}})
		}
		return f.command(ctx, args...)
	}
	f.store.checkRuntime = newDeploymentCheckRuntime(t.Context(), "dev")
	f.store.checkRuntime.custodyReader = reader
	privateReads := 0
	command := func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 {
			if _, private := f.states[args[len(args)-1]]; private {
				privateReads++
				return nil, fmt.Errorf("private projection must not be read")
			}
		}
		return f.command(ctx, args...)
	}
	before := deploymentplan.Digest(f.listener)
	result, err := readAccountRegistrationPublicListener(t.Context(), f.cfg, f.store, f.account, f.listener, f.now, f.read, command)
	if err != nil {
		t.Fatalf("remote Account Manager public health failed: public reads=%d private reads=%d: %v", f.execs, privateReads, err)
	}
	actual, err := kubernetesSecretBytes(result, "client.crl")
	if err != nil || !bytes.Equal(actual, f.public) || deploymentplan.Digest(f.listener) != before {
		t.Fatal("public cache proof replaced the original listener/key/CA or actual export", err)
	}
	// A good public output does not excuse missing committed remote state.
	for id := range states {
		states[id] = json.RawMessage(`{"issuer_fingerprint":"foreign"}`)
		break
	}
	corrupt, _ := json.Marshal(states)
	if err := os.WriteFile(filepath.Join(fixture, "public-states.json"), corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	f.store.checkRuntime = newDeploymentCheckRuntime(t.Context(), "dev")
	f.store.checkRuntime.custodyReader = reader
	if _, err := readAccountRegistrationPublicListener(t.Context(), f.cfg, f.store, f.account, f.listener, f.now, f.read, command); err == nil {
		t.Fatal("public bytes accepted without original committed CRL cache")
	}
}
