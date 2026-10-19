package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentCreateResourceLedger struct {
	Schema      string                                      `json:"schema"`
	Environment string                                      `json:"environment"`
	Stack       string                                      `json:"stack"`
	PlanSHA256  string                                      `json:"plan_sha256"`
	Provider    map[string]environmentCreateProviderReceipt `json:"provider"`
	Kubernetes  map[string]string                           `json:"kubernetes"`
	Volumes     []environmentCreateVolumeOwner              `json:"volumes"`
	UpdatedAt   time.Time                                   `json:"updated_at"`
}

type environmentCreateVolumeOwner struct {
	NativeID  string `json:"native_id"`
	PVName    string `json:"pv_name"`
	PVUID     string `json:"pv_uid"`
	PVCName   string `json:"pvc_name"`
	PVCUID    string `json:"pvc_uid"`
	Namespace string `json:"namespace"`
}

// A previous generation cannot be silently abandoned during explicit create.
// Only its original exact volume IDs are inspected; unrelated volumes remain
// outside this environment's ownership.
func verifyEnvironmentCreatePriorVolumes(ctx context.Context, cfg deploymentConfig, store secretStore) error {
	proof, known, err := readDeploymentRemovalVolumes(cfg)
	if err != nil || !known {
		return err
	}
	operator, err := store.readOperator()
	if err != nil {
		return environmentCreateBlocker("deployment.create.resources", "prior volume ownership cannot be inspected with the selected provider credential")
	}
	for _, id := range proof.VolumeIDs {
		_, err := environmentCreateProviderRequest(ctx, operator["LINODE_TOKEN"], http.MethodGet, "/volumes/"+id, nil)
		var absent *environmentCreateProviderHTTPError
		if !errors.As(err, &absent) || absent.Status != http.StatusNotFound {
			return environmentCreateBlocker("deployment.create.resources", "an original-generation volume remains or cannot be verified absent; reconcile its original ownership before new allocation")
		}
	}
	return ctx.Err()
}

func environmentCreateLedgerPath(execution *environmentCreateExecution) string {
	return filepath.Join("deployment", "resources", execution.Journal.PlanSHA256, "resources.json")
}

func readEnvironmentCreateLedger(execution *environmentCreateExecution) (environmentCreateResourceLedger, error) {
	ledger := environmentCreateResourceLedger{Schema: "rtk-cloud.resource-ledger/v1", Environment: execution.Journal.Environment, Stack: execution.Journal.Stack, PlanSHA256: execution.Journal.PlanSHA256, Provider: map[string]environmentCreateProviderReceipt{}, Kubernetes: map[string]string{}, Volumes: []environmentCreateVolumeOwner{}}
	path, err := privateDeploymentStatePath(ledger.Environment, environmentCreateLedgerPath(execution))
	if err != nil {
		return ledger, err
	}
	raw, err := readStorageEvidenceFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ledger, nil
		}
		return ledger, err
	}
	if decodeEnvironmentJSON(raw, &ledger) != nil || ledger.Schema != "rtk-cloud.resource-ledger/v1" || ledger.Environment != execution.Journal.Environment || ledger.Stack != execution.Journal.Stack || ledger.PlanSHA256 != execution.Journal.PlanSHA256 || ledger.Provider == nil || ledger.Kubernetes == nil || ledger.Volumes == nil {
		return ledger, environmentCreateBlocker("deployment.create.resources", "original create resource ledger is invalid")
	}
	return ledger, nil
}

func saveEnvironmentCreateLedger(ctx context.Context, execution *environmentCreateExecution, ledger environmentCreateResourceLedger) error {
	if err := execution.Check(ctx); err != nil {
		return err
	}
	ledger.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	if err := writePrivateDeploymentState(ledger.Environment, environmentCreateLedgerPath(execution), raw); err != nil {
		return err
	}
	active, _ := json.Marshal(deploymentplan.Object{"schema": "rtk-cloud.resource-ledger-active/v1", "environment": ledger.Environment, "stack": ledger.Stack, "plan_sha256": ledger.PlanSHA256, "ledger_sha256": deploymentplan.Digest(ledger)})
	return writePrivateDeploymentState(ledger.Environment, "deployment/resources/active.json", active)
}

