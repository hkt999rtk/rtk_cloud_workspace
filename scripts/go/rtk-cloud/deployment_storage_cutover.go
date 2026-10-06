package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func (c deploymentCredentialChecker) cutoverRuntimeStorage(cfg deploymentConfig, values map[string]string, sourceFile, environmentFile string) error {
	if sourceFile == "" {
		return errors.New("--source-env-file is required for media storage-cutover")
	}
	if err := c.validateMediaMigrationCutover(cfg, values, sourceFile); err != nil {
		return err
	}
	if err := materializeDeploymentRuntime(cfg); err != nil {
		return err
	}
	receipt, err := readDeploymentStorageReceipt(cfg)
	if err != nil {
		return err
	}
	target := cfg.Storage.RuntimeMedia
	target.Endpoint = receipt.Endpoint
	access := firstNonEmpty(values["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"], values["LINODE_OBJ_ACCESS_KEY_ID"])
	secret := firstNonEmpty(values["LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY"], values["LINODE_OBJ_SECRET_ACCESS_KEY"])
	destination := provisionObjectStore{bucket: target.Bucket, endpoint: target.Endpoint, region: target.Region, accessKey: access, secretKey: secret}
	if err := c.validateClipStorageSmoke(destination, target.Prefix); err != nil {
		return err
	}
	sourceValues, check := deploymentCredentialValues(sourceFile)
	if !check.Passed {
		return errors.New(check.Detail)
	}
	source, err := provisionObjectStoreFromEnv(sourceValues)
	if err != nil {
		return err
	}
	restore, err := storageCutoverKubeconfig(cfg.Environment)
	if err != nil {
		return err
	}
	defer restore()
	stack, err := readEnvFile(filepath.Join(cfg.RuntimeRoot, "env", "stack.env"))
	if err != nil {
		return err
	}
	inventory, err := kubectlCombinedOutput(nil, "get", "deployments,statefulsets,daemonsets,jobs,cronjobs,replicasets,pods", "-A", "-o", "json")
	if err != nil {
		return fmt.Errorf("inspect all storage consumers: %w", err)
	}
	mutations, snapshots, err := planMediaStorageConsumers(inventory, stack["CLOUD_STACK_NAME"], source, target, access, secret)
	if err != nil {
		return err
	}
	if len(mutations) == 0 {
		return errors.New("no verified source-bucket consumers found; refuse to record a cutover")
	}
	// Revalidate after inventory, immediately before any workload mutation.
	if err := c.validateMediaMigrationCutover(cfg, values, sourceFile); err != nil {
		return err
	}
	store, journal, err := beginStorageCutover(cfg, "media", sourceFile, environmentFile, mutations, snapshots)
	if err != nil {
		return err
	}
	state := map[string]any{"environment": cfg.Environment, "bucket": target.Bucket, "region": target.Region, "prefix": target.Prefix, "rollback_credentials_retained": true, "clip_storage_smoke": "pass"}
	return completeStorageCutover(cfg, environmentFile, "media", store, &journal, state, func() error {
		if err := waitMediaStorageConsumers(journal, stack["CLOUD_STACK_NAME"], source, target, access, secret, func() ([]byte, error) {
			return kubectlCombinedOutput(nil, "get", "deployments,statefulsets,daemonsets,jobs,cronjobs,replicasets,pods", "-A", "-o", "json")
		}); err != nil {
			return err
		}
		// Waiting may outlast a concurrent credential or workload edit. Recheck
		// every journaled destination before recording completion or promotion.
		return verifyStorageCutover(journal)
	})
}

func (c deploymentCredentialChecker) cutoverOTAStorage(cfg deploymentConfig, values map[string]string, sourceFile, environmentFile string) error {
	if err := lkeValidateOTACDNBaseURL(cfg.Values); err != nil {
		return err
	}
	if check := c.checkResolvedOTAStorage(cfg, values); !check.Passed {
		return errors.New(check.Detail)
	}
	metricsEndpoint := ""
	if cfg.Values["VIDEO_CLOUD_OTA_CDN_BASE_URL"] == "" {
		bucket, err := c.resolveStorageBucket(values["LINODE_TOKEN"], cfg.Storage.OTAFirmware)
		if err != nil {
			return err
		}
		if err := validateOTADirectMetricsEndpoint(bucket); err != nil {
			return err
		}
		metricsEndpoint, err = normalizeLinodeS3Endpoint(bucket.S3Endpoint)
		if err != nil {
			return err
		}
	}
	if err := materializeDeploymentRuntime(cfg); err != nil {
		return err
	}
	stack, err := readEnvFile(filepath.Join(cfg.RuntimeRoot, "env", "stack.env"))
	if err != nil {
		return err
	}
	if !lkeOTAServiceRegistrationEnabled(stack) {
		return errors.New("OTA service registration is disabled; dedicated bucket is prepared but no workload can be cut over")
	}
	if metricsEndpoint != "" {
		if err := validateOTAMetricsQualification(cfg.RuntimeRoot, cfg.Environment, cfg.Storage.OTAFirmware.Bucket, cfg.Storage.OTAFirmware.Region, metricsEndpoint, time.Now().UTC()); err != nil {
			return err
		}
	}
	restoreKubeconfig, err := storageCutoverKubeconfig(cfg.Environment)
	if err != nil {
		return err
	}
	defer restoreKubeconfig()
	if err := ensureOTACutoverSource(lkeNamespaceName(stack, "video-cloud"), sourceFile, cfg.Storage.OTAFirmware.Prefix); err != nil {
		return err
	}
	if err := c.validateOTAMigrationCutover(cfg, values, sourceFile); err != nil {
		return err
	}
	restore := installDeploymentChildCredentialEnvironment(values)
	defer restore()
	if err := lkeRequireOTAServiceInputs(stack); err != nil {
		return err
	}
	mutations, err := planStorageCutoverManifests([]string{
		lkeAllowVideoCloudAPIOTAGatewayNetworkPolicyManifest(stack),
		lkeOTAStorageSecretManifest(stack),
		lkeOTAServiceDeploymentManifest(stack),
		lkeOTAServiceServiceManifest(stack),
	})
	if err != nil {
		return err
	}
	store, journal, err := beginStorageCutover(cfg, "ota", sourceFile, environmentFile, mutations, nil)
	if err != nil {
		return err
	}
	return completeStorageCutover(cfg, environmentFile, "ota", store, &journal, map[string]any{
		"environment": cfg.Environment, "bucket": cfg.Storage.OTAFirmware.Bucket,
		"region": cfg.Storage.OTAFirmware.Region, "prefix": cfg.Storage.OTAFirmware.Prefix,
		"rollback_credentials_retained": true, "service_ready": true,
	}, func() error { return lkeRequireReadyOTAServiceEndpoint(stack) })
}

