package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Only an exact, independently observed completed run of this repository may
// have its interrupted CI lease recovered. Other owners remain untouched.
func ciRolloutCanRecoverLease(ctx context.Context, repo string, currentRun int64, currentAttempt int, lease deploymentplan.Object, pending *ciRolloutPending, run environmentReleaseCommand) (bool, error) {
	if lease == nil || !ciRepositoryName.MatchString(repo) || run == nil {
		return false, nil
	}
	meta := deploymentplan.Map(lease["metadata"])
	annotations := deploymentplan.Map(meta["annotations"])
	if deploymentplan.String(annotations["rtk.realtek.com/ci-repository"]) != repo {
		return false, nil
	}
	runID, err := strconv.ParseInt(deploymentplan.String(annotations["rtk.realtek.com/ci-run-id"]), 10, 64)
	if err != nil || runID <= 0 {
		return false, errors.New("CI rollout: prior CI lease run identity is invalid")
	}
	attempt, err := strconv.Atoi(deploymentplan.String(annotations["rtk.realtek.com/ci-run-attempt"]))
	if err != nil || attempt <= 0 {
		return false, errors.New("CI rollout: prior CI lease attempt is invalid")
	}
	if runID == currentRun && attempt == currentAttempt {
		return false, nil
	}
	uid, holder := deploymentplan.String(meta["uid"]), deploymentplan.String(deploymentplan.Map(lease["spec"])["holderIdentity"])
	if uid == "" || holder == "" {
		return false, errors.New("CI rollout: prior lease owner is invalid")
	}
	if pending != nil && (pending.Repository != repo || pending.RunID != runID || pending.Attempt != attempt || pending.LeaseUID != uid || pending.Holder != holder) {
		return false, errors.New("CI rollout: pending recovery does not match the original lease")
	}
	raw, err := run(ctx, "gh", "api", fmt.Sprintf("repos/%s/actions/runs/%d/attempts/%d", repo, runID, attempt))
	if err != nil {
		return false, errors.New("CI rollout: prior run status cannot be verified")
	}
	var status struct {
		ID      int64  `json:"id"`
		Attempt int    `json:"run_attempt"`
		Status  string `json:"status"`
	}
	if json.Unmarshal(raw, &status) != nil || status.ID != runID || status.Attempt != attempt {
		return false, errors.New("CI rollout: prior run response is invalid")
	}
	return status.Status == "completed", nil
}

func ciRolloutReadPending(ctx context.Context, b environmentKubernetesBackend, plan *deploymentplan.Plan, repo string) (*ciRolloutPending, error) {
	name, err := ciRolloutStateName(repo)
	if err != nil {
		return nil, err
	}
	object, err := b.Read(ctx, "ConfigMap/"+plan.Stack+"-video-cloud/"+name)
	if err != nil || object == nil {
		return nil, errors.New("CI rollout: recovery state is unavailable")
	}
	raw := deploymentplan.String(deploymentplan.Map(object["data"])["state.json"])
	if raw == "" {
		return nil, nil
	}
	var state ciRolloutState
	if decodeEnvironmentJSON([]byte(raw), &state) != nil || state.Environment != "dev" || state.Repository != repo {
		return nil, errors.New("CI rollout: recovery state is invalid")
	}
	if state.Pending != nil {
		objectJSON, err := json.Marshal(object)
		if err != nil {
			return nil, err
		}
		if _, err = ciRolloutPendingStatePatch(objectJSON, plan.Stack+"-video-cloud", repo, state.Pending.Branch, state.Pending); err != nil {
			return nil, err
		}
	}
	return state.Pending, nil
}
func ciRolloutWritePending(ctx context.Context, b environmentKubernetesBackend, plan *deploymentplan.Plan, repo, branch string, pending *ciRolloutPending) error {
	name, err := ciRolloutStateName(repo)
	if err != nil {
		return err
	}
	namespace := plan.Stack + "-video-cloud"
	id := "ConfigMap/" + namespace + "/" + name
	object, err := b.Read(ctx, id)
	if err != nil || object == nil {
		return errors.New("CI rollout: recovery state is unavailable")
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return err
	}
	patch, err := ciRolloutPendingStatePatch(raw, namespace, repo, branch, pending)
	if err != nil {
		return err
	}
	args := []string{"patch", "configmap", name, "--namespace", namespace, "--type=json", "--patch-file=/dev/stdin", "-o", "json"}
	if _, err = b.command(ctx, patch, append(append([]string(nil), args...), "--dry-run=server")...); err != nil {
		return err
	}
	if _, err = b.command(ctx, patch, args...); err != nil {
		actual, readErr := ciRolloutReadPending(ctx, b, plan, repo)
		if readErr == nil && deploymentplan.Digest(actual) == deploymentplan.Digest(pending) {
			return nil
		}
		return err
	}
	return nil
}

