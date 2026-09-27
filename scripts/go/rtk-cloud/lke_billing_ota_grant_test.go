package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLKEBillingOTAGrantHistoryBoundary(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}
	t.Setenv("LKE_BILLING_OTA_GRANT_HISTORY_ENABLED", "false")
	if got := lkeBillingOTAGrantHistorySecretFields(env); got != "" {
		t.Fatal("grant-history credentials appeared without opt-in")
	}
	if strings.Contains(lkeBillingSecretManifest(env), "BILLING_OTA_GRANT_HISTORY_") {
		t.Fatal("Billing runtime Secret exposes grant-history settings without opt-in")
	}
	for _, manifest := range lkePublicHTTPSNetworkPolicyManifests(env, nil) {
		if strings.Contains(manifest, "allow-billing-account-manager") {
			t.Fatal("Billing ingress appeared without opt-in")
		}
	}

	t.Setenv("LKE_BILLING_OTA_GRANT_HISTORY_ENABLED", "true")
	t.Setenv("LKE_INTERNAL_AUTH", strings.Repeat("a", 40))
	if err := lkeValidateBillingOTAGrantHistoryRuntime(env); err != nil {
		t.Fatal(err)
	}
	fields := lkeBillingOTAGrantHistorySecretFields(env)
	if !strings.Contains(lkeBillingSecretManifest(env), fields) {
		t.Fatal("Billing runtime Secret omitted the paired grant-history settings")
	}
	if !strings.Contains(fields, "BILLING_OTA_GRANT_HISTORY_BASE_URL: \"http://account-manager.video-cloud-dev-account-manager.svc.cluster.local:80\"") ||
		!strings.Contains(fields, "BILLING_OTA_GRANT_HISTORY_TOKEN: \""+strings.Repeat("a", 40)+"\"") {
		t.Fatal("grant-history endpoint and token were not paired")
	}
	var policy struct {
		Metadata struct{ Name, Namespace string }
		Spec     struct {
			PodSelector struct {
				MatchLabels map[string]string `yaml:"matchLabels"`
			} `yaml:"podSelector"`
			Ingress []struct {
				From []struct {
					NamespaceSelector struct {
						MatchLabels map[string]string `yaml:"matchLabels"`
					} `yaml:"namespaceSelector"`
					PodSelector struct {
						MatchLabels map[string]string `yaml:"matchLabels"`
					} `yaml:"podSelector"`
				} `yaml:"from"`
				Ports []struct {
					Protocol string
					Port     int
				} `yaml:"ports"`
			} `yaml:"ingress"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(lkeAllowBillingAccountManagerNetworkPolicyManifest(env)), &policy); err != nil {
		t.Fatal(err)
	}
	if policy.Metadata.Name != "allow-billing-account-manager" || policy.Metadata.Namespace != "video-cloud-dev-account-manager" ||
		policy.Spec.PodSelector.MatchLabels["app.kubernetes.io/name"] != "account-manager" || len(policy.Spec.Ingress) != 1 ||
		len(policy.Spec.Ingress[0].From) != 1 || len(policy.Spec.Ingress[0].Ports) != 1 ||
		policy.Spec.Ingress[0].From[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "video-cloud-dev-billing" ||
		policy.Spec.Ingress[0].From[0].PodSelector.MatchLabels["app.kubernetes.io/name"] != "billing" ||
		policy.Spec.Ingress[0].Ports[0].Protocol != "TCP" || policy.Spec.Ingress[0].Ports[0].Port != 8080 {
		t.Fatalf("grant-history ingress is not restricted to Billing Pod on Account Manager TCP 8080")
	}
}

func TestLKEBillingOTAGrantHistoryRejectsWeakToken(t *testing.T) {
	t.Setenv("LKE_BILLING_OTA_GRANT_HISTORY_ENABLED", "true")
	t.Setenv("LKE_INTERNAL_AUTH", "short")
	if err := lkeValidateBillingOTAGrantHistoryRuntime(map[string]string{}); err == nil {
		t.Fatal("short Account Manager token was accepted")
	}
}