// Storage cutover journals contain Secret data and live environment snapshots.
// They belong in the private SecretStore, never in runtime receipts or logs.
type storageCutoverMutation struct {
	Kind            string         `json:"kind"`
	Namespace       string         `json:"namespace"`
	Name            string         `json:"name"`
	UID             string         `json:"uid,omitempty"`
	ResourceVersion string         `json:"resource_version,omitempty"`
	Before          map[string]any `json:"before,omitempty"`
	After           map[string]any `json:"after"`
	Create          map[string]any `json:"create,omitempty"`
	Attempted       bool           `json:"attempted"`
}

type storageCutoverJournal struct {
	Operation          string                        `json:"operation,omitempty"`
	Reinitialization   *storageReinitializationProof `json:"reinitialization,omitempty"`
	SourceFile         string                        `json:"source_profile"`
	SourceAccessSHA256 string                        `json:"source_access_sha256"`
	DestinationFile    string                        `json:"destination_profile"`
	MigrationSHA256    string                        `json:"migration_receipt_sha256"`
	Environment        string                        `json:"environment"`
	Purpose            string                        `json:"purpose"`
	ClusterUID         string                        `json:"cluster_uid"`
	ID                 string                        `json:"id"`
	Status             string                        `json:"status"`
	Mutations          []storageCutoverMutation      `json:"mutations"`
	SourceSecrets      []map[string]any              `json:"source_secrets,omitempty"`
}

func storageCutoverMap(value any) map[string]any { m, _ := value.(map[string]any); return m }
func storageCutoverString(value any) string      { s, _ := value.(string); return s }
func storageCutoverClone(value any) any {
	b, _ := json.Marshal(value)
	var v any
	_ = json.Unmarshal(b, &v)
	return v
}

func storageCutoverGet(object map[string]any, path string) any {
	var current any = object
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch v := current.(type) {
		case map[string]any:
			current = v[part]
		case []any:
			var i int
			if _, err := fmt.Sscanf(part, "%d", &i); err != nil || i < 0 || i >= len(v) {
				return nil
			}
			current = v[i]
		default:
			return nil
		}
	}
	return current
}

func storageCutoverRead(kind, namespace, name string) (map[string]any, error) {
	args := []string{"get", kind, name, "--ignore-not-found=true", "-o", "json"}
	if namespace != "" {
		args = append([]string{"-n", namespace}, args...)
	}
	b, err := kubectlCombinedOutput(nil, args...)
	if err != nil {
		return nil, fmt.Errorf("read %s %s/%s: %w", kind, namespace, name, err)
	}
	if strings.TrimSpace(string(b)) == "" {
		return nil, nil
	}
	var object map[string]any
	if json.Unmarshal(b, &object) != nil || storageCutoverGet(object, "/metadata/name") != name {
		return nil, fmt.Errorf("invalid %s %s/%s inventory", kind, namespace, name)
	}
	return object, nil
}

func storageCutoverFieldsMatch(object map[string]any, fields map[string]any) bool {
	for path, want := range fields {
		if !reflect.DeepEqual(storageCutoverGet(object, path), want) {
			return false
		}
	}
	return true
}

func storageCutoverMutationFor(object map[string]any, fields map[string]any) (storageCutoverMutation, error) {
	m := storageCutoverMutation{Kind: storageCutoverString(object["kind"]), Namespace: storageCutoverString(storageCutoverGet(object, "/metadata/namespace")), Name: storageCutoverString(storageCutoverGet(object, "/metadata/name")), UID: storageCutoverString(storageCutoverGet(object, "/metadata/uid")), ResourceVersion: storageCutoverString(storageCutoverGet(object, "/metadata/resourceVersion")), Before: map[string]any{}, After: fields}
	if m.Kind == "" || m.Namespace == "" || m.Name == "" || m.UID == "" || m.ResourceVersion == "" {
		return m, errors.New("storage consumer inventory lacks kind, namespace, name, UID or resourceVersion")
	}
	for path := range fields {
		m.Before[path] = storageCutoverClone(storageCutoverGet(object, path))
	}
	return m, nil
}

func storageCutoverCAS(m storageCutoverMutation, reverse bool) error {
	live, err := storageCutoverRead(m.Kind, m.Namespace, m.Name)
	if err != nil {
		return err
	}
	if live == nil || storageCutoverGet(live, "/metadata/uid") != m.UID {
		return fmt.Errorf("%s %s/%s identity changed", m.Kind, m.Namespace, m.Name)
	}
	before, after := m.Before, m.After
	if reverse {
		before, after = after, before
	}
	if !storageCutoverFieldsMatch(live, before) {
		return fmt.Errorf("%s %s/%s storage fields changed; refusing overwrite", m.Kind, m.Namespace, m.Name)
	}
	if !reverse && storageCutoverGet(live, "/metadata/resourceVersion") != m.ResourceVersion {
		return fmt.Errorf("%s %s/%s resourceVersion changed; refresh the cutover plan", m.Kind, m.Namespace, m.Name)
	}
	ops := []map[string]any{{"op": "test", "path": "/metadata/uid", "value": m.UID}, {"op": "test", "path": "/metadata/resourceVersion", "value": storageCutoverGet(live, "/metadata/resourceVersion")}}
	paths := make([]string, 0, len(after))
	for path := range after {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if before[path] != nil {
			ops = append(ops, map[string]any{"op": "test", "path": path, "value": before[path]})
		}
		if after[path] == nil {
			ops = append(ops, map[string]any{"op": "remove", "path": path})
		} else {
			ops = append(ops, map[string]any{"op": "add", "path": path, "value": after[path]})
		}
	}
	body, _ := json.Marshal(ops)
	// Keep Secret values out of argv, kubectl diagnostics and stdout.
	if _, err := kubectlCombinedOutput(strings.NewReader(string(body)), "-n", m.Namespace, "patch", m.Kind, m.Name, "--type=json", "--patch-file=/dev/stdin", "-o", "json"); err != nil {
		return fmt.Errorf("conditional update of %s %s/%s failed: %w", m.Kind, m.Namespace, m.Name, err)
	}
	return nil
}

