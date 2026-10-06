package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCommonPlanPermissionsMatchCollectionCreateNamedUpdatesAndWaiting(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			stack := "video-cloud-" + environment
			plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: stack}, Resources: []deploymentplan.Resource{
				{ID: "Deployment/" + stack + "-platform/app", Action: "update", Desired: deploymentplan.Object{"apiVersion": "apps/v1"}},
				{ID: "Secret/" + stack + "-platform/new", Action: "create", Desired: deploymentplan.Object{"apiVersion": "v1"}},
				{ID: "PersistentVolumeClaim/" + stack + "-platform/state", Action: "retain", Desired: deploymentplan.Object{"apiVersion": "v1"}, Baseline: deploymentplan.Object{"spec": deploymentplan.Object{"volumeName": "original-volume"}}},
				{ID: "Service/" + stack + "-platform/old", Action: "retire", Desired: deploymentplan.Object{"apiVersion": "v1"}},
				{ID: "Namespace//" + stack + "-platform", Action: "retain", Desired: deploymentplan.Object{"apiVersion": "v1"}},
			}}
			requirements, err := environmentPlanAccessRequirements(plan)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[environmentAccessRequirement]bool{}
			for _, r := range requirements {
				if seen[r] {
					t.Fatal("duplicate permission review")
				}
				seen[r] = true
			}
			for _, wanted := range []environmentAccessRequirement{
				{Group: "apps", Resource: "deployments", Namespace: stack + "-platform", Name: "app", Verb: "update"},
				{Group: "apps", Resource: "deployments", Namespace: stack + "-platform", Name: "app", Verb: "watch"},
				{Group: "apps", Resource: "deployments", Namespace: stack + "-platform", Name: "app", Verb: "list"},
				{Group: "apps", Resource: "replicasets", Namespace: stack + "-platform", Verb: "list"},
				{Resource: "pods", Namespace: stack + "-platform", Verb: "list"},
				{Resource: "secrets", Namespace: stack + "-platform", Verb: "list"},
				{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Verb: "list"},
				{Resource: "secrets", Namespace: stack + "-platform", Verb: "create"},
				{Resource: "persistentvolumeclaims", Namespace: stack + "-platform", Name: "state", Verb: "watch"},
				{Resource: "persistentvolumes", Name: "original-volume", Verb: "get"},
				{Resource: "services", Namespace: stack + "-platform", Name: "old", Verb: "delete"},
				{Resource: "namespaces", Verb: "list"},
			} {
				if !seen[wanted] {
					t.Fatal("missing actual execution permission", wanted)
				}
			}
			if seen[environmentAccessRequirement{Resource: "secrets", Namespace: stack + "-platform", Name: "new", Verb: "create"}] {
				t.Fatal("named create cannot prove collection authorization")
			}
			if seen[environmentAccessRequirement{Resource: "persistentvolumeclaims", Namespace: stack + "-platform", Name: "state", Verb: "update"}] {
				t.Fatal("retained data requested mutation rights")
			}
		})
	}
}

func TestNativePlanAccessReviewsRejectDeniedMissingOrEvaluationErrors(t *testing.T) {
	for _, mode := range []string{"valid", "denied", "ambiguous", "missing", "evaluation-error", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			kube := frozenKubernetesFixture(t)
			raw, _ := yaml.Marshal(kube)
			backend, err := newEnvironmentKubernetesBackend(string(raw), "dev")
			if err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			input := filepath.Join(directory, "review.json")
			response := map[string]any{"status": map[string]any{"allowed": true}}
			switch mode {
			case "denied":
				response["status"] = map[string]any{"allowed": false, "reason": "private-denial"}
			case "ambiguous":
				response["status"] = map[string]any{"allowed": true, "denied": true}
			case "missing":
				response = map[string]any{}
			case "evaluation-error":
				response["status"] = map[string]any{"allowed": true, "evaluationError": "private-evaluation-error"}
			}
			responseRaw, _ := json.Marshal(response)
			responsePath := filepath.Join(directory, "response.json")
			writeTestFile(t, responsePath, string(responseRaw))
			code := `#!/bin/sh
case "$*" in
 *'create --raw=/apis/authorization.k8s.io/v1/selfsubjectaccessreviews -f -'*) cat >> "$RTK_TEST_REVIEW_INPUT"; printf '\n' >> "$RTK_TEST_REVIEW_INPUT"; cat "$RTK_TEST_REVIEW_RESPONSE";;
 *) exit 1;;
esac
`
			if mode == "unavailable" {
				code = "#!/bin/sh\nexit 1\n"
			}
			writeTestFile(t, filepath.Join(directory, "kubectl"), code)
			os.Chmod(filepath.Join(directory, "kubectl"), 0755)
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_TEST_REVIEW_INPUT", input)
			t.Setenv("RTK_TEST_REVIEW_RESPONSE", responsePath)
			plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev"}, Resources: []deploymentplan.Resource{{ID: "Deployment/video-cloud-dev-platform/app", Action: "update", Desired: deploymentplan.Object{"apiVersion": "apps/v1"}}}}
			err = backend.CheckPlanAccess(context.Background(), plan)
			if (err == nil) != (mode == "valid") {
				t.Fatal("incorrect access decision", err)
			}
			if err != nil && strings.Contains(err.Error(), "private-") {
				t.Fatal("authorizer details exposed")
			}
			if mode == "valid" {
				raw, _ := os.ReadFile(input)
				requirements, _ := environmentPlanAccessRequirements(plan)
				lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
				if len(lines) != len(requirements) {
					t.Fatal("not every execution/inventory permission was reviewed")
				}
				for i, line := range lines {
					var request struct {
						Kind string
						Spec struct {
							ResourceAttributes environmentAccessRequirement `json:"resourceAttributes"`
						}
					}
					if json.Unmarshal([]byte(line), &request) != nil || request.Kind != "SelfSubjectAccessReview" || request.Spec.ResourceAttributes != requirements[i] {
						t.Fatal("review escaped exact planned operation")
					}
				}
			}
		})
	}
}

func TestCommonPlanPermissionsRejectUnknownKindsAndWrongAPIGroups(t *testing.T) {
	for _, resource := range []deploymentplan.Resource{
		{ID: "Unknown/ns/object", Action: "create", Desired: deploymentplan.Object{"apiVersion": "v1"}},
		{ID: "Deployment/ns/object", Action: "update", Desired: deploymentplan.Object{"apiVersion": "v1"}},
		{ID: "Secret/ns/object", Action: "update", Desired: deploymentplan.Object{"apiVersion": "apps/v1"}},
		{ID: "Secret/ns/object", Action: "unknown", Desired: deploymentplan.Object{"apiVersion": "v1"}},
	} {
		if _, err := environmentPlanAccessRequirements(&deploymentplan.Plan{Resources: []deploymentplan.Resource{resource}}); err == nil {
			t.Fatal("ambiguous permission mapping accepted", resource.ID)
		}
	}
}
