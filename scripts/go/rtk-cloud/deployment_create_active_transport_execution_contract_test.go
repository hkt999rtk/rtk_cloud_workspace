package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func crlAcceptedTransportOwnersFixture(t *testing.T, mode string) (*crlExecutorFixture, map[string]environmentCreateTransport, map[string]environmentCreateTransport, freshStageBackend) {
	t.Helper()
	f := newCRLExecutorFixture(t)
	e := f.execution
	ns := e.Journal.Stack + "-video-cloud"
	old, current := map[string]environmentCreateTransport{}, map[string]environmentCreateTransport{}
	for _, role := range []string{"management", "openbao"} {
		names := map[string][]string{"openbao": {"openbao.isolated.svc"}}
		if role == "management" {
			names = map[string][]string{}
			for i := 0; i < 7; i++ {
				name := fmt.Sprintf("original-client-%d", i)
				names[name] = []string{name + ".isolated.svc"}
			}
		}
		initial, err := createEnvironmentTransport(f.store, e.Journal.PlanSHA256, role, names, time.Now().UTC().Add(-4*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		original, key, err := renewInitialTransportMaterial(initial, time.Now().UTC().Add(-2*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		renewed, err := renewActiveTransportSameKey(original, key, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		old[role], current[role] = original, renewed
	}
	defs := []deploymentplan.Definition{}
	add := func(kind, name string, fields deploymentplan.Object) {
		body := deploymentplan.Object{"apiVersion": "v1", "kind": kind, "metadata": deploymentplan.Object{"name": name, "namespace": ns}, "data": deploymentplan.Object{}}
		for k, v := range fields {
			body[k] = v
		}
		body, _ = initialTransportOverride(body, old)
		phase := "configuration"
		if kind == "Deployment" {
			body["apiVersion"] = "apps/v1"
			phase = "workload"
		}
		defs = append(defs, deploymentplan.Definition{Object: body, Phase: phase})
	}
	for i := 0; i < 7; i++ {
		name := fmt.Sprintf("original-client-%d", i)
		add("Secret", "pki-create-"+name+"-transport", nil)
	}
	add("Secret", "openbao-transport-tls", nil)
	add("ConfigMap", "pki-create-transport-trust", nil)
	add("ConfigMap", "pki-openbao-transport-ca", nil)
	roots := "managed-root\n" + old["openbao"].CACert
	if mode == "duplicated-old-CA" {
		roots += "\n" + old["openbao"].CACert
	}
	add("ConfigMap", "pki-create-openbao-tls-consumer-trust", deploymentplan.Object{"data": deploymentplan.Object{"roots.pem": roots, "unrelated": "preserved"}})
	for _, name := range []string{"pki-controller", "certissuer"} {
		add("Deployment", name, deploymentplan.Object{"spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app": name}}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": name, "image": "synthetic-original-image"}}}}}})
	}
	stage := "transport-owner-source"
	p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: e.Journal.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest("isolated-original-release"), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs)}, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	memory := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}}}
	if err := executeEnvironmentCreateStagePlan(t.Context(), f.store, e, stage, p, memory, func(ctx context.Context, p *deploymentplan.Plan) error {
		return p.AdmitResumable(ctx, memory, f.store.Root)
	}, func(ctx context.Context, p *deploymentplan.Plan) error {
		return memory.CheckParentExecution(ctx, p, f.store.Root)
	}, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, memory) }, func(ctx context.Context) error { return e.Check(ctx) }, io.Discard); err != nil {
		t.Fatal("actual accepted source producer", err)
	}

	return f, old, current, memory
}

func TestCRLExecutionContractActiveTransportOwnersUseAcceptedStage(t *testing.T) {
	for _, mode := range []string{"accepted", "replacement-owner", "unreviewed-body", "missing-controller", "duplicated-old-CA", "changed-parent-intent"} {
		t.Run(mode, func(t *testing.T) {
			f, old, current, memory := crlAcceptedTransportOwnersFixture(t, mode)
			e := f.execution
			ns := e.Journal.Stack + "-video-cloud"
			state := f.state(t)
			for id, body := range memory.objects {
				state.Objects[id] = body
			}
			id := "Deployment/" + ns + "/pki-controller"
			switch mode {
			case "replacement-owner":
				deploymentplan.Map(state.Objects[id]["metadata"])["uid"] = "foreign"
			case "unreviewed-body":
				deploymentplan.Map(state.Objects[id]["spec"])["replicas"] = float64(7)
			case "missing-controller":
				delete(state.Objects, id)
			case "changed-parent-intent":
				e.Journal.Entries[0].IntentSHA256 = strings.Repeat("f", 64)
				if err := e.save(); err != nil {
					t.Fatal(err)
				}
			}
			f.writeState(t, state)
			resources, err := activeTransportOwners(t.Context(), f.cfg, f.store, e, f.backend, old, current)
			if mode != "accepted" {
				if err == nil {
					t.Fatal("unqualified owner/body admitted")
				}
				return
			}
			if err != nil || len(resources) != 13 {
				t.Fatal("accepted owners unavailable", len(resources), err)
			}
			for _, r := range resources {
				actual := memory.objects[r.ID]
				if r.UID != deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) || !deploymentplan.Matches(r.Before, actual) {
					t.Fatal("owner or original body lost", r.ID)
				}
				if r.Desired["kind"] == "Secret" && deploymentplan.Map(r.Before["data"])["tls.key"] != deploymentplan.Map(r.Desired["data"])["tls.key"] {
					t.Fatal("serving key changed", r.ID)
				}
			}
			after := f.state(t)
			if after.Imports != 0 || after.SecretWrites != 0 {
				t.Fatal("owner qualification mutated native state")
			}
		})
	}
}

