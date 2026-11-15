package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type ciRolloutBatchHooks struct {
	RecoveryTimeout time.Duration
	Acquire         func(context.Context) (func() error, error)
	Fence           func(context.Context) error
	Head            func(context.Context, string, string) (string, error)
	Read            func(context.Context, ciRolloutTarget) ([]byte, error)
	Patch           func(context.Context, ciRolloutTarget, []byte, bool) error
	Wait            func(context.Context, ciRolloutTarget) error
	Commit          func(context.Context) (func(context.Context) error, error)
	Record          func(context.Context, string, ciRolloutTarget, string, string) error
}
type ciRolloutBatchChange struct {
	step     ciRolloutBatchStep
	previous map[string]string
}

func executeCIRolloutBatch(ctx context.Context, plan *deploymentplan.Plan, policy ciBranchSelection, repo, branch string, images map[string]environmentReleaseImage, hooks ciRolloutBatchHooks) (result error) {
	steps, err := ciRolloutBatchSteps(plan, policy, repo, branch, images)
	if err != nil {
		return err
	}
	if hooks.RecoveryTimeout <= 0 || hooks.Acquire == nil || hooks.Fence == nil || hooks.Head == nil || hooks.Read == nil || hooks.Patch == nil || hooks.Wait == nil || hooks.Commit == nil || hooks.Record == nil {
		return errors.New("CI rollout: complete execution dependencies required")
	}
	revision := ""
	for _, image := range images {
		revision = image.SourceRevision
		break
	}
	release, err := hooks.Acquire(ctx)
	if err != nil {
		return err
	}
	if release == nil {
		return errors.New("CI rollout: execution lease release required")
	}
	keepLease := false
	defer func() {
		if keepLease {
			result = errors.Join(result, errors.New("CI rollout: original lease retained for recovery on the next manual run"))
			return
		}
		if err := release(); err != nil {
			result = errors.Join(result, errors.New("CI rollout: execution lease release failed"))
		}
	}()
	var changed []ciRolloutBatchChange
	var undoState func(context.Context) error
	defer func() {
		if result == nil {
			return
		}
		recovery, cancel := context.WithTimeout(context.Background(), hooks.RecoveryTimeout)
		defer cancel()
		recovered := true
		if err := hooks.Fence(recovery); err != nil {
			result = errors.Join(result, errors.New("CI rollout: recovery lease owner changed"))
			keepLease = true
			return
		}
		if undoState != nil {
			if err := undoState(recovery); err != nil {
				result = errors.Join(result, errors.New("CI rollout: completed version state recovery failed"))
				recovered = false
			}
		}
		for i := len(changed) - 1; i >= 0; i-- {
			item := changed[i]
			raw, err := hooks.Read(recovery, item.step.Target)
			if err != nil {
				result = errors.Join(result, errors.New("CI rollout: cannot inspect workload for recovery"))
				recovered = false
				continue
			}
			patch, _, err := ciRolloutWorkloadPatch(raw, item.step.Target, item.previous, item.step.Images)
			if err != nil {
				result = errors.Join(result, errors.New("CI rollout: workload changed during recovery"))
				recovered = false
				continue
			}
			if patch == nil {
				continue
			}
			if err = hooks.Patch(recovery, item.step.Target, patch, true); err == nil {
				err = hooks.Patch(recovery, item.step.Target, patch, false)
			}
			if err == nil {
				err = hooks.Wait(recovery, item.step.Target)
			}
			if err != nil {
				result = errors.Join(result, errors.New("CI rollout: previous workload image recovery failed"))
				recovered = false
			}
		}
		status := "failed"
		if recovered {
			status = "recovered"
		}
		if err := hooks.Record(recovery, status, ciRolloutTarget{}, "", ""); err != nil {
			result = errors.Join(result, errors.New("CI rollout: failure could not be recorded"))
			recovered = false
		}
		keepLease = !recovered
	}()
	if err = hooks.Record(ctx, "started", ciRolloutTarget{}, "", ""); err != nil {
		return err
	}
	for _, step := range steps {
		if err := hooks.Fence(ctx); err != nil {
			return err
		}
		raw, err := hooks.Read(ctx, step.Target)
		if err != nil {
			return err
		}
		patch, previous, err := ciRolloutWorkloadPatch(raw, step.Target, step.Images, nil)
		if err != nil {
			return err
		}
		if patch == nil {
			continue
		}
		if err = hooks.Patch(ctx, step.Target, patch, true); err != nil {
			return err
		}
		head, err := hooks.Head(ctx, repo, branch)
		if err != nil || head != revision {
			return errors.New("CI rollout: selected branch advanced before deployment")
		}
		names := make([]string, 0, len(step.Images))
		for name := range step.Images {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if previous[name] == step.Images[name] {
				continue
			}
			target := step.Target
			target.Container = name
			if err = hooks.Record(ctx, "applying", target, previous[name], step.Images[name]); err != nil {
				return err
			}
		}
		changed = append(changed, ciRolloutBatchChange{step: step, previous: previous})
		if err = hooks.Patch(ctx, step.Target, patch, false); err != nil {
			return fmt.Errorf("CI rollout: workload update failed: %w", err)
		}
		if err = hooks.Wait(ctx, step.Target); err != nil {
			return fmt.Errorf("CI rollout: workload did not become ready: %w", err)
		}
	}
	head, err := hooks.Head(ctx, repo, branch)
	if err != nil || head != revision {
		return errors.New("CI rollout: selected branch advanced before completion")
	}
	if err = hooks.Fence(ctx); err != nil {
		return err
	}
	undoState, err = hooks.Commit(ctx)
	if err != nil {
		return err
	}
	return hooks.Record(ctx, "completed", ciRolloutTarget{}, "", "")
}

