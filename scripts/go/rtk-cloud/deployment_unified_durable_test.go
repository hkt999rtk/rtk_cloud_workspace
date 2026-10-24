package main

import (
	"errors"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func durableEnvironmentFixture(environment string) ([]deploymentplan.Definition, map[string]string) {
	stack := "video-cloud-" + environment
	definitions := []deploymentplan.Definition{{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": stack + "-platform"}}, Phase: "namespace"}}
	for _, name := range []string{"redis", "video-cloud-prometheus"} {
		container, path := "redis", "/data"
		if name != "redis" {
			container, path = "prometheus", "/prometheus"
		}
		definitions = append(definitions, deploymentplan.Definition{Phase: "workload", Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": name, "namespace": stack + "-platform"}, "spec": map[string]any{"replicas": float64(1), "selector": map[string]any{"matchLabels": map[string]any{"app": name}}, "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": container, "image": "fixture@sha256:" + strings.Repeat("a", 64), "volumeMounts": []any{map[string]any{"name": "data", "mountPath": path}}}}, "volumes": []any{map[string]any{"name": "data", "emptyDir": map[string]any{}}}}}}}})
	}
	return definitions, map[string]string{"CLOUD_ENV_NAME": environment, "CLOUD_STACK_NAME": stack, "REDIS_DATA_STORAGE": "5Gi", "PROMETHEUS_DATA_STORAGE": "20Gi"}
}

func durablePod(object deploymentplan.Object) deploymentplan.Object {
	return deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])["spec"])
}

func TestCommonDurableStorageCreatesPersistentTargetsAndRetainsAllocations(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			definitions, env := durableEnvironmentFixture(environment)
			definitions, err := applyEnvironmentDurableStorage(definitions, env)
			if err != nil {
				t.Fatal(err)
			}
			input := deploymentplan.Inputs{Environment: environment, Stack: env["CLOUD_STACK_NAME"], Operation: "create", ConfigurationSHA256: "config", ReleaseSHA256: "release", BindingsSHA256: "bindings"}
			if err := validateEnvironmentDurableStorageTransition(definitions, nil); err != nil {
				t.Fatal("fresh target cannot initialize", err)
			}
			fresh, err := deploymentplan.Compile(input, definitions, nil)
			if err != nil {
				t.Fatal(err)
			}
			var live []deploymentplan.Object
			for _, resource := range fresh.Resources {
				object := deploymentplan.Clone(resource.Desired)
				meta := deploymentplan.Map(object["metadata"])
				meta["uid"], meta["resourceVersion"] = "owned-"+resource.ID, "7"
				if object["kind"] == "PersistentVolumeClaim" {
					spec := deploymentplan.Map(object["spec"])
					spec["volumeName"], spec["storageClassName"], spec["volumeMode"] = "existing-volume-"+deploymentplan.String(meta["name"]), "selected-retain-class", "Filesystem"
					object["status"] = map[string]any{"phase": "Bound"}
				} else if object["kind"] == "Deployment" {
					if deploymentplan.Map(deploymentplan.Map(object["spec"])["strategy"])["type"] != "Recreate" {
						t.Fatal("multiple RWO writers could overlap")
					}
					pod := durablePod(object)
					volume := deploymentplan.Map(certIssuerObjectList(pod["volumes"])[0])
					if volume["emptyDir"] != nil || deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"] != deploymentplan.String(meta["name"])+"-data" {
						t.Fatal("data is still ephemeral")
					}
					if meta["name"] == "redis" && deploymentplan.Digest(deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])["args"]) != deploymentplan.Digest([]any{"--dir", "/data", "--appendonly", "yes", "--appendfsync", "everysec"}) {
						t.Fatal("Redis has no durable AOF policy")
					}
				}
				live = append(live, object)
			}
			if err := validateEnvironmentDurableStorageTransition(definitions, live); err != nil {
				t.Fatal("matched persistent state rejected", err)
			}
			input.Operation = "upgrade"
			upgrade, err := deploymentplan.Compile(input, definitions, live)
			if err != nil {
				t.Fatal(err)
			}
			for _, resource := range upgrade.Resources {
				if resource.Desired["kind"] != "PersistentVolumeClaim" {
					continue
				}
				spec := deploymentplan.Map(resource.Desired["spec"])
				if spec["volumeName"] == nil || spec["storageClassName"] != "selected-retain-class" || spec["volumeMode"] != "Filesystem" || resource.Action != "retain" {
					t.Fatal("ordinary update lost the existing PVC allocation", resource.ID)
				}
			}
		})
	}
}

func TestDurableStorageTransitionNeverInitializesReplacementData(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"ephemeral", "different-claim", "missing-claim", "unbound", "terminating", "wrong-mount", "subpath"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				definitions, env := durableEnvironmentFixture(environment)
				definitions, err := applyEnvironmentDurableStorage(definitions, env)
				if err != nil {
					t.Fatal(err)
				}
				deployment := deploymentplan.Clone(definitions[1].Object)
				pod := durablePod(deployment)
				claim := deploymentplan.Clone(definitions[3].Object)
				claim["status"] = map[string]any{"phase": "Bound"}
				volume := deploymentplan.Map(certIssuerObjectList(pod["volumes"])[0])
				mount := deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])["volumeMounts"])[0])
				switch mode {
				case "ephemeral":
					delete(volume, "persistentVolumeClaim")
					volume["emptyDir"] = map[string]any{}
				case "different-claim":
					deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"] = "another-data"
				case "unbound":
					deploymentplan.Map(claim["status"])["phase"] = "Pending"
				case "terminating":
					deploymentplan.Map(claim["metadata"])["deletionTimestamp"] = "2026-01-01T00:00:00Z"
				case "wrong-mount":
					mount["mountPath"] = "/other-data"
				case "subpath":
					mount["subPath"] = "another-database"
				}
				live := []deploymentplan.Object{deployment}
				if mode != "missing-claim" {
					live = append(live, claim)
				}
				err = validateEnvironmentDurableStorageTransition(definitions, live)
				var blocker *deploymentplan.Blocker
				if !errors.As(err, &blocker) || blocker.ID != "deployment.migration.durable-storage" {
					t.Fatal("unsafe storage transition was allowed", err)
				}
			})
		}
	}
}

func TestDurableStorageRejectsMissingIntentAndMultipleWriters(t *testing.T) {
	for _, mode := range []string{"missing-size", "invalid-size", "multiple-writers", "missing-volume", "missing-container", "wrong-mount"} {
		t.Run(mode, func(t *testing.T) {
			definitions, env := durableEnvironmentFixture("dev")
			pod := durablePod(definitions[1].Object)
			switch mode {
			case "missing-size":
				delete(env, "REDIS_DATA_STORAGE")
			case "invalid-size":
				env["REDIS_DATA_STORAGE"] = "-1Gi"
			case "multiple-writers":
				deploymentplan.Map(definitions[1].Object["spec"])["replicas"] = float64(2)
			case "missing-volume":
				delete(pod, "volumes")
			case "missing-container":
				delete(pod, "containers")
			case "wrong-mount":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])["volumeMounts"])[0])["mountPath"] = "/other-data"
			}
			if _, err := applyEnvironmentDurableStorage(definitions, env); err == nil {
				t.Fatal("invalid durable target accepted")
			}
		})
	}
}
