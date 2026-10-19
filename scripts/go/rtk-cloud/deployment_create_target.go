package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Fresh qualification proves absence, rather than deriving it from a missing
// kubeconfig. These reads never allocate resources or restore archived owners.
// Executor capability and the complete provider/bootstrap gates are separate
// requirements; absence alone cannot authorize creation.
func qualifyEnvironmentCreateTargetAbsence(ctx context.Context, cfg deploymentConfig, store secretStore) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cfg.Adapter != "lke" || cfg.Environment != store.Environment || cfg.Values["CLOUD_STACK_NAME"] != "video-cloud-"+cfg.Environment {
		return environmentCreateBlocker("deployment.create.target", "fresh target discovery requires the selected maintained LKE environment and exact stack")
	}
	selected, err := newSecretStore("", cfg.Environment)
	if err != nil || selected.Root != store.Root {
		return environmentCreateBlocker("deployment.create.target", "fresh target discovery requires the selected canonical private environment store")
	}
	if err := checkLocalExecutionFence(store.Root); err != nil {
		return createCheckpointPreflightHint(cfg, store)
	}
	// The native executor publishes new access/state only into absent canonical
	// destinations. Retained owners need their original removal/recovery flow.
	kubeconfig, err := privateDeploymentStatePath(cfg.Environment, "kube/kubeconfig.yaml")
	if err != nil {
		return environmentCreateBlocker("deployment.create.target", "selected canonical Kubernetes identity path cannot be inspected")
	}
	if _, err := os.Lstat(kubeconfig); !errors.Is(err, os.ErrNotExist) {
		return environmentCreateBlocker("deployment.create.target", "canonical Kubernetes identity exists or cannot be inspected; fresh creation cannot overwrite the original generation")
	}
	proof, known, err := readDeploymentRemovalVolumes(cfg)
	if err != nil {
		return environmentCreateBlocker("deployment.create.resources", "original-generation volume ownership is invalid or cannot be inspected")
	}
	if known && proof.ClusterID == "" {
		return environmentCreateBlocker("deployment.create.target", "original-generation ownership has no captured cluster ID; its exact absence cannot be verified for fresh allocation")
	}
	history, err := checkEnvironmentCreateTargetHistory(cfg, store)
	if err != nil {
		return err
	}
	if history && !known {
		return environmentCreateBlocker("deployment.create.resources", "prior create execution exists without original-generation volume ownership; its removal cannot be verified")
	}
	tokenPath, err := privateDeploymentStatePath(cfg.Environment, filepath.Join("operator", "env", "LINODE_TOKEN"))
	var token []byte
	if err == nil {
		token, err = readBoundedEnvironmentFile(tokenPath, true)
	}
	if err != nil || strings.TrimSpace(string(token)) == "" {
		return environmentCreateBlocker("deployment.create.target", "selected canonical provider credentials cannot be read for fresh target discovery")
	}
	input := appendMap(cfg.Values, cfg.AdapterResolved)
	values := appendMap(input, deploymentLegacyLKEValues(input, cfg.Environment))
	values["CLOUD_ENV_NAME"], values["CLOUD_STACK_NAME"] = cfg.Environment, cfg.Values["CLOUD_STACK_NAME"]
	// Inventory discovery must retain the original provider-state guard and
	// ignore selectors inherited from another environment in the caller's shell.
	restore := installAllCredentialEnvironment(map[string]string{"LKE_CLUSTER_ID": values["LKE_CLUSTER_ID"], "LKE_CLUSTER_LABEL": values["LKE_CLUSTER_LABEL"]})
	defer restore()
	cluster, err := discoverDeploymentPreflightLKECreateTarget(ctx, strings.TrimSpace(string(token)), provisionPaths{EnvRoot: cfg.RuntimeRoot}, values)
	if !errors.Is(err, errLKEMissingCluster) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			return environmentCreateBlocker("deployment.create.target", "selected provider cluster already exists (ID="+strconv.Itoa(cluster.ID)+"); fresh creation cannot adopt or replace its owner")
		}
		return environmentCreateBlocker("deployment.create.target", "selected cluster absence cannot be verified from original provider state, access and complete inventory")
	}
	// Label/tag absence alone cannot exclude a renamed original generation.
	// Its captured native ID must independently return an exact HTTP 404.
	if known && proof.ClusterID != "" {
		_, err := environmentCreateProviderRequest(ctx, strings.TrimSpace(string(token)), http.MethodGet, "/lke/clusters/"+proof.ClusterID, nil)
		var absent *environmentCreateProviderHTTPError
		if !errors.As(err, &absent) || absent.Status != http.StatusNotFound {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return environmentCreateBlocker("deployment.create.target", "original-generation cluster remains or cannot be verified absent; retain its original ownership before fresh allocation")
		}
	}
	if err := verifyEnvironmentCreatePriorVolumes(ctx, cfg, store); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return environmentCreateBlocker("deployment.create.resources", "original-generation volumes remain or cannot be verified absent; reconcile their captured native owners before fresh allocation")
	}
	return ctx.Err()
}

