package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCIRolloutCommandUsesSelectedCIImageAndLimitedIdentity(t *testing.T) {
	kube := frozenKubernetesFixture(t)
	kubeRaw, _ := yaml.Marshal(kube)
	store, err := newSecretStore("", "dev")
	if err != nil {
		t.Fatal(err)
	}
	restricted, err := ciRolloutKubeconfig(string(kubeRaw), "dev", "fixture-restricted-token")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.write("kube/kubeconfig.yaml", restricted, true); err != nil {
		t.Fatal(err)
	}
	workspace := writeDeploymentFixture(t, "dev", "lke")
	repo := "hkt999rtk/rtk_cloud_frontend"
	imageRepo := "ghcr.io/" + repo + "/frontend"
	writeTestFile(t, filepath.Join(workspace, "cloud_env/dev/ci-rollout.env"), "CI_ROLLOUT_BRANCHES={\""+repo+"\":\"main\"}\n")
	old := imageRepo + "@sha256:" + strings.Repeat("a", 64)
	next := imageRepo + "@sha256:" + strings.Repeat("b", 64)
	revision := strings.Repeat("c", 40)
	object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"namespace": "video-cloud-dev-frontend", "name": "frontend", "uid": "uid", "resourceVersion": "1"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "frontend", "image": old}}, "volumes": []any{map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "original-pvc"}}}}}}}
	plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev"}, Resources: []deploymentplan.Resource{{UID: "uid", Desired: object}}}
	planRaw, _ := json.Marshal(plan)
	if err = store.write("deployment/ci-rollout/targets.json", planRaw, true); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(fmt.Sprintf(`{"schema":"rtk-service-image/v1","repository":%q,"image":%q,"digest":%q,"source_commit":%q,"run_id":"3","run_attempt":"1"}`, repo, imageRepo, "sha256:"+strings.Repeat("b", 64), revision))
	archive := publicationZIP(t, "image-manifest.json", manifest)
	runJSON := func(id int, name string) []byte {
		return []byte(fmt.Sprintf(`{"id":%d,"head_sha":%q,"head_branch":"main","path":%q,"event":"push","status":"completed","conclusion":"success","run_attempt":1}`, id, revision, ".github/workflows/"+name))
	}
	responses := map[string][]byte{
		"repos/" + repo + "/commits/main": []byte(`{"sha":"` + revision + `"}`),
		"repos/" + repo + "/actions/workflows/ci.yml/runs?head_sha=" + revision + "&per_page=100":      []byte(fmt.Sprintf(`{"total_count":1,"workflow_runs":[%s]}`, runJSON(2, "ci.yml"))),
		"repos/" + repo + "/actions/workflows/release.yml/runs?head_sha=" + revision + "&per_page=100": []byte(fmt.Sprintf(`{"total_count":1,"workflow_runs":[%s]}`, runJSON(3, "release.yml"))),
		"repos/" + repo + "/actions/runs/2": runJSON(2, "ci.yml"), "repos/" + repo + "/actions/runs/3": runJSON(3, "release.yml"),
		"repos/" + repo + "/actions/runs/3/jobs?per_page=100":      []byte(`{"total_count":1,"jobs":[{"name":"Publish LKE image","status":"completed","conclusion":"success"}]}`),
		"repos/" + repo + "/actions/runs/3/artifacts?per_page=100": []byte(fmt.Sprintf(`{"total_count":1,"artifacts":[{"id":4,"name":"image-manifest","expired":false,"size_in_bytes":%d}]}`, len(archive))),
		"repos/" + repo + "/actions/artifacts/4/zip":               archive,
	}
	encoded := map[string]string{}
	for key, value := range responses {
		encoded[key] = base64.StdEncoding.EncodeToString(value)
	}
	dir := t.TempDir()
	responseFile := filepath.Join(dir, "responses.json")
	responseRaw, _ := json.Marshal(encoded)
	if err = os.WriteFile(responseFile, responseRaw, 0600); err != nil {
		t.Fatal(err)
	}
	cm := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"namespace": "video-cloud-dev-video-cloud", "name": "rtk-ci-state-rtk-cloud-frontend", "uid": "state-uid", "resourceVersion": "1"}, "data": map[string]any{"state.json": ""}}
	stateFile := filepath.Join(dir, "state.json")
	state, _ := json.Marshal(map[string]any{"deployment": object, "configmap": cm, "lease": nil, "fail": false})
	if err = os.WriteFile(stateFile, state, 0600); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(dir, "kubectl")
	if err = os.WriteFile(tool, []byte(ciRolloutFakeKubectl), 0700); err != nil {
		t.Fatal(err)
	}
	gh := `#!/usr/bin/env python3
import base64,json,os,sys
with open(os.environ['CI_RESPONSE_FILE']) as f: responses=json.load(f)
if len(sys.argv)!=3 or sys.argv[1]!='api' or sys.argv[2] not in responses:sys.exit(1)
sys.stdout.buffer.write(base64.b64decode(responses[sys.argv[2]]))
`
	if err = os.WriteFile(filepath.Join(dir, "gh"), []byte(gh), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_CLOUD_KUBECTL", tool)
	t.Setenv("CI_TEST_STATE", stateFile)
	t.Setenv("CI_RESPONSE_FILE", responseFile)
	t.Setenv("GH_TOKEN", "fixture-only-token")
	t.Setenv("GITHUB_REPOSITORY", repo)
	t.Setenv("GITHUB_RUN_ID", "101")
	t.Setenv("GITHUB_RUN_ATTEMPT", "1")
	if err = runCIRollout([]string{"--environment", "dev", "--workspace", workspace, "--repository", repo}); err != nil {
		t.Fatal("manual executor:", err)
	}
	final, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	var observed struct {
		Deployment deploymentplan.Object `json:"deployment"`
		ConfigMap  deploymentplan.Object `json:"configmap"`
		Lease      any                   `json:"lease"`
	}
	if json.Unmarshal(final, &observed) != nil || environmentReleaseSlotImage(observed.Deployment, "frontend") != next || observed.Lease != nil || !strings.Contains(string(final), "original-pvc") || !strings.Contains(deploymentplan.String(deploymentplan.Map(observed.ConfigMap["data"])["state.json"]), next) {
		t.Fatal("published image, durable state, or lease was not handled correctly")
	}
}