func TestCRLExecutionContractActiveTransportMaterialBindsOriginalKeys(t *testing.T) {
	for _, mode := range []string{"complete", "missing-original", "changed-original", "changed-candidate-CA", "changed-candidate-key", "missing-managed-identity", "escaping-candidate"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			parent := strings.Repeat("a", 64)
			review := strings.Repeat("b", 64)
			candidate := "deployment/repairs/active-transport-candidate"
			now := time.Now().UTC()
			proof := initialTransportRenewal{Schema: "rtk-cloud.initial-transport-renewal/v1", Parent: parent, Review: review, Completed: true, Material: map[string]string{}, Owners: map[string]string{}}
			for i := 0; i < 10; i++ {
				proof.Owners[fmt.Sprintf("original-resource-%d", i)] = fmt.Sprintf("original-owner-%d", i)
			}
			write := func(path string, body []byte) {
				t.Helper()
				if err := store.write(path, body, false); err != nil {
					t.Fatal(err)
				}
			}
			for _, role := range []string{"management", "openbao"} {
				original, err := createEnvironmentTransport(store, parent, role, map[string][]string{role: {role + ".isolated.svc"}}, now.Add(-4*time.Hour))
				if err != nil {
					t.Fatal(err)
				}
				prior, key, err := renewInitialTransportMaterial(original, now.Add(-2*time.Hour))
				if err != nil {
					t.Fatal(err)
				}
				renewed, err := renewActiveTransportSameKey(prior, key, now)
				if err != nil {
					t.Fatal(err)
				}
				originalPath := filepath.Join("deployment/create/material", parent, role+"-transport/identities.json")
				raw, err := os.ReadFile(filepath.Join(store.Root, originalPath))
				if err != nil {
					t.Fatal(err)
				}
				proof.Material[originalPath] = environmentPreflightBytesSHA256(raw)
				for name, body := range map[string][]byte{"identities.json": mustProviderRecoveryJSON(prior), "ca.crt": []byte(prior.CACert), "ca.key": key} {
					rel := filepath.Join("deployment/create/transport-renewal", review, role+"-transport", name)
					write(rel, body)
					proof.Material[rel] = environmentPreflightBytesSHA256(body)
				}
				if mode == "changed-candidate-key" && role == "openbao" {
					leaf := renewed.Identities[role]
					leaf.Key = "foreign"
					renewed.Identities[role] = leaf
				}
				write(filepath.Join(candidate, role+"-transport/identities.json"), mustProviderRecoveryJSON(renewed))
				write(filepath.Join(candidate, role+"-transport/ca.crt"), []byte(renewed.CACert))
			}
			raw := mustProviderRecoveryJSON(proof)
			write(filepath.Join("deployment/create/material", parent, "transport-renewal.json"), raw)
			write(filepath.Join("deployment/create/transport-renewal", review, "completed.json"), raw)
			for _, rel := range []string{"openbao/init.json", "openbao/root-token", "openbao/unseal-key", "pki/service/issuer.json", "pki/mqtt/issuer.json", "pki/openbao_tls/issuer.json", "pki/device/issuer.json", "pki/app/issuer.json"} {
				write(rel, []byte("original isolated identity: "+rel))
			}
			originalPath := filepath.Join(store.Root, "deployment/create/material", parent, "management-transport/identities.json")
			switch mode {
			case "missing-original":
				if err := os.Remove(originalPath); err != nil {
					t.Fatal(err)
				}
			case "changed-original":
				if err := os.WriteFile(originalPath, []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed-candidate-CA":
				if err := os.WriteFile(filepath.Join(store.Root, candidate, "openbao-transport/ca.crt"), []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-managed-identity":
				if err := os.Remove(filepath.Join(store.Root, "pki/device/issuer.json")); err != nil {
					t.Fatal(err)
				}
			case "escaping-candidate":
				candidate = "deployment/repairs/../foreign"
			}
			before := map[string]string{}
			for rel := range proof.Material {
				raw, _ := os.ReadFile(filepath.Join(store.Root, rel))
				before[rel] = environmentPreflightBytesSHA256(raw)
			}
			old, current, got, material, keys, err := activeTransportReadMaterial(store, parent, candidate)
			if mode != "complete" {
				if err == nil {
					t.Fatal("changed/missing original custody accepted")
				}
			} else {
				if err != nil || got.Review != review || len(material) != 21 || len(keys) != 2 {
					t.Fatal("complete original material rejected", len(material), err)
				}
				for role, value := range old {
					for name, leaf := range value.Identities {
						if current[role].Identities[name].Key != leaf.Key {
							t.Fatal("original serving key replaced")
						}
					}
				}
			}
			for rel, want := range before {
				raw, _ := os.ReadFile(filepath.Join(store.Root, rel))
				if environmentPreflightBytesSHA256(raw) != want {
					t.Fatal("read-only qualification changed original custody", rel)
				}
			}
		})
	}
}

