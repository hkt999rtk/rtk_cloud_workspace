package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func newNormalHydratedAliasFixture(t *testing.T) *normalPostgresAccessFixture {
	t.Helper()
	defs := managedMQTTConsumersFixture(t)
	for i := range defs {
		name := deploymentplan.String(deploymentplan.Map(defs[i].Object["metadata"])["name"])
		defs[i].Retain = name != "video-cloud-api-pki" && name != "video-cloud-mqttfoundation" && name != "video-cloud-shadowworker"
	}
	for _, claim := range []string{"video-cloud-api-pki-controller-identity", "pki-product-mqtt-identity", "pki-product-shadow-identity"} {
		defs = append(defs, deploymentplan.Definition{Retain: true, Object: deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": claim, "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"accessModes": []any{"ReadWriteOnce"}}}})
	}
	f := newNormalPostgresAccessFixture(t, defs)
	if err := f.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	return f
}

func publishRejectedAliasForward(t *testing.T, f *normalPostgresAccessFixture) (*createNormalTailWorkloadForward, []byte) {
	t.Helper()
	d := publishNormalWorkloadForwardFixture(t, f)
	raw, _ := json.Marshal(d.Target.Plan)
	var old deploymentplan.Plan
	if json.Unmarshal(raw, &old) != nil {
		t.Fatal("clone")
	}
	defs := make([]deploymentplan.Definition, len(f.workload.Resources))
	for i, r := range f.workload.Resources {
		defs[i] = deploymentplan.Definition{Object: deploymentplan.Clone(r.Desired)}
	}
	if err := bindEnvironmentManagedMQTTConsumersLegacy(defs); err != nil {
		t.Fatal(err)
	}
	for i := range defs {
		old.Resources[i].Desired = defs[i].Object
	}
	d.Target.Plan = &old
	_, j, err := readCreateNormalTailWorkloadOriginal(f.store, f.h.Execution, f.workload)
	if err != nil || validateCreateNormalTailWorkloadForwardLegacyPlan(f.workload, j, &old) != nil {
		t.Fatal("legacy derivation", err)
	}
	if validateCreateNormalTailWorkloadForwardPlan(f.workload, j, &old) == nil {
		t.Fatal("rejected duplicate mount accepted as corrected")
	}
	raw = mustProviderRecoveryJSON(d)
	rel, _ := filepath.Rel(f.store.Root, createNormalTailWorkloadForwardPath(f.h.Execution))
	if err := f.store.write(rel, raw, true); err != nil {
		t.Fatal(err)
	}
	return d, raw
}

func hydrateAliasForward(t *testing.T, f *normalPostgresAccessFixture) (*compiledEnvironmentDeployment, error) {
	t.Helper()
	// Only canonical snapshot IO/backend construction are substituted. All frozen
	// wrapper/configuration pins, descriptor selection and hydration run in the
	// same production function used by the native entry point.
	return hydrateCreateNormalTailTargetWithInputLoader(t.Context(), f.cfg, f.store, f.h, nil, func() (string, environmentKubernetesBackend, error) {
		return f.workload.ConfigurationSHA256, environmentKubernetesBackend{}, nil
	})
}