func storageCutoverJournalName(purpose string) string {
	return "migration-backup/storage-cutover-" + purpose + ".json"
}
func saveStorageCutoverJournal(store secretStore, journal storageCutoverJournal, replace bool) error {
	b, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	if replace {
		return store.write(storageCutoverJournalName(journal.Purpose), append(b, '\n'), true)
	}
	path, err := store.safePath(storageCutoverJournalName(journal.Purpose))
	if err != nil {
		return err
	}
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(b, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func beginStorageCutover(cfg deploymentConfig, purpose, sourceFile, environmentFile string, mutations []storageCutoverMutation, secrets []map[string]any) (secretStore, storageCutoverJournal, error) {
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return store, storageCutoverJournal{}, err
	}
	migration, err := readDeploymentStorageState(cfg.Environment, storageMigrationReceiptName(purpose))
	if err != nil {
		return store, storageCutoverJournal{}, err
	}
	journal := storageCutoverJournal{SourceFile: sourceFile, DestinationFile: environmentFile, MigrationSHA256: fmt.Sprintf("%x", sha256.Sum256(migration)), Environment: cfg.Environment, Purpose: purpose, ID: fmt.Sprint(time.Now().UTC().UnixNano()), Status: "prepared", Mutations: mutations, SourceSecrets: secrets}
	sourceValues, check := deploymentCredentialValues(sourceFile)
	if !check.Passed {
		return store, journal, errors.New("preserved source profile is required for the cutover journal")
	}
	source, err := provisionObjectStoreFromEnv(sourceValues)
	if err != nil {
		return store, journal, err
	}
	journal.SourceAccessSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(source.accessKey)))
	cluster, err := storageCutoverRead("namespace", "", "kube-system")
	if err != nil {
		return store, journal, err
	}
	journal.ClusterUID = storageCutoverString(storageCutoverGet(cluster, "/metadata/uid"))
	if journal.ClusterUID == "" {
		return store, journal, errors.New("cannot identify Kubernetes cluster for storage cutover")
	}
	if err := saveStorageCutoverJournal(store, journal, false); err != nil {
		return store, journal, fmt.Errorf("preserve existing cutover journal before starting another cutover: %w", err)
	}
	return store, journal, nil
}

func applyStorageCutover(store secretStore, journal *storageCutoverJournal) error {
	for i := range journal.Mutations {
		m := &journal.Mutations[i]
		m.Attempted = true
		if err := saveStorageCutoverJournal(store, *journal, true); err != nil {
			return err
		}
		if m.Create != nil {
			metadata := storageCutoverMap(m.Create["metadata"])
			annotations := storageCutoverMap(metadata["annotations"])
			if annotations == nil {
				annotations = map[string]any{}
				metadata["annotations"] = annotations
			}
			annotations["rtk.realtek.com/storage-cutover"] = journal.ID
			b, _ := json.Marshal(m.Create)
			if _, err := kubectlCombinedOutput(strings.NewReader(string(b)), "create", "-f", "-", "-o", "json"); err != nil {
				return fmt.Errorf("create %s %s/%s failed: %w", m.Kind, m.Namespace, m.Name, err)
			}
		} else if err := storageCutoverCAS(*m, false); err != nil {
			return err
		}
		live, err := storageCutoverRead(m.Kind, m.Namespace, m.Name)
		if err != nil {
			return err
		}
		if live == nil {
			return errors.New("storage cutover resource disappeared after update")
		}
		if m.Create == nil && (storageCutoverGet(live, "/metadata/uid") != m.UID || !storageCutoverFieldsMatch(live, m.After)) {
			return fmt.Errorf("%s %s/%s does not match requested storage settings", m.Kind, m.Namespace, m.Name)
		}
		if m.Create != nil {
			for path, wanted := range m.After {
				if !storageCutoverContains(storageCutoverGet(live, path), wanted) {
					return fmt.Errorf("created %s %s/%s changed requested storage settings", m.Kind, m.Namespace, m.Name)
				}
			}
		}
		m.UID = storageCutoverString(storageCutoverGet(live, "/metadata/uid"))
		for path := range m.After {
			m.After[path] = storageCutoverClone(storageCutoverGet(live, path))
		}
		if err := saveStorageCutoverJournal(store, *journal, true); err != nil {
			return err
		}
	}
	journal.Status = "applied"
	return saveStorageCutoverJournal(store, *journal, true)
}

func verifyStorageCutover(journal storageCutoverJournal) error {
	for _, m := range journal.Mutations {
		switch m.Kind {
		case "Deployment", "StatefulSet", "DaemonSet":
			if err := runKubectl("-n", m.Namespace, "rollout", "status", strings.ToLower(m.Kind)+"/"+m.Name, "--timeout", firstNonEmpty(os.Getenv("LKE_ROLLOUT_TIMEOUT"), "5m")); err != nil {
				return err
			}
		}
		live, err := storageCutoverRead(m.Kind, m.Namespace, m.Name)
		if err != nil {
			return err
		}
		if live == nil || storageCutoverGet(live, "/metadata/uid") != m.UID || !storageCutoverFieldsMatch(live, m.After) {
			return fmt.Errorf("%s %s/%s differs after rollout", m.Kind, m.Namespace, m.Name)
		}
	}
	return nil
}

