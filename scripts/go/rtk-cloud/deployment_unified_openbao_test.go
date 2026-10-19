package main

import (
	"encoding/json"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func existingFileOpenBaoFixture(environment string, managed bool) (map[string]string, environmentRelease, []deploymentplan.Object) {
	env, _, release := completeEnvironmentRenderFixture(environment)
	env["OPENBAO_STORAGE_BACKEND"], env["OPENBAO_EFFECTIVE_REPLICAS"] = "file", "1"
	env["OPENBAO_DATA_STORAGE"], env["OPENBAO_AUDIT_STORAGE"] = "10Gi", "5Gi"
	env["OPENBAO_POD_MANAGEMENT_POLICY"], env["OPENBAO_UPDATE_STRATEGY"] = "OrderedReady", "OnDelete"
	ns := env["CLOUD_STACK_NAME"] + "-secrets"
	meta := func(name, uid string) map[string]any {
		return map[string]any{"name": name, "namespace": ns, "uid": uid, "resourceVersion": "1"}
	}
	main := map[string]any{"name": "openbao", "image": "quay.io/openbao/openbao:2.5.4", "command": []any{"legacy-chart-command"}, "resources": map[string]any{"requests": map[string]any{"cpu": "999m"}}, "env": []any{map[string]any{"name": "BAO_ADDR", "value": "https://127.0.0.1:8200"}}, "volumeMounts": []any{map[string]any{"name": "config", "mountPath": "/openbao/config"}, map[string]any{"name": "data", "mountPath": "/openbao/data"}, map[string]any{"name": "audit", "mountPath": "/openbao/audit"}}}
	selector := map[string]any{"app.kubernetes.io/instance": "openbao", "app.kubernetes.io/name": "openbao", "component": "server"}
	pod := map[string]any{"containers": []any{main}, "serviceAccountName": "openbao", "securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": float64(100), "runAsGroup": float64(1000), "fsGroup": float64(1000)}, "volumes": []any{map[string]any{"name": "config", "configMap": map[string]any{"name": "openbao-config"}}}}
	tlsCert, tlsKey := "/openbao/tls/tls.crt", "/openbao/tls/tls.key"
	if managed {
		// Existing local-package provenance never selects the new target image.
		main["image"] = "ghcr.io/hkt999rtk/rtk_cloud_dev/openbao-pki@sha256:" + strings.Repeat("e", 64)
		main["volumeMounts"] = append(certIssuerObjectList(main["volumeMounts"]), map[string]any{"name": "pki-runtime", "mountPath": "/run/openbao-pki"})
		pod["shareProcessNamespace"] = true
		pod["volumes"] = append(certIssuerObjectList(pod["volumes"]), map[string]any{"name": "pki-runtime", "emptyDir": map[string]any{}}, map[string]any{"name": "pki-state", "persistentVolumeClaim": map[string]any{"claimName": "openbao-pki-identity"}})
		makeHost := func(name string) map[string]any {
			return map[string]any{"name": name, "image": main["image"], "command": []any{"never-import-launch"}, "env": []any{map[string]any{"name": "OPENBAO_PKI_RUNTIME_DIR", "value": "/run/openbao-pki/private"}, map[string]any{"name": "OPENBAO_PKI_PROCESS_PID_FILE", "value": "/run/openbao-pki/private/openbao.pid"}, map[string]any{"name": "PKI_ENVIRONMENT", "value": environment}, map[string]any{"name": "PKI_DATABASE_URL", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "original-pki-database", "key": "url"}}}}, "volumeMounts": []any{map[string]any{"name": "pki-state", "mountPath": "/var/lib/openbao-pki"}, map[string]any{"name": "pki-runtime", "mountPath": "/run/openbao-pki"}}}
		}
		pod["containers"] = append(certIssuerObjectList(pod["containers"]), makeHost("openbao-pki"))
		pod["initContainers"] = []any{makeHost("openbao-pki-install")}
		tlsCert, tlsKey = "/run/openbao-pki/private/current/chain.pem", "/run/openbao-pki/private/current/key.pem"
	} else {
		main["volumeMounts"] = append(certIssuerObjectList(main["volumeMounts"]), map[string]any{"name": "openbao-tls", "mountPath": "/openbao/tls", "readOnly": true})
		pod["volumes"] = append(certIssuerObjectList(pod["volumes"]), map[string]any{"name": "openbao-tls", "secret": map[string]any{"secretName": "original-openbao-tls"}})
	}
	claim := func(name, size string) any {
		return map[string]any{"metadata": map[string]any{"name": name}, "spec": map[string]any{"accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": size}}}}
	}
	controller := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": meta("openbao", "original-controller"), "spec": map[string]any{"replicas": float64(1), "serviceName": "openbao-internal", "selector": map[string]any{"matchLabels": selector}, "podManagementPolicy": "OrderedReady", "updateStrategy": map[string]any{"type": "OnDelete"}, "volumeClaimTemplates": []any{claim("data", "10Gi"), claim("audit", "5Gi")}, "template": map[string]any{"metadata": map[string]any{"labels": selector}, "spec": pod}}}
	hcl := "listener \"tcp\" {\n  tls_cert_file = \"" + tlsCert + "\"\n  tls_key_file = \"" + tlsKey + "\"\n}\nstorage \"file\" {\n  path = \"/openbao/data\"\n}\n"
	config := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": meta("openbao-config", "original-config"), "data": map[string]any{"extraconfig-from-values.hcl": hcl}}
	return env, release, []deploymentplan.Object{controller, config}
}

