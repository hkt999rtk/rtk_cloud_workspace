package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A fresh dev generation uses the configured branch as its version source.
// It never reads a previous generation's Kubernetes state or identity.
func resolveFreshCIBranchSelection(ctx context.Context, selection environmentImageSelection, policy ciBranchSelection, resolve func(context.Context, ciBranchSelection, string, string) (environmentReleaseImage, error)) (environmentImageSelection, error) {
	if selection.Environment != "dev" || len(policy) == 0 {
		return selection, nil
	}
	if resolve == nil {
		return environmentImageSelection{}, errors.New("CI rollout: fresh branch resolver required")
	}
	known := map[string]bool{}
	for _, source := range lkeServiceImageSources() {
		known["hkt999rtk/"+source.RepoName] = true
	}
	result := environmentImageSelection{Environment: selection.Environment, Images: map[string]environmentReleaseImage{}}
	for role, image := range selection.Images {
		result.Images[role] = image
	}
	roles := environmentVersionRoles()
	keys := make([]string, 0, len(roles))
	for role := range roles {
		keys = append(keys, role)
	}
	sort.Strings(keys)
	cache := map[string]environmentReleaseImage{}
	for _, role := range keys {
		repository := roles[role]
		parts := strings.Split(repository, "/")
		if len(parts) < 4 || parts[0] != "ghcr.io" {
			continue
		}
		repo := parts[1] + "/" + parts[2]
		if !known[repo] || policy[repo] == "" {
			continue
		}
		image, ok := cache[repository]
		if !ok {
			var err error
			image, err = resolve(ctx, policy, repo, repository)
			if errors.Is(err, errEnvironmentPublicationConsumerMissing) && role == "LKE_OPENBAO_PKI_IMAGE" && selection.Images[role].Reference == "" {
				continue
			}
			if err != nil {
				return environmentImageSelection{}, err
			}
			cache[repository] = image
		}
		result.Images[role] = image
	}
	return result, nil
}

func applyEnvironmentCIRolloutStates(ctx context.Context, selection environmentImageSelection, policy ciBranchSelection, read func(context.Context, string) (*ciRolloutState, error)) (environmentImageSelection, error) {
	if len(policy) == 0 {
		return selection, nil
	}
	if read == nil {
		return environmentImageSelection{}, errors.New("CI rollout: environment state reader required")
	}
	repositories := make([]string, 0, len(policy))
	for repo := range policy {
		repositories = append(repositories, repo)
	}
	sort.Strings(repositories)
	var err error
	for _, repo := range repositories {
		state, readErr := read(ctx, repo)
		if readErr != nil {
			return environmentImageSelection{}, readErr
		}
		if state == nil {
			continue
		}
		if state.Repository != repo {
			return environmentImageSelection{}, errors.New("CI rollout: environment state belongs to another repository")
		}
		selection, err = projectCIRolloutState(selection, policy, *state)
		if err != nil {
			return environmentImageSelection{}, err
		}
	}
	return selection, nil
}

// Only existing dev upgrades consult runtime CI state. Fresh creates and original
// create-recovery callers use their own selected generation and never call this.
func readEnvironmentCIRolloutSelection(ctx context.Context, cfg deploymentConfig, selection environmentImageSelection) (environmentImageSelection, error) {
	if cfg.Environment != "dev" {
		return selection, nil
	}
	policy, err := readCIBranchSelection(cfg)
	if err != nil {
		return environmentImageSelection{}, err
	}
	if len(policy) == 0 {
		return selection, nil
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return environmentImageSelection{}, err
	}
	path := filepath.Join(store.Root, "kube", "kubeconfig.yaml")
	if _, err = os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return selection, nil
	} else if err != nil {
		return environmentImageSelection{}, err
	}
	path, err = privateDeploymentStatePath(cfg.Environment, filepath.Join("kube", "kubeconfig.yaml"))
	if err != nil {
		return environmentImageSelection{}, err
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil {
		return environmentImageSelection{}, err
	}
	backend, err := newEnvironmentKubernetesBackend(string(raw), cfg.Environment)
	if err != nil {
		return environmentImageSelection{}, err
	}
	return applyEnvironmentCIRolloutStates(ctx, selection, policy, func(ctx context.Context, repo string) (*ciRolloutState, error) {
		name, err := ciRolloutStateName(repo)
		if err != nil {
			return nil, err
		}
		object, err := backend.Read(ctx, "ConfigMap/"+cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud/"+name)
		if err != nil {
			return nil, err
		}
		if object == nil {
			return nil, nil
		}
		raw := deploymentplan.String(deploymentplan.Map(object["data"])["state.json"])
		if raw == "" {
			return nil, nil
		}
		var state ciRolloutState
		if decodeEnvironmentJSON([]byte(raw), &state) != nil {
			return nil, errors.New("CI rollout: malformed completed environment state")
		}
		return &state, nil
	})
}
