package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func createApplicationFinalizeFixture(t *testing.T, environment string) (deploymentConfig, environmentCreateBootstrapPlan, environmentRelease, environmentCreateBootstrapRuntime, environmentCreateServiceCeremony, map[string]environmentCreateRegisteredIssuer, map[string]environmentCreateRegisteredIssuer, []deploymentplan.Definition) {
	t.Helper()
	cfg, p, runtime, service := createServiceRecipeFixture(t, environment)
	service.Root.Reference = "pki-issuers/service/" + service.Root.ID + "/v1"
	service.Intermediate.Reference = "pki-issuers/service/" + service.Intermediate.ID + "/v1"
	service.Root.Document["signer_reference"] = service.Root.Reference
	service.Intermediate.Document["signer_reference"] = service.Intermediate.Reference
	cfg.Values["OPENBAO_STORAGE_BACKEND"], cfg.Values["OPENBAO_EFFECTIVE_REPLICAS"] = "file", "1"
	_, _, release := completeEnvironmentRenderFixture(environment)
	p.Images = map[string]string{}
	for role, image := range release.Images {
		p.Images[role] = image.Reference
	}
	roots, intermediates := map[string]environmentCreateRegisteredIssuer{}, map[string]environmentCreateRegisteredIssuer{}
	for index, domain := range []string{"device", "app", "mqtt", "openbao_tls"} {
		root, issuer := service.Root, service.Intermediate
		root.Domain, root.Document = domain, deploymentplan.Clone(root.Document)
		issuer.Domain, issuer.Document = domain, deploymentplan.Clone(issuer.Document)
		root.ID = []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "cccccccc-cccc-4ccc-8ccc-cccccccccccc", "dddddddd-dddd-4ddd-8ddd-dddddddddddd"}[index]
		issuer.ID = []string{"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", "ffffffff-ffff-4fff-8fff-ffffffffffff", "11111111-2222-4333-8444-555555555555", "22222222-3333-4444-8555-666666666666"}[index]
		root.Reference = "pki-issuers/" + domain + "/" + root.ID + "/v1"
		issuer.Reference = "pki-issuers/" + domain + "/" + issuer.ID + "/v1"
		root.Document["trust_domain"], root.Document["issuer_id"], root.Document["signer_reference"] = domain, root.ID, root.Reference
		issuer.Document["trust_domain"], issuer.Document["issuer_id"], issuer.Document["parent_issuer_id"], issuer.Document["signer_reference"] = domain, issuer.ID, root.ID, issuer.Reference
		roots[domain], intermediates[domain] = root, issuer
		policy := environmentCreateIssuerPolicy{Domain: domain, Consumers: []string{"actual-consumer"}, BundleConsumers: []string{"actual-consumer"}}
		if domain == "mqtt" {
			policy.ServerDNSNames = []string{"mqtt-pki." + p.Stack + "-video-cloud.svc"}
		}
		if domain == "openbao_tls" {
			policy.ServerDNSNames = []string{"openbao." + p.Stack + "-secrets.svc"}
		}
		p.Domains = append(p.Domains, policy)
	}
	var definitions []deploymentplan.Definition
	for _, owner := range environmentCreateApplicationOwners() {
		settings := environmentCreateApplicationClientSettings(p, service, owner)
		settings["RTK_DEPLOYMENT_BOOTSTRAP_TRUST_ADDR"] = "0.0.0.0:19310"
		container := deploymentplan.Object{"name": owner.Container, "image": p.Image, "command": []any{"/app/native", "--deployment-bootstrap-trust"}, "env": environmentCreateEnv(settings), "volumeMounts": []any{environmentCreateServiceMount("service-identity", owner.StateRoot, false), environmentCreateServiceMount("service-trust", environmentCreateServiceTrust, true)}}
		definitions = append(definitions, deploymentplan.Definition{Phase: "bootstrap", Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": owner.Name, "namespace": p.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"replicas": float64(1), "strategy": deploymentplan.Object{"type": "Recreate"}, "selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app.kubernetes.io/name": owner.Name}}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app.kubernetes.io/name": owner.Name}}, "spec": deploymentplan.Object{"containers": []any{container}, "volumes": []any{environmentCreateServiceVolume("service-identity", "persistentVolumeClaim", owner.Claim), environmentCreateServiceVolume("service-trust", "configMap", "pki-create-service-trust")}}}}}})
	}
	return cfg, p, release, runtime, service, roots, intermediates, definitions
}

