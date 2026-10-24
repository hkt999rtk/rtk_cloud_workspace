package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestRenderedOpenBaoPIDSurvivesOneKubernetesExpansion(t *testing.T) {
	for _, oldControl := range []bool{false, true} {
		t.Run(map[bool]string{false: "fixed-producer", true: "historical-control"}[oldControl], func(t *testing.T) {
			dir := t.TempDir()
			private := filepath.Join(dir, "private")
			if err := os.Mkdir(private, 0700); err != nil {
				t.Fatal(err)
			}
			// exec retains the launched PID across both the fixture script and
			// its long-running provider stand-in; no real provider is contacted.
			fixture := []byte("#!/bin/sh\nset -eu\ntest \"$1\" = server\ncase \"$2\" in -config=*) ;; *) exit 1 ;; esac\nprintf '%s\\n' \"$$\" > \"$PID_PRODUCER_FIXTURE_READY\"\nexec sleep 30\n")
			if err := os.WriteFile(filepath.Join(dir, "bao"), fixture, 0700); err != nil {
				t.Fatal(err)
			}
			env, release, live := existingFileOpenBaoFixture("dev", true)
			bindings, err := observeEnvironmentBindings("dev", release, live)
			if err != nil {
				t.Fatal(err)
			}
			bindings.Pods[0].OpenBao.RuntimeDir = private
			bindings.Pods[0].OpenBao.PIDFile = filepath.Join(private, "openbao.pid")
			object, err := renderEnvironmentOpenBao(env, bindings.Pods[0])
			if err != nil {
				t.Fatal(err)
			}
			main := managedNamedField(environmentPKIPod(object)["containers"], "openbao")
			command := certIssuerObjectList(main["command"])
			if len(command) != 3 || command[0] != "/bin/sh" || command[1] != "-ec" {
				t.Fatal("renderer lost its exact native provider launcher")
			}
			script := deploymentplan.String(command[2])
			if oldControl {
				script = strings.ReplaceAll(script, "$$$$", "$$")
			}
			// Kubelet's expansion.Expand consumes each escaped dollar pair once.
			// This rendered launcher has no $(VAR) expressions, so that is its
			// entire Kubernetes expansion step (Expand/tryReadVariableName):
			// https://github.com/kubernetes/kubernetes/blob/master/third_party/forked/golang/expansion/expand.go
			if strings.Contains(script, "$(") {
				t.Fatal("fixture requires Kubernetes variable mapping, not just dollar escaping")
			}
			script = strings.ReplaceAll(script, "$$", "$")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-ec", script)
			providerMarker := filepath.Join(private, "provider.started")
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "PID_PRODUCER_FIXTURE_READY="+providerMarker)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			if cmd.Process.Pid <= 1 {
				t.Fatal("provider fixture has no usable owned process PID")
			}
			marker := bindings.Pods[0].OpenBao.PIDFile
			var raw []byte
			var provider []byte
			for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
				raw, err = os.ReadFile(marker)
				provider, _ = os.ReadFile(providerMarker)
				if err == nil && len(raw) > 0 && len(provider) > 0 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			want := strconv.Itoa(cmd.Process.Pid) + "\n"
			if oldControl {
				want = "$\n"
			}
			info, statErr := os.Stat(marker)
			if err != nil || statErr != nil || string(raw) != want || string(provider) != strconv.Itoa(cmd.Process.Pid)+"\n" || info.Mode().Perm() != 0600 {
				t.Fatal("Kubernetes-expanded provider launcher wrote the wrong PID or permissions")
			}
		})
	}
}
