package main

import (
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func controllerPermissionFixture(env string) deploymentplan.Object {
	r := identityPermissionFixture(env)
	pod := environmentPKIPod(r.Desired)
	c := deploymentplan.Map(identityPermissionList(pod["containers"])[0])
	c["env"] = []any{deploymentplan.Object{"name": "PKI_HOST_IDENTITY_STATE", "value": "/var/lib/identity/identity/host.json"}, deploymentplan.Object{"name": "PKI_SERVICE_CLIENT_IDENTITY_STATE", "value": "/var/lib/identity/identity/client.json"}}
	deploymentplan.Map(pod["securityContext"])["fsGroup"] = float64(10001)
	return r.Desired
}

func TestControllerPermissionPreparationProfile(t *testing.T) {
	for _, env := range []string{"dev", "staging", "prod"} {
		t.Run(env, func(t *testing.T) {
			object := controllerPermissionFixture(env)
			release := environmentRelease{Images: map[string]environmentReleaseImage{"LKE_ALPINE_IMAGE": {Reference: identityPermissionAlpine}}}
			before := deploymentplan.Clone(object)
			if err := applyControllerIdentityPreparation(object, release); err != nil {
				t.Fatal(err)
			}
			if err := controllerIdentityTargetPolicy(deploymentplan.Resource{ID: deploymentplan.ID(object), Desired: object}, identityPermissionAlpine); err != nil {
				t.Fatal(err)
			}
			pod := environmentPKIPod(object)
			if deploymentplan.Map(pod["securityContext"])["fsGroup"] != float64(10001) || deploymentplan.Map(pod["securityContext"])["fsGroupChangePolicy"] != "OnRootMismatch" {
				t.Fatal("Secret/token group readability lost")
			}
			if deploymentplan.Digest(pod["volumes"]) != deploymentplan.Digest(environmentPKIPod(before)["volumes"]) || deploymentplan.Digest(pod["containers"]) != deploymentplan.Digest(environmentPKIPod(before)["containers"]) {
				t.Fatal("original identity references or consumers changed")
			}
			for _, mutation := range []string{"command", "image", "env", "mount", "privilege", "sidecar"} {
				changed := deploymentplan.Clone(object)
				init := deploymentplan.Map(identityPermissionList(environmentPKIPod(changed)["initContainers"])[0])
				switch mutation {
				case "command":
					init["command"] = []any{"sh", "-ec", "rm -rf /identity-volume"}
				case "image":
					init["image"] = "alpine:latest"
				case "env":
					init["env"] = []any{deploymentplan.Object{"name": "TOKEN", "value": "private-token"}}
				case "mount":
					deploymentplan.Map(identityPermissionList(init["volumeMounts"])[0])["mountPath"] = "/other"
				case "privilege":
					deploymentplan.Map(init["securityContext"])["privileged"] = true
				case "sidecar":
					init["restartPolicy"] = "Always"
				}
				if err := validateControllerIdentityInitializer(changed, identityPermissionAlpine); err == nil {
					t.Fatalf("arbitrary initializer accepted: %s", mutation)
				}
			}
		})
	}
}

func TestControllerPermissionRecipePreservesStateAndRejectsLinks(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "symlink", "hardlink"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, "identity")
			if err := os.Mkdir(directory, 0770); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(directory, "client.json")
			bytes := []byte("original synthetic identity\n")
			if err := os.WriteFile(state, bytes, 0660); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(root, "outside")
			if err := os.WriteFile(outside, []byte("untouched"), 0644); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing":
				os.Remove(state)
			case "symlink":
				os.Symlink(outside, filepath.Join(directory, "unsafe"))
			case "hardlink":
				os.Link(outside, filepath.Join(directory, "unsafe"))
			}
			bin := filepath.Join(root, "bin")
			os.Mkdir(bin, 0700)
			// Tests do not take root authority or alter ownership outside isolated fixtures.
			os.WriteFile(filepath.Join(bin, "chown"), []byte("#!/bin/sh\nexit 0\n"), 0700)
			script := strings.ReplaceAll(controllerIdentityPreparation, "/identity-volume", root)
			cmd := exec.Command("/bin/sh", "-ec", script, "test", state)
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			output, err := cmd.CombinedOutput()
			if mode != "valid" {
				if err == nil {
					t.Fatal("unsafe/missing state repaired")
				}
				return
			}
			if err != nil {
				t.Fatalf("permission recipe failed: %v %s", err, output)
			}
			after, _ := os.ReadFile(state)
			if sha256.Sum256(after) != sha256.Sum256(bytes) {
				t.Fatal("identity bytes changed")
			}
			info, _ := os.Stat(directory)
			if info.Mode().Perm() != 0700 {
				t.Fatal("directory is not private")
			}
			info, _ = os.Stat(state)
			if info.Mode().Perm() != 0600 {
				t.Fatal("state is not private")
			}
			info, _ = os.Stat(outside)
			if info.Mode().Perm() != 0644 {
				t.Fatal("scope escaped identity directory")
			}
		})
	}
}