func TestCreateApplicationFinalizationPreservesEnrolledOwnersAndUsesNativeTrust(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			cfg, p, release, runtime, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, environment)
			before := deploymentplan.Digest(initial)
			final, err := finalizeEnvironmentCreateApplicationDefinitions(context.Background(), cfg, p, release, runtime, service, roots, intermediates, initial)
			if err != nil {
				t.Fatal(err)
			}
			if deploymentplan.Digest(initial) != before {
				t.Fatal("finalization changed initial enrolled evidence")
			}
			for _, owner := range environmentCreateApplicationOwners() {
				object := createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/"+owner.Name)
				pod := environmentPKIPod(object)
				var container deploymentplan.Object
				for _, value := range certIssuerObjectList(pod["containers"]) {
					if deploymentplan.Map(value)["name"] == owner.Container {
						container = deploymentplan.Map(value)
					}
				}
				if container == nil {
					t.Fatal("native runtime owner remains absent or temporary")
				}
				for _, value := range certIssuerObjectList(container["command"]) {
					if value == "--deployment-bootstrap-trust" {
						t.Fatal("temporary readiness command survived finalization")
					}
				}
				settings := createRecipeEnv(container)
				prefix := "VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE"
				if owner.Role == "video-cloud-logingester" {
					prefix = "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_STATE"
				}
				if owner.Role == "factory-enroll" {
					prefix = "FACTORY_ENROLL_SERVICE_IDENTITY_STATE"
				}
				if owner.Role == "pkibroker" {
					prefix = "PKI_BROKER_IDENTITY_STATE"
				}
				if settings[prefix] != owner.ClientState || settings["PKI_BOOTSTRAP_SESSION_ID"] != service.BootstrapSessionID || settings["RTK_DEPLOYMENT_BOOTSTRAP_TRUST_ADDR"] != "" {
					t.Fatal("normal runtime replaced enrolled client state, lost its installation session, or retained a temporary startup address", owner.Name, settings)
				}
				mountPaths := map[string]bool{}
				for _, value := range certIssuerObjectList(container["volumeMounts"]) {
					mount := deploymentplan.Map(value)
					path := deploymentplan.String(mount["mountPath"])
					if mountPaths[path] {
						t.Fatal("final runtime has overlapping duplicate mounts", owner.Name, path)
					}
					mountPaths[path] = true
				}
				if owner.Name == "video-cloud-api-pki" || owner.Name == "video-cloud-api-app-pki" {
					wantedSecret := owner.Name + "-tls"
					var found bool
					for _, value := range certIssuerObjectList(pod["volumes"]) {
						volume := deploymentplan.Map(value)
						found = found || volume["name"] == "pki-server" && deploymentplan.Map(volume["secret"])["secretName"] == wantedSecret
					}
					if !found || settings["VIDEO_CLOUD_API_TLS_KEY_PATH"] != "/run/pki-server/tls.key" {
						t.Fatal("API used an unowned TLS key reference")
					}
					if settings["VIDEO_CLOUD_AUTH_DEVICE_CA_CERT"] != "" || settings["VIDEO_CLOUD_AUTH_APP_CA_CERT"] != "" {
						t.Fatal("final dynamic API trust retained a conflicting bootstrap static CA")
					}
					if settings["VIDEO_CLOUD_AUTH_ROOT_TRUST_BOOTSTRAP_STATE"] != filepath.Join(owner.StateRoot, "private", "incoming-root-bootstrap.json") {
						t.Fatal("native first-policy admission lost its original prepared owner state")
					}
					for _, value := range certIssuerObjectList(pod["initContainers"]) {
						command := certIssuerObjectList(deploymentplan.Map(value)["command"])
						if len(command) == 3 && strings.Contains(deploymentplan.String(command[2]), "cp ") {
							t.Fatal("fresh finalization fabricated an initial trust policy")
						}
					}
					binding := environmentPodBindings{Kind: "Deployment", NamespaceSuffix: "video-cloud", Name: owner.Name, Component: "managed-api", PodFields: map[string]any{"volumes": pod["volumes"]}, Containers: []environmentContainerBindings{{Name: "app", ImageKey: "LKE_VIDEO_CLOUD_IMAGE", Fields: map[string]any{"env": container["env"], "volumeMounts": container["volumeMounts"]}}}}
					if owner.Name == "video-cloud-api-app-pki" {
						binding.Component = "managed-app-api"
						binding.NativeTrustInitialization = true
					}
					for _, value := range certIssuerObjectList(pod["initContainers"]) {
						initializer := deploymentplan.Map(value)
						if binding.Component == "managed-api" {
							if err := observeDeviceInitializer(&binding, initializer, release, deploymentplan.ID(object)); err != nil {
								t.Fatal("native Device initializer cannot be reobserved for upgrade", err)
							}
						}
						binding.InitContainers = append(binding.InitContainers, environmentContainerBindings{Name: deploymentplan.String(initializer["name"]), ImageKey: "LKE_VIDEO_CLOUD_IMAGE", Fields: map[string]any{"volumeMounts": initializer["volumeMounts"]}})
					}
					if !binding.NativeTrustInitialization {
						t.Fatal("native trust initialization was lost in canonical observation")
					}
					if binding.Component == "managed-app-api" {
						if err := validateEnvironmentAppBinding(binding); err != nil {
							t.Fatal("final native App owner cannot be canonically adopted", err)
						}
					}
				}
			}
			trust := createRecipeObject(t, final, "ConfigMap/"+p.Stack+"-video-cloud/pki-create-device-trust")
			data := deploymentplan.Map(trust["data"])
			if data["roots.pem"] != roots["device"].Certificate || data["root-trust.json"] != nil || !strings.Contains(deploymentplan.String(data["api-crls.json"]), roots["device"].ID) {
				t.Fatal("finalization invented Root policy or lost native issuer metadata")
			}
		})
	}
}

