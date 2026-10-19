package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The real provider callers install the future managed CA in their TLS pools
// while the separately recorded bootstrap CA still authenticates current
// OpenBao traffic. Native prepared-bundle hooks emit the callers' own receipts;
// registry admission is enabled only after the managed server exists.
func renderEnvironmentCreatePreparedOpenBaoConsumers(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, root environmentCreateRegisteredIssuer, additional ...environmentCreateRegisteredIssuer) ([]deploymentplan.Definition, error) {
	if root.Environment != p.Environment || root.Domain != "openbao_tls" || root.Kind != "root" || (root.Status != "ready" && root.Status != "active") || !environmentPreflightDigest.MatchString(root.RootFingerprint) || environmentPreflightBytesSHA256([]byte(root.ChainPEM)) != root.BundleVersion {
		return nil, environmentCreateBootstrapBlocker("prepared OpenBao consumers require actual original ready or active managed Root metadata")
	}
	refs := []map[string]string{{"issuer_id": root.ID, "trust_bundle_version": root.BundleVersion}}
	for _, issuer := range additional {
		if issuer.Environment != root.Environment || issuer.Domain != root.Domain || issuer.Kind != "intermediate" || issuer.RootFingerprint != root.RootFingerprint || (issuer.Status != "ready" && issuer.Status != "active") || !environmentBootstrapOwnerUUID.MatchString(issuer.ID) || issuer.Document["parent_issuer_id"] != root.ID || environmentPreflightBytesSHA256([]byte(issuer.ChainPEM)) != issuer.BundleVersion {
			return nil, environmentCreateBootstrapBlocker("prepared OpenBao intermediate differs from the actual managed parent lineage")
		}
		refs = append(refs, map[string]string{"issuer_id": issuer.ID, "trust_bundle_version": issuer.BundleVersion})
	}
	manifest, err := json.Marshal(refs)
	if err != nil {
		return nil, err
	}
	originalCA, err := readBoundedEnvironmentFile(runtime.OpenBao.CACertPath, true)
	if err != nil {
		return nil, environmentCreateBootstrapBlocker("the original private OpenBao bootstrap transport authority cannot be read")
	}
	definitions, err := renderEnvironmentCreateManagedServiceDefinitions(ctx, cfg, p, runtime, s)
	if err != nil {
		return nil, err
	}
	trustName, trustPath := "pki-create-openbao-tls-consumer-trust", "/run/pki-openbao-create"
	trust := deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": trustName, "namespace": p.Stack + "-video-cloud"}, "data": deploymentplan.Object{"roots.pem": root.Certificate + "\n" + string(originalCA), "bundles.json": string(manifest)}}}
	definitions = append(definitions, trust)
	for index, d := range definitions {
		if d.Object["kind"] != "Deployment" {
			continue
		}
		name := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"])
		if name != "pki-controller" && name != "certissuer" {
			continue
		}
		definitions[index].Phase = "workload"
		pod := environmentPKIPod(d.Object)
		c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
		settings := map[string]string{"OPENBAO_CACERT": trustPath + "/roots.pem", "OPENBAO_SERVER_BUNDLE_MANIFEST": trustPath + "/bundles.json", "OPENBAO_SERVER_BUNDLE_ROOT_SHA256": root.RootFingerprint, "OPENBAO_SERVER_BUNDLE_PKI_CONTROLLER_URL": "https://pki-controller." + p.Stack + "-video-cloud.svc:18446", "OPENBAO_SERVER_BUNDLE_MANAGEMENT_CA": environmentCreateServiceTrust + "/root.pem"}
		c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(settings))
		c["volumeMounts"] = mergeEnvironmentNamedFields(c["volumeMounts"], []any{environmentCreateServiceMount("openbao-create-trust", trustPath, true)})
		pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("openbao-create-trust", "configMap", trustName)})
		meta := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(d.Object["spec"])["template"])["metadata"])
		annotations := deploymentplan.Map(meta["annotations"])
		if annotations == nil {
			annotations = deploymentplan.Object{}
			meta["annotations"] = annotations
		}
		annotations["rtk.realtek.com/pki-create-openbao-bundle-sha256"] = environmentPreflightBytesSHA256(manifest)
	}
	return definitions, nil
}

