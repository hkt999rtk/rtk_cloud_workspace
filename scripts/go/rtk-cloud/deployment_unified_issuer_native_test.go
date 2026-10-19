package main

import (
	"context"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func registryIssuerCanonicalFixture(t *testing.T, environment string) (map[string]string, environmentRelease, deploymentplan.Object, []deploymentplan.Object) {
	t.Helper()
	cfg, p, release, runtime, service, roots, intermediates, _ := createApplicationFinalizeFixture(t, environment)
	service.Intermediate.Reference = "pki-issuers/service/" + service.Intermediate.ID + "/v1"
	service.Intermediate.Document["signer_reference"] = service.Intermediate.Reference
	roots["service"], intermediates["service"] = service.Root, service.Intermediate
	app := intermediates["app"]
	app.Reference = "pki-issuers/app/" + app.ID + "/v1"
	app.Document["signer_reference"] = app.Reference
	intermediates["app"] = app
	definitions, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	definitions, err = configureEnvironmentCreateFinalIssuer(p, roots, intermediates, definitions)
	if err != nil {
		t.Fatal(err)
	}
	object := deploymentplan.Clone(createRecipeObject(t, definitions, "Deployment/"+p.Stack+"-video-cloud/certissuer"))
	deploymentplan.Map(object["metadata"])["uid"] = "original-native-issuer"
	secrets := []deploymentplan.Object{
		{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "pki-issuer-database", "namespace": p.Stack + "-video-cloud", "uid": "original-native-database"}, "data": deploymentplan.Object{"url": "isolated-database-url"}},
		{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "factoryenroll-runtime", "namespace": p.Stack + "-video-cloud", "uid": "original-native-jwt"}, "data": deploymentplan.Object{"FACTORY_ENROLL_PRODUCTION_JWT_SECRET": "isolated-original-jwt", "FACTORY_ENROLL_PRODUCTION_JWT_AUDIENCE": "isolated-original-audience"}},
	}
	env, _, _ := completeEnvironmentRenderFixture(environment)
	return env, release, object, secrets
}

