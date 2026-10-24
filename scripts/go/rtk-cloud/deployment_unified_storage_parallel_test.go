package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type storageParallelMetrics struct {
	Active, Peak, Started, Finished int
	PIDs                            []int
}

type storageParallelFixture struct {
	backend  environmentKubernetesBackend
	plan     *deploymentplan.Plan
	observed map[string]deploymentplan.Object
	volumes  map[string]deploymentplan.Object
	dir      string
}

func newStorageParallelFixture(t *testing.T, environment string, count int, fail bool) storageParallelFixture {
	t.Helper()
	makeIsolatedTestSecretStore(t, environment)
	raw, _ := yaml.Marshal(frozenKubernetesFixture(t))
	backend, err := newEnvironmentKubernetesBackend(string(raw), environment)
	if err != nil {
		t.Fatal(err)
	}
	f := storageParallelFixture{backend: backend, plan: &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: "video-cloud-" + environment}}, observed: map[string]deploymentplan.Object{}, volumes: map[string]deploymentplan.Object{}, dir: t.TempDir()}
	for index := 0; index < count; index++ {
		name, volume, uid := fmt.Sprintf("data-%02d", index), fmt.Sprintf("pv-%02d", index), fmt.Sprintf("claim-%02d", index)
		namespace := "video-cloud-" + environment + "-platform"
		claim := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": name, "namespace": namespace, "uid": uid}, "spec": deploymentplan.Object{"volumeName": volume, "storageClassName": "fast", "accessModes": []any{"ReadWriteOnce"}}, "status": deploymentplan.Object{"phase": "Bound"}}
		pv := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": volume, "uid": "owner-" + volume}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"name": name, "namespace": namespace, "uid": uid}, "csi": deploymentplan.Object{"driver": "fixture", "volumeHandle": "private-volume-" + volume}}, "status": deploymentplan.Object{"phase": "Bound"}}
		id := deploymentplan.ID(claim)
		f.observed[id], f.volumes[deploymentplan.ID(pv)] = claim, pv
		f.plan.Resources = append(f.plan.Resources, deploymentplan.Resource{ID: id, UID: uid, Action: "retain", Desired: deploymentplan.Projection(claim), Baseline: deploymentplan.Projection(claim)})
		if fail && index == 0 {
			pv = deploymentplan.Clone(pv)
			deploymentplan.Map(deploymentplan.Map(pv["spec"])["claimRef"])["uid"] = "private-replacement"
		}
		body, _ := json.Marshal(pv)
		if err := os.WriteFile(filepath.Join(f.dir, volume+".json"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Independent named GETs run as fixture processes. A locked counter measures
	// actual overlap; barriers let failures and cancellation settle out of order.
	stub := `#!/usr/bin/perl
use strict; use warnings;
use Fcntl qw(:flock SEEK_SET); use JSON::PP;
my $dir=$ENV{RTK_STORAGE_PARALLEL_DIR};
my $name;
for (my $i=0; $i<@ARGV-2; $i++) {
 if ($ARGV[$i] eq 'get' && $ARGV[$i+1] eq 'PersistentVolume') { $name=$ARGV[$i+2]; last; }
}
die 'unexpected storage operation' unless defined($name) && $name =~ /^pv-[0-9]+$/;
sub update {
 my ($start)=@_;
 open(my $f, '+>>', "$dir/metrics.json") or die 'metrics unavailable';
 flock($f, LOCK_EX) or die 'metrics lock unavailable'; seek($f, 0, SEEK_SET);
 local $/; my $raw=<$f>;
 my $m=$raw ? decode_json($raw) : {Active=>0, Peak=>0, Started=>0, Finished=>0, PIDs=>[]};
 if ($start) { $m->{Active}++; $m->{Started}++; push(@{$m->{PIDs}}, $$); $m->{Peak}=$m->{Active} if $m->{Active}>$m->{Peak}; }
 else { $m->{Active}--; $m->{Finished}++; }
 seek($f, 0, SEEK_SET); truncate($f, 0); print $f encode_json($m); close($f);
}
update(1);
my $release=($ENV{RTK_STORAGE_PARALLEL_FAILURE} && $name eq 'pv-00') ? 'first-release' : 'release';
while (!-e "$dir/$release") { select(undef, undef, undef, 0.01); }
update(0);
if ($ENV{RTK_STORAGE_PARALLEL_FAILURE} && $name ne 'pv-00') { print STDERR 'Forbidden private-storage-marker'; exit 1; }
open(my $object, '<', "$dir/$name.json") or die 'fixture unavailable';
local $/; print scalar(<$object>);
`
	tool := filepath.Join(f.dir, "kubectl")
	if err := os.WriteFile(tool, []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", tool)
	t.Setenv("RTK_STORAGE_PARALLEL_DIR", f.dir)
	t.Setenv("RTK_STORAGE_PARALLEL_FAILURE", "")
	if fail {
		t.Setenv("RTK_STORAGE_PARALLEL_FAILURE", "1")
	}
	return f
}

func (f storageParallelFixture) metrics(t *testing.T) storageParallelMetrics {
	t.Helper()
	path := filepath.Join(f.dir, "metrics.json")
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return storageParallelMetrics{}
	}
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH); err != nil {
		t.Fatal(err)
	}
	var metrics storageParallelMetrics
	if err := json.NewDecoder(file).Decode(&metrics); errors.Is(err, io.EOF) {
		return storageParallelMetrics{}
	} else if err != nil {
		t.Fatal(err)
	}
	return metrics
}

func (f storageParallelFixture) await(t *testing.T, ready func(storageParallelMetrics) bool) storageParallelMetrics {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		metrics := f.metrics(t)
		if ready(metrics) {
			return metrics
		}
		if time.Now().After(deadline) {
			t.Fatalf("storage workers did not reach the fixture barrier: %+v", metrics)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (f storageParallelFixture) release(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), nil, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestParallelStorageReadsAreBoundedAndDigestOrderedAcrossEnvironments(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			f := newStorageParallelFixture(t, environment, 20, false)
			all := map[string]deploymentplan.Object{}
			for id, object := range f.observed {
				all[id] = object
			}
			for id, object := range f.volumes {
				all[id] = object
			}
			want, err := f.backend.storageBindingsFromInventory(context.Background(), f.plan, all)
			if err != nil || f.metrics(t).Started != 0 {
				t.Fatal("complete fresh inventory performed named GETs", err)
			}
			f.plan.StorageSHA256 = deploymentplan.Digest(want)
			approved := f.plan.SHA256()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- f.backend.CheckStorageBindingsFromInventory(ctx, f.plan, f.observed) }()
			metrics := f.await(t, func(m storageParallelMetrics) bool { return m.Started >= environmentDeploymentCheckConcurrency })
			if metrics.Active != environmentDeploymentCheckConcurrency || metrics.Peak != environmentDeploymentCheckConcurrency || metrics.Started != environmentDeploymentCheckConcurrency {
				t.Fatal("named PV reads did not obey the shared worker bound", metrics)
			}
			f.release(t, "release")
			if err := <-done; err != nil || f.plan.SHA256() != approved {
				t.Fatal("parallel completion changed ordered storage digest or plan", err)
			}
			metrics = f.metrics(t)
			if metrics.Active != 0 || metrics.Started != 20 || metrics.Finished != 20 || metrics.Peak > environmentDeploymentCheckConcurrency {
				t.Fatal("named reads exceeded their bound or workers were not joined", metrics)
			}
		})
	}
}

