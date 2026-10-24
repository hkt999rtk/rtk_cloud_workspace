package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Device Root generation belongs to an explicit native one-shot bootstrap, not
// ordinary controller startup or the human offline-root activation endpoint.
// The second Job reconciles exactly the first UUID after actual consumers ack.
func executeEnvironmentCreateDeviceRoot(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, operator map[string]string, runtime environmentCreateBootstrapRuntime, apply environmentCreateBootstrapApply, distribute func(environmentCreateRegisteredIssuer) error) (environmentCreateRegisteredIssuer, error) {
	var root environmentCreateRegisteredIssuer
	policy := findEnvironmentCreateDomainPolicy(p, "device")
	if runtime.Execution == nil || runtime.PKIClient == nil || apply == nil || distribute == nil || runtime.ExecutionSHA256 != runtime.Execution.Journal.PlanSHA256 || runtime.Execution.Store.Root != store.Root || store.Environment != cfg.Environment || p.Environment != cfg.Environment || p.Stack != cfg.Values["CLOUD_STACK_NAME"] || policy.Domain != "device" || !slices.Equal(policy.BundleConsumers, []string{"pkibroker", "video-cloud-api"}) {
		return root, environmentCreateBootstrapBlocker("Device Root requires the original selected execution and both actual API/broker bundle consumers")
	}
	if err := runtime.Execution.Check(ctx); err != nil {
		return root, err
	}
	var id string
	var err error
	var recovery *createDeviceBootstrapRecovery
	var trustRecovery *createDeviceTrustRecovery
	if runtime.ServiceSessionRecovery != nil {
		recovery = runtime.ServiceSessionRecovery.DeviceBootstrapRecovery
		trustRecovery = runtime.ServiceSessionRecovery.DeviceTrustRecovery
	}
	if trustRecovery != nil {
		id = trustRecovery.Root.ID
		root = trustRecovery.Root
	} else if recovery != nil {
		id = recovery.RootID
	} else {
		id, err = allocateEnvironmentCreateUUID()
	}
	if err != nil {
		return root, err
	}
	intent, _ := json.Marshal(deploymentplan.Object{"environment": cfg.Environment, "issuer_id": id, "bundle_consumers": policy.BundleConsumers})
	if recovery == nil && trustRecovery == nil {
		if err := runtime.Execution.BeginEntry("device-root-bootstrap", "bootstrap", intent, time.Now().UTC()); err != nil {
			return root, err
		}
		if _, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join("deployment", "create", runtime.ExecutionSHA256, "bootstrap", "device", "root-intent.json"), json.RawMessage(intent)); err != nil {
			return root, err
		}
		for _, capability := range []struct{ command, name string }{{"render-device-bootstrap-policy", "create-device-root-bootstrap"}, {"render-automatic-device-policy", "create-device-controller"}, {"render-automatic-device-signer-policy", "create-device-signer"}} {
			raw, err := runtime.Backend.command(ctx, nil, "--namespace", p.Stack+"-video-cloud", "exec", "deployment/pki-controller", "--container", "pki-controller", "--", "/app/pkicontroller", capability.command)
			if err != nil || len(raw) == 0 || len(raw) > 64<<10 || !strings.Contains(string(raw), "pki-issuers/device/") || capability.command != "render-device-bootstrap-policy" && strings.Contains(string(raw), "root/generate/") {
				return root, environmentCreateBootstrapBlocker("native Device policy capability is unavailable or exposes bootstrap Root generation to a runtime owner")
			}
			if err := installEnvironmentCreateOpenBaoPolicy(ctx, cfg, runtime, capability.name, string(raw)); err != nil {
				return root, err
			}
		}
		if err := configureEnvironmentCreateOpenBaoRole(ctx, cfg, runtime, "pki-device-bootstrap-"+cfg.Environment, []string{"pki-device-bootstrap"}, []string{"create-device-root-bootstrap"}); err != nil {
			return root, err
		}
		if err := configureEnvironmentCreateOpenBaoRole(ctx, cfg, runtime, "pki-controller-"+cfg.Environment, []string{"pki-controller"}, environmentCreateControllerPolicies(runtime)); err != nil {
			return root, err
		}
	}
	for _, stage := range []string{"ready", "active"} {
		definitions, err := renderEnvironmentCreateDeviceBootstrapJob(ctx, cfg, p, id, stage)
		if err != nil {
			return root, err
		}
		if trustRecovery != nil && stage == "ready" {
			// Its admitted forward Job already created the original READY issuer.
			// This continuation must never run Root generation again.
		} else if recovery != nil && stage == "ready" {
			if err := executeDeviceBootstrapForward(ctx, cfg, runtime, apply); err != nil {
				return root, err
			}
		} else if err := apply(ctx, "device-root-"+stage, definitions); err != nil {
			return root, err
		}
		if err := refreshEnvironmentCreateAuthentication(ctx, runtime.PKIClient, operator); err != nil {
			return root, err
		}
		document, err := runtime.PKIClient.ReadIssuer(ctx, id)
		if err != nil {
			return root, err
		}
		observed, err := environmentCreateRegisteredIssuerFromMap(document, "")
		if err != nil || validateEnvironmentCreateDeviceRoot(cfg, observed) != nil || observed.ID != id || observed.Status != stage {
			return root, environmentCreateBootstrapBlocker("native Device bootstrap did not produce the original expected Root UUID, provider, certificate and lifecycle state")
		}
		if trustRecovery != nil && stage == "ready" && deploymentplan.Digest(document) != deploymentplan.Digest(trustRecovery.Root.Document) {
			return root, environmentCreateBootstrapBlocker("original READY Device identity changed before trust continuation")
		}
		if stage == "active" && (observed.Certificate != root.Certificate || observed.ChainPEM != root.ChainPEM || observed.RootFingerprint != root.RootFingerprint || observed.BundleVersion != root.BundleVersion || observed.Reference != root.Reference) {
			return root, environmentCreateBootstrapBlocker("Device activation changed the original ready Root identity")
		}
		root = observed
		if err := recordEnvironmentCreateDeviceIssuer(store, runtime.ExecutionSHA256, stage, document, trustRecovery); err != nil {
			return root, err
		}
		receipt, _ := json.Marshal(deploymentplan.Object{"environment": root.Environment, "issuer_id": root.ID, "signer_reference": root.Reference, "root_sha256": root.RootFingerprint, "bundle_version": root.BundleVersion})
		if err := runtime.Execution.RecordReceipt("device-root-bootstrap", receipt); err != nil {
			return root, err
		}
		if stage == "ready" {
			if err := distribute(root); err != nil {
				return root, err
			}
			if err := waitEnvironmentCreateIssuerAcks(ctx, cfg, runtime, root, policy.BundleConsumers); err != nil {
				return root, err
			}
		}
	}
	if err := retireEnvironmentCreateDeviceBootstrapRole(ctx, cfg, runtime); err != nil {
		return root, err
	}
	if err := runtime.Execution.CompleteEntry("device-root-bootstrap", time.Now().UTC()); err != nil {
		return root, err
	}
	return root, nil
}

