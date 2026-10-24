package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func createDeviceRecipeFixture(t *testing.T, environment string) (deploymentConfig, environmentCreateBootstrapPlan, environmentCreateRegisteredIssuer) {
	t.Helper()
	cfg, p, _, service := createServiceRecipeFixture(t, environment)
	root := service.Root
	root.Domain, root.Status, root.Reference = "device", "ready", "pki-issuers/device/"+root.ID+"/v1"
	root.Document = deploymentplan.Clone(root.Document)
	root.Document["trust_domain"], root.Document["status"], root.Document["signer_reference"] = root.Domain, root.Status, root.Reference
	p.Domains = append(p.Domains, environmentCreateIssuerPolicy{Domain: "device", Consumers: []string{"pkibroker", "video-cloud-api"}, BundleConsumers: []string{"pkibroker", "video-cloud-api"}})
	return cfg, p, root
}

func TestCreateDeviceConsumersInstallNativeReadyRootIntoBothActualOwners(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			cfg, p, root := createDeviceRecipeFixture(t, environment)
			makeOwner := func(name string, containers []any) deploymentplan.Definition {
				return deploymentplan.Definition{Phase: "workload", Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": name, "namespace": p.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": name}}, "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": containers, "volumes": []any{environmentCreateServiceVolume("pki-state", "persistentVolumeClaim", name+"-trust")}}}}}}
			}
			api := deploymentplan.Object{"name": "app", "image": p.Image, "env": environmentCreateEnv(map[string]string{"VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED": "false", "VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_STATE": "/run/pki-state/app-root.json", "VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE": "/run/pki-state/api-client.json"}), "volumeMounts": []any{environmentCreateServiceMount("pki-state", "/run/pki-state", false)}}
			broker := deploymentplan.Object{"name": "pkibroker", "image": p.Image, "env": environmentCreateEnv(map[string]string{"PKI_BROKER_DEVICE_BUNDLE_ACK_ENABLED": "false", "PKI_BROKER_IDENTITY_STATE": environmentCreateBrokerIdentity}), "volumeMounts": []any{environmentCreateServiceMount("pki-state", "/run/pki-state", false)}}
			mqtt := deploymentplan.Object{"name": "mqtt", "image": "original-formal-mqtt-image", "env": environmentCreateEnv(map[string]string{"EMQX_PKI_HOST_IDENTITY_STATE": environmentCreateMQTTIdentity + "/state.json"})}
			desired := []deploymentplan.Definition{makeOwner("video-cloud-api-pki", []any{api}), makeOwner("mqtt-pki", []any{mqtt, broker})}
			before := deploymentplan.Digest(desired)
			configured, err := configureEnvironmentCreateDeviceConsumers(cfg, p, root, desired)
			if err != nil || len(configured) != len(desired)+1 || deploymentplan.Digest(desired) != before {
				t.Fatal("Device distribution did not preserve the original desired objects", err)
			}
			apiObject := createRecipeObject(t, configured, "Deployment/"+p.Stack+"-video-cloud/video-cloud-api-pki")
			apiContainer := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(apiObject)["containers"])[0])
			apiEnv := createRecipeEnv(apiContainer)
			if apiEnv["VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED"] != "true" || apiEnv["VIDEO_CLOUD_AUTH_MTLS_REQUIRED"] != "true" || apiEnv["VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST"] != "/run/pki-device-manifest/bundles.json" || apiEnv["VIDEO_CLOUD_AUTH_DEVICE_CA_CERT"] != "/run/pki-device/roots.pem" || apiEnv["VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_STATE"] != "" || apiEnv["VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_STATE"] != "" || apiEnv["VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE"] != "/run/pki-state/api-client.json" || apiContainer["image"] != p.Image {
				t.Fatal("API cannot natively acknowledge the ready installed Device Root or lost its original Service owner", apiEnv)
			}
			mqttObject := createRecipeObject(t, configured, "Deployment/"+p.Stack+"-video-cloud/mqtt-pki")
			containers := certIssuerObjectList(environmentPKIPod(mqttObject)["containers"])
			if deploymentplan.Digest(containers[0]) != deploymentplan.Digest(mqtt) {
				t.Fatal("Device trust changed MQTT's native server identity")
			}
			brokerEnv := createRecipeEnv(deploymentplan.Map(containers[1]))
			if brokerEnv["PKI_BROKER_MANAGEMENT_CA"] != environmentCreateServiceTrust+"/root.pem" || brokerEnv["PKI_BROKER_DEVICE_BUNDLE_ACK_ENABLED"] != "true" || brokerEnv["PKI_BROKER_REQUIRE_CRLS"] != "true" || brokerEnv["PKI_BROKER_DEVICE_ROOT_ID"] != root.ID || brokerEnv["PKI_BROKER_DEVICE_ROOT_STATE"] != "/run/pki-state/device-root.json" || brokerEnv["PKI_BROKER_IDENTITY_STATE"] != environmentCreateBrokerIdentity || brokerEnv["PKI_BROKER_DEVICE_CRL_MANIFEST"] != "/run/pki-device/broker-crls.json" {
				t.Fatal("broker lacks its real registry and installed-bundle acknowledgment policy", brokerEnv)
			}
			trust := createRecipeObject(t, configured, "ConfigMap/"+p.Stack+"-video-cloud/pki-create-device-trust")
			data := deploymentplan.Map(trust["data"])
			var refs []map[string]string
			var crls []struct {
				Issuer    map[string]any `json:"issuer"`
				StatePath string         `json:"state_path"`
			}
			if data["roots.pem"] != root.Certificate || json.Unmarshal([]byte(deploymentplan.String(data["bundles.json"])), &refs) != nil || !reflect.DeepEqual(refs, []map[string]string{{"issuer_id": root.ID, "trust_bundle_version": root.BundleVersion}}) || json.Unmarshal([]byte(deploymentplan.String(data["broker-crls.json"])), &crls) != nil || len(crls) != 1 || deploymentplan.Digest(crls[0].Issuer) != deploymentplan.Digest(root.Document) || crls[0].StatePath != "/run/pki-state/device-crl.json" {
				t.Fatal("Device distribution manufactured public identity rather than using the original registry document", data)
			}
		})
	}
}

