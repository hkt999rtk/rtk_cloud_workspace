package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestNativeStorageProofRejectsReboundLostAndChangedVolumesForEveryEnvironment(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "claim-replaced", "claim-pending", "claim-lost", "volume-replaced", "volume-handle-changed", "claimref-uid", "claimref-namespace", "volume-pending", "volume-terminating", "status-only"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				kube := frozenKubernetesFixture(t)
				makeIsolatedTestSecretStore(t, environment)
				raw, _ := yaml.Marshal(kube)
				backend, err := newEnvironmentKubernetesBackend(string(raw), environment)
				if err != nil {
					t.Fatal(err)
				}
				namespace := "video-cloud-" + environment + "-platform"
				claim := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": "data", "namespace": namespace, "uid": "claim-original", "resourceVersion": "1"}, "spec": deploymentplan.Object{"volumeName": "pv-original", "storageClassName": "fast", "accessModes": []any{"ReadWriteOnce"}, "resources": deploymentplan.Object{"requests": deploymentplan.Object{"storage": "10Gi"}}}, "status": deploymentplan.Object{"phase": "Bound"}}
				pv := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": "pv-original", "uid": "volume-original", "resourceVersion": "1"}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"name": "data", "namespace": namespace, "uid": "claim-original"}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": "original-data"}}, "status": deploymentplan.Object{"phase": "Bound"}}
				plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: "video-cloud-" + environment}, Resources: []deploymentplan.Resource{{ID: "PersistentVolumeClaim/" + namespace + "/data", UID: "claim-original", Action: "retain", Desired: deploymentplan.Projection(claim), Baseline: deploymentplan.Projection(claim)}}}
				directory := t.TempDir()
				claimPath, pvPath := filepath.Join(directory, "claim.json"), filepath.Join(directory, "pv.json")
				publish := func() {
					claimRaw, _ := json.Marshal(claim)
					pvRaw, _ := json.Marshal(pv)
					writeTestFile(t, claimPath, string(claimRaw))
					writeTestFile(t, pvPath, string(pvRaw))
				}
				writeTestFile(t, filepath.Join(directory, "kubectl"), `#!/bin/sh
case "$*" in
 *'get PersistentVolumeClaim data --namespace '*) cat "$RTK_TEST_STORAGE_CLAIM";;
 *'get PersistentVolume pv-original '*) cat "$RTK_TEST_STORAGE_PV";;
 *) exit 1;;
esac
`)
				if err := os.Chmod(filepath.Join(directory, "kubectl"), 0755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("RTK_TEST_STORAGE_CLAIM", claimPath)
				t.Setenv("RTK_TEST_STORAGE_PV", pvPath)
				publish()
				bindings, err := backend.storageBindings(context.Background(), plan)
				if err != nil || len(bindings) != 1 {
					t.Fatal("original native storage proof unavailable", err)
				}
				plan.StorageSHA256 = deploymentplan.Digest(bindings)
				approved := plan.SHA256()
				switch mode {
				case "claim-replaced":
					deploymentplan.Map(claim["metadata"])["uid"] = "claim-new"
				case "claim-pending":
					deploymentplan.Map(claim["status"])["phase"] = "Pending"
				case "claim-lost":
					deploymentplan.Map(claim["status"])["phase"] = "Lost"
				case "volume-replaced":
					deploymentplan.Map(pv["metadata"])["uid"] = "volume-new"
				case "volume-handle-changed":
					deploymentplan.Map(deploymentplan.Map(pv["spec"])["csi"])["volumeHandle"] = "different-data"
				case "claimref-uid":
					deploymentplan.Map(deploymentplan.Map(pv["spec"])["claimRef"])["uid"] = "other-claim"
				case "claimref-namespace":
					deploymentplan.Map(deploymentplan.Map(pv["spec"])["claimRef"])["namespace"] = "another-environment"
				case "volume-pending":
					deploymentplan.Map(pv["status"])["phase"] = "Available"
				case "volume-terminating":
					deploymentplan.Map(pv["metadata"])["deletionTimestamp"] = "now"
				case "status-only":
					deploymentplan.Map(pv["metadata"])["resourceVersion"] = "2"
					deploymentplan.Map(pv["status"])["lastPhaseTransitionTime"] = "later"
				}
				publish()
				err = backend.CheckStorageBindings(context.Background(), plan)
				allowed := mode == "valid" || mode == "status-only"
				if (err == nil) != allowed || plan.SHA256() != approved {
					t.Fatal("incorrect original-volume qualification or mutated plan", err)
				}
				if err != nil && strings.Contains(err.Error(), "original-data") {
					t.Fatal("storage identity leaked through report")
				}
			})
		}
	}
}

