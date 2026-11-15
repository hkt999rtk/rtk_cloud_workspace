package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCIRolloutKubeconfigContainsOnlyRestrictedIdentity(t *testing.T) {
	original, err := yaml.Marshal(frozenKubernetesFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	config, err := ciRolloutKubeconfig(string(original), "dev", "restricted-ci-token")
	if err != nil || !strings.Contains(string(config), "restricted-ci-token") || strings.Contains(string(config), "private-frozen-token") || strings.Contains(string(config), "must-not-execute") {
		t.Fatalf("restricted identity not isolated: %v", err)
	}
	if _, err = newEnvironmentKubernetesBackend(string(config), "dev"); err != nil {
		t.Fatal(err)
	}
}

func TestCIRolloutIdentityBindingRejectsReplacement(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	binding := ciRolloutIdentityBinding{Environment: "dev", Repository: "hkt999rtk/rtk_cloud_frontend", Namespace: "video-cloud-dev-video-cloud", ServiceAccount: "rtk-ci-rtk-cloud-frontend", ServiceAccountUID: "sa-uid", TokenSecret: "rtk-ci-rtk-cloud-frontend-token", TokenSecretUID: "secret-uid"}
	base, err := ciRolloutSaveBinding(store, binding, []byte("restricted-kubeconfig"), []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Root, base, "kubeconfig.yaml")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("identity permissions %v %v", info, err)
	}
	if _, err = ciRolloutSaveBinding(store, binding, []byte("different-kubeconfig"), []byte("{}")); err == nil {
		t.Fatal("different credentials for an existing identity accepted")
	}
	binding.TokenSecretUID = "replacement"
	if _, err = ciRolloutSaveBinding(store, binding, []byte("replacement"), []byte("{}")); err == nil {
		t.Fatal("replaced token identity accepted")
	}
	value, err := os.ReadFile(path)
	if err != nil || string(value) != "restricted-kubeconfig" {
		t.Fatal("original identity was modified", err)
	}
}

func TestCIRolloutAccessVerificationRejectsBroadIdentity(t *testing.T) {
	cfg := frozenKubernetesFixture(t)
	raw, _ := yaml.Marshal(cfg)
	backend, err := newEnvironmentKubernetesBackend(string(raw), "dev")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	tool := filepath.Join(dir, "kubectl")
	script := `#!/bin/sh
case "$*" in *" get secrets "*|*" create pods "*|*" patch deployments.apps/rtk-ci-unrelated-probe "*) if [ "$CI_TEST_BROAD" = yes ]; then echo yes; exit 0; else echo no; exit 1; fi ;; *) echo yes; exit 0 ;; esac
`
	if err = os.WriteFile(tool, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", tool)
	target := ciRolloutTarget{Namespace: "video-cloud-dev-frontend", Name: "frontend", UID: "uid"}
	if err = ciRolloutVerifyAccess(context.Background(), backend, "video-cloud-dev-video-cloud", "hkt999rtk/rtk_cloud_frontend", []ciRolloutTarget{target}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CI_TEST_BROAD", "yes")
	if err = ciRolloutVerifyAccess(context.Background(), backend, "video-cloud-dev-video-cloud", "hkt999rtk/rtk_cloud_frontend", []ciRolloutTarget{target}); err == nil {
		t.Fatal("identity with broad Secret or Pod access accepted")
	}
}
