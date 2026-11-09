package main

import (
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCustodyRegistrationProjectionUsesEnvironmentAndOriginalServiceRoot(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			cfg := deploymentConfig{Environment: environment, Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + environment, "PKI_STATE_BACKEND": "openbao", "PKI_STATE_KV_MOUNT": "identities", "PKI_STATE_LEASE_DURATION": "30s"}}
			for _, flag := range []string{"MQTT_FOUNDATION_REGISTRATION_ENABLED", "SHADOW_WORKER_REGISTRATION_ENABLED", "OTA_SERVICE_REGISTRATION_ENABLED", "VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED", "WEBRTC_SERVICE_REGISTRATION_ENABLED", "LOGGER_SERVICE_REGISTRATION_ENABLED"} {
				cfg.Values[flag] = "true"
			}
			products, err := environmentCreateProductIdentityIntent(cfg)
			if err != nil {
				t.Fatal(err)
			}
			p := environmentCreateBootstrapPlan{Environment: environment, Stack: cfg.Values["CLOUD_STACK_NAME"], ProductIdentities: products}
			binding, err := createEnvironmentCustodyBinding(cfg, p, strings.Repeat("a", 64), strings.Repeat("b", 64))
			if err != nil {
				t.Fatal(err)
			}
			binding.ServiceRootSHA256 = strings.Repeat("c", 64)
			var definitions []deploymentplan.Definition
			for _, product := range products {
				workload := environmentCustodyProductWorkload(strings.TrimPrefix(product.Subject, "service:"))
				secret := strings.Split(product.ID, "/")[2]
				pod := deploymentplan.Object{"securityContext": deploymentplan.Object{"runAsUser": float64(12345), "fsGroup": float64(23456)}, "containers": []any{deploymentplan.Object{"name": "app", "image": "fixture", "env": environmentCreateEnv(map[string]string{"EXISTING_SETTING": "preserved"}), "volumeMounts": []any{environmentCreateServiceMount("registration", "/run/registration", true)}}}, "volumes": []any{environmentCreateServiceVolume("registration", "secret", secret)}}
				definitions = append(definitions, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": workload, "namespace": p.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": pod}}}})
			}
			before := deploymentplan.Digest(definitions)
			projected, err := projectEnvironmentCustodyResources(cfg.Values, environment, binding, "", definitions)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, definition := range projected {
				pod := environmentPKIPod(definition.Object)
				if pod == nil {
					continue
				}
				if deploymentplan.Map(pod["securityContext"])["runAsUser"] != float64(12345) || deploymentplan.Map(pod["securityContext"])["fsGroup"] != float64(23456) {
					t.Fatal("registration replaced selected workload permissions")
				}
				c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
				if mqttFoundationEnv(c, "PKI_REGISTRATION_IDENTITY_STATE") != "/var/lib/pki-service/private/client.json" || mqttFoundationEnv(c, "PKI_REGISTRATION_ROOT_SHA256") != binding.ServiceRootSHA256 || mqttFoundationEnv(c, "PKI_STATE_BACKEND") != "openbao" {
					t.Fatal("registration owner was not bound to original KV identity")
				}
				if mqttFoundationEnv(c, "PKI_REGISTRATION_CONTROLLER_URL") != "https://pki-controller."+p.Stack+"-video-cloud.svc:18446" || mqttFoundationEnv(c, "PKI_REGISTRATION_RENEWAL_URL") != "https://certissuer."+p.Stack+"-video-cloud.svc:18445" {
					t.Fatal("registration used another environment endpoint")
				}
				if mqttFoundationEnv(c, "EXISTING_SETTING") != "preserved" {
					t.Fatal("registration changed unrelated settings")
				}
				for _, raw := range certIssuerObjectList(pod["volumes"]) {
					if deploymentplan.Map(raw)["persistentVolumeClaim"] != nil {
						t.Fatal("registration allocated an identity PVC")
					}
				}
				count++
			}
			if count != len(products) {
				t.Fatal("missing registration workload")
			}
			if before != deploymentplan.Digest(definitions) {
				t.Fatal("registration mutated maintained source recipes")
			}
			for attempt := 0; attempt < 32; attempt++ {
				fresh, err := projectEnvironmentCustodyResources(cfg.Values, environment, binding, "", definitions)
				if err != nil || deploymentplan.Digest(fresh) != deploymentplan.Digest(projected) {
					t.Fatal("equal maintained inputs produced another child plan", err)
				}
			}
			again, err := projectEnvironmentCustodyResources(cfg.Values, environment, binding, "", projected)
			if err != nil || deploymentplan.Digest(again) != deploymentplan.Digest(projected) {
				t.Fatal("registration replay changed projection", err)
			}
			binding.ServiceRootSHA256 = ""
			if _, err := projectEnvironmentCustodyResources(cfg.Values, environment, binding, "", definitions); err == nil {
				t.Fatal("registration invented missing Service Root")
			}
		})
	}
}

func TestCreateCustodyCompletionBindsOriginalGenerationAndAuthorities(t *testing.T) {
	cfg, p, _, service := createServiceRecipeFixture(t, "dev")
	_, _, _, next := createServiceRecipeFixture(t, "dev")
	cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	generation := strings.Repeat("a", 64)
	original, err := createEnvironmentCustodyBinding(cfg, p, generation, "")
	if err != nil {
		t.Fatal(err)
	}
	runtime := environmentCreateBootstrapRuntime{ExecutionSHA256: generation, Custody: original, ServiceCeremony: &service}
	managed := next.Root
	managed.Environment, managed.Domain, managed.Kind, managed.Status = "dev", "openbao_tls", "root", "active"
	service.Root.Status = "active"
	bindings := environmentIdentityBindings{Version: 1, Environment: "dev", Stack: p.Stack, Owners: map[string]string{"Deployment/owned/original": "original-uid"}}
	before := deploymentplan.Digest(bindings.Owners)
	if err := applyEnvironmentCreateCustodyBindings(cfg, p, runtime, service.Root, managed, &bindings); err != nil {
		t.Fatal(err)
	}
	if bindings.Custody == nil || bindings.Custody.Generation != generation || bindings.Custody.OpenBaoRootSHA256 != managed.RootFingerprint || bindings.Custody.ServiceRootSHA256 != service.Root.RootFingerprint || before != deploymentplan.Digest(bindings.Owners) {
		t.Fatal("completed custody binding changed original owners/authorities")
	}
	managed.Domain = "service"
	if err := applyEnvironmentCreateCustodyBindings(cfg, p, runtime, service.Root, managed, &bindings); err == nil {
		t.Fatal("completion accepted another trust domain")
	}
}
