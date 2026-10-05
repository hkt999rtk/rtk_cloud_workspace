package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

type deploymentUpgradeTarget struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec map[string]any `json:"spec"`
}

func deploymentUpgradeTargets(paths []string, environment string) ([]deploymentUpgradeTarget, error) {
	var targets []deploymentUpgradeTarget
	seen := map[string]bool{}
	var add func(json.RawMessage) error
	add = func(raw json.RawMessage) error {
		var object struct {
			Kind  string            `json:"kind"`
			Items []json.RawMessage `json:"items"`
		}
		if json.Unmarshal(raw, &object) != nil {
			return errors.New("candidate workload JSON is invalid")
		}
		if object.Kind == "List" {
			for _, item := range object.Items {
				if err := add(item); err != nil {
					return err
				}
			}
			return nil
		}
		var target deploymentUpgradeTarget
		if json.Unmarshal(raw, &target) != nil || target.Kind != "Deployment" || target.Metadata.Name == "" || !deploymentHealthNamespace(environment, target.Metadata.Namespace) || target.Spec == nil {
			return errors.New("image upgrade candidates must be complete Deployments in the selected environment")
		}
		key := target.Metadata.Namespace + "/" + target.Metadata.Name
		if seen[key] {
			return errors.New("candidate workload is duplicated")
		}
		seen[key] = true
		targets = append(targets, target)
		return nil
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, errors.New("candidate workload cannot be read")
		}
		if err := add(raw); err != nil {
			return nil, err
		}
	}
	if len(targets) == 0 {
		return nil, errors.New("candidate workload inventory is empty")
	}
	return targets, nil
}

func deploymentUpgradeSpec(spec map[string]any) (map[string]any, map[string]string, error) {
	raw, _ := json.Marshal(spec)
	var copy map[string]any
	_ = json.Unmarshal(raw, &copy)
	template, ok := copy["template"].(map[string]any)
	if !ok {
		return nil, nil, errors.New("candidate has no complete Pod template")
	}
	pod, ok := template["spec"].(map[string]any)
	if !ok {
		return nil, nil, errors.New("candidate has no complete Pod specification")
	}
	images := map[string]string{}
	for _, group := range []string{"containers", "initContainers"} {
		containers, ok := pod[group].([]any)
		if !ok {
			if group == "initContainers" && pod[group] == nil {
				continue
			}
			return nil, nil, errors.New("candidate container inventory is invalid")
		}
		for _, value := range containers {
			container, ok := value.(map[string]any)
			if !ok {
				return nil, nil, errors.New("candidate container is invalid")
			}
			name, _ := container["name"].(string)
			image, _ := container["image"].(string)
			key := group + "/" + name
			if name == "" || image == "" || images[key] != "" {
				return nil, nil, errors.New("candidate container identity/image is missing or duplicated")
			}
			images[key] = image
			delete(container, "image")
		}
	}
	if len(images) == 0 {
		return nil, nil, errors.New("candidate has no containers")
	}
	return copy, images, nil
}

func validateDeploymentUpgradeTargets(targets, live []deploymentUpgradeTarget, images []string, phase, environment string) error {
	byName := map[string]deploymentUpgradeTarget{}
	for _, target := range live {
		byName[target.Metadata.Namespace+"/"+target.Metadata.Name] = target
	}
	qualified := keySet(images...)
	selected := map[string]bool{}
	for _, target := range targets {
		selected[target.Metadata.Namespace+"/"+target.Metadata.Name] = true
	}
	// One shared package supplies APIs, workers and auxiliary executables. Its
	// affected family must be present; omitting a broken simulator cannot yield GO.
	for _, target := range live {
		if len(targets) == 0 || !deploymentHealthNamespace(environment, target.Metadata.Namespace) {
			continue
		}
		_, liveImages, err := deploymentUpgradeSpec(target.Spec)
		if err != nil {
			return err
		}
		for _, currentImage := range liveImages {
			for _, candidateImage := range images {
				packageName := strings.SplitN(candidateImage, "@", 2)[0]
				if (strings.HasPrefix(currentImage, packageName+"@") || strings.HasPrefix(currentImage, packageName+":")) && !selected[target.Metadata.Namespace+"/"+target.Metadata.Name] {
					return errors.New("candidate inventory omits an affected API, worker or auxiliary workload sharing the selected package")
				}
			}
		}
	}
	for _, target := range targets {
		key := target.Metadata.Namespace + "/" + target.Metadata.Name
		current, exists := byName[key]
		if !exists {
			return errors.New("candidate workload has no existing rollback/configuration owner")
		}
		desiredSpec, desiredImages, err := deploymentUpgradeSpec(target.Spec)
		if err != nil {
			return err
		}
		currentSpec, currentImages, err := deploymentUpgradeSpec(current.Spec)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(desiredSpec, currentSpec) {
			return fmt.Errorf("%s changes nonimage workload configuration; use a separately reviewed configuration/migration operation", key)
		}
		for name, image := range desiredImages {
			if strings.HasPrefix(image, "ghcr.io/hkt999rtk/") && !qualified[image] {
				return errors.New("every selected RTK container image must be explicitly qualified")
			}
			if currentImages[name] != image {
				if !qualified[image] {
					return errors.New("candidate image is absent from the explicitly qualified release images")
				}
				if phase == deploymentCheckPostDeploy {
					return fmt.Errorf("%s has not reached its selected immutable image", key)
				}
			}
		}
	}
	return nil
}

