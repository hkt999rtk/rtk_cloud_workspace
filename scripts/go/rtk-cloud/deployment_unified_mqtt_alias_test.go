package main

import (
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func nativeCoreAliasFixture(t *testing.T) ([]deploymentplan.Definition, deploymentplan.Object) {
	t.Helper()
	definitions, live := coreMQTTFixture(t, "dev")
	pod := environmentPKIPod(definitions[0].Object)
	app := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	fields, _ := environmentPKINamedFields(app["env"])
	fields["VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE"]["value"] = "/var/lib/video-cloud-service/private/client.json"
	mounts, _ := environmentPKINamedFields(app["volumeMounts"])
	mounts["service-identity"]["mountPath"] = "/var/lib/video-cloud-service"
	pod["volumes"] = append(certIssuerObjectList(pod["volumes"]), environmentCreateServiceVolume("service-state", "persistentVolumeClaim", "original-client-identity"))
	app["volumeMounts"] = append(certIssuerObjectList(app["volumeMounts"]), environmentCreateServiceMount("service-state", "/var/lib/pki-service", false))
	for _, object := range live {
		if deploymentplan.Map(object["metadata"])["name"] == "account-manager-crls" {
			deploymentplan.Map(object["data"])["crls.json"] = `[{"state_path":"/var/lib/pki-service/private/crl/outbound-root.json"}]`
		}
		definitions = append(definitions, deploymentplan.Definition{Object: object})
	}
	return definitions, app
}

func TestNativeMQTTStateAliasRequiresTheSameFullWritableClaim(t *testing.T) {
	for _, problem := range []string{"", "foreign-claim", "readonly", "subpath", "subpath-expression", "shadow", "duplicate-path", "traversal", "outside", "primary-readonly", "mixed-source", "filesystem-root"} {
		t.Run(problem, func(t *testing.T) {
			definitions, app := nativeCoreAliasFixture(t)
			object := definitions[0].Object
			pod := environmentPKIPod(object)
			volumes, _ := environmentPKINamedFields(pod["volumes"])
			mounts, _ := environmentPKINamedFields(app["volumeMounts"])
			file := "/var/lib/pki-service/private/crl/outbound-root.json"
			switch problem {
			case "foreign-claim":
				deploymentplan.Map(volumes["service-state"]["persistentVolumeClaim"])["claimName"] = "another-owner"
			case "readonly":
				mounts["service-state"]["readOnly"] = true
			case "subpath":
				mounts["service-state"]["subPath"] = "private"
			case "subpath-expression":
				mounts["service-state"]["subPathExpr"] = "$(POD_NAME)"
			case "shadow":
				app["volumeMounts"] = append(certIssuerObjectList(app["volumeMounts"]), deploymentplan.Object{"name": "shadow", "mountPath": file})
			case "duplicate-path":
				app["volumeMounts"] = append(certIssuerObjectList(app["volumeMounts"]), deploymentplan.Object{"name": "another", "mountPath": "/var/lib/pki-service"})
			case "traversal":
				file = "/var/lib/pki-service/private/../crl/state.json"
			case "outside":
				file = "/another-owner/private/crl/state.json"
			case "primary-readonly":
				mounts["service-identity"]["readOnly"] = true
			case "mixed-source":
				volumes["service-state"]["secret"] = deploymentplan.Object{"secretName": "other-source"}
			case "filesystem-root":
				mounts["service-identity"]["mountPath"] = "/"
			}
			relative, valid := managedMQTTPVCRelativeFile(object, app, "service-identity", file)
			if problem == "" && (!valid || relative != "private/crl/outbound-root.json") || problem != "" && valid {
				t.Fatal("original-PVC alias accepted a changed source or lost the native relative cache", relative, valid)
			}
		})
	}
}

func TestNativeCoreCRLAliasPreparesOnlyItsExistingPrivateDirectory(t *testing.T) {
	definitions, app := nativeCoreAliasFixture(t)
	object := definitions[0].Object
	before := deploymentplan.Digest(environmentPKIPod(object)["volumes"])
	if err := deriveEnvironmentCoreMQTTTransport(nil, definitions); err != nil {
		t.Fatal(err)
	}
	if err := applyCoreIdentityPreparation(definitions, identityPermissionAlpine); err != nil {
		t.Fatal(err)
	}
	if err := validateEnvironmentCoreMQTTTransport(definitions); err != nil {
		t.Fatal(err)
	}
	if err := validateCoreIdentityInitializer(object, identityPermissionAlpine); err != nil {
		t.Fatal(err)
	}
	initializer := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(object)["initContainers"])[0])
	if deploymentplan.Digest(initializer["command"]) != deploymentplan.Digest([]any{"/bin/sh", "-ec", coreNativeIdentityPreparation}) || deploymentplan.Digest(initializer["args"]) != deploymentplan.Digest([]any{"identity-permissions", "/identity-volume/private/client.json"}) {
		t.Fatal("native cache initializer changed state location or selected the volume root")
	}
	if deploymentplan.Digest(environmentPKIPod(object)["volumes"]) != before {
		t.Fatal("native cache custody changed during permission preparation")
	}
	if !coreCRLParentPrepared(object, app, "/var/lib/pki-service/private/crl/outbound-root.json") || coreCRLParentPrepared(object, app, "/var/lib/pki-service/elsewhere/outbound-root.json") || coreCRLParentPrepared(object, app, "/var/lib/pki-service/outbound-root.json") {
		t.Fatal("native cache accepted an unprepared directory or moved cached files to volume root")
	}
}