func TestCreateNormalTailHydratedForwardAliasRefreshAndClosure(t *testing.T) {
	f := newNormalHydratedAliasFixture(t)
	old, raw := publishRejectedAliasForward(t, f)
	originalPath := filepath.Join(f.store.Root, "deployment/rollback", f.workload.SHA256(), "journal.json")
	before, _ := os.ReadFile(originalPath)
	c, err := hydrateAliasForward(t, f)
	if err != nil || c.Plan.SHA256() != old.Target.Plan.SHA256() {
		t.Fatal("hydrate exact unstarted rejected input", err)
	}
	noInventory := func(context.Context, string) ([]deploymentplan.Object, error) {
		t.Fatal("saved descriptor must not fetch another inventory")
		return nil, nil
	}
	c, err = prepareCreateNormalTailWorkloadForwardWithInventory(t.Context(), f.cfg, f.h, c, noInventory)
	if err != nil {
		t.Fatal("real prepare refresh", err)
	}
	if c.Plan.SHA256() == old.Target.Plan.SHA256() {
		t.Fatal("rejected plan was reused")
	}
	archived, err := os.ReadFile(filepath.Join(f.h.Execution.Directory, "rejected", environmentPreflightBytesSHA256(raw)+".json"))
	if err != nil || !bytes.Equal(archived, raw) {
		t.Fatal("rejected raw input was not preserved", err)
	}
	if err := checkEnvironmentManagedMQTTConsumers(t.Context(), c.Plan, f.b.Read); err != nil {
		t.Fatal("real native owner/file checker", err)
	}
	for _, r := range c.Plan.Resources {
		for _, raw := range certIssuerObjectList(environmentPKIPod(r.Desired)["containers"]) {
			seen := map[string]bool{}
			for _, v := range certIssuerObjectList(deploymentplan.Map(raw)["volumeMounts"]) {
				path := deploymentplan.String(deploymentplan.Map(v)["mountPath"])
				if seen[path] {
					t.Fatal("duplicate mountPath", r.ID, path)
				}
				seen[path] = true
			}
		}
	}
	again, err := hydrateAliasForward(t, f)
	if err != nil || again.Plan.SHA256() != c.Plan.SHA256() {
		t.Fatal("new canonical input not selected", err)
	}
	again, err = prepareCreateNormalTailWorkloadForwardWithInventory(t.Context(), f.cfg, f.h, again, noInventory)
	if err != nil || again.Plan.SHA256() != c.Plan.SHA256() {
		t.Fatal("canonical retry changed target", err)
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.h.Execution.Check(ctx) }
	if err := executeCreateNormalTailForwardWorkloadBackend(t.Context(), f.cfg, f.store, f.h, c, f.b, check, check, check, io.Discard, func(context.Context) error { return nil }); err != nil {
		t.Fatal("real corrected execute/composite", err)
	}
	after, _ := os.ReadFile(originalPath)
	if !bytes.Equal(before, after) {
		t.Fatal("original native journal changed")
	}
	if err := executeCreateNormalTailSequence(t.Context(), f.h.Execution, []func(context.Context) error{func(ctx context.Context) error { return f.h.Execution.Check(ctx) }}); err != nil {
		t.Fatal("original closure", err)
	}
}