type storageReadFixture struct {
	backend           environmentKubernetesBackend
	plan              *deploymentplan.Plan
	claim, volume     deploymentplan.Object
	directory, calls  string
	claimPath, pvPath string
	configPaths, pids string
	claimID, volumeID string
}

func newStorageReadFixture(t *testing.T, environment string) storageReadFixture {
	t.Helper()
	makeIsolatedTestSecretStore(t, environment)
	raw, err := yaml.Marshal(frozenKubernetesFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	backend, err := newEnvironmentKubernetesBackend(string(raw), environment)
	if err != nil {
		t.Fatal(err)
	}
	f := storageReadFixture{backend: backend, directory: t.TempDir()}
	namespace := "video-cloud-" + environment + "-platform"
	f.claimID, f.volumeID = "PersistentVolumeClaim/"+namespace+"/data", "PersistentVolume//pv-original"
	f.claim = deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": "data", "namespace": namespace, "uid": "claim-original"}, "spec": deploymentplan.Object{"volumeName": "pv-original", "storageClassName": "fast", "accessModes": []any{"ReadWriteOnce"}, "resources": deploymentplan.Object{"requests": deploymentplan.Object{"storage": "10Gi"}}}, "status": deploymentplan.Object{"phase": "Bound"}}
	f.volume = deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": "pv-original", "uid": "volume-original"}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"name": "data", "namespace": namespace, "uid": "claim-original"}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": "private-original-data"}}, "status": deploymentplan.Object{"phase": "Bound"}}
	f.plan = &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: "video-cloud-" + environment}, Resources: []deploymentplan.Resource{{ID: f.claimID, UID: "claim-original", Action: "retain", Desired: deploymentplan.Projection(f.claim), Baseline: deploymentplan.Projection(f.claim)}}}
	f.claimPath, f.pvPath = filepath.Join(f.directory, "claim.json"), filepath.Join(f.directory, "pv.json")
	f.calls, f.configPaths, f.pids = filepath.Join(f.directory, "calls"), filepath.Join(f.directory, "config-paths"), filepath.Join(f.directory, "pids")
	f.publish(t)
	stub := `#!/bin/sh
set -eu
umask 077
case "$*" in
 *'get PersistentVolumeClaim data --namespace '*) kind=claim; object="$RTK_STORAGE_READ_CLAIM";;
 *'get PersistentVolume pv-original '*) kind=volume; object="$RTK_STORAGE_READ_VOLUME";;
 *) printf 'unexpected-storage-operation\n' >> "$RTK_STORAGE_READ_CALLS"; exit 1;;
esac
printf '%s\n' "$kind" >> "$RTK_STORAGE_READ_CALLS"
previous=''
for arg in "$@"; do
 if [ "$previous" = --kubeconfig ]; then test -r "$arg"; printf '%s\n' "$arg" >> "$RTK_STORAGE_READ_CONFIG_PATHS"; fi
 previous="$arg"
done
count="$(awk -v kind="$kind" '$0 == kind {count++} END {print count+0}' "$RTK_STORAGE_READ_CALLS")"
if [ "$kind" != "${RTK_STORAGE_READ_TARGET:-volume}" ]; then cat "$object"; exit 0; fi
mode="${RTK_STORAGE_READ_MODE:-valid}"
case "$mode" in
 *-once) if [ "$count" -gt 1 ]; then cat "$object"; exit 0; fi; mode="${mode%-once}";;
esac
case "$mode" in
 valid) cat "$object";;
 unavailable) printf 'Service Unavailable private-storage-marker\n' >&2; exit 1;;
 rate-limit) printf 'TooManyRequests private-storage-marker\n' >&2; exit 1;;
 timeout) printf 'TLS handshake timeout private-storage-marker\n' >&2; exit 1;;
 forbidden) printf 'Error from server (Forbidden): private-storage-marker\n' >&2; exit 1;;
 unauthorized) printf 'Unauthorized private-storage-marker\n' >&2; exit 1;;
 not-found) printf 'Error from server (NotFound): private-storage-marker\n' >&2; exit 1;;
 command-failed) printf 'private-storage-marker\n' >&2; exit 1;;
 malformed) printf '{"credential":"private-storage-marker"';;
 missing) printf ' \n';;
 oversized-stdout|oversized-stderr)
  printf '%s\n' "$$" > "$RTK_STORAGE_READ_PIDS"
  if [ "$mode" = oversized-stdout ]; then head -c 33554433 /dev/zero; else printf 'Service Unavailable private-storage-marker\n' >&2; head -c 33554433 /dev/zero >&2; fi
  sleep 30;;
 hang) sleep 30 & child=$!; printf '%s %s\n' "$$" "$child" > "$RTK_STORAGE_READ_PIDS"; wait "$child";;
 *) exit 1;;
esac
`
	tool := filepath.Join(f.directory, "kubectl")
	if err := os.WriteFile(tool, []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", tool)
	t.Setenv("PATH", f.directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	for key, value := range map[string]string{"RTK_STORAGE_READ_CLAIM": f.claimPath, "RTK_STORAGE_READ_VOLUME": f.pvPath, "RTK_STORAGE_READ_CALLS": f.calls, "RTK_STORAGE_READ_CONFIG_PATHS": f.configPaths, "RTK_STORAGE_READ_PIDS": f.pids, "RTK_STORAGE_READ_MODE": "valid", "RTK_STORAGE_READ_TARGET": "volume"} {
		t.Setenv(key, value)
	}
	return f
}

func (f storageReadFixture) publish(t *testing.T) {
	t.Helper()
	for path, object := range map[string]deploymentplan.Object{f.claimPath: f.claim, f.pvPath: f.volume} {
		raw, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func (f storageReadFixture) count(t *testing.T, kind string) int {
	t.Helper()
	raw, err := os.ReadFile(f.calls)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), kind+"\n")
}

func (f storageReadFixture) assertClean(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		for _, private := range []string{"private-storage-marker", "private-original-data", "private-frozen-token", f.directory, f.backend.Store.ConfigRoot} {
			if strings.Contains(err.Error(), private) {
				t.Fatalf("private storage diagnostic escaped: %v", err)
			}
		}
	}
	paths, readErr := os.ReadFile(f.configPaths)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	for _, path := range strings.Fields(string(paths)) {
		if !strings.HasPrefix(path, filepath.Join(f.backend.Store.Root, "deployment", "tmp")+string(os.PathSeparator)) {
			t.Fatalf("storage read used another environment's credentials: %s", path)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("storage read left a private kubeconfig: %s %v", path, err)
		}
	}
	entries, readErr := os.ReadDir(filepath.Join(f.backend.Store.Root, "deployment", "tmp"))
	if readErr != nil && !os.IsNotExist(readErr) || len(entries) != 0 {
		t.Fatalf("storage read left temporary credentials: entries=%v err=%v", entries, readErr)
	}
}