func TestParallelStorageFailureJoinsActiveReadsAndReturnsFirstPlanError(t *testing.T) {
	f := newStorageParallelFixture(t, "dev", 20, true)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.backend.storageBindingsFromInventory(ctx, f.plan, f.observed); done <- err }()
	f.await(t, func(m storageParallelMetrics) bool { return m.Started == environmentDeploymentCheckConcurrency })
	f.release(t, "release")
	metrics := f.await(t, func(m storageParallelMetrics) bool { return m.Finished == environmentDeploymentCheckConcurrency-1 })
	select {
	case err := <-done:
		t.Fatal("storage returned before joining the first dispatched claim", err)
	default:
	}
	if metrics.Started != environmentDeploymentCheckConcurrency || metrics.Active != 1 {
		t.Fatal("failure dispatched additional claims", metrics)
	}
	f.release(t, "first-release")
	err := <-done
	var blocker *deploymentplan.Blocker
	if !errors.As(err, &blocker) || blocker.ID != "deployment.execution.storage" || blocker.Resource != f.plan.Resources[0].ID || !strings.Contains(blocker.Cause, "claimRef") || strings.Contains(err.Error(), "private-") {
		t.Fatal("completion order replaced the first plan error or exposed private output", err)
	}
	metrics = f.metrics(t)
	if metrics.Active != 0 || metrics.Started != environmentDeploymentCheckConcurrency || metrics.Finished != environmentDeploymentCheckConcurrency {
		t.Fatal("failure left active reads or dispatched the remaining plan", metrics)
	}
}

