package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// ciRolloutNativeHooks uses the same Kubernetes backend and execution Lease as
// the formal environment deployment. The dedicated identity only needs the
// RBAC generated from its selected target inventory.
func ciRolloutNativeHooks(backend environmentKubernetesBackend, plan *deploymentplan.Plan, journal *ciRolloutJournal, run environmentReleaseCommand) ciRolloutExecutionHooks {
	return ciRolloutExecutionHooks{
		Acquire: func(ctx context.Context) (func() error, error) {
			leaseID := "Lease/" + plan.Stack + "-video-cloud/rtk-deployment-execution"
			return waitForCIRolloutLease(ctx, 2*time.Second, func(ctx context.Context) (bool, error) {
				object, err := backend.Read(ctx, leaseID)
				return object != nil, err
			}, func(ctx context.Context) (func() error, error) {
				lease, err := backend.AcquireExecution(ctx, plan)
				if err != nil {
					return nil, err
				}
				return func() error {
					cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					return lease.Release(cleanup)
				}, nil
			})
		},
		Head: func(ctx context.Context, repo, branch string) (string, error) {
			if run == nil || !ciRepositoryName.MatchString(repo) || !validCIBranchName(branch) {
				return "", errors.New("CI rollout: branch reader unavailable")
			}
			raw, err := run(ctx, "gh", "api", "repos/"+repo+"/commits/"+url.PathEscape(branch))
			if err != nil {
				return "", err
			}
			var result struct {
				SHA string `json:"sha"`
			}
			if json.Unmarshal(raw, &result) != nil || !deploymentUpgradeSHA.MatchString(result.SHA) {
				return "", errors.New("CI rollout: invalid branch head")
			}
			return result.SHA, nil
		},
		Read: func(ctx context.Context, target ciRolloutTarget) ([]byte, error) {
			object, err := backend.Read(ctx, "Deployment/"+target.Namespace+"/"+target.Name)
			if err != nil {
				return nil, err
			}
			if object == nil {
				return nil, errors.New("CI rollout: selected deployment is missing")
			}
			return json.Marshal(object)
		},
		Patch: func(ctx context.Context, target ciRolloutTarget, patch []byte, dry bool) error {
			args := []string{"patch", "deployment", target.Name, "--namespace", target.Namespace, "--type=json", "--patch-file=/dev/stdin", "-o", "json"}
			if dry {
				args = append(args, "--dry-run=server")
			}
			_, err := backend.command(ctx, patch, args...)
			return err
		},
		Wait: func(ctx context.Context, target ciRolloutTarget) error {
			return backend.Wait(ctx, deploymentplan.Resource{ID: "Deployment/" + target.Namespace + "/" + target.Name, Phase: "workload", Action: "update"})
		},
		Commit: func(ctx context.Context) (func(context.Context) error, error) {
			return ciRolloutCommitState(ctx, backend, plan, journal)
		},
		Record: func(ctx context.Context, status string, target ciRolloutTarget, previous string) error {
			if journal == nil {
				return errors.New("CI rollout: journal unavailable")
			}
			return journal.record(ctx, status, target, previous)
		},
	}
}