func TestCreateApplicationFinalizationReattestsRenewedSessionFromRetainedClients(t *testing.T) {
	cfg, p, release, runtime, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	originalSession := service.BootstrapSessionID
	service.BootstrapSessionID = "12345678-1234-4234-8234-123456789abc"
	service.BootstrapCredentialSecret = "service-bootstrap-" + service.BootstrapSessionID
	service.CleanupBinding.SessionID = service.BootstrapSessionID
	if originalSession == service.BootstrapSessionID {
		t.Fatal("fixture did not replace the expired seed")
	}
	unrelated := deploymentplan.Definition{Phase: "workload", Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "ordinary-worker", "namespace": p.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "worker", "image": p.Image}}}}}}}
	initial = append(initial, unrelated)
	before := deploymentplan.Digest(initial)
	final, err := finalizeEnvironmentCreateApplicationDefinitions(context.Background(), cfg, p, release, runtime, service, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Digest(initial) != before {
		t.Fatal("reattestation changed the original enrolled evidence")
	}
	if got := createRecipeObject(t, final, deploymentplan.ID(unrelated.Object)); deploymentplan.Digest(got) != deploymentplan.Digest(unrelated.Object) {
		t.Fatal("session reattestation changed an unrelated ordinary app")
	}
	for _, definition := range final {
		if definition.Object["kind"] == "Job" && strings.HasPrefix(deploymentplan.String(deploymentplan.Map(definition.Object["metadata"])["name"]), "pki-enroll-") {
			t.Fatal("retained native owners were replayed through client enrollment")
		}
	}
	for _, owner := range environmentCreateApplicationOwners() {
		object := createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/"+owner.Name)
		pod := environmentPKIPod(object)
		var container deploymentplan.Object
		for _, value := range certIssuerObjectList(pod["containers"]) {
			if deploymentplan.Map(value)["name"] == owner.Container {
				container = deploymentplan.Map(value)
			}
		}
		settings := createRecipeEnv(container)
		if settings["PKI_BOOTSTRAP_SESSION_ID"] != service.BootstrapSessionID {
			t.Fatal("native retained owner cannot attest the renewed session", owner.Name)
		}
		prefix := "VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE"
		switch owner.Role {
		case "video-cloud-logingester":
			prefix = "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_STATE"
		case "factory-enroll":
			prefix = "FACTORY_ENROLL_SERVICE_IDENTITY_STATE"
		case "pkibroker":
			prefix = "PKI_BROKER_IDENTITY_STATE"
		}
		if settings[prefix] != owner.ClientState {
			t.Fatal("reattestation replaced the original managed client state", owner.Name)
		}
		var retainedClaim bool
		for _, value := range certIssuerObjectList(pod["volumes"]) {
			volume := deploymentplan.Map(value)
			retainedClaim = retainedClaim || volume["name"] == "service-state" && deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"] == owner.Claim
			if volume["name"] == "bootstrap" {
				t.Fatal("reattestation introduced a bootstrap credential mount", owner.Name)
			}
		}
		if !retainedClaim {
			t.Fatal("reattestation lost the original native claim", owner.Name)
		}
		for name := range settings {
			if strings.HasSuffix(name, "_IDENTITY_BOOTSTRAP_CERT") || strings.HasSuffix(name, "_IDENTITY_BOOTSTRAP_KEY") {
				t.Fatal("reattestation introduced first-enrollment credentials", owner.Name)
			}
		}
	}
}

