package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestPostCleanupControllerObservationKeepsBusinessImageAndOriginalRelease(t *testing.T) {
	old := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("1", 64)
	next := "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:" + strings.Repeat("2", 64)
	release := environmentRelease{Environment: "dev", Stack: "video-cloud-dev", Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": {Reference: old}}}
	before := deploymentplan.Digest(release)
	makeOwner := func(name, container, image string) deploymentplan.Object {
		return deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": release.Stack + "-video-cloud", "name": name, "uid": "original-" + name}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": container, "image": image}}}}}}
	}
	controller := makeOwner("pki-controller", "pki-controller", next)
	business := makeOwner("unrelated-api", "app", old)
	live := []deploymentplan.Object{controller, business}
	id := deploymentplan.ID(controller)
	r := &createNormalTailPostCleanupCorrection{Source: createNormalTailPostCleanupCorrectionSource{ControllerImage: next}, Plans: map[string]*deploymentplan.Plan{"apply": {}}, Journals: map[string]deploymentplan.Journal{"apply": {Accepted: true, Owners: map[string]string{id: "original-pki-controller"}}}}
	effective, err := postCleanupControllerObservationRelease(release, r, live)
	if err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Digest(release) != before || effective.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference != old {
		t.Fatal("changed original or shared release role")
	}
	bindings, err := observeEnvironmentBindings("dev", effective, live)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bindings.Pods {
		want := "LKE_VIDEO_CLOUD_IMAGE"
		if b.Name == "pki-controller" {
			want = "LKE_PKI_CONTROLLER_IMAGE"
		}
		if len(b.Containers) != 1 || b.Containers[0].ImageKey != want {
			t.Fatalf("incorrect role for %s: %+v", b.Name, b.Containers)
		}
	}
	for _, mutate := range []func([]deploymentplan.Object){
		func(v []deploymentplan.Object) { deploymentplan.Map(v[0]["metadata"])["uid"] = "replacement" },
		func(v []deploymentplan.Object) { mqttFoundationPrimary(v[0])["image"] = old },
		func(v []deploymentplan.Object) { mqttFoundationPrimary(v[0])["name"] = "other" },
	} {
		changed := []deploymentplan.Object{deploymentplan.Clone(controller), deploymentplan.Clone(business)}
		mutate(changed)
		if _, err := postCleanupControllerObservationRelease(release, r, changed); err == nil {
			t.Fatal("changed owner or image accepted")
		}
	}
	if _, err := postCleanupControllerObservationRelease(release, r, append(live, controller)); err == nil {
		t.Fatal("duplicate controller accepted")
	}
	if _, err := postCleanupControllerObservationRelease(release, r, live[1:]); err == nil {
		t.Fatal("missing controller accepted")
	}
	changed := []deploymentplan.Object{deploymentplan.Clone(controller), deploymentplan.Clone(business)}
	mqttFoundationPrimary(changed[1])["image"] = next
	if _, err := observeEnvironmentBindings("dev", effective, changed); err == nil {
		t.Fatal("business borrowed controller image authority")
	}
	r.Journals["apply"] = deploymentplan.Journal{}
	unaccepted, err := postCleanupControllerObservationRelease(release, r, live)
	if err != nil || deploymentplan.Digest(unaccepted) != before {
		t.Fatal("unaccepted correction added an image role")
	}
}

func TestPostCleanupControllerRoleCannotBeUsedByOtherConsumers(t *testing.T) {
	if !environmentManagedImageRoleAllowed("pki-controller", "pki-controller", "pki-controller", 0, "LKE_PKI_CONTROLLER_IMAGE") {
		t.Fatal("native controller rejected")
	}
	for _, tc := range []struct {
		component, workload, container string
		index                          int
	}{
		{"managed-api", "video-cloud-api", "app", 0},
		{"certissuer", "certissuer", "certissuer", 0},
		{"pki-controller", "pki-controller", "sidecar", 1},
		{"pki-controller", "other", "pki-controller", 0},
		{"existing-workload", "pki-controller", "pki-controller", 0},
		{"pki-controller", "pki-controller", "pki-controller", 1},
	} {
		if environmentManagedImageRoleAllowed(tc.component, tc.workload, tc.container, tc.index, "LKE_PKI_CONTROLLER_IMAGE") {
			t.Fatalf("alias escaped: %+v", tc)
		}
	}
}
