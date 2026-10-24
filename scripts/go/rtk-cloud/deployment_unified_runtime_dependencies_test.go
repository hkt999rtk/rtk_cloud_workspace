package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestRuntimeDependencyProducersCoverMaintainedFullRendererReferences(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Fatal("complete maintained target coverage requires its Helm renderer")
	}
	cfg, store, release, snapshot := runtimeDependencyFixture(t, "dev")
	// The enabled SDK consumer is selected by the frozen operator profile even
	// when the invoking process has no SDK variables.
	snapshot.Operator = appendMap(snapshot.Operator, map[string]string{"SDK_DOWNLOADS_ENABLED": "true", "SDK_ARTIFACT_BUCKET": "selected-sdk", "SDK_ARTIFACT_ENDPOINT": "https://objects.example.test", "SDK_ARTIFACT_ACCESS_KEY_ID": "selected-key", "SDK_ARTIFACT_SECRET_ACCESS_KEY": "selected-secret"})
	cfg.Workspace = mustWorkspaceRoot(t)
	baseline, err := readStrictEnv(filepath.Join(cfg.Workspace, "cloud_deploy", "architectures", "kubernetes", "architecture.env"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Values = appendMap(baseline, cfg.Values)
	for _, role := range environmentProviderImageKeys {
		if strings.HasPrefix(role, "LKE_ZAMMAD_") {
			release.Images[role] = environmentReleaseImage{Reference: cfg.Values[environmentProviderImageConfigKey(role)]}
		}
	}
	_, bindings, _ := completeEnvironmentRenderFixture("dev")
	bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
	values, chart, err := readEnvironmentZammadInputs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	support, err := renderEnvironmentZammad(context.Background(), cfg, store, release, "create", values, chart)
	if err != nil {
		t.Fatal(err)
	}
	var target, produced []deploymentplan.Definition
	err = withEnvironmentRenderInputs(cfg, store, release, snapshot, func(env map[string]string) error {
		var err error
		target, err = renderEnvironmentResources(env, bindings, release, support)
		if err != nil {
			return err
		}
		produced, err = renderEnvironmentRuntimeDependencyDefinitions(env, snapshot.Secrets, snapshot.Operator)
		return err
	})
	if err != nil {
		t.Fatal("maintained full renderer cannot consume the frozen producer inputs", err)
	}
	refs := map[string]bool{}
	for _, d := range target {
		for _, ref := range environmentResourceReferences(d.Object) {
			refs[ref.ID] = true
		}
	}
	for _, d := range produced {
		if !refs[deploymentplan.ID(d.Object)] {
			t.Fatal("runtime dependency has no maintained full-target consumer", deploymentplan.ID(d.Object))
		}
	}
	// Deliberately supply no invented bootstrap identities or owner placeholders.
	// The full reference pass must report every other lifecycle prerequisite,
	// while each canonical mirror and direct claim is supplied by its producer.
	_, err = bindRetainedEnvironmentDependencies(append(target, produced...), nil, bindings)
	var blocker *deploymentplan.Blocker
	if !errors.As(err, &blocker) || blocker.ID != "deployment.dependency.missing" {
		t.Fatal("missing genuine bootstrap lifecycle was not reported", err)
	}
	missing := map[string]bool{}
	for _, id := range strings.Split(blocker.Resource, ",") {
		missing[id] = true
	}
	for _, d := range produced {
		if missing[deploymentplan.ID(d.Object)] {
			t.Fatal("full target discarded its canonical producer", deploymentplan.ID(d.Object))
		}
	}
}

func runtimeDependencyFixture(t *testing.T, environment string) (deploymentConfig, secretStore, environmentRelease, environmentDeploymentSnapshot) {
	t.Helper()
	env, _, release := completeEnvironmentRenderFixture(environment)
	env["DEPLOYMENT_ADAPTER"], env["SUPPORT_TICKETS_ENABLED"], env["PAYPAL_ENABLED"] = "lke", "true", "false"
	env["LKE_BILLING_BACKUP_ENABLED"], env["LKE_BILLING_RAW_RETENTION_AUTHORITY_ENABLED"] = "false", "false"
	env["LKE_BILLING_RAW_RETIREMENT_ENABLED"], env["LKE_BILLING_INBOX_COMPACTION_ENABLED"] = "false", "false"
	env["LKE_CLOUD_LOGGER_BILLING_INBOX_STORAGE"], env["LKE_VIDEO_CLOUD_MQTT_USAGE_STORAGE"] = "5Gi", "5Gi"
	secrets := map[string]string{}
	for _, entry := range rtkSecretCatalog() {
		secrets[lkeSecretFileName(entry.ID)] = "isolated-original-" + entry.ID
	}
	secrets["payment-reference-encryption"] = base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg := deploymentConfig{Environment: environment, Adapter: "lke", DNSAdapter: "godaddy", Values: env}
	store := makeIsolatedTestSecretStore(t, environment)
	snapshot := environmentDeploymentSnapshot{Secrets: secrets, Operator: map[string]string{"GHCR_PULL_USERNAME": "isolated-pull-user", "GHCR_PULL_TOKEN": "private-pull-canary"}}
	return cfg, store, release, snapshot
}

func renderRuntimeDependencyFixture(t *testing.T, cfg deploymentConfig, store secretStore, release environmentRelease, snapshot environmentDeploymentSnapshot) ([]deploymentplan.Definition, error) {
	t.Helper()
	var defs []deploymentplan.Definition
	err := withEnvironmentRenderInputs(cfg, store, release, snapshot, func(env map[string]string) error {
		var err error
		defs, err = renderEnvironmentRuntimeDependencyDefinitions(env, snapshot.Secrets, snapshot.Operator)
		return err
	})
	return defs, err
}

func TestRuntimeDependencyProducersCreateCompleteSetAndRetainOriginalData(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, store, release, snapshot := runtimeDependencyFixture(t, environment)
			cfg.Values["TEST_LAB_ENABLED"] = "false"
			produced, err := renderRuntimeDependencyFixture(t, cfg, store, release, snapshot)
			if err != nil || len(produced) != 12 {
				t.Fatal("complete canonical producer set unavailable", err, len(produced))
			}
			inputs := deploymentplan.Inputs{Environment: environment, Stack: release.Stack, Operation: "scoped", ConfigurationSHA256: snapshot.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(produced)}
			plan, err := deploymentplan.Compile(inputs, produced, nil)
			if err != nil {
				t.Fatal("real producer definitions cannot compile", err)
			}
			owners := map[string]string{}
			var live []deploymentplan.Object
			secrets, claims, pulls := 0, 0, 0
			for i, r := range plan.Resources {
				if r.Action != "create" {
					t.Fatal("new mirror unexpectedly reused an owner")
				}
				object, err := r.Body(nil)
				if err != nil {
					t.Fatal(err)
				}
				uid := fmt.Sprintf("%08x-1111-4111-8111-%012x", i+1, i+1)
				deploymentplan.Map(object["metadata"])["uid"] = uid
				deploymentplan.Map(object["metadata"])["resourceVersion"] = "1"
				owners[r.ID] = uid
				live = append(live, object)
				if object["kind"] == "PersistentVolumeClaim" {
					claims++
				} else if object["type"] == "kubernetes.io/dockerconfigjson" {
					pulls++
				} else {
					secrets++
				}
				if strings.HasSuffix(r.ID, "/billing-runtime") {
					data := deploymentplan.Map(object["data"])
					if data["ENVIRONMENT"] != base64.StdEncoding.EncodeToString([]byte(environment)) {
						t.Fatal("selected environment replaced by staging")
					}
					for _, key := range []string{"PAYMENT_SIMULATOR_ENABLED", "NEWEBPAY_ENABLED"} {
						if data[key] != base64.StdEncoding.EncodeToString([]byte("false")) {
							t.Fatal("test facilities enabled without selected intent", key)
						}
					}
				}
			}
			if secrets != 6 || pulls != 4 || claims != 2 {
				t.Fatal("producer inventory is incomplete", secrets, pulls, claims)
			}
			before := deploymentplan.Digest(live)
			retained, err := bindEnvironmentRuntimeDependencyOwners(produced, produced, live, owners)
			if err != nil {
				t.Fatal(err)
			}
			inputs.Operation = "upgrade"
			next, err := deploymentplan.Compile(inputs, retained, live)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range next.Resources {
				if r.Action != "retain" || r.UID != owners[r.ID] {
					t.Fatal("existing canonical owner would be rewritten", r.ID)
				}
				var current deploymentplan.Object
				for _, object := range live {
					if deploymentplan.ID(object) == r.ID {
						current = object
					}
				}
				if body, err := r.Body(current); err != nil || body != nil {
					t.Fatal("retained dependency emitted a write", err)
				}
			}
			if before != deploymentplan.Digest(live) {
				t.Fatal("canonical owner data changed")
			}
		})
	}
}

