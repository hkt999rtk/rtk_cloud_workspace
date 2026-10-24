package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoturnCLIPasswordBootstrapAndUpgradeLifecycle(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"bootstrap", "import-original", "missing-original", "mismatch", "invalid-original", "unsafe-file", "symlink-directory"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				store, err := newSecretStore(t.TempDir(), environment)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(store.ConfigRoot, 0700); err != nil {
					t.Fatal(err)
				}
				original := "original-private-marker"
				bootstrap := false
				switch mode {
				case "bootstrap":
					bootstrap, original = true, ""
				case "missing-original":
					original = ""
				case "invalid-original":
					original = "invalid private-marker"
				case "symlink-directory":
					if err := os.Symlink(t.TempDir(), store.Root); err != nil {
						t.Fatal(err)
					}
				}
				path := filepath.Join(store.Root, "deployment", "turn", "turn01", "cli-password")
				if mode == "mismatch" || mode == "unsafe-file" {
					if _, err := retainCoturnCLIPassword(store, "turn01", "retained-private-marker", false); err != nil {
						t.Fatal(err)
					}
					if mode == "unsafe-file" {
						if err := os.Chmod(path, 0644); err != nil {
							t.Fatal(err)
						}
					}
				}
				value, err := retainCoturnCLIPassword(store, "turn01", original, bootstrap)
				if mode != "bootstrap" && mode != "import-original" {
					if err == nil || strings.Contains(err.Error(), "private-marker") {
						t.Fatal("unsafe lifecycle accepted or credential leaked", err)
					}
					if mode == "mismatch" || mode == "unsafe-file" {
						raw, err := os.ReadFile(path)
						if err != nil || string(raw) != "retained-private-marker\n" {
							t.Fatal("failure replaced original credential")
						}
					} else if mode != "symlink-directory" {
						if _, err := os.Stat(path); !os.IsNotExist(err) {
							t.Fatal("failed upgrade generated a credential")
						}
					}
					return
				}
				if err != nil || !validCoturnCLIPassword(value) || (original != "" && value != original) {
					t.Fatal("valid lifecycle rejected", err)
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal("credential not saved privately", err)
				}
				for _, directory := range []string{store.Root, filepath.Dir(filepath.Dir(path)), filepath.Dir(path)} {
					info, err := os.Stat(directory)
					if err != nil || info.Mode().Perm() != 0700 {
						t.Fatal("credential directory not private", err)
					}
				}
				// Retry, including after allocation but before installation, must
				// use the same canonical credential instead of generating another.
				for _, observed := range []string{"", value} {
					retained, err := retainCoturnCLIPassword(store, "turn01", observed, false)
					if err != nil || retained != value {
						t.Fatal("retry/upgrade did not retain password", err)
					}
				}
				if _, err := os.Stat(filepath.Join(store.ConfigRoot, "other", "deployment", "turn", "turn01", "cli-password")); !os.IsNotExist(err) {
					t.Fatal("credential escaped selected environment")
				}
			})
		}
	}
}

func TestCoturnInstallerPreservesAndFencesCLICredential(t *testing.T) {
	script := renderLKECoturnInstallScript(map[string]string{"CLOUD_ENV_NAME": "dev"})
	if strings.Contains(script, "openssl rand") || !strings.Contains(script, "coturn_cli_password=$COTURN_CLI_PASSWORD") {
		t.Fatal("remote reinstall generates a new credential")
	}
	// Exercise the actual pre-write fence with isolated files, before apt or
	// service changes. Tests never touch runtime /etc or the environment store.
	fence := strings.Split(script, "export DEBIAN_FRONTEND=")[0]
	for _, mode := range []string{"new", "retained", "mismatch", "missing-password", "partial-install"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			input := strings.ReplaceAll(fence, "/etc/", root+"/")
			if mode == "retained" || mode == "mismatch" {
				password := "retained-private-marker"
				if mode == "mismatch" {
					password = "different-private-marker"
				}
				if err := os.WriteFile(filepath.Join(root, "turnserver.conf"), []byte("cli-password="+password+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "video-cloud-coturn-cli.env"), []byte("COTURN_CLI_PASSWORD="+password+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "partial-install" {
				// An interrupted first package install may have left its default
				// config. The caller must still supply the saved bootstrap value.
				if err := os.WriteFile(filepath.Join(root, "turnserver.conf"), []byte("# default package config\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command("bash", "-s")
			command.Stdin = strings.NewReader(input)
			command.Env = append(os.Environ(), "VIDEO_CLOUD_COTURN_SHARED_SECRET=test-hmac")
			if mode != "missing-password" {
				command.Env = append(command.Env, "COTURN_CLI_PASSWORD=retained-private-marker")
			}
			output, err := command.CombinedOutput()
			if (err != nil) != (mode == "mismatch" || mode == "missing-password") || strings.Contains(string(output), "private-marker") {
				t.Fatal("incorrect pre-write fence or credential leaked", err, string(output))
			}
		})
	}
}
