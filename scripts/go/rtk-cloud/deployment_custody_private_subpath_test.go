package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestDeviceMemoryPrivateSubPathProjection(t *testing.T) {
	cfg, p, release, runtime, s, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	defs, err := finalizeEnvironmentCreateApplicationDefinitions(t.Context(), cfg, p, release, runtime, s, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	original := createRecipeObject(t, defs, "Deployment/"+p.Stack+"-video-cloud/video-cloud-api-pki")
	pvc := deploymentplan.Clone(original)
	changed, err := projectDeviceMemoryTrustPrivateSubPath(pvc)
	if err != nil || changed || deploymentplan.Digest(pvc) != deploymentplan.Digest(original) {
		t.Fatal("PVC changed", err)
	}
	obj := deploymentplan.Clone(original)
	pod := environmentPKIPod(obj)
	v := managedNamedField(pod["volumes"], "pki-state")
	delete(v, "persistentVolumeClaim")
	v["emptyDir"] = deploymentplan.Object{"medium": "Memory"}
	projectDeviceMemoryTrustInitializer(obj)
	changed, err = projectDeviceMemoryTrustPrivateSubPath(obj)
	if err != nil || !changed {
		t.Fatal("projection", err)
	}
	init := managedNamedField(pod["initContainers"], "initialize-trust-state")
	im := managedNamedField(init["volumeMounts"], "pki-state")
	if im["subPath"] != nil {
		t.Fatal("initializer must mount root")
	}
	count := 0
	for _, raw := range certIssuerObjectList(pod["containers"]) {
		c := deploymentplan.Map(raw)
		for _, item := range certIssuerObjectList(c["volumeMounts"]) {
			m := deploymentplan.Map(item)
			if m["name"] == "pki-state" && m["mountPath"] == "/run/pki-state" {
				count++
				if m["subPath"] != "private" {
					t.Fatal("missing private subpath")
				}
			}
		}
	}
	if count == 0 {
		t.Fatal("no application mount")
	}
	var b environmentPodBindings
	if err := observeDeviceInitializer(&b, init, release, "device"); err != nil || !b.NativeTrustInitialization {
		t.Fatal("observation", err)
	}
	digest := deploymentplan.Digest(obj)
	changed, err = projectDeviceMemoryTrustPrivateSubPath(obj)
	if err != nil || changed || deploymentplan.Digest(obj) != digest {
		t.Fatal("not idempotent", err)
	}
}

func TestDevicePrivateTrustPreparationPreservesState(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "mount")
	if err := os.Mkdir(root, 0777); err != nil {
		t.Fatal(err)
	}
	state := "/run/pki-state/root-trust-01010101-0202-0303-0404-050505050505.json"
	script := strings.ReplaceAll(devicePrivateMemoryTrustPreparation(state), "/run/pki-state", root)
	run := func() {
		t.Helper()
		if out, err := exec.Command("/bin/sh", "-ec", script).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	run()
	private := filepath.Join(root, "private")
	info, err := os.Stat(private)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("private directory mode", err)
	}
	file := filepath.Join(private, filepath.Base(state))
	if err := os.WriteFile(file, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	run()
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "original" {
		t.Fatal("state changed", err)
	}
	info, err = os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("file mode", err)
	}
}

func TestDeviceMemoryPrivateSubPathRejectsForeignMount(t *testing.T) {
	cfg, p, release, runtime, s, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	defs, err := finalizeEnvironmentCreateApplicationDefinitions(t.Context(), cfg, p, release, runtime, s, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	original := createRecipeObject(t, defs, "Deployment/"+p.Stack+"-video-cloud/video-cloud-api-pki")
	obj := deploymentplan.Clone(original)
	pod := environmentPKIPod(obj)
	v := managedNamedField(pod["volumes"], "pki-state")
	delete(v, "persistentVolumeClaim")
	v["emptyDir"] = deploymentplan.Object{"medium": "Memory"}
	projectDeviceMemoryTrustInitializer(obj)
	app := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	mount := managedNamedField(app["volumeMounts"], "pki-state")
	mount["subPath"] = "foreign"
	before := deploymentplan.Digest(obj)
	if changed, err := projectDeviceMemoryTrustPrivateSubPath(obj); err == nil || changed || deploymentplan.Digest(obj) != before {
		t.Fatal("foreign mount accepted or changed", err)
	}
}

func TestDevicePrivateTrustPreparationRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "mount")
	other := filepath.Join(dir, "other")
	if err := os.Mkdir(root, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(other, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(root, "private")); err != nil {
		t.Fatal(err)
	}
	file := "/run/pki-state/root-trust-01010101-0202-0303-0404-050505050505.json"
	script := strings.ReplaceAll(devicePrivateMemoryTrustPreparation(file), "/run/pki-state", root)
	if out, err := exec.Command("/bin/sh", "-ec", script).CombinedOutput(); err == nil {
		t.Fatalf("symlink accepted: %s", out)
	}
	info, err := os.Stat(other)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("foreign target changed", err)
	}
}
