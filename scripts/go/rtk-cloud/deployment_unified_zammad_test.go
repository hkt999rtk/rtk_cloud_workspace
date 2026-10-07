package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func zammadRenderFixture(t *testing.T, environment string) (deploymentConfig, secretStore, environmentRelease) {
	t.Helper()
	makeIsolatedTestSecretStore(t, environment)
	store, err := newSecretStore("", environment)
	if err != nil {
		t.Fatal(err)
	}
	root := mustWorkspaceRoot(t)
	values, err := readStrictEnv(filepath.Join(root, "cloud_deploy", "architectures", "kubernetes", "architecture.env"))
	if err != nil {
		t.Fatal(err)
	}
	values["SUPPORT_TICKETS_ENABLED"] = "true"
	values["CLOUD_STACK_NAME"] = "video-cloud-" + environment
	cfg := deploymentConfig{Workspace: root, Environment: environment, Values: values}
	_, _, release := completeEnvironmentRenderFixture(environment)
	for _, role := range environmentProviderImageKeys {
		if strings.HasPrefix(role, "LKE_ZAMMAD_") {
			release.Images[role] = environmentReleaseImage{Reference: values[environmentProviderImageConfigKey(role)]}
		}
	}
	return cfg, store, release
}

func TestZammadImageRolesSeparateCoreAndSupportProviders(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		cfg, _, release := zammadRenderFixture(t, environment)
		core := deploymentplan.Object{"kind": "Deployment", "metadata": map[string]any{"name": "postgresql", "namespace": release.Stack + "-platform", "uid": "core-owner"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "postgres", "image": release.Images["LKE_POSTGRES_IMAGE"].Reference}}}}}}
		support := deploymentplan.Clone(core)
		deploymentplan.Map(support["metadata"])["name"] = "zammad-postgres"
		deploymentplan.Map(support["metadata"])["namespace"] = release.Stack + "-support"
		deploymentplan.Map(support["metadata"])["uid"] = "support-owner"
		deploymentplan.Map(certIssuerObjectList(environmentZammadPod(support)["containers"])[0])["image"] = cfg.Values["ZAMMAD_POSTGRES_IMAGE"]
		bindings, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{core, support})
		if err != nil {
			t.Fatal(err)
		}
		if len(bindings.Pods) != 1 || bindings.Owners[deploymentplan.ID(support)] != "support-owner" {
			t.Fatal("chart support settings were imported into private bindings")
		}
		defs := []deploymentplan.Definition{{Object: core}, {Object: support}}
		if err := pinEnvironmentResourceImages(defs, release); err != nil {
			t.Fatal(err)
		}
		for _, item := range []struct {
			object deploymentplan.Object
			role   string
		}{{core, "LKE_POSTGRES_IMAGE"}, {support, "LKE_ZAMMAD_POSTGRES_IMAGE"}} {
			if deploymentplan.Map(certIssuerObjectList(environmentZammadPod(item.object)["containers"])[0])["image"] != release.Images[item.role].Reference {
				t.Fatal("core and support provider versions were mixed")
			}
		}
		deploymentplan.Map(certIssuerObjectList(environmentZammadPod(support)["containers"])[0])["image"] = "unmaintained.example/worker:latest"
		if validateZammadInventoryImages(support, release) == nil {
			t.Fatal("unknown support image accepted")
		}
	}
}

