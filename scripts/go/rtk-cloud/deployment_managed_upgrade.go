package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

const managedUpgradePolicy = "replace-managed-workloads"

// A private desired-state plan is both the environment-local overwrite policy
// and the input to the full upgrade. It never contains CA replacement authority.
type managedUpgradePlan struct {
	Version     int                      `json:"schema_version"`
	Environment string                   `json:"environment"`
	Stack       string                   `json:"stack"`
	Policy      string                   `json:"policy"`
	Images      []string                 `json:"images"`
	Workloads   []managedUpgradeWorkload `json:"workloads"`
	Retained    []managedUpgradeWorkload `json:"retained"`
	digest      string
}

type managedUpgradeWorkload struct {
	UID      string                  `json:"uid"`
	Baseline map[string]any          `json:"baseline_spec"`
	Desired  deploymentUpgradeTarget `json:"desired"`
}

func managedUpgradePath(store secretStore) string {
	return filepath.Join(store.Root, "deployment", "managed-upgrade.json")
}

func loadManagedUpgradePlan(store secretStore, stack string) (*managedUpgradePlan, error) {
	path := managedUpgradePath(store)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("cannot inspect managed upgrade plan")
	}
	if store.Environment != "dev" && store.Environment != "staging" {
		return nil, errors.New("managed workload overwrite policy is permitted only for dev and staging")
	}
	for _, ancestor := range []string{store.ConfigRoot, store.Root} {
		if ancestor == "" {
			continue
		}
		info, err := os.Lstat(ancestor)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("managed upgrade requires private real SecretStore directories")
		}
	}
	dir, err := os.Lstat(filepath.Dir(path))
	if err != nil || !dir.IsDir() || dir.Mode()&os.ModeSymlink != 0 || dir.Mode().Perm()&0o077 != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("managed upgrade plan requires a private regular file (0600) and directory (0700), without symlinks")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("cannot read managed upgrade plan")
	}
	var plan managedUpgradePlan
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&plan) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("managed upgrade plan JSON is invalid")
	}
	if plan.Version != 1 || plan.Environment != store.Environment || plan.Stack != stack || plan.Policy != managedUpgradePolicy || len(plan.Workloads) == 0 {
		return nil, errors.New("managed upgrade plan requires schema 1, exact environment/stack, replace-managed-workloads policy and a complete inventory")
	}
	plan.digest = managedUpgradeDigest(raw)
	return &plan, nil
}