func TestCreateDeviceConsumersRejectForeignAndIncompleteNativeIdentity(t *testing.T) {
	for _, mutation := range []string{"environment", "domain", "provider", "reference", "parent", "fingerprint", "bundle", "chain", "status", "missing-owners"} {
		t.Run(mutation, func(t *testing.T) {
			cfg, p, root := createDeviceRecipeFixture(t, "dev")
			switch mutation {
			case "environment":
				root.Environment = "staging"
			case "domain":
				root.Domain = "service"
			case "provider":
				root.Document["signer_provider"] = "offline"
			case "reference":
				root.Reference = "pki-issuers/device/foreign/v1"
			case "parent":
				root.Document["parent_issuer_id"] = "foreign-parent"
			case "fingerprint":
				root.RootFingerprint = strings.Repeat("0", 64)
			case "bundle":
				root.BundleVersion = strings.Repeat("0", 64)
			case "chain":
				root.ChainPEM += root.Certificate
				root.BundleVersion = environmentPreflightBytesSHA256([]byte(root.ChainPEM))
			case "status":
				root.Status = "pending"
			}
			if _, err := configureEnvironmentCreateDeviceConsumers(cfg, p, root, nil); err == nil {
				t.Fatal("Device distribution accepted foreign/incomplete original metadata or lacked both actual consumers")
			}
		})
	}
}

