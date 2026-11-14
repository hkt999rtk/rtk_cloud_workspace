package main

import (
	"fmt"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestFreshProductFinalizerRetainsOriginalRenewalAndKVAuthority(t *testing.T) {
	cfg, p, _, service := createServiceRecipeFixture(t, "dev")
	store, e, _, _ := freshStageFixture(t)
	if err := e.Check(t.Context()); err != nil {
		t.Fatal("fixture lost its canonical execution fence", err)
	}
	cfg.Values["PKI_STATE_BACKEND"] = "openbao"
	selected := &environmentCreatePlan{Environment: "dev", Stack: e.Journal.Stack, Bootstrap: p}
	review := strings.Repeat("a", 64)
	recovery := &createServiceSessionRecovery{qualifiedPlan: selected, Review: review, Renewal: true, SecondSession: true, RenewedIntent: createServiceSessionIntent{SessionID: service.BootstrapSessionID}}
	custody := &environmentCustodyBinding{Generation: e.Journal.PlanSHA256, ServiceRootSHA256: service.Root.RootFingerprint}
	old := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, Backend: environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("old")}, ServiceSessionRecovery: recovery, Custody: custody}
	ctx := withEnvironmentCreateBootstrapRuntime(t.Context(), old)
	l := &createNormalTailProductLifecycle{Root: service.Root, Intermediate: service.Intermediate, Bootstrap: p, FreshResume: &environmentFreshProductResumeBinding{SelectedPlanSHA256: deploymentplan.Digest(selected), SessionReviewSHA256: review}}
	current := environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("current")}
	client := &environmentCreatePKIClient{Execution: e}
	got, err := freshProductFinalizerRuntime(ctx, cfg, l, e, current, client, service)
	if err != nil || got.ServiceSessionRecovery != recovery || got.Custody != custody || got.PKIClient != client || string(got.Backend.KubeconfigContent) != "current" || got.ServiceCeremony == nil || got.ServiceCeremony.BootstrapSessionID != service.BootstrapSessionID {
		t.Fatal("deferred finalizer lost the original renewed seed, KV authority or current transport", err)
	}
	for _, mode := range []string{"missing-context", "foreign-parent", "foreign-session", "foreign-selection"} {
		t.Run(mode, func(t *testing.T) {
			changed := old
			alternate := *recovery
			changed.ServiceSessionRecovery = &alternate
			changedContext := withEnvironmentCreateBootstrapRuntime(t.Context(), changed)
			switch mode {
			case "missing-context":
				changedContext = t.Context()
			case "foreign-parent":
				changed.ExecutionSHA256 = strings.Repeat("f", 64)
				changedContext = withEnvironmentCreateBootstrapRuntime(t.Context(), changed)
			case "foreign-session":
				alternate.Review = strings.Repeat("f", 64)
			case "foreign-selection":
				copy := *selected
				copy.Stack = "another"
				alternate.qualifiedPlan = &copy
			}
			if _, err := freshProductFinalizerRuntime(changedContext, cfg, l, e, current, client, service); err == nil {
				t.Fatal("accepted unrelated finalizer context", mode)
			}
		})
	}
}

func TestRemainingIssuerStagePreservesOriginalImmutableCleanupMap(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, p, _, runtime, service, roots, intermediates, _ := createApplicationFinalizeFixture(t, environment)
			roots["service"], intermediates["service"] = service.Root, service.Intermediate
			original, err := remainingFreshProductIssuerDefinitions(t.Context(), cfg, p, runtime, service, roots, intermediates)
			if err != nil {
				t.Fatal(err)
			}
			var live []deploymentplan.Object
			oldMap := "ConfigMap/" + p.Stack + "-video-cloud/pki-bootstrap-cleanup-binding"
			var originalMap deploymentplan.Object
			for i, definition := range original {
				object := deploymentplan.Clone(definition.Object)
				metadata := deploymentplan.Map(object["metadata"])
				metadata["uid"] = fmt.Sprintf("aaaaaaaa-aaaa-4aaa-8aaa-%012d", i+1)
				if metadata["labels"] == nil {
					metadata["labels"] = deploymentplan.Object{}
				}
				deploymentplan.Map(metadata["labels"])["rtk.realtek.com/stack"] = p.Stack
				live = append(live, object)
				if deploymentplan.ID(object) == oldMap {
					originalMap = object
				}
			}
			if originalMap == nil || originalMap["immutable"] != true {
				t.Fatal("missing original immutable cleanup map")
			}
			before := deploymentplan.Digest(originalMap)
			service.BootstrapSessionID = "66666666-6666-4666-8666-666666666666"
			service.BootstrapCredentialSecret = "service-bootstrap-" + service.BootstrapSessionID
			service.CleanupBinding.SessionID = service.BootstrapSessionID
			service.CleanupBinding.SecretUID = "77777777-7777-4777-8777-777777777777"
			service.CleanupBinding.ConfigMapName = "pki-bootstrap-cleanup-binding-" + service.BootstrapSessionID[:12]
			definitions, err := remainingFreshProductIssuerDefinitions(t.Context(), cfg, p, runtime, service, roots, intermediates)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest("unchanged-formal-release"), BindingsSHA256: environmentCreateStageBindingsSHA("managed-complete-issuer", definitions)}, definitions, live)
			if err != nil {
				t.Fatal("same complete next-stage template must admit beside original immutable map", err)
			}
			newMap := "ConfigMap/" + p.Stack + "-video-cloud/" + service.CleanupBinding.ConfigMapName
			created, mounted := false, false
			for _, resource := range plan.Resources {
				if resource.ID == oldMap {
					t.Fatal("remaining stage targets original immutable cleanup map")
				}
				if resource.ID == newMap {
					created = resource.Action == "create" && resource.Desired["immutable"] == true
				}
				if resource.ID == "Deployment/"+p.Stack+"-video-cloud/pki-controller" {
					volume := managedNamedField(environmentPKIPod(resource.Desired)["volumes"], "bootstrap-cleanup")
					mounted = deploymentplan.Map(volume["configMap"])["name"] == service.CleanupBinding.ConfigMapName
				}
			}
			if !created || !mounted || deploymentplan.Digest(originalMap) != before {
				t.Fatal("new stage lost exact cleanup projection or altered original owner")
			}
		})
	}
}
