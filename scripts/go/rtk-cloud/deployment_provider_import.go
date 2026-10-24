package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func runDeploymentProviderImport(args []string) error {
	fs := flag.NewFlagSet("deployment provider-import-state", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	environment := fs.String("environment", "", "selected maintained environment")
	workspace := fs.String("workspace", "", "workspace root")
	root := fs.String("environment-root", "", "maintained environment root")
	source := fs.String("source-runtime", "", "absolute original runtime; never searched automatically")
	kube := fs.String("source-kubeconfig", "", "explicit original Kubernetes identity; defaults to the canonical environment kubeconfig")
	confirm := fs.String("confirm", "", "exact selected stack")
	if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return exitCode(2)
	}
	if fs.NArg() != 0 || !secretEnvironmentPattern.MatchString(*environment) || !filepath.IsAbs(*source) || *kube != "" && !filepath.IsAbs(*kube) {
		fmt.Fprintln(os.Stderr, "error: valid --environment and absolute original source paths are required")
		return exitCode(2)
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, *root)
	if err != nil || cfg.Adapter != "lke" || *confirm != cfg.Values["CLOUD_STACK_NAME"] {
		fmt.Fprintln(os.Stderr, "error: maintained LKE configuration and exact --confirm stack are required")
		return exitCode(2)
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return exitCode(2)
	}
	if *kube == "" {
		*kube = store.KubeconfigPath()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	count, err := importDeploymentProviderState(ctx, cfg, *source, *kube)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: original provider state or ownership could not be verified; retain original files and inspect the selected identity, provider access and private destination; existing state is never overwritten")
		return exitCode(1)
	}
	fmt.Fprintf(os.Stdout, "IMPORTED environment=%s files=%d location=~/.config/rtk_cloud/%s/deployment/adapters/lke\n", cfg.Environment, count, cfg.Environment)
	fmt.Fprintln(os.Stdout, "Original provider IDs and source bytes are retained. No cloud resources or Kubernetes credentials were changed. Regenerate configuration and repeat full preflight; import is not GO.")
	return nil
}