// MQTT's distribution callback is owned by the actual API/log-ingester
// consumers. It must install and acknowledge each ready lineage before native
// activation; an adapter cannot report those consumers' success itself.
func executeEnvironmentCreateTransportLineages(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, operator map[string]string, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, distributeMQTT func(environmentCreateRegisteredIssuer, ...environmentCreateRegisteredIssuer) error, apply environmentCreateBootstrapApply) (map[string]environmentCreateRegisteredIssuer, map[string]environmentCreateRegisteredIssuer, map[string]string, error) {
	roots, intermediates, rootCRLs := map[string]environmentCreateRegisteredIssuer{}, map[string]environmentCreateRegisteredIssuer{}, map[string]string{}
	if distributeMQTT == nil || apply == nil {
		return roots, intermediates, rootCRLs, environmentCreateBootstrapBlocker("managed transport creation requires native MQTT and OpenBao consumers for ready-authority distribution")
	}
	for _, domain := range []string{"mqtt", "openbao_tls"} {
		if r := runtime.ServiceSessionRecovery; r != nil && r.PostTransportResume && domain == "mqtt" {
			roots[domain], intermediates[domain] = *r.MQTTRoot, *r.MQTTIntermediate
			for _, v := range runtime.Execution.Journal.Entries {
				if v.Key == "pki-initial-root-crl-mqtt" && v.State == "completed" {
					var receipt struct {
						File string `json:"crl_file"`
						SHA  string `json:"crl_pem_sha256"`
					}
					if json.Unmarshal(v.Receipt, &receipt) != nil {
						return roots, intermediates, rootCRLs, environmentCreateBootstrapBlocker("original MQTT CRL receipt changed")
					}
					raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, receipt.File), true)
					if err != nil || environmentPreflightBytesSHA256(raw) != receipt.SHA {
						return roots, intermediates, rootCRLs, environmentCreateBootstrapBlocker("original MQTT CRL changed")
					}
					rootCRLs[domain] = receipt.File
				}
			}
			if rootCRLs[domain] == "" {
				return roots, intermediates, rootCRLs, environmentCreateBootstrapBlocker("original MQTT CRL receipt missing")
			}
			continue
		}
		if r := runtime.ServiceSessionRecovery; r != nil && (r.DeviceBootstrapRecovery != nil || r.DeviceTrustRecovery != nil) && domain == "openbao_tls" {
			roots[domain], intermediates[domain] = *r.OpenBaoRoot, *r.OpenBaoIntermediate
			var err error
			rootCRLs[domain], err = originalOpenBaoRootCRL(store, runtime.Execution, roots[domain])
			if err != nil {
				return roots, intermediates, rootCRLs, err
			}
			continue
		}
		distribute := distributeMQTT
		if domain == "openbao_tls" {
			distribute = func(root environmentCreateRegisteredIssuer, additional ...environmentCreateRegisteredIssuer) error {
				definitions, err := renderEnvironmentCreatePreparedOpenBaoConsumers(ctx, cfg, p, runtime, s, root, additional...)
				if err != nil {
					return err
				}
				stage := "openbao-root-trust"
				if r := runtime.ServiceSessionRecovery; r != nil && r.PostTransportResume && len(additional) == 0 {
					stage += "-name-recovery-" + r.Review
				}
				if len(additional) > 0 {
					stage = "openbao-intermediate-trust"
				}
				if err := apply(ctx, stage, definitions); err != nil {
					return err
				}
				if r := runtime.ServiceSessionRecovery; r != nil && r.PostTransportResume && len(additional) == 0 {
					if err := runtime.Execution.RecordReceipt("kubernetes/openbao-root-trust", mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_ready_root": root.ID})); err != nil {
						return err
					}
					return runtime.Execution.CompleteEntry("kubernetes/openbao-root-trust", time.Now().UTC())
				}
				return nil
			}
		}
		root, intermediate, crl, err := executeEnvironmentCreateIssuerLineage(ctx, cfg, p, store, operator, runtime, findEnvironmentCreateDomainPolicy(p, domain), distribute)
		if err != nil {
			return roots, intermediates, rootCRLs, err
		}
		roots[domain], intermediates[domain], rootCRLs[domain] = root, intermediate, crl
	}
	return roots, intermediates, rootCRLs, nil
}