func rollbackStorageJournal(store secretStore, journal *storageCutoverJournal) error {
	cluster, err := storageCutoverRead("namespace", "", "kube-system")
	if err != nil {
		return err
	}
	if storageCutoverGet(cluster, "/metadata/uid") != journal.ClusterUID {
		return errors.New("rollback cluster does not match cutover journal")
	}
	for _, secret := range journal.SourceSecrets {
		mutated := false
		for _, m := range journal.Mutations {
			if m.Kind == "Secret" && m.Namespace == storageCutoverGet(secret, "/metadata/namespace") && m.Name == storageCutoverGet(secret, "/metadata/name") {
				mutated = true
				break
			}
		}
		if mutated {
			continue
		}
		live, err := storageCutoverRead("Secret", storageCutoverString(storageCutoverGet(secret, "/metadata/namespace")), storageCutoverString(storageCutoverGet(secret, "/metadata/name")))
		if err != nil {
			return err
		}
		if live == nil || storageCutoverGet(live, "/metadata/uid") != storageCutoverGet(secret, "/metadata/uid") || !reflect.DeepEqual(live["data"], secret["data"]) {
			return errors.New("source credential Secret changed; review its saved snapshot before rollback")
		}
	}
	// Detect known drift across the whole rollback before restoring a shared
	// credential. Otherwise an edited workload could keep destination settings
	// after its Secret had already been switched back to source credentials.
	for _, m := range journal.Mutations {
		if !m.Attempted {
			continue
		}
		live, err := storageCutoverRead(m.Kind, m.Namespace, m.Name)
		if err != nil {
			return err
		}
		if m.Create != nil && live == nil {
			continue
		}
		if m.UID == "" || live == nil || storageCutoverGet(live, "/metadata/uid") != m.UID {
			return fmt.Errorf("rollback resource %s %s/%s identity changed or creation outcome is unknown", m.Kind, m.Namespace, m.Name)
		}
		if m.Create != nil {
			if storageCutoverGet(live, "/metadata/annotations/rtk.realtek.com~1storage-cutover") != journal.ID || !storageCutoverFieldsMatch(live, m.After) {
				return errors.New("created storage resource changed before rollback; refusing deletion")
			}
		} else if !storageCutoverFieldsMatch(live, m.Before) && !storageCutoverFieldsMatch(live, m.After) {
			return errors.New("storage resource changed before rollback; refusing credential or workload changes")
		}
	}
	// Restore pre-existing credentials before waiting for any old workload.
	for _, m := range journal.Mutations {
		if !m.Attempted || m.Kind != "Secret" || m.Create != nil {
			continue
		}
		live, err := storageCutoverRead(m.Kind, m.Namespace, m.Name)
		if err != nil {
			return err
		}
		if live != nil && storageCutoverGet(live, "/metadata/uid") == m.UID && storageCutoverFieldsMatch(live, m.Before) {
			continue
		}
		if err := storageCutoverCAS(m, true); err != nil {
			return err
		}
	}
	for i := len(journal.Mutations) - 1; i >= 0; i-- {
		m := journal.Mutations[i]
		if !m.Attempted {
			continue
		}
		live, err := storageCutoverRead(m.Kind, m.Namespace, m.Name)
		if err != nil {
			return err
		}
		if m.Create != nil {
			if live == nil {
				continue
			}
			if storageCutoverGet(live, "/metadata/annotations/rtk.realtek.com~1storage-cutover") != journal.ID || (m.UID != "" && storageCutoverGet(live, "/metadata/uid") != m.UID) {
				return errors.New("created storage resource no longer belongs to this cutover")
			}
			if m.UID == "" {
				return errors.New("creation outcome was not recorded; review the live resource before rollback")
			}
			if !storageCutoverFieldsMatch(live, m.After) {
				return errors.New("created storage resource changed after cutover; refusing deletion")
			}
			plural := map[string]string{"Secret": "secrets", "Deployment": "deployments", "Service": "services", "NetworkPolicy": "networkpolicies"}[m.Kind]
			version := storageCutoverString(live["apiVersion"])
			base := "/apis/" + version
			if version == "v1" {
				base = "/api/v1"
			}
			if plural == "" {
				return errors.New("unsupported created-resource rollback")
			}
			options, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "propagationPolicy": "Foreground", "preconditions": map[string]any{"uid": storageCutoverGet(live, "/metadata/uid"), "resourceVersion": storageCutoverGet(live, "/metadata/resourceVersion")}})
			if _, err := kubectlCombinedOutput(strings.NewReader(string(options)), "delete", "--raw="+base+"/namespaces/"+m.Namespace+"/"+plural+"/"+m.Name, "-f", "-"); err != nil {
				return fmt.Errorf("conditional rollback deletion failed: %w", err)
			}
			if err := runKubectl("-n", m.Namespace, "wait", "--for=delete", strings.ToLower(m.Kind)+"/"+m.Name, "--timeout", firstNonEmpty(os.Getenv("LKE_ROLLOUT_TIMEOUT"), "5m")); err != nil {
				return err
			}
			if remaining, err := storageCutoverRead(m.Kind, m.Namespace, m.Name); err != nil || remaining != nil {
				return errors.New("created storage resource remains after rollback deletion; verify its identity before retrying")
			}
		} else {
			if live != nil && storageCutoverGet(live, "/metadata/uid") == m.UID && storageCutoverFieldsMatch(live, m.Before) {
				continue
			}
			if err := storageCutoverCAS(m, true); err != nil {
				return err
			}
			switch m.Kind {
			case "Deployment", "StatefulSet", "DaemonSet":
				if err := runKubectl("-n", m.Namespace, "rollout", "status", strings.ToLower(m.Kind)+"/"+m.Name, "--timeout", firstNonEmpty(os.Getenv("LKE_ROLLOUT_TIMEOUT"), "5m")); err != nil {
					return err
				}
			}
		}
	}
	journal.Status = "rolled-back"
	return saveStorageCutoverJournal(store, *journal, true)
}

func rollbackStorageCutover(cfg deploymentConfig, purpose string) error {
	if purpose != "media" && purpose != "ota" {
		return errors.New("storage rollback purpose must be media or ota")
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	raw, err := store.read(storageCutoverJournalName(purpose))
	if err != nil {
		return err
	}
	var journal storageCutoverJournal
	if json.Unmarshal([]byte(raw), &journal) != nil || journal.Environment != cfg.Environment || journal.Purpose != purpose {
		return errors.New("storage rollback journal identity does not match")
	}
	restore, err := storageCutoverKubeconfig(cfg.Environment)
	if err != nil {
		return err
	}
	defer restore()
	if err := validateStorageRollbackData(cfg, journal); err != nil {
		return err
	}
	if err := rollbackStorageJournal(store, &journal); err != nil {
		return err
	}
	name := "storage-cutover.json"
	if purpose == "ota" {
		name = "storage-cutover-ota.json"
	}
	path, err := deploymentStorageStatePath(cfg.Environment, name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintln(os.Stderr, "Storage workload settings restored. Keep writers fenced until destination writes/deletions are reconciled back and the active credential profile is restored.")
	return nil
}

func storageCutoverKubeconfig(environment string) (func(), error) {
	store, err := newSecretStore("", environment)
	if err != nil {
		return nil, err
	}
	path := store.KubeconfigPath()
	if _, err := os.Stat(path); err != nil {
		return nil, errors.New("environment kubeconfig is required for storage cutover")
	}
	names := []string{"RTK_CLOUD_KUBECONFIG", "KUBECONFIG", "RTK_CLOUD_LKE_KUBECONFIG"}
	previous := map[string]string{}
	present := map[string]bool{}
	for _, name := range names {
		previous[name], present[name] = os.LookupEnv(name)
		_ = os.Setenv(name, path)
	}
	return func() {
		for _, name := range names {
			if present[name] {
				_ = os.Setenv(name, previous[name])
			} else {
				_ = os.Unsetenv(name)
			}
		}
	}, nil
}

func storageCutoverPodPath(kind string) string {
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet", "Job":
		return "/spec/template/spec"
	case "CronJob":
		return "/spec/jobTemplate/spec/template/spec"
	}
	return ""
}

func storageCutoverFinishedJob(object map[string]any) bool {
	conditions, _ := storageCutoverGet(object, "/status/conditions").([]any)
	for _, v := range conditions {
		c := storageCutoverMap(v)
		if (c["type"] == "Complete" || c["type"] == "Failed") && c["status"] == "True" {
			return true
		}
	}
	return false
}

type storageCutoverDrainingPods struct {
	owners map[string]string
}

func (e *storageCutoverDrainingPods) Error() string {
	return "managed source-bound Pods are still terminating; keep writers fenced until they disappear"
}

func waitMediaStorageConsumers(journal storageCutoverJournal, stack string, source provisionObjectStore, target deploymentStorageTarget, access, secret string, inspect func() ([]byte, error)) error {
	deadline := time.Now().Add(envDurationDefault("LKE_ROLLOUT_TIMEOUT", 5*time.Minute))
	var previous map[string]string
	for {
		body, err := inspect()
		if err != nil {
			return err
		}
		_, _, err = planMediaStorageConsumers(body, stack, source, target, access, secret, journal)
		var pending *storageCutoverDrainingPods
		if !errors.As(err, &pending) {
			return err
		}
		if previous != nil {
			for uid, owner := range pending.owners {
				if previous[uid] != owner {
					return errors.New("source-bound Pod appeared or changed ownership while waiting for termination")
				}
			}
		}
		previous = pending.owners
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("timed out waiting for source-bound Pods to terminate: %w", pending)
		}
		time.Sleep(min(2*time.Second, remaining))
	}
}

