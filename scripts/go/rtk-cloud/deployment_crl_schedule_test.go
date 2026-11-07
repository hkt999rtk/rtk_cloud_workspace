package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCRLScheduleRunnerPreservesEnvironmentArgumentsAndFailure(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			root := t.TempDir()
			workspace := filepath.Join(root, "workspace ' $dollar %percent")
			if err := os.MkdirAll(filepath.Join(workspace, "scripts"), 0700); err != nil {
				t.Fatal(err)
			}
			capture := filepath.Join(root, "arguments")
			facade := "#!/bin/bash\nprintf '%s\\n' \"$@\" > " + shellSingleQuote(capture) + "\nexit 23\n"
			if err := os.WriteFile(filepath.Join(workspace, "scripts/renew-environment-crls.sh"), []byte(facade), 0600); err != nil {
				t.Fatal(err)
			}
			store, err := newSecretStore(filepath.Join(root, "config"), environment)
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(store.Root, "deployment/crl/schedule")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			o := crlScheduleOptions{environment: environment, workspace: workspace, confirm: "stack-" + environment, image: "registry/helper@sha256:" + strings.Repeat("a", 64), helperRevision: strings.Repeat("c", 40)}
			runner := filepath.Join(dir, "run.sh")
			if err := os.WriteFile(runner, []byte(renderCRLScheduleRunner(o, store, os.Getenv("PATH"))), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/bash", runner)
			if err := cmd.Run(); err == nil {
				t.Fatal("failed renewal reported success")
			}
			got, _ := os.ReadFile(capture)
			want := "--environment\n" + environment + "\n--execute\n--recover\n--confirm\nstack-" + environment + "\n--image\n" + o.image + "\n--helper-revision\n" + o.helperRevision + "\n"
			if string(got) != want {
				t.Fatalf("arguments %q, want %q", got, want)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "last-result.json"))
			if err != nil {
				t.Fatal(err)
			}
			var result crlScheduleResult
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			if result.ExitCode != 23 || result.FinishedAt.IsZero() {
				t.Fatalf("lost failure: %+v", result)
			}
			info, _ := os.Stat(filepath.Join(dir, "last-result.json"))
			if info.Mode().Perm() != 0600 {
				t.Fatal("result is not private")
			}
			if _, err := os.Stat(filepath.Join(dir, "last-success.json")); !os.IsNotExist(err) {
				t.Fatal("failure advanced last success")
			}
			if err := os.WriteFile(filepath.Join(workspace, "scripts/renew-environment-crls.sh"), []byte("exit 0\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := exec.Command("/bin/bash", runner).Run(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(dir, "last-success.json")); err != nil {
				t.Fatal("successful renewal was not recorded", err)
			}
			if err := os.RemoveAll(workspace); err != nil {
				t.Fatal(err)
			}
			if err := exec.Command("/bin/bash", runner).Run(); err == nil {
				t.Fatal("missing checkout reported success")
			}
			failed, err := os.ReadFile(filepath.Join(dir, "last-result.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(failed, &result); err != nil {
				t.Fatal(err)
			}
			if result.ExitCode == 0 {
				t.Fatal("missing checkout failure not recorded")
			}
		})
	}
}

func TestCRLScheduleNativeFilesBindOneEnvironmentAndEscapePaths(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home ' & % user")
			store, _ := newSecretStore(filepath.Join(home, ".config/rtk_cloud"), "staging")
			files, err := renderCRLScheduleNative(platform, home, store)
			if err != nil {
				t.Fatal(err)
			}
			if len(files) == 0 {
				t.Fatal("no scheduler files")
			}
			for path, data := range files {
				if strings.Contains(data, "PRIVATE KEY") || strings.Contains(data, "--environment prod") {
					t.Fatal("custody or environment leak")
				}
				if !strings.HasPrefix(path, home+"/") {
					t.Fatal(path)
				}
			}
			if platform == "darwin" {
				data := files[filepath.Join(home, "Library/LaunchAgents/com.rtk-cloud.crl.staging.plist")]
				if !strings.Contains(data, "&amp;") || !strings.Contains(data, "<integer>3600</integer>") {
					t.Fatal(data)
				}
			} else {
				data := files[filepath.Join(home, ".config/systemd/user/rtk-cloud-crl-staging.service")]
				if !strings.Contains(data, "%%") {
					t.Fatal("systemd percent expansion not escaped")
				}
			}
		})
	}
}