func (f storageReadFixture) assertReaped(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile(f.pids)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range strings.Fields(string(raw)) {
		pid, err := strconv.Atoi(value)
		if err != nil || pid <= 0 {
			t.Fatalf("invalid fixture process: %q", value)
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			err = syscall.Kill(pid, 0)
			if err == syscall.ESRCH {
				break
			}
			if time.Now().After(deadline) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Fatalf("storage subprocess survived its bound: pid=%d err=%v", pid, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestStorageReadsClassifyRetryAndPreserveOwnersAcrossEnvironments(t *testing.T) {
	cases := []struct {
		mode, code string
		calls      int
	}{
		{"unavailable-once", "", 2}, {"rate-limit-once", "", 2}, {"timeout-once", "", 2},
		{"unavailable", "KUBE_UNAVAILABLE", 2}, {"rate-limit", "KUBE_RATE_LIMITED", 2}, {"timeout", "KUBE_TIMEOUT", 2},
		{"forbidden", "KUBE_FORBIDDEN", 1}, {"unauthorized", "KUBE_UNAUTHORIZED", 1}, {"not-found", "KUBE_NOT_FOUND", 1}, {"command-failed", "KUBE_COMMAND_FAILED", 1},
		{"malformed", "KUBE_INVALID_RESPONSE", 1}, {"mismatched-id", "KUBE_INVALID_RESPONSE", 1},
		{"oversized-stdout", "KUBE_INVALID_RESPONSE", 1}, {"oversized-stderr", "KUBE_INVALID_RESPONSE", 1}, {"missing", "missing", 1},
	}
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, target := range []string{"claim", "volume"} {
			for _, tc := range cases {
				t.Run(environment+"/"+target+"/"+tc.mode, func(t *testing.T) {
					f := newStorageReadFixture(t, environment)
					t.Setenv("RTK_STORAGE_READ_TARGET", target)
					t.Setenv("RTK_STORAGE_READ_MODE", tc.mode)
					id := f.volumeID
					if target == "claim" {
						id = f.claimID
					}
					if tc.mode == "mismatched-id" {
						t.Setenv("RTK_STORAGE_READ_MODE", "valid")
						object := f.volume
						if target == "claim" {
							object = f.claim
						}
						deploymentplan.Map(object["metadata"])["name"] = "foreign-resource"
						f.publish(t)
					}
					before := f.plan.SHA256()
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					bindings, err := f.backend.storageBindings(ctx, f.plan)
					if tc.code == "" {
						if err != nil || len(bindings) != 1 {
							t.Fatalf("transient GET did not recover original storage: bindings=%v err=%v", bindings, err)
						}
					} else {
						var blocker *deploymentplan.Blocker
						if !errors.As(err, &blocker) {
							t.Fatalf("storage failure lacked a stable blocker: %v", err)
						}
						if tc.code == "missing" {
							if blocker.ID != "deployment.execution.storage" || blocker.Resource != f.claimID {
								t.Fatalf("successful empty GET became an access failure: %+v", blocker)
							}
						} else if blocker.ID != "deployment.access.storage" || blocker.Resource != id || !strings.Contains(blocker.Cause, "["+tc.code+"]") || strings.Contains(blocker.Next, "migration/recovery") {
							t.Fatalf("GET failure was mistaken for damaged storage: %+v", blocker)
						}
					}
					if f.count(t, target) != tc.calls {
						t.Fatalf("incorrect read retry count: %d want %d", f.count(t, target), tc.calls)
					}
					if target == "claim" && tc.code != "" && f.count(t, "volume") != 0 {
						t.Fatal("failed claim qualification proceeded to its volume")
					}
					if f.plan.SHA256() != before {
						t.Fatal("storage read changed the frozen plan")
					}
					f.assertClean(t, err)
					if strings.HasPrefix(tc.mode, "oversized-") {
						f.assertReaped(t)
					}
				})
			}
		}
	}
}

func TestStorageRetryDoesNotAcceptChangedOwnerState(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"claim-pending", "claim-replaced", "volume-pending", "volume-terminating", "claimref-changed"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				f := newStorageReadFixture(t, environment)
				target := "volume"
				switch mode {
				case "claim-pending":
					target = "claim"
					deploymentplan.Map(f.claim["status"])["phase"] = "Pending"
				case "claim-replaced":
					target = "claim"
					deploymentplan.Map(f.claim["metadata"])["uid"] = "another-owner"
				case "volume-pending":
					deploymentplan.Map(f.volume["status"])["phase"] = "Available"
				case "volume-terminating":
					deploymentplan.Map(f.volume["metadata"])["deletionTimestamp"] = "now"
				case "claimref-changed":
					deploymentplan.Map(deploymentplan.Map(f.volume["spec"])["claimRef"])["uid"] = "another-owner"
				}
				f.publish(t)
				t.Setenv("RTK_STORAGE_READ_TARGET", target)
				t.Setenv("RTK_STORAGE_READ_MODE", "unavailable-once")
				before := f.plan.SHA256()
				_, err := f.backend.storageBindings(context.Background(), f.plan)
				var blocker *deploymentplan.Blocker
				if !errors.As(err, &blocker) || blocker.ID != "deployment.execution.storage" || f.count(t, target) != 2 || f.plan.SHA256() != before {
					t.Fatalf("successful retry accepted an incompatible owner or mutated plan: %v", err)
				}
				f.assertClean(t, err)
			})
		}
	}
}

