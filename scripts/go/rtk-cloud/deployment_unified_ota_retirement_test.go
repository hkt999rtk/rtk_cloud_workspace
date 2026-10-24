package main

import (
	"errors"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func otaRetirementFixture(environment string) ([]deploymentplan.Object, environmentIdentityBindings) {
	ns := "video-cloud-" + environment + "-video-cloud"
	meta := func(name string) map[string]any {
		return map[string]any{"name": name, "namespace": ns, "uid": "owner-" + name, "resourceVersion": "1"}
	}
	live := []deploymentplan.Object{
		{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": meta("video-cloud-otaregistrar"), "spec": map[string]any{"replicas": float64(0)}},
		{"apiVersion": "v1", "kind": "Service", "metadata": meta("video-cloud-otaregistrar")},
		{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": meta("video-cloud-otaservice"), "spec": map[string]any{"replicas": float64(1)}},
		{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": meta("video-cloud-api"), "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "env": []any{map[string]any{"name": "VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED", "value": "true"}}}}}}}},
		{"apiVersion": "v1", "kind": "Secret", "metadata": meta("ota-service-platform-identity"), "data": map[string]any{"private": "fixture-private-canary"}},
	}
	bindings := environmentIdentityBindings{Version: 1, Environment: environment, Stack: "video-cloud-" + environment, Owners: map[string]string{}}
	for _, o := range live {
		bindings.Owners[deploymentplan.ID(o)] = deploymentplan.String(deploymentplan.Map(o["metadata"])["uid"])
	}
	return live, bindings
}
func TestRetiredOTARegistrarIsDeletedByPlanWithoutDeletingSharedIdentity(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			live, b := otaRetirementFixture(environment)
			definitions, err := retireLegacyOTARegistrar(nil, live, b)
			if err != nil || len(definitions) != 2 {
				t.Fatal(err)
			}
			for _, object := range live[2:] {
				definitions = append(definitions, deploymentplan.Definition{Object: deploymentplan.Clone(object), Phase: environmentResourcePhase(deploymentplan.String(object["kind"])), Retain: true})
			}
			plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: b.Stack, Operation: "upgrade", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}, definitions, live, b.Owners)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range plan.Resources {
				if strings.HasSuffix(r.ID, "/video-cloud-otaregistrar") {
					if r.Action != "retire" || r.UID == "" {
						t.Fatal("retirement is not bound to the original owner")
					}
				} else if r.Action != "retain" {
					t.Fatal("retirement touched replacement workload or identity", r.ID)
				}
			}
			again, err := retireLegacyOTARegistrar(nil, live[2:], b)
			if err != nil || len(again) != 0 {
				t.Fatal("completed retirement not idempotent", err)
			}
		})
	}
}
func TestRetiredOTARegistrarRefusesActiveForeignOrUnmigratedOwners(t *testing.T) {
	for _, scenario := range []string{"active", "status", "owner", "no-replacement", "no-cutover", "pod"} {
		t.Run(scenario, func(t *testing.T) {
			live, b := otaRetirementFixture("dev")
			switch scenario {
			case "active":
				deploymentplan.Map(live[0]["spec"])["replicas"] = float64(1)
			case "status":
				live[0]["status"] = map[string]any{"replicas": float64(1)}
			case "owner":
				b.Owners[deploymentplan.ID(live[0])] = "other-owner"
			case "no-replacement":
				deploymentplan.Map(live[2]["spec"])["replicas"] = float64(0)
			case "no-cutover":
				environmentPKIPod(live[3])["containers"] = nil
			case "pod":
				live = append(live, deploymentplan.Object{"kind": "Pod", "metadata": map[string]any{"name": "old-pod", "namespace": b.Stack + "-video-cloud", "labels": map[string]any{"app.kubernetes.io/name": "video-cloud-otaregistrar"}}, "status": map[string]any{"phase": "Running"}})
			}
			var blocker *deploymentplan.Blocker
			if _, err := retireLegacyOTARegistrar(nil, live, b); !errors.As(err, &blocker) || blocker.ID != "deployment.ota.retirement" {
				t.Fatal("unsafe retirement accepted", err)
			}
		})
	}
}
func TestLegacyOTARegistrarIsAbsentFromObservedWorkloadCatalog(t *testing.T) {
	live, b := otaRetirementFixture("dev")
	_, _, release := completeEnvironmentRenderFixture("dev")
	deployment := deploymentplan.Clone(live[2])
	deployment["spec"] = map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "otaservice", "image": release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference}}}}}
	observed, err := observeEnvironmentBindings("dev", release, []deploymentplan.Object{live[0], live[1], deployment})
	if err != nil {
		t.Fatal(err)
	}
	if len(observed.Pods) != 1 || observed.Owners[deploymentplan.ID(live[0])] != b.Owners[deploymentplan.ID(live[0])] {
		t.Fatal("legacy registrar still requires renderer/image selection or lost retirement ownership")
	}
}

func TestQualifiedOTARegistrarRetirementPassesPKITransitionOnlyWhenStopped(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			live, bindings := otaRetirementFixture(environment)
			definitions, err := retireLegacyOTARegistrar(nil, live, bindings)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateEnvironmentPKITransition(definitions, live[:1]); err != nil {
				t.Fatal("qualified stopped retirement rejected", err)
			}
			deploymentplan.Map(live[0]["spec"])["replicas"] = float64(1)
			if err := validateEnvironmentPKITransition(definitions, live[:1]); err == nil {
				t.Fatal("active consumer retirement bypassed PKI gate")
			}
			deploymentplan.Map(live[0]["spec"])["replicas"] = float64(0)
			definitions[0].Phase = "workload"
			if err := validateEnvironmentPKITransition(definitions, live[:1]); err == nil {
				t.Fatal("unqualified retirement phase accepted")
			}
		})
	}
}