func TestCRLScheduleStatusReportsStaleAndFailedRuns(t *testing.T) {
	store, _ := newSecretStore(t.TempDir(), "prod")
	dir := filepath.Join(store.Root, "deployment/crl/schedule")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	result := crlScheduleResult{ExitCode: 7, FinishedAt: time.Now().UTC().Add(-3 * time.Hour)}
	raw, _ := json.Marshal(result)
	if err := os.WriteFile(filepath.Join(dir, "last-result.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := printCRLScheduleStatus(&out, store, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"installed=true", "stale=true", "last_exit_code=7", "last-success.json"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s: %s", want, out.String())
		}
	}
}

func TestCRLScheduleInstallRemoveUsesNativeManagerAndPrivateStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", filepath.Join(home, ".config/rtk_cloud"))
	workspace := writeDeploymentFixture(t, "staging", "lke")
	writeTestFile(t, filepath.Join(workspace, "scripts/renew-environment-crls.sh"), "#!/bin/bash\nexit 0\n")
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(home, "manager-arguments")
	for _, name := range []string{"launchctl", "systemctl", "go", "kubectl", "docker", "git", "gh"} {
		body := "#!/bin/bash\nprintf '%s\\n' \"$@\" >> " + shellSingleQuote(capture) + "\n"
		if name == "git" {
			body = "#!/bin/bash\nprintf '" + strings.Repeat("c", 40) + "\\n'\n"
		}
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	args := []string{"install", "--environment", "staging", "--workspace", workspace, "--confirm", "video-cloud-staging", "--image", "registry/helper@sha256:" + strings.Repeat("b", 64)}
	store, _ := newSecretStore("", "staging")
	logPath := filepath.Join(store.Root, "deployment/crl/schedule/stdout.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(home, "unrelated")
	if err := os.WriteFile(outside, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, logPath); err != nil {
		t.Fatal(err)
	}
	if err := runEnvironmentCRLSchedule(args); err == nil {
		t.Fatal("accepted scheduler log symlink")
	}
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	if err := runEnvironmentCRLSchedule(args); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"run.sh", "installation.json", "stdout.log", "stderr.log"} {
		path := filepath.Join(store.Root, "deployment/crl/schedule", name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("nonprivate %s", path)
		}
	}
	if err := runEnvironmentCRLSchedule(args); err == nil {
		t.Fatal("duplicate install silently replaced live schedule")
	}
	if err := runEnvironmentCRLSchedule([]string{"remove", "--environment", "staging"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, "deployment/crl/schedule/installation.json")); err != nil {
		t.Fatal("remove discarded evidence")
	}
	data, _ := os.ReadFile(capture)
	if !strings.Contains(string(data), "staging") || strings.Contains(string(data), "prod") {
		t.Fatal(string(data))
	}
	if _, err := os.Stat(filepath.Join(home, ".config/rtk_cloud/prod")); !os.IsNotExist(err) {
		t.Fatal("created other environment")
	}
}

func TestCRLScheduleStatusShowsEngineExpiryAndFailure(t *testing.T) {
	store, _ := newSecretStore(t.TempDir(), "staging")
	if err := store.write("deployment/crl-maintenance/status.json", []byte(`{"environment":"staging","next_expiry":"2026-11-10T01:00:00Z","last_success":"2026-11-07T01:00:00Z","failure":"consumer ACK missing"}`), true); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := printCRLScheduleStatus(&out, store, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"2026-11-10T01:00:00Z", "consumer ACK missing"} {
		if !strings.Contains(out.String(), s) {
			t.Fatalf("engine status hidden: %s", out.String())
		}
	}
}

func TestCRLScheduleRejectsImplicitEnvironmentAndMissingDependencies(t *testing.T) {
	for _, args := range [][]string{{"status"}, {"status", "--environment", "../prod"}, {"install", "--environment", "all"}} {
		if err := runEnvironmentCRLSchedule(args); err == nil {
			t.Fatalf("accepted unsafe target: %v", args)
		}
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := crlScheduleExecutablePath(); err == nil || !strings.Contains(err.Error(), "go") {
		t.Fatalf("missing tool not identified: %v", err)
	}
}