func managedUpgradeDigest(value any) string {
	raw, ok := value.([]byte)
	if !ok {
		raw, _ = json.Marshal(value)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func managedUpgradeSelected(opts provisionOptions) bool {
	return len(opts.workloads) == 0 && !opts.videoOnly && !opts.loggerOnly
}

func managedUpgradeStore(env map[string]string) (secretStore, *managedUpgradePlan, error) {
	if rtkCloudTestMode() && strings.TrimSpace(os.Getenv("RTK_CLOUD_CONFIG_ROOT")) == "" {
		return secretStore{}, nil, nil
	}
	environment := firstNonEmpty(env["CLOUD_ENV_NAME"], strings.TrimPrefix(env["CLOUD_STACK_NAME"], "video-cloud-"))
	if environment == "" {
		return secretStore{}, nil, nil
	}
	store, err := newSecretStore("", environment)
	if err != nil {
		return store, nil, err
	}
	plan, err := loadManagedUpgradePlan(store, env["CLOUD_STACK_NAME"])
	if err == nil && env["RTK_MANAGED_UPGRADE_PLAN_SHA256"] != "" && (plan == nil || env["RTK_MANAGED_UPGRADE_PLAN_SHA256"] != plan.digest) {
		err = errors.New("managed upgrade plan changed after qualification; repeat preflight before any deployment")
	}
	return store, plan, err
}

func managedUpgradeInventory(ctx context.Context, store secretStore) ([]map[string]any, error) {
	raw, err := secretCheckKubectl([]*deploymentCheckRuntime{newDeploymentCheckRuntime(ctx, store.Environment)}, false, "--kubeconfig", store.KubeconfigPath(), "get", "deployments,statefulsets,daemonsets", "--all-namespaces", "-o", "json")
	if err != nil {
		return nil, secretCheckFailure(err, "cannot read managed upgrade inventory")
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if json.Unmarshal(raw, &list) != nil || list.Items == nil {
		return nil, errors.New("managed upgrade inventory is invalid")
	}
	var result []map[string]any
	for _, item := range list.Items {
		metadata := certIssuerObjectMap(item["metadata"])
		if deploymentHealthNamespace(store.Environment, certIssuerObjectString(metadata["namespace"])) {
			result = append(result, item)
		}
	}
	return result, nil
}

// PKI/credential plumbing retains the independently managed identity lifecycle.
// All other declared workload settings (including replicas, resources, probes
// and ordinary literal environment settings) can be overwritten by the plan.
func managedUpgradeIdentitySpec(spec map[string]any) map[string]any {
	raw, _ := json.Marshal(spec)
	var copy map[string]any
	_ = json.Unmarshal(raw, &copy)
	pod := certIssuerObjectMap(certIssuerObjectMap(copy["template"])["spec"])
	identity := map[string]any{"selector": copy["selector"]}
	for _, field := range []string{"volumes", "securityContext", "serviceAccountName", "automountServiceAccountToken", "imagePullSecrets"} {
		identity[field] = pod[field]
	}
	for _, group := range []string{"containers", "initContainers"} {
		var containers []any
		for _, value := range certIssuerObjectList(pod[group]) {
			container := certIssuerObjectMap(value)
			entry := map[string]any{}
			for _, field := range []string{"name", "command", "args", "ports", "volumeMounts", "securityContext", "envFrom"} {
				entry[field] = container[field]
			}
			var settings []any
			for _, value := range certIssuerObjectList(container["env"]) {
				setting := certIssuerObjectMap(value)
				name := strings.ToUpper(certIssuerObjectString(setting["name"]))
				protected := setting["valueFrom"] != nil
				for _, token := range []string{"PKI", "IDENTITY", "TLS", "CERT", "CRL", "DATABASE", "DB_", "DSN", "TOKEN", "SECRET", "PASSWORD", "OPENBAO", "REGISTR", "CA_", "MIGRAT", "ENABLED", "ROOT"} {
					protected = protected || strings.Contains(name, token)
				}
				if protected {
					settings = append(settings, setting)
				}
			}
			entry["env"] = settings
			containers = append(containers, entry)
		}
		identity[group] = containers
	}
	return identity
}

func validateManagedUpgradeInventory(plan *managedUpgradePlan, live []map[string]any) error {
	if len(plan.Workloads) == 0 {
		return errors.New("managed full upgrade requires existing environment Deployments")
	}
	current := map[string]map[string]any{}
	for _, item := range live {
		m := certIssuerObjectMap(item["metadata"])
		key := certIssuerObjectString(item["kind"]) + "/" + certIssuerObjectString(m["namespace"]) + "/" + certIssuerObjectString(m["name"])
		if current[key] != nil {
			return errors.New("duplicate live managed upgrade owner")
		}
		current[key] = item
	}
	selected := map[string]bool{}
	usedImages := map[string]bool{}
	qualified := keySet(plan.Images...)
	for _, image := range plan.Images {
		if !rolloutImagePattern.MatchString(image) {
			return errors.New("managed upgrade requires exact immutable GHCR images")
		}
	}
	for index, group := range [][]managedUpgradeWorkload{plan.Workloads, plan.Retained} {
		for _, workload := range group {
			desired := workload.Desired
			key := desired.Kind + "/" + desired.Metadata.Namespace + "/" + desired.Metadata.Name
			item := current[key]
			if !deploymentHealthNamespace(plan.Environment, desired.Metadata.Namespace) || item == nil || selected[key] || workload.UID == "" || workload.Baseline == nil || desired.Spec == nil {
				return errors.New("managed upgrade plan has a missing, duplicate or foreign workload owner")
			}
			selected[key] = true
			metadata := certIssuerObjectMap(item["metadata"])
			labels := certIssuerObjectMap(metadata["labels"])
			if certIssuerObjectString(metadata["uid"]) != workload.UID || metadata["deletionTimestamp"] != nil || (labels["rtk.realtek.com/stack"] != nil && labels["rtk.realtek.com/stack"] != plan.Stack) {
				return fmt.Errorf("%s ownership changed; prepare a new reviewed upgrade plan", key)
			}
			spec := certIssuerObjectMap(item["spec"])
			if !reflect.DeepEqual(spec, workload.Baseline) && !reflect.DeepEqual(spec, desired.Spec) {
				return fmt.Errorf("%s configuration changed since planning; repeat planning and qualification", key)
			}
			if index == 1 {
				if desired.Kind != "StatefulSet" && desired.Kind != "DaemonSet" {
					return errors.New("retained inventory must contain StatefulSets and DaemonSets")
				}
				if !reflect.DeepEqual(desired.Spec, workload.Baseline) {
					return errors.New("database, broker and provider controller changes require their own migration; retained specs must be unchanged")
				}
				continue
			}
			if desired.Kind != "Deployment" {
				return errors.New("managed workload replacement requires complete Deployments")
			}
			if !reflect.DeepEqual(managedUpgradeIdentitySpec(desired.Spec), managedUpgradeIdentitySpec(workload.Baseline)) {
				return fmt.Errorf("%s changes identity, credential, feature activation or bootstrap plumbing; complete its managed lifecycle migration before planning the full upgrade", key)
			}
			_, images, err := deploymentUpgradeSpec(desired.Spec)
			if err != nil {
				return err
			}
			_, baselineImages, err := deploymentUpgradeSpec(workload.Baseline)
			if err != nil {
				return err
			}
			for name, image := range images {
				usedImages[image] = true
				if (strings.HasPrefix(image, "ghcr.io/hkt999rtk/") || image != baselineImages[name]) && !qualified[image] {
					return errors.New("every RTK or changed container image must be explicitly qualified in the managed upgrade plan")
				}
			}
		}
	}
	for _, image := range plan.Images {
		if !usedImages[image] {
			return errors.New("managed upgrade qualified image has no selected workload consumer")
		}
	}
	if len(selected) != len(current) {
		return errors.New("managed upgrade plan omits environment Deployments or retained StatefulSet/DaemonSet dependencies; it cannot qualify a full upgrade")
	}
	return nil
}

func qualifyManagedUpgrade(ctx context.Context, paths provisionPaths, env map[string]string, store secretStore, plan *managedUpgradePlan) error {
	store.checkRuntime = newDeploymentCheckRuntime(ctx, store.Environment)
	operator, err := store.readOperator()
	if err != nil {
		return err
	}
	operator["RTK_CLOUD_KUBECONFIG"] = store.KubeconfigPath()
	operator["RTK_CLOUD_LKE_KUBECONFIG"] = store.KubeconfigPath()
	restore := installAllCredentialEnvironment(operator)
	defer restore()
	live, err := managedUpgradeInventory(ctx, store)
	if err != nil {
		return err
	}
	if err := validateManagedUpgradeInventory(plan, live); err != nil {
		return err
	}
	for _, w := range plan.Workloads {
		raw, _ := json.Marshal(w.Desired)
		if check := checkRolloutMountsJSON(raw, "managed candidate", false); !check.Passed {
			return errors.New(check.Detail)
		}
	}
	for _, check := range []func() error{
		func() error { return verifySecretStoreK8SBindings(store) },
		func() error { return verifySecretStoreK8SRuntime(store, time.Now()) },
		func() error { return verifyDeploymentWorkloadHealth(store) },
	} {
		if err := check(); err != nil {
			return err
		}
	}
	cfg := deploymentConfig{Environment: store.Environment, Workspace: paths.Workspace, Values: env}
	if err := verifyDeploymentPublicCertIssuer(ctx, cfg, store); err != nil {
		return err
	}
	if _, err := lkePlanCertIssuerIngressMigrationWithIO(paths, env, newCertIssuerIngressIO(ctx)); err != nil {
		return err
	}
	if err := managedUpgradeDryRun(ctx, store, plan, live); err != nil {
		return err
	}
	checker := defaultDeploymentCredentialChecker()
	checker.ctx = ctx
	for _, image := range uniqueNonEmpty(plan.Images...) {
		if check := checker.checkRolloutImage(operator, image); !check.Passed {
			return errors.New(check.Detail)
		}
		if err := verifyDeploymentUpgradeImageCI(ctx, paths.Workspace, image); err != nil {
			return err
		}
	}
	if err := verifyDeploymentUpgradeSchemas(store, paths.Workspace, plan.Images); err != nil {
		return err
	}
	// Qualify published-image startup and effective restricted migration settings
	// for the simulator as in the exact-image path, without imposing image-only
	// equality on the complete managed desired state.
	for _, w := range plan.Workloads {
		if w.Desired.Metadata.Name == "payment-simulator" {
			if err := verifyDeploymentSimulatorMigrationSetting(store, w.Desired); err != nil {
				return err
			}
			container, err := deploymentUpgradeSimulatorContainer(w.Desired)
			if err != nil {
				return err
			}
			if err := verifyDeploymentSimulatorStartup(ctx, certIssuerObjectString(container["image"])); err != nil {
				return err
			}
		}
	}
	return nil
}

func prepareManagedUpgrade(cfg deploymentConfig, images []string, out io.Writer) error {
	if cfg.Adapter != "lke" || (cfg.Environment != "dev" && cfg.Environment != "staging") {
		return errors.New("prepare-upgrade supports existing dev/staging LKE environments only")
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	for _, image := range images {
		if !rolloutImagePattern.MatchString(image) {
			return errors.New("prepare-upgrade --image requires a GHCR @sha256 reference")
		}
	}
	byPackage := map[string]string{}
	for _, image := range images {
		name := strings.SplitN(image, "@", 2)[0]
		if byPackage[name] != "" && byPackage[name] != image {
			return errors.New("prepare-upgrade has conflicting images for one package")
		}
		byPackage[name] = image
	}
	live, err := managedUpgradeInventory(context.Background(), store)
	if err != nil {
		return err
	}
	plan := managedUpgradePlan{Version: 1, Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], Policy: managedUpgradePolicy}
	selected := map[string]bool{}
	for _, item := range live {
		metadata := certIssuerObjectMap(item["metadata"])
		raw, _ := json.Marshal(item)
		var desired deploymentUpgradeTarget
		_ = json.Unmarshal(raw, &desired)
		baselineRaw, _ := json.Marshal(desired.Spec)
		var baseline map[string]any
		_ = json.Unmarshal(baselineRaw, &baseline)
		w := managedUpgradeWorkload{UID: certIssuerObjectString(metadata["uid"]), Baseline: baseline, Desired: desired}
		if desired.Kind != "Deployment" {
			plan.Retained = append(plan.Retained, w)
			continue
		}
		pod := certIssuerObjectMap(certIssuerObjectMap(desired.Spec["template"])["spec"])
		for _, group := range []string{"containers", "initContainers"} {
			for _, value := range certIssuerObjectList(pod[group]) {
				container := certIssuerObjectMap(value)
				image := certIssuerObjectString(container["image"])
				name := strings.SplitN(strings.SplitN(image, "@", 2)[0], ":", 2)[0]
				if replacement := byPackage[name]; replacement != "" {
					image = replacement
					container["image"] = image
					selected[name] = true
				}
				if strings.HasPrefix(image, "ghcr.io/hkt999rtk/") || byPackage[name] != "" {
					if !rolloutImagePattern.MatchString(image) {
						return errors.New("existing RTK image is mutable; supply its immutable --image before preparing the full plan")
					}
					plan.Images = append(plan.Images, image)
				}
			}
		}
		plan.Workloads = append(plan.Workloads, w)
	}
	for name := range byPackage {
		if !selected[name] {
			return errors.New("prepare-upgrade image has no existing Deployment consumer")
		}
	}
	plan.Images = uniqueNonEmpty(plan.Images...)
	sort.Strings(plan.Images)
	if err := validateManagedUpgradeInventory(&plan, live); err != nil {
		return err
	}
	path := managedUpgradePath(store)
	if err := ensurePrivateDirectory(store.ConfigRoot); err != nil {
		return err
	}
	if err := ensurePrivateDirectory(store.Root); err != nil {
		return err
	}
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errors.New("cannot create private managed upgrade directory")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("managed upgrade plan already exists or cannot be created; archive the reviewed previous plan before preparing another")
	}
	raw, _ := json.MarshalIndent(plan, "", "  ")
	_, writeErr := file.Write(raw)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return errors.New("cannot persist managed upgrade plan")
	}
	fmt.Fprintf(out, "Prepared private full managed workload plan: %s\nReview desired specs, then run the default preflight; no cloud resources changed and readiness is not yet qualified.\n", path)
	return nil
}

func managedUpgradeReplacement(w managedUpgradeWorkload, current map[string]any) (map[string]any, error) {
	metadata := certIssuerObjectMap(current["metadata"])
	if metadata["uid"] != w.UID || certIssuerObjectString(metadata["resourceVersion"]) == "" {
		return nil, errors.New("managed workload API identity changed or is missing")
	}
	raw, _ := json.Marshal(current)
	var replacement map[string]any
	_ = json.Unmarshal(raw, &replacement)
	replacement["spec"] = w.Desired.Spec
	delete(replacement, "status")
	return replacement, nil
}

func managedUpgradeDryRun(ctx context.Context, store secretStore, plan *managedUpgradePlan, live []map[string]any) error {
	byName := map[string]map[string]any{}
	for _, item := range live {
		m := certIssuerObjectMap(item["metadata"])
		byName[certIssuerObjectString(m["namespace"])+"/"+certIssuerObjectString(m["name"])] = item
	}
	var items []any
	for _, w := range plan.Workloads {
		replacement, err := managedUpgradeReplacement(w, byName[w.Desired.Metadata.Namespace+"/"+w.Desired.Metadata.Name])
		if err != nil {
			return err
		}
		items = append(items, replacement)
	}
	raw, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "List", "items": items})
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, lkeKubectl(), "--kubeconfig", store.KubeconfigPath(), "--request-timeout=20s", "replace", "--dry-run=server", "-f", "-")
	cmd.WaitDelay = time.Second
	cmd.Stdin = bytes.NewReader(raw)
	if err := cmd.Run(); err != nil {
		return errors.New("managed upgrade server dry-run failed; resolve Kubernetes update permission, immutable fields, admission or quota before deployment")
	}
	return nil
}