func deploymentUpgradeCommand(ctx context.Context, command string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.WaitDelay = time.Second
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s release prerequisite failed; check authentication, tool availability and deadline", command)
	}
	return raw, nil
}

func verifyDeploymentImageUpgrade(ctx context.Context, cfg deploymentConfig, store secretStore, o deploymentCheckOptions) error {
	targets, err := deploymentUpgradeTargets(o.qualification.manifests, cfg.Environment)
	if err != nil {
		return err
	}
	raw, err := secretCheckKubectl([]*deploymentCheckRuntime{store.checkRuntime}, false, "--kubeconfig", store.KubeconfigPath(), "get", "deployments", "--all-namespaces", "-o", "json")
	if err != nil {
		return secretCheckFailure(err, "cannot read existing workload rollback/configuration inventory")
	}
	var live struct {
		Items []deploymentUpgradeTarget `json:"items"`
	}
	if json.Unmarshal(raw, &live) != nil || live.Items == nil {
		return errors.New("existing workload inventory is invalid")
	}
	if err := validateDeploymentUpgradeTargets(targets, live.Items, o.qualification.images, o.phase, cfg.Environment); err != nil {
		return err
	}
	return verifyDeploymentUpgradeRelease(ctx, cfg.Workspace, store, targets, o.qualification.images)
}

func verifyDeploymentUpgradeRelease(ctx context.Context, workspace string, store secretStore, targets []deploymentUpgradeTarget, images []string) error {
	for _, image := range uniqueNonEmpty(images...) {
		if err := verifyDeploymentUpgradeImageCI(ctx, workspace, image); err != nil {
			return err
		}
	}
	if err := verifyDeploymentUpgradeSchemas(store, workspace, images); err != nil {
		return err
	}
	for _, target := range targets {
		if target.Metadata.Name != "payment-simulator" {
			continue
		}
		container, err := deploymentUpgradeSimulatorContainer(target)
		if err != nil {
			return err
		}
		image, _ := container["image"].(string)
		if err := verifyDeploymentSimulatorMigrationSetting(store, target); err != nil {
			return err
		}
		if err := verifyDeploymentSimulatorStartup(ctx, image); err != nil {
			return err
		}
	}
	return nil
}

func deploymentUpgradeSimulatorContainer(target deploymentUpgradeTarget) (map[string]any, error) {
	pod := target.Spec["template"].(map[string]any)["spec"].(map[string]any)
	var selected map[string]any
	for _, item := range pod["containers"].([]any) {
		container := item.(map[string]any)
		command, _ := container["command"].([]any)
		if len(command) == 0 || command[0] != "/rtk-billing-payment-simulator" {
			continue
		}
		image, _ := container["image"].(string)
		if selected != nil || !strings.HasPrefix(image, "ghcr.io/hkt999rtk/rtk_billing/billing-twd@sha256:") {
			return nil, errors.New("payment simulator executable/image identity is ambiguous or unsupported")
		}
		selected = container
	}
	if selected == nil {
		return nil, errors.New("payment simulator executable/image is not identified")
	}
	return selected, nil
}