func planMediaStorageConsumers(body []byte, stack string, source provisionObjectStore, target deploymentStorageTarget, access, secret string, afterRollout ...storageCutoverJournal) ([]storageCutoverMutation, []map[string]any, error) {
	return planStorageConsumers(body, stack, source, target, access, secret, "media", afterRollout...)
}

func planStorageConsumers(body []byte, stack string, source provisionObjectStore, target deploymentStorageTarget, access, secret, credentialPurpose string, afterRollout ...storageCutoverJournal) ([]storageCutoverMutation, []map[string]any, error) {
	secretName := "rtk-storage-" + credentialPurpose
	if access == "" || secret == "" {
		return nil, nil, errors.New("destination storage credentials are missing")
	}
	var inventory struct {
		Items []map[string]any `json:"items"`
	}
	if json.Unmarshal(body, &inventory) != nil {
		return nil, nil, errors.New("invalid storage consumer inventory")
	}
	mutations := []storageCutoverMutation{}
	sourceSecrets := []map[string]any{}
	seenSecrets := map[string]bool{}
	namespaces := map[string]bool{}
	references := map[string]map[string]any{}
	plannedUIDs := map[string]bool{}
	owners := map[string]string{}
	activePods := map[string]string{}
	objects := map[string]map[string]any{}
	for _, object := range inventory.Items {
		uid := storageCutoverString(storageCutoverGet(object, "/metadata/uid"))
		owners[uid] = storageCutoverControllerUID(object)
		objects[uid] = object
	}
	if len(afterRollout) > 0 {
		for _, m := range afterRollout[0].Mutations {
			if storageCutoverPodPath(m.Kind) == "" {
				continue
			}
			live := objects[m.UID]
			if m.UID == "" || live == nil || live["kind"] != m.Kind || storageCutoverGet(live, "/metadata/namespace") != m.Namespace || storageCutoverGet(live, "/metadata/name") != m.Name || !storageCutoverFieldsMatch(live, m.After) {
				return nil, nil, fmt.Errorf("%s %s/%s differs while waiting for source Pods", m.Kind, m.Namespace, m.Name)
			}
			plannedUIDs[m.UID] = true
		}
	}
	for _, object := range inventory.Items {
		kind := storageCutoverString(object["kind"])
		ns := storageCutoverString(storageCutoverGet(object, "/metadata/namespace"))
		name := storageCutoverString(storageCutoverGet(object, "/metadata/name"))
		if kind == "ReplicaSet" {
			// Old revision templates are not active consumers; their Pods below
			// must resolve to an inventoried controller selected for this cutover.
			continue
		}
		podPath := storageCutoverPodPath(kind)
		if kind == "Pod" {
			phase := storageCutoverGet(object, "/status/phase")
			if phase == "Succeeded" || phase == "Failed" {
				continue
			}
			podPath = "/spec"
		}
		if podPath == "" {
			return nil, nil, fmt.Errorf("unsupported storage inventory kind %s", kind)
		}
		if kind == "Job" && storageCutoverFinishedJob(object) {
			continue
		}
		fields := map[string]any{}
		for _, containerType := range []string{"containers", "initContainers", "ephemeralContainers"} {
			containers, _ := storageCutoverGet(object, podPath+"/"+containerType).([]any)
			for index, value := range containers {
				container := storageCutoverMap(value)
				effective, indirect, err := storageCutoverEffectiveStorageEnv(container, ns, references)
				if err != nil {
					return nil, nil, fmt.Errorf("inspect %s/%s storage configuration: %w", ns, name, err)
				}
				oldEnv, _ := container["env"].([]any)
				env, _ := storageCutoverClone(oldEnv).([]any)
				names := map[string]int{}
				for i, v := range env {
					entry := storageCutoverMap(v)
					key := storageCutoverString(entry["name"])
					if _, found := names[key]; found {
						return nil, nil, fmt.Errorf("duplicate env name in %s/%s", ns, name)
					}
					names[key] = i
				}
				matched := false
				for _, prefix := range []string{"VIDEO_CLOUD_BLOB_", "VIDEO_CLOUD_OTA_BLOB_", "LINODE_OBJ_"} {
					if effective[prefix+"BUCKET"] != source.bucket {
						continue
					}
					if kind == "Pod" {
						if len(afterRollout) > 0 {
							p := strings.Trim(effective[prefix+"PREFIX"], "/")
							endpointMatches := strings.TrimRight(effective[prefix+"ENDPOINT"], "/") == strings.TrimRight(source.endpoint, "/") || (source.reinitializeEmptyEndpoint && effective[prefix+"ENDPOINT"] == "" && !indirect[prefix+"ENDPOINT"])
							if !strings.HasPrefix(ns, stack+"-") || !endpointMatches || effective[prefix+"REGION"] != source.region || (source.prefixSet && p != source.prefix) || (!source.prefixSet && p != "" && p != target.Prefix) {
								return nil, nil, fmt.Errorf("source-bound Pod %s/%s storage binding differs from verified migration source", ns, name)
							}
						}
						activePods[ns+"/"+name] = storageCutoverString(storageCutoverGet(object, "/metadata/uid"))
						continue
					}
					if len(afterRollout) > 0 {
						return nil, nil, fmt.Errorf("source-bucket consumer %s/%s appeared during cutover", ns, name)
					}
					if indirect[prefix+"BUCKET"] {
						return nil, nil, fmt.Errorf("%s/%s has indirect source bucket configuration; explicit consumer mapping required", ns, name)
					}
					if !strings.HasPrefix(ns, stack+"-") {
						return nil, nil, fmt.Errorf("source bucket consumer %s/%s is outside stack %s", ns, name, stack)
					}
					endpointIndex, ok := names[prefix+"ENDPOINT"]
					if !ok && source.reinitializeEmptyEndpoint && !indirect[prefix+"ENDPOINT"] {
						endpointIndex = len(env)
						names[prefix+"ENDPOINT"] = endpointIndex
						env = append(env, map[string]any{"name": prefix + "ENDPOINT", "value": ""})
						ok = true
					}
					if !ok {
						return nil, nil, fmt.Errorf("%s/%s source endpoint is not explicit", ns, name)
					}
					liveEndpoint := storageCutoverMap(env[endpointIndex])
					endpointValue := storageCutoverString(liveEndpoint["value"])
					if liveEndpoint["valueFrom"] != nil || (strings.TrimRight(endpointValue, "/") != strings.TrimRight(source.endpoint, "/") && !(source.reinitializeEmptyEndpoint && endpointValue == "")) {
						return nil, nil, fmt.Errorf("%s/%s bucket matches but endpoint differs", ns, name)
					}
					prefixIndex, ok := names[prefix+"PREFIX"]
					if !ok {
						return nil, nil, fmt.Errorf("%s/%s source prefix is not explicit", ns, name)
					}
					livePrefix := storageCutoverMap(env[prefixIndex])
					p := strings.Trim(storageCutoverString(livePrefix["value"]), "/")
					if livePrefix["valueFrom"] != nil || (source.prefixSet && p != source.prefix) || (!source.prefixSet && p != "" && p != target.Prefix) {
						return nil, nil, fmt.Errorf("%s/%s prefix differs from verified migration source", ns, name)
					}
					regionIndex, ok := names[prefix+"REGION"]
					if !ok || storageCutoverMap(env[regionIndex])["valueFrom"] != nil || storageCutoverMap(env[regionIndex])["value"] != source.region {
						return nil, nil, fmt.Errorf("%s/%s source region differs from migration", ns, name)
					}
					for suffix, next := range map[string]string{"BUCKET": target.Bucket, "ENDPOINT": target.Endpoint, "REGION": target.Region, "PREFIX": target.Prefix} {
						storageCutoverMap(env[names[prefix+suffix]])["value"] = next
					}
					matched = true
				}
				if !matched {
					continue
				}
				for _, prefix := range []string{"VIDEO_CLOUD_BLOB_", "VIDEO_CLOUD_OTA_BLOB_", "LINODE_OBJ_"} {
					if other := effective[prefix+"BUCKET"]; other != "" && other != source.bucket {
						return nil, nil, fmt.Errorf("%s/%s shares container credentials with another bucket; explicit credential separation required", ns, name)
					}
				}
				if kind == "Job" {
					return nil, nil, fmt.Errorf("unfinished Job %s/%s still consumes source storage; finish or recreate it under the reviewed write fence", ns, name)
				}
				if kind == "CronJob" && storageCutoverGet(object, "/spec/suspend") != true {
					return nil, nil, fmt.Errorf("CronJob %s/%s must be suspended before storage cutover", ns, name)
				}
				for _, credential := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
					i, ok := names[credential]
					if !ok {
						return nil, nil, fmt.Errorf("%s/%s storage credentials are indirect or absent; explicit mapping required", ns, name)
					}
					entry := storageCutoverMap(env[i])
					ref := storageCutoverMap(storageCutoverMap(entry["valueFrom"])["secretKeyRef"])
					if ref != nil {
						secretName := storageCutoverString(ref["name"])
						id := ns + "/" + secretName
						if !seenSecrets[id] {
							live, err := storageCutoverRead("Secret", ns, secretName)
							if err != nil {
								return nil, nil, err
							}
							if live == nil {
								return nil, nil, fmt.Errorf("source credential Secret %s is missing", id)
							}
							sourceSecrets = append(sourceSecrets, live)
							seenSecrets[id] = true
						}
					} else if entry["valueFrom"] != nil {
						return nil, nil, fmt.Errorf("%s/%s has unsupported storage credential reference", ns, name)
					}
					env[i] = map[string]any{"name": credential, "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": secretName, "key": credential}}}
				}
				fields[fmt.Sprintf("%s/%s/%d/env", podPath, containerType, index)] = env
				namespaces[ns] = true
			}
		}
		if len(fields) > 0 {
			plannedUIDs[storageCutoverString(storageCutoverGet(object, "/metadata/uid"))] = true
			m, err := storageCutoverMutationFor(object, fields)
			if err != nil {
				return nil, nil, err
			}
			mutations = append(mutations, m)
		}
	}
	draining := &storageCutoverDrainingPods{owners: map[string]string{}}
	for pod, uid := range activePods {
		podUID := uid
		chain := []string{}
		seen := map[string]bool{}
		for uid != "" && !plannedUIDs[uid] && !seen[uid] {
			seen[uid] = true
			chain = append(chain, uid)
			uid = owners[uid]
		}
		if !plannedUIDs[uid] {
			return nil, nil, fmt.Errorf("source-bound Pod %s is not controlled by a verified cutover workload", pod)
		}
		if len(afterRollout) > 0 {
			if _, err := time.Parse(time.RFC3339, storageCutoverString(storageCutoverGet(objects[podUID], "/metadata/deletionTimestamp"))); err != nil {
				return nil, nil, fmt.Errorf("source-bound Pod %s is not terminating after rollout; keep writers fenced", pod)
			}
			draining.owners[podUID] = strings.Join(append(chain, uid), "/")
		}
	}
	if len(draining.owners) > 0 {
		return nil, nil, draining
	}
	secrets := []storageCutoverMutation{}
	sortedNamespaces := make([]string, 0, len(namespaces))
	for ns := range namespaces {
		sortedNamespaces = append(sortedNamespaces, ns)
	}
	sort.Strings(sortedNamespaces)
	for _, ns := range sortedNamespaces {
		desired := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": secretName, "namespace": ns, "labels": map[string]any{"rtk.realtek.com/stack": stack, "rtk.realtek.com/storage-purpose": credentialPurpose}}, "type": "Opaque", "data": map[string]any{"AWS_ACCESS_KEY_ID": base64.StdEncoding.EncodeToString([]byte(access)), "AWS_SECRET_ACCESS_KEY": base64.StdEncoding.EncodeToString([]byte(secret))}}
		live, err := storageCutoverRead("Secret", ns, secretName)
		if err != nil {
			return nil, nil, err
		}
		if live != nil && (storageCutoverGet(live, "/metadata/labels/rtk.realtek.com~1stack") != stack || storageCutoverGet(live, "/metadata/labels/rtk.realtek.com~1storage-purpose") != credentialPurpose) {
			return nil, nil, errors.New("existing destination Secret is not owned by the selected storage cutover")
		}
		m, err := storageCutoverDesiredMutation(desired, live)
		if err != nil {
			return nil, nil, err
		}
		secrets = append(secrets, m)
	}
	return append(secrets, mutations...), sourceSecrets, nil
}

