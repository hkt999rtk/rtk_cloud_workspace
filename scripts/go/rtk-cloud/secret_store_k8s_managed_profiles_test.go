package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedPKIManifestResolvesOriginalDirectoryAndProjectedKeys(t *testing.T) {
	for _, mode := range []string{"directory", "subpath", "mapped-key", "foreign-path", "relative", "writeable", "subpath-expression", "unmapped-key", "duplicate-mount"} {
		t.Run(mode, func(t *testing.T) {
			mount := deploymentplan.Object{"name": "trust", "mountPath": "/run/pki-service", "readOnly": true}
			volume := deploymentplan.Object{"name": "trust", "configMap": deploymentplan.Object{"name": "original-service-trust"}}
			path, expectedKey := "/run/pki-service/listener-crls.json", "listener-crls.json"
			if mode == "subpath" || mode == "mapped-key" || mode == "unmapped-key" {
				mount["mountPath"], mount["subPath"] = path, "crls.json"
				expectedKey = "crls.json"
			}
			if mode == "mapped-key" {
				deploymentplan.Map(volume["configMap"])["items"] = []any{deploymentplan.Object{"key": "actual-crls", "path": "crls.json"}}
				expectedKey = "actual-crls"
			}
			if mode == "unmapped-key" {
				deploymentplan.Map(volume["configMap"])["items"] = []any{deploymentplan.Object{"key": "other", "path": "other.json"}}
			}
			if mode == "foreign-path" {
				path = "/run/other/listener-crls.json"
			}
			if mode == "relative" {
				path = "listener-crls.json"
			}
			if mode == "writeable" {
				mount["readOnly"] = false
			}
			if mode == "subpath-expression" {
				mount["subPathExpr"] = "$(OTHER)"
			}
			mounts := []any{mount}
			if mode == "duplicate-mount" {
				mounts = append(mounts, deploymentplan.Clone(mount))
			}
			object := deploymentplan.Object{"spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "native", "volumeMounts": mounts}}, "volumes": []any{volume}}}}}
			raw, _ := json.Marshal(object)
			var d liveDeployment
			if json.Unmarshal(raw, &d) != nil {
				t.Fatal("fixture decode")
			}
			name, key, err := mountedPKICRLConfigMap(d, "native", path)
			valid := mode == "directory" || mode == "subpath" || mode == "mapped-key"
			if valid && (err != nil || name != "original-service-trust" || key != expectedKey) {
				t.Fatal("original mounted CRL source was lost", name, key, err)
			}
			if !valid && err == nil {
				t.Fatal("unqualified mount accepted")
			}
		})
	}
}

func managedRuntimeRegistryDocuments(t *testing.T) ([]map[string]any, map[string]string, string) {
	t.Helper()
	rootPEM, root, rootKey := testSigningCA(t, "runtime Service Root", nil, nil, 2201)
	issuerPEM, _, _ := testSigningCA(t, "runtime Service intermediate", root, rootKey, 2202)
	rootID, issuerID := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	doc := func(id, kind, pem, chain string) map[string]any {
		return map[string]any{"issuer_id": id, "environment": "dev", "trust_domain": "service", "kind": kind, "status": "active", "certificate_pem": pem, "chain_pem": chain, "trust_bundle_version": environmentPreflightBytesSHA256([]byte(chain)), "certificate_fingerprint_sha256": environmentPreflightBytesSHA256(root.Raw)}
	}
	rootDoc, issuerDoc := doc(rootID, "root", rootPEM, rootPEM), doc(issuerID, "intermediate", issuerPEM, issuerPEM+rootPEM)
	issuerDoc["parent_issuer_id"] = rootID
	return []map[string]any{rootDoc, issuerDoc}, map[string]string{rootID: rootDoc["trust_bundle_version"].(string), issuerID: issuerDoc["trust_bundle_version"].(string)}, environmentPreflightBytesSHA256(root.Raw)
}

func TestManagedPKIRegistryUsesMountedVersionedPublicLineage(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "duplicate", "foreign-environment", "foreign-domain", "foreign-root", "changed-version", "changed-parent", "revoked", "unrelated-certificate", "expired-window"} {
		t.Run(mode, func(t *testing.T) {
			docs, versions, root := managedRuntimeRegistryDocuments(t)
			switch mode {
			case "missing":
				docs = docs[:1]
			case "duplicate":
				docs[1] = docs[0]
			case "foreign-environment":
				docs[1]["environment"] = "staging"
			case "foreign-domain":
				docs[1]["trust_domain"] = "mqtt"
			case "foreign-root":
				root = strings.Repeat("f", 64)
			case "changed-version":
				versions[docs[1]["issuer_id"].(string)] = strings.Repeat("f", 64)
			case "changed-parent":
				docs[1]["parent_issuer_id"] = "33333333-3333-4333-8333-333333333333"
			case "revoked":
				docs[1]["status"] = "revoked"
			case "unrelated-certificate":
				docs[1]["certificate_pem"] = docs[0]["certificate_pem"]
			}
			raw, _ := json.Marshal(docs)
			at := time.Now()
			if mode == "expired-window" {
				at = at.Add(20 * 365 * 24 * time.Hour)
			}
			ids, err := validateMountedServiceRegistryIssuers(raw, versions, "dev", root, at)
			if mode == "valid" && (err != nil || len(ids) != 1 || ids[0] != docs[1]["issuer_id"]) {
				t.Fatal("valid native lineage refused", err)
			}
			if mode != "valid" && err == nil {
				t.Fatal("foreign or incomplete native lineage accepted")
			}
		})
	}
}