func TestRegistryOnlyIssuerCanonicalAdoptionRetainsNativeRuntimeAndOriginalSecretKeys(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			env, release, current, secrets := registryIssuerCanonicalFixture(t, environment)
			service := deploymentplan.Object{"apiVersion": "v1", "kind": "Service", "metadata": deploymentplan.Object{"name": "certissuer", "namespace": release.Stack + "-video-cloud", "uid": "original-native-issuer-service"}, "spec": deploymentplan.Object{"ports": []any{deploymentplan.Object{"name": "https", "port": float64(18445), "targetPort": float64(18445)}}}}
			before := deploymentplan.Digest(current)
			observed, err := observeEnvironmentBindings(environment, release, append([]deploymentplan.Object{current, service}, secrets...))
			if err != nil {
				t.Fatal(err)
			}
			if len(observed.Pods) != 1 || observed.Pods[0].IssuerRuntimeSecret != "pki-issuer-database" {
				t.Fatal("registry-only issuer fabricated a legacy CA runtime Secret", observed)
			}
			binding := observed.Pods[0]
			candidate, err := renderManagedEnvironmentComponent(env, binding)
			if err != nil {
				t.Fatal(err)
			}
			if err := applyEnvironmentPodBindings(candidate, observed, release); err != nil {
				t.Fatal(err)
			}
			if err := applyEnvironmentAuthRole(env, candidate); err != nil {
				t.Fatal(err)
			}
			if err := applyEnvironmentIssuerRuntime(env, candidate, binding); err != nil {
				t.Fatal(err)
			}
			container, original := mqttFoundationPrimary(candidate), mqttFoundationPrimary(current)
			for _, key := range []string{"CERT_ISSUER_REGISTRY_ONLY_ENABLED", "PKI_BOOTSTRAP_SESSION_ID", "CERT_ISSUER_OPENBAO_PKI_MOUNT", "CERT_ISSUER_APP_OPENBAO_PKI_MOUNT", "CERT_ISSUER_OPENBAO_GATEWAY_ROLE", "CERT_ISSUER_MAX_TTL_DAYS", "CERT_ISSUER_SERVICE_CLIENT_IDENTITY_STATE", "CERT_ISSUER_HOST_IDENTITY_STATE", "CERT_ISSUER_GATEWAY_DNS_NAMES"} {
				if mqttFoundationEnv(container, key) == "" || mqttFoundationEnv(container, key) != mqttFoundationEnv(original, key) {
					t.Fatal("native mode, signing reference, policy or original identity was lost", key)
				}
			}
			for _, key := range []string{"CERT_ISSUER_DB_DSN", "CERT_ISSUER_PRODUCTION_JWT_SECRET", "CERT_ISSUER_PRODUCTION_JWT_AUDIENCE"} {
				if deploymentplan.Digest(managedNamedField(container["env"], key)) != deploymentplan.Digest(managedNamedField(original["env"], key)) {
					t.Fatal("an original database or JWT Secret key reference was flattened or replaced", key)
				}
			}
			if mqttFoundationEnv(container, "CERT_ISSUER_CA_CERT_PATH") != "" || mqttFoundationEnv(container, "CERT_ISSUER_OPENBAO_PKI_ROLE") == "factory-device" || mqttFoundationEnv(container, "CERT_ISSUER_LISTEN_ADDR") != ":18445" || managedNamedField(container["ports"], "tls")["containerPort"] != float64(18445) {
				t.Fatal("native issuer acquired legacy CA/role defaults or a mismatched listener")
			}
			if deploymentplan.Digest(environmentPKIPod(candidate)["securityContext"]) != deploymentplan.Digest(environmentPKIPod(current)["securityContext"]) || deploymentplan.Digest(container["securityContext"]) != deploymentplan.Digest(original["securityContext"]) {
				t.Fatal("native token or private state owner permissions changed during canonical adoption")
			}
			if deploymentplan.Digest(current) != before {
				t.Fatal("observation mutated the original owner")
			}
			serviceDefinitions, err := decodeEnvironmentCreateManifests(lkeCertIssuerServiceManifest(env))
			if err != nil || len(serviceDefinitions) != 1 {
				t.Fatal("canonical issuer Service could not render", err)
			}
			if err := applyEnvironmentRegistryIssuerService(serviceDefinitions[0].Object, observed); err != nil {
				t.Fatal(err)
			}
			port := managedNamedField(deploymentplan.Map(serviceDefinitions[0].Object["spec"])["ports"], "https")
			if port["port"] != float64(18445) || port["targetPort"] != float64(18445) {
				t.Fatal("native issuer Service lost its exact frozen port/target")
			}
		})
	}
}

