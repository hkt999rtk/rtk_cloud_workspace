package main

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCustodyProjectionUsesSameRecordAndLeaseForEnrollmentAndRuntime(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			cfg, p, runtime, service := createServiceRecipeFixture(t, environment)
			cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "workload-identities", "9s"
			jobs, err := renderEnvironmentCreateServiceEnrollmentDefinitions(context.Background(), cfg, p, runtime, service)
			if err != nil {
				t.Fatal(err)
			}
			owners, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, service)
			if err != nil {
				t.Fatal(err)
			}
			binding := environmentCustodyBinding{Generation: strings.Repeat("a", 64), OpenBaoRootSHA256: strings.Repeat("b", 64)}
			for _, role := range []string{"pki-controller", "certissuer", "account-manager"} {
				namespace := p.Stack + "-video-cloud"
				if role == "account-manager" {
					namespace = p.Stack + "-account-manager"
				}
				binding.Owners = append(binding.Owners, environmentCustodyOwnerBinding{ID: environmentCreateServiceOwnerClaim(role), Namespace: namespace, ServiceAccount: role})
			}
			before := deploymentplan.Digest(append(append([]deploymentplan.Definition{}, jobs...), owners...))
			var signatures []map[string]string
			for _, definitions := range [][]deploymentplan.Definition{jobs, owners} {
				projected, err := projectEnvironmentCustodyResources(cfg.Values, environment, &binding, strings.Repeat("c", 64), definitions)
				if err != nil {
					t.Fatal(err)
				}
				signature := map[string]string{}
				leases := map[string]bool{}
				roles := 0
				for _, d := range projected {
					if d.Object["kind"] == "NetworkPolicy" && deploymentplan.Map(d.Object["metadata"])["name"] == "allow-openbao-workload-custody" {
						spec := deploymentplan.Map(d.Object["spec"])
						if deploymentplan.Digest(spec["policyTypes"]) != deploymentplan.Digest([]string{"Ingress"}) {
							t.Fatal("custody network access isolated unrelated existing egress")
						}
						peer := deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(certIssuerObjectList(spec["ingress"])[0])["from"])[0])
						if deploymentplan.Map(deploymentplan.Map(peer["namespaceSelector"])["matchLabels"])["kubernetes.io/metadata.name"] != p.Stack+"-account-manager" || deploymentplan.Map(deploymentplan.Map(peer["podSelector"])["matchLabels"])["rtk.realtek.com/pki-state-backend"] != "openbao" {
							t.Fatal("cross-namespace custody access differs from the original selected consumers")
						}
					}
					if d.Object["kind"] == "PersistentVolumeClaim" {
						t.Fatal("identity-only PVC remains allocated", deploymentplan.ID(d.Object))
					}
					if d.Object["kind"] == "Lease" {
						leases[deploymentplan.ID(d.Object)] = true
					}
					if d.Object["kind"] == "Role" && strings.HasPrefix(deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"]), "pki-state-") {
						roles++
						rules := certIssuerObjectList(d.Object["rules"])
						if len(rules) != 1 {
							t.Fatal("unexpected owner RBAC")
						}
						rule := deploymentplan.Map(rules[0])
						if deploymentplan.Digest(rule["verbs"]) != deploymentplan.Digest([]string{"get", "update"}) || len(certIssuerObjectList(rule["resourceNames"])) == 0 {
							t.Fatal("identity lease role widened beyond exact pre-created resources")
						}
					}
					pod := environmentPKIPod(d.Object)
					if pod == nil {
						continue
					}
					for _, volume := range certIssuerObjectList(pod["volumes"]) {
						v := deploymentplan.Map(volume)
						if v["persistentVolumeClaim"] != nil {
							t.Fatal("PKI private projection still depends on a PVC")
						}
					}
					for _, container := range certIssuerObjectList(pod["containers"]) {
						c := deploymentplan.Map(container)
						prefix := mqttFoundationEnv(c, "PKI_STATE_RECORD_PREFIX")
						if prefix == "" {
							continue
						}
						if mqttFoundationEnv(c, "PKI_STATE_KV_MOUNT") != "workload-identities" || mqttFoundationEnv(c, "PKI_STATE_LEASE_DURATION") != "9s" || mqttFoundationEnv(c, "PKI_STATE_BACKEND") != "openbao" {
							t.Fatal("projection replaced environment custody policy")
						}
						for _, field := range certIssuerObjectList(c["env"]) {
							e := deploymentplan.Map(field)
							name, value := deploymentplan.String(e["name"]), deploymentplan.String(e["value"])
							if strings.HasSuffix(name, "_IDENTITY_STATE") && value != "" {
								signature[prefix+":"+value] = environmentCustodyLeaseName(prefix, value)
							}
						}
						for _, mount := range certIssuerObjectList(c["volumeMounts"]) {
							m := deploymentplan.Map(mount)
							if m["name"] == "pki-custody-trust" && (m["readOnly"] != true || m["subPath"] != nil) {
								t.Fatal("snapshot updates cannot reach the native custody transport")
							}
						}
					}
				}
				if roles == 0 || len(leases) == 0 || len(signature) == 0 {
					t.Fatal("native identity ownership was not provisioned")
				}
				again, err := projectEnvironmentCustodyResources(cfg.Values, environment, &binding, strings.Repeat("c", 64), projected)
				if err != nil || deploymentplan.Digest(again) != deploymentplan.Digest(projected) {
					t.Fatal("recovery projection changed the already selected workload custody", err)
				}
				signatures = append(signatures, signature)
			}
			for state, lease := range signatures[0] {
				if signatures[1][state] != lease {
					t.Fatal("enrollment/runtime selected different durable identity or fencing resource", state)
				}
			}
			if before != deploymentplan.Digest(append(append([]deploymentplan.Definition{}, jobs...), owners...)) {
				t.Fatal("custody projection mutated source recipes")
			}
		})
	}
}