func recordEnvironmentCreateProviderOwner(ctx context.Context, execution *environmentCreateExecution, receipt environmentCreateProviderReceipt) error {
	if receipt.Key == "" || receipt.NativeID == "" {
		return environmentCreateBlocker("deployment.create.resources", "native provider allocation has no original identity")
	}
	ledger, err := readEnvironmentCreateLedger(execution)
	if err != nil {
		return err
	}
	if previous, exists := ledger.Provider[receipt.Key]; exists && (previous.NativeID != receipt.NativeID || previous.Kind != receipt.Kind || previous.Region != receipt.Region || previous.Name != receipt.Name) {
		return environmentCreateBlocker("deployment.create.resources", "a provider allocation changed its original owner")
	}
	ledger.Provider[receipt.Key] = receipt
	return saveEnvironmentCreateLedger(ctx, execution, ledger)
}

func captureEnvironmentCreateVolumeOwners(ctx context.Context, cfg deploymentConfig, backend environmentKubernetesBackend, execution *environmentCreateExecution) error {
	raw, err := backend.command(ctx, nil, "get", "persistentvolumes", "-o", "json")
	if err != nil {
		return environmentCreateBlocker("deployment.create.resources", "native persistent volume inventory cannot be captured")
	}
	var response struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Items == nil {
		return environmentCreateBlocker("deployment.create.resources", "persistent volume inventory is incomplete")
	}
	ledger, err := readEnvironmentCreateLedger(execution)
	if err != nil {
		return err
	}
	known := map[string]environmentCreateVolumeOwner{}
	claims, pvcs, pvs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, volume := range ledger.Volumes {
		id := volume.Namespace + "/" + volume.PVCName
		if _, duplicate := known[volume.NativeID]; duplicate || claims[id] || pvcs[volume.PVCUID] || pvs[volume.PVUID] {
			return environmentCreateBlocker("deployment.create.resources", "native durable ownership contains a repeated allocation")
		}
		known[volume.NativeID] = volume
		claims[id], pvcs[volume.PVCUID], pvs[volume.PVUID] = true, true, true
	}
	observed := map[string]bool{}
	for _, pv := range response.Items {
		spec := deploymentplan.Map(pv["spec"])
		claim := deploymentplan.Map(spec["claimRef"])
		namespace := deploymentplan.String(claim["namespace"])
		if !strings.HasPrefix(namespace, cfg.Values["CLOUD_STACK_NAME"]+"-") {
			continue
		}
		meta := deploymentplan.Map(pv["metadata"])
		csi := deploymentplan.Map(spec["csi"])
		if csi["driver"] != "linodebs.csi.linode.com" {
			return environmentCreateBlocker("deployment.create.resources", "selected durable volume lacks supported native CSI ownership")
		}
		nativeID := deploymentplan.String(csi["volumeHandle"])
		if prefix, suffix, ok := strings.Cut(nativeID, "-"); ok && strings.HasPrefix(suffix, "pvc-") {
			nativeID = prefix
		}
		if _, err := positiveIntValue("volume_id", nativeID); err != nil {
			return environmentCreateBlocker("deployment.create.resources", "selected durable volume lacks an exact native volume ID")
		}
		volume := environmentCreateVolumeOwner{nativeID, deploymentplan.String(meta["name"]), deploymentplan.String(meta["uid"]), deploymentplan.String(claim["name"]), deploymentplan.String(claim["uid"]), namespace}
		if volume.PVUID == "" || volume.PVCUID == "" || volume.PVCName == "" {
			return environmentCreateBlocker("deployment.create.resources", "selected durable volume lacks original PV/PVC owner UIDs")
		}
		if original, exists := known[nativeID]; exists && original != volume {
			return environmentCreateBlocker("deployment.create.resources", "native durable allocation changed its original PV/PVC binding")
		}
		if observed[nativeID] {
			return environmentCreateBlocker("deployment.create.resources", "native persistent volume inventory contains a repeated allocation")
		}
		observed[nativeID] = true
		known[nativeID] = volume
	}
	ledger.Volumes = ledger.Volumes[:0]
	for _, volume := range known {
		ledger.Volumes = append(ledger.Volumes, volume)
	}
	sort.Slice(ledger.Volumes, func(i, j int) bool { return ledger.Volumes[i].NativeID < ledger.Volumes[j].NativeID })
	if err := captureManagedLatePlannedVolumes(ctx, backend, execution, &ledger); err != nil {
		return err
	}
	return saveEnvironmentCreateLedger(ctx, execution, ledger)
}

