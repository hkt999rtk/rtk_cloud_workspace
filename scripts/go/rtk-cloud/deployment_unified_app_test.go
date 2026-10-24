package main

import (
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func environmentAppFixture(environment string) environmentPodBindings {
	volumes, mounts := []any{}, []any{}
	for _, name := range []string{"pki-server", "pki-app", "pki-state", "pki-management"} {
		source, field := "secret", "secretName"
		if name == "pki-app" {
			source, field = "configMap", "name"
		}
		if name == "pki-state" {
			source, field = "persistentVolumeClaim", "claimName"
		}
		volumes = append(volumes, map[string]any{"name": name, source: map[string]any{field: environment + "-original-" + name}})
		mounts = append(mounts, map[string]any{"name": name, "mountPath": "/run/" + name, "readOnly": name != "pki-state"})
	}
	return environmentPodBindings{Kind: "Deployment", NamespaceSuffix: "video-cloud", Name: "video-cloud-api-app-pki", Component: "managed-app-api", PodFields: map[string]any{"volumes": volumes}, Containers: []environmentContainerBindings{{Name: "app", ImageKey: "LKE_VIDEO_CLOUD_IMAGE", Fields: map[string]any{"volumeMounts": mounts, "env": []any{map[string]any{"name": "VIDEO_CLOUD_AUTH_APP_CA_CERT", "value": "/run/pki-app/roots.pem"}}}}}, InitContainers: []environmentContainerBindings{{Name: "initialize-app-trust-state", ImageKey: "LKE_VIDEO_CLOUD_IMAGE", Fields: map[string]any{"volumeMounts": mounts[1:3], "env": []any{map[string]any{"name": "VIDEO_CLOUD_AUTH_APP_CA_CERT", "value": "/run/pki-app/roots.pem"}}}}}}
}
func attachEnvironmentAppFixture(b *environmentIdentityBindings) {
	b.Pods = append(b.Pods, environmentAppFixture(b.Environment))
	b.AppIngress = &environmentAppIngressBinding{Name: "existing-app-mtls", Host: "app." + b.Environment + ".example.invalid"}
	b.Owners["Ingress/"+b.Stack+"-video-cloud/existing-app-mtls"] = "original-ingress-uid"
}
func TestAppPKICommonPlanPreservesRolesReferencesAndOriginalRoute(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-app-fixture")
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, b, release := completeEnvironmentRenderFixture(environment)
			attachEnvironmentAppFixture(&b)
			b.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			restore := installAllCredentialEnvironment(env)
			defer restore()
			definitions, err := renderEnvironmentResources(env, b, release)
			if err != nil {
				t.Fatal(err)
			}
			objects := map[string]deploymentplan.Object{}
			for _, d := range definitions {
				objects[deploymentplan.ID(d.Object)] = d.Object
			}
			ns := b.Stack + "-video-cloud"
			app := objects["Deployment/"+ns+"/video-cloud-api-app-pki"]
			pod := environmentPKIPod(app)
			main := managedNamedField(pod["containers"], "app")
			init := managedNamedField(pod["initContainers"], "initialize-app-trust-state")
			for _, c := range []map[string]any{main, init} {
				if c["image"] != release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference {
					t.Fatal("App main/initializer is not formal CI pinned")
				}
			}
			for name, want := range map[string]string{"VIDEO_CLOUD_AUTH_APP_PKI_ENABLED": "true", "VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED": "false", "VIDEO_CLOUD_AUTH_MTLS_REQUIRED": "true", "VIDEO_CLOUD_MQTT_ENABLED": "false", "VIDEO_CLOUD_TURN_CREDENTIAL_TTL": "1m"} {
				if managedNamedField(main["env"], name)["value"] != want {
					t.Fatal("wrong App policy", name)
				}
			}
			device := managedNamedField(environmentPKIPod(objects["Deployment/"+ns+"/video-cloud-api-pki"])["containers"], "app")
			if managedNamedField(device["env"], "VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED")["value"] != "true" || managedNamedField(device["env"], "VIDEO_CLOUD_AUTH_APP_PKI_ENABLED")["value"] != "false" {
				t.Fatal("App changes disabled Device role")
			}
			for _, name := range []string{"certissuer", "factoryenroll"} {
				c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(objects["Deployment/"+ns+"/"+name])["containers"])[0])
				key := "CERT_ISSUER_PRODUCT_PKI_ENABLED"
				if name == "factoryenroll" {
					key = "FACTORY_ENROLL_PRODUCT_PKI_ENABLED"
				}
				if managedNamedField(c["env"], key)["value"] != "true" {
					t.Fatal("Device CSR admission/issuance disabled")
				}
			}
			binding := environmentAppFixture(environment)
			for _, value := range certIssuerObjectList(binding.PodFields["volumes"]) {
				v := deploymentplan.Map(value)
				if !reflect.DeepEqual(v, managedNamedField(pod["volumes"], deploymentplan.String(v["name"]))) {
					t.Fatal("original private/state reference replaced")
				}
			}
			ingress := objects["Ingress/"+ns+"/existing-app-mtls"]
			if ingress == nil || deploymentplan.Map(ingress["spec"])["ingressClassName"] != environmentIngressClass(env) || deploymentplan.Map(deploymentplan.Map(ingress["metadata"])["annotations"])["nginx.ingress.kubernetes.io/ssl-passthrough"] != "true" {
				t.Fatal("original passthrough route lost")
			}
			// Observation followed by the same renderer must preserve lifecycle inputs.
			deploymentplan.Map(app["metadata"])["uid"] = "original-app-uid"
			deploymentplan.Map(ingress["metadata"])["uid"] = "original-ingress-uid"
			init["image"] = "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:" + strings.Repeat("d", 64)
			observed, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{app, ingress})
			if err != nil {
				t.Fatal(err)
			}
			target, err := renderManagedEnvironmentAPI(env, observed.Pods[0])
			if err != nil {
				t.Fatal(err)
			}
			if err = applyEnvironmentPodBindings(target, observed, release); err != nil {
				t.Fatal(err)
			}
			if err = validateEnvironmentPKITransition([]deploymentplan.Definition{{Object: target}}, []deploymentplan.Object{app}); err != nil {
				t.Fatal(err)
			}
			env["APP_API_AUTH_PRODUCT_PKI_ENABLED"] = "true"
			if err = applyEnvironmentAuthRole(env, target); err != nil {
				t.Fatal(err)
			}
			if err = validateEnvironmentPKITransition([]deploymentplan.Definition{{Object: target}}, []deploymentplan.Object{app}); err == nil {
				t.Fatal("role change silently accepted")
			}
		})
	}
}