func TestRuntimeDependencyProducersRefuseMissingInputsAndImplicitReplacement(t *testing.T) {
	for _, mode := range []string{"missing-support-pg", "missing-support-redis", "missing-pull", "invalid-feature", "recorded-missing", "unrecorded-existing", "replacement-uid", "changed-data", "changed-pvc", "deleting"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, release, snapshot := runtimeDependencyFixture(t, "dev")
			switch mode {
			case "missing-support-pg":
				delete(snapshot.Secrets, "zammad-postgres-password")
			case "missing-support-redis":
				delete(snapshot.Secrets, "zammad-redis-password")
			case "missing-pull":
				delete(snapshot.Operator, "GHCR_PULL_TOKEN")
			case "invalid-feature":
				cfg.Values["TEST_LAB_ENABLED"] = "invalid-private-canary"
			}
			produced, err := renderRuntimeDependencyFixture(t, cfg, store, release, snapshot)
			if strings.HasPrefix(mode, "missing-") || mode == "invalid-feature" {
				if err == nil || strings.Contains(err.Error(), "private-canary") {
					t.Fatal("incomplete or private input accepted/exposed", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			d := produced[0]
			if mode == "changed-pvc" {
				for _, candidate := range produced {
					if candidate.Object["kind"] == "PersistentVolumeClaim" {
						d = candidate
						break
					}
				}
			}
			plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: release.Stack, Operation: "scoped", ConfigurationSHA256: "cfg", ReleaseSHA256: "release", BindingsSHA256: "bindings"}, []deploymentplan.Definition{d}, nil)
			if err != nil {
				t.Fatal(err)
			}
			actual := deploymentplan.Clone(plan.Resources[0].Desired)
			id := deploymentplan.ID(actual)
			uid := "11111111-1111-4111-8111-111111111111"
			deploymentplan.Map(actual["metadata"])["uid"] = uid
			owners := map[string]string{id: uid}
			live := []deploymentplan.Object{actual}
			switch mode {
			case "recorded-missing":
				live = nil
			case "unrecorded-existing":
				owners = nil
			case "replacement-uid":
				deploymentplan.Map(actual["metadata"])["uid"] = "22222222-2222-4222-8222-222222222222"
			case "changed-data":
				deploymentplan.Map(actual["data"])["DATABASE_URL"] = base64.StdEncoding.EncodeToString([]byte("private-drift-canary"))
			case "changed-pvc":
				deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(actual["spec"])["resources"])["requests"])["storage"] = "10Gi"
			case "deleting":
				deploymentplan.Map(actual["metadata"])["deletionTimestamp"] = "2026-10-18T00:00:00Z"
			}
			if _, err := bindEnvironmentRuntimeDependencyOwners([]deploymentplan.Definition{d}, []deploymentplan.Definition{d}, live, owners); err == nil || strings.Contains(err.Error(), "private-drift-canary") {
				t.Fatal("implicit dependency replacement accepted or leaked", err)
			}
		})
	}
}

