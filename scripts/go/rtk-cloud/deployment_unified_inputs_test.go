package main

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedBindingsCannotOverrideReleaseOrRemoveConsumer(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			binding := environmentIdentityBindings{Version: 1, Environment: environment, Stack: "video-cloud-" + environment, Pods: []environmentPodBindings{{Kind: "Deployment", NamespaceSuffix: "video-cloud", Name: "video-cloud-api", Component: "existing-workload", Containers: []environmentContainerBindings{{Name: "app", ImageKey: "LKE_VIDEO_CLOUD_IMAGE", Fields: map[string]any{"env": []any{map[string]any{"name": "PKI_ENVIRONMENT", "value": environment}}}}}}}}
			if err := validateEnvironmentIdentityBindings(binding, environment); err != nil {
				t.Fatal(err)
			}
			binding.Pods[0].Containers[0].Fields["image"] = "old:latest"
			if err := validateEnvironmentIdentityBindings(binding, environment); err == nil {
				t.Fatal("private snapshot chose image")
			}
			delete(binding.Pods[0].Containers[0].Fields, "image")
			for _, field := range []string{"command", "args", "ports"} {
				binding.Pods[0].Containers[0].Fields[field] = []any{"private-override"}
				if validateEnvironmentIdentityBindings(binding, environment) == nil {
					t.Fatal("private workload behavior accepted", field)
				}
				delete(binding.Pods[0].Containers[0].Fields, field)
			}
			binding.Pods[0].Containers[0].Fields["env"] = []any{map[string]any{"name": "ENABLE_UNREVIEWED_FEATURE", "value": "true"}}
			if validateEnvironmentIdentityBindings(binding, environment) == nil {
				t.Fatal("private feature override accepted")
			}
			binding.Pods[0].Containers[0].Fields["env"] = []any{map[string]any{"name": "PKI_DATABASE_URL", "value": "postgres://user:secret@db"}}
			if err := validateEnvironmentIdentityBindings(binding, environment); err == nil {
				t.Fatal("plaintext credential accepted")
			}
			binding.Pods[0].Containers[0].Fields = map[string]any{}
			object := deploymentplan.Object{"kind": "Deployment", "metadata": map[string]any{"name": "video-cloud-api", "namespace": binding.Stack + "-video-cloud"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app"}, map[string]any{"name": "identity-sidecar"}}}}}}
			if err := applyEnvironmentPodBindings(object, binding, environmentRelease{}); err == nil {
				t.Fatal("sidecar silently removed")
			}
		})
	}
}

func TestUnregisteredManagedSidecarCannotBorrowAnOfficialImage(t *testing.T) {
	if _, err := environmentManagedSidecar("unmaintained-worker"); err == nil {
		t.Fatal("unregistered sidecar accepted")
	}
	for _, name := range []string{"pkibroker", "pkimanagement"} {
		container, err := environmentManagedSidecar(name)
		if err != nil || len(certIssuerObjectList(container["command"])) == 0 {
			t.Fatal("maintained sidecar lacks its own launch behavior", err)
		}
	}
}

