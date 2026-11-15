package main

import (
	"context"
	"io"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"testing"
)

func currentRegistrationOwnerFixture(t *testing.T) (deploymentConfig, environmentCreateBootstrapRuntime, []deploymentplan.Definition, *ordinaryPendingHistoryBackend) {
	t.Helper()
	cfg, _, _, _, _, _, defs := accountRegistrationFixture(t)
	store, e, template, native := freshStageFixture(t)
	b := &ordinaryPendingHistoryBackend{freshStageBackend: native}
	in := template.Inputs
	in.BindingsSHA256 = environmentCreateStageBindingsSHA("managed-complete-issuer", defs)
	p, err := deploymentplan.Compile(in, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "managed-complete-issuer", p, b, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
		t.Fatal(err)
	}
	id := "Deployment/" + e.Journal.Stack + "-video-cloud/certissuer"
	o := deploymentplan.Clone(b.objects[id])
	c := mqttFoundationPrimary(o)
	c["env"] = environmentCreateServiceWithoutEnv(c["env"], "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER", "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID", "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SUBJECTS")
	in.BindingsSHA256 = environmentCreateStageBindingsSHA("fresh-product-enrollment-closed", []deploymentplan.Definition{{Object: o, Phase: "workload"}})
	next, err := deploymentplan.Compile(in, []deploymentplan.Definition{{Object: o, Phase: "workload"}}, []deploymentplan.Object{b.objects[id]})
	if err != nil {
		t.Fatal(err)
	}
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "fresh-product-enrollment-closed", next, b, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
		t.Fatal(err)
	}
	return cfg, environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256}, defs, b
}

func TestCurrentAccountRegistrationPrerequisitesFollowAcceptedClosedIssuer(t *testing.T) {
	cfg, r, defs, b := currentRegistrationOwnerFixture(t)
	before := createBootstrapStateSnapshot(t, r.Execution.Store.Root)
	writes := deploymentplan.Digest(b.writes)
	got, err := currentEnvironmentCreateAccountRegistrationCoreWithBackend(t.Context(), cfg, r, defs, b)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range got {
		if deploymentplan.ID(d.Object) == "Deployment/"+r.Execution.Journal.Stack+"-video-cloud/certissuer" {
			found = true
			if mqttFoundationEnv(mqttFoundationPrimary(d.Object), "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID") != "" {
				t.Fatal("registration reopens accepted closed issuer")
			}
			for _, original := range defs {
				if deploymentplan.ID(original.Object) == deploymentplan.ID(d.Object) && !slices.Equal(d.Dependencies, original.Dependencies) {
					t.Fatal("registration dropped declared predecessor dependencies")
				}
			}
			if deploymentplan.Map(d.Object["metadata"])["uid"] != deploymentplan.Map(b.objects[deploymentplan.ID(d.Object)]["metadata"])["uid"] {
				t.Fatal("current prerequisite lost original native UID")
			}
		}
	}
	if !found || writes != deploymentplan.Digest(b.writes) {
		t.Fatal("missing issuer or replayed native write")
	}
	assertRecoveryContractReadOnly(t, r.Execution.Store, before)
}

func TestCurrentAccountRegistrationPrerequisitesRejectForeignCurrentOwner(t *testing.T) {
	for _, mode := range []string{"uid", "body"} {
		t.Run(mode, func(t *testing.T) {
			cfg, r, defs, b := currentRegistrationOwnerFixture(t)
			id := "Deployment/" + r.Execution.Journal.Stack + "-video-cloud/certissuer"
			if mode == "uid" {
				deploymentplan.Map(b.objects[id]["metadata"])["uid"] = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			} else {
				mqttFoundationPrimary(b.objects[id])["image"] = "foreign"
			}
			if _, err := currentEnvironmentCreateAccountRegistrationCoreWithBackend(t.Context(), cfg, r, defs, b); err == nil {
				t.Fatal("unrecorded current owner accepted")
			}
		})
	}
}