func TestSharedOpenBaoFileProfilePreservesBothTransportLifecycles(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, managed := range []bool{false, true} {
			t.Run(environment+map[bool]string{false: "/static", true: "/managed"}[managed], func(t *testing.T) {
				env, release, live := existingFileOpenBaoFixture(environment, managed)
				before := deploymentplan.Digest(live)
				bindings, err := observeEnvironmentBindings(environment, release, live)
				if err != nil {
					t.Fatal(err)
				}
				object, err := renderEnvironmentOpenBao(env, bindings.Pods[0])
				if environment == "prod" {
					if err == nil {
						t.Fatal("single file writer qualified production")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := applyEnvironmentPodBindings(object, bindings, release); err != nil {
					t.Fatal(err)
				}
				defs := []deploymentplan.Definition{{Object: object, Phase: "workload"}}
				if err := bindOpenBaoAllocations(defs, live); err != nil {
					t.Fatal(err)
				}
				if err := validateEnvironmentPKITransition(defs, live); err != nil {
					t.Fatal(err)
				}
				if deploymentplan.Digest(live) != before {
					t.Fatal("observation mutated live lifecycle state")
				}
				spec := deploymentplan.Map(object["spec"])
				original := deploymentplan.Map(live[0]["spec"])
				for _, field := range []string{"selector", "serviceName", "podManagementPolicy", "volumeClaimTemplates"} {
					if deploymentplan.Digest(spec[field]) != deploymentplan.Digest(original[field]) {
						t.Fatal("immutable allocation changed", field)
					}
				}
				pod := environmentPKIPod(object)
				main := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
				if main["image"] != release.Images["LKE_OPENBAO_IMAGE"].Reference {
					t.Fatal("main did not select common official provider image")
				}
				raw, _ := json.Marshal(object)
				if strings.Contains(string(raw), "legacy-chart-command") || strings.Contains(string(raw), "999m") || strings.Contains(string(raw), "never-import-launch") || strings.Contains(string(raw), "bootstrap") {
					t.Fatal("renderer imported launch behavior or enabled bootstrap")
				}
				if spec["updateStrategy"].(map[string]any)["type"] != "OnDelete" {
					t.Fatal("common update strategy ignored")
				}
				if managed {
					if pod["shareProcessNamespace"] != true || len(certIssuerObjectList(pod["initContainers"])) != 1 {
						t.Fatal("TLS reload lifecycle lost")
					}
					for _, group := range []string{"containers", "initContainers"} {
						for _, v := range certIssuerObjectList(pod[group]) {
							c := deploymentplan.Map(v)
							if c["name"] != "openbao" && c["image"] != release.Images["LKE_OPENBAO_PKI_IMAGE"].Reference {
								t.Fatal("host did not select formal CI image")
							}
						}
					}
					if !strings.Contains(string(raw), "private/openbao.pid") || !strings.Contains(string(raw), "private/ready") {
						t.Fatal("reload PID/readiness not wired")
					}
				}
				for _, dependency := range environmentOpenBaoDependencies(env) {
					if dependency["kind"] == "ConfigMap" {
						t.Fatal("file backend would overwrite existing provider/seal configuration")
					}
				}
				// Exercise the real planner boundary, including retained ConfigMap,
				// Secret and identity PVC references, not only the renderer skeleton.
				for _, ref := range environmentResourceReferences(object) {
					if ref.ID == deploymentplan.ID(live[1]) {
						continue
					}
					dependency := deploymentplan.Object{"apiVersion": "v1", "kind": ref.Kind, "metadata": map[string]any{"name": ref.Name, "namespace": ref.Namespace, "uid": "fixture-" + ref.Name, "resourceVersion": "1"}}
					if ref.Kind == "Secret" {
						dependency["data"] = map[string]any{"key": "retained-key-fixture"}
					}
					live = append(live, dependency)
				}
				bindings, err = observeEnvironmentBindings(environment, release, live)
				if err != nil {
					t.Fatal(err)
				}
				defs = append(defs, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": bindings.Stack + "-secrets"}}, Phase: "namespace"})
				defs, err = bindRetainedEnvironmentDependencies(defs, live, bindings)
				if err != nil {
					t.Fatal(err)
				}
				plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: bindings.Stack, Operation: "upgrade", ConfigurationSHA256: deploymentplan.Digest(env), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(bindings)}, defs, live, bindings.Owners)
				if err != nil {
					t.Fatal(err)
				}
				for _, resource := range plan.Resources {
					if resource.Desired["kind"] == "ConfigMap" || resource.Desired["kind"] == "Secret" || resource.Desired["kind"] == "PersistentVolumeClaim" {
						if resource.Action != "retain" || resource.UID == "" || resource.Desired["data"] != nil {
							t.Fatal("plan would rewrite lifecycle configuration or keys", resource.ID)
						}
					}
				}
			})
		}
	}
}