// This renders the actual current certissuer with named native transport signer
// routes. All keys remain on their original owners and the roles grant only the
// approved immutable issuer mounts; the bootstrap provider connection remains
// on its authenticated transport until its host replacement is ready.
func renderEnvironmentCreateTransportEnrollmentIssuer(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, roots, intermediates map[string]environmentCreateRegisteredIssuer) ([]deploymentplan.Definition, error) {
	if err := validateEnvironmentCreateTransportIssuers(cfg, p, runtime, s, roots, intermediates); err != nil {
		return nil, err
	}
	definitions, err := renderEnvironmentCreatePreparedOpenBaoConsumers(ctx, cfg, p, runtime, s, roots["openbao_tls"], intermediates["openbao_tls"])
	if err != nil {
		return nil, err
	}
	for _, d := range definitions {
		if deploymentplan.ID(d.Object) != "Deployment/"+p.Stack+"-video-cloud/certissuer" {
			continue
		}
		c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["containers"])[0])
		mqttNames, err := environmentCreateTransportNames(p, "mqtt")
		if err != nil {
			return nil, err
		}
		baoNames, err := environmentCreateTransportNames(p, "openbao_tls")
		if err != nil {
			return nil, err
		}
		settings := map[string]string{"CERT_ISSUER_MQTT_SERVER_PKI_ROOT_SHA256": roots["mqtt"].RootFingerprint, "CERT_ISSUER_MQTT_SERVER_DNS_NAMES": strings.Join(mqttNames, ","), "CERT_ISSUER_MQTT_SERVER_CLIENT_CN_PATTERN": "^service:emqx-pki$", "CERT_ISSUER_OPENBAO_HOST_PKI_ROOT_SHA256": roots["openbao_tls"].RootFingerprint, "CERT_ISSUER_OPENBAO_HOST_PKI_VERIFY_ROOT_SHA256": roots["openbao_tls"].RootFingerprint, "CERT_ISSUER_OPENBAO_HOST_DNS_NAMES": strings.Join(baoNames, ","), "CERT_ISSUER_OPENBAO_HOST_CLIENT_CN_PATTERN": "^service:openbao$"}
		c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(settings))
	}
	return definitions, nil
}

