package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Commit the completed digest only after workload readiness. The returned
// compensator restores the prior state if finalization fails under the lease.
func ciRolloutCommitState(ctx context.Context, b environmentKubernetesBackend, plan *deploymentplan.Plan, journal *ciRolloutJournal) (func(context.Context) error, error) {
	if plan == nil || journal == nil || plan.Environment != "dev" || journal.Environment != plan.Environment {
		return nil, errors.New("CI rollout: selected environment state required")
	}
	name, err := ciRolloutStateName(journal.Repository)
	if err != nil {
		return nil, err
	}
	namespace := plan.Stack + "-video-cloud"
	id := "ConfigMap/" + namespace + "/" + name
	object, err := b.Read(ctx, id)
	if err != nil || object == nil {
		return nil, errors.New("CI rollout: selected environment version state is unavailable")
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	images := journal.Images
	if len(images) == 0 {
		images = map[string]environmentReleaseImage{environmentImageRepository(journal.Image.Reference): journal.Image}
	}
	patch, err := ciRolloutStateBatchPatch(raw, namespace, journal.Repository, journal.Branch, images)
	if err != nil {
		return nil, err
	}
	var ops []struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(patch, &ops) != nil || len(ops) != 4 {
		return nil, errors.New("CI rollout: invalid state update")
	}
	uid, previous, next := ops[0].Value, ops[2].Value, ops[3].Value
	apply := func(ctx context.Context, patch []byte, dry bool) error {
		args := []string{"patch", "configmap", name, "--namespace", namespace, "--type=json", "--patch-file=/dev/stdin", "-o", "json"}
		if dry {
			args = append(args, "--dry-run=server")
		}
		_, err := b.command(ctx, patch, args...)
		return err
	}
	undo := func(ctx context.Context) error {
		current, err := b.Read(ctx, id)
		if err != nil || current == nil {
			return errors.New("CI rollout: cannot inspect completed version state")
		}
		meta := deploymentplan.Map(current["metadata"])
		data := deploymentplan.Map(current["data"])
		version := deploymentplan.String(meta["resourceVersion"])
		value := deploymentplan.String(data["state.json"])
		if deploymentplan.String(meta["uid"]) != uid || version == "" {
			return errors.New("CI rollout: completed version state identity changed")
		}
		if value == previous {
			return nil
		}
		if value != next {
			return errors.New("CI rollout: completed version state changed concurrently")
		}
		type operation struct {
			Op    string `json:"op"`
			Path  string `json:"path"`
			Value string `json:"value"`
		}
		inverse, err := json.Marshal([]operation{{"test", "/metadata/uid", uid}, {"test", "/metadata/resourceVersion", version}, {"test", "/data/state.json", next}, {"replace", "/data/state.json", previous}})
		if err != nil {
			return err
		}
		if err = apply(ctx, inverse, true); err != nil {
			return err
		}
		return apply(ctx, inverse, false)
	}
	if previous == next {
		return nil, nil
	}
	if err = apply(ctx, patch, true); err != nil {
		return nil, err
	}
	if err = apply(ctx, patch, false); err != nil {
		recovery, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if undoErr := undo(recovery); undoErr != nil {
			return nil, errors.Join(err, errors.New("CI rollout: uncertain version state could not be restored"))
		}
		return nil, err
	}
	return undo, nil
}