func TestOpenBaoCompatibilityRejectsRealTransitionsAndUnknownComponents(t *testing.T) {
	for _, mode := range []string{"backend", "claim-size", "policy", "unknown-sidecar", "missing-installer", "missing-host-image", "config-ambiguous", "PID-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			env, release, live := existingFileOpenBaoFixture("dev", true)
			pod := environmentPKIPod(live[0])
			switch mode {
			case "backend":
				env["OPENBAO_STORAGE_BACKEND"] = "raft"
			case "claim-size":
				env["OPENBAO_DATA_STORAGE"] = "20Gi"
			case "policy":
				env["OPENBAO_POD_MANAGEMENT_POLICY"] = "Parallel"
			case "unknown-sidecar":
				deploymentplan.Map(certIssuerObjectList(pod["containers"])[1])["name"] = "unknown"
			case "missing-installer":
				delete(pod, "initContainers")
			case "missing-host-image":
				delete(env, "LKE_OPENBAO_PKI_IMAGE")
			case "config-ambiguous":
				deploymentplan.Map(live[1]["data"])["second.hcl"] = "storage \"file\" {}"
			case "PID-mismatch":
				c := deploymentplan.Map(certIssuerObjectList(pod["initContainers"])[0])
				deploymentplan.Map(certIssuerObjectList(c["env"])[1])["value"] = "/wrong/pid"
			}
			bindings, err := observeEnvironmentBindings("dev", release, live)
			if err == nil {
				var object deploymentplan.Object
				object, err = renderEnvironmentOpenBao(env, bindings.Pods[0])
				if err == nil {
					err = bindOpenBaoAllocations([]deploymentplan.Definition{{Object: object}}, live)
				}
			}
			if err == nil {
				t.Fatal("incompatible lifecycle was accepted")
			}
		})
	}
}

func TestSharedOpenBaoPreservesRotatingLocalReviewerOnlyInPrimaryContainer(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			env, release, live := existingFileOpenBaoFixture(environment, true)
			pod := environmentPKIPod(live[0])
			projection := deploymentplan.Object{"name": "openbao-kubernetes-reviewer", "projected": deploymentplan.Object{"sources": []any{deploymentplan.Object{"serviceAccountToken": deploymentplan.Object{"expirationSeconds": float64(900), "path": "token"}}, deploymentplan.Object{"configMap": deploymentplan.Object{"name": "kube-root-ca.crt"}}}}}
			mount := deploymentplan.Object{"name": "openbao-kubernetes-reviewer", "mountPath": "/var/run/secrets/kubernetes.io/serviceaccount", "readOnly": true}
			pod["volumes"] = append(certIssuerObjectList(pod["volumes"]), projection)
			main := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			main["volumeMounts"] = append(certIssuerObjectList(main["volumeMounts"]), mount)
			bindings, err := observeEnvironmentBindings(environment, release, live)
			if err != nil {
				t.Fatal(err)
			}
			target, err := renderEnvironmentOpenBao(env, bindings.Pods[0])
			if err != nil {
				t.Fatal(err)
			}
			if err = applyEnvironmentPodBindings(target, bindings, release); err != nil {
				t.Fatal(err)
			}
			wanted := environmentPKIPod(target)
			if deploymentplan.Digest(managedNamedField(wanted["volumes"], "openbao-kubernetes-reviewer")) != deploymentplan.Digest(projection) {
				t.Fatal("rotating API-default reviewer projection changed")
			}
			for i, value := range certIssuerObjectList(wanted["containers"]) {
				found := managedNamedField(deploymentplan.Map(value)["volumeMounts"], "openbao-kubernetes-reviewer")
				if i == 0 && deploymentplan.Digest(found) != deploymentplan.Digest(mount) || i != 0 && len(found) != 0 {
					t.Fatal("reviewer credential disappeared or spread to another consumer")
				}
			}
		})
	}
}
