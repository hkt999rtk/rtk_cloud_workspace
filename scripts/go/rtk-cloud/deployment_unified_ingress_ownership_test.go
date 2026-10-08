package main

import (
	"encoding/base64"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"testing"
)

func TestOriginalHelmIngressAdmissionRetainedAcrossEnvironments(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, _, release := completeEnvironmentRenderFixture(environment)
			binding := &environmentAdmissionBinding{WebhookName: "ingress-nginx-admission", SecretName: "original-admission-secret", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			definitions, err := renderEnvironmentIngress(env, binding, release)
			if err != nil {
				t.Fatal(err)
			}
			var live []deploymentplan.Object
			for _, d := range definitions {
				if d.Object["kind"] == "ValidatingWebhookConfiguration" || d.Object["kind"] == "Deployment" {
					object := deploymentplan.Clone(d.Object)
					meta := deploymentplan.Map(object["metadata"])
					meta["uid"] = "original-" + deploymentplan.String(meta["name"])
					delete(meta, "labels")
					live = append(live, object)
				}
			}
			live = append(live, deploymentplan.Object{"kind": "Deployment", "metadata": map[string]any{"name": "video-cloud-api", "namespace": release.Stack + "-video-cloud", "uid": "original-api"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "image": release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference}}}}}})
			observed, err := observeEnvironmentBindings(environment, release, live)
			if err != nil {
				t.Fatal(err)
			}
			if observed.IngressAdmission.WebhookName != binding.WebhookName || observed.IngressAdmission.CABundle != binding.CABundle || observed.IngressAdmission.SecretName != binding.SecretName {
				t.Fatal("original admission owner or identity changed")
			}
			if err := validateEnvironmentIdentityBindings(observed, environment); err != nil {
				t.Fatal(err)
			}
			planned, err := renderEnvironmentIngress(env, observed.IngressAdmission, release)
			if err != nil {
				t.Fatal(err)
			}
			if mqttFoundationTarget(planned, "ValidatingWebhookConfiguration", binding.WebhookName) == nil {
				t.Fatal("original owner not rendered")
			}
			for _, scenario := range []string{"foreign", "mixed", "duplicate"} {
				objects := []deploymentplan.Object{deploymentplan.Clone(live[1])}
				// Select the webhook explicitly rather than relying on definition order.
				for _, o := range live {
					if o["kind"] == "ValidatingWebhookConfiguration" {
						objects[0] = deploymentplan.Clone(o)
					}
				}
				hooks := certIssuerObjectList(objects[0]["webhooks"])
				if scenario == "foreign" {
					deploymentplan.Map(deploymentplan.Map(hooks[0])["clientConfig"])["service"] = map[string]any{"namespace": "another-environment", "name": "ingress-nginx-controller-admission"}
				}
				if scenario == "mixed" {
					objects[0]["webhooks"] = append(hooks, map[string]any{"clientConfig": map[string]any{"url": "https://foreign.invalid"}})
				}
				if scenario == "duplicate" {
					other := deploymentplan.Clone(objects[0])
					deploymentplan.Map(other["metadata"])["name"] = "second-admission"
					deploymentplan.Map(other["metadata"])["uid"] = "second-owner"
					objects = append(objects, other)
				}
				if _, err := observeEnvironmentBindings(environment, release, objects); err == nil {
					t.Fatal("unverified admission owner accepted", scenario)
				}
			}
		})
	}
}
