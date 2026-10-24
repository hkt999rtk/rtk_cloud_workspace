package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestDeviceInitializersPreserveOriginalPathsAndFormalRolesAcrossEnvironments(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-device-init")
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, bindings, release := completeEnvironmentRenderFixture(environment)
			restore := installAllCredentialEnvironment(env)
			defer restore()
			var binding environmentPodBindings
			for _, b := range bindings.Pods {
				if b.Component == "managed-api" {
					binding = b
				}
			}
			binding.DeviceTrustStateFile = "/run/pki-state/root-trust-01010101-0202-0303-0404-050505050505.json"
			for _, name := range []string{"initialize-trust-state", "initialize-controller-identity"} {
				binding.InitContainers = append(binding.InitContainers, environmentContainerBindings{Name: name, ImageKey: "LKE_VIDEO_CLOUD_IMAGE", Fields: map[string]any{}})
			}
			current, err := renderManagedEnvironmentComponent(env, binding)
			if err != nil {
				t.Fatal(err)
			}
			deploymentplan.Map(current["metadata"])["uid"] = "original-device-api"
			for _, v := range certIssuerObjectList(environmentPKIPod(current)["containers"]) {
				deploymentplan.Map(v)["image"] = release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference
			}
			for _, v := range certIssuerObjectList(environmentPKIPod(current)["initContainers"]) {
				deploymentplan.Map(v)["image"] = "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:" + strings.Repeat("d", 64)
			}
			environmentPKIPod(current)["volumes"] = append(certIssuerObjectList(environmentPKIPod(current)["volumes"]), map[string]any{"name": "pki-state", "persistentVolumeClaim": map[string]any{"claimName": "original-device-trust"}}, map[string]any{"name": "pki-initial-state", "secret": map[string]any{"secretName": "original-initial-trust"}}, map[string]any{"name": "controller-identity", "persistentVolumeClaim": map[string]any{"claimName": "original-controller-identity"}})
			observed, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{current})
			if err != nil {
				t.Fatal(err)
			}
			if err := validateEnvironmentIdentityBindings(observed, environment); err != nil {
				t.Fatal(err)
			}
			if observed.Pods[0].DeviceTrustStateFile != binding.DeviceTrustStateFile {
				t.Fatal("original filename lost")
			}
			target, err := renderManagedEnvironmentComponent(env, observed.Pods[0])
			if err != nil {
				t.Fatal(err)
			}
			if err := applyEnvironmentPodBindings(target, observed, release); err != nil {
				t.Fatal(err)
			}
			if err := validateEnvironmentPKITransition([]deploymentplan.Definition{{Object: target}}, []deploymentplan.Object{current}); err != nil {
				t.Fatal(err)
			}
			for _, v := range certIssuerObjectList(environmentPKIPod(target)["initContainers"]) {
				if deploymentplan.Map(v)["image"] != release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference {
					t.Fatal("initializer not formally pinned")
				}
			}
			bad := deploymentplan.Clone(current)
			deploymentplan.Map(certIssuerObjectList(environmentPKIPod(bad)["initContainers"])[0])["command"] = []any{"/bin/sh", "-ec", "cp /run/pki-initial/root-trust.json /run/pki-state/replacement.json"}
			if _, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{bad}); err == nil {
				t.Fatal("unqualified state replacement accepted")
			}
		})
	}
}
func TestDeviceTrustPreparationRetriesWithoutOverwritingState(t *testing.T) {
	root := t.TempDir()
	state, source := filepath.Join(root, "state"), filepath.Join(root, "source")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	sourceFile := filepath.Join(source, "root-trust.json")
	if err := os.WriteFile(sourceFile, []byte("original-policy"), 0600); err != nil {
		t.Fatal(err)
	}
	file := "/run/pki-state/root-trust-01010101-0202-0303-0404-050505050505.json"
	recipe := strings.ReplaceAll(strings.ReplaceAll(deviceTrustPreparation(file), "/run/pki-state", state), "/run/pki-initial", source)
	run := func() {
		t.Helper()
		if err := exec.Command("/bin/sh", "-ec", recipe).Run(); err != nil {
			t.Fatal("isolated preparation failed", err)
		}
	}
	run()
	target := filepath.Join(state, filepath.Base(file))
	if err := os.WriteFile(sourceFile, []byte("different-policy"), 0600); err != nil {
		t.Fatal(err)
	}
	run()
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "original-policy" {
		t.Fatal("retry reset trust state")
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private permissions missing")
	}
}
