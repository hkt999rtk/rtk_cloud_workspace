package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"path/filepath"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Project before compiling/hashing the child plan: the reviewed bodies, writer
// identity and actual Kubernetes apply must all describe the same storage.
func projectEnvironmentCreateCustodyStage(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, execution *environmentCreateExecution, definitions []deploymentplan.Definition, original *deploymentplan.Plan) ([]deploymentplan.Definition, error) {
	if cfg.Values["PKI_STATE_BACKEND"] != "openbao" {
		return definitions, nil
	}
	runtime, ok := ctx.Value(environmentCreateBootstrapRuntimeKey{}).(environmentCreateBootstrapRuntime)
	generation := execution.Journal.PlanSHA256
	if ok && environmentPreflightDigest.MatchString(runtime.ExecutionSHA256) {
		generation = runtime.ExecutionSHA256
	}
	expected, err := createEnvironmentCustodyBinding(cfg, parent.Bootstrap, generation, "")
	if err != nil {
		return nil, err
	}
	// Foundation/business/public-only stages do not require workload custody.
	if !environmentCustodyStageOwnsStorage(expected, definitions) {
		return definitions, nil
	}
	if !ok || runtime.Execution == nil || !environmentPreflightDigest.MatchString(runtime.ExecutionSHA256) {
		return nil, environmentCreateBootstrapBlocker("workload stage lacks its original custody generation and completed provider configuration")
	}
	if runtime.Execution.Store.Root != store.Root || store.Environment != cfg.Environment || execution.Store.Root != store.Root {
		return nil, environmentCreateBootstrapBlocker("custody stage belongs to another selected environment")
	}
	intent := deploymentplan.Object{"schema": "rtk.custody.bootstrap/v1", "environment": cfg.Environment, "generation": generation, "mount": cfg.Values["PKI_STATE_KV_MOUNT"], "cas_required": true, "owners": expected.Owners}
	body, err := json.Marshal(intent)
	if err != nil {
		return nil, err
	}
	scopeSHA, err := environmentCreatePlanSHA256(body)
	if err != nil {
		return nil, err
	}
	path, err := store.safePath(filepath.Join("deployment", "create", generation, "custody", "owners.json"))
	if err != nil {
		return nil, err
	}
	raw, err := readBoundedEnvironmentInputLimit(path, true, false, 256<<10)
	if err != nil {
		return nil, environmentCreateBootstrapBlocker("original workload custody scope is unavailable")
	}
	actualSHA, err := environmentCreatePlanSHA256(raw)
	if err != nil || actualSHA != scopeSHA {
		return nil, environmentCreateBootstrapBlocker("workload stage differs from its original mount, generation or owner scope")
	}
	completed := false
	for _, entry := range runtime.Execution.Journal.Entries {
		if entry.Key != "workload-custody-initialize" {
			continue
		}
		if completed || entry.Phase != "bootstrap" || entry.State != "completed" || entry.IntentSHA256 != scopeSHA || entry.StartedAt.IsZero() || entry.CompletedAt.IsZero() ||
			deploymentplan.Digest(jsonRawObject(entry.Receipt)) != deploymentplan.Digest(map[string]string{"scope_sha256": scopeSHA}) {
			return nil, environmentCreateBootstrapBlocker("custody owner configuration has no exact completed native readback")
		}
		completed = true
	}
	if !completed {
		return nil, environmentCreateBootstrapBlocker("custody configuration must complete before workload stages")
	}
	if runtime.Custody != nil && (runtime.Custody.Generation != generation || deploymentplan.Digest(runtime.Custody.Owners) != deploymentplan.Digest(expected.Owners)) {
		return nil, environmentCreateBootstrapBlocker("runtime custody binding differs from its original recorded owner inventory")
	}
	caPath, err := store.safePath(filepath.Join("deployment", "create", "material", generation, "openbao-transport", "ca.crt"))
	if err != nil || runtime.OpenBao.CACertPath != caPath {
		return nil, environmentCreateBootstrapBlocker("custody stage requires its original OpenBao foundation authority")
	}
	ca, err := readBoundedEnvironmentFile(caPath, true)
	if err != nil {
		return nil, err
	}
	path, err = store.safePath(filepath.Join("deployment", "create", generation, "custody", "transport.json"))
	if err != nil {
		return nil, err
	}
	raw, err = readBoundedEnvironmentInputLimit(path, true, false, 256<<10)
	if err != nil {
		return nil, environmentCreateBootstrapBlocker("custody stage lacks its original public transport snapshot")
	}
	var snapshot environmentCustodyTransportTrust
	if json.Unmarshal(raw, &snapshot) != nil {
		return nil, environmentCreateBootstrapBlocker("original custody transport snapshot is invalid")
	}
	var managed *environmentCreateRegisteredIssuer
	if snapshot.ManagedPEM != "" {
		status := "ready"
		if snapshot.ManagedOnly {
			status = "active"
		}
		managed = &environmentCreateRegisteredIssuer{ID: snapshot.ManagedRootID, Environment: cfg.Environment, Domain: "openbao_tls", Kind: "root", Status: status, Certificate: snapshot.ManagedPEM, ChainPEM: snapshot.ManagedPEM, RootFingerprint: snapshot.ManagedRootSHA256, BundleVersion: environmentPreflightBytesSHA256([]byte(snapshot.ManagedPEM))}
	}
	trust, err := renderEnvironmentCustodyTrust(cfg, generation, string(ca), managed, snapshot.ManagedOnly)
	if err != nil {
		return nil, err
	}
	canonical := []byte(deploymentplan.String(deploymentplan.Map(trust[0].Object["data"])["transport.json"]))
	if deploymentplan.Digest(jsonRawObject(raw)) != deploymentplan.Digest(jsonRawObject(canonical)) {
		return nil, environmentCreateBootstrapBlocker("custody stage transport snapshot differs from its original environment or authority")
	}
	bootstrapRoot := ""
	if !snapshot.ManagedOnly {
		block, _ := pem.Decode(ca) // renderEnvironmentCustodyTrust already verified this exact CA.
		if block == nil {
			return nil, environmentCreateBootstrapBlocker("original custody foundation CA is invalid")
		}
		bootstrapRoot = environmentPreflightBytesSHA256(block.Bytes)
	}
	expected.OpenBaoRootSHA256 = snapshot.ManagedRootSHA256
	if runtime.ServiceCeremony != nil {
		expected.ServiceRootSHA256 = runtime.ServiceCeremony.Root.RootFingerprint
	} else if runtime.Custody != nil {
		expected.ServiceRootSHA256 = runtime.Custody.ServiceRootSHA256
	}
	// A completed early stage retains its originally compiled TLS mode even
	// after global transport handoff. The public snapshot may advance, but that
	// must not change a replayed child plan's reviewed resource bodies.
	if original != nil {
		seen := false
		for _, resource := range original.Resources {
			pod := environmentPKIPod(resource.Desired)
			if pod == nil {
				continue
			}
			for _, collection := range []string{"containers", "initContainers"} {
				for _, raw := range certIssuerObjectList(pod[collection]) {
					container := deploymentplan.Map(raw)
					if mqttFoundationEnv(container, "PKI_STATE_BACKEND") != "openbao" {
						continue
					}
					b, r := mqttFoundationEnv(container, "PKI_STATE_BOOTSTRAP_ROOT_SHA256"), mqttFoundationEnv(container, "PKI_STATE_OPENBAO_SERVER_PKI_ROOT_SHA256")
					if b != "" {
						block, _ := pem.Decode(ca)
						if block == nil || b != environmentPreflightBytesSHA256(block.Bytes) {
							return nil, environmentCreateBootstrapBlocker("frozen custody stage replaced its original foundation authority")
						}
					} else if !snapshot.ManagedOnly {
						return nil, environmentCreateBootstrapBlocker("frozen managed-only custody stage cannot restore bootstrap admission")
					}
					if r != "" && r != snapshot.ManagedRootSHA256 || seen && (b != bootstrapRoot || r != expected.OpenBaoRootSHA256) {
						return nil, environmentCreateBootstrapBlocker("frozen custody stage differs from its original transport authority")
					}
					bootstrapRoot, expected.OpenBaoRootSHA256, seen = b, r, true
				}
			}
		}
	}
	return projectEnvironmentCustodyResources(cfg.Values, cfg.Environment, expected, bootstrapRoot, definitions)
}

