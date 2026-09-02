package main

import (
	"os"
	"path/filepath"
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

func TestSharedKubernetesPlacementUsesPerWorkloadNodeClasses(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kubectl.log")
	kubectl := filepath.Join(dir, "kubectl")
	writeTestFile(t, kubectl, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+logPath+"\"\ncase \"$*\" in *\" get \"*) printf 'present\\n' ;; esac\n")
	if err := os.Chmod(kubectl, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "1")
	env := map[string]string{
		"DEPLOYMENT_ARCHITECTURE": "kubernetes", "CLOUD_STACK_NAME": "video-cloud-dev",
		"DEFAULT_WORKLOAD_NODE_CLASS": "general", "NODE_CLASS_LABEL_KEY": "rtk.io/node-class",
		"MQTT_NODE_CLASS": "general", "POSTGRES_NODE_CLASS": "general",
	}
	if err := applySharedKubernetesNodeClassPlacement(provisionContext{Env: env}); err != nil {
		t.Fatal(err)
	}
	log := readTestFile(t, logPath)
	for _, want := range []string{
		"-n video-cloud-dev-billing patch deployment billing",
		"-n video-cloud-dev-billing patch deployment billing-payment-worker",
		"-n video-cloud-dev-account-manager patch deployment account-manager-email-worker",
		"-n video-cloud-dev-platform patch statefulset postgresql",
		"-n video-cloud-dev-video-cloud patch statefulset mqtt",
		`rtk.io/node-class":"general`,
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("placement log missing %q:\n%s", want, log)
		}
	}
}