func captureEnvironmentCreateResources(ctx context.Context, cfg deploymentConfig, compiled *compiledEnvironmentDeployment, receipts map[string]environmentCreateProviderReceipt, execution *environmentCreateExecution) error {
	if err := captureEnvironmentCreateVolumeOwners(ctx, cfg, compiled.Backend, execution); err != nil {
		return err
	}
	ledger, err := readEnvironmentCreateLedger(execution)
	if err != nil {
		return err
	}
	for key, receipt := range receipts {
		ledger.Provider[key] = receipt
	}
	live, err := compiled.Backend.Inventory(ctx, cfg.Values["CLOUD_STACK_NAME"])
	if err != nil {
		return err
	}
	for _, object := range live {
		id := deploymentplan.ID(object)
		uid := deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
		if uid == "" {
			return environmentCreateBlocker("deployment.create.resources", "accepted Kubernetes inventory contains an unbound owner")
		}
		if original := ledger.Kubernetes[id]; original != "" && original != uid && (strings.HasPrefix(id, "PersistentVolumeClaim/") || strings.HasPrefix(id, "StatefulSet/")) {
			return environmentCreateBlocker("deployment.create.resources", "captured persistent storage owner changed before final inventory")
		}
		ledger.Kubernetes[id] = uid
	}
	if err := saveEnvironmentCreateLedger(ctx, execution, ledger); err != nil {
		return err
	}
	return saveEnvironmentCreateRemovalVolumeProof(ctx, cfg, compiled, ledger, execution)
}

// The final ordinary remove proof is based on real native PV/PVC ownership.
// Historical generation evidence is archived only after its exact volumes are
// proven absent. No historical recovery receipt is rewritten as a fresh capture.
func saveEnvironmentCreateRemovalVolumeProof(ctx context.Context, cfg deploymentConfig, compiled *compiledEnvironmentDeployment, ledger environmentCreateResourceLedger, execution *environmentCreateExecution) error {
	previous, known, err := readDeploymentRemovalVolumes(cfg)
	if err != nil {
		return err
	}
	currentID := ledger.Provider["lke"].NativeID
	if known && previous.ClusterID != currentID {
		operator, err := compiled.Store.readOperator()
		if err != nil {
			return err
		}
		for _, id := range previous.VolumeIDs {
			_, err := environmentCreateProviderRequest(ctx, operator["LINODE_TOKEN"], http.MethodGet, "/volumes/"+id, nil)
			var absent *environmentCreateProviderHTTPError
			if !errors.As(err, &absent) || absent.Status != http.StatusNotFound {
				return environmentCreateBlocker("deployment.create.resources", "an original-generation volume remains or cannot be verified absent; retain both generations")
			}
		}
		raw, _ := json.Marshal(previous)
		if err := execution.Store.write(filepath.Join("deployment", "create", "executions", execution.Journal.PlanSHA256, filepath.Base(execution.Directory), "prior-remove-volume-ownership.json"), raw, false); err != nil {
			return err
		}
	}
	ids := []string{}
	for _, volume := range ledger.Volumes {
		ids = append(ids, volume.NativeID)
	}
	raw, err := readBoundedEnvironmentFile(compiled.Store.KubeconfigPath(), true)
	if err != nil {
		return err
	}
	proof := deploymentRemovalVolumes{Schema: 1, Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], ClusterID: currentID, KubeSHA256: environmentPreflightBytesSHA256(raw), VolumeIDs: ids}
	body, _ := json.Marshal(proof)
	return writePrivateDeploymentState(cfg.Environment, "deployment/remove-volume-ownership.json", body)
}