func TestZammadInitJobRetirementRequiresCompletedOriginalOwner(t *testing.T) {
	_, _, release := zammadRenderFixture(t, "dev")
	job := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": "zammad-init-old", "namespace": release.Stack + "-support", "uid": "original-job", "resourceVersion": "2"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "init", "image": release.Images["LKE_ZAMMAD_IMAGE"].Reference}}}}}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Complete", "status": "True"}}}}
	defs := []deploymentplan.Definition{{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": release.Stack + "-support"}}, Phase: "namespace"}}
	retired, err := retireCompletedZammadInitJobs(defs, []deploymentplan.Object{job}, release)
	if err != nil || len(retired) != 2 || !retired[1].Retire {
		t.Fatal("completed old Job not explicitly retired", err)
	}
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: release.Stack, Operation: "upgrade", ConfigurationSHA256: deploymentplan.Digest("cfg"), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest("owners")}, retired, []deploymentplan.Object{job}, map[string]string{deploymentplan.ID(job): "original-job"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Resources[1].Action != "retire" || plan.Resources[1].UID != "original-job" {
		t.Fatal("retirement lost the original UID fence")
	}
	job["status"] = map[string]any{}
	if _, err := retireCompletedZammadInitJobs(defs, []deploymentplan.Object{job}, release); err == nil {
		t.Fatal("pending original migration silently discarded")
	}
}

func TestZammadInputsFenceChartAndTopology(t *testing.T) {
	cfg, _, _ := zammadRenderFixture(t, "dev")
	values, chart, err := readEnvironmentZammadInputs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	before := environmentDeploymentSnapshot{ZammadValues: values, ZammadChart: chart}
	after := before
	after.ZammadValues = append(append([]byte{}, values...), []byte("\n# changed topology\n")...)
	if before.Digest(cfg) == after.Digest(cfg) {
		t.Fatal("topology drift did not invalidate input qualification")
	}
	cfg.Values["ZAMMAD_CHART_SHA256"] = strings.Repeat("0", 64)
	if _, _, err := readEnvironmentZammadInputs(cfg); err == nil {
		t.Fatal("changed chart archive accepted")
	}
}

func TestZammadCompletedInitializerRequiresSuccessfulRuntimeImages(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-image", "failed-process", "missing-pod"} {
		t.Run(mode, func(t *testing.T) {
			digest := "sha256:" + strings.Repeat("a", 64)
			image := "ghcr.io/zammad/zammad@" + digest
			spec := map[string]any{"containers": []any{map[string]any{"name": "init", "image": image}}}
			job := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": "zammad-init-checked", "namespace": "video-cloud-dev-support", "uid": "job-owner", "labels": map[string]any{"rtk.realtek.com/stack": "video-cloud-dev", "app.kubernetes.io/managed-by": "rtk-cloud"}}, "spec": map[string]any{"template": map[string]any{"spec": spec}}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Complete", "status": "True"}}}}
			pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "zammad-init-pod", "namespace": "video-cloud-dev-support", "uid": "pod-owner", "ownerReferences": []any{map[string]any{"apiVersion": "batch/v1", "kind": "Job", "name": "zammad-init-checked", "uid": "job-owner", "controller": true}}}, "spec": spec, "status": map[string]any{"phase": "Succeeded", "containerStatuses": []any{map[string]any{"name": "init", "containerID": "containerd://init", "imageID": "docker-pullable://" + image, "state": map[string]any{"terminated": map[string]any{"exitCode": float64(0)}}}}}}
			if mode == "wrong-image" {
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["status"])["containerStatuses"])[0])["imageID"] = "sha256:" + strings.Repeat("b", 64)
			}
			if mode == "failed-process" {
				deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["status"])["containerStatuses"])[0])["state"])["terminated"])["exitCode"] = float64(1)
			}
			objects := []deploymentplan.Object{job, pod}
			if mode == "missing-pod" {
				objects = objects[:1]
			}
			plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "upgrade", ConfigurationSHA256: deploymentplan.Digest("cfg"), ReleaseSHA256: deploymentplan.Digest(image), BindingsSHA256: deploymentplan.Digest("owners")}, []deploymentplan.Definition{{Object: deploymentplan.Projection(job), Phase: "migration"}}, objects)
			if err != nil {
				t.Fatal(err)
			}
			appPlan, appObjects, _ := targetWorkloadFixture(t, "dev", "StatefulSet")
			plan.Resources = append(plan.Resources, appPlan.Resources...)
			objects = append(objects, appObjects...)
			err = verifyEnvironmentTargetWorkloads(context.Background(), plan, objects, func(context.Context, string) (map[string]bool, error) { return map[string]bool{digest: true}, nil })
			if (err == nil) != (mode == "valid") {
				t.Fatal("initializer runtime verification differs", err)
			}
		})
	}
}