func TestControllerPermissionInitializerAdoptionKeepsOnlyReferences(t *testing.T) {
	object := controllerPermissionFixture("dev")
	meta := deploymentplan.Map(object["metadata"])
	meta["uid"] = "original-controller"
	meta["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-dev"}
	image := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("a", 64)
	deploymentplan.Map(identityPermissionList(environmentPKIPod(object)["containers"])[0])["image"] = image
	release := environmentRelease{Environment: "dev", Stack: "video-cloud-dev", Images: map[string]environmentReleaseImage{"LKE_ALPINE_IMAGE": {Reference: identityPermissionAlpine}, "LKE_VIDEO_CLOUD_IMAGE": {Reference: image}}}
	main := deploymentplan.Map(identityPermissionList(environmentPKIPod(object)["containers"])[0])
	main["env"] = append(identityPermissionList(main["env"]), deploymentplan.Object{"name": "RTK_DEPLOYMENT_SERVICE_ISSUER_ID", "value": "original-registry-issuer"})
	if err := applyControllerIdentityPreparation(object, release); err != nil {
		t.Fatal(err)
	}
	bindings, err := observeEnvironmentBindings("dev", release, []deploymentplan.Object{object})
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings.Pods) != 1 || len(bindings.Pods[0].InitContainers) != 1 {
		t.Fatal("maintained initializer lost during observation")
	}
	if mqttFoundationEnv(bindings.Pods[0].Containers[0].Fields, "RTK_DEPLOYMENT_SERVICE_ISSUER_ID") != "original-registry-issuer" {
		t.Fatal("original registry issuer metadata dropped")
	}
	init := bindings.Pods[0].InitContainers[0]
	if init.ImageKey != "LKE_ALPINE_IMAGE" || len(init.Fields) != 1 || init.Fields["volumeMounts"] == nil {
		t.Fatal("live commands or privilege imported into bindings")
	}
	changed := deploymentplan.Clone(object)
	deploymentplan.Map(identityPermissionList(environmentPKIPod(changed)["initContainers"])[0])["command"] = []any{"sh", "-ec", "echo unsupported"}
	if _, err := observeEnvironmentBindings("dev", release, []deploymentplan.Object{changed}); err == nil {
		t.Fatal("unsupported live initializer adopted")
	}
}

func TestPKIRuntimeQualificationPreservesSafeDiagnosticsOnly(t *testing.T) {
	for _, err := range []error{secretCheckSummary("live Kubernetes secret validation failed: original consumer unavailable"), &deploymentRuntimeError{"KUBE_TIMEOUT", "required read timed out"}} {
		var blocker *deploymentplan.Blocker
		got := environmentRuntimePKIQualificationError(err)
		if !errors.As(got, &blocker) || blocker.Cause != err.Error() {
			t.Fatal("sanitized validation detail lost")
		}
	}
	got := environmentRuntimePKIQualificationError(errors.New("private-token-from-provider"))
	if strings.Contains(got.Error(), "private-token") {
		t.Fatal("unknown private error exposed")
	}
}