func deployManagedUpgrade(paths provisionPaths, env map[string]string, store secretStore, plan *managedUpgradePlan) error {
	return deployManagedUpgradeWithQualification(paths, env, store, plan, qualifyManagedUpgrade)
}

func deployManagedUpgradeWithQualification(paths provisionPaths, env map[string]string, store secretStore, plan *managedUpgradePlan, qualify func(context.Context, provisionPaths, map[string]string, secretStore, *managedUpgradePlan) error) error {
	if err := qualify(context.Background(), paths, env, store, plan); err != nil {
		return err
	}
	inventory, err := managedUpgradeInventory(context.Background(), store)
	if err != nil {
		return err
	}
	if err := validateManagedUpgradeInventory(plan, inventory); err != nil {
		return err
	}
	// Save complete rollback specs privately before the first workload mutation.
	backupDir := filepath.Join(store.Root, "deployment", "rollback", plan.digest)
	for _, directory := range []string{filepath.Join(store.Root, "deployment"), filepath.Dir(backupDir), backupDir} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return errors.New("cannot create private managed upgrade rollback directory")
		}
	}
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return errors.New("cannot create managed upgrade rollback directory")
	}
	raw, _ := json.Marshal(map[string]any{"plan_sha256": plan.digest, "items": inventory})
	backupPath := filepath.Join(backupDir, "workloads.json")
	file, err := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, err = file.Write(raw)
		err = errors.Join(err, file.Close())
	} else if errors.Is(err, os.ErrExist) {
		err = nil
	}
	if err != nil {
		return errors.New("cannot persist managed upgrade rollback specs")
	}
	for _, w := range plan.Workloads {
		if _, currentPlan, err := managedUpgradeStore(env); err != nil || currentPlan == nil || currentPlan.digest != plan.digest {
			return errors.New("managed upgrade plan changed during rollout; stop and requalify")
		}
		current, err := kubectlResourceJSON(w.Desired.Metadata.Namespace, "deployment", w.Desired.Metadata.Name)
		if err != nil {
			return errors.New("cannot read managed workload before replacement")
		}
		// Kubernetes resourceVersion supplies a second concurrency guard at write.
		local := *plan
		local.Workloads = []managedUpgradeWorkload{w}
		local.Retained = nil
		if err := validateManagedUpgradeInventory(&local, []map[string]any{current}); err != nil {
			return err
		}
		replacement, err := managedUpgradeReplacement(w, current)
		if err != nil {
			return err
		}
		body, _ := json.Marshal(replacement)
		if _, err := kubectlCombinedOutput(bytes.NewReader(body), "replace", "-f", "-"); err != nil {
			return fmt.Errorf("managed workload replacement failed for %s/%s; inspect private rollback specs before retrying", w.Desired.Metadata.Namespace, w.Desired.Metadata.Name)
		}
	}
	for _, w := range plan.Workloads {
		if err := runKubectl("-n", w.Desired.Metadata.Namespace, "rollout", "status", "deployment/"+w.Desired.Metadata.Name, "--timeout=300s"); err != nil {
			return errors.New("managed workload rollout is incomplete; run environment health and inspect private rollback specs")
		}
	}
	return nil
}

