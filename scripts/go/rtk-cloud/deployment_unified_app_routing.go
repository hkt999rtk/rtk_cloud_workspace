package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const environmentAppRoutingDeclaration = "pki-create-app-routing"

// The declaration is created only after the actual Service and listener owner
// exist. DeclarationUID is captured from live metadata, never put into its own
// desired body or used as a substitute for provider allocation evidence.
type environmentAppRoutingBinding struct {
	Schema           string `json:"schema"`
	Environment      string `json:"environment"`
	Stack            string `json:"stack"`
	Mode             string `json:"mode"`
	ServiceUID       string `json:"service_uid"`
	DeploymentUID    string `json:"deployment_uid"`
	CreatePlanSHA256 string `json:"create_plan_sha256"`
	DeclarationUID   string `json:"declaration_uid,omitempty"`
}

func environmentAppRoutingDefinition(binding environmentAppRoutingBinding) deploymentplan.Definition {
	binding.DeclarationUID = ""
	raw, _ := json.Marshal(binding)
	return deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{
		"apiVersion": "v1", "kind": "ConfigMap", "immutable": true,
		"metadata": deploymentplan.Object{"name": environmentAppRoutingDeclaration, "namespace": binding.Stack + "-video-cloud", "labels": deploymentplan.Object{"rtk.realtek.com/stack": binding.Stack}},
		"data":     deploymentplan.Object{"binding.json": string(raw)},
	}}
}

func environmentAppInternalServiceDependencies(env map[string]string) []deploymentplan.Definition {
	ns, stack := lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"]
	selector := deploymentplan.Object{"app.kubernetes.io/name": "video-cloud-api-app-pki"}
	meta := func(name string) deploymentplan.Object {
		return deploymentplan.Object{"name": name, "namespace": ns, "labels": deploymentplan.Object{"rtk.realtek.com/stack": stack}}
	}
	return []deploymentplan.Definition{
		{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Service", "metadata": meta("video-cloud-api-app-pki"), "spec": deploymentplan.Object{"type": "ClusterIP", "selector": selector, "ports": []any{deploymentplan.Object{"name": "https", "port": float64(8443), "targetPort": float64(8443)}}}}},
		{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": meta("allow-video-cloud-api-app-pki"), "spec": deploymentplan.Object{"podSelector": deploymentplan.Object{"matchLabels": selector}, "policyTypes": []any{"Ingress"}, "ingress": []any{deploymentplan.Object{"from": []any{deploymentplan.Object{"namespaceSelector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"rtk.realtek.com/stack": stack}}, "podSelector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"rtk.cloud/pki-consumer": "true"}}}}, "ports": []any{deploymentplan.Object{"protocol": "TCP", "port": float64(8443)}}}}}}},
	}
}

func validateEnvironmentAppRoutingBinding(b environmentIdentityBindings) error {
	r := b.AppRouting
	if r == nil {
		return nil
	}
	ns := b.Stack + "-video-cloud"
	if b.AppIngress != nil || r.Schema != "rtk-cloud.native-app-routing/v1" || r.Environment != b.Environment || r.Stack != b.Stack || r.Mode != "internal-service" || !environmentPreflightDigest.MatchString(r.CreatePlanSHA256) || r.DeclarationUID == "" || r.ServiceUID == "" || r.DeploymentUID == "" ||
		b.Owners["ConfigMap/"+ns+"/"+environmentAppRoutingDeclaration] != r.DeclarationUID ||
		b.Owners["Service/"+ns+"/video-cloud-api-app-pki"] != r.ServiceUID ||
		b.Owners["Deployment/"+ns+"/video-cloud-api-app-pki"] != r.DeploymentUID {
		return environmentAppBlocker(environmentAppRoutingDeclaration, "original explicit internal App routing declaration or its allocated owners changed")
	}
	for _, pod := range b.Pods {
		if pod.Name == "video-cloud-api-app-pki" && pod.Component == "managed-app-api" && pod.NativeTrustInitialization {
			return nil
		}
	}
	return environmentAppBlocker(environmentAppRoutingDeclaration, "internal routing declaration requires its original native App listener owner")
}

func observeEnvironmentAppRouting(b *environmentIdentityBindings, object deploymentplan.Object, live []deploymentplan.Object) error {
	ns := b.Stack + "-video-cloud"
	if b.AppRouting != nil || deploymentplan.ID(object) != "ConfigMap/"+ns+"/"+environmentAppRoutingDeclaration || object["immutable"] != true {
		return environmentAppBlocker(environmentAppRoutingDeclaration, "internal App route requires one original immutable allocated declaration")
	}
	var binding environmentAppRoutingBinding
	if decodeEnvironmentJSON([]byte(deploymentplan.String(deploymentplan.Map(object["data"])["binding.json"])), &binding) != nil || binding.DeclarationUID != "" {
		return environmentAppBlocker(environmentAppRoutingDeclaration, "internal App routing declaration is invalid")
	}
	binding.DeclarationUID = deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
	if binding.Schema != "rtk-cloud.native-app-routing/v1" || binding.Environment != b.Environment || binding.Stack != b.Stack || binding.Mode != "internal-service" || !environmentPreflightDigest.MatchString(binding.CreatePlanSHA256) {
		return environmentAppBlocker(environmentAppRoutingDeclaration, "internal App routing declaration belongs to another mode or environment")
	}
	path, err := privateDeploymentStatePath(b.Environment, filepath.Join("pki", "app", "routing.json"))
	var original environmentAppRoutingBinding
	raw, readErr := readBoundedEnvironmentFile(path, true)
	if err != nil || readErr != nil || decodeEnvironmentJSON(raw, &original) != nil || original != binding {
		return environmentAppBlocker(environmentAppRoutingDeclaration, "original private App routing allocation evidence is missing or differs from the live declaration")
	}
	service := deploymentplan.Object(nil)
	for _, current := range live {
		if deploymentplan.ID(current) == "Service/"+ns+"/video-cloud-api-app-pki" {
			if service != nil {
				return environmentAppBlocker(environmentAppRoutingDeclaration, "internal App Service ownership is ambiguous")
			}
			service = current
		}
	}
	expected := environmentAppInternalServiceDependencies(map[string]string{"CLOUD_STACK_NAME": b.Stack})[0].Object
	if service == nil || deploymentplan.String(deploymentplan.Map(service["metadata"])["uid"]) != binding.ServiceUID || !deploymentplan.Matches(deploymentplan.Projection(expected), deploymentplan.Projection(service)) {
		return environmentAppBlocker(environmentAppRoutingDeclaration, "original internal App Service reference or endpoint changed")
	}
	b.AppRouting = &binding
	return nil
}

