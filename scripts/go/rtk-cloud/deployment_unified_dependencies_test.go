package main

import (
	"errors"
	"reflect"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"testing"
)

func TestEnvironmentDependenciesReadAPIReferencesWithoutInterpretingPrivatePayloads(t *testing.T) {
	for _, kind := range []string{"Secret", "ConfigMap"} {
		object := deploymentplan.Object{"apiVersion": "v1", "kind": kind, "metadata": map[string]any{"name": "private", "namespace": "ns", "annotations": map[string]any{"secretName": "private-diagnostic-fixture"}}, "data": map[string]any{"secretName": "private-diagnostic-fixture", "secret": map[string]any{"name": "do-not-interpret"}}}
		if refs := environmentResourceReferences(object); len(refs) != 0 {
			t.Fatal("private payload interpreted as an API reference")
		}
	}
	pod := map[string]any{"serviceAccountName": "operator", "imagePullSecrets": []any{map[string]any{"name": "pull"}}, "containers": []any{map[string]any{"env": []any{map[string]any{"valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "credentials"}}}}}}, "initContainers": []any{map[string]any{"envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "settings"}}}}}, "volumes": []any{map[string]any{"persistentVolumeClaim": map[string]any{"claimName": "state"}}, map[string]any{"projected": map[string]any{"sources": []any{map[string]any{"secret": map[string]any{"name": "credentials"}}, map[string]any{"configMap": map[string]any{"name": "trust"}}}}}}}
	for _, kind := range []string{"Deployment", "CronJob"} {
		spec := map[string]any{"template": map[string]any{"spec": pod}}
		if kind == "CronJob" {
			spec = map[string]any{"jobTemplate": map[string]any{"spec": spec}}
		}
		object := deploymentplan.Object{"kind": kind, "metadata": map[string]any{"namespace": "ns"}, "spec": spec}
		var ids []string
		for _, ref := range environmentResourceReferences(object) {
			ids = append(ids, ref.ID)
		}
		want := []string{"ConfigMap/ns/settings", "ConfigMap/ns/trust", "PersistentVolumeClaim/ns/state", "Secret/ns/credentials", "Secret/ns/pull", "ServiceAccount/ns/operator"}
		if !reflect.DeepEqual(ids, want) {
			t.Fatal("workload dependencies are incomplete", kind, ids)
		}
	}
}

func TestEnvironmentDependenciesFenceOwnedReferencesAndOrderRBACTargetsAcrossAllEnvironments(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		stack, ns := "video-cloud-"+environment, "video-cloud-"+environment+"-test"
		object := func(kind, name string, fields map[string]any) deploymentplan.Object {
			fields["apiVersion"], fields["kind"], fields["metadata"] = "v1", kind, map[string]any{"name": name, "namespace": ns}
			return fields
		}
		role := object("Role", "z-role", map[string]any{"rules": []any{}})
		role["apiVersion"] = "rbac.authorization.k8s.io/v1"
		sa := object("ServiceAccount", "z-account", map[string]any{})
		binding := object("RoleBinding", "a-binding", map[string]any{"roleRef": map[string]any{"kind": "Role", "name": "z-role", "apiGroup": "rbac.authorization.k8s.io"}, "subjects": []any{map[string]any{"kind": "ServiceAccount", "name": "z-account", "namespace": ns}}})
		binding["apiVersion"] = "rbac.authorization.k8s.io/v1"
		workload := object("Deployment", "consumer", map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"serviceAccountName": "z-account", "volumes": []any{map[string]any{"secret": map[string]any{"secretName": "identity"}}}}}}})
		workload["apiVersion"] = "apps/v1"
		definitions := []deploymentplan.Definition{{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": ns}}, Phase: "namespace"}, {Object: binding, Phase: "configuration"}, {Object: role, Phase: "configuration"}, {Object: sa, Phase: "configuration"}, {Object: workload, Phase: "workload"}}
		bindings := environmentIdentityBindings{Environment: environment, Stack: stack, Owners: map[string]string{}}
		_, err := bindRetainedEnvironmentDependencies(definitions, nil, bindings)
		var blocker *deploymentplan.Blocker
		if !errors.As(err, &blocker) || blocker.ID != "deployment.dependency.missing" {
			t.Fatal("missing identity did not give a stable blocker", err)
		}
		secret := object("Secret", "identity", map[string]any{"data": map[string]any{"key": "private-diagnostic-fixture"}})
		metadata := deploymentplan.Map(secret["metadata"])
		metadata["uid"], metadata["resourceVersion"] = "identity-owner", "1"
		_, err = bindRetainedEnvironmentDependencies(definitions, []deploymentplan.Object{secret}, bindings)
		if !errors.As(err, &blocker) || blocker.ID != "deployment.ownership.dependency" {
			t.Fatal("unowned identity was not blocked", err)
		}
		bindings.Owners[deploymentplan.ID(secret)] = "identity-owner"
		bound, err := bindRetainedEnvironmentDependencies(definitions, []deploymentplan.Object{secret}, bindings)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: stack, Operation: "upgrade", ConfigurationSHA256: deploymentplan.Digest("config"), ReleaseSHA256: deploymentplan.Digest("release"), BindingsSHA256: deploymentplan.Digest(bindings)}, bound, []deploymentplan.Object{secret}, bindings.Owners)
		if err != nil {
			t.Fatal(err)
		}
		positions := map[string]int{}
		for i, r := range plan.Resources {
			positions[r.ID] = i
		}
		if positions[deploymentplan.ID(binding)] < positions[deploymentplan.ID(role)] || positions[deploymentplan.ID(binding)] < positions[deploymentplan.ID(sa)] || positions[deploymentplan.ID(workload)] < positions[deploymentplan.ID(secret)] {
			t.Fatal("dependencies did not precede their consumers")
		}
	}
}