func TestRegistryOnlyIssuerRejectsMissingSecretRolesAndUnsafeNativeSettings(t *testing.T) {
	for _, mode := range []string{"missing-db", "missing-jwt", "missing-audience", "optional-db", "wrong-db-key", "inline-jwt", "foreign-jwt", "duplicate-db", "legacy-ca", "legacy-ca-reference", "wrong-root", "legacy-mount", "unbounded-ttl", "temporary-provisioner", "foreign-listener", "unknown-policy"} {
		t.Run(mode, func(t *testing.T) {
			_, _, current, secrets := registryIssuerCanonicalFixture(t, "dev")
			container := mqttFoundationPrimary(current)
			switch mode {
			case "missing-db":
				delete(deploymentplan.Map(secrets[0]["data"]), "url")
			case "missing-jwt":
				delete(deploymentplan.Map(secrets[1]["data"]), "FACTORY_ENROLL_PRODUCTION_JWT_SECRET")
			case "missing-audience":
				delete(deploymentplan.Map(secrets[1]["data"]), "FACTORY_ENROLL_PRODUCTION_JWT_AUDIENCE")
			case "optional-db", "wrong-db-key":
				ref := deploymentplan.Map(deploymentplan.Map(managedNamedField(container["env"], "CERT_ISSUER_DB_DSN")["valueFrom"])["secretKeyRef"])
				if mode == "optional-db" {
					ref["optional"] = true
				} else {
					ref["key"] = "POSTGRES_PASSWORD"
				}
			case "inline-jwt":
				container["env"] = mergeEnvironmentNamedFields(container["env"], []any{deploymentplan.Object{"name": "CERT_ISSUER_PRODUCTION_JWT_SECRET", "value": "private-canary"}})
			case "legacy-ca-reference":
				container["env"] = mergeEnvironmentNamedFields(container["env"], []any{deploymentplan.Object{"name": "CERT_ISSUER_CA_CERT_PATH", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "old-runtime", "key": "device-ca.crt"}}}})
			case "foreign-jwt":
				deploymentplan.Map(secrets[1]["metadata"])["namespace"] = "video-cloud-staging-video-cloud"
			case "duplicate-db":
				container["env"] = append(certIssuerObjectList(container["env"]), managedNamedField(container["env"], "CERT_ISSUER_DB_DSN"))
			default:
				key, value := map[string]string{"legacy-ca": "CERT_ISSUER_CA_CERT_PATH", "wrong-root": "CERT_ISSUER_HOST_ROOT_SHA256", "legacy-mount": "CERT_ISSUER_OPENBAO_PKI_MOUNT", "unbounded-ttl": "CERT_ISSUER_MAX_TTL_DAYS", "temporary-provisioner": "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER", "foreign-listener": "CERT_ISSUER_LISTEN_ADDR", "unknown-policy": "CERT_ISSUER_GATEWAY_CLIENT_CN_PATTERN"}[mode], "private-canary"
				container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{key: value}))
			}
			before := deploymentplan.Digest(current)
			if _, err := observeEnvironmentIssuerRuntimeSecret(current, secrets); err == nil || strings.Contains(err.Error(), "private-canary") {
				t.Fatal("unsafe native issuer reference passed or exposed private settings", err)
			}
			if deploymentplan.Digest(current) != before {
				t.Fatal("rejected issuer observation changed its original owner")
			}
		})
	}
	for _, unsupported := range []string{"CERT_ISSUER_DEBUG_SKIP_VERIFICATION", "CERT_ISSUER_UNVERIFIED_ROOT", "CERT_ISSUER_APP_OPENBAO_PKCS11_TOKEN"} {
		if environmentIdentitySetting(unsupported) {
			t.Fatal("issuer canonical binding imported an unrestricted live process setting", unsupported)
		}
	}
}

func TestNativeFactoryRuntimeRetainsExactManagedAccountAndIssuerEndpoints(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		cfg, p, _, _, service, _, _, initial := createApplicationFinalizeFixture(t, "dev")
		object := deploymentplan.Clone(createRecipeObject(t, initial, "Deployment/"+p.Stack+"-video-cloud/factoryenroll"))
		container := mqttFoundationPrimary(object)
		if invalid {
			container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"FACTORY_ENROLL_ACCOUNT_MANAGER_URL": "https://account-manager." + p.Stack + "-account-manager.svc.cluster.local:8443"}))
		}
		binding := environmentPodBindings{Name: "factoryenroll", Component: "certissuer", IssuerRuntimeSecret: "factoryenroll-runtime", Containers: []environmentContainerBindings{{Name: "factoryenroll"}}}
		if err := applyEnvironmentIssuerRuntime(cfg.Values, object, binding); (err != nil) != invalid {
			t.Fatal("native Factory endpoint qualification admitted another listener or rejected its original endpoints", err)
		}
		if !invalid && (mqttFoundationEnv(container, "FACTORY_ENROLL_ACCOUNT_MANAGER_URL") != "https://account-manager."+p.Stack+"-account-manager.svc.cluster.local:18443" || mqttFoundationEnv(container, "FACTORY_ENROLL_CERT_ISSUER_URL") != "https://certissuer."+p.Stack+"-video-cloud.svc:18445" || mqttFoundationEnv(container, "FACTORY_ENROLL_SERVICE_IDENTITY_ROOT_SHA256") != service.Root.RootFingerprint) {
			t.Fatal("canonical Factory runtime replaced a native owner or endpoint")
		}
	}
}
