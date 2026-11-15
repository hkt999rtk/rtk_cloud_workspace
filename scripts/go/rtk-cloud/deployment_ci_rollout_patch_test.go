package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCIRolloutPatchChangesOnlySelectedContainerWithConcurrencyGuards(t *testing.T) {
	old := "ghcr.io/hkt999rtk/rtk_cloud_frontend/frontend@sha256:" + strings.Repeat("a", 64)
	next := "ghcr.io/hkt999rtk/rtk_cloud_frontend/frontend@sha256:" + strings.Repeat("b", 64)
	object := []byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"namespace":"dev-frontend","name":"frontend","uid":"uid-1","resourceVersion":"42"},"spec":{"template":{"spec":{"containers":[{"name":"sidecar","image":"elsewhere/sidecar:v1"},{"name":"frontend","image":"` + old + `"}]}}}}`)
	patch, err := ciRolloutImagePatch(object, "dev-frontend", "frontend", "uid-1", "frontend", next)
	if err != nil {
		t.Fatal(err)
	}
	var ops []struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(patch, &ops); err != nil {
		t.Fatal(err)
	}
	want := []struct{ Op, Path, Value string }{{"test", "/metadata/uid", "uid-1"}, {"test", "/metadata/resourceVersion", "42"}, {"test", "/spec/template/spec/containers/1/image", old}, {"replace", "/spec/template/spec/containers/1/image", next}}
	if len(ops) != len(want) {
		t.Fatalf("unexpected operations: %s", patch)
	}
	for i := range want {
		if ops[i].Op != want[i].Op || ops[i].Path != want[i].Path || ops[i].Value != want[i].Value {
			t.Fatalf("unexpected operation %d: %s", i, patch)
		}
	}
	if _, err = ciRolloutImagePatch(object, "dev-frontend", "other", "uid-1", "frontend", next); err == nil {
		t.Fatal("wrong workload accepted")
	}
	if _, err = ciRolloutImagePatch(object, "dev-frontend", "frontend", "uid-1", "sidecar", next); err == nil {
		t.Fatal("unrelated image accepted")
	}
	if _, err = ciRolloutImagePatch(object, "dev-frontend", "frontend", "other-uid", "frontend", next); err == nil {
		t.Fatal("replaced workload identity accepted")
	}
}

func TestCIRolloutWorkloadPatchUpdatesMultipleImagesAtomically(t *testing.T) {
	api := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:"
	mqtt := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-emqx-pki@sha256:"
	oldAPI, newAPI := api+strings.Repeat("a", 64), api+strings.Repeat("b", 64)
	oldMQTT, newMQTT := mqtt+strings.Repeat("c", 64), mqtt+strings.Repeat("d", 64)
	raw := []byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"namespace":"dev-video","name":"mqtt-pki","uid":"uid","resourceVersion":"3"},"spec":{"template":{"spec":{"containers":[{"name":"mqtt","image":"` + oldMQTT + `"},{"name":"broker","image":"` + oldAPI + `"},{"name":"sidecar","image":"other/sidecar:v1"}]}}}}`)
	target := ciRolloutTarget{Namespace: "dev-video", Name: "mqtt-pki", UID: "uid"}
	patch, previous, err := ciRolloutWorkloadPatch(raw, target, map[string]string{"mqtt": newMQTT, "broker": newAPI}, nil)
	if err != nil || previous["mqtt"] != oldMQTT || previous["broker"] != oldAPI {
		t.Fatalf("previous images: %#v %v", previous, err)
	}
	var ops []struct {
		Op, Path string
		Value    string
	}
	if json.Unmarshal(patch, &ops) != nil || len(ops) != 6 {
		t.Fatalf("unexpected patch %s", patch)
	}
	if ops[0].Path != "/metadata/uid" || ops[1].Path != "/metadata/resourceVersion" || ops[2].Path != "/spec/template/spec/containers/0/image" || ops[3].Path != "/spec/template/spec/containers/0/image" || ops[4].Path != "/spec/template/spec/containers/1/image" || ops[5].Path != "/spec/template/spec/containers/1/image" {
		t.Fatalf("patch changed unrelated fields: %s", patch)
	}
	if _, _, err = ciRolloutWorkloadPatch(raw, target, map[string]string{"mqtt": newMQTT}, map[string]string{"mqtt": newMQTT}); err == nil {
		t.Fatal("concurrent different image accepted for rollback")
	}
}