func TestManagedOpenBaoCannotReintroduceFileStorage(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		b := environmentPodBindings{Kind: "StatefulSet", NamespaceSuffix: "secrets", Name: "openbao", Component: "managed-openbao", Containers: []environmentContainerBindings{{Name: "openbao", Fields: map[string]any{"command": []any{"sh", "-c", "bao server -config=file.hcl"}}}}}
		if validateOpenBaoBindingProfile(b) == nil {
			t.Fatal("file-storage override accepted")
		}
		object, err := renderEnvironmentOpenBaoRaft(map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + environment, "OPENBAO_EFFECTIVE_REPLICAS": "3", "OPENBAO_DATA_STORAGE": "20Gi", "OPENBAO_AUDIT_STORAGE": "10Gi", "OPENBAO_POD_MANAGEMENT_POLICY": "Parallel", "OPENBAO_UPDATE_STRATEGY": "RollingUpdate", "OPENBAO_REQUEST_CPU": "500m", "OPENBAO_REQUEST_MEMORY": "512Mi", "OPENBAO_NODE_CLASS": "general", "LKE_OPENBAO_IMAGE": "quay.io/openbao/openbao@sha256:" + strings.Repeat("a", 64)}, b)
		if err != nil {
			t.Fatal(err)
		}
		spec := deploymentplan.Map(object["spec"])
		if spec["replicas"] != float64(3) || len(certIssuerObjectList(spec["volumeClaimTemplates"])) != 2 {
			t.Fatal("Raft profile lacks HA persistence")
		}
		pod := deploymentplan.Map(deploymentplan.Map(spec["template"])["spec"])
		if len(deploymentplan.Map(deploymentplan.Map(pod["affinity"])["podAntiAffinity"])["requiredDuringSchedulingIgnoredDuringExecution"].([]any)) != 1 {
			t.Fatal("distinct-node placement missing")
		}
		container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
		fields := map[string]map[string]any{}
		for _, v := range certIssuerObjectList(container["env"]) {
			entry := deploymentplan.Map(v)
			fields[deploymentplan.String(entry["name"])] = entry
		}
		if fields["BAO_RAFT_NODE_ID"]["value"] != "$(POD_NAME)" || deploymentplan.Map(deploymentplan.Map(fields["POD_NAME"]["valueFrom"])["fieldRef"])["fieldPath"] != "metadata.name" {
			t.Fatal("persistent pod identity is not advertised")
		}
		for key, port := range map[string]string{"BAO_API_ADDR": "8200", "BAO_CLUSTER_ADDR": "8201"} {
			if fields[key]["value"] != "https://$(POD_NAME).openbao-internal.video-cloud-"+environment+"-secrets.svc:"+port {
				t.Fatal("peer address differs from pod DNS", key)
			}
		}
		config := deploymentplan.Map(environmentOpenBaoRaftDependencies(map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + environment, "OPENBAO_EFFECTIVE_REPLICAS": "3"})[0]["data"])["raft.hcl"].(string)
		if !strings.Contains(config, `audit "file" "deployment-audit"`) || !strings.Contains(config, `file_path = "/openbao/audit/audit.jsonl"`) || !strings.Contains(config, `log_raw = "false"`) {
			t.Fatal("durable audit is not enabled")
		}
		for _, fields := range []map[string]any{
			{"env": []any{map[string]any{"name": "BAO_CLUSTER_ADDR", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "redirect", "key": "address"}}}}},
			{"volumeMounts": []any{map[string]any{"name": "other", "mountPath": "/openbao/data"}}},
		} {
			b.Containers[0].Fields = fields
			if validateOpenBaoBindingProfile(b) == nil {
				t.Fatal("binding could redirect Raft identity or persistence")
			}
		}
	}
}

func TestManagedImageRolesCannotBeExchangedByIdentityBindings(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		_, bindings, _ := completeEnvironmentRenderFixture(environment)
		if err := validateEnvironmentIdentityBindings(bindings, environment); err != nil {
			t.Fatal(err)
		}
		for index := range bindings.Pods {
			original := bindings.Pods[index].Containers[0].ImageKey
			wrong := "LKE_VIDEO_CLOUD_IMAGE"
			if original == wrong {
				wrong = "LKE_MQTT_PKI_IMAGE"
			}
			bindings.Pods[index].Containers[0].ImageKey = wrong
			if err := validateEnvironmentIdentityBindings(bindings, environment); err == nil {
				t.Fatal("consumer could exchange its maintained image role", bindings.Pods[index].Component)
			}
			bindings.Pods[index].Containers[0].ImageKey = original
		}
	}
}

func TestManagedOpenBaoUsesSharedCapacityAndRequiresProductionQuorum(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		env, bindings, _ := completeEnvironmentRenderFixture(environment)
		var binding environmentPodBindings
		for _, candidate := range bindings.Pods {
			if candidate.Component == "managed-openbao" {
				binding = candidate
			}
		}
		for _, replicas := range []string{"", "1", "2", "3"} {
			env["OPENBAO_EFFECTIVE_REPLICAS"] = replicas
			object, err := renderEnvironmentOpenBaoRaft(env, binding)
			valid := replicas == "3" || replicas == "1" && environment != "prod"
			if (err == nil) != valid {
				t.Fatalf("%s members=%s invalid capacity decision: %v", environment, replicas, err)
			}
			if err != nil {
				continue
			}
			pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])["spec"])
			if fmt.Sprint(deploymentplan.Map(object["spec"])["replicas"]) != replicas || deploymentplan.Map(pod["nodeSelector"])["rtk.io/node-class"] != "general" || pod["automountServiceAccountToken"] != false {
				t.Fatal("renderer ignored shared capacity or placement")
			}
			dependencies := environmentOpenBaoRaftDependencies(env)
			config := deploymentplan.Map(dependencies[0]["data"])["raft.hcl"].(string)
			want, _ := strconv.Atoi(replicas)
			if strings.Count(config, "retry_join {") != want {
				t.Fatal("peer configuration does not match member count")
			}
		}
	}
}
