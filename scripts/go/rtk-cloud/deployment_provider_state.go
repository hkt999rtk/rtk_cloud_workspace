package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func deploymentLKEStatePath(environment, name string) (string, error) {
	if name != "state.env" && name != "account.env" {
		return "", errors.New("unknown LKE environment state file")
	}
	return privateDeploymentStatePath(environment, filepath.Join("deployment", "adapters", "lke", name))
}

func lkeProviderTarget(paths provisionPaths, values map[string]string) (string, string, error) {
	configured, err := readOptionalStrictEnv(filepath.Join(paths.EnvRoot, "env", "stack.env"))
	if err != nil {
		return "", "", errors.New("LKE target configuration is unavailable")
	}
	environment := firstNonEmpty(values["CLOUD_ENV_NAME"], configured["CLOUD_ENV_NAME"])
	if !secretEnvironmentPattern.MatchString(environment) || configured["CLOUD_ENV_NAME"] != "" && configured["CLOUD_ENV_NAME"] != environment {
		return "", "", errors.New("explicit matching CLOUD_ENV_NAME is required before LKE environment state access")
	}
	stack := firstNonEmpty(values["CLOUD_STACK_NAME"], configured["CLOUD_STACK_NAME"], "video-cloud-"+environment)
	return environment, stack, nil
}

func lkeProviderStateBlocker(cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.provider.state", Cause: cause, Next: "restore the selected environment's private deployment/adapters/lke/state.env; explicitly migrate original legacy state with deployment provider-import-state, then repeat full preflight; do not select another cluster or recreate missing infrastructure"}
}

func validateLKEProviderState(environment, stack string, state map[string]string) error {
	if state["CLOUD_ENV_NAME"] != environment || state["CLOUD_STACK_NAME"] != stack {
		return lkeProviderStateBlocker("provider state is not bound to the selected environment and stack")
	}
	if state["LKE_CLUSTER_ID"] == "" {
		return lkeProviderStateBlocker("provider state has no recorded cluster owner")
	}
	allowed := keySet("CLOUD_ENV_NAME", "CLOUD_STACK_NAME", "LKE_CLUSTER_ID", "LKE_CLUSTER_LABEL", "LKE_CLUSTER_REGION", "LKE_CLUSTER_VERSION", "LKE_POSTGRES_NODE_POOL_ID", "LKE_POSTGRES_NODE_TYPE", "LKE_POSTGRES_NODE_COUNT")
	for key, value := range state {
		if !allowed[key] || strings.ContainsAny(value, "\r\n\x00") || strings.TrimSpace(value) != value || value == "" {
			return lkeProviderStateBlocker("provider state contains an unknown field or invalid single-line value")
		}
		if key == "LKE_CLUSTER_ID" || key == "LKE_POSTGRES_NODE_POOL_ID" {
			if _, err := positiveIntValue(key, value); err != nil {
				return lkeProviderStateBlocker("provider state resource IDs must be positive integers")
			}
		}
		if key == "LKE_POSTGRES_NODE_COUNT" {
			if _, err := nonNegativeIntValue(key, value); err != nil {
				return lkeProviderStateBlocker("provider state node count must be a nonnegative integer")
			}
		}
	}
	return nil
}

func readLKEProviderState(environment, stack string) (map[string]string, error) {
	path, err := deploymentLKEStatePath(environment, "state.env")
	if err != nil {
		return nil, lkeProviderStateBlocker("private provider state path or permissions cannot be verified")
	}
	body, err := readStorageEvidenceFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil || len(body) > 16<<10 {
		return nil, lkeProviderStateBlocker("bounded private provider state cannot be read")
	}
	state, err := parseStrictEnv(body, "LKE provider state")
	if err != nil {
		return nil, lkeProviderStateBlocker("provider state assignments are malformed or duplicated")
	}
	if err := validateLKEProviderState(environment, stack, state); err != nil {
		return nil, err
	}
	return state, nil
}

func writeLKEProviderState(environment, stack string, state map[string]string) error {
	state = appendMap(state, map[string]string{"CLOUD_ENV_NAME": environment, "CLOUD_STACK_NAME": stack})
	if err := validateLKEProviderState(environment, stack, state); err != nil {
		return err
	}
	return writePrivateDeploymentState(environment, filepath.Join("deployment", "adapters", "lke", "state.env"), []byte(sortedEnvContents(state)))
}

func readLKEStateForTarget(paths provisionPaths, values map[string]string) (string, string, map[string]string, error) {
	environment, stack, err := lkeProviderTarget(paths, values)
	if err != nil {
		return "", "", nil, err
	}
	state, err := readLKEProviderState(environment, stack)
	if err != nil {
		return "", "", nil, err
	}
	if len(state) == 0 {
		if envFileValue(filepath.Join(paths.EnvRoot, "env", "stack.env"), "LKE_CLUSTER_ID") != "" {
			return "", "", nil, lkeProviderStateBlocker("legacy stack configuration contains an operational cluster ID; it cannot select infrastructure")
		}
		if _, err := os.Lstat(filepath.Join(paths.EnvRoot, "adapters", "lke", "state.env")); !errors.Is(err, os.ErrNotExist) {
			return "", "", nil, lkeProviderStateBlocker("legacy provider state exists or cannot be inspected; normal operations never read it as a fallback")
		}
	}
	return environment, stack, state, nil
}
