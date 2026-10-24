package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateStageParentIntentBindsExactPlanWithoutPrivateSecretBodies(t *testing.T) {
	private := "private-native-server-export-response"
	encoded := base64.StdEncoding.EncodeToString([]byte(private))
	plan := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}, Resources: []deploymentplan.Resource{{ID: "Secret/video-cloud-dev-video-cloud/video-cloud-api-pki-tls", Phase: "configuration", Action: "create", Desired: environmentCreateSecret("video-cloud-dev-video-cloud", "video-cloud-api-pki-tls", map[string]string{"tls.key": private}).Object}}}
	before := plan.SHA256()
	intent, err := environmentCreateKubernetesStageIntent(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(intent), private) || strings.Contains(string(intent), encoded) || strings.Contains(string(intent), "tls.key") || strings.Contains(string(intent), "valueFrom") || plan.SHA256() != before {
		t.Fatal("parent stage intent retained native credential bodies or changed the admitted plan")
	}
	var proof struct {
		PlanSHA256 string `json:"plan_sha256"`
		Resources  []struct {
			ID            string `json:"resource_id"`
			DesiredSHA256 string `json:"desired_sha256"`
		} `json:"resources"`
	}
	if json.Unmarshal(intent, &proof) != nil || proof.PlanSHA256 != before || len(proof.Resources) != 1 || proof.Resources[0].ID != plan.Resources[0].ID || proof.Resources[0].DesiredSHA256 != deploymentplan.Digest(plan.Resources[0].Desired) {
		t.Fatal("safe parent intent lost exact resource-plan binding")
	}
	plan.Resources[0].Desired["data"] = map[string]any{"tls.key": "different-original-input"}
	changed, err := environmentCreateKubernetesStageIntent(plan)
	if err != nil || string(changed) == string(intent) {
		t.Fatal("changed private input did not change the exact safe plan digest")
	}
}