func verifyDeploymentSimulatorMigrationSetting(store secretStore, target deploymentUpgradeTarget) error {
	container, err := deploymentUpgradeSimulatorContainer(target)
	if err != nil {
		return err
	}
	// Explicit environment values override all envFrom sources.
	if variables, ok := container["env"].([]any); ok {
		for _, variable := range variables {
			v, ok := variable.(map[string]any)
			if !ok || v["name"] != "BILLING_DB_MIGRATE_ON_STARTUP" {
				continue
			}
			if value, ok := v["value"].(string); ok && strings.TrimSpace(value) == "false" {
				return nil
			}
			return errors.New("simulator migration gate must explicitly disable startup migration with its restricted runtime identity")
		}
	}
	value := ""
	if sources, ok := container["envFrom"].([]any); ok {
		for _, source := range sources {
			binding, ok := source.(map[string]any)
			if !ok {
				return errors.New("simulator environment binding is invalid")
			}
			if prefix, _ := binding["prefix"].(string); prefix != "" {
				return errors.New("prefixed simulator environment requires explicit migration-gate qualification")
			}
			ref, ok := binding["secretRef"].(map[string]any)
			if !ok {
				return errors.New("simulator migration setting has an unsupported environment source")
			}
			name, _ := ref["name"].(string)
			if name == "" {
				return errors.New("simulator environment Secret reference is missing")
			}
			raw, err := secretCheckKubectl([]*deploymentCheckRuntime{store.checkRuntime}, false, "--kubeconfig", store.KubeconfigPath(), "-n", target.Metadata.Namespace, "get", "secret", name, "-o", "json")
			if err != nil {
				return secretCheckFailure(err, "cannot verify simulator runtime migration setting")
			}
			var secret struct {
				Data map[string]string `json:"data"`
			}
			if json.Unmarshal(raw, &secret) != nil || secret.Data == nil {
				return errors.New("simulator runtime Secret evidence is invalid")
			}
			if encoded, exists := secret.Data["BILLING_DB_MIGRATE_ON_STARTUP"]; exists {
				decoded, err := base64.StdEncoding.DecodeString(encoded)
				if err != nil {
					return errors.New("simulator migration setting is invalid")
				}
				value = string(decoded)
			}
		}
	}
	if strings.TrimSpace(value) == "false" {
		return nil
	}
	return errors.New("simulator runtime configuration does not prove startup migration is disabled")
}

func deploymentUpgradeImageSource(image string) (lkeServiceImageSource, error) {
	match := rolloutImagePattern.FindStringSubmatch(image)
	if match != nil {
		for _, source := range lkeServiceImageSources() {
			if match[1] == "hkt999rtk/"+source.RepoName+"/"+source.Name {
				return source, nil
			}
		}
		if match[1] == "hkt999rtk/rtk_video_cloud/video-cloud-emqx-pki" || match[1] == "hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki" {
			for _, source := range lkeServiceImageSources() {
				if source.Key == "video-cloud" {
					return source, nil
				}
			}
		}
	}
	return lkeServiceImageSource{}, errors.New("candidate package has no maintained RTK source/publication mapping")
}

var deploymentUpgradeSHA = regexp.MustCompile(`^[a-f0-9]{40}$`)

