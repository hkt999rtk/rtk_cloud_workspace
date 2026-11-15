package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

var errCIRolloutMigrationChanged = errors.New("CI rollout: database migrations changed; use the full environment deployment")

// Only schema-owning services need this comparison. Unchanged SQL migrations
// do not require database credentials or a full preflight for ordinary changes.
func checkCIRolloutMigrationChange(ctx context.Context, repo, previous, candidate string, read func(context.Context, string, string) (string, error)) error {
	if repo != "hkt999rtk/rtk_account_manager" && repo != "hkt999rtk/rtk_billing" {
		return nil
	}
	if !deploymentUpgradeSHA.MatchString(previous) || !deploymentUpgradeSHA.MatchString(candidate) || read == nil {
		return errors.New("CI rollout: existing schema source is unavailable")
	}
	if previous == candidate {
		return nil
	}
	old, err := read(ctx, repo, previous)
	if err != nil {
		return err
	}
	next, err := read(ctx, repo, candidate)
	if err != nil {
		return err
	}
	if old != next {
		return errCIRolloutMigrationChanged
	}
	return nil
}

func ciRolloutMigrationFingerprint(ctx context.Context, repo, revision string, run environmentReleaseCommand) (string, error) {
	if !ciRepositoryName.MatchString(repo) || !deploymentUpgradeSHA.MatchString(revision) || run == nil {
		return "", errors.New("CI rollout: migration source unavailable")
	}
	type entry struct {
		Path string `json:"path"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
	}
	read := func(ref string) ([]entry, error) {
		raw, err := run(ctx, "gh", "api", "repos/"+repo+"/git/trees/"+ref)
		if err != nil {
			return nil, err
		}
		var value struct {
			Truncated bool    `json:"truncated"`
			Tree      []entry `json:"tree"`
		}
		if json.Unmarshal(raw, &value) != nil || value.Truncated || value.Tree == nil {
			return nil, errors.New("CI rollout: incomplete migration tree")
		}
		return value.Tree, nil
	}
	root, err := read(revision)
	if err != nil {
		return "", err
	}
	tree := ""
	for _, item := range root {
		if item.Path == "migrations" {
			if tree != "" || item.Type != "tree" || !deploymentUpgradeSHA.MatchString(item.SHA) {
				return "", errors.New("CI rollout: invalid migration tree")
			}
			tree = item.SHA
		}
	}
	if tree == "" {
		return "", errors.New("CI rollout: migration directory is unavailable")
	}
	entries, err := read(tree)
	if err != nil {
		return "", err
	}
	versions := []string{}
	for _, item := range entries {
		if item.Type == "blob" && strings.HasSuffix(item.Path, ".sql") {
			if !deploymentUpgradeSHA.MatchString(item.SHA) {
				return "", errors.New("CI rollout: invalid migration blob")
			}
			versions = append(versions, item.Path+":"+item.SHA)
		}
	}
	if len(versions) == 0 {
		return "", errors.New("CI rollout: migration inventory is empty")
	}
	sort.Strings(versions)
	return environmentPreflightBytesSHA256([]byte(strings.Join(versions, "\n"))), nil
}

func checkCIRolloutSchemaState(ctx context.Context, backend environmentKubernetesBackend, plan *deploymentplan.Plan, repo string, images map[string]environmentReleaseImage, run environmentReleaseCommand) error {
	if repo != "hkt999rtk/rtk_account_manager" && repo != "hkt999rtk/rtk_billing" {
		return nil
	}
	if plan == nil || plan.Environment != "dev" || len(images) != 1 {
		return errors.New("CI rollout: schema-owning service image selection is invalid")
	}
	var repository string
	var candidate environmentReleaseImage
	for repository, candidate = range images {
		break
	}
	name, err := ciRolloutStateName(repo)
	if err != nil {
		return err
	}
	object, err := backend.Read(ctx, "ConfigMap/"+plan.Stack+"-video-cloud/"+name)
	if err != nil || object == nil {
		return errors.New("CI rollout: existing schema version state is unavailable")
	}
	raw := deploymentplan.String(deploymentplan.Map(object["data"])["state.json"])
	var state ciRolloutState
	if raw == "" || decodeEnvironmentJSON([]byte(raw), &state) != nil || state.Environment != "dev" || state.Repository != repo {
		return errors.New("CI rollout: existing schema version baseline is unavailable")
	}
	previous, ok := state.Images[repository]
	if !ok || !deploymentUpgradeSHA.MatchString(previous.SourceRevision) || !immutableEnvironmentImage.MatchString(previous.Reference) {
		return errors.New("CI rollout: existing schema source is unavailable")
	}
	for _, resource := range plan.Resources {
		meta := deploymentplan.Map(resource.Desired["metadata"])
		namespace, name := deploymentplan.String(meta["namespace"]), deploymentplan.String(meta["name"])
		pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(resource.Desired["spec"])["template"])["spec"])
		containers, _ := pod["containers"].([]any)
		var matching []string
		for _, entry := range containers {
			c := deploymentplan.Map(entry)
			if environmentImageRepository(deploymentplan.String(c["image"])) == repository {
				matching = append(matching, deploymentplan.String(c["name"]))
			}
		}
		if len(matching) == 0 {
			continue
		}
		current, err := backend.Read(ctx, "Deployment/"+namespace+"/"+name)
		if err != nil || current == nil {
			return errors.New("CI rollout: existing schema workload cannot be inspected")
		}
		if deploymentplan.String(deploymentplan.Map(current["metadata"])["uid"]) != resource.UID {
			return errors.New("CI rollout: existing schema workload identity changed")
		}
		for _, container := range matching {
			if environmentReleaseSlotImage(current, container) != previous.Reference {
				return errors.New("CI rollout: existing schema baseline differs from current workload")
			}
		}
	}
	return checkCIRolloutMigrationChange(ctx, repo, previous.SourceRevision, candidate.SourceRevision, func(ctx context.Context, repo, revision string) (string, error) {
		return ciRolloutMigrationFingerprint(ctx, repo, revision, run)
	})
}
