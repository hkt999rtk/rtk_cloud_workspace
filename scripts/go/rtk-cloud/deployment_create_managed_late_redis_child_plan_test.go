package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestManagedLateRedisAllMaintainedChildrenCompileAndPinOwners(t *testing.T) {
	defs, err := decodeEnvironmentCreateManifests(lkeRedisDeploymentManifest(map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": "video-cloud-dev"}))
	if err != nil || len(defs) != 1 {
		t.Fatal(err)
	}
	original := defs[0].Object
	uid := "11111111-1111-4111-8111-111111111111"
	image := "valkey/valkey@sha256:" + strings.Repeat("a", 64)
	deploymentplan.Map(certIssuerObjectList(environmentPKIPod(original)["containers"])[0])["image"] = image
	deploymentplan.Map(original["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-dev", "app.kubernetes.io/managed-by": "rtk-cloud"}
	live := deploymentplan.Clone(original)
	deploymentplan.Map(live["metadata"])["uid"] = uid
	deploymentplan.Map(live["metadata"])["resourceVersion"] = "7"
	d := &createManagedLateRedisDurable{ParentSHA: strings.Repeat("b", 64), ResourceID: deploymentplan.ID(original), UID: uid, Image: image, Original: deploymentplan.Resource{ID: deploymentplan.ID(original), UID: uid, Desired: original}}
	cfg := deploymentConfig{Values: map[string]string{"REDIS_DATA_STORAGE": "5Gi", "DURABLE_DATA_STORAGE_CLASS": "selected-retain"}}
	claim, helper, switched, err := managedLateRedisRecipes(d, cfg)
	if err != nil {
		t.Fatal(err)
	}
	in := deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest("cfg"), ReleaseSHA256: deploymentplan.Digest("release"), BindingsSHA256: deploymentplan.Digest(d)}
	compile := func(def deploymentplan.Definition, objects []deploymentplan.Object) *deploymentplan.Plan {
		t.Helper()
		p, e := deploymentplan.Compile(in, []deploymentplan.Definition{def}, objects)
		if e != nil {
			t.Fatal(e)
		}
		return p
	}
	claimPlan := compile(deploymentplan.Definition{Object: claim, Phase: "storage"}, []deploymentplan.Object{live})
	helperPlan := compile(deploymentplan.Definition{Object: helper, Phase: "workload"}, []deploymentplan.Object{live})
	if claimPlan.Resources[0].Action != "create" || helperPlan.Resources[0].Action != "create" {
		t.Fatal("originally absent allocations not create")
	}
	target := deploymentplan.Clone(helperPlan.Resources[0].Desired)
	deploymentplan.Map(target["metadata"])["uid"] = "22222222-2222-4222-8222-222222222222"
	deploymentplan.Map(target["metadata"])["resourceVersion"] = "9"
	retired := deploymentplan.Definition{Object: deploymentplan.Projection(target), Phase: "retirement", Retire: true}
	retirement := compile(retired, []deploymentplan.Object{live, target})
	if retirement.Resources[0].Action != "retire" || retirement.Resources[0].UID != deploymentplan.Map(target["metadata"])["uid"] {
		t.Fatal("retirement did not pin original helper")
	}
	stopped := deploymentplan.Clone(original)
	deploymentplan.Map(stopped["metadata"])["uid"] = uid
	deploymentplan.Map(stopped["spec"])["replicas"] = float64(0)
	stop := compile(deploymentplan.Definition{Object: stopped, Phase: "workload"}, []deploymentplan.Object{live})
	if stop.Resources[0].Action != "update" || stop.Resources[0].UID != uid {
		t.Fatal("stop replaced original Redis")
	}
	stoppedLive := deploymentplan.Clone(stopped)
	deploymentplan.Map(stoppedLive["metadata"])["resourceVersion"] = "10"
	switchPlan := compile(deploymentplan.Definition{Object: switched, Phase: "workload"}, []deploymentplan.Object{stoppedLive})
	if switchPlan.Resources[0].Action != "update" || switchPlan.Resources[0].UID != uid {
		t.Fatal("switch replaced stopped original Redis")
	}
	if body, e := switchPlan.Resources[0].Body(stoppedLive); e != nil || deploymentplan.Map(body["metadata"])["uid"] != uid || deploymentplan.Map(body["metadata"])["resourceVersion"] != "10" {
		t.Fatal("native switch lost UID/version fence", e)
	}
	for _, mode := range []string{"missing-writer", "replacement-writer", "replacement-helper", "wrong-retire-phase", "foreign-pod", "changed-pod", "missing-dependency", "later-dependency", "ordinary-create", "ordinary-upgrade"} {
		t.Run(mode, func(t *testing.T) {
			input := in
			definition := deploymentplan.Definition{Object: deploymentplan.Clone(switched), Phase: "workload"}
			objects := []deploymentplan.Object{deploymentplan.Clone(stoppedLive)}
			var definitions []deploymentplan.Definition
			switch mode {
			case "missing-writer":
				objects = nil
			case "replacement-writer":
				deploymentplan.Map(objects[0]["metadata"])["uid"] = "33333333-3333-4333-8333-333333333333"
			case "replacement-helper":
				definition = retired
				objects = []deploymentplan.Object{deploymentplan.Clone(target)}
				deploymentplan.Map(objects[0]["metadata"])["uid"] = "33333333-3333-4333-8333-333333333333"
			case "wrong-retire-phase":
				definition = retired
				definition.Phase = "workload"
				objects = []deploymentplan.Object{target}
			case "foreign-pod":
				definition = deploymentplan.Definition{Object: deploymentplan.Clone(helper), Phase: "workload"}
				deploymentplan.Map(definition.Object["metadata"])["namespace"] = "foreign"
				objects = nil
			case "changed-pod":
				definition = deploymentplan.Definition{Object: deploymentplan.Clone(helper), Phase: "workload"}
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(definition.Object["spec"])["containers"])[0])["image"] = "foreign@sha256:" + strings.Repeat("f", 64)
				objects = []deploymentplan.Object{target}
			case "missing-dependency":
				definition = deploymentplan.Definition{Object: helper, Phase: "workload", Dependencies: []string{deploymentplan.ID(claim)}}
				objects = nil
			case "later-dependency":
				definition = deploymentplan.Definition{Object: helper, Phase: "workload", Dependencies: []string{deploymentplan.ID(claim)}}
				definitions = []deploymentplan.Definition{{Object: claim, Phase: "routing"}}
				objects = nil
			case "ordinary-create", "ordinary-upgrade":
				input.Operation = strings.TrimPrefix(mode, "ordinary-")
				definition = deploymentplan.Definition{Object: helper, Phase: "workload"}
				objects = nil
			}
			definitions = append(definitions, definition)
			if _, e := deploymentplan.Compile(input, definitions, objects); e == nil {
				t.Fatal("unsafe scoped child accepted", mode)
			}
		})
	}
}