func storageCutoverDesiredMutation(desired, live map[string]any) (storageCutoverMutation, error) {
	fields := map[string]any{}
	for _, key := range []string{"spec", "data"} {
		if value, found := desired[key]; found {
			fields["/"+key] = value
		}
	}
	if live == nil {
		return storageCutoverMutation{Kind: storageCutoverString(desired["kind"]), Name: storageCutoverString(storageCutoverGet(desired, "/metadata/name")), Namespace: storageCutoverString(storageCutoverGet(desired, "/metadata/namespace")), After: fields, Create: desired}, nil
	}
	if live["immutable"] == true {
		return storageCutoverMutation{}, errors.New("storage destination Secret is immutable")
	}
	// Preserve server-owned defaults (including Service addresses) and fields
	// outside the reviewed manifest instead of replacing an entire live object.
	for path, value := range fields {
		if old := storageCutoverMap(storageCutoverGet(live, path)); old != nil {
			fields[path] = storageCutoverMerge(old, storageCutoverMap(value))
		}
	}
	return storageCutoverMutationFor(live, fields)
}

func storageCutoverMerge(old, next map[string]any) map[string]any {
	result := storageCutoverMap(storageCutoverClone(old))
	for key, value := range next {
		if a, b := storageCutoverMap(result[key]), storageCutoverMap(value); a != nil && b != nil {
			result[key] = storageCutoverMerge(a, b)
		} else {
			result[key] = value
		}
	}
	return result
}

