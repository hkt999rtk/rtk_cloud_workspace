package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type ciRolloutIdentityBinding struct {
	Environment       string `json:"environment"`
	Repository        string `json:"repository"`
	Namespace         string `json:"namespace"`
	ServiceAccount    string `json:"service_account"`
	ServiceAccountUID string `json:"service_account_uid"`
	TokenSecret       string `json:"token_secret"`
	TokenSecretUID    string `json:"token_secret_uid"`
}

type ciRolloutPreparedIdentity struct {
	Repository string
	Kubeconfig []byte
	Targets    []byte
}

func ciRolloutTargetsForPlan(plan *deploymentplan.Plan, repo string) ([]ciRolloutTarget, error) {
	repositories, err := ciRolloutImageRepositories(plan, repo)
	if err != nil {
		return nil, err
	}
	var all []ciRolloutTarget
	for _, repository := range repositories {
		targets, err := ciRolloutTargetsFromPlan(plan, repository)
		if err != nil {
			return nil, err
		}
		all = append(all, targets...)
	}
	return all, nil
}

func ciRolloutEnsureState(ctx context.Context, b environmentKubernetesBackend, plan *deploymentplan.Plan, repo, branch string, observed map[string]environmentReleaseImage) error {
	name, err := ciRolloutStateName(repo)
	if err != nil {
		return err
	}
	namespace := plan.Stack + "-video-cloud"
	id := "ConfigMap/" + namespace + "/" + name
	current, err := b.Read(ctx, id)
	if err != nil {
		return err
	}
	encoded := ""
	if len(observed) > 0 {
		raw, err := json.Marshal(ciRolloutState{Environment: "dev", Repository: repo, Branch: branch, Origin: "observed", Images: observed})
		if err != nil {
			return err
		}
		encoded = string(raw)
	}
	desired := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"namespace": namespace, "name": name, "annotations": map[string]any{"rtk.realtek.com/ci-rollout-repository": repo}}, "data": map[string]any{"state.json": encoded}}
	if current == nil {
		return ciRolloutEnsureManagedObject(ctx, b, repo, desired)
	}
	meta := deploymentplan.Map(current["metadata"])
	if deploymentplan.String(deploymentplan.Map(meta["annotations"])["rtk.realtek.com/ci-rollout-repository"]) != repo {
		return errors.New("CI rollout: existing version state belongs to another owner")
	}
	data := deploymentplan.Map(current["data"])
	value, exists := data["state.json"]
	if !exists {
		return errors.New("CI rollout: existing version state has no state.json")
	}
	if deploymentplan.String(value) != "" || encoded == "" {
		return nil
	}
	// An empty owned state may be initialized from directly observed matching
	// workloads. Preserve every unrelated ConfigMap field and its original UID.
	if deploymentplan.String(meta["uid"]) == "" || deploymentplan.String(meta["resourceVersion"]) == "" {
		return errors.New("CI rollout: version state identity is unavailable")
	}
	update := deploymentplan.Clone(current)
	delete(update, "status")
	um := deploymentplan.Map(update["metadata"])
	delete(um, "managedFields")
	delete(um, "creationTimestamp")
	deploymentplan.Map(update["data"])["state.json"] = encoded
	if _, err = b.Write(ctx, "update", update, true); err != nil {
		return err
	}
	actual, err := b.Write(ctx, "update", update, false)
	if err != nil || actual == nil || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != deploymentplan.String(meta["uid"]) {
		return errors.New("CI rollout: observed version state could not be saved")
	}
	return nil
}

func ciRolloutObserveBaseline(ctx context.Context, b environmentKubernetesBackend, plan *deploymentplan.Plan, policy ciBranchSelection, repo string, run environmentReleaseCommand) (map[string]environmentReleaseImage, error) {
	repositories, err := ciRolloutImageRepositories(plan, repo)
	if err != nil {
		return nil, err
	}
	images := map[string]environmentReleaseImage{}
	for _, repository := range repositories {
		image, err := resolveCIRolloutImage(ctx, policy, repo, repository, run)
		if errors.Is(err, errCIRolloutNotReady) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		images[repository] = image
	}
	steps, err := ciRolloutBatchSteps(plan, policy, repo, policy[repo], images)
	if err != nil {
		return nil, err
	}
	for _, step := range steps {
		object, err := b.Read(ctx, "Deployment/"+step.Target.Namespace+"/"+step.Target.Name)
		if err != nil || object == nil {
			return nil, errors.New("CI rollout: existing workload is unavailable")
		}
		raw, err := json.Marshal(object)
		if err != nil {
			return nil, err
		}
		patch, _, err := ciRolloutWorkloadPatch(raw, step.Target, step.Images, nil)
		if err != nil {
			return nil, err
		}
		if patch != nil {
			return nil, nil
		}
		if err = b.Wait(ctx, deploymentplan.Resource{ID: deploymentplan.ID(object), Phase: "workload", Action: "update"}); err != nil {
			return nil, err
		}
	}
	return images, nil
}