func TestManagedLateRedisDataReadbackAllowsOnlyExpectedDataWithNativeInjection(t *testing.T) {
	image := "valkey/valkey@sha256:" + strings.Repeat("a", 64)
	pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "spec": deploymentplan.Object{"volumes": []any{deploymentplan.Object{"name": "data", "persistentVolumeClaim": deploymentplan.Object{"claimName": "redis-data"}}, deploymentplan.Object{"name": "kube-api-access-native", "projected": deploymentplan.Object{"sources": []any{deploymentplan.Object{"serviceAccountToken": deploymentplan.Object{"path": "token"}}}}}}, "containers": []any{deploymentplan.Object{"name": "redis", "image": image, "volumeMounts": []any{deploymentplan.Object{"name": "data", "mountPath": "/data"}, deploymentplan.Object{"name": "kube-api-access-native", "mountPath": "/var/run/secrets/kubernetes.io/serviceaccount", "readOnly": true}}}}}}
	if !managedLateRedisDataPodMatches(pod, image) {
		t.Fatal("normal Kubernetes projected mount rejected valid Redis data claim")
	}
	for _, mode := range []string{"wrong-claim", "emptydir", "readonly-claim", "wrong-path", "readonly-mount", "subpath", "subpath-expr", "duplicate-data-volume", "duplicate-data-mount", "other-data-mount", "other-data-submount", "wrong-image"} {
		t.Run(mode, func(t *testing.T) {
			changed := deploymentplan.Clone(pod)
			spec := deploymentplan.Map(changed["spec"])
			volumes := certIssuerObjectList(spec["volumes"])
			data := deploymentplan.Map(volumes[0])
			container := deploymentplan.Map(certIssuerObjectList(spec["containers"])[0])
			mounts := certIssuerObjectList(container["volumeMounts"])
			mount := deploymentplan.Map(mounts[0])
			switch mode {
			case "wrong-claim":
				deploymentplan.Map(data["persistentVolumeClaim"])["claimName"] = "other"
			case "emptydir":
				delete(data, "persistentVolumeClaim")
				data["emptyDir"] = deploymentplan.Object{}
			case "readonly-claim":
				deploymentplan.Map(data["persistentVolumeClaim"])["readOnly"] = true
			case "wrong-path":
				mount["mountPath"] = "/other"
			case "readonly-mount":
				mount["readOnly"] = true
			case "subpath":
				mount["subPath"] = "partial"
			case "subpath-expr":
				mount["subPathExpr"] = "partial"
			case "duplicate-data-volume":
				spec["volumes"] = append(volumes, deploymentplan.Clone(data))
			case "duplicate-data-mount":
				container["volumeMounts"] = append(mounts, deploymentplan.Clone(mount))
			case "other-data-mount":
				container["volumeMounts"] = append(mounts, deploymentplan.Object{"name": "other", "mountPath": "/data"})
			case "other-data-submount":
				container["volumeMounts"] = append(mounts, deploymentplan.Object{"name": "other", "mountPath": "/data/appendonlydir"})
			case "wrong-image":
				container["image"] = "foreign@sha256:" + strings.Repeat("f", 64)
			}
			if managedLateRedisDataPodMatches(changed, image) {
				t.Fatal("unproved data mount accepted", mode)
			}
		})
	}
}