func TestManagedPKIDeviceProfileKeepsSignedCRLsWithFixedActivationRoot(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-root", "no-api-crl", "trusted-header", "foreign-policy-root", "no-pvc", "missing-app-crl", "missing-ack", "bad-signature"} {
		t.Run(mode, func(t *testing.T) {
			manifest, state, registry := testLiveCRLEvidence(t, "device", false)
			var entries []livePKICRLManifestEntry
			_ = json.Unmarshal([]byte(manifest), &entries)
			root := entries[0].Issuer
			appManifest, appState, appRegistry := testLiveCRLEvidence(t, "app", false)
			appManifest = strings.ReplaceAll(appManifest, "64062c4a-2bdc-474c-bb11-ab1b61026574", "64062c4a-2bdc-474c-bb11-ab1b61026575")
			appManifest = strings.ReplaceAll(appManifest, "/crls/issuer.json", "/crls/app.json")
			appState = strings.ReplaceAll(appState, "64062c4a-2bdc-474c-bb11-ab1b61026574", "64062c4a-2bdc-474c-bb11-ab1b61026575")
			if mode == "missing-ack" {
				registry = strings.Replace(registry, `"ack":true`, `"ack":false`, 1)
			}
			if mode == "bad-signature" {
				state = strings.Replace(state, `"issuer_fingerprint":"`, `"issuer_fingerprint":"f`, 1)
			}
			files := map[string]any{"device.json": map[string]any{"data": map[string]string{"roots.pem": root.Certificate, "api-crls.json": manifest, "broker-crls.json": manifest}}, "app.json": map[string]any{"data": map[string]string{"broker-crls.json": appManifest}}}
			dir := t.TempDir()
			for name, value := range files {
				raw, _ := json.Marshal(value)
				if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			for name, value := range map[string]string{"device-state.json": state, "device-registry.json": registry, "app-state.json": appState, "app-registry.json": appRegistry} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			settings := map[string]map[string]string{
				"pki-controller/pki-controller": {"PKI_ENVIRONMENT": "dev", "PKI_FIXED_DEVICE_ROOT_TRUST": "true", "PKI_DEVICE_ROOT_ID": root.ID, "PKI_DEVICE_ROOT_SHA256": root.Fingerprint},
				"video-cloud-api-pki/app":       {"VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED": "true", "VIDEO_CLOUD_AUTH_MTLS_REQUIRED": "true", "VIDEO_CLOUD_AUTH_PRODUCT_PKI_REQUIRE_CRLS": "true", "VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_ROOT_ID": root.ID, "VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_STATE": "/var/lib/pki-state/device-root.json", "VIDEO_CLOUD_AUTH_DEVICE_AUTOMATIC_STATE_DIR": "/var/lib/pki-state/automatic", "VIDEO_CLOUD_AUTH_CRL_MANIFEST": "/run/pki-device/api-crls.json", "VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST": "/run/pki-device/bundles.json"},
				"mqtt-pki/pkibroker":            {"PKI_ENVIRONMENT": "dev", "PKI_BROKER_REQUIRE_CRLS": "true", "PKI_BROKER_DEVICE_BUNDLE_ACK_ENABLED": "true", "PKI_BROKER_DEVICE_ROOT_ID": root.ID, "PKI_BROKER_DEVICE_ROOT_STATE": "/var/lib/pki-state/device-root.json", "PKI_BROKER_DEVICE_AUTOMATIC_STATE_DIR": "/var/lib/pki-state/automatic", "PKI_BROKER_DEVICE_CRL_MANIFEST": "/run/pki-device/broker-crls.json", "PKI_BROKER_APP_PKI_ENABLED": "true", "PKI_BROKER_APP_BUNDLE_ACK_ENABLED": "true", "PKI_BROKER_APP_CRL_MANIFEST": "/run/pki-app/broker-crls.json"},
			}
			switch mode {
			case "wrong-root":
				settings["pki-controller/pki-controller"]["PKI_DEVICE_ROOT_SHA256"] = strings.Repeat("f", 64)
			case "no-api-crl":
				settings["video-cloud-api-pki/app"]["VIDEO_CLOUD_AUTH_PRODUCT_PKI_REQUIRE_CRLS"] = "false"
			case "trusted-header":
				settings["video-cloud-api-pki/app"]["VIDEO_CLOUD_AUTH_TRUSTED_CLIENT_CERT_HEADERS"] = "true"
			case "foreign-policy-root":
				settings["video-cloud-api-pki/app"]["VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_ROOT_ID"] = "11111111-1111-4111-8111-111111111111"
			case "missing-app-crl":
				settings["mqtt-pki/pkibroker"]["PKI_BROKER_APP_CRL_MANIFEST"] = ""
			}
			var objects []any
			for _, owner := range []struct{ name, container string }{{"pki-controller", "pki-controller"}, {"video-cloud-api-pki", "app"}, {"mqtt-pki", "pkibroker"}} {
				volumes := []any{environmentCreateServiceVolume("device", "configMap", "device"), environmentCreateServiceVolume("app", "configMap", "app"), environmentCreateServiceVolume("state", "persistentVolumeClaim", "original-state")}
				if mode == "no-pvc" {
					volumes = volumes[:2]
				}
				objects = append(objects, deploymentplan.Object{"metadata": deploymentplan.Object{"name": owner.name}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": owner.container, "env": environmentCreateEnv(settings[owner.name+"/"+owner.container]), "volumeMounts": []any{environmentCreateServiceMount("device", "/run/pki-device", true), environmentCreateServiceMount("app", "/run/pki-app", true), environmentCreateServiceMount("state", "/var/lib/pki-state", false)}}}, "volumes": volumes}}}})
			}
			var deployments liveDeploymentList
			raw, _ := json.Marshal(map[string]any{"items": objects})
			_ = json.Unmarshal(raw, &deployments)
			deploymentRuntimeTestKubectl(t, `case "$*" in
 *'get configmap device'*) cat "$RTK_MANAGED_DEVICE_FIXTURE/device.json";;
 *'get configmap app'*) cat "$RTK_MANAGED_DEVICE_FIXTURE/app.json";;
 *'get pods'*) printf '%s' '{"items":[{"metadata":{"name":"postgresql-0"}}]}' ;;
 *'exec deployment/'*'cat /var/lib/pki-state/crls/app.json'*) cat "$RTK_MANAGED_DEVICE_FIXTURE/app-state.json";;
 *'exec deployment/'*) cat "$RTK_MANAGED_DEVICE_FIXTURE/device-state.json";;
 *"consumer_id='video-cloud-api-pki'"*|*"consumer_id='mqtt-pki'"*) printf '%s' '{"ack":false}';;
 *'64062c4a-2bdc-474c-bb11-ab1b61026575'*) cat "$RTK_MANAGED_DEVICE_FIXTURE/app-registry.json";;
 *'64062c4a-2bdc-474c-bb11-ab1b61026574'*) cat "$RTK_MANAGED_DEVICE_FIXTURE/device-registry.json";;
 *) exit 99;;
esac`)
			t.Setenv("RTK_MANAGED_DEVICE_FIXTURE", dir)
			err := verifyAutomaticDeviceTrustConsumers("dev", "isolated", "video-cloud-dev-video-cloud", deployments, time.Now(), newDeploymentCheckRuntime(context.Background(), "dev"))
			if mode == "valid" && err != nil {
				t.Fatal("native Device CRL profile rejected", err)
			}
			if mode != "valid" && err == nil {
				t.Fatal("invalid managed Device trust accepted")
			}
		})
	}
}

func TestManagedPKICRLAcknowledgmentUsesExactNativeEnrollmentRole(t *testing.T) {
	cfg, p, runtime, service := createServiceRecipeFixture(t, "dev")
	owners := append(environmentCreateApplicationOwners(), environmentCreateApplicationOwner{Name: "account-manager", Container: "pkimanagement", Role: "account-manager"}, environmentCreateApplicationOwner{Name: "certissuer", Container: "certissuer", Role: "certissuer"})
	for _, owner := range owners {
		t.Run(owner.Name, func(t *testing.T) {
			// The original native enrollment primitive binds the issued subject.
			// The fake registry returns a row only for that exact receipt member;
			// a Deployment alias must never manufacture ACK membership.
			job, err := environmentCreateClientEnrollmentDefinition(cfg, p, runtime, service, p.Stack+"-video-cloud", owner.Role, "isolated-original-identity")
			if err != nil {
				t.Fatal(err)
			}
			containers := certIssuerObjectList(environmentPKIPod(job.Object)["containers"])
			command := append(certIssuerObjectList(deploymentplan.Map(containers[0])["command"]), certIssuerObjectList(deploymentplan.Map(containers[0])["args"])...)
			nativeSubject := "service:" + owner.Role
			found := false
			for _, arg := range command {
				if arg == nativeSubject {
					found = true
				}
			}
			if !found {
				t.Fatal("native producer did not declare the exact Service subject")
			}
			manifest, state, registry := testLiveCRLEvidence(t, "service", false)
			var entries []livePKICRLManifestEntry
			_ = json.Unmarshal([]byte(manifest), &entries)
			dir := t.TempDir()
			_ = os.WriteFile(filepath.Join(dir, "state.json"), []byte(state), 0600)
			_ = os.WriteFile(filepath.Join(dir, "registry.json"), []byte(registry), 0600)
			deploymentRuntimeTestKubectl(t, `case "$*" in
 *'get pods'*) printf '%s' '{"items":[{"metadata":{"name":"postgresql-0"}}]}' ;;
 *'exec deployment/'*) cat "$RTK_NATIVE_ACK_FIXTURE/state.json";;
 *"consumer_id='`+strings.TrimPrefix(nativeSubject, "service:")+`'"*) cat "$RTK_NATIVE_ACK_FIXTURE/registry.json";;
 *) printf '%s' '{"ack":false}' ;;
esac`)
			t.Setenv("RTK_NATIVE_ACK_FIXTURE", dir)
			if err := verifyLivePKICRLEvidence("isolated", "video-cloud-dev-video-cloud", "dev", owner.Name, owner.Container, entries[0], time.Now(), newDeploymentCheckRuntime(context.Background(), "dev")); err != nil {
				t.Fatal("original native receipt role was lost", err)
			}
			if owner.Name != owner.Role {
				deploymentRuntimeTestKubectl(t, `case "$*" in
 *'get pods'*) printf '%s' '{"items":[{"metadata":{"name":"postgresql-0"}}]}' ;;
 *'exec deployment/'*) cat "$RTK_NATIVE_ACK_FIXTURE/state.json";;
 *"consumer_id='`+owner.Name+`'"*) cat "$RTK_NATIVE_ACK_FIXTURE/registry.json";;
 *) printf '%s' '{"ack":false}' ;;
esac`)
				if err := verifyLivePKICRLEvidence("isolated", "video-cloud-dev-video-cloud", "dev", owner.Name, owner.Container, entries[0], time.Now(), newDeploymentCheckRuntime(context.Background(), "dev")); err == nil {
					t.Fatal("Deployment alias accepted as native acknowledgment")
				}
			}
		})
	}
}

// Exercise the aggregate checker used by both formal qualification and final
// health, with actual managed outbound paths and no deployment-only issuer env.
func TestManagedPKIRuntimeAggregateUsesNativeOriginsAndSignedCurrentEvidence(t *testing.T) {
	for _, mode := range []string{"valid", "foreign-registry", "missing-ack", "changed-origin", "indirect-origin", "bad-crl", "expired-crl"} {
		t.Run(mode, func(t *testing.T) {
			files := map[string]string{}
			for index, role := range []string{"account", "certissuer", "openbao"} {
				domain := "service"
				if role == "openbao" {
					domain = "openbao_tls"
				}
				manifest, state, registry := testLiveCRLEvidence(t, domain, mode == "expired-crl" && role == "account")
				id := fmt.Sprintf("64062c4a-2bdc-474c-bb11-ab1b6102657%d", 4+index)
				manifest, state = strings.ReplaceAll(manifest, "64062c4a-2bdc-474c-bb11-ab1b61026574", id), strings.ReplaceAll(state, "64062c4a-2bdc-474c-bb11-ab1b61026574", id)
				if role == "openbao" {
					manifest = strings.ReplaceAll(manifest, "/crls/issuer.json", "/crls/openbao.json")
				}
				if mode == "bad-crl" && role == "account" {
					state = strings.Replace(state, `"issuer_fingerprint":"`, `"issuer_fingerprint":"f`, 1)
				}
				cm, _ := json.Marshal(map[string]any{"data": map[string]string{"crls.json": manifest, "listener-crls.json": manifest, "outbound-crls.json": manifest}})
				files[role+"-manifest.json"], files[role+"-state.json"], files[role+"-registry.json"] = string(cm), state, registry
			}
			store := testRuntimeMountedCRLStore(t, "dev", files)
			dir := os.Getenv("RTK_TEST_CRL_SOURCE")
			var account liveDeploymentList
			raw, _ := os.ReadFile(filepath.Join(dir, "account-deployments.json"))
			_ = json.Unmarshal(raw, &account)
			objectRaw, _ := json.Marshal(account.Items[0])
			var object deploymentplan.Object
			_ = json.Unmarshal(objectRaw, &object)
			c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(object)["containers"])[0])
			fields := map[string]string{"PKI_MANAGEMENT_IDENTITY_STATE": "/var/lib/pki-state/private/client.json"}
			for _, prefix := range []string{"PKI_MANAGEMENT_CONTROLLER", "PKI_MANAGEMENT_ISSUER"} {
				fields[prefix+"_URL"] = "https://controller.internal:18446"
				fields[prefix+"_SERVER_PKI_NAME"] = "controller.internal"
				fields[prefix+"_SERVER_PKI_ROOT_SHA256"] = strings.Repeat("a", 64)
				fields[prefix+"_TLS_CA"] = "/run/root.pem"
				fields[prefix+"_PKI_CONTROLLER_URL"] = "https://controller.internal:18446"
				fields[prefix+"_MANAGEMENT_CA"] = "/run/root.pem"
				fields[prefix+"_SERVER_CRL_MANIFEST"] = "/run/service-crls/outbound-crls.json"
			}
			if mode == "changed-origin" {
				fields["PKI_MANAGEMENT_CONTROLLER_SERVER_PKI_NAME"] = "foreign.internal"
			}
			c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(fields))
			if mode == "indirect-origin" {
				for _, field := range certIssuerObjectList(c["env"]) {
					f := deploymentplan.Map(field)
					for _, suffix := range []string{"_SERVER_CRL_MANIFEST", "_PKI_CONTROLLER_URL", "_MANAGEMENT_CA"} {
						if f["name"] == "PKI_MANAGEMENT_CONTROLLER"+suffix {
							delete(f, "value")
							f["valueFrom"] = deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "unknown-origin", "key": "value"}}
						}
					}
				}
			}
			raw, _ = json.Marshal(map[string]any{"items": []any{object}})
			_ = os.WriteFile(filepath.Join(dir, "account-deployments.json"), raw, 0600)
			docs, versions, root := managedRuntimeRegistryDocuments(t)
			var refs []map[string]string
			for id, version := range versions {
				refs = append(refs, map[string]string{"issuer_id": id, "trust_bundle_version": version})
			}
			manifest, _ := json.Marshal(refs)
			cm, _ := json.Marshal(map[string]any{"data": map[string]string{"bundles.json": string(manifest)}})
			raw, _ = json.Marshal(docs)
			_ = os.WriteFile(filepath.Join(dir, "registry-documents.json"), raw, 0600)
			_ = os.WriteFile(filepath.Join(dir, "registry-bundle.json"), cm, 0600)
			var video liveDeploymentList
			raw, _ = os.ReadFile(filepath.Join(dir, "video-deployments.json"))
			_ = json.Unmarshal(raw, &video)
			controller := deploymentplan.Object{"metadata": deploymentplan.Object{"name": "pki-controller", "generation": 1}, "status": deploymentplan.Object{"observedGeneration": 1, "updatedReplicas": 1, "availableReplicas": 1}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "pki-controller", "env": environmentCreateEnv(map[string]string{"PKI_SERVICE_CLIENT_ROOT_SHA256": root, "PKI_SERVICE_CLIENT_BUNDLE_MANIFEST": "/run/pki-service/bundles.json", "PKI_REQUIRED_CONSUMERS_SERVICE": "account-manager"}), "volumeMounts": []any{environmentCreateServiceMount("trust", "/run/pki-service", true)}}}, "volumes": []any{environmentCreateServiceVolume("trust", "configMap", "registry-bundle")}}}}}
			var d liveDeployment
			raw, _ = json.Marshal(controller)
			_ = json.Unmarshal(raw, &d)
			video.Items = append(video.Items, d)
			raw, _ = json.Marshal(video)
			_ = os.WriteFile(filepath.Join(dir, "video-deployments.json"), raw, 0600)
			zero, count, missing := 0, 3, 0
			if mode == "missing-ack" {
				missing = 1
			}
			if mode == "foreign-registry" {
				root = strings.Repeat("e", 64)
			}
			report := serviceClientInventoryReport{Status: "service-client-registry-inventory-checked", Environment: "dev", Domain: "service", IssuerID: docs[1]["issuer_id"].(string), RootFingerprint: root, CheckedAt: time.Now().UTC(), Issuances: &count, Pending: &zero, Invalid: &zero, Revoked: &zero, Unpublished: &zero, MissingAcknowledgments: &missing}
			raw, _ = json.Marshal(report)
			_ = os.WriteFile(filepath.Join(dir, "inventory.json"), raw, 0600)
			kubectl := os.Getenv("RTK_CLOUD_KUBECTL")
			original, _ := os.ReadFile(kubectl)
			additional := `case "$*" in
 *'get configmap registry-bundle'*) cat "$RTK_TEST_CRL_SOURCE/registry-bundle.json"; exit;;
 *'json_agg(document)'*) cat "$RTK_TEST_CRL_SOURCE/registry-documents.json"; exit;;
 *'recovery-inventory-service-client'*) cat "$RTK_TEST_CRL_SOURCE/inventory.json"; exit;;
 *'pki_deployment_bootstrap_sessions'*) printf '%s' '{"active_caller_index":true,"sessions":[]}'; exit;;
esac
`
			_ = os.WriteFile(kubectl, []byte(strings.Replace(string(original), "#!/bin/sh\n", "#!/bin/sh\n"+additional, 1)), 0700)
			store.checkRuntime = newDeploymentCheckRuntime(context.Background(), "dev")
			err := verifySecretStoreK8SRuntime(store, time.Now())
			if mode == "valid" && err != nil {
				t.Fatal("current managed runtime rejected", err)
			}
			if mode != "valid" && err == nil {
				t.Fatal("invalid current native evidence accepted")
			}
		})
	}
}