func TestCustodyProjectionPreservesBusinessDataAndIndependentOpenBao(t *testing.T) {
	cfg := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "PKI_STATE_BACKEND": "openbao", "PKI_STATE_KV_MOUNT": "identities", "PKI_STATE_LEASE_DURATION": "30s"}
	binding := &environmentCustodyBinding{Generation: strings.Repeat("a", 64), OpenBaoRootSHA256: strings.Repeat("b", 64), Owners: []environmentCustodyOwnerBinding{{ID: "mqtt-host", Namespace: "video-cloud-dev-video-cloud", ServiceAccount: "mqtt-pki"}}}
	container := deploymentplan.Object{"name": "host", "env": environmentCreateEnv(map[string]string{"EMQX_PKI_HOST_IDENTITY_STATE": "/identity/private/server.json"}), "volumeMounts": []any{environmentCreateServiceMount("identity", "/identity", false)}}
	object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "mqtt-pki", "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{container}, "volumes": []any{environmentCreateServiceVolume("identity", "persistentVolumeClaim", "mqtt-host"), environmentCreateServiceVolume("data", "persistentVolumeClaim", "mqtt-pki-data")}}}}}
	openbao := deploymentplan.Clone(object)
	openbao["kind"] = "StatefulSet"
	deploymentplan.Map(openbao["metadata"])["name"] = "openbao"
	deploymentplan.Map(openbao["metadata"])["namespace"] = "video-cloud-dev-secrets"
	before := deploymentplan.Digest(openbao)
	projected, err := projectEnvironmentCustodyResources(cfg, "dev", binding, "", []deploymentplan.Definition{{Object: object}, {Object: openbao}})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range projected {
		if deploymentplan.ID(d.Object) == deploymentplan.ID(openbao) && deploymentplan.Digest(d.Object) != before {
			t.Fatal("OpenBao root startup now depends on its own KV custody")
		}
		if deploymentplan.ID(d.Object) == deploymentplan.ID(object) {
			volumes := certIssuerObjectList(environmentPKIPod(d.Object)["volumes"])
			if deploymentplan.Map(deploymentplan.Map(volumes[1])["persistentVolumeClaim"])["claimName"] != "mqtt-pki-data" {
				t.Fatal("business storage was removed with PKI identity storage")
			}
			c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["containers"])[0])
			if mqttFoundationEnv(c, "PKI_STATE_BOOTSTRAP_ROOT_SHA256") != "" || mqttFoundationEnv(c, "PKI_STATE_TRANSPORT_TRUST") != "" {
				t.Fatal("completed runtime retained creation-only transport admission")
			}
		}
	}
	for i := range projected {
		if projected[i].Object["kind"] == "Lease" {
			projected[i].Object["spec"] = deploymentplan.Object{"holderIdentity": "original-running-owner", "leaseDurationSeconds": float64(30)}
		}
	}
	before = deploymentplan.Digest(projected)
	again, err := projectEnvironmentCustodyResources(cfg, "dev", binding, "", projected)
	if err != nil || before != deploymentplan.Digest(again) {
		t.Fatal("declaration replay changed a live identity Lease", err)
	}
	for _, d := range projected {
		if d.Object["kind"] == "Role" {
			d.Object["rules"] = []any{deploymentplan.Object{"apiGroups": []any{"*"}, "resources": []any{"*"}, "verbs": []any{"*"}}}
			break
		}
	}
	if _, err = projectEnvironmentCustodyResources(cfg, "dev", binding, "", projected); err == nil {
		t.Fatal("conflicting pre-existing owner RBAC silently accepted")
	}
}

