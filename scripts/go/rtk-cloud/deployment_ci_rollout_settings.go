package main

import (
	"errors"
	"path/filepath"
	"strconv"
	"time"
)

// The fast path reads only stack and timeout settings needed for an existing
// dev rollout. It does not qualify storage, provider, PKI or other services.
func resolveCIRolloutSettings(workspace, environment string) (deploymentConfig, time.Duration, time.Duration, error) {
	bad := func() (deploymentConfig, time.Duration, time.Duration, error) {
		return deploymentConfig{}, 0, 0, errors.New("CI rollout: valid dev environment, stack and configured timeouts are required")
	}
	if environment != "dev" || workspace == "" {
		return bad()
	}
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return bad()
	}
	root := filepath.Join(workspace, "cloud_env", environment)
	identity, err := readStrictEnv(filepath.Join(root, "environment.env"))
	if err != nil || !ciRolloutKubernetesName.MatchString(identity["CLOUD_STACK_NAME"]) {
		return bad()
	}
	selection, err := readStrictEnv(filepath.Join(root, "deployment.env"))
	if err != nil || !ciRolloutKubernetesName.MatchString(selection["DEPLOYMENT_ARCHITECTURE"]) {
		return bad()
	}
	values, err := readStrictEnv(filepath.Join(workspace, "cloud_deploy", "architectures", selection["DEPLOYMENT_ARCHITECTURE"], "architecture.env"))
	if err != nil {
		return bad()
	}
	overrides, err := readOptionalStrictEnv(filepath.Join(root, "overrides", "architecture.env"))
	if err != nil {
		return bad()
	}
	for key, value := range overrides {
		values[key] = value
	}
	duration := func(key string) (time.Duration, error) {
		n, err := strconv.ParseInt(values[key], 10, 64)
		if err != nil || n <= 0 || n > int64((1<<63-1)/int64(time.Second)) {
			return 0, errors.New("invalid timeout")
		}
		return time.Duration(n) * time.Second, nil
	}
	rollout, err := duration("DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS")
	if err != nil {
		return bad()
	}
	total, err := duration("DEPLOYMENT_UPGRADE_WINDOW_SECONDS")
	if err != nil {
		return bad()
	}
	return deploymentConfig{Workspace: workspace, Environment: environment, EnvironmentRoot: root, Values: map[string]string{"CLOUD_STACK_NAME": identity["CLOUD_STACK_NAME"]}}, rollout, total, nil
}