func TestCreateApplicationFinalizationRefreshesReadyMQTTManifestFromActiveNativeDocuments(t *testing.T) {
	cfg, p, release, runtime, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	readyRoot, readyIssuer := roots["mqtt"], intermediates["mqtt"]
	readyRoot.Status, readyIssuer.Status = "ready", "ready"
	readyRoot.Document, readyIssuer.Document = deploymentplan.Clone(readyRoot.Document), deploymentplan.Clone(readyIssuer.Document)
	readyRoot.Document["status"], readyIssuer.Document["status"] = "ready", "ready"
	initial, err := configureEnvironmentCreateMQTTConsumers(p, service, readyRoot, []environmentCreateRegisteredIssuer{readyIssuer}, initial)
	if err != nil {
		t.Fatal(err)
	}
	before := deploymentplan.Digest(initial)
	final, err := finalizeEnvironmentCreateApplicationDefinitions(context.Background(), cfg, p, release, runtime, service, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Digest(initial) != before {
		t.Fatal("finalization rewrote the earlier actual READY evidence")
	}
	trust := createRecipeObject(t, final, "ConfigMap/"+p.Stack+"-video-cloud/pki-create-mqtt-consumer-trust")
	data := deploymentplan.Map(trust["data"])
	var entries []struct {
		Issuer    deploymentplan.Object `json:"issuer"`
		StatePath string                `json:"state_path"`
	}
	if err := json.Unmarshal([]byte(deploymentplan.String(data["crls.json"])), &entries); err != nil || len(entries) != 2 {
		t.Fatal("normal MQTT manifest lacks its exact native Root/intermediate", err)
	}
	for index, issuer := range []environmentCreateRegisteredIssuer{roots["mqtt"], intermediates["mqtt"]} {
		if deploymentplan.Digest(entries[index].Issuer) != deploymentplan.Digest(issuer.Document) || entries[index].Issuer["status"] != "active" || !strings.HasPrefix(entries[index].StatePath, "/var/lib/mqtt-trust/") {
			t.Fatal("normal consumer retained READY metadata or fabricated its activation", entries[index])
		}
	}
	readyIssuer.Status = "ready"
	intermediates["mqtt"] = readyIssuer
	if _, err := finalizeEnvironmentCreateApplicationDefinitions(context.Background(), cfg, p, release, runtime, service, roots, intermediates, initial); err == nil {
		t.Fatal("finalization admitted an unactivated native MQTT intermediate")
	}
}

func TestCreateMQTTTrustInstallsConfigBeforeConsumerPods(t *testing.T) {
	_, p, _, _, service, roots, _, initial := createApplicationFinalizeFixture(t, "dev")
	root := roots["mqtt"]
	root.Status = "ready"
	root.Document = deploymentplan.Clone(root.Document)
	root.Document["status"] = "ready"
	defs, err := configureEnvironmentCreateMQTTConsumers(p, service, root, nil, initial)
	if err != nil {
		t.Fatal(err)
	}
	defs = environmentCreateApplicationDefinitionsFor(defs, "video-cloud-api", "video-cloud-logingester")
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: p.Environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest("cfg"), ReleaseSHA256: deploymentplan.Digest("release"), BindingsSHA256: deploymentplan.Digest("mqtt")}, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	positions := map[string]int{}
	for i, r := range plan.Resources {
		positions[r.ID] = i
	}
	for _, r := range plan.Resources {
		if r.Desired["kind"] != "Deployment" {
			continue
		}
		for _, v := range certIssuerObjectList(environmentPKIPod(r.Desired)["volumes"]) {
			volume := deploymentplan.Map(v)
			for _, kind := range []string{"secret", "configMap"} {
				reference := deploymentplan.Map(volume[kind])
				key := "name"
				resourceKind := "ConfigMap"
				if kind == "secret" {
					key = "secretName"
					resourceKind = "Secret"
				}
				id := resourceKind + "/" + p.Stack + "-video-cloud/" + deploymentplan.String(reference[key])
				if before, ok := positions[id]; ok && before >= positions[r.ID] {
					t.Fatal("consumer Pod would wait before its mount prerequisite", r.ID, id)
				}
			}
		}
	}
}