func TestParallelStorageCancellationReapsEveryActiveRead(t *testing.T) {
	f := newStorageParallelFixture(t, "dev", 20, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.backend.storageBindingsFromInventory(ctx, f.plan, f.observed); done <- err }()
	metrics := f.await(t, func(m storageParallelMetrics) bool { return m.Started == environmentDeploymentCheckConcurrency })
	cancel()
	err := <-done
	var blocker *deploymentplan.Blocker
	if !errors.As(err, &blocker) || blocker.ID != "deployment.access.storage" || blocker.Resource != "PersistentVolume//pv-00" || !strings.Contains(blocker.Cause, "[CHECK_CANCELLED]") || strings.Contains(err.Error(), "private-") {
		t.Fatal("parallel cancellation lost the ordered safe storage blocker", err)
	}
	if f.metrics(t).Started != environmentDeploymentCheckConcurrency {
		t.Fatal("cancellation dispatched more reads")
	}
	for _, pid := range metrics.PIDs {
		if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
			t.Fatal("cancelled storage subprocess was not joined", pid, err)
		}
	}
}

func TestStorageInventoryFallbackAndIdentityChecksPreserveNativeSemantics(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"claim-missing", "volume-missing", "nil-volume", "both-missing", "forbidden", "claim-replaced", "volume-replaced", "volume-handle-changed", "wrong-inventory-id"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				f := newStorageReadFixture(t, environment)
				bindings, err := f.backend.storageBindings(context.Background(), f.plan)
				if err != nil {
					t.Fatal(err)
				}
				f.plan.StorageSHA256 = deploymentplan.Digest(bindings)
				approved := f.plan.SHA256()
				observed := map[string]deploymentplan.Object{f.claimID: deploymentplan.Clone(f.claim), f.volumeID: deploymentplan.Clone(f.volume)}
				wantClaim, wantPV, allowed := 1, 1, false
				switch mode {
				case "claim-missing":
					delete(observed, f.claimID)
					wantClaim, allowed = 2, true
				case "volume-missing", "forbidden":
					delete(observed, f.volumeID)
					wantPV = 2
					allowed = mode == "volume-missing"
					if !allowed {
						t.Setenv("RTK_STORAGE_READ_MODE", "forbidden")
					}
				case "nil-volume":
					observed[f.volumeID] = nil
					wantPV, allowed = 2, true
				case "both-missing":
					observed = nil
					wantClaim, wantPV, allowed = 2, 2, true
				case "claim-replaced":
					deploymentplan.Map(observed[f.claimID]["metadata"])["uid"] = "private-replacement"
				case "volume-replaced":
					deploymentplan.Map(observed[f.volumeID]["metadata"])["uid"] = "private-replacement"
				case "volume-handle-changed":
					deploymentplan.Map(deploymentplan.Map(observed[f.volumeID]["spec"])["csi"])["volumeHandle"] = "private-replacement"
				case "wrong-inventory-id":
					deploymentplan.Map(observed[f.volumeID]["metadata"])["name"] = "private-foreign"
				}
				err = f.backend.CheckStorageBindingsFromInventory(context.Background(), f.plan, observed)
				if (err == nil) != allowed || f.count(t, "claim") != wantClaim || f.count(t, "volume") != wantPV || f.plan.SHA256() != approved {
					t.Fatal("snapshot omitted an original named read or bypassed owner/spec checks", err)
				}
				if mode == "forbidden" || mode == "wrong-inventory-id" {
					code := "KUBE_FORBIDDEN"
					if mode == "wrong-inventory-id" {
						code = "KUBE_INVALID_RESPONSE"
					}
					var blocker *deploymentplan.Blocker
					if !errors.As(err, &blocker) || blocker.ID != "deployment.access.storage" || blocker.Resource != f.volumeID || !strings.Contains(blocker.Cause, "["+code+"]") {
						t.Fatal("snapshot fallback changed safe access diagnostics", err)
					}
				}
				f.assertClean(t, err)
			})
		}
	}
}
