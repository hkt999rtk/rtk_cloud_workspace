package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCustodyPublicCRLReferenceBindsOriginalLogicalOwnerAndAuthority(t *testing.T) {
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "PKI_STATE_BACKEND": "openbao", "PKI_STATE_KV_MOUNT": "identities", "PKI_STATE_LEASE_DURATION": "30s"}}
	binding := &environmentCustodyBinding{Generation: strings.Repeat("a", 64), OpenBaoRootSHA256: strings.Repeat("b", 64), Owners: []environmentCustodyOwnerBinding{{ID: "api", Namespace: "video-cloud-dev-video-cloud", ServiceAccount: "api", StorageAliases: []string{"api-state"}}}}
	issuer := "11111111-1111-4111-8111-111111111111"
	path, pin := "/identity/crl/service.json", strings.Repeat("c", 64)
	ref, err := newEnvironmentCustodyPublicCRLReference(cfg, binding, "video-cloud-dev-video-cloud", "api-state", path, "service", issuer, pin)
	if err != nil {
		t.Fatal(err)
	}
	if ref.RecordKey != "dev/"+binding.Generation+"/api/public/"+strings.TrimPrefix(environmentCustodyRecordKey("", path), "/") {
		t.Fatal("wrong public record key")
	}
	for _, change := range []func(*environmentCustodyPublicCRLReference){
		func(r *environmentCustodyPublicCRLReference) {
			r.RecordKey = environmentCustodyRecordKey("dev/"+binding.Generation+"/api", path)
		},
		func(r *environmentCustodyPublicCRLReference) { r.Domain = "mqtt" },
		func(r *environmentCustodyPublicCRLReference) { r.IssuerID = "22222222-2222-4222-8222-222222222222" },
		func(r *environmentCustodyPublicCRLReference) { r.IssuerSHA256 = strings.Repeat("d", 64) },
		func(r *environmentCustodyPublicCRLReference) { r.Owner = "other" },
		func(r *environmentCustodyPublicCRLReference) { r.Generation = strings.Repeat("e", 64) },
	} {
		modified := *ref
		change(&modified)
		if validateEnvironmentCustodyPublicCRLReference(&modified, cfg, binding, ref.Namespace, "api-state", path, "service", issuer, pin) == nil {
			t.Fatal("foreign public state accepted", deploymentplan.Digest(modified))
		}
	}
	if _, err := newEnvironmentCustodyPublicCRLReference(cfg, binding, ref.Namespace, "business-data", path, "service", issuer, pin); err == nil {
		t.Fatal("unbound storage purpose accepted")
	}
}