func TestFreshTrustPreparationRefusesPriorStateBeforeChangingOwnership(t *testing.T) {
	_, p, _ := createDeviceRecipeFixture(t, "dev")
	job, err := environmentCreateApplicationTrustPreparation(p, "video-cloud-api-pki-trust")
	if err != nil {
		t.Fatal(err)
	}
	container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(job.Object)["containers"])[0])
	script := deploymentplan.String(certIssuerObjectList(container["command"])[2])
	root := t.TempDir()
	state := filepath.Join(root, "original-root-policy.json")
	if err := os.WriteFile(state, []byte("original-native-policy"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	local := strings.ReplaceAll(script, "/state", shellSingleQuote(root))
	if err := exec.Command("/bin/sh", "-ec", local).Run(); err == nil {
		t.Fatal("preexisting trust state was reclassified as a new empty claim")
	}
	actual, err := os.ReadFile(state)
	if err != nil || string(actual) != "original-native-policy" {
		t.Fatal("unexpected prior trust state was changed", err)
	}
	after, err := os.Stat(root)
	if err != nil || before.Mode() != after.Mode() {
		t.Fatal("preexisting trust ownership metadata was changed before rejection", err)
	}
	if strings.Contains(script, "chown -R") || strings.Contains(script, "cp ") {
		t.Fatal("fresh claim recipe recursively rewrites an original owner or copies policy")
	}
	if _, err := environmentCreateApplicationTrustPreparation(p, "original-service-identity"); err == nil {
		t.Fatal("fresh trust preparation accepted an enrolled identity claim")
	}
}

func TestNativeAppTrustBindingRequiresOriginalManagedClientClaim(t *testing.T) {
	for _, changed := range []string{"", "claim", "state"} {
		t.Run(changed, func(t *testing.T) {
			claim := "video-cloud-api-app-service-state"
			mounts := []any{environmentCreateServiceMount("pki-server", "/run/pki-server", true), environmentCreateServiceMount("pki-app", "/run/pki-app", true), environmentCreateServiceMount("pki-state", "/run/pki-state", false), environmentCreateServiceMount("pki-management", "/run/pki-management", true), environmentCreateServiceMount("service-identity", "/var/lib/video-cloud-app-service", false)}
			state := "/var/lib/video-cloud-app-service/private/client.json"
			if changed == "state" {
				state = "/foreign/client.json"
			}
			if changed == "claim" {
				claim = "foreign-identity"
			}
			binding := environmentPodBindings{Kind: "Deployment", NamespaceSuffix: "video-cloud", Name: "video-cloud-api-app-pki", Component: "managed-app-api", NativeTrustInitialization: true, PodFields: map[string]any{"volumes": []any{environmentCreateServiceVolume("pki-server", "secret", "video-cloud-api-app-pki-tls"), environmentCreateServiceVolume("pki-app", "configMap", "pki-create-app-trust"), environmentCreateServiceVolume("pki-state", "persistentVolumeClaim", "video-cloud-api-app-pki-trust"), environmentCreateServiceVolume("pki-management", "persistentVolumeClaim", claim), environmentCreateServiceVolume("service-identity", "persistentVolumeClaim", claim)}}, Containers: []environmentContainerBindings{{Name: "app", ImageKey: "LKE_VIDEO_CLOUD_IMAGE", Fields: map[string]any{"volumeMounts": mounts, "env": environmentCreateEnv(map[string]string{"VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE": state})}}}, InitContainers: []environmentContainerBindings{{Name: "initialize-app-trust-state", ImageKey: "LKE_VIDEO_CLOUD_IMAGE", Fields: map[string]any{"volumeMounts": []any{environmentCreateServiceMount("pki-state", "/run/pki-state", false), environmentCreateServiceMount("pki-app", "/run/pki-app", true)}}}}}
			err := validateEnvironmentAppBinding(binding)
			if changed == "" && err != nil || changed != "" && err == nil {
				t.Fatal("native App trust accepted changed original claim or rejected managed identity", err)
			}
		})
	}
	if reflect.DeepEqual(environmentAppTrustInitialize, environmentAppNativeTrustInitialize) {
		t.Fatal("native runtime trust replaced the existing static initializer contract")
	}
}

func TestFinalIssuerRetainsEveryNativeSigningDomainAndControllerRoot(t *testing.T) {
	cfg, p, _, runtime, service, roots, intermediates, _ := createApplicationFinalizeFixture(t, "dev")
	roots["service"], intermediates["service"] = service.Root, service.Intermediate
	definitions, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	before := deploymentplan.Digest(definitions)
	final, err := configureEnvironmentCreateFinalIssuer(p, roots, intermediates, definitions)
	if err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Digest(definitions) != before {
		t.Fatal("final signer configuration rewrote original Service evidence")
	}
	issuer := createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/certissuer")
	container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(issuer)["containers"])[0])
	settings := createRecipeEnv(container)
	for key, wanted := range map[string]string{"CERT_ISSUER_CREATE_SERVICE_ENROLLMENT_ONLY": "false", "CERT_ISSUER_OPENBAO_PKI_MOUNT": intermediates["service"].Reference, "CERT_ISSUER_APP_OPENBAO_PKI_MOUNT": intermediates["app"].Reference, "CERT_ISSUER_MQTT_SERVER_PKI_ROOT_SHA256": roots["mqtt"].RootFingerprint, "CERT_ISSUER_OPENBAO_HOST_PKI_ROOT_SHA256": roots["openbao_tls"].RootFingerprint, "CERT_ISSUER_SERVICE_CLIENT_ROOT_SHA256": service.Root.RootFingerprint} {
		if settings[key] != wanted {
			t.Fatal("final native issuer lost an original signing mount/domain pin", key, settings[key], wanted)
		}
	}
	controller := createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/pki-controller")
	controlEnv := createRecipeEnv(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(controller)["containers"])[0]))
	if controlEnv["PKI_DEVICE_ROOT_ID"] != roots["device"].ID || controlEnv["PKI_DEVICE_ROOT_SHA256"] != roots["device"].RootFingerprint {
		t.Fatal("final controller re-render lost actual Device Root identity", controlEnv)
	}
	root := roots["app"]
	root.Status = "ready"
	roots["app"] = root
	if _, err := configureEnvironmentCreateFinalIssuer(p, roots, intermediates, definitions); err == nil {
		t.Fatal("pending native signing domain entered normal final issuer runtime")
	}
}

