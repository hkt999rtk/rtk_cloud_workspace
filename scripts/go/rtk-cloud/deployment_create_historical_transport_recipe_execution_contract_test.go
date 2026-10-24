package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This is the seven-resource recipe boundary, using actual guarded stage
// publication and native owner readback. It does not claim the full archived
// source/admission chain of the separate PostManaged lifecycle test.
func TestHistoricalPostManagedTransportRecipesRetainAllSevenAcceptedBodies(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(fmt.Sprintf("historical-pid-%t", legacy), func(t *testing.T) {
			assertHistoricalPostManagedTransportRecipes(t, legacy)
		})
	}
}

func assertHistoricalPostManagedTransportRecipes(t *testing.T, legacy bool) {
	t.Helper()
	f, roots, intermediates, _ := newHistoricalTransportCoordinatorExecutionContract(t)
	original, err := renderEnvironmentCreateTransportDefinitions(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service, roots, intermediates, f.p.Plan.Release)
	if err != nil || len(original) != 7 {
		t.Fatal("maintained seven-resource transport recipe", err, len(original))
	}
	historical := historicalTransportHostDefinitions(original, 2)
	if !legacy {
		for i := range historical {
			if historical[i].Object["kind"] != "StatefulSet" {
				continue
			}
			for _, d := range original {
				if deploymentplan.ID(d.Object) == deploymentplan.ID(historical[i].Object) {
					oldProvider := managedNamedField(environmentPKIPod(historical[i].Object)["containers"], "openbao")
					currentProvider := managedNamedField(environmentPKIPod(d.Object)["containers"], "openbao")
					oldProvider["command"] = slices.Clone(certIssuerObjectList(currentProvider["command"]))
				}
			}
		}
	}
	f.acceptedHistoricalStage(t, "isolated-postmanaged-original-hosts", historical)
	pull := cloneHistoricalTransportDefinitions(historical)
	for i := range pull {
		if pull[i].Object["kind"] == "StatefulSet" {
			environmentPKIPod(pull[i].Object)["imagePullSecrets"] = []any{deploymentplan.Object{"name": lkeImagePullSecretName(f.cfg.Values)}}
		}
	}
	f.acceptedHistoricalStage(t, "isolated-postmanaged-pull-repair", pull)
	audit := cloneHistoricalTransportDefinitions(pull)
	for i := range audit {
		if audit[i].Object["kind"] == "ConfigMap" && deploymentplan.Map(audit[i].Object["metadata"])["name"] == "openbao-file" {
			data := deploymentplan.Map(audit[i].Object["data"])
			data["file.hcl"] = deploymentplan.String(data["file.hcl"]) + managedTransportOriginalAuditBlock
		}
	}
	plan := f.acceptedHistoricalStage(t, "isolated-postmanaged-audit-repair", audit)
	_, journal, err := acceptedCreateStage(f.store, f.e, "kubernetes/isolated-postmanaged-audit-repair")
	if err != nil || validateManagedTransportPostManagedJournal(plan, journal) != nil {
		t.Fatal("actual audit-only accepted seven-owner stage", err)
	}
	partial := &createManagedTransportPartialRecovery{Original: plan, Journal: journal, AdditionalOwners: map[string]deploymentplan.Resource{}, PostManaged: &createManagedTransportPostManaged{Plan: plan, Journal: journal, Session: f.service.BootstrapSessionID}}
	for _, resource := range plan.Resources {
		if resource.ID == "Deployment/"+f.p.Plan.Stack+"-video-cloud/pki-controller" || resource.ID == "ConfigMap/"+f.p.Plan.Stack+"-secrets/pki-create-service-trust" {
			partial.AdditionalOwners[resource.ID] = resource
		}
	}
	r := &createServiceSessionRecovery{Ceremony: f.service, ManagedTransportRecovery: &createManagedTransportRecovery{Partial: partial}}
	runtime := f.runtime
	runtime.ServiceSessionRecovery = r
	// Match the loader's construction from the actual original listener child,
	// instead of borrowing a current managed-controller projection.
	listeners, _, err := loadInitialListenerCheckpoint(f.store, f.e, &f.p, true)
	if err != nil {
		t.Fatal("actual original listener checkpoint", err)
	}
	runtime.InitialListeners = nil
	for _, resource := range listeners.Resources {
		runtime.InitialListeners = append(runtime.InitialListeners, deploymentplan.Definition{Phase: resource.Phase, Object: deploymentplan.Clone(resource.Desired)})
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	writes := deploymentplan.Digest(f.native.writes)
	current, err := renderEnvironmentCreateTransportDefinitions(t.Context(), f.cfg, f.p.Plan.Bootstrap, runtime, r.Ceremony, roots, intermediates, f.p.Plan.Release)
	if err != nil {
		t.Fatal("ordinary current transport renderer", err)
	}
	// Future deployment/finalization still uses the current renderer even when
	// its runtime carries a historical PostManaged model. Only the source
	// reader's observation boundary applies the compatibility adapter.
	for _, definition := range current {
		if definition.Object["kind"] == "StatefulSet" {
			command := certIssuerObjectList(managedNamedField(environmentPKIPod(definition.Object)["containers"], "openbao")["command"])
			if len(command) != 3 || !strings.Contains(deploymentplan.String(command[2]), `printf '%s\n' "$$$$" > /run/openbao-pki/private/openbao.pid`) {
				t.Fatal("historical context downgraded the ordinary current renderer")
			}
		}
	}
	currentDigest := deploymentplan.Digest(current)
	reconstructed, err := preserveManagedTransportPostManagedDefinitions(f.p.Plan.Bootstrap, runtime, r.Ceremony, current)
	if err != nil {
		for _, resource := range plan.Resources {
			if resource.Desired["kind"] == "StatefulSet" {
				t.Logf("original command=%q", managedNamedField(environmentPKIPod(resource.Desired)["containers"], "openbao")["command"])
			}
		}
		for _, definition := range original {
			if definition.Object["kind"] == "StatefulSet" {
				t.Logf("current command=%q", managedNamedField(environmentPKIPod(definition.Object)["containers"], "openbao")["command"])
			}
		}
		t.Fatal("actual retained source renderer", err)
	}
	if deploymentplan.Digest(current) != currentDigest {
		t.Fatal("source observation changed the ordinary rendered recipe")
	}
	candidate := historicalTransportHostPreview(t, f, reconstructed)
	if len(candidate.Resources) != 7 {
		t.Fatal("reconstructed transport resource count", len(candidate.Resources))
	}
	for _, current := range candidate.Resources {
		var prior deploymentplan.Resource
		for _, resource := range plan.Resources {
			if resource.ID == current.ID {
				prior = resource
			}
		}
		differences := historicalTransportRecipeDifferencePaths("", prior.Desired, current.Desired)
		t.Logf("resource=%s action=%s exact_owner=%t differing_paths=%v", current.ID, current.Action, current.UID == journal.Owners[current.ID], differences)
		if len(differences) != 0 || current.Action != "retain" || current.UID != journal.Owners[current.ID] {
			t.Errorf("post-managed re-render changes accepted resource %s", current.ID)
		}
	}
	if err := validateManagedTransportPostManagedDefinitions(partial, reconstructed); err != nil {
		t.Error("exact original post-managed definition guard", err)
	}
	if err := validateManagedTransportPartialForwardActions(partial, candidate); err != nil {
		t.Error("native observation-only action guard", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	if deploymentplan.Digest(f.native.writes) != writes {
		t.Fatal("recipe comparison mutated original native owners")
	}
	// Neither accepted-body drift nor a new renderer change may be hidden by
	// the compatibility branch. Each case is an untrusted input model only;
	// original accepted files and native objects remain untouched.
	for _, mode := range []string{"new-command", "new-image", "new-config", "missing-resource", "source-command", "source-image", "source-owner", "source-not-accepted", "source-session"} {
		t.Run(mode, func(t *testing.T) {
			var badPartial createManagedTransportPartialRecovery
			if err := json.Unmarshal(mustProviderRecoveryJSON(partial), &badPartial); err != nil {
				t.Fatal(err)
			}
			defs := cloneHistoricalTransportDefinitions(original)
			for i := range defs {
				if defs[i].Object["kind"] == "StatefulSet" {
					provider := managedNamedField(environmentPKIPod(defs[i].Object)["containers"], "openbao")
					if mode == "new-command" {
						command := certIssuerObjectList(provider["command"])
						command[2] = deploymentplan.String(command[2]) + "; echo unexpected"
						provider["command"] = command
					}
					if mode == "new-image" {
						provider["image"] = "docker.io/openbao/openbao@sha256:" + strings.Repeat("a", 64)
					}
				}
				if mode == "new-config" && deploymentplan.ID(defs[i].Object) == "ConfigMap/"+f.p.Plan.Stack+"-video-cloud/mqtt-pki-config" {
					deploymentplan.Map(defs[i].Object["data"])["unrelated"] = "changed"
				}
			}
			switch mode {
			case "missing-resource":
				defs = defs[:len(defs)-1]
			case "source-not-accepted":
				badPartial.PostManaged.Journal.Accepted = false
			case "source-session":
				badPartial.PostManaged.Session = "99999999-9999-4999-8999-999999999999"
			case "source-owner":
				badPartial.PostManaged.Journal.Owners["StatefulSet/"+f.p.Plan.Stack+"-secrets/openbao"] = "99999999-9999-4999-8999-999999999999"
			case "source-command", "source-image":
				for i := range badPartial.PostManaged.Plan.Resources {
					resource := &badPartial.PostManaged.Plan.Resources[i]
					if resource.Desired["kind"] != "StatefulSet" {
						continue
					}
					for _, object := range []deploymentplan.Object{resource.Desired, resource.Baseline} {
						provider := managedNamedField(environmentPKIPod(object)["containers"], "openbao")
						if mode == "source-command" {
							command := certIssuerObjectList(provider["command"])
							command[2] = deploymentplan.String(command[2]) + "; echo unexpected"
							provider["command"] = command
						} else {
							provider["image"] = "docker.io/openbao/openbao@sha256:" + strings.Repeat("a", 64)
						}
					}
				}
				badPartial.PostManaged.Journal.PlanSHA256 = badPartial.PostManaged.Plan.SHA256()
			}
			badRuntime := runtime
			badRuntime.ServiceSessionRecovery = &createServiceSessionRecovery{Ceremony: f.service, ManagedTransportRecovery: &createManagedTransportRecovery{Partial: &badPartial}}
			if _, err := preserveManagedTransportPostManagedDefinitions(f.p.Plan.Bootstrap, badRuntime, f.service, defs); err == nil {
				t.Fatal("changed post-managed recipe accepted", mode)
			}
			assertRecoveryContractReadOnly(t, f.store, before)
			if deploymentplan.Digest(f.native.writes) != writes {
				t.Fatal("rejected recipe changed a native owner")
			}
		})
	}
}

func cloneHistoricalTransportDefinitions(source []deploymentplan.Definition) []deploymentplan.Definition {
	out := slices.Clone(source)
	for i := range out {
		out[i].Object = deploymentplan.Clone(source[i].Object)
		out[i].Dependencies = slices.Clone(source[i].Dependencies)
	}
	return out
}

func historicalTransportRecipeDifferencePaths(path string, a, b any) []string {
	if deploymentplan.Digest(a) == deploymentplan.Digest(b) {
		return nil
	}
	left, right := deploymentplan.Map(a), deploymentplan.Map(b)
	if left != nil && right != nil {
		keys := map[string]string{}
		for key := range left {
			keys[key] = key
		}
		for key := range right {
			keys[key] = key
		}
		var out []string
		for _, key := range sortedMapKeys(keys) {
			out = append(out, historicalTransportRecipeDifferencePaths(path+"/"+key, left[key], right[key])...)
		}
		return out
	}
	la, ra := certIssuerObjectList(a), certIssuerObjectList(b)
	if la != nil && ra != nil && len(la) == len(ra) {
		var out []string
		for i := range la {
			out = append(out, historicalTransportRecipeDifferencePaths(fmt.Sprintf("%s/%d", path, i), la[i], ra[i])...)
		}
		return out
	}
	return []string{strings.TrimPrefix(path, "/")}
}