func verifyManagedUpgradePostDeploy(paths provisionPaths, env map[string]string) error {
	store, plan, err := managedUpgradeStore(env)
	if err != nil {
		return err
	}
	if plan == nil {
		return errors.New("qualified managed upgrade plan is missing")
	}
	store.checkRuntime = newDeploymentCheckRuntime(context.Background(), store.Environment)
	live, err := managedUpgradeInventory(context.Background(), store)
	if err != nil {
		return err
	}
	if err := validateManagedUpgradeInventory(plan, live); err != nil {
		return err
	}
	byName := map[string]map[string]any{}
	for _, item := range live {
		m := certIssuerObjectMap(item["metadata"])
		byName[certIssuerObjectString(m["namespace"])+"/"+certIssuerObjectString(m["name"])] = item
	}
	for _, w := range plan.Workloads {
		if !reflect.DeepEqual(certIssuerObjectMap(byName[w.Desired.Metadata.Namespace+"/"+w.Desired.Metadata.Name]["spec"]), w.Desired.Spec) {
			return errors.New("managed upgrade has not reached every desired workload specification")
		}
	}
	for _, check := range []func() error{func() error { return verifySecretStoreK8SBindings(store) }, func() error { return verifySecretStoreK8SRuntime(store, time.Now()) }, func() error { return verifyDeploymentWorkloadHealth(store) }} {
		if err := check(); err != nil {
			return err
		}
	}
	return verifyDeploymentPublicCertIssuer(context.Background(), deploymentConfig{Environment: store.Environment, Workspace: paths.Workspace, Values: env}, store)
}
