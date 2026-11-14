package main

import (
	"context"
	"io"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestProductPublicCRLSourceReadsKnownPartialOwnersWithoutReadiness(t *testing.T) {
	cfg, p, r, service := createServiceRecipeFixture(t, "dev")
	store, e, template, b := freshStageFixture(t)
	cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	cfg.Values["MQTT_FOUNDATION_REGISTRATION_ENABLED"] = "true"
	var err error
	p.ProductIdentities, err = environmentCreateProductIdentityIntent(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cm := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "mqtt-crls", "namespace": p.Stack + "-video-cloud"}, "data": deploymentplan.Object{"crls.json": environmentCreateServiceCRLManifest(service.Root, "/var/lib/mqtt-trust/root.json", service.Intermediate)}}
	c := deploymentplan.Object{"name": "mqttfoundation", "image": p.Image, "env": environmentCreateEnv(map[string]string{"VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST": "/trust/crls.json"}), "volumeMounts": []any{environmentCreateServiceMount("trust", "/trust", true)}}
	owner := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "video-cloud-mqttfoundation", "namespace": p.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{c}, "volumes": []any{environmentCreateServiceVolume("trust", "configMap", "mqtt-crls")}, "securityContext": deploymentplan.Object{"fsGroup": float64(54321)}}}}}
	defs := []deploymentplan.Definition{{Object: cm, Phase: "configuration"}, {Object: owner, Phase: "workload"}}
	in := template.Inputs
	in.BindingsSHA256 = environmentCreateStageBindingsSHA("workload", defs)
	source, err := deploymentplan.Compile(in, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	fence := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	run := func(stage string, plan *deploymentplan.Plan) error {
		return executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, plan, b, fence, fence, fence, func(context.Context) error { return nil }, io.Discard)
	}
	b.failure = "after-write-workload"
	if err := run("workload", source); err == nil {
		t.Fatal("source readiness failure missing")
	} else {
		t.Logf("source interruption: %v", err)
	}
	j, ok, err := source.ReadExecution(store.Root)
	if err != nil || !ok || j.Accepted {
		t.Fatalf("native source journal exists=%v accepted=%v err=%v", ok, j.Accepted, err)
	}
	before := deploymentplan.Digest(j)
	got, err := recordedProductCRLDefinitions(t.Context(), cfg, store, e, b, p)
	if err != nil || len(got) != 2 {
		t.Fatal("known partial owner cannot prepare public trust", len(got), err)
	}
	jobs, err := renderCreateProductPublicCRLs(t.Context(), cfg, p, r, service, got)
	if err != nil || len(jobs) != 1 {
		t.Fatal("native initializer missing", len(jobs), err)
	}
	binding, err := createEnvironmentCustodyBinding(cfg, p, strings.Repeat("a", 64), "")
	if err != nil {
		t.Fatal(err)
	}
	jobs, err = projectEnvironmentCustodyResources(cfg.Values, cfg.Environment, binding, strings.Repeat("b", 64), jobs)
	if err != nil {
		t.Fatal(err)
	}
	in.BindingsSHA256 = environmentCreateStageBindingsSHA(productPublicCRLStage, jobs)
	plan, err := deploymentplan.Compile(in, jobs, nil)
	if err != nil {
		t.Fatal(err)
	}
	b.failure, b.failed = "", false
	if err = run(productPublicCRLStage, plan); err != nil {
		t.Fatal(err)
	}
	if err = run(productPublicCRLStage, plan); err != nil {
		t.Fatal("initializer retry failed", err)
	}
	after, _, _ := source.ReadExecution(store.Root)
	if deploymentplan.Digest(after) != before || b.writes[deploymentplan.ID(owner)] != 1 {
		t.Fatal("source completion fabricated or owner rewritten")
	}
	for _, res := range plan.Resources {
		if b.writes[res.ID] != 1 {
			t.Fatal("initializer native writes replayed", res.ID)
		}
	}
	// A replaced public ConfigMap is rejected before native initialization.
	deploymentplan.Map(b.objects[deploymentplan.ID(cm)]["metadata"])["uid"] = "foreign"
	if _, err = recordedProductCRLDefinitions(t.Context(), cfg, store, e, b, p); err == nil {
		t.Fatal("foreign public trust adopted")
	}
}

func TestProductPublicCRLToolRejectsServiceOnlyAndPreservesFailedJob(t *testing.T) {
	cfg, p, r, s := createServiceRecipeFixture(t, "dev")
	cfg.Values["PKI_STATE_BACKEND"] = "openbao"
	cfg.Values["MQTT_FOUNDATION_REGISTRATION_ENABLED"] = "true"
	var err error
	p.ProductIdentities, err = environmentCreateProductIdentityIntent(cfg)
	if err != nil {
		t.Fatal(err)
	}
	job := environmentCreateServiceEnrollmentJob(p, p.Stack+"-video-cloud", "mqtt-public-crls", "pki-product-mqtt-identity", s, []any{"/app/serviceidentity-bootstrap", "initialize-public-crl", "/var/lib/mqtt-trust/root.json", s.Root.ID, s.Root.RootFingerprint}, map[string]string{}, false)
	before := deploymentplan.Digest(job)
	tool := failedForwardTool{Image: environmentReleaseImage{Reference: p.Image, SourceRevision: strings.Repeat("a", 40)}, Capabilities: []byte(`{"schema":"rtk-cloud.deployment-capabilities/v1","service":"serviceidentity-bootstrap","features":["initial-service-crl-custody-v1"]}`)}
	if _, err := productPublicCRLToolJobs([]deploymentplan.Definition{job}, tool); err == nil {
		t.Fatal("old Service-only binary admitted for MQTT")
	}
	tool.Capabilities = []byte(`{"schema":"rtk-cloud.deployment-capabilities/v1","service":"serviceidentity-bootstrap","features":["initial-public-crl-custody-v2"]}`)
	tool.Image.Reference = "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("b", 64)
	jobs, err := productPublicCRLToolJobs([]deploymentplan.Definition{job}, tool)
	if err != nil || len(jobs) != 1 {
		t.Fatal(err)
	}
	c := managedNamedField(environmentPKIPod(jobs[0].Object)["containers"], "enroll")
	if deploymentplan.ID(jobs[0].Object) == deploymentplan.ID(job.Object) || c["image"] != tool.Image.Reference || deploymentplan.Digest(c["command"]) != deploymentplan.Digest(managedNamedField(environmentPKIPod(job.Object)["containers"], "enroll")["command"]) || deploymentplan.Digest(job) != before {
		t.Fatal("tool repair rewrote original Job or command")
	}
	_ = r
}