func planStorageCutoverManifests(manifests []string) ([]storageCutoverMutation, error) {
	mutations := []storageCutoverMutation{}
	for _, manifest := range manifests {
		var desired map[string]any
		if yaml.Unmarshal([]byte(manifest), &desired) != nil {
			return nil, errors.New("invalid storage workload manifest")
		}
		desired = storageCutoverMap(storageCutoverClone(desired))
		if stringData := storageCutoverMap(desired["stringData"]); stringData != nil {
			data := map[string]any{}
			for k, v := range stringData {
				data[k] = base64.StdEncoding.EncodeToString([]byte(storageCutoverString(v)))
			}
			desired["data"] = data
			delete(desired, "stringData")
		}
		live, err := storageCutoverRead(storageCutoverString(desired["kind"]), storageCutoverString(storageCutoverGet(desired, "/metadata/namespace")), storageCutoverString(storageCutoverGet(desired, "/metadata/name")))
		if err != nil {
			return nil, err
		}
		m, err := storageCutoverDesiredMutation(desired, live)
		if err != nil {
			return nil, err
		}
		mutations = append(mutations, m)
	}
	return mutations, nil
}

func completeStorageCutover(cfg deploymentConfig, environmentFile, purpose string, store secretStore, journal *storageCutoverJournal, state map[string]any, verify func() error) (result error) {
	receiptName := "storage-cutover.json"
	if purpose == "ota" {
		receiptName = "storage-cutover-ota.json"
	}
	receiptPath, err := deploymentStorageStatePath(cfg.Environment, receiptName)
	if err != nil {
		return err
	}
	credentialsActivated := false
	defer func() {
		if result != nil {
			_ = os.Remove(receiptPath)
			rollbackErr := validateStorageRollbackData(cfg, *journal)
			if rollbackErr == nil {
				rollbackErr = rollbackStorageJournal(store, journal)
			}
			if rollbackErr == nil && credentialsActivated {
				rollbackErr = rollbackStorageCandidate(cfg, environmentFile, purpose)
			}
			if err := rollbackErr; err != nil {
				result = fmt.Errorf("%w; rollback needs attention: %v", result, err)
			} else {
				result = fmt.Errorf("%w; workload settings restored; keep writers fenced until any destination data is reconciled", result)
			}
		}
	}()
	if err := applyStorageCutover(store, journal); err != nil {
		return err
	}
	if err := verifyStorageCutover(*journal); err != nil {
		return err
	}
	if err := verify(); err != nil {
		return err
	}
	state["cutover_at"] = time.Now().UTC().Format(time.RFC3339)
	state["cutover_id"] = journal.ID
	state["migration_receipt_sha256"] = journal.MigrationSHA256
	workloads := []string{}
	for _, m := range journal.Mutations {
		if storageCutoverPodPath(m.Kind) != "" {
			workloads = append(workloads, m.Namespace+"/"+m.Kind+"/"+m.Name)
		}
	}
	state["workloads_rolled"] = workloads
	if err := activateStorageCandidate(cfg, environmentFile, purpose); err != nil {
		return err
	}
	credentialsActivated = true
	journal.Status = "complete"
	if err := saveStorageCutoverJournal(store, *journal, true); err != nil {
		return fmt.Errorf("finalize private storage cutover journal: %w", err)
	}
	return writeDeploymentStorageState(cfg.Environment, receiptName, state)
}

func ensureOTACutoverSource(namespace, sourceFile, destinationPrefix string) error {
	values, check := deploymentCredentialValues(sourceFile)
	if !check.Passed {
		return errors.New(check.Detail)
	}
	source, err := provisionObjectStoreFromEnv(values)
	if err != nil {
		return err
	}
	workload, err := storageCutoverRead("Deployment", namespace, otaServiceWorkloadName)
	if err != nil {
		return err
	}
	if workload == nil {
		return ensureLiveOTASourceBucket(namespace, sourceFile, destinationPrefix)
	}
	containers, _ := storageCutoverGet(workload, "/spec/template/spec/containers").([]any)
	found := false
	for _, value := range containers {
		env, _ := storageCutoverMap(value)["env"].([]any)
		fields := map[string]string{}
		for _, entry := range env {
			v := storageCutoverMap(entry)
			name := storageCutoverString(v["name"])
			if strings.HasPrefix(name, "VIDEO_CLOUD_BLOB_") {
				if v["valueFrom"] != nil {
					return errors.New("live OTA source storage must use literal settings")
				}
				fields[name] = storageCutoverString(v["value"])
			}
		}
		if fields["VIDEO_CLOUD_BLOB_BUCKET"] == "" {
			continue
		}
		found = true
		prefix := strings.Trim(fields["VIDEO_CLOUD_BLOB_PREFIX"], "/")
		if fields["VIDEO_CLOUD_BLOB_BUCKET"] != source.bucket || fields["VIDEO_CLOUD_BLOB_REGION"] != source.region || strings.TrimRight(fields["VIDEO_CLOUD_BLOB_ENDPOINT"], "/") != strings.TrimRight(source.endpoint, "/") || (source.prefixSet && prefix != source.prefix) || (!source.prefixSet && prefix != "" && prefix != destinationPrefix) {
			return errors.New("source profile does not match live dedicated OTA consumer")
		}
	}
	if !found {
		return errors.New("cannot verify live dedicated OTA source storage")
	}
	return nil
}