func ciRolloutToken(ctx context.Context, b environmentKubernetesBackend, namespace, account, repo string) (string, string, error) {
	name := account + "-token"
	if len(name) > 63 {
		return "", "", errors.New("CI rollout: token Secret name is too long")
	}
	object := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"namespace": namespace, "name": name, "annotations": map[string]any{"rtk.realtek.com/ci-rollout-repository": repo, "kubernetes.io/service-account.name": account}}, "type": "kubernetes.io/service-account-token"}
	if err := ciRolloutEnsureManagedObject(ctx, b, repo, object); err != nil {
		return "", "", err
	}
	id := "Secret/" + namespace + "/" + name
	for {
		secret, err := b.Read(ctx, id)
		if err != nil {
			return "", "", err
		}
		if secret == nil {
			return "", "", errors.New("CI rollout: restricted token Secret disappeared")
		}
		meta := deploymentplan.Map(secret["metadata"])
		uid := deploymentplan.String(meta["uid"])
		encoded := deploymentplan.String(deploymentplan.Map(secret["data"])["token"])
		if encoded != "" && uid != "" {
			raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
			if err != nil || len(raw) == 0 {
				return "", "", errors.New("CI rollout: restricted token is invalid")
			}
			return string(raw), uid, nil
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", "", ctx.Err()
		case <-timer.C:
		}
	}
}

func ciRolloutSaveBinding(store secretStore, binding ciRolloutIdentityBinding, kube, targets []byte) (string, error) {
	name, err := ciRolloutStateName(binding.Repository)
	if err != nil {
		return "", err
	}
	base := filepath.Join("deployment", "ci-rollout", "identities", name)
	bindingPath := filepath.Join(base, "binding.json")
	for _, relative := range []string{bindingPath, filepath.Join(base, "kubeconfig.yaml"), filepath.Join(base, "targets.json")} {
		if _, err := ciRolloutPrivatePath(store, relative); err != nil {
			return "", err
		}
	}
	previous, err := store.read(bindingPath)
	if err == nil {
		var existing ciRolloutIdentityBinding
		if decodeEnvironmentJSON([]byte(previous), &existing) != nil || existing != binding {
			return "", errors.New("CI rollout: restricted identity differs from its original binding")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	} else {
		raw, err := json.Marshal(binding)
		if err != nil {
			return "", err
		}
		if err = store.write(bindingPath, raw, false); err != nil {
			return "", err
		}
	}
	kubePath := filepath.Join(base, "kubeconfig.yaml")
	existingKube, readErr := store.read(kubePath)
	if readErr == nil && existingKube != strings.TrimSpace(string(kube)) {
		return "", errors.New("CI rollout: existing restricted kubeconfig changed; preserve it for inspection")
	}
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", readErr
	}
	if errors.Is(readErr, os.ErrNotExist) {
		if err = store.write(kubePath, kube, false); err != nil {
			return "", err
		}
	}
	if err = store.write(filepath.Join(base, "targets.json"), targets, true); err != nil {
		return "", err
	}
	return base, nil
}

func ciRolloutGitHubEnvironment(ctx context.Context, repo string) error {
	endpoint := "repos/" + repo + "/environments/dev"
	cmd := exec.CommandContext(ctx, "gh", "api", endpoint)
	_, err := cmd.Output()
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || !strings.Contains(string(exit.Stderr), "HTTP 404") {
		return errors.New("CI rollout: GitHub dev environment cannot be inspected")
	}
	cmd = exec.CommandContext(ctx, "gh", "api", "--method", "PUT", endpoint, "--input", "-")
	cmd.Stdin = strings.NewReader("{}")
	if _, err = cmd.Output(); err != nil {
		return errors.New("CI rollout: GitHub dev environment cannot be created")
	}
	return nil
}
func ciRolloutGitHubSecret(ctx context.Context, repo, name string, value []byte) error {
	if !ciRepositoryName.MatchString(repo) || (name != "RTK_DEV_ROLLOUT_KUBECONFIG" && name != "RTK_DEV_ROLLOUT_TARGETS") || len(value) == 0 {
		return errors.New("CI rollout: invalid secret destination")
	}
	cmd := exec.CommandContext(ctx, "gh", "secret", "set", name, "--repo", repo, "--env", "dev")
	cmd.Stdin = bytes.NewReader(value)
	if err := cmd.Run(); err != nil {
		return errors.New("CI rollout: GitHub environment secret could not be stored")
	}
	return nil
}

func ciRolloutSortedRepositories(policy ciBranchSelection) []string {
	result := make([]string, 0, len(policy))
	for repo := range policy {
		result = append(result, repo)
	}
	sort.Strings(result)
	return result
}