func TestAppTURNPolicyRejectsUnexecutableCandidate(t *testing.T) {
	for _, ttl := range []string{"1m", "30s", "10m", "61s", "0s", "-1s", "", "invalid", "reference"} {
		t.Run(ttl, func(t *testing.T) {
			setting := deploymentplan.Object{"name": "VIDEO_CLOUD_TURN_CREDENTIAL_TTL", "value": ttl}
			if ttl == "reference" {
				setting = deploymentplan.Object{"name": "VIDEO_CLOUD_TURN_CREDENTIAL_TTL", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "private-token", "key": "ttl"}}}
			}
			object := deploymentplan.Object{"kind": "Deployment", "metadata": deploymentplan.Object{"name": "video-cloud-api-app-pki"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app", "env": []any{deploymentplan.Object{"name": "VIDEO_CLOUD_AUTH_APP_PKI_ENABLED", "value": "true"}, deploymentplan.Object{"name": "VIDEO_CLOUD_WEBRTC_TURN_URLS", "value": "turn:relay.example.invalid:3478"}, setting}}}}}}}
			err := validateEnvironmentAppTURNPolicy([]deploymentplan.Definition{{Object: object}})
			planErr := checkEnvironmentAppTURNPlan(&deploymentplan.Plan{Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(object), Desired: object}}})
			if (err == nil) != (planErr == nil) {
				t.Fatal("execution qualification and renderer policy differ")
			}
			if ttl == "1m" || ttl == "30s" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var blocker *deploymentplan.Blocker
				if !errors.As(err, &blocker) || blocker.ID != "deployment.configuration.app-turn-ttl" || strings.Contains(err.Error(), "private-token") {
					t.Fatal("unexecutable App candidate was not safely blocked", err)
				}
			}
			deploymentplan.Map(object["metadata"])["name"] = "video-cloud-api"
			if err := validateEnvironmentAppTURNPolicy([]deploymentplan.Definition{{Object: object}}); err != nil {
				t.Fatal("working core API policy was changed", err)
			}
		})
	}
}
func TestAppPKIRejectsMissingReferencesAndUnknownInitializer(t *testing.T) {
	env, _, release := completeEnvironmentRenderFixture("dev")
	for _, name := range []string{"pki-server", "pki-app", "pki-state", "pki-management"} {
		t.Run(name, func(t *testing.T) {
			b := environmentAppFixture("dev")
			var volumes []any
			for _, v := range certIssuerObjectList(b.PodFields["volumes"]) {
				if deploymentplan.Map(v)["name"] != name {
					volumes = append(volumes, v)
				}
			}
			b.PodFields["volumes"] = volumes
			_, err := renderManagedEnvironmentAPI(env, b)
			var blocker *deploymentplan.Blocker
			if !errors.As(err, &blocker) || blocker.ID != "deployment.compatibility.app-pki" {
				t.Fatal(err)
			}
		})
	}
	b := environmentAppFixture("dev")
	object, err := renderManagedEnvironmentAPI(env, b)
	if err != nil {
		t.Fatal(err)
	}
	bindings := environmentIdentityBindings{Version: 1, Environment: "dev", Stack: "video-cloud-dev", Pods: []environmentPodBindings{b}, Owners: map[string]string{}}
	if err = applyEnvironmentPodBindings(object, bindings, release); err != nil {
		t.Fatal(err)
	}
	deploymentplan.Map(object["metadata"])["uid"] = "original-app"
	managedNamedField(environmentPKIPod(object)["initContainers"], "initialize-app-trust-state")["command"] = []any{"sh", "-c", "overwrite private state"}
	if _, err = observeEnvironmentBindings("dev", release, []deploymentplan.Object{object}); err == nil {
		t.Fatal("unknown state initializer accepted")
	}
}
func TestAppTrustInitializerRetryPreservesExistingState(t *testing.T) {
	root := t.TempDir()
	state, trust := filepath.Join(root, "state"), filepath.Join(root, "trust")
	if err := os.MkdirAll(trust, 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("original-trust")
	source := filepath.Join(trust, "root-trust.json")
	if err := os.WriteFile(source, original, 0600); err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(strings.ReplaceAll(environmentAppTrustInitialize, "/run/pki-state", state), "/run/pki-app", trust)
	run := func() {
		if output, err := exec.Command("/bin/sh", "-ec", script).CombinedOutput(); err != nil {
			t.Fatal(err, string(output))
		}
	}
	run()
	if err := os.WriteFile(source, []byte("replacement-trust"), 0600); err != nil {
		t.Fatal(err)
	}
	run()
	file := filepath.Join(state, "app/root-policy.json")
	got, err := os.ReadFile(file)
	if err != nil || string(got) != string(original) {
		t.Fatal("retry overwrote initialized state")
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private state permissions lost")
	}
}
func TestSharedAuthConfigurationRejectsMissingInvalidAndEnvironmentOverrides(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, _, _ := completeEnvironmentRenderFixture(environment)
			for _, role := range environmentAuthRoles {
				for _, key := range role {
					old := env[key]
					for _, value := range []string{"", "yes"} {
						env[key] = value
						if validateEnvironmentAuthConfiguration(env) == nil {
							t.Fatal("implicit role accepted", key)
						}
					}
					env[key] = old
				}
			}
			snapshot := environmentDeploymentSnapshot{}
			cfg := deploymentConfig{Values: env}
			before := snapshot.Digest(cfg)
			env["APP_API_AUTH_PRODUCT_PKI_ENABLED"] = "true"
			if before == snapshot.Digest(cfg) {
				t.Fatal("role drift not hashed")
			}
			workspace := writeDeploymentFixture(t, environment, "lke")
			writeTestFile(t, filepath.Join(workspace, "cloud_env", environment, "overrides/architecture.env"), "APP_API_AUTH_PRODUCT_PKI_ENABLED=true\n")
			if _, err := resolveDeploymentConfig(workspace, environment, ""); err == nil {
				t.Fatal("shared policy overridden by environment")
			}
		})
	}
	env, _, _ := completeEnvironmentRenderFixture("dev")
	issuer := deploymentplan.Object{"kind": "Deployment", "metadata": map[string]any{"name": "certissuer-service-pki"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app"}}}}}}
	if err := applyEnvironmentAuthRole(env, issuer); err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Map(certIssuerObjectList(environmentPKIPod(issuer)["containers"])[0])["env"] != nil {
		t.Fatal("main issuer policy leaked to Service issuer")
	}
}

func TestAppRouteRejectsMissingAmbiguousOrTerminatedClientTLS(t *testing.T) {
	env, b, _ := completeEnvironmentRenderFixture("dev")
	attachEnvironmentAppFixture(&b)
	b.AppIngress.TLS = []environmentAppTLSBinding{{Hosts: []string{b.AppIngress.Host}, SecretName: "original-app-public-tls"}}
	definitions, err := environmentAppDependencies(env, b)
	if err != nil {
		t.Fatal(err)
	}
	original := definitions[2].Object
	observed := environmentIdentityBindings{Stack: b.Stack}
	if err := observeEnvironmentAppIngress(&observed, original); err != nil || !reflect.DeepEqual(observed.AppIngress, b.AppIngress) {
		t.Fatal("original public TLS reference was not retained", err)
	}
	for _, scenario := range []string{"duplicate", "termination", "backend", "tls-host"} {
		t.Run(scenario, func(t *testing.T) {
			object := deploymentplan.Clone(original)
			current := environmentIdentityBindings{Stack: b.Stack}
			switch scenario {
			case "duplicate":
				current.AppIngress = b.AppIngress
			case "termination":
				deploymentplan.Map(deploymentplan.Map(object["metadata"])["annotations"])["nginx.ingress.kubernetes.io/ssl-passthrough"] = "false"
			case "backend":
				rule := deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(object["spec"])["rules"])[0])
				path := deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(rule["http"])["paths"])[0])
				deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(path["backend"])["service"])["port"])["number"] = float64(80)
			case "tls-host":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(object["spec"])["tls"])[0])["hosts"] = []any{"foreign.example.invalid"}
			}
			if err := observeEnvironmentAppIngress(&current, object); err == nil {
				t.Fatal("incompatible original route accepted")
			}
		})
	}
	b.AppIngress = nil
	if _, err := environmentAppDependencies(env, b); err == nil {
		t.Fatal("invented missing original route")
	}
}