// Import is an explicit baseline migration, not a desired-spec renderer or a
// completion receipt. Native provider reads bind recorded IDs to the original
// selected API identity; quota remains the original operator-confirmed profile.
func importDeploymentProviderState(ctx context.Context, cfg deploymentConfig, sourceRuntime, kubeconfig string) (int, error) {
	configurationPath := filepath.Join(sourceRuntime, "env", "stack.env")
	configurationRaw, err := os.ReadFile(configurationPath)
	if err != nil {
		return 0, errors.New("original runtime configuration is unavailable")
	}
	configured, err := parseStrictEnv(configurationRaw, "original runtime configuration")
	if err != nil || configured["CLOUD_ENV_NAME"] != cfg.Environment || configured["CLOUD_STACK_NAME"] != "" && configured["CLOUD_STACK_NAME"] != cfg.Values["CLOUD_STACK_NAME"] {
		return 0, errors.New("original runtime environment and stack must match the selected configuration")
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return 0, err
	}
	if _, err := os.Lstat(filepath.Join(store.Root, "deployment", ".execution-active")); !errors.Is(err, os.ErrNotExist) {
		return 0, errors.New("provider migration cannot run while an execution fence exists or cannot be inspected")
	}
	originals, outputs := map[string][]byte{}, map[string][]byte{}
	for _, name := range []string{"state.env", "account.env"} {
		path := filepath.Join(sourceRuntime, "adapters", "lke", name)
		raw, err := readStorageEvidenceFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || len(raw) > 16<<10 {
			return 0, errors.New("bounded original private provider file required")
		}
		originals[name] = raw
		values, err := parseStrictEnv(raw, "original provider state")
		if err != nil {
			return 0, errors.New("original provider assignments are malformed or duplicated")
		}
		if name == "account.env" {
			if len(values) != 1 || validateActiveServiceLimit(values["LKE_ACTIVE_SERVICE_LIMIT"]) != nil {
				return 0, errors.New("original account quota profile is invalid or contains unrelated fields")
			}
			outputs[name] = raw
			continue
		}
		// Environment metadata is added from the verified target, never from
		// a different source owner. All allocated IDs remain unchanged.
		if values["CLOUD_ENV_NAME"] != "" && values["CLOUD_ENV_NAME"] != cfg.Environment || values["CLOUD_STACK_NAME"] != "" && values["CLOUD_STACK_NAME"] != cfg.Values["CLOUD_STACK_NAME"] {
			return 0, errors.New("original provider state belongs to another environment")
		}
		values["CLOUD_ENV_NAME"], values["CLOUD_STACK_NAME"] = cfg.Environment, cfg.Values["CLOUD_STACK_NAME"]
		if err := validateLKEProviderState(cfg.Environment, cfg.Values["CLOUD_STACK_NAME"], values); err != nil {
			return 0, err
		}
		if err := verifyImportedLKEOwner(ctx, cfg, store, values, kubeconfig); err != nil {
			return 0, err
		}
		outputs[name] = []byte(sortedEnvContents(values))
	}
	if len(originals) == 0 {
		return 0, errors.New("explicit original provider source has no supported state")
	}
	if current, err := os.ReadFile(configurationPath); err != nil || !bytes.Equal(current, configurationRaw) {
		return 0, errors.New("original runtime configuration changed during ownership verification")
	}
	// Validate all destinations and original bytes before the first publication.
	for name, raw := range outputs {
		path, err := deploymentLKEStatePath(cfg.Environment, name)
		if err != nil {
			return 0, err
		}
		if existing, err := readStorageEvidenceFile(path); err == nil {
			if !bytes.Equal(existing, raw) {
				return 0, errors.New("existing private provider state conflicts with the original source")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		current, err := readStorageEvidenceFile(filepath.Join(sourceRuntime, "adapters", "lke", name))
		if err != nil || !bytes.Equal(current, originals[name]) {
			return 0, errors.New("original provider source changed during verification")
		}
	}
	archive := filepath.Join("migration-backup", "provider-state", deploymentplan.Digest(originals))
	for _, name := range []string{"state.env", "account.env"} {
		if raw := originals[name]; raw != nil {
			path, err := privateDeploymentStatePath(cfg.Environment, filepath.Join(archive, name))
			if err != nil {
				return 0, err
			}
			if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
				return 0, err
			}
			if err := publishProviderImportFile(path, raw); err != nil {
				return 0, err
			}
		}
	}
	for _, name := range []string{"state.env", "account.env"} {
		if raw := outputs[name]; raw != nil {
			path, err := deploymentLKEStatePath(cfg.Environment, name)
			if err != nil {
				return 0, err
			}
			if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
				return 0, err
			}
			if err := publishProviderImportFile(path, raw); err != nil {
				return 0, err
			}
		}
	}
	return len(outputs), nil
}