// A qualified trust continuation reuses its exact original READY document.
// Fresh lifecycle records and the later ACTIVE record remain exclusive writes.
func recordEnvironmentCreateDeviceIssuer(store secretStore, executionSHA, stage string, document map[string]any, recovery *createDeviceTrustRecovery) error {
	relative := filepath.Join("deployment", "create", executionSHA, "bootstrap", "device", "root-"+stage+".json")
	if recovery != nil && stage == "ready" {
		var original map[string]any
		if readExecutionJSON(filepath.Join(store.Root, relative), &original) != nil || deploymentplan.Digest(original) != deploymentplan.Digest(recovery.Root.Document) || deploymentplan.Digest(document) != deploymentplan.Digest(original) {
			return environmentCreateBootstrapBlocker("qualified Device trust continuation requires its unchanged original READY ceremony record")
		}
		return nil
	}
	_, err := writeEnvironmentCreateBootstrapJSON(store, relative, document)
	return err
}

func renderEnvironmentCreateDeviceBootstrapJob(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, id, stage string) ([]deploymentplan.Definition, error) {
	if !environmentBootstrapOwnerUUID.MatchString(id) || stage != "ready" && stage != "active" {
		return nil, environmentCreateBootstrapBlocker("Device bootstrap retry requires the original stable UUID and explicit native stage")
	}
	policy := findEnvironmentCreateDomainPolicy(p, "device")
	definitions, err := renderEnvironmentCreatePKIPhase(ctx, cfg, p, "bootstrap", "--consumers", strings.Join(policy.BundleConsumers, ","), "--bootstrap-root-id", id, "--openbao-ca-configmap", "pki-openbao-transport-ca")
	if err != nil {
		return nil, err
	}
	// Each retry retains the original Job and result. Only its object name
	// differs; its immutable Root UUID and consumer policy never do.
	serviceAccount := ""
	for _, definition := range definitions {
		if definition.Object["kind"] == "ServiceAccount" {
			serviceAccount = deploymentplan.ID(definition.Object)
		}
	}
	if serviceAccount == "" {
		return nil, environmentCreateBootstrapBlocker("Device bootstrap requires its exact ServiceAccount dependency")
	}
	for _, definition := range definitions {
		if definition.Object["kind"] == "Job" {
			metadata := deploymentplan.Map(definition.Object["metadata"])
			metadata["name"] = deploymentplan.String(metadata["name"]) + "-" + stage
		}
	}
	for i := range definitions {
		if definitions[i].Object["kind"] == "Job" {
			definitions[i].Dependencies = append(definitions[i].Dependencies, serviceAccount)
		}
	}
	return definitions, nil
}