// Call after the other domain policy/ACL ceremonies which still need the
// bootstrap provider transport. The runtime context is preserved so OnDelete
// replacement unseals only the original initialized OpenBao store/share.
func executeEnvironmentCreateManagedTransports(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, roots, intermediates map[string]environmentCreateRegisteredIssuer, apply environmentCreateBootstrapApply) error {
	if runtime.Execution == nil || apply == nil {
		return environmentCreateBootstrapBlocker("managed transport cutover requires its original parent execution and exact Kubernetes stage adapter")
	}
	if err := runtime.Execution.Check(ctx); err != nil {
		return err
	}
	if err := validateEnvironmentCreateTransportIssuers(cfg, p, runtime, s, roots, intermediates); err != nil {
		return err
	}
	policies := []string{"create-" + s.Intermediate.ID + "-signer", "create-" + s.Intermediate.ID + "-service-client", "create-" + intermediates["mqtt"].ID + "-signer", "create-" + intermediates["openbao_tls"].ID + "-signer"}
	// Preserve prior native certissuer policy bindings, including App's approved
	// mount if it was configured before this transport stage.
	for _, entry := range runtime.Execution.Journal.Entries {
		if entry.State != "completed" || !strings.HasPrefix(entry.Key, "openbao-role-certissuer-"+cfg.Environment+"-") {
			continue
		}
		var receipt struct {
			Role     string   `json:"role"`
			Policies []string `json:"policies"`
		}
		if json.Unmarshal(entry.Receipt, &receipt) != nil || receipt.Role != "certissuer-"+cfg.Environment {
			return environmentCreateBootstrapBlocker("original native certissuer role receipt cannot be preserved")
		}
		policies = append(policies, receipt.Policies...)
	}
	slices.Sort(policies)
	policies = slices.Compact(policies)
	writtenOpenBao := managedTransportOpenBaoPullRepair(runtime.ServiceSessionRecovery)
	if writtenOpenBao {
		if err := verifyManagedTransportRecordedOpenBaoRole(runtime, cfg, "certissuer-"+cfg.Environment, []string{"certissuer"}, policies); err != nil {
			return err
		}
	} else if err := configureEnvironmentCreateOpenBaoRole(ctx, cfg, runtime, "certissuer-"+cfg.Environment, []string{"certissuer"}, policies); err != nil {
		return err
	}
	partial := runtime.ServiceSessionRecovery != nil && runtime.ServiceSessionRecovery.ManagedTransportRecovery != nil && runtime.ServiceSessionRecovery.ManagedTransportRecovery.Partial != nil
	if !partial {
		issuer, err := renderEnvironmentCreateTransportEnrollmentIssuer(ctx, cfg, p, runtime, s, roots, intermediates)
		if err != nil {
			return err
		}
		if err := apply(ctx, "transport-enrollment-issuer", issuer); err != nil {
			return err
		}
	}
	// Client enrollment already completed before Device/App bootstrap. Verify
	// that exact accepted stage; never replay or replace its owned keys.
	clientStage := "kubernetes/transport-client-enrollment"
	const clientRenewalPrefix = "kubernetes/transport-client-enrollment-renewal-"
	for _, entry := range runtime.Execution.Journal.Entries {
		if !strings.HasPrefix(entry.Key, clientRenewalPrefix) {
			continue
		}
		review := strings.TrimPrefix(entry.Key, clientRenewalPrefix)
		if !environmentPreflightDigest.MatchString(review) || entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 {
			return environmentCreateBootstrapBlocker("transport client renewal must have exact completed original evidence")
		}
		if _, _, err := loadAdmittedCreateRecovery(runtime.Execution.Store, runtime.Execution, review); err != nil {
			return err
		}
		clientStage = entry.Key
	}
	clients, clientJournal, err := acceptedCreateStage(runtime.Execution.Store, runtime.Execution, clientStage)
	if err != nil {
		return err
	}
	for _, resource := range clients.Resources {
		actual, err := runtime.Backend.Read(ctx, resource.ID)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != clientJournal.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) {
			return environmentCreateBootstrapBlocker("accepted transport client owner changed")
		}
	}
	if !partial {
		servers, err := renderEnvironmentCreateTransportServerDefinitions(ctx, cfg, p, runtime, s, roots, intermediates)
		if err != nil {
			return err
		}
		if err := apply(ctx, "transport-server-enrollment", servers); err != nil {
			return err
		}
	}
	managed, err := renderEnvironmentCreateTransportDefinitions(ctx, cfg, p, runtime, s, roots, intermediates, release)
	if err != nil {
		return err
	}
	if partial {
		return executeManagedTransportPartialForward(ctx, cfg, runtime, managed, apply)
	}
	return apply(ctx, "transport-managed-hosts", managed)
}