// Restore only the original, finished CI owner's recorded image changes. The
// shared Lease stays present until every original workload is recovered.
func ciRolloutRecoverStale(ctx context.Context, b environmentKubernetesBackend, plan *deploymentplan.Plan, journal *ciRolloutJournal, lease deploymentplan.Object, pending *ciRolloutPending, ops ciRolloutExecutionHooks) error {
	meta := deploymentplan.Map(lease["metadata"])
	uid, holder := deploymentplan.String(meta["uid"]), deploymentplan.String(deploymentplan.Map(lease["spec"])["holderIdentity"])
	id := "Lease/" + plan.Stack + "-video-cloud/rtk-deployment-execution"
	if err := journal.recordBatch(ctx, "recovering", ciRolloutTarget{}, "", ""); err != nil {
		return err
	}
	type group struct {
		target            ciRolloutTarget
		previous, desired map[string]string
	}
	groups := []group{}
	indices := map[string]int{}
	if pending != nil {
		for _, change := range pending.Changes {
			key := change.Namespace + "/" + change.Name
			index, ok := indices[key]
			if !ok {
				index = len(groups)
				indices[key] = index
				groups = append(groups, group{target: ciRolloutTarget{Namespace: change.Namespace, Name: change.Name, UID: change.UID}, previous: map[string]string{}, desired: map[string]string{}})
			}
			item := &groups[index]
			if item.target.UID != change.UID || item.previous[change.Container] != "" {
				return errors.New("CI rollout: recovery workload identity is ambiguous")
			}
			item.previous[change.Container] = change.PreviousImage
			item.desired[change.Container] = change.DesiredImage
		}
	}
	for i := len(groups) - 1; i >= 0; i-- {
		currentLease, err := b.Read(ctx, id)
		if err != nil || currentLease == nil || deploymentplan.String(deploymentplan.Map(currentLease["metadata"])["uid"]) != uid || deploymentplan.String(deploymentplan.Map(currentLease["spec"])["holderIdentity"]) != holder {
			return errors.New("CI rollout: original recovery lease changed")
		}
		item := groups[i]
		raw, err := ops.Read(ctx, item.target)
		if err != nil {
			return err
		}
		patch, _, err := ciRolloutWorkloadPatch(raw, item.target, item.previous, item.desired)
		if err != nil {
			return err
		}
		if patch != nil {
			if err = ops.Patch(ctx, item.target, patch, true); err != nil {
				return err
			}
			if err = ops.Patch(ctx, item.target, patch, false); err != nil {
				return err
			}
			if err = ops.Wait(ctx, item.target); err != nil {
				return err
			}
		}
		for container, old := range item.previous {
			target := item.target
			target.Container = container
			if err = journal.recordBatch(ctx, "recovering", target, old, item.desired[container]); err != nil {
				return err
			}
		}
	}
	if err := journal.recordBatch(ctx, "recovered", ciRolloutTarget{}, "", ""); err != nil {
		return err
	}
	if pending != nil {
		current, err := ciRolloutReadPending(ctx, b, plan, journal.Repository)
		if err != nil || current == nil || current.LeaseUID != uid || current.Holder != holder {
			return errors.New("CI rollout: recovery record owner changed")
		}
		if err = ciRolloutWritePending(ctx, b, plan, journal.Repository, pending.Branch, nil); err != nil {
			return err
		}
	}
	current, err := b.Read(ctx, id)
	if err != nil || current == nil {
		return errors.New("CI rollout: original recovery lease cannot be inspected")
	}
	cm := deploymentplan.Map(current["metadata"])
	if deploymentplan.String(cm["uid"]) != uid || deploymentplan.String(deploymentplan.Map(current["spec"])["holderIdentity"]) != holder {
		return errors.New("CI rollout: original recovery lease owner changed")
	}
	version := deploymentplan.String(cm["resourceVersion"])
	if version == "" {
		return errors.New("CI rollout: original recovery lease version is unavailable")
	}
	if err = b.DeleteExecutionLease(ctx, id, uid, version); err != nil {
		return err
	}
	remaining, err := b.Read(ctx, id)
	if err != nil || remaining != nil {
		return errors.New("CI rollout: recovered lease release could not be confirmed")
	}
	return nil
}
