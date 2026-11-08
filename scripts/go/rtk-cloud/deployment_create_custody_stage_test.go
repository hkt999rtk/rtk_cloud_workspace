package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateCustodyStageUsesOriginalScopeBeforePlanCompilation(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			_, bootstrap, _, service := createServiceRecipeFixture(t, environment)
			cfg, store, operator, artifact := createStateFixture(t, environment)
			e, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operator, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			// Isolated projection boundary; complete admission is covered separately.
			cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
			generation := e.Journal.PlanSHA256
			binding, err := createEnvironmentCustodyBinding(cfg, bootstrap, generation, "")
			if err != nil {
				t.Fatal(err)
			}
			parent := &environmentCreatePlan{Environment: environment, Stack: bootstrap.Stack, Bootstrap: bootstrap}
			runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: generation, Custody: binding}
			relativeCA := filepath.Join("deployment", "create", "material", generation, "openbao-transport", "ca.crt")
			runtime.OpenBao.CACertPath = filepath.Join(store.Root, relativeCA)
			if err := store.write(relativeCA, []byte(service.Root.Certificate), false); err != nil {
				t.Fatal(err)
			}
			trust, err := renderEnvironmentCustodyTrust(cfg, generation, service.Root.Certificate, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.write(filepath.Join("deployment", "create", generation, "custody", "transport.json"), []byte(deploymentplan.String(deploymentplan.Map(trust[0].Object["data"])["transport.json"])), false); err != nil {
				t.Fatal(err)
			}
			intent := deploymentplan.Object{"schema": "rtk.custody.bootstrap/v1", "environment": environment, "generation": generation, "mount": "identities", "cas_required": true, "owners": binding.Owners}
			body, _ := json.Marshal(intent)
			if err := store.write(filepath.Join("deployment", "create", generation, "custody", "owners.json"), body, false); err != nil {
				t.Fatal(err)
			}
			if err := e.BeginEntry("workload-custody-initialize", "bootstrap", body, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			// A record written before native config readback cannot start a workload.
			owner := binding.Owners[0]
			path := "/identity/private/client.json"
			definitions := []deploymentplan.Definition{{Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": owner.Namespace, "name": "test-owner"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"metadata": deploymentplan.Object{}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "owner", "image": "fixture", "env": environmentCreateEnv(map[string]string{"TEST_IDENTITY_STATE": path}), "volumeMounts": []any{environmentCreateServiceMount("identity", "/identity", false)}}}, "volumes": []any{environmentCreateServiceVolume("identity", "persistentVolumeClaim", owner.StorageAliases[0])}}}}}}}
			ctx := withEnvironmentCreateBootstrapRuntime(context.Background(), runtime)
			if _, err := projectEnvironmentCreateCustodyStage(ctx, cfg, store, parent, e, definitions, nil); err == nil {
				t.Fatal("pending custody config started its owner")
			}
			receipt, _ := json.Marshal(map[string]string{"scope_sha256": environmentPreflightBytesSHA256(body)})
			if err := e.RecordReceipt("workload-custody-initialize", receipt); err != nil {
				t.Fatal(err)
			}
			if err := e.CompleteEntry("workload-custody-initialize", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			for _, recovering := range []bool{false, true} {
				if recovering {
					runtime.Custody = nil
				}
				ctx = withEnvironmentCreateBootstrapRuntime(context.Background(), runtime)
				projected, err := projectEnvironmentCreateCustodyStage(ctx, cfg, store, parent, e, definitions, nil)
				if err != nil {
					t.Fatal("original custody stage", err)
				}
				found := false
				for _, d := range projected {
					pod := environmentPKIPod(d.Object)
					if pod == nil {
						continue
					}
					for _, raw := range certIssuerObjectList(pod["containers"]) {
						c := deploymentplan.Map(raw)
						if mqttFoundationEnv(c, "PKI_STATE_RECORD_PREFIX") != environment+"/"+generation+"/"+owner.ID {
							t.Fatal("stage lost original generation")
						}
						if mqttFoundationEnv(c, "PKI_STATE_BOOTSTRAP_ROOT_SHA256") == "" {
							t.Fatal("creation TLS dependency was not retained")
						}
						found = true
					}
					for _, raw := range certIssuerObjectList(pod["volumes"]) {
						if deploymentplan.Map(raw)["persistentVolumeClaim"] != nil {
							t.Fatal("stage kept identity PVC")
						}
					}
				}
				if !found {
					t.Fatal("native custody owner missing")
				}
			}
			// Global TLS handoff cannot rewrite an already frozen early child.
			ctx = withEnvironmentCreateBootstrapRuntime(context.Background(), runtime)
			early, err := projectEnvironmentCreateCustodyStage(ctx, cfg, store, parent, e, definitions, nil)
			if err != nil {
				t.Fatal(err)
			}
			frozen := &deploymentplan.Plan{}
			for _, d := range early {
				frozen.Resources = append(frozen.Resources, deploymentplan.Resource{Desired: d.Object})
			}
			_, _, _, next := createServiceRecipeFixture(t, environment)
			managed := next.Root
			managed.Environment, managed.Domain, managed.Kind, managed.Status = environment, "openbao_tls", "root", "active"
			finalTrust, err := renderEnvironmentCustodyTrust(cfg, generation, service.Root.Certificate, &managed, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.write(filepath.Join("deployment", "create", generation, "custody", "transport.json"), []byte(deploymentplan.String(deploymentplan.Map(finalTrust[0].Object["data"])["transport.json"])), true); err != nil {
				t.Fatal(err)
			}
			replayed, err := projectEnvironmentCreateCustodyStage(ctx, cfg, store, parent, e, definitions, frozen)
			if err != nil || deploymentplan.Digest(replayed) != deploymentplan.Digest(early) {
				t.Fatal("TLS handoff changed frozen child inputs", err)
			}
			final, err := projectEnvironmentCreateCustodyStage(ctx, cfg, store, parent, e, definitions, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range final {
				pod := environmentPKIPod(d.Object)
				if pod == nil {
					continue
				}
				for _, raw := range certIssuerObjectList(pod["containers"]) {
					c := deploymentplan.Map(raw)
					if mqttFoundationEnv(c, "PKI_STATE_BOOTSTRAP_ROOT_SHA256") != "" || mqttFoundationEnv(c, "PKI_STATE_TRANSPORT_TRUST") != "" || mqttFoundationEnv(c, "PKI_STATE_OPENBAO_SERVER_PKI_ROOT_SHA256") != managed.RootFingerprint {
						t.Fatal("normal stage retained bootstrap authorization")
					}
				}
			}
			if _, err := projectEnvironmentCreateCustodyStage(context.Background(), cfg, store, parent, e, definitions, nil); err == nil {
				t.Fatal("owned stage accepted missing original generation context")
			}
			cfg.Values["PKI_STATE_KV_MOUNT"] = "different"
			if _, err := projectEnvironmentCreateCustodyStage(ctx, cfg, store, parent, e, definitions, nil); err == nil {
				t.Fatal("stage changed original mount")
			}
		})
	}
}

func TestCreateCustodyNativeStageDoesNotApplyBeforeConfiguration(t *testing.T) {
	f := newNativeCreateStageContract(t, "normal", func(path string) {
		writeTestFile(t, path, readTestFile(t, path)+"\nPKI_STATE_BACKEND=openbao\nPKI_STATE_KV_MOUNT=identities\nPKI_STATE_LEASE_DURATION=30s\n")
	})
	products, err := environmentCreateProductIdentityIntent(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.parent.Bootstrap = environmentCreateBootstrapPlan{Environment: f.cfg.Environment, Stack: f.parent.Stack, ProductIdentities: products}
	f.definitions = []deploymentplan.Definition{{Phase: "storage", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": environmentCreateServiceOwnerClaim("pki-controller"), "namespace": f.parent.Stack + "-video-cloud"}}}}
	if err := f.apply(t); err == nil {
		t.Fatal("formal stage applied a workload before its native custody configuration")
	}
	if state := f.read(t); state.Writes != 0 || state.Leases != 0 {
		t.Fatal("failed custody preparation reached Kubernetes mutation")
	}
}