func TestStorageFenceUsesFreshVolumeEvidence(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			f := newStorageReadFixture(t, environment)
			bindings, err := f.backend.storageBindings(context.Background(), f.plan)
			if err != nil {
				t.Fatal(err)
			}
			f.plan.StorageSHA256 = deploymentplan.Digest(bindings)
			before := f.plan.SHA256()
			deploymentplan.Map(deploymentplan.Map(f.volume["spec"])["csi"])["volumeHandle"] = "private-replaced-data"
			f.publish(t)
			err = f.backend.CheckStorageBindings(context.Background(), f.plan)
			var blocker *deploymentplan.Blocker
			if !errors.As(err, &blocker) || blocker.ID != "deployment.execution.storage" || f.count(t, "claim") != 2 || f.count(t, "volume") != 2 || f.plan.SHA256() != before {
				t.Fatalf("storage fence reused stale evidence or changed plan: %v", err)
			}
			f.assertClean(t, err)
		})
	}
}

func TestStorageReadsRespectParentCancellationAndDeadline(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"pre-cancelled", "cancelled", "deadline", "missing-tool"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				f := newStorageReadFixture(t, environment)
				before := f.plan.SHA256()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				code, calls := "CHECK_CANCELLED", 0
				switch mode {
				case "pre-cancelled":
					cancel()
				case "missing-tool":
					code = "KUBE_TOOL_MISSING"
					t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(f.directory, "missing-kubectl"))
				case "cancelled":
					calls = 1
					t.Setenv("RTK_STORAGE_READ_TARGET", "claim")
					t.Setenv("RTK_STORAGE_READ_MODE", "hang")
				case "deadline":
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 300*time.Millisecond)
					defer cancel()
					code, calls = "KUBE_TIMEOUT", 1
					t.Setenv("RTK_STORAGE_READ_TARGET", "claim")
					t.Setenv("RTK_STORAGE_READ_MODE", "hang")
				}
				if mode == "cancelled" {
					go func() {
						deadline := time.Now().Add(3 * time.Second)
						for time.Now().Before(deadline) {
							if _, err := os.Stat(f.pids); err == nil {
								cancel()
								return
							}
							time.Sleep(5 * time.Millisecond)
						}
						cancel()
					}()
				}
				_, err := f.backend.storageBindings(ctx, f.plan)
				var blocker *deploymentplan.Blocker
				if !errors.As(err, &blocker) || blocker.ID != "deployment.access.storage" || blocker.Resource != f.claimID || !strings.Contains(blocker.Cause, "["+code+"]") || f.count(t, "claim") != calls || f.count(t, "volume") != 0 || f.plan.SHA256() != before {
					t.Fatalf("parent cancellation/deadline was not preserved: %v", err)
				}
				f.assertClean(t, err)
				if calls == 1 {
					f.assertReaped(t)
				}
			})
		}
	}
}
