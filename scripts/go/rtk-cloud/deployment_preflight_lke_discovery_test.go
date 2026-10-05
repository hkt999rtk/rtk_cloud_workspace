package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeDeploymentPreflightLKERead(t *testing.T, responses map[string]string) string {
	t.Helper()
	// Quote exact query paths rather than treating their '?'/ampersand bytes as
	// shell case-pattern syntax in the shared fake provider executable.
	quoted := map[string]string{}
	for path, body := range responses {
		quoted[shellSingleQuote(path)] = body
	}
	return fakeLinodeCurl(t, quoted)
}

func TestDeploymentPreflightClusterDiscoveryUsesSelectedIDAndFreshRead(t *testing.T) {
	for _, source := range []string{"process", "resolved-env", "adapter-state", "stack-state"} {
		t.Run(source, func(t *testing.T) {
			t.Setenv("LKE_CLUSTER_ID", "")
			t.Setenv("LKE_CLUSTER_LABEL", "")
			paths := provisionPaths{EnvRoot: t.TempDir()}
			env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}
			switch source {
			case "process":
				t.Setenv("LKE_CLUSTER_ID", "123")
				env["LKE_CLUSTER_ID"] = "456"
				writeTestFile(t, filepath.Join(paths.EnvRoot, "adapters", "lke", "state.env"), "LKE_CLUSTER_ID=789\n")
				writeTestFile(t, filepath.Join(paths.EnvRoot, "env", "stack.env"), "LKE_CLUSTER_ID=999\n")
			case "resolved-env":
				env["LKE_CLUSTER_ID"] = "123"
				writeTestFile(t, filepath.Join(paths.EnvRoot, "adapters", "lke", "state.env"), "LKE_CLUSTER_ID=789\n")
			case "adapter-state":
				writeTestFile(t, filepath.Join(paths.EnvRoot, "adapters", "lke", "state.env"), "LKE_CLUSTER_ID=123\n")
				writeTestFile(t, filepath.Join(paths.EnvRoot, "env", "stack.env"), "LKE_CLUSTER_ID=999\n")
			case "stack-state":
				writeTestFile(t, filepath.Join(paths.EnvRoot, "env", "stack.env"), "LKE_CLUSTER_ID=123\n")
			}
			log := fakeDeploymentPreflightLKERead(t, map[string]string{
				"/lke/clusters/123": `{"id":123,"label":"renamed-existing-cluster","region":"us-sea"}`,
			})
			cluster, err := discoverDeploymentPreflightLKECluster(context.Background(), "test-private-token", paths, env, false)
			if err != nil || cluster.ID != 123 || cluster.Label != "renamed-existing-cluster" {
				t.Fatalf("selected %s cluster was misclassified: cluster=%+v error=%v", source, cluster, err)
			}
			if got := readTestFile(t, log); got != "GET /lke/clusters/123\n" {
				t.Fatalf("expected one fresh selected-ID read and no label fallback: %s", got)
			}
		})
	}
}

func TestDeploymentPreflightSelectedClusterCannotBeClassifiedAbsent(t *testing.T) {
	for _, scenario := range []struct {
		name, id, label, response, cause string
	}{
		{name: "missing-selected-cluster", id: "123", response: "__ERROR_404__", cause: "missing or inaccessible"},
		{name: "incorrect-response-ID", id: "123", response: `{"id":456,"label":"video-cloud-dev-lke"}`, cause: "does not match its requested ID"},
		{name: "explicit-label-conflicts-with-ID", id: "123", label: "video-cloud-dev-lke", response: `{"id":123,"label":"different-existing-label"}`, cause: "conflicts with explicit LKE_CLUSTER_LABEL"},
		{name: "missing-response-label", id: "123", response: `{"id":123}`, cause: "does not match its requested ID"},
		{name: "invalid-selected-ID", id: "not-a-number", cause: "must be a positive integer"},
		{name: "private-response-redacted", id: "123", response: "private-response-marker", cause: "does not match its requested ID"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Setenv("LKE_CLUSTER_ID", "")
			t.Setenv("LKE_CLUSTER_LABEL", "")
			paths := provisionPaths{EnvRoot: t.TempDir()}
			env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "LKE_CLUSTER_ID": scenario.id, "LKE_CLUSTER_LABEL": scenario.label}
			log := fakeDeploymentPreflightLKERead(t, map[string]string{"/lke/clusters/123": scenario.response})
			_, err := discoverDeploymentPreflightLKECluster(context.Background(), "private-token-marker", paths, env, false)
			if err == nil || errors.Is(err, errLKEMissingCluster) || !strings.Contains(err.Error(), scenario.cause) {
				t.Fatalf("selected target was allowed through absent-environment policy: %v", err)
			}
			if strings.Contains(err.Error(), "private-token-marker") || strings.Contains(err.Error(), "private-response-marker") {
				t.Fatal("provider credential or response leaked in discovery error")
			}
			got, _ := os.ReadFile(log)
			if scenario.id == "not-a-number" && len(got) != 0 {
				t.Fatalf("invalid selected ID reached provider: %s", got)
			}
			if scenario.id != "not-a-number" && string(got) != "GET /lke/clusters/123\n" {
				t.Fatalf("selected ID error caused discovery fallback or mutation: %s", got)
			}
		})
	}
}