func environmentCustodyStageOwnsStorage(binding *environmentCustodyBinding, definitions []deploymentplan.Definition) bool {
	claims := map[string]bool{}
	for _, owner := range binding.Owners {
		for _, alias := range owner.StorageAliases {
			claims[owner.Namespace+"/"+alias] = true
		}
	}
	for _, d := range definitions {
		metadata := deploymentplan.Map(d.Object["metadata"])
		namespace := deploymentplan.String(metadata["namespace"])
		if d.Object["kind"] == "PersistentVolumeClaim" && claims[namespace+"/"+deploymentplan.String(metadata["name"])] {
			return true
		}
		pod := environmentPKIPod(d.Object)
		if pod == nil {
			continue
		}
		for _, owner := range binding.Owners {
			if owner.Namespace == namespace && owner.ID == deploymentplan.String(metadata["name"]) {
				return true
			}
		}
		for _, raw := range certIssuerObjectList(pod["volumes"]) {
			if claims[namespace+"/"+deploymentplan.String(deploymentplan.Map(deploymentplan.Map(raw)["persistentVolumeClaim"])["claimName"])] {
				return true
			}
		}
		for _, collection := range []string{"containers", "initContainers"} {
			for _, raw := range certIssuerObjectList(pod[collection]) {
				if mqttFoundationEnv(deploymentplan.Map(raw), "PKI_STATE_BACKEND") == "openbao" {
					return true
				}
			}
		}
	}
	return false
}