func validateEnvironmentCreateDeviceRoot(cfg deploymentConfig, root environmentCreateRegisteredIssuer) error {
	if root.Environment != cfg.Environment || root.Domain != "device" || root.Kind != "root" || root.Status != "ready" && root.Status != "active" || !environmentBootstrapOwnerUUID.MatchString(root.ID) || root.Document["signer_provider"] != "openbao" || deploymentplan.String(root.Document["parent_issuer_id"]) != "" || root.Reference != "pki-issuers/device/"+root.ID+"/v1" || environmentPreflightBytesSHA256([]byte(root.ChainPEM)) != root.BundleVersion {
		return environmentCreateBootstrapBlocker("native registered Device Root scope or provider lineage is invalid")
	}
	block, rest := pem.Decode([]byte(root.Certificate))
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return environmentCreateBootstrapBlocker("native registered Device Root certificate is malformed")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !certificate.IsCA || !certificate.BasicConstraintsValid || certificate.CheckSignatureFrom(certificate) != nil || environmentPreflightBytesSHA256(certificate.Raw) != root.RootFingerprint || time.Now().Before(certificate.NotBefore) || !time.Now().Before(certificate.NotAfter) {
		return environmentCreateBootstrapBlocker("native Device Root certificate does not match its original registered fingerprint and validity")
	}
	chainBlock, tail := pem.Decode([]byte(root.ChainPEM))
	if chainBlock == nil || chainBlock.Type != "CERTIFICATE" || !bytes.Equal(chainBlock.Bytes, certificate.Raw) || len(bytes.TrimSpace(tail)) != 0 {
		return environmentCreateBootstrapBlocker("native Device Root chain does not contain only its original self-signed anchor")
	}
	return nil
}