func TestCreateNormalTailHydratedForwardRefreshRefusesStartedOrForeign(t *testing.T) {
	for _, mode := range []string{"parent", "rollback-directory", "saved-input", "lease", "foreign-desired", "archive-changed"} {
		t.Run(mode, func(t *testing.T) {
			f := newNormalHydratedAliasFixture(t)
			d, raw := publishRejectedAliasForward(t, f)
			e := f.h.Execution
			switch mode {
			case "parent":
				intent, _ := environmentCreateKubernetesStageIntent(d.Target.Plan)
				if err := e.BeginEntry("kubernetes/"+d.ForwardStage, d.ForwardStage, intent, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			case "rollback-directory":
				if err := ensurePrivateDirectory(filepath.Join(f.store.Root, "deployment/rollback", d.Target.Plan.SHA256())); err != nil {
					t.Fatal(err)
				}
			case "saved-input":
				path := createNormalTailStagePlanPath(e, d.ForwardStage)
				if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "lease":
				e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, deploymentplan.LeaseOwner{Holder: d.Target.Plan.SHA256() + ":00000000000000000000000000000001"})
			case "foreign-desired":
				d.Target.Plan.Resources[len(f.workload.Resources)].Desired["spec"] = deploymentplan.Object{}
				rel, _ := filepath.Rel(f.store.Root, createNormalTailWorkloadForwardPath(e))
				if err := f.store.write(rel, mustProviderRecoveryJSON(d), true); err != nil {
					t.Fatal(err)
				}
				raw, _ = os.ReadFile(createNormalTailWorkloadForwardPath(e))
			case "archive-changed":
				path := filepath.Join(e.Directory, "rejected", environmentPreflightBytesSHA256(raw)+".json")
				if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			c := &compiledEnvironmentDeployment{Store: f.store, Plan: d.Target.Plan}
			_, err := prepareCreateNormalTailWorkloadForwardWithInventory(t.Context(), f.cfg, f.h, c, func(context.Context, string) ([]deploymentplan.Object, error) {
				t.Fatal("unexpected inventory")
				return nil, nil
			})
			if err == nil {
				t.Fatal("started or foreign input refreshed")
			}
			current, _ := os.ReadFile(createNormalTailWorkloadForwardPath(e))
			if !bytes.Equal(current, raw) {
				t.Fatal("rejected refresh replaced input")
			}
		})
	}
}

func TestCreateNormalTailHydratedForwardArchiveBeforeReplacementResumes(t *testing.T) {
	for _, mode := range []string{"archive-complete", "temporary-partial"} {
		t.Run(mode, func(t *testing.T) {
			f := newNormalHydratedAliasFixture(t)
			d, raw := publishRejectedAliasForward(t, f)
			path := filepath.Join(f.h.Execution.Directory, "rejected", environmentPreflightBytesSHA256(raw)+".json")
			if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
				t.Fatal(err)
			}
			data := raw
			if mode == "temporary-partial" {
				path = filepath.Join(filepath.Dir(path), ".rejected-workload-interrupted")
				data = raw[:len(raw)/2]
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			c := &compiledEnvironmentDeployment{Store: f.store, Plan: d.Target.Plan}
			if _, err := prepareCreateNormalTailWorkloadForwardWithInventory(t.Context(), f.cfg, f.h, c, func(context.Context, string) ([]deploymentplan.Object, error) {
				t.Fatal("unexpected inventory")
				return nil, nil
			}); err != nil {
				t.Fatal("archive interruption resume", err)
			}
		})
	}
}

func TestCreateNormalTailHydratedForwardLegacyCannotBeginExecution(t *testing.T) {
	f := newNormalHydratedAliasFixture(t)
	d, raw := publishRejectedAliasForward(t, f)
	c, err := hydrateAliasForward(t, f)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.h.Execution.Check(ctx) }
	if err := executeCreateNormalTailForwardWorkloadBackend(t.Context(), f.cfg, f.store, f.h, c, f.b, check, check, check, io.Discard, func(context.Context) error { return nil }); err == nil {
		t.Fatal("rejected target began native child")
	}
	if err := createNormalTailWorkloadForwardUnstarted(f.store, f.h.Execution, d); err != nil {
		t.Fatal("rejected target created request/lease/child evidence", err)
	}
	after, _ := os.ReadFile(createNormalTailWorkloadForwardPath(f.h.Execution))
	if !bytes.Equal(raw, after) {
		t.Fatal("backend bypass changed rejected input")
	}
}

func TestCreateNormalTailHydratedForwardConfigurationDriftStopsBeforeRefresh(t *testing.T) {
	f := newNormalHydratedAliasFixture(t)
	_, raw := publishRejectedAliasForward(t, f)
	_, err := hydrateCreateNormalTailTargetWithInputLoader(t.Context(), f.cfg, f.store, f.h, nil, func() (string, environmentKubernetesBackend, error) {
		return deploymentplan.Digest("changed canonical configuration"), environmentKubernetesBackend{}, nil
	})
	if err == nil {
		t.Fatal("changed canonical inputs hydrated")
	}
	after, _ := os.ReadFile(createNormalTailWorkloadForwardPath(f.h.Execution))
	if !bytes.Equal(raw, after) {
		t.Fatal("drift hydration changed rejected input")
	}
}