func TestNativeCorePermissionRecipePreservesBytesAndRejectsUnownedDirectories(t *testing.T) {
	for _, problem := range []string{"", "missing-crl-parent", "private-directory-link", "cache-link", "cache-hardlink", "state-link", "state-hardlink", "state-public", "directory-public"} {
		t.Run(problem, func(t *testing.T) {
			root := t.TempDir()
			private, parent := filepath.Join(root, "private"), filepath.Join(root, "private", "crl")
			if err := os.MkdirAll(parent, 0700); err != nil {
				t.Fatal(err)
			}
			state, cache, outside := filepath.Join(private, "client.json"), filepath.Join(parent, "outbound-root.json"), filepath.Join(root, "outside")
			originalState, originalCache := []byte("original synthetic managed identity"), []byte("original synthetic signed CRL cache")
			if err := os.WriteFile(state, originalState, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cache, originalCache, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(outside, []byte("untouched"), 0644); err != nil {
				t.Fatal(err)
			}
			switch problem {
			case "missing-crl-parent":
				if err := os.RemoveAll(parent); err != nil {
					t.Fatal(err)
				}
			case "private-directory-link":
				if err := os.Rename(private, filepath.Join(root, "original-private")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "original-private"), private); err != nil {
					t.Fatal(err)
				}
			case "cache-link":
				os.Remove(cache)
				if err := os.Symlink(outside, cache); err != nil {
					t.Fatal(err)
				}
			case "cache-hardlink":
				os.Remove(cache)
				if err := os.Link(outside, cache); err != nil {
					t.Fatal(err)
				}
			case "state-link":
				os.Remove(state)
				if err := os.Symlink(outside, state); err != nil {
					t.Fatal(err)
				}
			case "state-hardlink":
				if err := os.Link(state, filepath.Join(root, "another-state-link")); err != nil {
					t.Fatal(err)
				}
			case "state-public":
				os.Chmod(state, 0644)
			case "directory-public":
				os.Chmod(parent, 0770)
			}
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			// Simulate only UID authority in isolated fixtures; real mode/link
			// checks and metadata changes still execute on these actual files.
			stat := `#!/usr/bin/env python3
import os,stat,sys
value=os.stat(sys.argv[3])
print(value.st_nlink if sys.argv[2]=='%h' else format(stat.S_IMODE(value.st_mode),'o')+':10001')
`
			if err := os.WriteFile(filepath.Join(bin, "stat"), []byte(stat), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "chown"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			script := strings.ReplaceAll(coreNativeIdentityPreparation, "/identity-volume", root)
			command := exec.Command("/bin/bash", "-ec", script, "identity-permissions", state)
			command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			output, err := command.CombinedOutput()
			if problem != "" {
				if err == nil {
					t.Fatal("missing or linked private state was silently prepared")
				}
				return
			}
			if err != nil {
				t.Fatalf("native permission-only recipe failed: %v %s", err, output)
			}
			afterState, _ := os.ReadFile(state)
			afterCache, _ := os.ReadFile(cache)
			if sha256.Sum256(afterState) != sha256.Sum256(originalState) || sha256.Sum256(afterCache) != sha256.Sum256(originalCache) {
				t.Fatal("native identity/cache bytes changed")
			}
			outsideBytes, _ := os.ReadFile(outside)
			if string(outsideBytes) != "untouched" {
				t.Fatal("private parent preparation escaped original claim")
			}
		})
	}
}