// Apply to maintained desired core workload recipes only after the managed
// OpenBao server is ready. This removes bootstrap transport trust from both
// actual provider callers and enables their native registry/CRL admission.
func applyEnvironmentCreateManagedOpenBaoConsumers(p environmentCreateBootstrapPlan, s environmentCreateServiceCeremony, roots, intermediates map[string]environmentCreateRegisteredIssuer, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	r, i := roots["openbao_tls"], intermediates["openbao_tls"]
	if r.Environment != p.Environment || r.Domain != "openbao_tls" || r.Status != "active" || i.Environment != p.Environment || i.Domain != r.Domain || i.Status != "active" || i.RootFingerprint != r.RootFingerprint || i.Document["parent_issuer_id"] != r.ID {
		return nil, environmentCreateBootstrapBlocker("managed OpenBao consumer cutover requires the original active server lineage")
	}
	manifest, err := json.Marshal([]map[string]string{{"issuer_id": r.ID, "trust_bundle_version": r.BundleVersion}, {"issuer_id": i.ID, "trust_bundle_version": i.BundleVersion}})
	if err != nil {
		return nil, err
	}
	trustPath, trustName := "/run/pki-openbao-create", "pki-create-openbao-tls-consumer-trust"
	out := make([]deploymentplan.Definition, 0, len(definitions)+1)
	found := map[string]bool{}
	for _, definition := range definitions {
		d := definition
		d.Object = deploymentplan.Clone(d.Object)
		if deploymentplan.ID(d.Object) == "ConfigMap/"+p.Stack+"-video-cloud/"+trustName {
			continue
		}
		if d.Object["kind"] == "Deployment" {
			name := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"])
			if (name == "pki-controller" || name == "certissuer") && deploymentplan.Map(d.Object["metadata"])["namespace"] == p.Stack+"-video-cloud" {
				found[name] = true
				d.Phase = "workload"
				pod := environmentPKIPod(d.Object)
				c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
				settings := map[string]string{"OPENBAO_CACERT": trustPath + "/roots.pem", "OPENBAO_SERVER_BUNDLE_MANIFEST": trustPath + "/bundles.json", "OPENBAO_SERVER_BUNDLE_ROOT_SHA256": r.RootFingerprint, "OPENBAO_SERVER_BUNDLE_PKI_CONTROLLER_URL": "https://pki-controller." + p.Stack + "-video-cloud.svc:18446", "OPENBAO_SERVER_BUNDLE_MANAGEMENT_CA": environmentCreateServiceTrust + "/root.pem", "OPENBAO_SERVER_PKI_ROOT_SHA256": r.RootFingerprint, "OPENBAO_SERVER_PKI_NAME": "openbao." + p.Stack + "-secrets.svc", "OPENBAO_SERVER_PKI_SWEEP_INTERVAL": "10s", "OPENBAO_SERVER_CRL_MANIFEST": trustPath + "/crls.json", "OPENBAO_PKI_CONTROLLER_URL": "https://pki-controller." + p.Stack + "-video-cloud.svc:18446", "OPENBAO_MANAGEMENT_CA": environmentCreateServiceTrust + "/root.pem"}
				c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(settings))
				c["volumeMounts"] = mergeEnvironmentNamedFields(c["volumeMounts"], []any{environmentCreateServiceMount("openbao-create-trust", trustPath, true)})
				pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume("openbao-create-trust", "configMap", trustName)})
			}
		}
		out = append(out, d)
	}
	if !found["pki-controller"] || !found["certissuer"] {
		return nil, environmentCreateBootstrapBlocker("both exact native provider consumers must enter managed OpenBao admission together")
	}
	trust := deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": trustName, "namespace": p.Stack + "-video-cloud"}, "data": deploymentplan.Object{"roots.pem": r.Certificate, "bundles.json": string(manifest), "crls.json": environmentCreateServiceCRLManifest(r, environmentCreateServiceState+"/private/crl/openbao-root.json", i)}}}
	return append(out, trust), nil
}
