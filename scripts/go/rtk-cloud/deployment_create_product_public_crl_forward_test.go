package main

import (
	"context"
	"errors"
	"io"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

type productCRLFailedJobBackend struct {
	freshStageBackend
	fail string
}

func (b *productCRLFailedJobBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	if r.ID == b.fail {
		return errors.New("isolated terminal native Job failure")
	}
	return b.freshStageBackend.Wait(ctx, r)
}
func TestProductPublicCRLForwardResolvesPartialFailedJobWithoutRewritingHistory(t *testing.T) {
	cfg, p, _, service := createServiceRecipeFixture(t, "dev")
	store, e, template, base := freshStageFixture(t)
	anchorFence := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "anchor", template, base, anchorFence, anchorFence, anchorFence, func(context.Context) error { return nil }, io.Discard); err != nil {
		t.Fatal(err)
	}
	cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	cfg.Values["MQTT_FOUNDATION_REGISTRATION_ENABLED"], cfg.Values["SHADOW_WORKER_REGISTRATION_ENABLED"] = "true", "true"
	var err error
	p.ProductIdentities, err = environmentCreateProductIdentityIntent(cfg)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := createEnvironmentCustodyBinding(cfg, p, strings.Repeat("a", 64), "")
	if err != nil {
		t.Fatal(err)
	}
	var raw []deploymentplan.Definition
	for _, role := range []string{"mqtt", "shadow"} {
		job := environmentCreateServiceEnrollmentJob(p, p.Stack+"-video-cloud", role+"-public-crls", "pki-product-"+role+"-identity", service, []any{"/app/serviceidentity-bootstrap", "initialize-public-crl", "/var/lib/mqtt-trust/root.json", service.Root.ID, service.Root.RootFingerprint}, map[string]string{"PKI_CLIENT_IDENTITY_STATE": environmentCreateServiceOwnerIdentityPath(role, "client.json")}, false)
		pod := environmentPKIPod(job.Object)
		pod["volumes"] = environmentCreateServiceWithoutNamed(pod["volumes"], "bootstrap", "service-trust")
		pod["initContainers"] = []any{}
		c := managedNamedField(pod["containers"], "enroll")
		c["volumeMounts"] = environmentCreateServiceWithoutNamed(c["volumeMounts"], "bootstrap", "service-trust")
		raw = append(raw, job)
	}
	defs, err := projectEnvironmentCustodyResources(cfg.Values, cfg.Environment, binding, strings.Repeat("b", 64), raw)
	if err != nil {
		t.Fatal(err)
	}
	in := template.Inputs
	in.BindingsSHA256 = environmentCreateStageBindingsSHA(productPublicCRLStage, defs)
	original, err := deploymentplan.Compile(in, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	b := &productCRLFailedJobBackend{freshStageBackend: base}
	for _, res := range original.Resources {
		if res.Desired["kind"] == "Job" {
			b.fail = res.ID
			break
		}
	}
	fence := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	run := func(stage string, plan *deploymentplan.Plan) error {
		return executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, plan, b, fence, fence, fence, func(context.Context) error { return nil }, io.Discard)
	}
	if err := run(productPublicCRLStage, original); err == nil {
		t.Fatal("original terminal Job did not fail")
	}
	source, ok, err := original.ReadExecution(store.Root)
	if err != nil || !ok || source.Accepted || len(source.Completed) >= len(original.Resources) || source.Owners[source.Failed] == "" {
		t.Fatal("not native partial failure", err)
	}
	before := deploymentplan.Digest(source)
	tool := failedForwardTool{Image: environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("b", 64), SourceRevision: strings.Repeat("a", 40)}, Capabilities: []byte(`{"schema":"rtk-cloud.deployment-capabilities/v1","service":"serviceidentity-bootstrap","features":["initial-public-crl-custody-v2"]}`)}
	jobs, err := productPublicCRLToolJobs(raw, tool)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err = projectEnvironmentCustodyResources(cfg.Values, cfg.Environment, binding, strings.Repeat("b", 64), jobs)
	if err != nil {
		t.Fatal(err)
	}
	var live []deploymentplan.Object
	for _, o := range b.objects {
		live = append(live, o)
	}
	stage := productPublicCRLStage + "-" + tool.Image.SourceRevision[:12]
	in.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, jobs)
	next, err := deploymentplan.Compile(in, jobs, live)
	if err != nil {
		t.Fatal(err)
	}
	b.fail = ""
	if err := run(stage, next); err != nil {
		t.Fatal(err)
	}
	if err := completeProductCRLForward(t.Context(), store, e, original, stage, next); err != nil {
		t.Fatal("real successor cannot resolve original request", err)
	}
	if err := completeProductCRLForward(t.Context(), store, e, original, stage, next); err != nil {
		t.Fatal("resolution cannot resume", err)
	}
	after, _, _ := original.ReadExecution(store.Root)
	if deploymentplan.Digest(after) != before || after.Accepted || b.writes[source.Failed] != 1 {
		t.Fatal("failed journal or Job rewritten")
	}
	if _, ok, err := readEnvironmentCreateStageProgress(store, e, productPublicCRLStage, original); err != nil || !ok {
		t.Fatal("literal failed history cannot be read", err)
	}
	if _, err := verifyAcceptedStageAfterRecordedHistory(t.Context(), store, e, b, "kubernetes/"+productPublicCRLStage); err != nil {
		t.Fatal("resolved source verifier fails", err)
	}
	ar := template.Resources[0]
	aj, _, err := template.ReadExecution(store.Root)
	if err != nil {
		t.Fatal(err)
	}
	tracked := []activeTransportRenewalResource{sessionRecordedOwnerBase(ar.ID, aj.Owners[ar.ID], ar.Desired)}
	if _, err := completedRecordedOwnerSuccessorsAfterHistory(t.Context(), store, e, b, "kubernetes/anchor", tracked, nil); err != nil {
		t.Fatal("common owner observation cannot read resolved initializer", err)
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+productPublicCRLStage)
	if entry.State != "completed" || !productCRLForwardReceiptMatches(entry.Receipt) {
		t.Fatal("parent lacks explicit successor receipt")
	}
	changed := source
	changed.InFlight = source.Failed
	if err := validateProductCRLForwardReceipt(store, e, original, changed, entry.Receipt); err == nil {
		t.Fatal("unknown source outcome adopted")
	}
}

func TestPersistedCreateReceiptResumesAcrossJSONFieldOrder(t *testing.T) {
	first := []byte(`{"z":"original","n":9007199254740993,"nested":{"b":2,"a":1}}`)
	same := []byte(`{ "nested": {"a":1,"b":2}, "n":9007199254740993,"z":"original" }`)
	if !samePersistedJSONReceipt(first, same) {
		t.Fatal("canonical persistence rejects equivalent native receipt")
	}
	for _, bad := range [][]byte{[]byte(`{"z":"original","n":9007199254740992,"nested":{"b":2,"a":1}}`), []byte(`{"z":"changed","n":9007199254740993,"nested":{"b":2,"a":1}}`), []byte(`invalid`), nil} {
		if samePersistedJSONReceipt(first, bad) {
			t.Fatal("changed or invalid receipt accepted")
		}
	}
}
