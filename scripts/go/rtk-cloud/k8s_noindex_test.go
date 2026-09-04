package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLKEIngressNoIndexHelmRoundTrip(t *testing.T) {
	var config map[string]string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lkeIngressNoIndexHelmValue(), "controller.config=")), &config); err != nil {
		t.Fatal(err)
	}
	if config["server-snippet"] != lkeNoIndexServerSnippet || config["location-snippet"] != lkeNoIndexHeader {
		t.Fatal("Helm encoding lost the crawler policy")
	}
	if config["allow-snippet-annotations"] != "true" || config["annotations-risk-level"] != "Critical" {
		t.Fatal("mTLS forwarding annotations must remain enabled")
	}
}

func TestLKEFrontendAlwaysDisablesSearchIndexing(t *testing.T) {
	for _, stack := range []string{"video-cloud-dev", "video-cloud-staging", "video-cloud-prod"} {
		env := k8sWorkloadTestEnv()
		env["CLOUD_STACK_NAME"] = stack
		for _, workload := range k8sWorkloads(env) {
			if workload.Key != "frontend" {
				continue
			}
			manifest := lkeDeploymentManifest(env, workload, nil)
			if !strings.Contains(manifest, "- name: DISABLE_SEARCH_INDEXING\n              value: \"true\"") {
				t.Fatalf("%s frontend can expose its sitemap", stack)
			}
		}
	}
}
