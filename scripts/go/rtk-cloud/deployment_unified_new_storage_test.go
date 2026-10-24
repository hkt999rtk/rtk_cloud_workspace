package main

import (
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestNewStorageClassPinsOnlyNewAllocationsAndPreservesOriginalTemplates(t *testing.T) {
	makeSet := func(name string) deploymentplan.Definition {
		return deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"name": name, "namespace": "video-cloud-dev-support"}, "spec": deploymentplan.Object{"replicas": float64(1), "volumeClaimTemplates": []any{deploymentplan.Object{"metadata": deploymentplan.Object{"name": "data"}, "spec": deploymentplan.Object{"accessModes": []any{"ReadWriteOnce"}}}}}}, Phase: "workload"}
	}
	original, newSet := makeSet("original"), makeSet("new")
	originalSHA := deploymentplan.Digest(original)
	claim := deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": "new-direct", "namespace": "video-cloud-dev-support"}, "spec": deploymentplan.Object{"accessModes": []any{"ReadWriteOnce"}}}, Phase: "storage"}
	defs := []deploymentplan.Definition{original, newSet, claim}
	owners := map[string]string{deploymentplan.ID(original.Object): "11111111-1111-4111-8111-111111111111"}
	if err := bindEnvironmentNewStorageClasses(defs, "linode-block-storage-retain", owners); err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Digest(defs[0]) != originalSHA {
		t.Fatal("existing immutable template was rewritten")
	}
	spec := deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(defs[1].Object["spec"])["volumeClaimTemplates"])[0])["spec"])
	if spec["storageClassName"] != "linode-block-storage-retain" || deploymentplan.Map(defs[2].Object["spec"])["storageClassName"] != "linode-block-storage-retain" {
		t.Fatal("new direct/template allocation remained dependent on cluster default")
	}
	for _, mode := range []string{"no-selected-class", "foreign-declared-class"} {
		d := makeSet("blocked")
		class := ""
		if mode == "foreign-declared-class" {
			class = "selected-retain"
			deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(d.Object["spec"])["volumeClaimTemplates"])[0])["spec"])["storageClassName"] = "foreign-class"
		}
		if err := bindEnvironmentNewStorageClasses([]deploymentplan.Definition{d}, class, nil); err == nil {
			t.Fatal("missing/foreign new allocation class was accepted")
		}
	}
}