func ciRolloutNativeBatchHooks(backend environmentKubernetesBackend, plan *deploymentplan.Plan, journal *ciRolloutJournal, run environmentReleaseCommand, recoveryTimeout time.Duration) ciRolloutBatchHooks {
	single := ciRolloutNativeHooks(backend, plan, journal, run)
	var active deploymentplan.ExecutionLease
	var runID int64
	var attempt int
	leaseID := "Lease/" + plan.Stack + "-video-cloud/rtk-deployment-execution"
	acquire := func(ctx context.Context) (func() error, error) {
		var err error
		runID, err = strconv.ParseInt(os.Getenv("GITHUB_RUN_ID"), 10, 64)
		if err != nil || runID <= 0 {
			return nil, errors.New("CI rollout: GitHub run identity is required")
		}
		attempt, err = strconv.Atoi(os.Getenv("GITHUB_RUN_ATTEMPT"))
		if err != nil || attempt <= 0 {
			return nil, errors.New("CI rollout: GitHub run attempt is required")
		}
		if err = checkGenerationRemovalIdle(backend.Store.Root); err != nil {
			return nil, err
		}
		return waitForCIRolloutLease(ctx, 2*time.Second, func(ctx context.Context) (bool, error) {
			lease, err := backend.Read(ctx, leaseID)
			if err != nil || lease == nil {
				return false, err
			}
			annotations := deploymentplan.Map(deploymentplan.Map(lease["metadata"])["annotations"])
			if deploymentplan.String(annotations["rtk.realtek.com/ci-repository"]) != journal.Repository {
				return true, nil
			}
			pending, err := ciRolloutReadPending(ctx, backend, plan, journal.Repository)
			if err != nil {
				return true, err
			}
			recoverable, err := ciRolloutCanRecoverLease(ctx, journal.Repository, runID, attempt, lease, pending, run)
			if err != nil {
				return true, err
			}
			if !recoverable {
				return true, nil
			}
			if err = ciRolloutRecoverStale(ctx, backend, plan, journal, lease, pending, single); err != nil {
				return true, err
			}
			return false, nil
		}, func(ctx context.Context) (func() error, error) {
			annotations := map[string]string{"rtk.realtek.com/ci-repository": journal.Repository, "rtk.realtek.com/ci-run-id": strconv.FormatInt(runID, 10), "rtk.realtek.com/ci-run-attempt": strconv.Itoa(attempt)}
			active, err = acquireEnvironmentExecutionLease(ctx, backend, plan, annotations)
			if err != nil {
				return nil, err
			}
			return func() error {
				cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				return active.Release(cleanup)
			}, nil
		})
	}
	fence := func(ctx context.Context) error {
		if active == nil {
			return errors.New("CI rollout: execution lease is unavailable")
		}
		return active.Check(ctx)
	}
	record := func(ctx context.Context, status string, target ciRolloutTarget, previous, desired string) error {
		if err := journal.recordBatch(ctx, status, target, previous, desired); err != nil {
			return err
		}
		if status != "started" && status != "applying" && status != "recovered" {
			return nil
		}
		if active == nil {
			return errors.New("CI rollout: execution lease is unavailable")
		}
		owner := active.Owner()
		pending, err := ciRolloutReadPending(ctx, backend, plan, journal.Repository)
		if err != nil {
			return err
		}
		if status == "started" {
			if pending != nil {
				return errors.New("CI rollout: an earlier recovery record remains")
			}
			pending = &ciRolloutPending{Repository: journal.Repository, Branch: journal.Branch, RunID: runID, Attempt: attempt, LeaseUID: owner.UID, Holder: owner.Holder}
		} else if pending != nil {
			if pending.LeaseUID != owner.UID || pending.Holder != owner.Holder || pending.RunID != runID || pending.Attempt != attempt {
				return errors.New("CI rollout: recovery state owner changed")
			}
			if status == "applying" {
				pending.Changes = append(pending.Changes, ciRolloutPendingChange{Namespace: target.Namespace, Name: target.Name, UID: target.UID, Container: target.Container, PreviousImage: previous, DesiredImage: desired})
			} else {
				pending = nil
			}
		} else if status == "applying" {
			return errors.New("CI rollout: recovery state is missing")
		}
		return ciRolloutWritePending(ctx, backend, plan, journal.Repository, journal.Branch, pending)
	}
	return ciRolloutBatchHooks{RecoveryTimeout: recoveryTimeout, Acquire: acquire, Fence: fence, Head: single.Head, Read: single.Read, Patch: single.Patch, Wait: single.Wait, Commit: single.Commit, Record: record}
}