// Configuration installs a public READY Root into the actual API and broker.
// Their native processes verify the registry and installed runtime before they
// send any receipt. The deployment controller never writes acknowledgments.
func configureEnvironmentCreateDeviceConsumers(cfg deploymentConfig, p environmentCreateBootstrapPlan, root environmentCreateRegisteredIssuer, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	if p.Environment != cfg.Environment || p.Stack != cfg.Values["CLOUD_STACK_NAME"] || validateEnvironmentCreateDeviceRoot(cfg, root) != nil {
		return nil, environmentCreateBootstrapBlocker("Device consumers require the actual selected registered Root lineage")
	}
	refs, _ := json.Marshal([]map[string]string{{"issuer_id": root.ID, "trust_bundle_version": root.BundleVersion}})
	namespace := p.Stack + "-video-cloud"
	trustName, trustPath := "pki-create-device-trust", "/run/pki-device"
	controller := "https://pki-controller." + namespace + ".svc:18446"
	apiSettings := map[string]string{"VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED": "true", "VIDEO_CLOUD_AUTH_MTLS_REQUIRED": "true", "VIDEO_CLOUD_AUTH_DISABLE_ACL": "false", "VIDEO_CLOUD_AUTH_TRUSTED_CLIENT_CERT_HEADERS": "false", "VIDEO_CLOUD_AUTH_PRODUCT_PKI_REQUIRE_CRLS": "false", "VIDEO_CLOUD_AUTH_DEVICE_CA_CERT": trustPath + "/roots.pem", "VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST": trustPath + "/bundles.json", "VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_CONTROLLER_URL": controller, "VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_ROOT_ID": "", "VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_STATE": "", "VIDEO_CLOUD_AUTH_DEVICE_AUTOMATIC_STATE_DIR": "", "VIDEO_CLOUD_AUTH_CRL_MANIFEST": "", "VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_STATE": "", "VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_ROOT_ID": ""}
	brokerSettings := map[string]string{"PKI_BROKER_MANAGEMENT_CA": environmentCreateServiceTrust + "/root.pem", "PKI_ENVIRONMENT": cfg.Environment, "PKI_BROKER_REQUIRE_CRLS": "true", "PKI_BROKER_DEVICE_BUNDLE_ACK_ENABLED": "true", "PKI_BROKER_DEVICE_CRL_MANIFEST": trustPath + "/broker-crls.json", "PKI_BROKER_DEVICE_ROOT_ID": root.ID, "PKI_BROKER_DEVICE_ROOT_STATE": "/run/pki-state/device-root.json", "PKI_BROKER_DEVICE_ROOTS": trustPath + "/roots.pem", "PKI_BROKER_PKI_CONTROLLER_URL": controller}
	out := make([]deploymentplan.Definition, 0, len(definitions)+1)
	seen := map[string]bool{}
	for _, definition := range definitions {
		definition.Object = deploymentplan.Clone(definition.Object)
		object := definition.Object
		meta := deploymentplan.Map(object["metadata"])
		name := deploymentplan.String(meta["name"])
		if object["kind"] == "Deployment" && meta["namespace"] == namespace && (name == "video-cloud-api-pki" || name == "mqtt-pki") {
			pod := environmentPKIPod(object)
			target := "app"
			settings := apiSettings
			if name == "mqtt-pki" {
				target, settings = "pkibroker", brokerSettings
			}
			for _, value := range certIssuerObjectList(pod["containers"]) {
				container := deploymentplan.Map(value)
				if container["name"] != target {
					continue
				}
				if seen[name] {
					return nil, environmentCreateBootstrapBlocker("Device consumers contain a duplicate selected native runtime owner")
				}
				seen[name] = true
				container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(settings))
				container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{environmentCreateServiceMount("device-trust", trustPath, true)})
			}
			pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("device-trust", "configMap", trustName)})
			if name == "video-cloud-api-pki" {
				if err := projectIncomingBootstrapManifest(object, "device"); err != nil {
					return nil, err
				}
			}
		}
		out = append(out, definition)
	}
	if !seen["video-cloud-api-pki"] || !seen["mqtt-pki"] {
		return nil, environmentCreateBootstrapBlocker("Device Root distribution requires both concrete desired API and broker runtime owners")
	}
	if err := prepareDeviceBrokerTrustDirectory(out, p.Images["LKE_ALPINE_IMAGE"]); err != nil {
		return nil, err
	}
	out = append(out, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": trustName, "namespace": namespace}, "data": deploymentplan.Object{"roots.pem": root.Certificate, "bundles.json": string(refs), "broker-crls.json": environmentCreateServiceCRLManifest(root, "/run/pki-state/device-crl.json")}}})
	return out, nil
}

func retireEnvironmentCreateDeviceBootstrapRole(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime) error {
	role, policy := "pki-device-bootstrap-"+cfg.Environment, "create-device-root-bootstrap"
	intent, _ := json.Marshal(deploymentplan.Object{"role": role, "policy": policy, "environment": cfg.Environment})
	key := "device-root-bootstrap-retire"
	if err := runtime.Execution.BeginEntry(key, "bootstrap", intent, time.Now().UTC()); err != nil {
		return err
	}
	token, err := readBoundedEnvironmentFile(runtime.OpenBao.RootTokenPath, true)
	if err != nil {
		return environmentCreateBootstrapBlocker("original OpenBao bootstrap role cleanup owner is unavailable")
	}
	script := "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao delete auth/kubernetes/role/" + role + " >/dev/null\nbao policy delete " + policy + " >/dev/null\nprintf '{\"roles\":'\nbao list -format=json auth/kubernetes/role\nprintf ',\"policies\":'\nbao policy list -format=json\nprintf '}'\n"
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, append(bytes.TrimSpace(token), '\n'), script)
	var observed struct {
		Roles    []string `json:"roles"`
		Policies []string `json:"policies"`
	}
	if err != nil || decodeEnvironmentJSON(raw, &observed) != nil || observed.Roles == nil || observed.Policies == nil || slices.Contains(observed.Roles, role) || slices.Contains(observed.Policies, policy) || !slices.Contains(observed.Policies, "create-device-controller") || !slices.Contains(observed.Policies, "create-device-signer") {
		return environmentCreateBootstrapBlocker("native Device bootstrap role removal cannot be verified without losing runtime policy owners")
	}
	if err := runtime.Execution.RecordReceipt(key, raw); err != nil {
		return err
	}
	return runtime.Execution.CompleteEntry(key, time.Now().UTC())
}