func TestCreateDeviceBootstrapRetryUsesSameUUIDAndExclusiveNativeIdentity(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			cfg, p, root := createDeviceRecipeFixture(t, environment)
			workspace, err := filepath.Abs("../../..")
			if err != nil {
				t.Fatal(err)
			}
			cfg.Workspace = workspace
			path := filepath.Join(workspace, "repos", "rtk_video_cloud", "deploy", "pki", "render-staging-controller.py")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			p.Templates = map[string]string{"deploy/pki/render-staging-controller.py": environmentPreflightBytesSHA256(raw)}
			var jobs []deploymentplan.Object
			for _, stage := range []string{"ready", "active"} {
				defs, err := renderEnvironmentCreateDeviceBootstrapJob(context.Background(), cfg, p, root.ID, stage)
				if err != nil {
					t.Fatal(err)
				}
				if len(defs) != 2 || defs[0].Object["kind"] != "ServiceAccount" || deploymentplan.Map(defs[0].Object["metadata"])["name"] != "pki-device-bootstrap" || defs[1].Object["kind"] != "Job" {
					t.Fatal("native Device bootstrap reused a daily controller owner or did not preserve separate SA/Job")
				}
				// The executor uses compiler order, not renderer slice order.
				compiled, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}, defs, nil)
				if err != nil || len(compiled.Resources) != 2 || compiled.Resources[0].Desired["kind"] != "ServiceAccount" || compiled.Resources[1].Desired["kind"] != "Job" || !reflect.DeepEqual(compiled.Resources[1].Dependencies, []string{compiled.Resources[0].ID}) {
					t.Fatal("compiled Device Job precedes its ServiceAccount", err)
				}
				job := defs[1].Object
				pod := environmentPKIPod(job)
				container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
				env := createRecipeEnv(container)
				if pod["serviceAccountName"] != "pki-device-bootstrap" || env["OPENBAO_KUBERNETES_ROLE"] != "pki-device-bootstrap-"+environment || env["PKI_REQUIRED_BUNDLE_CONSUMERS_DEVICE"] != "pkibroker,video-cloud-api" || deploymentplan.Digest(container["args"]) != deploymentplan.Digest([]string{"bootstrap-device-root", root.ID}) || container["image"] != p.Image {
					t.Fatal("Device retry replaced its original Root, policy or reviewed native image", pod)
				}
				jobs = append(jobs, job)
			}
			if deploymentplan.Map(jobs[0]["metadata"])["name"] == deploymentplan.Map(jobs[1]["metadata"])["name"] || deploymentplan.Digest(jobs[0]["spec"]) != deploymentplan.Digest(jobs[1]["spec"]) {
				t.Fatal("Device retry overwrote original Job evidence or changed its original native ceremony")
			}
			if _, err := renderEnvironmentCreateDeviceBootstrapJob(context.Background(), cfg, p, "foreign", "active"); err == nil {
				t.Fatal("Device retry accepted replacement root metadata")
			}
		})
	}
}

func TestCreateDeviceRootRejectsMissingParentExecutionBeforeMutation(t *testing.T) {
	cfg, p, _ := createDeviceRecipeFixture(t, "dev")
	store := makeIsolatedTestSecretStore(t, "dev")
	before := snapshotCreateTargetStore(t, store.Root)
	_, err := executeEnvironmentCreateDeviceRoot(context.Background(), cfg, p, store, nil, environmentCreateBootstrapRuntime{}, func(context.Context, string, []deploymentplan.Definition) error {
		t.Fatal("missing native parent reached Kubernetes mutations")
		return nil
	}, func(environmentCreateRegisteredIssuer) error {
		t.Fatal("missing native parent reached trust consumers")
		return nil
	})
	if err == nil || !reflect.DeepEqual(before, snapshotCreateTargetStore(t, store.Root)) {
		t.Fatal("missing Device parent generated replacement identity or success evidence", err)
	}
}

func TestCreateDeviceTrustContinuationReusesOnlyExactOriginalReadyRecord(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	sha := strings.Repeat("a", 64)
	_, _, root := createDeviceRecipeFixture(t, "dev")
	recovery := &createDeviceTrustRecovery{Root: root}
	if err := recordEnvironmentCreateDeviceIssuer(store, sha, "ready", root.Document, nil); err != nil {
		t.Fatal(err)
	}
	before := snapshotCreateTargetStore(t, store.Root)
	if err := recordEnvironmentCreateDeviceIssuer(store, sha, "ready", root.Document, recovery); err != nil {
		t.Fatal("qualified retry rewrote retained ceremony", err)
	}
	if !reflect.DeepEqual(before, snapshotCreateTargetStore(t, store.Root)) {
		t.Fatal("retry modified original record")
	}
	changed := deploymentplan.Clone(root.Document)
	changed["issuer_id"] = "foreign"
	if recordEnvironmentCreateDeviceIssuer(store, sha, "ready", changed, recovery) == nil {
		t.Fatal("changed native metadata accepted")
	}
	if recordEnvironmentCreateDeviceIssuer(store, sha, "ready", root.Document, nil) == nil {
		t.Fatal("fresh ceremony overwrote original record")
	}
	active := deploymentplan.Clone(root.Document)
	active["status"] = "active"
	if err := recordEnvironmentCreateDeviceIssuer(store, sha, "active", active, recovery); err != nil {
		t.Fatal(err)
	}
	if recordEnvironmentCreateDeviceIssuer(store, sha, "active", active, recovery) == nil {
		t.Fatal("activation record was overwritten")
	}
	if err := os.Remove(filepath.Join(store.Root, "deployment/create", sha, "bootstrap/device/root-ready.json")); err != nil {
		t.Fatal(err)
	}
	if recordEnvironmentCreateDeviceIssuer(store, sha, "ready", root.Document, recovery) == nil {
		t.Fatal("missing original record was regenerated")
	}
}
