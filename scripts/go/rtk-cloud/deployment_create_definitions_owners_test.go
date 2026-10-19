package main

import (
	"encoding/json"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func createDefinitionOwnersFixture(t *testing.T) (*deploymentplan.Plan, []deploymentplan.Object) {
	t.Helper()
	stack := "video-cloud-dev"
	meta := func(name, uid string) deploymentplan.Object {
		m := deploymentplan.Object{"name": name, "namespace": stack + "-video-cloud", "labels": deploymentplan.Object{"rtk.realtek.com/stack": stack, "app.kubernetes.io/managed-by": "rtk-cloud"}}
		if uid != "" {
			m["uid"], m["resourceVersion"] = uid, "7"
		}
		return m
	}
	wanted := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": meta("owned", ""), "spec": deploymentplan.Object{"replicas": float64(2), "selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": "owned"}}, "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app", "image": "registry.example/app@sha256:" + strings.Repeat("a", 64)}}}}}}
	live := deploymentplan.Clone(wanted)
	live["metadata"] = meta("owned", "11111111-1111-4111-8111-111111111111")
	deploymentplan.Map(live["spec"])["replicas"] = float64(1)
	claim := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": meta("original-data", "22222222-2222-4222-8222-222222222222"), "spec": deploymentplan.Object{"storageClassName": "linode-block-storage-retain", "volumeName": "original-pv", "accessModes": []any{"ReadWriteOnce"}, "resources": deploymentplan.Object{"requests": deploymentplan.Object{"storage": "5Gi"}}}}
	newConfig := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": meta("new-output", ""), "data": deploymentplan.Object{"public": "declared"}}
	defs := []deploymentplan.Definition{{Object: wanted, Phase: "workload"}, {Object: deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": meta("original-data", "")}, Phase: "storage", Retain: true}, {Object: newConfig, Phase: "configuration"}}
	p, err := deploymentplan.Compile(deploymentplan.Inputs{Operation: "create", Environment: "dev", Stack: stack, ConfigurationSHA256: deploymentplan.Digest("same-configuration"), ReleaseSHA256: deploymentplan.Digest("same-release"), BindingsSHA256: deploymentplan.Digest("same-bindings")}, defs, []deploymentplan.Object{live, claim})
	if err != nil {
		t.Fatal(err)
	}
	return p, []deploymentplan.Object{live, claim}
}

func TestCreateDefinitionsPreserveFullPlanOwnersWhenScoped(t *testing.T) {
	full, live := createDefinitionOwnersFixture(t)
	before, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	defs := environmentCreateDefinitions(full)
	in := full.Inputs
	in.Operation = "scoped"
	scoped, err := deploymentplan.Compile(in, defs, live)
	if err != nil {
		t.Fatal("same original owners could not be scoped", err)
	}
	byID := map[string]deploymentplan.Resource{}
	for _, resource := range scoped.Resources {
		byID[resource.ID] = resource
	}
	for i, original := range full.Resources {
		got := byID[original.ID]
		if got.UID != original.UID || got.Action != original.Action || defs[i].Retain != (original.Action == "retain") || defs[i].Retire != (original.Action == "retire") {
			t.Fatal("scoped conversion changed original owner/action", original.ID)
		}
		meta := deploymentplan.Map(defs[i].Object["metadata"])
		if original.UID != "" && meta["uid"] != original.UID {
			t.Fatal("recorded owner was not pinned on the cloned definition", original.ID)
		}
		if original.Action == "create" && meta["uid"] != nil {
			t.Fatal("unallocated create invented an owner")
		}
	}
	// Mutating returned definitions must never rewrite the immutable full plan.
	deploymentplan.Map(defs[0].Object["metadata"])["uid"] = "changed-copy"
	after, _ := json.Marshal(full)
	if string(before) != string(after) {
		t.Fatal("adapter or scoped compilation rewrote original Plan/Desired bytes")
	}
}

func TestCreateDefinitionsRefuseChangedOriginalOwnerSnapshots(t *testing.T) {
	for _, kind := range []string{"Deployment", "PersistentVolumeClaim"} {
		for _, mode := range []string{"replacement", "missing", "foreign-stack", "unbound"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				full, live := createDefinitionOwnersFixture(t)
				var changed []deploymentplan.Object
				for _, object := range live {
					object = deploymentplan.Clone(object)
					if object["kind"] == kind {
						meta := deploymentplan.Map(object["metadata"])
						switch mode {
						case "replacement":
							meta["uid"] = "99999999-9999-4999-8999-999999999999"
						case "missing":
							continue
						case "foreign-stack":
							deploymentplan.Map(meta["labels"])["rtk.realtek.com/stack"] = "video-cloud-foreign"
						case "unbound":
							delete(meta, "uid")
						}
					}
					changed = append(changed, object)
				}
				in := full.Inputs
				in.Operation = "scoped"
				if _, err := deploymentplan.Compile(in, environmentCreateDefinitions(full), changed); err == nil {
					t.Fatal("scoped conversion adopted an unknown original owner")
				}
			})
		}
	}
}
