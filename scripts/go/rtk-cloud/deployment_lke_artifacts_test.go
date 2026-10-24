package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLKEOperationalArtifactsUsePrivateEnvironmentStoreInEveryCheckout(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, environment)
			paths := provisionPaths{EnvRoot: t.TempDir()}
			writeTestFile(t, filepath.Join(paths.EnvRoot, "env", "stack.env"), "CLOUD_ENV_NAME="+environment+"\n")
			legacy := filepath.Join(paths.EnvRoot, "artifacts", "coturn-vm", "coturn-vm.json")
			writeTestFile(t, legacy, "legacy-evidence-must-not-change")
			if err := writeLKEEdgeHAProxyArtifacts(paths, lkeEdgeHAProxyPlan{}, "fixture-config", "fixture-install", map[string]any{"passed": true}, lkeEdgeHAProxySSHAccess{}); err != nil {
				t.Fatal(err)
			}
			vm := lkeCoturnVM{Name: "turn01", Domain: "turn.example.test"}
			if err := writeLKECoturnVMArtifacts(paths, vm, "redacted-config", "fixture-install", map[string]any{"passed": true}); err != nil {
				t.Fatal(err)
			}
			if err := writeLKECoturnVMsSummary(paths, []lkeCoturnVM{vm}); err != nil {
				t.Fatal(err)
			}
			for _, relative := range []string{"edge-haproxy/edge-vms.json", "edge-haproxy/upstreams.json", "edge-haproxy/haproxy.cfg", "edge-haproxy/install.sh", "edge-haproxy/validation.json", "coturn-vm/turn01/coturn-vm.json", "coturn-vm/turn01/install.sh", "coturn-vm/turn01/turnserver.conf.redacted", "coturn-vm/turn01/validation.json", "coturn-vm/coturn-vms.json"} {
				path, err := deploymentLKEArtifactPath(paths, relative)
				if err != nil || path != filepath.Join(store.Root, "deployment", "artifacts", relative) {
					t.Fatal("artifact destination is not canonical", path, err)
				}
				if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
					t.Fatal("operational artifact is not a private regular file", path, err)
				}
				for directory := filepath.Dir(path); directory != filepath.Dir(store.ConfigRoot); directory = filepath.Dir(directory) {
					if info, err := os.Lstat(directory); err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
						t.Fatal("operational artifact directory is not private", directory, err)
					}
				}
			}
			other := provisionPaths{EnvRoot: t.TempDir()}
			writeTestFile(t, filepath.Join(other.EnvRoot, "env", "stack.env"), "CLOUD_ENV_NAME="+environment+"\n")
			first, _ := deploymentLKEArtifactPath(paths, "coturn-vm/turn01/coturn-vm.json")
			second, _ := deploymentLKEArtifactPath(other, "coturn-vm/turn01/coturn-vm.json")
			if first != second || readTestFile(t, legacy) != "legacy-evidence-must-not-change" {
				t.Fatal("changing checkout changes state authority or overwrites legacy evidence")
			}
			if _, err := os.Stat(filepath.Join(store.Root, "deployment", "artifacts", "coturn-vm", "coturn-vm.json")); !os.IsNotExist(err) {
				t.Fatal("legacy duplicate TURN evidence was recreated")
			}
		})
	}
}

func TestLKEOperationalArtifactsRejectAmbiguousOrUnsafeLocations(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	paths := provisionPaths{EnvRoot: t.TempDir()}
	writeTestFile(t, filepath.Join(paths.EnvRoot, "env", "stack.env"), "CLOUD_ENV_NAME=dev\n")
	for _, relative := range []string{"../operator.env", "coturn-vm/../wrong.json", "coturn-vm/turn01/../../operator.env", "coturn-vm//x", "coturn-vm/x:y", "/coturn-vm/x", "unknown/x"} {
		if err := writeDeploymentLKEArtifact(paths, relative, []byte("fixture")); err == nil {
			t.Fatal("unsafe artifact destination accepted", relative)
		}
	}
	if _, err := deploymentLKEArtifactPath(provisionPaths{EnvRoot: t.TempDir()}, "coturn-vm/turn01/coturn-vm.json"); err == nil {
		t.Fatal("missing environment silently selected a default")
	}
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(store.Root, "deployment"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(store.Root, "deployment", "artifacts")); err != nil {
		t.Fatal(err)
	}
	if err := writeDeploymentLKEArtifact(paths, "edge-haproxy/validation.json", []byte("fixture")); err == nil {
		t.Fatal("symlink artifact directory accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("unsafe writer escaped the store", err)
	}
}