func TestFinalIssuerRejectsNonCanonicalSigningReferenceBeforeRuntimeValidation(t *testing.T) {
	cfg, p, _, runtime, service, roots, intermediates, _ := createApplicationFinalizeFixture(t, "dev")
	roots["service"], intermediates["service"] = service.Root, service.Intermediate
	for _, domain := range []string{"service", "app", "mqtt", "openbao_tls"} {
		issuer := intermediates[domain]
		issuer.Reference = "pki-issuers/" + domain + "/" + issuer.ID + "/v1"
		issuer.Document["signer_reference"] = issuer.Reference
		intermediates[domain] = issuer
	}
	broken := intermediates["service"]
	broken.Reference = "isolated-issuer/1"
	broken.Document["signer_reference"] = broken.Reference
	intermediates["service"] = broken
	definitions, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := configureEnvironmentCreateFinalIssuer(p, roots, intermediates, definitions); err == nil || !strings.Contains(err.Error(), "signing reference") {
		t.Fatal("noncanonical signer reference reached late registry-only runtime validation", err)
	}
}

func TestCreateTrustBootstrapOmitsWorkerCredentialsOnly(t *testing.T) {
	c := deploymentplan.Object{"env": []any{
		deploymentplan.Object{"name": "unused-worker", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "video-cloud-workers-runtime", "key": "token"}}},
		deploymentplan.Object{"name": "bootstrap-database", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "pki-verifier-database", "key": "url"}}},
		deploymentplan.Object{"name": "runtime", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "video-cloud-runtime", "key": "token"}}},
		deploymentplan.Object{"name": "setting", "value": "retained"},
	}}
	removeCreateBootstrapWorkerSecretRefs(c)
	values := certIssuerObjectList(c["env"])
	if len(values) != 3 || deploymentplan.Map(values[0])["name"] != "bootstrap-database" || deploymentplan.Map(values[1])["name"] != "runtime" || deploymentplan.Map(values[2])["value"] != "retained" {
		t.Fatal("bootstrap removed required configuration or retained worker dependency")
	}
}

