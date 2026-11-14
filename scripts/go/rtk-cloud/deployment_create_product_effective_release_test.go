package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestFreshProductFinalCompileUsesOriginalSelectedReleaseOnRetry(t *testing.T) {
	plan, defs, live := openBaoPodQualificationFixture(t, "dev")
	set, pod := live[0], live[1]
	addOpenBaoFixtureClaimVolumes(set, pod)
	target := deploymentplan.String(managedNamedField(environmentPKIPod(plan.Resources[0].Desired)["containers"], "openbao-pki")["image"])
	old := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("b", 64)
	original := deploymentplan.Clone(set)
	managedNamedField(environmentPKIPod(original)["containers"], "openbao-pki")["image"] = old
	managedNamedField(environmentPKIPod(original)["initContainers"], "openbao-pki-install")["image"] = old
	managedNamedField(deploymentplan.Map(pod["spec"])["initContainers"], "openbao-pki-install")["image"] = old
	status := deploymentplan.Map(pod["status"])
	status["containerStatuses"] = []any{deploymentplan.Object{"name": "openbao", "ready": true, "containerID": "provider-id"}, deploymentplan.Object{"name": "openbao-pki", "ready": true, "containerID": "host-id", "imageID": target}}
	status["initContainerStatuses"] = []any{deploymentplan.Object{"name": "openbao-pki-install", "state": deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(0)}}}}
	q := openBaoInPlaceQualification{TargetImage: target, PodUID: "original-pod", ProviderContainerID: "provider-id", OldHostContainerID: "old-host"}
	owner := openBaoInPlaceOwner{Namespace: deploymentplan.String(deploymentplan.Map(set["metadata"])["namespace"]), PodName: "openbao-0", PodUID: "original-pod", ContainerID: "host-id", StatefulSetUID: "original-controller", ReleaseImage: environmentReleaseImage{Reference: target}, qualification: q, original: original}
	selected := environmentRelease{Images: map[string]environmentReleaseImage{"LKE_OPENBAO_PKI_IMAGE": {Reference: target}}}
	c := &compiledEnvironmentDeployment{Release: selected}
	interrupted := errors.New("simulated interruption before final compilation")
	calls := 0
	compile := func(ctx context.Context, cfg deploymentConfig, operation string, release environmentRelease, source environmentReleaseCommand) (*compiledEnvironmentDeployment, error) {
		calls++
		if operation != "create" || deploymentplan.Digest(release) != deploymentplan.Digest(selected) {
			t.Fatal("final compile changed environment-selected release")
		}
		if calls == 1 {
			return nil, interrupted
		}
		if err := qualifyEnvironmentOnDeleteOpenBaoPlan(plan, defs, live, owner); err != nil || plan.Resources[0].Action != "retain" {
			t.Fatalf("matching OnDelete proof not retained: %v", err)
		}
		return &compiledEnvironmentDeployment{Plan: plan, Release: release}, nil
	}
	if _, err := compileFreshProductSteadyTarget(t.Context(), deploymentConfig{}, c, compile); !errors.Is(err, interrupted) {
		t.Fatalf("interruption lost: %v", err)
	}
	final, err := compileFreshProductSteadyTarget(t.Context(), deploymentConfig{}, c, compile)
	if err != nil || final == nil || calls != 2 || deploymentplan.Digest(final.Release) != deploymentplan.Digest(selected) {
		t.Fatalf("retry changed selected release: %v", err)
	}
}