func TestFixedOfficialZammadChartCompilesSharedCompletePlan(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("native Helm renderer requires Helm")
	}
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, store, release := zammadRenderFixture(t, environment)
			values, chart, err := readEnvironmentZammadInputs(cfg)
			if err != nil {
				t.Fatal(err)
			}
			definitions, err := renderEnvironmentZammad(context.Background(), cfg, store, release, "upgrade", values, chart)
			if err != nil {
				t.Fatal(err)
			}
			again, err := renderEnvironmentZammad(context.Background(), cfg, store, release, "upgrade", values, chart)
			if err != nil || deploymentplan.Digest(definitions) != deploymentplan.Digest(again) {
				t.Fatal("preflight/deployment target is nondeterministic", err)
			}
			ids := map[string]bool{}
			var live []deploymentplan.Object
			for _, d := range definitions {
				object := deploymentplan.Clone(d.Object)
				id := deploymentplan.ID(object)
				ids[id] = true
				meta := deploymentplan.Map(object["metadata"])
				meta["uid"] = "owner-" + deploymentplan.String(meta["name"])
				meta["resourceVersion"] = "1"
				if object["kind"] == "Job" {
					continue
				}
				live = append(live, object)
				for _, ref := range environmentResourceReferences(object) {
					if ref.Kind == "Secret" || ref.Kind == "PersistentVolumeClaim" || ref.Name == "default" {
						if ids[ref.ID] {
							continue
						}
						ids[ref.ID] = true
						live = append(live, deploymentplan.Object{"apiVersion": "v1", "kind": ref.Kind, "metadata": map[string]any{"name": ref.Name, "namespace": ref.Namespace, "uid": "original-" + ref.Name, "resourceVersion": "1"}, "data": map[string]any{"key": "retained-credential"}})
					}
				}
			}
			for _, name := range []string{"zammad-memcached", "zammad-redis", "zammad-nginx", "zammad-railsserver", "zammad-scheduler", "zammad-websocket"} {
				if !ids["Deployment/"+release.Stack+"-support/"+name] {
					t.Fatal("missing enabled support workload", name)
				}
			}
			for _, name := range []string{"zammad-postgres", "zammad-elasticsearch-master"} {
				if !ids["StatefulSet/"+release.Stack+"-support/"+name] {
					t.Fatal("missing support data owner", name)
				}
			}
			if !ids["CronJob/"+release.Stack+"-support/zammad-cronjob-reindex"] {
				t.Fatal("missing scheduled component")
			}
			for _, d := range definitions {
				for _, group := range []string{"containers", "initContainers"} {
					for _, v := range certIssuerObjectList(environmentZammadPod(d.Object)[group]) {
						if !immutableEnvironmentImage.MatchString(deploymentplan.String(deploymentplan.Map(v)["image"])) {
							t.Fatal("unreleased chart container")
						}
					}
				}
			}
			// Include one core consumer: adoption never invents a support PKI binding.
			core := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "redis", "namespace": release.Stack + "-platform", "uid": "core-owner", "resourceVersion": "1"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "redis", "image": release.Images["LKE_REDIS_IMAGE"].Reference}}}}}}
			live = append(live, core)
			bindings, err := observeEnvironmentBindings(environment, release, live)
			if err != nil {
				t.Fatal(err)
			}
			if err := bindZammadLifecycle(definitions, live); err != nil {
				t.Fatal(err)
			}
			definitions = append(definitions, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": release.Stack + "-support"}}, Phase: "namespace"}, deploymentplan.Definition{Object: core, Phase: "workload", Retain: true}, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": release.Stack + "-platform"}}, Phase: "namespace"})
			definitions, err = bindRetainedEnvironmentDependencies(definitions, live, bindings)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: release.Stack, Operation: "upgrade", ConfigurationSHA256: deploymentplan.Digest(values), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(bindings)}, definitions, live, bindings.Owners)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range plan.Resources {
				if r.Desired["kind"] == "Secret" || r.Desired["kind"] == "PersistentVolumeClaim" {
					if r.Action != "retain" {
						t.Fatal("plan replaces support credentials or claims")
					}
				}
			}
			privateTmp := filepath.Join(store.Root, "deployment", "tmp")
			entries, err := os.ReadDir(privateTmp)
			if err != nil || len(entries) != 0 {
				t.Fatal("private renderer scratch was retained", err)
			}
			// Actual major-version changes stay explicit migration blockers.
			for _, d := range definitions {
				if d.Object["kind"] == "StatefulSet" && deploymentplan.Map(d.Object["metadata"])["name"] == "zammad-postgres" {
					deploymentplan.Map(certIssuerObjectList(environmentZammadPod(d.Object)["containers"])[0])["image"] = "docker.io/postgres:19.0@sha256:" + strings.Repeat("d", 64)
				}
			}
			if bindZammadLifecycle(definitions, live) == nil {
				t.Fatal("database major change silently accepted")
			}
		})
	}
}
