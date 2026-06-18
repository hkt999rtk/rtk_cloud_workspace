package main

import (
	"strings"
	"testing"
)

func TestRenderK8STemplateFailsOnMissingKey(t *testing.T) {
	_, err := renderK8STemplate("testdata/missing-key.yaml.tmpl", map[string]string{"Name": "demo"})
	if err == nil || !strings.Contains(err.Error(), "map has no entry for key") {
		t.Fatalf("expected missing key template error, got %v", err)
	}
}

func TestK8SMetadataLabelsUseStackAndApp(t *testing.T) {
	metadata := newK8SMetadata("video-cloud-staging", "video-cloud-api")
	if metadata.Labels["app.kubernetes.io/name"] != "video-cloud-api" {
		t.Fatalf("app label got %q", metadata.Labels["app.kubernetes.io/name"])
	}
	if metadata.Labels["app.kubernetes.io/part-of"] != "video-cloud-staging" {
		t.Fatalf("stack label got %q", metadata.Labels["app.kubernetes.io/part-of"])
	}
	if metadata.Labels["rtk.realtek.com/runtime"] != "kubernetes" {
		t.Fatalf("runtime label got %q", metadata.Labels["rtk.realtek.com/runtime"])
	}
}

func TestK8STypedSecretObjectUsesStringData(t *testing.T) {
	secret := newK8SSecretObject("video-cloud-staging-video-cloud", "video-cloud-runtime", map[string]string{
		"VIDEO_CLOUD_LOGGER_TOKEN": "secret-token",
	})
	if secret["kind"] != "Secret" {
		t.Fatalf("kind got %v", secret["kind"])
	}
	stringData, ok := secret["stringData"].(map[string]string)
	if !ok {
		t.Fatalf("stringData type got %T", secret["stringData"])
	}
	if stringData["VIDEO_CLOUD_LOGGER_TOKEN"] != "secret-token" {
		t.Fatalf("secret value missing from typed object")
	}
}

func TestRenderK8SNamespaceTemplate(t *testing.T) {
	manifest, err := renderK8SNamespaceManifest("video-cloud-staging-video-cloud", "video-cloud-staging")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"kind: Namespace",
		"name: video-cloud-staging-video-cloud",
		"rtk.realtek.com/runtime: kubernetes",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("namespace manifest missing %q:\n%s", want, manifest)
		}
	}
}
