package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func openBaoFirstCompletedEntry(j environmentCreateJournal, key, phase string) (*environmentCreateJournalEntry, error) {
	var found *environmentCreateJournalEntry
	for i := range j.Entries {
		e := &j.Entries[i]
		if e.Key != key {
			continue
		}
		if found != nil || e.Phase != phase || e.State != "completed" || len(e.Receipt) == 0 || e.StartedAt.Before(j.AdmittedAt) || e.CompletedAt.IsZero() || e.CompletedAt.Before(e.StartedAt) || !environmentPreflightDigest.MatchString(e.IntentSHA256) {
			return nil, createProviderRecoveryBlock("original completed prerequisite is ambiguous or incomplete")
		}
		found = e
	}
	if found == nil {
		return nil, createProviderRecoveryBlock("original completed prerequisite is missing")
	}
	return found, nil
}

func openBaoFirstIntentMatches(entry *environmentCreateJournalEntry, intent any) bool {
	raw, err := json.Marshal(intent)
	if err != nil {
		return false
	}
	sha, err := environmentCreatePlanSHA256(raw)
	return err == nil && sha == entry.IntentSHA256
}

func verifyOpenBaoFirstRecordedObject(j environmentCreateJournal, phase string, desired, actual deploymentplan.Object) error {
	id := deploymentplan.ID(desired)
	entry, err := openBaoFirstCompletedEntry(j, id, phase)
	if err != nil {
		return err
	}
	var proof struct {
		ResourceID   string `json:"resource_id"`
		UID          string `json:"uid"`
		PolicySHA256 string `json:"policy_sha256"`
	}
	match := desired
	if phase == "storage" {
		// The original creator permits an already existing class without
		// adopting its labels. The parent intent still authenticates its full
		// requested body, while the receipt binds the retained policy and UID.
		match = deploymentplan.Object{"apiVersion": desired["apiVersion"], "kind": desired["kind"], "metadata": deploymentplan.Object{"name": deploymentplan.Map(desired["metadata"])["name"]}, "provisioner": desired["provisioner"], "reclaimPolicy": desired["reclaimPolicy"], "volumeBindingMode": desired["volumeBindingMode"], "allowVolumeExpansion": desired["allowVolumeExpansion"]}
	}
	if decodeEnvironmentJSON(entry.Receipt, &proof) != nil || proof.ResourceID != id || proof.UID == "" || actual == nil || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != proof.UID || !openBaoFirstIntentMatches(entry, desired) || !deploymentplan.Matches(match, actual) {
		return createProviderRecoveryBlock("original Kubernetes prerequisite owner or intent changed")
	}
	if phase == "storage" {
		policy := deploymentplan.Object{"provisioner": actual["provisioner"], "reclaimPolicy": actual["reclaimPolicy"], "volumeBindingMode": actual["volumeBindingMode"], "allowVolumeExpansion": actual["allowVolumeExpansion"]}
		if !environmentPreflightDigest.MatchString(proof.PolicySHA256) || proof.PolicySHA256 != deploymentplan.Digest(policy) {
			return createProviderRecoveryBlock("original storage class policy changed")
		}
	}
	return nil
}

// Read the original VM host identity to reconstruct its frozen request. Unlike
// the fresh producer, this function never creates or repairs either key file.
func readOpenBaoFirstProviderMaterial(store secretStore, planSHA string, resource environmentCreateProviderResource) (map[string]string, error) {
	if resource.Kind != "instance" {
		return nil, nil
	}
	bad := func() (map[string]string, error) {
		return nil, createProviderRecoveryBlock("original VM host identity cannot be authenticated")
	}
	if !environmentPreflightDigest.MatchString(planSHA) || !secretEnvironmentPattern.MatchString(resource.Key) {
		return bad()
	}
	rel := filepath.Join("deployment", "create", "material", planSHA, "host-identity", resource.Key, "ssh_host_ed25519_key")
	private, err := store.read(rel)
	if err != nil {
		return bad()
	}
	private = strings.TrimSpace(private) + "\n"
	key, err := ssh.ParseRawPrivateKey([]byte(private))
	if err != nil {
		return bad()
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return bad()
	}
	public := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	storedPublic, err := store.read(rel + ".pub")
	if err != nil || strings.TrimSpace(storedPublic) != public {
		return bad()
	}
	cloud, err := yaml.Marshal(map[string]any{"ssh_keys": map[string]string{"ed25519_private": private, "ed25519_public": public}})
	if err != nil {
		return bad()
	}
	cloud = append([]byte("#cloud-config\n"), cloud...)
	if validateEnvironmentCreateHostIdentityMaterial(cloud, public) != nil {
		return bad()
	}
	return map[string]string{"user_data": base64.StdEncoding.EncodeToString(cloud), "host_public_key": public, "host_key_sha256": environmentPreflightBytesSHA256([]byte(public))}, nil
}

