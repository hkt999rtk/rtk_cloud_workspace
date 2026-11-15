package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type ciRolloutExecutionHooks struct {
	Acquire func(context.Context) (func() error, error)
	Head    func(context.Context, string, string) (string, error)
	Read    func(context.Context, ciRolloutTarget) ([]byte, error)
	Patch   func(context.Context, ciRolloutTarget, []byte, bool) error
	Wait    func(context.Context, ciRolloutTarget) error
	Commit  func(context.Context) (func(context.Context) error, error)
	Record  func(context.Context, string, ciRolloutTarget, string) error
}

type ciRolloutChangedTarget struct {
	target   ciRolloutTarget
	previous string
}

// executeCIRollout applies only environment-plan targets under the shared
// execution lease. A failed rollout restores changed images using the same UID
// and resourceVersion guarded patch, and records its progress through Record.
func executeCIRollout(ctx context.Context, policy ciBranchSelection, repo, branch string, plan *deploymentplan.Plan, image environmentReleaseImage, hooks ciRolloutExecutionHooks) (result error) {
	if !policy.matches(repo, branch) || !deploymentUpgradeSHA.MatchString(image.SourceRevision) || image.Publication == nil || image.Publication.Repository != repo || image.Publication.RunID <= 0 || image.Publication.Attempt <= 0 || hooks.Acquire == nil || hooks.Head == nil || hooks.Read == nil || hooks.Patch == nil || hooks.Wait == nil || hooks.Commit == nil || hooks.Record == nil {
		return errors.New("CI rollout: selected release, branch and execution dependencies required")
	}
	repository := environmentImageRepository(image.Reference)
	if !immutableEnvironmentImage.MatchString(image.Reference) || len(repository) <= len("ghcr.io/"+repo+"/") || repository[:len("ghcr.io/"+repo+"/")] != "ghcr.io/"+repo+"/" {
		return errors.New("CI rollout: image is not from selected repository")
	}
	targets, err := ciRolloutTargetsFromPlan(plan, repository)
	if err != nil {
		return err
	}
	release, err := hooks.Acquire(ctx)
	if err != nil {
		return err
	}
	if release == nil {
		return errors.New("CI rollout: missing execution lease release")
	}
	defer func() {
		if err := release(); err != nil {
			result = errors.Join(result, errors.New("CI rollout: execution lease release failed"))
		}
	}()
	if err = hooks.Record(ctx, "started", ciRolloutTarget{}, ""); err != nil {
		return err
	}
	changed := []ciRolloutChangedTarget{}
	var undoState func(context.Context) error
	defer func() {
		if result == nil {
			return
		}
		recovery, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if undoState != nil {
			if err := undoState(recovery); err != nil {
				result = errors.Join(result, errors.New("CI rollout: completed version state recovery failed"))
			}
		}
		for i := len(changed) - 1; i >= 0; i-- {
			item := changed[i]
			raw, err := hooks.Read(recovery, item.target)
			if err != nil {
				result = errors.Join(result, errors.New("CI rollout: cannot inspect prior image for recovery"))
				continue
			}
			patch, err := ciRolloutImagePatch(raw, item.target.Namespace, item.target.Name, item.target.UID, item.target.Container, item.previous)
			if err != nil {
				result = errors.Join(result, errors.New("CI rollout: prior image recovery cannot be prepared"))
				continue
			}
			var operations []struct {
				Value string `json:"value"`
			}
			if json.Unmarshal(patch, &operations) != nil || len(operations) != 4 {
				result = errors.Join(result, errors.New("CI rollout: invalid recovery patch"))
				continue
			}
			if operations[2].Value == item.previous {
				continue
			}
			if operations[2].Value != image.Reference {
				result = errors.Join(result, errors.New("CI rollout: workload changed during recovery"))
				continue
			}
			if err = hooks.Patch(recovery, item.target, patch, true); err == nil {
				err = hooks.Patch(recovery, item.target, patch, false)
			}
			if err == nil {
				err = hooks.Wait(recovery, item.target)
			}
			if err != nil {
				result = errors.Join(result, errors.New("CI rollout: prior image recovery failed"))
			}
		}
		if err := hooks.Record(recovery, "failed", ciRolloutTarget{}, ""); err != nil {
			result = errors.Join(result, errors.New("CI rollout: failure could not be recorded"))
		}
	}()
	for _, target := range targets {
		raw, err := hooks.Read(ctx, target)
		if err != nil {
			return err
		}
		patch, err := ciRolloutImagePatch(raw, target.Namespace, target.Name, target.UID, target.Container, image.Reference)
		if err != nil {
			return err
		}
		var operations []struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(patch, &operations) != nil || len(operations) != 4 || !immutableEnvironmentImage.MatchString(operations[2].Value) {
			return errors.New("CI rollout: existing immutable image is required for recovery")
		}
		if operations[2].Value == image.Reference {
			continue
		}
		if err = hooks.Patch(ctx, target, patch, true); err != nil {
			return err
		}
		head, err := hooks.Head(ctx, repo, branch)
		if err != nil || head != image.SourceRevision {
			return errors.New("CI rollout: selected branch advanced before deployment")
		}
		if err = hooks.Record(ctx, "applying", target, operations[2].Value); err != nil {
			return err
		}
		changed = append(changed, ciRolloutChangedTarget{target: target, previous: operations[2].Value})
		if err = hooks.Patch(ctx, target, patch, false); err != nil {
			return fmt.Errorf("CI rollout: image update failed: %w", err)
		}
		if err = hooks.Wait(ctx, target); err != nil {
			return fmt.Errorf("CI rollout: workload did not become ready: %w", err)
		}
	}
	undoState, err = hooks.Commit(ctx)
	if err != nil {
		return err
	}
	return hooks.Record(ctx, "completed", ciRolloutTarget{}, "")
}
