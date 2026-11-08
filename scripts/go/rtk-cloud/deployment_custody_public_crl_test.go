package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	binding := &environmentCustodyBinding{Generation: generation, ServiceRootSHA256: original.Root.RootFingerprint, OpenBaoRootSHA256: managed.RootFingerprint, Owners: []environmentCustodyOwnerBinding{{ID: "api", Namespace: ns, ServiceAccount: "api", StorageAliases: []string{"api-state", "registration-state"}, Clients: []environmentCustodyClientBinding{{Subject: "service:video-cloud-api", StatePath: "/identity/private/client.json"}, {Subject: "service:logger", StatePath: "/registration/private/client.json"}}}}}
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
	c := deploymentplan.Object{"name": "app", "env": environmentCreateEnv(map[string]string{
		"TEST_CLIENT_IDENTITY_STATE":              "/identity/private/client.json",
		"TEST_SERVICE_CLIENT_PUBLIC_CRL_BUNDLE":   "/export/client.crl",
		"TEST_SERVICE_CLIENT_SERVER_CRL_MANIFEST": "/trust/crls.json",
	}), "securityContext": environmentCreateServiceContainerSecurity(), "volumeMounts": []any{
		environmentCreateServiceMount("state", "/identity", false), environmentCreateServiceMount("export", "/export", false), environmentCreateServiceMount("trust", "/trust", true),
	}}
	parent := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": ns, "name": "api", "uid": "original-api"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"metadata": deploymentplan.Object{}, "spec": deploymentplan.Object{"securityContext": environmentCreateServicePodSecurity(), "containers": []any{c}, "volumes": []any{environmentCreateServiceVolume("state", "persistentVolumeClaim", "api-state"), deploymentplan.Object{"name": "export", "emptyDir": deploymentplan.Object{"medium": "Memory"}}, environmentCreateServiceVolume("trust", "configMap", "maintenance-crls")}}}}}
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
	manifest := environmentCreateServiceCRLManifest(item.Issuer, "/identity/crl/root.json")
	r.readOverride = func(_ context.Context, id string) (deploymentplan.Object, error) {
		if id == "Pod/"+ns+"/faulted-api" {
			return pod, nil
		}
		if id == deploymentplan.ID(parent) {
			return parent, nil
		}
		if id == "ConfigMap/"+ns+"/maintenance-crls" {
			return deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "original-manifest"}, "data": deploymentplan.Object{"crls.json": manifest}}, nil
		}
		if id == "Deployment/"+ns+"/pki-controller" {
			return deploymentplan.Object{"spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "pki-controller", "env": environmentCreateEnv(map[string]string{"PKI_REQUIRED_CONSUMERS_SERVICE": "video-cloud-api"})}}}}}}, nil
		}
		t.Fatal("unexpected original owner read", id)
		return nil, nil
	}
	ackChecks, exportReads := 0, 0
	publicReady := false
	r.commandOverride = func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "get" && args[1] == "pods" {
			return json.Marshal(deploymentplan.Object{"items": []any{pod}})
		}
		if len(args) >= 2 && args[len(args)-2] == "cat" && args[len(args)-1] == "/export/client.crl" {
			exportReads++
			if !publicReady {
				return nil, fmt.Errorf("public producer has not booted")
			}
			return []byte(item.Snapshot.Prior.PEM), nil
		}
		if len(args) >= 2 && args[len(args)-2] == "crl-maintenance-read" {
			query := args[len(args)-1]
			if strings.Contains(query, "json_agg(document") {
				return json.Marshal([]any{item.Issuer.Document})
			}
			if strings.Contains(query, "json_build_object('prior'") {
				return json.Marshal(item.Snapshot)
			}
			if strings.Contains(query, "pki_crl_acknowledgments") {
				if !strings.Contains(query, "consumer_id='video-cloud-api'") || !strings.Contains(query, "digest='"+item.Snapshot.Prior.Digest+"'") {
					t.Fatal("ACK lost native consumer/digest binding")
				}
				ackChecks++
				return []byte("t"), nil
			}
		}
		t.Fatal("maintenance must not read a private projection or stat the faulted application")
		return nil, nil
	}
	// Discovery must work before KV transport or the failed application can boot.
	source := managedNamedField(deploymentplan.Map(pod["spec"])["containers"], "app")
	identity, err := r.consumerID(t.Context(), ns, "faulted-api", "app", source)
	if err != nil || identity != "video-cloud-api" {
		t.Fatalf("original enrollment subject unavailable without app startup: %q %v", identity, err)
	}
	originalPod := pod
	for _, change := range []struct {
		name   string
		mutate func(deploymentplan.Object)
	}{
		{"unregistered-client-path", func(p deploymentplan.Object) {
			setEnvironmentContainerEnv(managedNamedField(deploymentplan.Map(p["spec"])["containers"], "app"), "TEST_CLIENT_IDENTITY_STATE", "/identity/private/foreign.json")
		}},
		{"foreign-service-account", func(p deploymentplan.Object) { deploymentplan.Map(p["spec"])["serviceAccountName"] = "foreign" }},
		{"foreign-kv-mount", func(p deploymentplan.Object) {
			setEnvironmentContainerEnv(managedNamedField(deploymentplan.Map(p["spec"])["containers"], "app"), "PKI_STATE_KV_MOUNT", "foreign")
		}},
		{"foreign-auth-role", func(p deploymentplan.Object) {
			setEnvironmentContainerEnv(managedNamedField(deploymentplan.Map(p["spec"])["containers"], "app"), "PKI_STATE_OPENBAO_ROLE", "foreign")
		}},
		{"replaced-owner", func(p deploymentplan.Object) {
			deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(p["metadata"])["ownerReferences"])[0])["uid"] = "replaced"
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			pod = deploymentplan.Clone(originalPod)
			defer func() { pod = originalPod }()
			change.mutate(pod)
			candidate := managedNamedField(deploymentplan.Map(pod["spec"])["containers"], "app")
			if _, err := r.consumerID(t.Context(), ns, "faulted-api", "app", candidate); err == nil {
				t.Fatal("accepted foreign original consumer purpose")
			}
		})
	}
	t.Run("multiple-original-client-purposes", func(t *testing.T) {
		pod = deploymentplan.Clone(originalPod)
		defer func() { pod = originalPod }()
		spec := deploymentplan.Map(pod["spec"])
		candidate := managedNamedField(spec["containers"], "app")
		setEnvironmentContainerEnv(candidate, "PKI_REGISTRATION_IDENTITY_STATE", "/registration/private/client.json")
		candidate["volumeMounts"] = append(certIssuerObjectList(candidate["volumeMounts"]), environmentCreateServiceMount("registration", "/registration", false))
		spec["volumes"] = append(certIssuerObjectList(spec["volumes"]), deploymentplan.Object{"name": "registration", "emptyDir": deploymentplan.Object{"medium": "Memory"}})
		for path, want := range map[string]string{"/identity/crl/root.json": "video-cloud-api", "/registration/private/crl/root.json": "logger"} {
			got, err := r.consumerID(t.Context(), ns, "faulted-api", "app", candidate, path)
			if err != nil || got != want {
				t.Fatalf("cache %s resolved %q instead of original purpose %q: %v", path, got, want, err)
			}
		}
		// Exercise the actual observer too: the second cache must not reuse
		// the first purpose's container-level identity-cache entry.
		setEnvironmentContainerEnv(candidate, "PKI_REGISTRATION_CONTROLLER_SERVER_CRL_MANIFEST", "/registration-trust/crls.json")
		candidate["volumeMounts"] = append(certIssuerObjectList(candidate["volumeMounts"]), environmentCreateServiceMount("registration-trust", "/registration-trust", true))
		spec["volumes"] = append(certIssuerObjectList(spec["volumes"]), environmentCreateServiceVolume("registration-trust", "configMap", "registration-crls"))
		originalRead := r.readOverride
		defer func() { r.readOverride = originalRead }()
		r.readOverride = func(ctx context.Context, id string) (deploymentplan.Object, error) {
			if id == "ConfigMap/"+ns+"/registration-crls" {
				return deploymentplan.Object{"data": deploymentplan.Object{"crls.json": environmentCreateServiceCRLManifest(item.Issuer, "/registration/private/crl/root.json")}}, nil
			}
			return originalRead(ctx, id)
		}
		inventory, err := r.observe(t.Context())
		if err != nil {
			t.Fatal("actual multi-purpose observation", err)
		}
		found := map[string]bool{}
		for _, consumer := range inventory.Consumers {
			found[consumer.ConsumerID] = true
		}
		if len(inventory.Consumers) != 2 || !found["video-cloud-api"] || !found["logger"] {
			t.Fatal("observer conflated independently owned client purposes")
		}
		if _, err := r.consumerID(t.Context(), ns, "faulted-api", "app", candidate, "/outside/root.json"); err == nil {
			t.Fatal("guessed a consumer for an unbound multi-purpose cache")
		}
		if _, err := r.consumerID(t.Context(), ns, "faulted-api", "app", candidate); err == nil {
			t.Fatal("accepted ambiguous purposes without cache ownership")
		}
	})
	projection, err := r.publicProjection(t.Context(), pod, source, "/export/client.crl", map[string]environmentCreateRegisteredIssuer{item.Issuer.ID: item.Issuer})
	if err != nil {
		t.Fatal("discover remote public producer before transport or application boot", err)
	}
	if len(projection.Claims) != 0 || projection.Producer != "app" || projection.Original != "" || exportReads != 0 {
		t.Fatal("discovery substituted a PVC, fabricated export or required application startup")
	}
	observedInventory, err := r.observe(t.Context())
	if err != nil {
		t.Fatal("actual remote CRL discovery", err)
	}
	if len(observedInventory.Consumers) != 1 || len(observedInventory.Public) != 1 || !observedInventory.Consumers[0].Custody || observedInventory.Consumers[0].ConsumerID != identity {
		t.Fatal("observed inventory lost original remote storage or consumer")
	}
	originalManifest := manifest
	var entries []json.RawMessage
	if err := json.Unmarshal([]byte(manifest), &entries); err != nil {
		t.Fatal(err)
	}
	duplicate, _ := json.Marshal(append(entries, entries[0]))
	manifest = string(duplicate)
	if _, err := r.publicProjection(t.Context(), pod, source, "/export/client.crl", map[string]environmentCreateRegisteredIssuer{item.Issuer.ID: item.Issuer}); err == nil {
		t.Fatal("ambiguous native public CRL producer manifest accepted")
	}
	manifest = originalManifest
	records := map[string]createRootCRLRecord{item.Issuer.ID: item.Snapshot.Prior}
	if err := r.verifyPublicCRLs(t.Context(), []maintenancePublicCRL{projection}, records); err == nil {
		t.Fatal("missing public reload accepted")
	}
	publicReady = true
	deploymentplan.Map(pod["status"])["containerStatuses"] = []any{deploymentplan.Object{"name": "app", "ready": true}}
	if err := r.verifyPublicCRLs(t.Context(), []maintenancePublicCRL{projection}, records); err != nil {
		t.Fatal("committed cache and actual public reload", err)
	}
	deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["status"])["containerStatuses"])[0])["ready"] = false
	if err := r.verifyPublicCRLs(t.Context(), []maintenancePublicCRL{projection}, records); err == nil {
		t.Fatal("unready public producer accepted")
	}
	deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["status"])["containerStatuses"])[0])["ready"] = true
	// A public file alone does not prove durable remote cache state.
	if err := os.WriteFile(filepath.Join(fixture, "public.json"), []byte(`{"issuer_fingerprint":"foreign"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.verifyPublicCRLs(t.Context(), []maintenancePublicCRL{projection}, records); err == nil {
		t.Fatal("public reload without matching committed KV proof accepted")
	}
	if err := os.WriteFile(filepath.Join(fixture, "public.json"), public, 0600); err != nil {
		t.Fatal(err)
	}
	consumer := crlMaintenanceConsumer{Namespace: ns, Pod: "faulted-api", PodUID: "original-pod", Container: "app", StatePath: "/identity/crl/root.json", IssuerID: item.Issuer.ID}
	if err := r.install(t.Context(), consumer, item.Issuer, item.Snapshot.Prior); err != nil {
		t.Fatal("actual remote maintenance path", err)
	}
	consumer.Custody, consumer.ConsumerID = true, identity
	if err := r.converge(t.Context(), observedInventory, map[string]createRootCRLRecord{item.Issuer.ID: item.Snapshot.Prior}); err != nil {
		t.Fatal("actual remote maintenance convergence", err)
	}
	if ackChecks != 1 {
		t.Fatal("committed KV proof replaced native runtime ACK")
	}
	// The ordinary readiness reader must use the same committed proof and
	// expected subject. A stopped application's private cat is forbidden.
	if err := os.WriteFile(filepath.Join(fixture, "forbid-app-cat"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	latestRow, _ := json.Marshal(map[string]any{"digest": item.Snapshot.Prior.Digest, "number": item.Snapshot.Prior.Number, "ack": true})
	if err := os.WriteFile(filepath.Join(fixture, "sql-latest.json"), latestRow, 0600); err != nil {
		t.Fatal(err)
	}
	compiled := &compiledEnvironmentDeployment{Configuration: cfg, Store: store, Backend: r.backend, Release: environmentRelease{Images: map[string]environmentReleaseImage{"LKE_PKI_CONTROLLER_IMAGE": {Reference: r.image}}}}
	checker := compiled.newCheckRuntime(t.Context())
	if checker.custodyReader == nil || checker.custodyReader.image != r.image || checker.custodyReader.backend.Store.Root != store.Root {
		t.Fatal("formal checker lost selected environment/native image/frozen backend")
	}
	readOnly := r
	readOnly.lease = nil
	checker.custodyReader = &readOnly
	var mountedEntries []livePKICRLManifestEntry
	if err := json.Unmarshal([]byte(manifest), &mountedEntries); err != nil {
		t.Fatal(err)
	}
	entry := mountedEntries[0]
	entry.Backend = "openbao"
	if err := verifyLivePKICRLEvidence("selected", ns, "dev", "api", "app", entry, time.Now().UTC(), checker); err != nil {
		t.Fatal("formal CRL readiness reader still depends on app projection or maintenance ownership", err)
	}
	createsBefore, err := os.ReadFile(filepath.Join(fixture, "creates"))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyLivePKICRLEvidence("selected", ns, "dev", "api", "app", entry, item.Snapshot.Prior.NextUpdate.Add(time.Second), checker); err == nil {
		t.Fatal("cached public inspection bypassed requested expiration cutoff")
	}
	createsAfter, err := os.ReadFile(filepath.Join(fixture, "creates"))
	if err != nil || string(createsAfter) != string(createsBefore) {
		t.Fatal("now/window checks repeated an identical native inspection Job", err)
	}
	// Missing configured context must not fall back to cat of a memory mirror.
	if err := verifyLivePKICRLEvidence("selected", ns, "dev", "api", "app", entry, time.Now().UTC(), newDeploymentCheckRuntime(t.Context(), "dev")); err == nil {
		t.Fatal("remote readiness accepted missing selected custody context")
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
	if _, err := r.consumerID(t.Context(), ns, "faulted-api", "app", source); err == nil {
		t.Fatal("discovery fabricated missing original enrollment evidence")
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