func TestCreateBundleChangesRollOriginalTrustConsumers(t *testing.T) {
	_, p, _, _, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	for _, domain := range []string{"mqtt", "app"} {
		var first, second []deploymentplan.Definition
		var err error
		if domain == "mqtt" {
			first, err = configureEnvironmentCreateMQTTConsumers(p, service, roots[domain], nil, initial)
			if err == nil {
				second, err = configureEnvironmentCreateMQTTConsumers(p, service, roots[domain], []environmentCreateRegisteredIssuer{intermediates[domain]}, first)
			}
		} else {
			first, err = configureEnvironmentCreateAppConsumers(p, roots[domain], nil, initial)
			if err == nil {
				second, err = configureEnvironmentCreateAppConsumers(p, roots[domain], []environmentCreateRegisteredIssuer{intermediates[domain]}, first)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, a := range first {
			if a.Object["kind"] != "Deployment" {
				continue
			}
			id := deploymentplan.ID(a.Object)
			for _, z := range second {
				if deploymentplan.ID(z.Object) != id {
					continue
				}
				annotation := func(d deploymentplan.Definition) any {
					return deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(d.Object["spec"])["template"])["metadata"])["annotations"])["rtk.realtek.com/pki-create-"+domain+"-bundle-sha256"]
				}
				if annotation(a) != nil {
					count++
					if annotation(a) == annotation(z) {
						t.Fatal("changed bundle cannot reload its native consumer", id)
					}
					if deploymentplan.Map(deploymentplan.Map(z.Object["spec"])["strategy"])["type"] != "Recreate" {
						t.Fatal("bundle rollout permits overlapping identity owners")
					}
				}
			}
		}
		if count != 2 {
			t.Fatal("both native consumers must roll for changed bundle", domain, count)
		}
	}
}

func TestCreateTransportServiceClientsPrecedeFutureTrustDomains(t *testing.T) {
	cfg, p, runtime, service := createServiceRecipeFixture(t, "dev")
	service.BootstrapCredentialNamespaces = append(service.BootstrapCredentialNamespaces, p.Stack+"-secrets")
	defs, err := renderEnvironmentCreateTransportClientDefinitions(context.Background(), cfg, p, runtime, service, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobs := map[string]bool{}
	for _, d := range defs {
		if d.Object["kind"] == "Job" {
			c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["containers"])[0])
			command := certIssuerObjectList(c["command"])
			if len(command) != 2 || command[0] != "/app/serviceidentity-bootstrap" {
				t.Fatal("transport client cannot use its native original owner")
			}
			jobs[deploymentplan.String(command[1])] = true
		}
	}
	if len(jobs) != 3 || !jobs["service:openbao"] || !jobs["service:emqx-pki"] || !jobs["service:pkibroker"] {
		t.Fatal("remaining original Service subjects not selected", jobs)
	}
}