func validateStorageRollbackData(cfg deploymentConfig, journal storageCutoverJournal) error {
	if journal.Operation == "reinitialize" {
		return errors.New("reinitialized storage has no source data to restore; rollback to the deleted source is prohibited")
	}
	attempted := false
	for _, m := range journal.Mutations {
		if m.Attempted {
			attempted = true
			break
		}
	}
	if !attempted {
		return nil
	}
	migration, err := readDeploymentStorageState(cfg.Environment, storageMigrationReceiptName(journal.Purpose))
	if err != nil {
		return err
	}
	if journal.MigrationSHA256 == "" || fmt.Sprintf("%x", sha256.Sum256(migration)) != journal.MigrationSHA256 {
		return errors.New("migration proof changed; a reviewed data reconciliation is required before workload rollback")
	}
	values, check := deploymentCredentialProfileValues(cfg.Environment, journal.DestinationFile, defaultDeploymentSharedCredentialFile())
	if !check.Passed {
		return errors.New(check.Detail)
	}
	checker := defaultDeploymentCredentialChecker()
	if journal.Purpose == "ota" {
		err = checker.validateOTAMigrationCutover(cfg, values, journal.SourceFile)
	} else {
		err = checker.validateMediaMigrationCutover(cfg, values, journal.SourceFile)
	}
	if err != nil {
		return fmt.Errorf("rollback blocked by changed or unverifiable storage data; keep writers fenced and reconcile destination writes and deletions before rollback: %w", err)
	}
	return nil
}

func storageCutoverControllerUID(object map[string]any) string {
	refs, _ := storageCutoverGet(object, "/metadata/ownerReferences").([]any)
	for _, value := range refs {
		ref := storageCutoverMap(value)
		if ref["controller"] == true {
			return storageCutoverString(ref["uid"])
		}
	}
	return ""
}

// Resolve Kubernetes envFrom order, then explicit env overrides, for storage
// identification only. Indirect source bindings require a reviewed literal
// mapping rather than changing shared Secret or ConfigMap contents.
func storageCutoverEffectiveStorageEnv(container map[string]any, namespace string, cache map[string]map[string]any) (map[string]string, map[string]bool, error) {
	values, indirect := map[string]string{}, map[string]bool{}
	isStorage := func(key string) bool {
		for _, prefix := range []string{"VIDEO_CLOUD_BLOB_", "VIDEO_CLOUD_OTA_BLOB_", "LINODE_OBJ_"} {
			if key == prefix+"BUCKET" || key == prefix+"ENDPOINT" || key == prefix+"REGION" || key == prefix+"PREFIX" {
				return true
			}
		}
		return false
	}
	read := func(kind string, ref map[string]any) (map[string]any, error) {
		name := storageCutoverString(ref["name"])
		if name == "" {
			return nil, errors.New("empty environment reference")
		}
		id := kind + "/" + namespace + "/" + name
		object, known := cache[id]
		if !known {
			var err error
			object, err = storageCutoverRead(kind, namespace, name)
			if err != nil {
				return nil, err
			}
			cache[id] = object
		}
		if object == nil && ref["optional"] != true {
			return nil, fmt.Errorf("required environment reference %s is missing", id)
		}
		return storageCutoverMap(object["data"]), nil
	}
	decode := func(kind, value string) (string, error) {
		if kind != "Secret" {
			return value, nil
		}
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return "", errors.New("invalid Secret environment data")
		}
		return string(decoded), nil
	}
	from, _ := container["envFrom"].([]any)
	for _, entry := range from {
		item := storageCutoverMap(entry)
		kind, ref := "Secret", storageCutoverMap(item["secretRef"])
		if ref == nil {
			kind, ref = "ConfigMap", storageCutoverMap(item["configMapRef"])
		}
		if ref == nil {
			return nil, nil, errors.New("unsupported envFrom reference")
		}
		data, err := read(kind, ref)
		if err != nil {
			return nil, nil, err
		}
		for key, raw := range data {
			name := storageCutoverString(item["prefix"]) + key
			if !isStorage(name) {
				continue
			}
			value, err := decode(kind, storageCutoverString(raw))
			if err != nil {
				return nil, nil, err
			}
			values[name], indirect[name] = value, true
		}
	}
	env, _ := container["env"].([]any)
	for _, entry := range env {
		item := storageCutoverMap(entry)
		name := storageCutoverString(item["name"])
		if !isStorage(name) {
			continue
		}
		values[name], indirect[name] = storageCutoverString(item["value"]), false
		from := storageCutoverMap(item["valueFrom"])
		if from == nil {
			continue
		}
		indirect[name] = true
		kind, ref := "Secret", storageCutoverMap(from["secretKeyRef"])
		if ref == nil {
			kind, ref = "ConfigMap", storageCutoverMap(from["configMapKeyRef"])
		}
		if ref == nil {
			return nil, nil, errors.New("unsupported indirect storage environment")
		}
		data, err := read(kind, ref)
		if err != nil {
			return nil, nil, err
		}
		raw, found := data[storageCutoverString(ref["key"])]
		if !found && ref["optional"] != true {
			return nil, nil, errors.New("required storage environment key is missing")
		}
		value, err := decode(kind, storageCutoverString(raw))
		if err != nil {
			return nil, nil, err
		}
		values[name] = value
	}
	return values, indirect, nil
}

// API defaulting may add fields, but creation must retain every requested value.
func storageCutoverContains(actual, wanted any) bool {
	switch next := wanted.(type) {
	case map[string]any:
		current, ok := actual.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range next {
			if !storageCutoverContains(current[key], value) {
				return false
			}
		}
		return true
	case []any:
		current, ok := actual.([]any)
		if !ok || len(current) != len(next) {
			return false
		}
		for i, value := range next {
			if !storageCutoverContains(current[i], value) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(actual, wanted)
	}
}