func verifyOpenBaoFirstWorkerOwners(ctx context.Context, cfg deploymentConfig, store secretStore, j environmentCreateJournal, cluster environmentCreateProviderReceipt, operator map[string]string) error {
	bad := func() error { return createProviderRecoveryBlock("original worker pool or instance owner changed") }
	entry, err := openBaoFirstCompletedEntry(j, "provider-worker-names", "provider")
	if err != nil {
		return err
	}
	path, err := store.safePath("deployment/adapters/lke/worker-labels.json")
	if err != nil {
		return bad()
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	var names environmentWorkerNames
	var recorded environmentWorkerNames
	if err != nil || decodeEnvironmentJSON(raw, &names) != nil || decodeEnvironmentJSON(entry.Receipt, &recorded) != nil || names.Schema != "rtk-cloud.lke-worker-labels/v1" || names.Environment != cfg.Environment || names.Stack != cfg.Values["CLOUD_STACK_NAME"] || names.ClusterID != cluster.NativeID || len(names.Entries) == 0 || deploymentplan.Digest(names) != deploymentplan.Digest(recorded) || !openBaoFirstIntentMatches(entry, names) {
		return bad()
	}
	pools, err := environmentCreateProviderList(ctx, operator["LINODE_TOKEN"], "/lke/clusters/"+cluster.NativeID+"/pools")
	if err != nil {
		return bad()
	}
	actual := map[int]map[int]bool{}
	classes := map[int]string{}
	for _, raw := range pools {
		var pool deploymentplan.Object
		if json.Unmarshal(raw, &pool) != nil {
			return bad()
		}
		id := int(workerNativeIDNumber(pool["id"]))
		class := deploymentplan.String(deploymentplan.Map(pool["labels"])["rtk.io/node-class"])
		if id <= 0 || actual[id] != nil || pool["label"] != names.Stack+"-"+class {
			return bad()
		}
		actual[id], classes[id] = map[int]bool{}, class
		for _, node := range createProviderObjectList(pool["nodes"]) {
			instanceID := int(workerNativeIDNumber(node["instance_id"]))
			if instanceID <= 0 || actual[id][instanceID] {
				return bad()
			}
			actual[id][instanceID] = true
		}
	}
	seen := map[int]bool{}
	seenPools := map[int]bool{}
	for _, item := range names.Entries {
		expected, err := environmentWorkerDisplayName(names.Stack, item.NodeClass, item.Ordinal)
		if err != nil || expected != item.Label || seen[item.InstanceID] || classes[item.PoolID] != item.NodeClass || !actual[item.PoolID][item.InstanceID] {
			return bad()
		}
		seen[item.InstanceID] = true
		seenPools[item.PoolID] = true
		delete(actual[item.PoolID], item.InstanceID)
		labelEntry, err := openBaoFirstCompletedEntry(j, fmt.Sprintf("worker-label-%d", item.InstanceID), "provider")
		if err != nil {
			return err
		}
		var proof struct {
			InstanceID int    `json:"instance_id"`
			PoolID     int    `json:"pool_id"`
			OldLabel   string `json:"old_label"`
			Label      string `json:"label"`
		}
		if decodeEnvironmentJSON(labelEntry.Receipt, &proof) != nil || proof.InstanceID != item.InstanceID || proof.PoolID != item.PoolID || proof.Label != item.Label || !openBaoFirstIntentMatches(labelEntry, proof) {
			return bad()
		}
		body, err := environmentCreateProviderRequest(ctx, operator["LINODE_TOKEN"], "GET", "/linode/instances/"+strconv.Itoa(item.InstanceID), nil)
		var instance deploymentplan.Object
		if err != nil || json.Unmarshal(body, &instance) != nil || int(workerNativeIDNumber(instance["id"])) != item.InstanceID || instance["label"] != item.Label || instance["status"] != "running" {
			return bad()
		}
	}
	for id, members := range actual {
		if !seenPools[id] || len(members) != 0 {
			return bad()
		}
	}
	return ctx.Err()
}

// verifyOpenBaoFirstOuterGates only reads the same selected parent and its
// existing provider/Kubernetes owners; it never allocates or rewrites state.
func verifyOpenBaoFirstOuterGates(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string) error {
	bad := func() error {
		return createProviderRecoveryBlock("original provider, storage or Kubernetes prerequisite changed")
	}
	if e == nil || e.Store != store || b.Store != store || cfg.Environment != store.Environment || e.Check(ctx) != nil {
		return bad()
	}
	plan, configSHA, err := loadEnvironmentCreateAdmittedGeneration(store, e.Journal.PlanSHA256, e.Journal.ArtifactSHA256)
	if err != nil || !plan.OpenBaoFirst || plan.Environment != cfg.Environment || plan.Stack != e.Journal.Stack || configSHA != plan.ConfigurationSHA256 || configSHA != deploymentplan.Digest(cfg) {
		return bad()
	}
	if len(plan.Provider.Resources) == 0 || validateEnvironmentCreateProviderPlan(cfg, plan.Provider) != nil {
		return bad()
	}
	if _, _, err := verifyCreateEnvironmentOperator(ctx, cfg, store, e, operator, &plan); err != nil {
		return err
	}
	ledger, err := readEnvironmentCreateLedger(e)
	if err != nil {
		return err
	}
	var cluster environmentCreateProviderReceipt
	for _, resource := range plan.Provider.Resources {
		entry, err := openBaoFirstCompletedEntry(e.Journal, resource.Key, "provider")
		if err != nil {
			return err
		}
		material, err := readOpenBaoFirstProviderMaterial(store, e.Journal.PlanSHA256, resource)
		if err != nil || !openBaoFirstIntentMatches(entry, struct {
			Resource       environmentCreateProviderResource `json:"resource"`
			MaterialSHA256 string                            `json:"material_sha256"`
		}{resource, deploymentplan.Digest(material)}) {
			return bad()
		}
		var receipt environmentCreateProviderReceipt
		if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil {
			return bad()
		}
		owner, ok := ledger.Provider[resource.Key]
		if !ok || owner.Key != receipt.Key || owner.Kind != receipt.Kind || owner.NativeID != receipt.NativeID || owner.Name != receipt.Name || owner.Region != receipt.Region {
			return bad()
		}
		actual, ready, err := verifyEnvironmentCreateProviderResource(ctx, cfg, resource, receipt, operator)
		if err != nil || !ready || receipt.PublicIP != "" && actual.PublicIP != receipt.PublicIP || receipt.PrivateIP != "" && actual.PrivateIP != receipt.PrivateIP || receipt.S3Endpoint != "" && actual.S3Endpoint != receipt.S3Endpoint {
			return bad()
		}
		if resource.Key == "lke" {
			cluster = receipt
		}
	}
	entry, err := openBaoFirstCompletedEntry(e.Journal, "provider-kube-access", "provider")
	if err != nil {
		return err
	}
	intent := map[string]any{"cluster": cluster, "destination": "kube/kubeconfig.yaml"}
	var access struct {
		ClusterID           string `json:"cluster_id"`
		KubeconfigSHA256    string `json:"kubeconfig_sha256"`
		ProviderStateSHA256 string `json:"provider_state_sha256"`
	}
	if cluster.Kind != "lke" || decodeEnvironmentJSON(entry.Receipt, &access) != nil || access.ClusterID != cluster.NativeID || !openBaoFirstIntentMatches(entry, intent) {
		return bad()
	}
	kubePath, err := store.safePath("kube/kubeconfig.yaml")
	if err != nil {
		return bad()
	}
	kube, err := readBoundedEnvironmentFile(kubePath, true)
	if err != nil || !environmentPreflightDigest.MatchString(access.KubeconfigSHA256) || environmentPreflightBytesSHA256(kube) != access.KubeconfigSHA256 {
		return bad()
	}
	selectedBackend, err := newEnvironmentKubernetesBackend(string(kube), cfg.Environment)
	if err != nil || selectedBackend.Store != store || !bytes.Equal(selectedBackend.KubeconfigContent, b.KubeconfigContent) {
		return bad()
	}
	state, err := readLKEProviderState(cfg.Environment, cfg.Values["CLOUD_STACK_NAME"])
	if err != nil || state["LKE_CLUSTER_ID"] != cluster.NativeID || state["LKE_CLUSTER_LABEL"] != cluster.Name || state["LKE_CLUSTER_REGION"] != cluster.Region || !validEnvironmentCreateKubernetesVersion(state["LKE_CLUSTER_VERSION"]) {
		return bad()
	}
	originalState := map[string]string{"LKE_CLUSTER_ID": state["LKE_CLUSTER_ID"], "LKE_CLUSTER_LABEL": state["LKE_CLUSTER_LABEL"], "LKE_CLUSTER_REGION": state["LKE_CLUSTER_REGION"], "LKE_CLUSTER_VERSION": state["LKE_CLUSTER_VERSION"]}
	if !environmentPreflightDigest.MatchString(access.ProviderStateSHA256) || deploymentplan.Digest(originalState) != access.ProviderStateSHA256 {
		return bad()
	}
	ready, err := b.command(ctx, nil, "get", "--raw=/readyz")
	if err != nil || strings.TrimSpace(string(ready)) != "ok" {
		return bad()
	}
	if err := qualifyEnvironmentCreateStorage(ctx, cfg, operator); err != nil {
		return err
	}
	if err := validateEnvironmentCreateStorageAllocation(cfg); err != nil {
		return err
	}
	allocationRaw, err := store.read(environmentCreateStorageAllocationPath)
	var allocation environmentCreateStorageAllocation
	if err != nil || decodeEnvironmentJSON([]byte(allocationRaw), &allocation) != nil || allocation.PlanSHA256 != e.Journal.PlanSHA256 {
		return bad()
	}
	if err := verifyOpenBaoFirstWorkerOwners(ctx, cfg, store, e.Journal, cluster, operator); err != nil {
		return err
	}
	for _, namespace := range lkeNamespaces(environmentCreateProviderValues(cfg)) {
		body := deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": namespace.Name, "labels": deploymentplan.Object{"rtk.realtek.com/stack": cfg.Values["CLOUD_STACK_NAME"], "app.kubernetes.io/managed-by": "rtk-cloud"}}}
		actual, err := b.Read(ctx, deploymentplan.ID(body))
		if err != nil {
			return bad()
		}
		if err := verifyOpenBaoFirstRecordedObject(e.Journal, "namespace", body, actual); err != nil {
			return err
		}
	}
	id := "StorageClass//" + cfg.Values["DURABLE_DATA_STORAGE_CLASS"]
	actual, err := b.Read(ctx, id)
	if err != nil || actual == nil {
		return bad()
	}
	body := deploymentplan.Object{"apiVersion": "storage.k8s.io/v1", "kind": "StorageClass", "metadata": deploymentplan.Object{"name": cfg.Values["DURABLE_DATA_STORAGE_CLASS"], "labels": deploymentplan.Object{"rtk.realtek.com/stack": cfg.Values["CLOUD_STACK_NAME"], "app.kubernetes.io/managed-by": "rtk-cloud"}}, "provisioner": actual["provisioner"], "reclaimPolicy": actual["reclaimPolicy"], "volumeBindingMode": actual["volumeBindingMode"], "allowVolumeExpansion": actual["allowVolumeExpansion"]}
	if err := verifyOpenBaoFirstRecordedObject(e.Journal, "storage", body, actual); err != nil {
		return err
	}
	return e.Check(ctx)
}