func publishProviderImportFile(path string, raw []byte) error {
	if existing, err := readStorageEvidenceFile(path); err == nil {
		if bytes.Equal(existing, raw) {
			return nil
		}
		return errors.New("provider import refuses to overwrite a concurrent destination")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Existing exclusive hard-link publication preserves concurrent writers.
	return publishOriginalStorageEvidence(path, raw)
}

func verifyImportedLKEOwner(ctx context.Context, cfg deploymentConfig, store secretStore, state map[string]string, kubeconfig string) error {
	operator, err := store.readOperator()
	if err != nil || operator["LINODE_TOKEN"] == "" {
		return errors.New("canonical environment provider credentials required for ownership verification")
	}
	id := state["LKE_CLUSTER_ID"]
	raw, err := deploymentPreflightLKERead(ctx, operator["LINODE_TOKEN"], "/lke/clusters/"+id)
	var cluster lkeCluster
	if err != nil || json.Unmarshal(raw, &cluster) != nil || strconv.Itoa(cluster.ID) != id || cluster.Label == "" || cluster.Region != cfg.AdapterResolved["LKE_REGION"] {
		return errors.New("original cluster ID and selected provider region cannot be verified")
	}
	if state["LKE_CLUSTER_LABEL"] != "" && state["LKE_CLUSTER_LABEL"] != cluster.Label || state["LKE_CLUSTER_REGION"] != "" && state["LKE_CLUSTER_REGION"] != cluster.Region {
		return errors.New("original provider owner metadata changed; explicit review is required")
	}
	originalKube, err := readStorageEvidenceFile(kubeconfig)
	if err != nil {
		return errors.New("original private selected Kubernetes identity required")
	}
	raw, err = deploymentPreflightLKERead(ctx, operator["LINODE_TOKEN"], "/lke/clusters/"+id+"/kubeconfig")
	var response struct {
		Kubeconfig string `json:"kubeconfig"`
	}
	if err != nil || json.Unmarshal(raw, &response) != nil {
		return errors.New("native provider Kubernetes identity cannot be read")
	}
	nativeKube, err := base64.StdEncoding.DecodeString(response.Kubeconfig)
	if err != nil {
		return errors.New("native provider Kubernetes identity cannot be decoded")
	}
	a, errA := providerKubernetesIdentity(originalKube, cfg.Environment)
	b, errB := providerKubernetesIdentity(nativeKube, cfg.Environment)
	if errA != nil || errB != nil || a != b {
		return errors.New("provider cluster differs from the original selected Kubernetes API identity")
	}
	if poolID := state["LKE_POSTGRES_NODE_POOL_ID"]; poolID != "" {
		raw, err = deploymentPreflightLKERead(ctx, operator["LINODE_TOKEN"], "/lke/clusters/"+id+"/pools")
		var pools struct {
			Data []lkeNodePool `json:"data"`
		}
		if err != nil || json.Unmarshal(raw, &pools) != nil {
			return errors.New("recorded node-pool owner cannot be verified")
		}
		matched := 0
		for _, pool := range pools.Data {
			if strconv.Itoa(pool.ID) == poolID {
				if state["LKE_POSTGRES_NODE_TYPE"] != "" && state["LKE_POSTGRES_NODE_TYPE"] != pool.Type || state["LKE_POSTGRES_NODE_COUNT"] != "" && state["LKE_POSTGRES_NODE_COUNT"] != strconv.Itoa(pool.Count) {
					return errors.New("recorded node-pool baseline changed")
				}
				matched++
			}
		}
		if matched != 1 {
			return errors.New("recorded node-pool owner is absent or ambiguous")
		}
	}
	if current, err := readStorageEvidenceFile(kubeconfig); err != nil || !bytes.Equal(current, originalKube) {
		return errors.New("original Kubernetes credentials changed during ownership verification")
	}
	return nil
}

// Only public API/CA identity is compared. Provider retrieval may return a
// different admin token or alias; neither is installed or used for mutation.
func providerKubernetesIdentity(raw []byte, environment string) (string, error) {
	backend, err := newEnvironmentKubernetesBackend(string(raw), environment)
	if err != nil {
		return "", err
	}
	var selected struct {
		Clusters []struct {
			Cluster map[string]any `yaml:"cluster"`
		} `yaml:"clusters"`
	}
	if yaml.Unmarshal(backend.KubeconfigContent, &selected) != nil || len(selected.Clusters) != 1 {
		return "", errors.New("selected public Kubernetes identity unavailable")
	}
	cluster := selected.Clusters[0].Cluster
	return deploymentplan.Digest(map[string]any{"server": cluster["server"], "certificate-authority-data": cluster["certificate-authority-data"], "tls-server-name": cluster["tls-server-name"]}), nil
}