// Losing a local fence does not turn an interrupted native allocation into an
// unused environment. Inspect only canonical create journals, never archives or
// other environments, and require every retained attempt to be completed.
func checkEnvironmentCreateTargetHistory(cfg deploymentConfig, store secretStore) (bool, error) {
	base, err := store.safePath("deployment/create/executions")
	if err != nil || base != filepath.Join(store.Root, "deployment", "create", "executions") {
		return false, environmentCreateBlocker("deployment.create.execution", "canonical create execution history cannot be inspected")
	}
	_, err = os.Lstat(base)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || validateEnvironmentPreflightDirectories(store, base) != nil {
		return false, environmentCreateBlocker("deployment.create.execution", "bounded private create execution history cannot be inspected")
	}
	plans, err := os.ReadDir(base)
	if err != nil || len(plans) > 1000 {
		return false, environmentCreateBlocker("deployment.create.execution", "bounded private create execution history cannot be inspected")
	}
	history := false
	for _, plan := range plans {
		parent := filepath.Join(base, plan.Name())
		if !plan.IsDir() || !environmentPreflightDigest.MatchString(plan.Name()) || validateEnvironmentPreflightDirectories(store, parent) != nil {
			return false, environmentCreateBlocker("deployment.create.execution", "create execution history contains an invalid original plan owner")
		}
		attempts, err := os.ReadDir(parent)
		if err != nil || len(attempts) > 1000 {
			return false, environmentCreateBlocker("deployment.create.execution", "bounded original create attempts cannot be inspected")
		}
		for _, attempt := range attempts {
			directory := filepath.Join(parent, attempt.Name())
			if !attempt.IsDir() || !strings.HasPrefix(attempt.Name(), "execution-") || validateEnvironmentPreflightDirectories(store, directory) != nil {
				return false, environmentCreateBlocker("deployment.create.execution", "create execution history contains an invalid original attempt owner")
			}
			var journal environmentCreateJournal
			if readEnvironmentPreflightJSON(filepath.Join(directory, "journal.json"), &journal) != nil || journal.Schema != environmentCreateJournalSchema || journal.Environment != cfg.Environment || journal.Stack != cfg.Values["CLOUD_STACK_NAME"] || journal.PlanSHA256 != plan.Name() || !environmentPreflightDigest.MatchString(journal.ArtifactSHA256) || !environmentPreflightDigest.MatchString(journal.OwnerToken) || journal.AdmittedAt.IsZero() || journal.CompletedAt.IsZero() || journal.CompletedAt.Before(journal.AdmittedAt) || journal.CompletedAt.After(time.Now().UTC()) || journal.Entries == nil {
				return false, environmentCreateBlocker("deployment.create.execution", "original create execution is incomplete or its completed ownership cannot be verified")
			}
			for _, entry := range journal.Entries {
				if entry.State != "completed" || entry.StartedAt.IsZero() || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(journal.CompletedAt) {
					return false, environmentCreateBlocker("deployment.create.execution", "original create stage has an unresolved native outcome; its identity cannot be replaced")
				}
			}
			history = true
		}
	}
	return history, nil
}
