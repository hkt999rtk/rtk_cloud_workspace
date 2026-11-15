package main

import (
	"context"
	"errors"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type ciRolloutProvisionAPI interface {
	Read(context.Context, string) (deploymentplan.Object, error)
	Write(context.Context, string, deploymentplan.Object, bool) (deploymentplan.Object, error)
}

// Provision only resources explicitly owned by this repository. Existing
// foreign resources are never adopted or replaced.
func ciRolloutEnsureManagedObject(ctx context.Context, api ciRolloutProvisionAPI, repo string, desired deploymentplan.Object) error {
	if api == nil || !ciRepositoryName.MatchString(repo) || desired == nil {
		return errors.New("CI rollout: managed resource and repository required")
	}
	id := deploymentplan.ID(desired)
	meta := deploymentplan.Map(desired["metadata"])
	annotations := deploymentplan.Map(meta["annotations"])
	if deploymentplan.String(annotations["rtk.realtek.com/ci-rollout-repository"]) != repo {
		return errors.New("CI rollout: managed resource ownership is missing")
	}
	current, err := api.Read(ctx, id)
	if err != nil {
		return err
	}
	if current == nil {
		admitted, err := api.Write(ctx, "create", desired, true)
		if err != nil || admitted == nil || !deploymentplan.Matches(desired, admitted) {
			return errors.New("CI rollout: managed resource admission failed")
		}
		created, err := api.Write(ctx, "create", desired, false)
		if err != nil || created == nil || !deploymentplan.Matches(desired, created) || deploymentplan.String(deploymentplan.Map(created["metadata"])["uid"]) == "" {
			return errors.New("CI rollout: managed resource creation could not be confirmed")
		}
		return nil
	}
	cm := deploymentplan.Map(current["metadata"])
	if deploymentplan.String(deploymentplan.Map(cm["annotations"])["rtk.realtek.com/ci-rollout-repository"]) != repo {
		return errors.New("CI rollout: existing resource belongs to another owner")
	}
	if deploymentplan.Matches(desired, current) {
		return nil
	}
	uid, version := deploymentplan.String(cm["uid"]), deploymentplan.String(cm["resourceVersion"])
	if uid == "" || version == "" {
		return errors.New("CI rollout: existing resource identity is unavailable")
	}
	update := deploymentplan.Clone(desired)
	um := deploymentplan.Map(update["metadata"])
	um["uid"], um["resourceVersion"] = uid, version
	for _, key := range []string{"annotations", "labels"} {
		combined := map[string]any{}
		for k, v := range deploymentplan.Map(cm[key]) {
			combined[k] = v
		}
		for k, v := range deploymentplan.Map(um[key]) {
			combined[k] = v
		}
		if len(combined) > 0 {
			um[key] = combined
		}
	}
	admitted, err := api.Write(ctx, "update", update, true)
	if err != nil || admitted == nil || !deploymentplan.Matches(desired, admitted) {
		return errors.New("CI rollout: managed resource update admission failed")
	}
	actual, err := api.Write(ctx, "update", update, false)
	if err != nil || actual == nil || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != uid || !deploymentplan.Matches(desired, actual) {
		return errors.New("CI rollout: managed resource update could not be confirmed")
	}
	return nil
}