func publishEnvironmentCreateAppRouting(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, apply environmentCreateBootstrapApply) error {
	if p.AppRoutingMode != "internal-service" || p.Environment != cfg.Environment || p.Stack != cfg.Values["CLOUD_STACK_NAME"] || runtime.Execution == nil || runtime.Execution.Store.Environment != cfg.Environment || runtime.Backend.Store.Environment != cfg.Environment || runtime.Execution.Journal.Environment != cfg.Environment || runtime.Execution.Journal.Stack != p.Stack || runtime.Backend.Store.Root != runtime.Execution.Store.Root || runtime.ExecutionSHA256 != runtime.Execution.Journal.PlanSHA256 || apply == nil {
		return environmentCreateBootstrapBlocker("native App routing requires its exact frozen fresh internal-service intent and parent execution")
	}
	if err := runtime.Execution.Check(ctx); err != nil {
		return err
	}
	ns := p.Stack + "-video-cloud"
	serviceID, deploymentID := "Service/"+ns+"/video-cloud-api-app-pki", "Deployment/"+ns+"/video-cloud-api-app-pki"
	service, se := runtime.Backend.Read(ctx, serviceID)
	listener, de := runtime.Backend.Read(ctx, deploymentID)
	expected := environmentAppInternalServiceDependencies(environmentCreateProviderValues(cfg))[0].Object
	if se != nil || de != nil || service == nil || listener == nil || deploymentplan.String(deploymentplan.Map(service["metadata"])["uid"]) == "" || deploymentplan.String(deploymentplan.Map(listener["metadata"])["uid"]) == "" || deploymentplan.Map(service["metadata"])["deletionTimestamp"] != nil || deploymentplan.Map(listener["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(deploymentplan.Projection(expected), deploymentplan.Projection(service)) ||
		deploymentplan.Map(deploymentplan.Map(listener["metadata"])["labels"])["rtk.realtek.com/stack"] != p.Stack || mqttFoundationPrimary(listener)["image"] != p.Image {
		return environmentCreateBootstrapBlocker("native internal App route cannot bind its original allocated Service and listener")
	}
	binding := environmentAppRoutingBinding{Schema: "rtk-cloud.native-app-routing/v1", Environment: p.Environment, Stack: p.Stack, Mode: p.AppRoutingMode, ServiceUID: deploymentplan.String(deploymentplan.Map(service["metadata"])["uid"]), DeploymentUID: deploymentplan.String(deploymentplan.Map(listener["metadata"])["uid"]), CreatePlanSHA256: runtime.ExecutionSHA256}
	definition := environmentAppRoutingDefinition(binding)
	if err := apply(ctx, "native-app-routing-profile", []deploymentplan.Definition{definition}); err != nil {
		return err
	}
	actual, ae := runtime.Backend.Read(ctx, deploymentplan.ID(definition.Object))
	serviceNow, se := runtime.Backend.Read(ctx, serviceID)
	listenerNow, de := runtime.Backend.Read(ctx, deploymentID)
	if ae != nil || se != nil || de != nil || actual == nil || serviceNow == nil || listenerNow == nil || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) == "" || !deploymentplan.Matches(deploymentplan.Projection(definition.Object), deploymentplan.Projection(actual)) || !deploymentplan.Matches(deploymentplan.Projection(expected), deploymentplan.Projection(serviceNow)) || !deploymentplan.Matches(deploymentplan.Projection(listener), deploymentplan.Projection(listenerNow)) || deploymentplan.Map(serviceNow["metadata"])["uid"] != binding.ServiceUID || deploymentplan.Map(listenerNow["metadata"])["uid"] != binding.DeploymentUID {
		return environmentCreateBootstrapBlocker("original App routing declaration, Service or listener changed before readback")
	}
	if err := runtime.Execution.Check(ctx); err != nil {
		return err
	}
	binding.DeclarationUID = deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"])
	if _, err := writeEnvironmentCreateBootstrapJSON(runtime.Execution.Store, filepath.Join("pki", "app", "routing.json"), binding); err != nil {
		return err
	}
	key := "native-app-routing-profile-proof"
	raw, _ := json.Marshal(binding)
	if err := runtime.Execution.BeginEntry(key, "bootstrap", raw, time.Now().UTC()); err != nil {
		return err
	}
	if err := runtime.Execution.RecordReceipt(key, raw); err != nil {
		return err
	}
	return runtime.Execution.CompleteEntry(key, time.Now().UTC())
}
