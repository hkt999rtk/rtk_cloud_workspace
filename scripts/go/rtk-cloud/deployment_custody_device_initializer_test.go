package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestDeviceTrustMemoryProjectionKeepsMountRootUnchanged(t *testing.T) {
	for _, env := range []string{"dev", "staging"} {
		t.Run(env, func(t *testing.T) {
			cfg, p, release, runtime, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, env)
			cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
			definitions, err := finalizeEnvironmentCreateApplicationDefinitions(t.Context(), cfg, p, release, runtime, service, roots, intermediates, initial)
			if err != nil {
				t.Fatal(err)
			}
			id := "Deployment/" + p.Stack + "-video-cloud/video-cloud-api-pki"
			original := createRecipeObject(t, definitions, id)
			pod := environmentPKIPod(original)
			init := managedNamedField(pod["initContainers"], "initialize-trust-state")
			cmd := certIssuerObjectList(init["command"])
			if len(cmd) != 3 || !strings.Contains(deploymentplan.String(cmd[2]), "chmod 700 /run/pki-state;") {
				t.Fatal("original PVC recipe changed")
			}
			binding, err := createEnvironmentCustodyBinding(cfg, p, strings.Repeat("a", 64), "")
			if err != nil {
				t.Fatal(err)
			}
			before := deploymentplan.Digest(definitions)
			projected, err := projectEnvironmentCustodyResources(cfg.Values, env, binding, strings.Repeat("b", 64), definitions)
			if err != nil {
				t.Fatal(err)
			}
			if before != deploymentplan.Digest(definitions) {
				t.Fatal("projection modified source")
			}
			obj := createRecipeObject(t, projected, id)
			pp := environmentPKIPod(obj)
			v := managedNamedField(pp["volumes"], "pki-state")
			if deploymentplan.Map(v["emptyDir"])["medium"] != "Memory" {
				t.Fatal("wrong memory mount")
			}
			init = managedNamedField(pp["initContainers"], "initialize-trust-state")
			cmd = certIssuerObjectList(init["command"])
			script := deploymentplan.String(cmd[2])
			if strings.Contains(script, "chmod 700 /run/pki-state;") || !strings.Contains(script, "chmod 700 /run/pki-state/private;") {
				t.Fatal("unsafe memory preparation", script)
			}
			var observed environmentPodBindings
			if err := observeDeviceInitializer(&observed, init, release, id); err != nil || !observed.NativeTrustInitialization {
				t.Fatal("upgrade cannot observe memory recipe", err)
			}
			again, err := projectEnvironmentCustodyResources(cfg.Values, env, binding, strings.Repeat("b", 64), projected)
			if err != nil || deploymentplan.Digest(again) != deploymentplan.Digest(projected) {
				t.Fatal("projection not idempotent", err)
			}
		})
	}
}

func TestDeviceMemoryPreparationPreservesMountAndExistingState(t *testing.T) {
	root := t.TempDir()
	mount := filepath.Join(root, "state")
	if err := os.Mkdir(mount, 01777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(mount, 0777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	file := "/run/pki-state/root-trust-01010101-0202-0303-0404-050505050505.json"
	recipe := strings.ReplaceAll(deviceMemoryTrustPreparation(file), "/run/pki-state", mount)
	run := func() {
		t.Helper()
		if out, err := exec.Command("/bin/sh", "-ec", recipe).CombinedOutput(); err != nil {
			t.Fatal("initializer failed", err, string(out))
		}
	}
	run()
	path := filepath.Join(mount, filepath.Base(file))
	if err := os.WriteFile(path, []byte("original-state"), 0644); err != nil {
		t.Fatal(err)
	}
	run()
	info, err := os.Stat(mount)
	if err != nil || info.Mode()&os.ModeSticky == 0 || info.Mode().Perm() != 0777 {
		t.Fatal("memory mount permissions changed", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original-state" {
		t.Fatal("original state changed", err)
	}
	info, err = os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("file permissions incorrect", err)
	}
}

func TestNativeDeviceCallbackRecognizesMemoryInitializer(t *testing.T) {
	_, p, _, _, _, definitions := createdNativeInventoryFixture(t, "dev")
	id := "Deployment/" + p.Stack + "-video-cloud/video-cloud-api-pki"
	o := deploymentplan.Clone(createRecipeObject(t, definitions, id))
	pod := environmentPKIPod(o)
	container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	state := mqttFoundationEnv(container, "VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_STATE")
	init := managedNamedField(pod["initContainers"], "initialize-trust-state")
	init["command"] = []any{"/bin/sh", "-ec", deviceMemoryTrustPreparation(state)}
	if err := validateEnvironmentDeviceMQTTCallback([]deploymentplan.Definition{{Object: o}}); err != nil {
		t.Fatal("memory recipe rejected", err)
	}
}

func TestDeviceMemoryInitializerSuccessorIsExactAndIdempotent(t *testing.T) {
	cfg, p, release, runtime, s, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	defs, err := finalizeEnvironmentCreateApplicationDefinitions(t.Context(), cfg, p, release, runtime, s, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	original := createRecipeObject(t, defs, "Deployment/"+p.Stack+"-video-cloud/video-cloud-api-pki")
	before := deploymentplan.Digest(original)
	if projectDeviceMemoryTrustInitializer(deploymentplan.Clone(original)) {
		t.Fatal("PVC recipe altered")
	}
	copy := deploymentplan.Clone(original)
	pod := environmentPKIPod(copy)
	volume := managedNamedField(pod["volumes"], "pki-state")
	delete(volume, "persistentVolumeClaim")
	volume["emptyDir"] = deploymentplan.Object{"medium": "Memory"}
	if !projectDeviceMemoryTrustInitializer(copy) {
		t.Fatal("memory successor not generated")
	}
	first := deploymentplan.Digest(copy)
	if projectDeviceMemoryTrustInitializer(copy) || deploymentplan.Digest(copy) != first || deploymentplan.Digest(original) != before {
		t.Fatal("not immutable/idempotent")
	}
}