func TestCustodyPublicCRLExecutionUsesOriginalScopeAndCommittedNativeReadback(t *testing.T) {
	for _, apply := range []bool{false, true} {
		for _, mode := range []string{"public-success", "public-exec-failed", "public-replaced-after-exec", "public-bad-output"} {
			t.Run(mode+map[bool]string{false: "/inspect", true: "/apply"}[apply], func(t *testing.T) {
				cfg, store, _, _ := createStateFixture(t, "dev")
				cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
				cfg.Values["DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS"], cfg.Values["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"] = "30", "300"
				item, _, _ := maintenanceFixture(t, "dev", time.Now().UTC().Truncate(time.Second).Add(72*time.Hour))
				binding := &environmentCustodyBinding{Generation: strings.Repeat("a", 64), OpenBaoRootSHA256: strings.Repeat("b", 64), Owners: []environmentCustodyOwnerBinding{{ID: "api", Namespace: "video-cloud-dev-video-cloud", ServiceAccount: "api", StorageAliases: []string{"api-state"}}}}
				pin := deploymentplan.String(item.Issuer.Document["certificate_fingerprint_sha256"])
				ref, err := newEnvironmentCustodyPublicCRLReference(cfg, binding, binding.Owners[0].Namespace, "api-state", "/identity/crl/service.json", item.Issuer.Domain, item.Issuer.ID, pin)
				if err != nil {
					t.Fatal(err)
				}
				source := deploymentplan.Object{"securityContext": environmentCreateServicePodSecurity(), "containers": []any{deploymentplan.Object{"securityContext": environmentCreateServiceContainerSecurity()}}}
				job, err := environmentCustodyPublicCRLJob(cfg, binding, ref, source, "ghcr.io/fixture/native@sha256:"+strings.Repeat("d", 64), "", apply)
				if err != nil {
					t.Fatal(err)
				}
				fixture := t.TempDir()
				installCustodyInspectionProcessFixture(t, fixture, mode)
				raw, _ := json.Marshal(map[string]any{"issuer_fingerprint": pin, "crl": item.Snapshot.Prior})
				if err := os.WriteFile(filepath.Join(fixture, "public.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
				backend := environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-inspection-kubeconfig"), RolloutTimeout: time.Minute}
				var record *createRootCRLRecord
				if apply {
					copy := item.Snapshot.Prior
					record = &copy
				}
				observed, err := runEnvironmentCustodyPublicCRL(context.Background(), backend, job, ref, item.Issuer, record)
				if (err == nil) != (mode == "public-success") {
					t.Fatal("native public CRL outcome disagrees with original helper scope", err)
				}
				if err != nil {
					if strings.Contains(err.Error(), "private-error-marker") {
						t.Fatal("private failure leaked")
					}
					return
				}
				if observed.CRL.Digest != item.Snapshot.Prior.Digest {
					t.Fatal("no committed public readback")
				}
				input, err := os.ReadFile(filepath.Join(fixture, "input.json"))
				if err != nil {
					t.Fatal(err)
				}
				var transferred string
				if json.Unmarshal(input, &transferred) != nil {
					t.Fatal("input fixture invalid")
				}
				if apply {
					var got createRootCRLRecord
					if json.Unmarshal([]byte(transferred), &got) != nil || got.Digest != record.Digest {
						t.Fatal("public repair did not transfer exact reviewed record through stdin")
					}
				} else if transferred != "" {
					t.Fatal("read-only inspection transferred a write")
				}
			})
		}
	}
}

func TestCustodyMaintenanceInstallUsesCommittedKVWithoutReadingFaultedApplication(t *testing.T) {
	_, _, _, original := createServiceRecipeFixture(t, "dev")
	_, _, _, next := createServiceRecipeFixture(t, "dev")
	cfg, store, _, _ := createStateFixture(t, "dev")
	cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	cfg.Values["DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS"], cfg.Values["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"] = "30", "300"
	generation := strings.Repeat("a", 64)
	ns := "video-cloud-dev-video-cloud"
	managed := next.Root
	managed.Domain = "openbao_tls"
	managed.Status = "active"
	binding := &environmentCustodyBinding{Generation: generation, ServiceRootSHA256: original.Root.RootFingerprint, OpenBaoRootSHA256: managed.RootFingerprint, Owners: []environmentCustodyOwnerBinding{{ID: "api", Namespace: ns, ServiceAccount: "api", StorageAliases: []string{"api-state"}}}}
	trust, err := renderEnvironmentCustodyTrust(cfg, generation, original.Root.Certificate, &managed, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.write(filepath.Join("deployment", "create", "material", generation, "openbao-transport", "ca.crt"), []byte(original.Root.Certificate), false); err != nil {
		t.Fatal(err)
	}
	if err := store.write(filepath.Join("deployment", "create", generation, "custody", "transport.json"), []byte(deploymentplan.String(deploymentplan.Map(trust[0].Object["data"])["transport.json"])), false); err != nil {
		t.Fatal(err)
	}
	completed := environmentIdentityBindings{Version: 1, Environment: "dev", Stack: "video-cloud-dev", Owners: map[string]string{"Deployment/" + ns + "/api": "original-api"}, Custody: binding}
	raw, _ := json.Marshal(completed)
	if err := store.write(filepath.Join("deployment", "create", generation, "bootstrap", "bindings.json"), raw, false); err != nil {
		t.Fatal(err)
	}
	item, _, _ := maintenanceFixture(t, "dev", time.Now().UTC().Truncate(time.Second).Add(72*time.Hour))
	c := deploymentplan.Object{"name": "app", "env": environmentCreateEnv(map[string]string{"TEST_CLIENT_IDENTITY_STATE": "/identity/private/client.json"}), "securityContext": environmentCreateServiceContainerSecurity(), "volumeMounts": []any{environmentCreateServiceMount("state", "/identity", false)}}
	parent := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": ns, "name": "api", "uid": "original-api"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"metadata": deploymentplan.Object{}, "spec": deploymentplan.Object{"securityContext": environmentCreateServicePodSecurity(), "containers": []any{c}, "volumes": []any{environmentCreateServiceVolume("state", "persistentVolumeClaim", "api-state")}}}}}
	projected, err := projectEnvironmentCustodyResources(cfg.Values, "dev", binding, "", []deploymentplan.Definition{{Object: parent}})
	if err != nil {
		t.Fatal(err)
	}
	parent = createRecipeObject(t, projected, deploymentplan.ID(parent))
	pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"namespace": ns, "name": "faulted-api", "uid": "original-pod", "ownerReferences": []any{deploymentplan.Object{"kind": "Deployment", "name": "api", "uid": "original-api", "controller": true}}}, "spec": environmentPKIPod(parent), "status": deploymentplan.Object{"phase": "Running"}}
	fixture := t.TempDir()
	installCustodyInspectionProcessFixture(t, fixture, "public-success")
	public, _ := json.Marshal(map[string]any{"issuer_fingerprint": deploymentplan.String(item.Issuer.Document["certificate_fingerprint_sha256"]), "crl": item.Snapshot.Prior})
	if err := os.WriteFile(filepath.Join(fixture, "public.json"), public, 0600); err != nil {
		t.Fatal(err)
	}
	r := maintenanceRuntime{cfg: cfg, store: store, backend: environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-inspection-kubeconfig"), RolloutTimeout: time.Minute}, image: "ghcr.io/fixture/native@sha256:" + strings.Repeat("d", 64), lease: maintenanceTestLease{}}
	r.readOverride = func(_ context.Context, id string) (deploymentplan.Object, error) {
		if id == "Pod/"+ns+"/faulted-api" {
			return pod, nil
		}
		if id == deploymentplan.ID(parent) {
			return parent, nil
		}
		t.Fatal("unexpected original owner read", id)
		return nil, nil
	}
	r.commandOverride = func(context.Context, []byte, ...string) ([]byte, error) {
		t.Fatal("maintenance must not read a private projection or stat the faulted application")
		return nil, nil
	}
	consumer := crlMaintenanceConsumer{Namespace: ns, Pod: "faulted-api", PodUID: "original-pod", Container: "app", StatePath: "/identity/crl/root.json", IssuerID: item.Issuer.ID}
	if err := r.install(t.Context(), consumer, item.Issuer, item.Snapshot.Prior); err != nil {
		t.Fatal("actual remote maintenance path", err)
	}
	observed := environmentIdentityBindings{Version: 1, Environment: "dev", Stack: "video-cloud-dev", Owners: map[string]string{deploymentplan.ID(parent): "current-api"}, Pods: []environmentPodBindings{{Kind: "Deployment", Name: "api", NamespaceSuffix: "video-cloud", PodFields: map[string]any{"serviceAccountName": "api"}, Containers: []environmentContainerBindings{{Name: "app", Fields: map[string]any{"env": managedNamedField(environmentPKIPod(parent)["containers"], "app")["env"]}}}}}}
	if err := restoreEnvironmentCustodyBindings(t.Context(), cfg, store, "upgrade", &observed); err != nil {
		t.Fatal("restore completed generation during upgrade", err)
	}
	if observed.Custody == nil || deploymentplan.Digest(observed.Custody) != deploymentplan.Digest(binding) || observed.Owners[deploymentplan.ID(parent)] != "current-api" {
		t.Fatal("scope restoration replaced current observed resource facts")
	}
	if err := os.Remove(filepath.Join(store.Root, "deployment", "create", generation, "bootstrap", "bindings.json")); err != nil {
		t.Fatal(err)
	}
	observed.Custody = nil
	if err := restoreEnvironmentCustodyBindings(t.Context(), cfg, store, "upgrade", &observed); err == nil {
		t.Fatal("upgrade invented missing completed scope")
	}
	creating := environmentCreateBootstrapRuntime{ExecutionSHA256: generation, Custody: binding, ServiceCeremony: &original}
	if err := restoreEnvironmentCustodyBindings(withEnvironmentCreateBootstrapRuntime(t.Context(), creating), cfg, store, "create", &observed); err != nil {
		t.Fatal("admitted creation lost current scope before completion file exists", err)
	}
}