func TestDeploymentPreflightClusterLabelDiscoveryRequiresCompleteInventory(t *testing.T) {
	const first = "/lke/clusters?page_size=500&page=1"
	const second = "/lke/clusters?page_size=500&page=2"
	for _, scenario := range []struct {
		name      string
		responses map[string]string
		id        int
		absent    bool
		cause     string
	}{
		{name: "target-on-second-page", responses: map[string]string{
			first:  `{"data":[{"id":456,"label":"other-stack"}],"page":1,"pages":2,"results":2}`,
			second: `{"data":[{"id":123,"label":"video-cloud-dev-lke"}],"page":2,"pages":2,"results":2}`,
		}, id: 123},
		{name: "absent-after-complete-pagination", responses: map[string]string{
			first:  `{"data":[{"id":456,"label":"other-stack"}],"page":1,"pages":2,"results":2}`,
			second: `{"data":[{"id":789,"label":"another-stack"}],"page":2,"pages":2,"results":2}`,
		}, absent: true},
		{name: "empty-complete-inventory", responses: map[string]string{
			first: `{"data":[],"page":1,"pages":1,"results":0}`,
		}, absent: true},
		{name: "empty-complete-zero-pages-inventory", responses: map[string]string{
			first: `{"data":[],"page":1,"pages":0,"results":0}`,
		}, absent: true},
		{name: "missing-pages-is-not-zero-pages-inventory", responses: map[string]string{
			first: `{"data":[],"page":1,"results":0}`,
		}, cause: "pagination or data is incomplete"},
		{name: "zero-pages-with-reported-results-is-not-complete", responses: map[string]string{
			first: `{"data":[],"page":1,"pages":0,"results":1}`,
		}, cause: "pagination or data is incomplete"},
		{name: "zero-pages-with-actual-results-is-not-complete", responses: map[string]string{
			first: `{"data":[{"id":123,"label":"video-cloud-dev-lke"}],"page":1,"pages":0,"results":0}`,
		}, cause: "pagination or data is incomplete"},
		{name: "missing-pagination-cannot-prove-absence", responses: map[string]string{
			first: `{"data":[]}`,
		}, cause: "pagination or data is incomplete"},
		{name: "missing-data-cannot-prove-absence", responses: map[string]string{
			first: `{"page":1,"pages":1,"results":0}`,
		}, cause: "pagination or data is incomplete"},
		{name: "wrong-page-cannot-prove-absence", responses: map[string]string{
			first: `{"data":[],"page":2,"pages":2,"results":0}`,
		}, cause: "pagination or data is incomplete"},
		{name: "truncated-results-cannot-prove-absence", responses: map[string]string{
			first: `{"data":[],"page":1,"pages":1,"results":1}`,
		}, cause: "does not contain all reported results"},
		{name: "changing-page-count-cannot-prove-absence", responses: map[string]string{
			first:  `{"data":[{"id":456,"label":"other-stack"}],"page":1,"pages":2,"results":2}`,
			second: `{"data":[{"id":789,"label":"another-stack"}],"page":2,"pages":3,"results":2}`,
		}, cause: "changed during pagination"},
		{name: "repeated-ID-cannot-prove-absence", responses: map[string]string{
			first:  `{"data":[{"id":456,"label":"other-stack"}],"page":1,"pages":2,"results":2}`,
			second: `{"data":[{"id":456,"label":"other-stack"}],"page":2,"pages":2,"results":2}`,
		}, cause: "invalid or repeated identities"},
		{name: "ambiguous-label-blocks-qualification", responses: map[string]string{
			first:  `{"data":[{"id":123,"label":"video-cloud-dev-lke"}],"page":1,"pages":2,"results":2}`,
			second: `{"data":[{"id":456,"label":"video-cloud-dev-lke"}],"page":2,"pages":2,"results":2}`,
		}, cause: "selects multiple IDs"},
		{name: "later-request-failure-blocks-qualification", responses: map[string]string{
			first:  `{"data":[{"id":123,"label":"video-cloud-dev-lke"}],"page":1,"pages":2,"results":2}`,
			second: "__ERROR_404__",
		}, cause: "discovery request failed"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Setenv("LKE_CLUSTER_ID", "")
			t.Setenv("LKE_CLUSTER_LABEL", "")
			log := fakeDeploymentPreflightLKERead(t, scenario.responses)
			cluster, err := discoverDeploymentPreflightLKECluster(context.Background(), "private-token-marker", provisionPaths{EnvRoot: t.TempDir()}, map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}, false)
			if scenario.id != 0 && (err != nil || cluster.ID != scenario.id) {
				t.Fatalf("later-page existing cluster was missed: cluster=%+v err=%v", cluster, err)
			}
			if scenario.absent && !errors.Is(err, errLKEMissingCluster) {
				t.Fatalf("complete inventory did not prove absence: %v", err)
			}
			if scenario.cause != "" && (err == nil || errors.Is(err, errLKEMissingCluster) || !strings.Contains(err.Error(), scenario.cause)) {
				t.Fatalf("incomplete/conflicting inventory passed absent-environment policy: %v", err)
			}
			for _, call := range strings.Split(strings.TrimSpace(readTestFile(t, log)), "\n") {
				if call != "GET "+first && call != "GET "+second {
					t.Fatalf("discovery used an unexpected resource or mutation: %s", call)
				}
			}
			if (scenario.id != 0 || len(scenario.responses) > 1) && !strings.Contains(readTestFile(t, log), "GET "+second+"\n") {
				t.Fatal("discovery did not read the remaining inventory page")
			}
		})
	}
}