func TestRuntimeDependencyClosureAggregatesAllUnproducedLifecycleReferences(t *testing.T) {
	refs := []string{"zammad-postgres-password", "zammad-redis-password", "mqtt-foundation-platform-identity", "shadow-worker-platform-identity", "ota-service-platform-identity", "video-storage-service-platform-identity", "webrtc-service-platform-identity"}
	var volumes []any
	for _, name := range refs {
		volumes = append(volumes, deploymentplan.Object{"name": name, "secret": deploymentplan.Object{"secretName": name}})
	}
	consumer := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "complete-dependency-consumer", "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"volumes": volumes}}}}
	explicit := "ConfigMap/video-cloud-dev-video-cloud/explicit-lifecycle-config"
	_, err := bindRetainedEnvironmentDependencies([]deploymentplan.Definition{{Object: consumer, Phase: "workload", Dependencies: []string{explicit}}}, nil, environmentIdentityBindings{Stack: "video-cloud-dev"})
	var blocker *deploymentplan.Blocker
	if !errors.As(err, &blocker) || blocker.ID != "deployment.dependency.missing" {
		t.Fatal("missing dependency closure did not block", err)
	}
	want := make([]string, 0, len(refs))
	for _, name := range refs {
		want = append(want, "Secret/video-cloud-dev-video-cloud/"+name)
	}
	want = append(want, explicit)
	sort.Strings(want)
	if blocker.Resource != strings.Join(want, ",") {
		t.Fatal("compiler stopped at first missing lifecycle dependency", blocker.Resource)
	}
}
