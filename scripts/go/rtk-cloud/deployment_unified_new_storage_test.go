package main

import (
	"testing"

	"gopkg.in/yaml.v3"

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

func TestFleetValkeyUpgradeBindsOriginalImmutableClaim(t *testing.T) {
	for _, change := range []string{"compatible", "different-owner", "different-size", "different-mode", "different-access", "different-class", "different-data-source"} {
		t.Run(change, func(t *testing.T) {
			const stack = "video-cloud-dev"
			const uid = "11111111-1111-4111-8111-111111111111"
			var object deploymentplan.Object
			if err := yaml.Unmarshal([]byte(lkeFleetValkeyStatefulSetManifest(map[string]string{
				"CLOUD_STACK_NAME": stack, "LKE_FLEET_VALKEY_IMAGE": "docker.io/valkey/valkey:maintained",
			})), &object); err != nil {
				t.Fatal(err)
			}
			object = deploymentplan.Clone(object)
			current := deploymentplan.Clone(object)
			meta := deploymentplan.Map(current["metadata"])
			meta["uid"], meta["resourceVersion"] = uid, "42"
			spec := deploymentplan.Map(current["spec"])
			claim := deploymentplan.Map(certIssuerObjectList(spec["volumeClaimTemplates"])[0])
			claim["apiVersion"], claim["kind"] = "v1", "PersistentVolumeClaim"
			claim["status"] = deploymentplan.Object{"phase": "Pending"}
			claimSpec := deploymentplan.Map(claim["spec"])
			claimSpec["storageClassName"], claimSpec["volumeMode"] = "linode-block-storage-retain", "Filesystem"
			spec["podManagementPolicy"] = "OrderedReady"
			mqttFoundationPrimary(current)["image"] = "docker.io/valkey/valkey:original"
			desiredSpec := deploymentplan.Map(object["spec"])
			desiredClaim := deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(desiredSpec["volumeClaimTemplates"])[0])["spec"])
			bindings := environmentIdentityBindings{Stack: stack, Owners: map[string]string{deploymentplan.ID(object): uid}}
			switch change {
			case "different-owner":
				meta["uid"] = "22222222-2222-4222-8222-222222222222"
			case "different-size":
				deploymentplan.Map(deploymentplan.Map(desiredClaim["resources"])["requests"])["storage"] = "21Gi"
			case "different-mode":
				claimSpec["volumeMode"] = "Block"
			case "different-access":
				claimSpec["accessModes"] = []any{"ReadWriteMany"}
			case "different-class":
				desiredClaim["storageClassName"] = "another-class"
			case "different-data-source":
				claimSpec["dataSource"] = deploymentplan.Object{"kind": "PersistentVolumeClaim", "name": "unexpected"}
			}
			before := deploymentplan.Digest(current)
			definitions := []deploymentplan.Definition{{Phase: "workload", Object: object}}
			err := bindFleetValkeyAllocations(definitions, []deploymentplan.Object{current}, bindings)
			if change != "compatible" {
				if err == nil {
					t.Fatal("immutable allocation change passed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: stack, Operation: "upgrade",
				ConfigurationSHA256: deploymentplan.Digest("config"), ReleaseSHA256: deploymentplan.Digest("release"), BindingsSHA256: deploymentplan.Digest(bindings),
			}, definitions, []deploymentplan.Object{current}, bindings.Owners)
			if err != nil || len(p.Resources) != 1 || p.Resources[0].Action != "update" {
				t.Fatalf("Fleet mutable image update was not compiled: %v", err)
			}
			body, err := p.Resources[0].Body(current)
			if err != nil || deploymentplan.Digest(deploymentplan.Map(body["spec"])["volumeClaimTemplates"]) != deploymentplan.Digest(spec["volumeClaimTemplates"]) ||
				mqttFoundationPrimary(body)["image"] != mqttFoundationPrimary(object)["image"] || deploymentplan.Digest(current) != before {
				t.Fatalf("update changed immutable allocation or lost desired image: %v", err)
			}
		})
	}
}
