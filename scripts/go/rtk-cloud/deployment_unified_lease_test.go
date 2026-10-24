package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

type executionLeaseAPIFixture struct {
	object                                 deploymentplan.Object
	fail, mutateAdmission, replaceOnDelete bool
	dry, writes, deletes                   int
}

func TestExecutionPreflightChecksReleasePermissionForOnlyItsNamedLease(t *testing.T) {
	for _, mode := range []string{"yes", "no", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "kubectl")
			script := `#!/bin/sh
shift 3
case "$1" in
get) exit 0 ;;
create) cat ;;
auth)
 test "$2" = can-i && test "$3" = delete && test "$4" = leases.coordination.k8s.io/rtk-deployment-execution && test "$5" = --namespace && test "$6" = video-cloud-dev-video-cloud || exit 2
 if test "$RTK_TEST_LEASE_PERMISSION" = unavailable; then exit 1; fi
 printf '%s\n' "$RTK_TEST_LEASE_PERMISSION"
 ;;
*) exit 2 ;;
esac
`
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_TEST_LEASE_PERMISSION", mode)
			store := makeIsolatedTestSecretStore(t, "dev")
			b := environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("private-frozen-fixture")}
			p := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev"}}
			err := b.CheckExecutionPrerequisites(context.Background(), p, t.TempDir())
			if (err == nil) != (mode == "yes") {
				t.Fatal("named lease release permission was not qualified", err)
			}
			if err != nil && strings.Contains(err.Error(), "private-frozen-fixture") {
				t.Fatal("private identity leaked through permission error")
			}
		})
	}
}

func TestExecutionLeasePreflightIsReadOnlyAndRejectsInterruptedOwners(t *testing.T) {
	for _, env := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"available", "cluster-owned", "local-owned", "local-symlink", "unreadable", "mutated-admission"} {
			t.Run(env+"/"+mode, func(t *testing.T) {
				plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: env, Stack: "video-cloud-" + env}}
				api := &executionLeaseAPIFixture{fail: mode == "unreadable", mutateAdmission: mode == "mutated-admission"}
				root := t.TempDir()
				if mode == "cluster-owned" {
					api.object = environmentExecutionLeaseBody(plan, "interrupted-owner")
				}
				if mode == "local-owned" || mode == "local-symlink" {
					if err := os.Mkdir(filepath.Join(root, "deployment"), 0700); err != nil {
						t.Fatal(err)
					}
					var err error
					if mode == "local-symlink" {
						err = os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "deployment", ".execution-active"))
					} else {
						err = os.Mkdir(filepath.Join(root, "deployment", ".execution-active"), 0700)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				err := checkEnvironmentExecutionLease(context.Background(), api, plan, root)
				if (err == nil) != (mode == "available") || api.writes != 0 || api.deletes != 0 {
					t.Fatal("lease qualification persisted, stole or accepted an unverified owner", err)
				}
				if mode == "available" && api.dry != 1 {
					t.Fatal("lease permission/admission was not exercised")
				}
				if err != nil {
					var blocker *deploymentplan.Blocker
					if !errors.As(err, &blocker) || blocker.ID != "deployment.execution.lease" || blocker.Next == "" {
						t.Fatal("missing actionable lease blocker", err)
					}
				}
			})
		}
	}
}

func (f *executionLeaseAPIFixture) Read(context.Context, string) (deploymentplan.Object, error) {
	if f.fail {
		return nil, errors.New("unavailable")
	}
	return deploymentplan.Clone(f.object), nil
}
func (f *executionLeaseAPIFixture) Write(_ context.Context, action string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	if f.fail || action != "create" {
		return nil, errors.New("not authorized")
	}
	if dry {
		f.dry++
		actual := deploymentplan.Clone(body)
		if f.mutateAdmission {
			deploymentplan.Map(actual["spec"])["holderIdentity"] = "other-controller"
		}
		return actual, nil
	}
	if f.object != nil {
		return nil, errors.New("conflict")
	}
	f.writes++
	f.object = deploymentplan.Clone(body)
	m := deploymentplan.Map(f.object["metadata"])
	m["uid"], m["resourceVersion"] = "lease-owner", "1"
	return deploymentplan.Clone(f.object), nil
}
func (f *executionLeaseAPIFixture) DeleteExecutionLease(_ context.Context, id, uid, version string) error {
	if f.replaceOnDelete {
		deploymentplan.Map(f.object["metadata"])["uid"] = "concurrent-owner"
	}
	if f.fail || f.object == nil || deploymentplan.ID(f.object) != id || deploymentplan.Map(f.object["metadata"])["uid"] != uid || deploymentplan.Map(f.object["metadata"])["resourceVersion"] != version {
		return errors.New("delete precondition conflict")
	}
	f.deletes++
	f.object = nil
	return nil
}

func TestClusterExecutionLeaseNeverStealsOrDeletesAConcurrentOwner(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		plan := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: deploymentplan.Inputs{Environment: environment, Stack: "video-cloud-" + environment}}
		api := &executionLeaseAPIFixture{}
		lease, err := acquireEnvironmentExecutionLease(context.Background(), api, plan)
		if err != nil || api.dry != 1 || api.writes != 1 {
			t.Fatal("exact lease acquisition failed", err)
		}
		if api.object["apiVersion"] != "coordination.k8s.io/v1" || deploymentplan.Map(api.object["metadata"])["namespace"] != plan.Stack+"-video-cloud" {
			t.Fatal("lease escaped the environment")
		}
		if _, err := acquireEnvironmentExecutionLease(context.Background(), api, plan); err == nil || api.writes != 1 {
			t.Fatal("existing lease was stolen")
		}
		if err := lease.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
		api.replaceOnDelete = true
		if err := lease.Release(context.Background()); err == nil || api.deletes != 0 || api.object == nil {
			t.Fatal("lease release deleted a concurrent owner")
		}
		if err := lease.Check(context.Background()); err == nil {
			t.Fatal("replacement owner accepted")
		}
	}
}

func TestClusterExecutionLeaseRequiresUnchangedAdmissionAndConfirmedRelease(t *testing.T) {
	plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev"}}
	for _, mode := range []string{"unavailable", "mutated-admission", "success"} {
		api := &executionLeaseAPIFixture{fail: mode == "unavailable", mutateAdmission: mode == "mutated-admission"}
		lease, err := acquireEnvironmentExecutionLease(context.Background(), api, plan)
		if mode != "success" {
			if err == nil || api.writes != 0 {
				t.Fatal("unverified lease was acquired")
			}
			continue
		}
		if err != nil || lease.Release(context.Background()) != nil || api.object != nil || api.deletes != 1 {
			t.Fatal("exact lease release was not confirmed")
		}
	}
}