func verifyDeploymentUpgradeImageCI(ctx context.Context, workspace, image string) error {
	source, err := deploymentUpgradeImageSource(image)
	if err != nil {
		return err
	}
	shaRaw, err := deploymentUpgradeCommand(ctx, "git", "-C", filepath.Join(workspace, source.RepoPath), "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	sha := strings.TrimSpace(string(shaRaw))
	if !deploymentUpgradeSHA.MatchString(sha) {
		return errors.New("candidate source revision is invalid")
	}
	raw, err := deploymentUpgradeCommand(ctx, "docker", "image", "inspect", image, "--format", "{{json .Config.Labels}}")
	if err != nil {
		return err
	}
	var labels map[string]string
	if json.Unmarshal(raw, &labels) != nil || labels["org.opencontainers.image.revision"] != sha {
		return errors.New("candidate image revision does not match the selected source checkout; pull the exact image and select its source")
	}
	repo := "hkt999rtk/" + source.RepoName
	for _, workflow := range []string{"ci.yml", "release.yml"} {
		raw, err := deploymentUpgradeCommand(ctx, "gh", "api", "repos/"+repo+"/actions/workflows/"+workflow+"/runs?head_sha="+sha+"&per_page=100")
		if err != nil {
			return err
		}
		runID, err := deploymentUpgradeSuccessfulRun(raw, sha)
		if err != nil {
			return fmt.Errorf("%s %s: %w", source.Key, workflow, err)
		}
		if workflow == "release.yml" {
			raw, err = deploymentUpgradeCommand(ctx, "gh", "api", fmt.Sprintf("repos/%s/actions/runs/%d/jobs?per_page=100", repo, runID))
			if err != nil {
				return err
			}
			if err := deploymentUpgradePublishedJob(raw); err != nil {
				return err
			}
		}
	}
	return nil
}

func deploymentUpgradeSuccessfulRun(raw []byte, sha string) (int64, error) {
	var result struct {
		TotalCount int `json:"total_count"`
		Runs       []struct {
			ID         int64  `json:"id"`
			HeadSHA    string `json:"head_sha"`
			HeadBranch string `json:"head_branch"`
			Event      string `json:"event"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"workflow_runs"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Runs == nil || result.TotalCount > len(result.Runs) {
		return 0, errors.New("CI run evidence is malformed or incomplete")
	}
	// GitHub returns newest runs first. Do not borrow an older passing rerun.
	for _, run := range result.Runs {
		if run.HeadSHA != sha || run.HeadBranch != "main" || (run.Event != "push" && run.Event != "workflow_dispatch") {
			continue
		}
		if run.ID <= 0 || run.Status != "completed" || run.Conclusion != "success" {
			return 0, errors.New("exact-source main CI/publication is pending or failed")
		}
		return run.ID, nil
	}
	return 0, errors.New("no successful exact-source main CI/publication evidence")
}

func deploymentUpgradePublishedJob(raw []byte) error {
	var result struct {
		TotalCount int `json:"total_count"`
		Jobs       []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"jobs"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Jobs == nil || result.TotalCount > len(result.Jobs) {
		return errors.New("image publication job evidence is malformed or incomplete")
	}
	for _, job := range result.Jobs {
		if job.Name == "Publish LKE image" && job.Status == "completed" && job.Conclusion == "success" {
			return nil
		}
	}
	return errors.New("formal LKE image publication job has not passed")
}

func verifyDeploymentUpgradeSchemas(store secretStore, workspace string, images []string) error {
	checked := map[string]bool{}
	for _, image := range images {
		source, err := deploymentUpgradeImageSource(image)
		if err != nil {
			return err
		}
		if checked[source.Key] || (source.Key != "account-manager" && source.Key != "billing") {
			continue
		}
		checked[source.Key] = true
		entries, err := os.ReadDir(filepath.Join(workspace, source.RepoPath, "migrations"))
		if err != nil {
			return errors.New("candidate migration inventory cannot be read")
		}
		var versions []string
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
				versions = append(versions, entry.Name())
			}
		}
		if len(versions) == 0 {
			return errors.New("candidate migration inventory is empty")
		}
		sort.Strings(versions)
		var literals []string
		for _, version := range versions {
			literals = append(literals, "('"+strings.ReplaceAll(version, "'", "''")+"')")
		}
		database := "rtk_" + strings.ReplaceAll(source.Key, "-", "_")
		query := "BEGIN TRANSACTION READ ONLY; SELECT COALESCE(json_agg(required.version),'[]'::json) FROM (VALUES " + strings.Join(literals, ",") + ") AS required(version) WHERE NOT EXISTS (SELECT 1 FROM public.schema_migrations applied WHERE applied.version=required.version); COMMIT;"
		raw, err := secretCheckKubectl([]*deploymentCheckRuntime{store.checkRuntime}, false, "--kubeconfig", store.KubeconfigPath(), "-n", "video-cloud-"+store.Environment+"-platform", "exec", "postgresql-0", "--", "psql", "-U", "postgres", "-d", database, "-X", "-qAt", "-v", "ON_ERROR_STOP=1", "-c", query)
		if err != nil {
			return secretCheckFailure(err, "candidate schema prerequisites cannot be verified")
		}
		var missing []string
		if json.Unmarshal(raw, &missing) != nil || missing == nil {
			return errors.New("candidate schema evidence is invalid")
		}
		if len(missing) != 0 {
			return fmt.Errorf("%s candidate has unapplied schema migrations; complete its owner migration before updating dependent Pods", source.Key)
		}
	}
	return nil
}