// A committed Kubernetes write may lose its response. Resume must reconcile the
// exact admitted body and UID, preserve every original, and avoid claiming
// completion while the final native reload outcome remains unavailable.
func TestCRLExecutionContractActiveTransportUncertainWriteResume(t *testing.T) {
	for _, stop := range []int{0, 1, 7, 13} {
		t.Run(fmt.Sprintf("after-write-%d", stop), func(t *testing.T) {
			f, old, current, memory := crlAcceptedTransportOwnersFixture(t, "accepted")
			state := f.state(t)
			for id, body := range memory.objects {
				state.Objects[id] = body
			}
			f.writeState(t, state)
			resources, err := activeTransportOwners(t.Context(), f.cfg, f.store, f.execution, f.backend, old, current)
			if err != nil {
				t.Fatal(err)
			}
			plan := activeTransportRenewalPlan{Schema: activeTransportRenewalSchema, Parent: f.execution.Journal.PlanSHA256, OwnerSHA: deploymentplan.Digest(f.execution.owner), JournalSHA: deploymentplan.Digest(f.execution.Journal), OriginalProofSHA: deploymentplan.Digest(old), ConfigurationSHA: deploymentplan.Digest(f.cfg), Resources: resources}
			artifact := activeTransportRenewalArtifact{Schema: activeTransportRenewalSchema, Plan: plan}
			state.FailWriteNumber = stop
			f.writeState(t, state)
			err = executeActiveTransportRenewal(t.Context(), f.cfg, f.store, f.execution, f.backend, artifact, current, io.Discard)
			if err == nil {
				t.Fatal("unknown final native outcome marked complete")
			}
			first := f.state(t)
			want := stop
			if stop == 0 {
				want = 13
			}
			if first.SecretWrites != want || first.LeaseCreates != first.LeaseDeletes {
				t.Fatal("wrong committed write or leaked lease", first.SecretWrites, first.LeaseCreates, first.LeaseDeletes)
			}
			first.FailWriteNumber = 0
			f.writeState(t, first)
			err = executeActiveTransportRenewal(t.Context(), f.cfg, f.store, f.execution, f.backend, artifact, current, io.Discard)
			if err == nil {
				t.Fatal("final native outage ignored")
			}
			after := f.state(t)
			if after.SecretWrites != 13 || after.Reloads < 1 || after.LeaseCreates != after.LeaseDeletes {
				t.Fatal("resume rewrote committed objects or skipped native reload", after.SecretWrites, after.Reloads)
			}
			review := deploymentplan.Digest(plan)
			for _, r := range resources {
				actual := after.Objects[r.ID]
				if !deploymentplan.Matches(r.Desired, actual) || deploymentplan.Map(actual["metadata"])["uid"] != r.UID {
					t.Fatal("original owner or desired body lost", r.ID)
				}
				raw, err := os.ReadFile(filepath.Join(f.store.Root, "deployment/create/transport-renewal", review, "original", strings.ReplaceAll(r.ID, "/", "-")+".json"))
				if err != nil || deploymentplan.Digest(json.RawMessage(raw)) != deploymentplan.Digest(memory.objects[r.ID]) {
					t.Fatal("original body was replaced on resume", r.ID, err)
				}
				if r.Desired["kind"] == "Secret" && deploymentplan.Map(actual["data"])["tls.key"] != deploymentplan.Map(r.Before["data"])["tls.key"] {
					t.Fatal("serving key rotated", r.ID)
				}
			}
			completed := 0
			for _, entry := range f.execution.Journal.Entries {
				if strings.HasPrefix(entry.Key, "active-transport-renewal-"+review+"-") && entry.State == "completed" && len(entry.Receipt) > 0 {
					completed++
				}
				if entry.Key == "active-transport-renewal-"+review && entry.State != "pending" {
					t.Fatal("unknown final outcome committed parent")
				}
			}
			if completed != 13 {
				t.Fatal("resumed resource receipts missing", completed)
			}
			if _, err := os.Stat(filepath.Join(f.store.Root, "deployment/create/material", plan.Parent, "active-transport-renewal.json")); !os.IsNotExist(err) {
				t.Fatal("premature completion pointer", err)
			}
		})
	}
}