func TestCustodyPolicyOnlyAllowsExactOriginalWorkloadData(t *testing.T) {
	values := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "PKI_STATE_BACKEND": "openbao", "PKI_STATE_KV_MOUNT": "identities", "PKI_STATE_LEASE_DURATION": "30s"}
	owner := environmentCustodyOwnerBinding{ID: "api-identity", Namespace: "video-cloud-dev-video-cloud", ServiceAccount: "video-cloud-api"}
	binding := environmentCustodyBinding{Generation: strings.Repeat("a", 64), Owners: []environmentCustodyOwnerBinding{owner}}
	policy, err := environmentCustodyPolicy(values, "dev", binding, owner)
	want := "path \"identities/data/dev/" + binding.Generation + "/api-identity/*\" { capabilities = [\"create\", \"read\", \"update\"] }\n"
	if err != nil || policy != want {
		t.Fatal("scoped native KV policy changed", err)
	}
	owner.ID = "other-workload"
	if _, err = environmentCustodyPolicy(values, "dev", binding, owner); err == nil {
		t.Fatal("policy authorized an owner outside the original generation binding")
	}
	if _, err = environmentCustodyPolicy(values, "staging", binding, binding.Owners[0]); err == nil {
		t.Fatal("policy authorized a foreign environment")
	}
}

func TestCustodyProjectionSteadyRendererRequiresOriginalCustodyBinding(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-custody-render")
	values, binding, release := completeEnvironmentRenderFixture("dev")
	binding.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
	restore := installAllCredentialEnvironment(values)
	defer restore()
	if _, err := renderEnvironmentResources(values, binding, release); err != nil {
		t.Fatal("legacy baseline:", err)
	}
	values["PKI_STATE_BACKEND"], values["PKI_STATE_KV_MOUNT"], values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	if _, err := renderEnvironmentResources(values, binding, release); err == nil || !strings.Contains(err.Error(), "custody projection requires") {
		t.Fatal("steady renderer accepted remote custody without the original generation binding", err)
	}
}

func TestCustodyProjectionNativeLeaseWireName(t *testing.T) {
	prefix := "dev/" + strings.Repeat("a", 64) + "/api-identity"
	if environmentCustodyLeaseName(prefix, "/identity/private/client.json") != "pki-state-b39c2fe9345ed9d9b6fdb41c858f540d" {
		t.Fatal("deployment Lease name differs from the native IdentityCustody wire contract")
	}
}

func TestCustodyProjectionKeepsMultipleIdentityPurposesWithinOneWorkload(t *testing.T) {
	values := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "PKI_STATE_BACKEND": "openbao", "PKI_STATE_KV_MOUNT": "identities", "PKI_STATE_LEASE_DURATION": "30s"}
	binding := &environmentCustodyBinding{Generation: strings.Repeat("a", 64), OpenBaoRootSHA256: strings.Repeat("b", 64), Owners: []environmentCustodyOwnerBinding{{ID: "logingester", Namespace: "video-cloud-dev-video-cloud", ServiceAccount: "video-cloud-logingester", StorageAliases: []string{"video-cloud-logingester-service-state", "pki-product-logger-identity"}}}}
	container := deploymentplan.Object{"name": "app", "env": environmentCreateEnv(map[string]string{"LOG_IDENTITY_STATE": "/log/private/client.json", "REGISTRATION_IDENTITY_STATE": "/registration/private/client.json"}), "volumeMounts": []any{environmentCreateServiceMount("log", "/log", false), environmentCreateServiceMount("registration", "/registration", false)}}
	object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "logingester", "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{container}, "volumes": []any{environmentCreateServiceVolume("log", "persistentVolumeClaim", "video-cloud-logingester-service-state"), environmentCreateServiceVolume("registration", "persistentVolumeClaim", "pki-product-logger-identity")}}}}}
	definitions, err := projectEnvironmentCustodyResources(values, "dev", binding, "", []deploymentplan.Definition{{Object: object}})
	if err != nil {
		t.Fatal(err)
	}
	leases := map[string]bool{}
	for _, d := range definitions {
		if d.Object["kind"] == "Lease" {
			leases[deploymentplan.ID(d.Object)] = true
		}
		if d.Object["kind"] == "Deployment" {
			c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["containers"])[0])
			if mqttFoundationEnv(c, "PKI_STATE_RECORD_PREFIX") != "dev/"+binding.Generation+"/logingester" {
				t.Fatal("separate credential purposes selected another workload")
			}
			for _, volume := range certIssuerObjectList(environmentPKIPod(d.Object)["volumes"]) {
				if deploymentplan.Map(volume)["persistentVolumeClaim"] != nil {
					t.Fatal("private purpose still requires a separate persistent claim")
				}
			}
		}
	}
	if len(leases) != 2 {
		t.Fatal("independent native identity paths collapsed into the same fencing resource")
	}
}
