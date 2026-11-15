package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCIRolloutSchemaStateChecksLiveBaselineAndMigrationChanges(t *testing.T) {
	kube := frozenKubernetesFixture(t)
	raw, _ := yaml.Marshal(kube)
	backend, err := newEnvironmentKubernetesBackend(string(raw), "dev")
	if err != nil {
		t.Fatal(err)
	}
	repo := "hkt999rtk/rtk_billing"
	imageRepo := "ghcr.io/" + repo + "/billing-twd"
	oldRev, newRev := strings.Repeat("a", 40), strings.Repeat("b", 40)
	old := environmentReleaseImage{Reference: imageRepo + "@sha256:" + strings.Repeat("d", 64), SourceRevision: oldRev}
	next := environmentReleaseImage{Reference: imageRepo + "@sha256:" + strings.Repeat("e", 64), SourceRevision: newRev}
	workload := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"namespace": "video-cloud-dev-billing", "name": "billing", "uid": "original-uid"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "billing", "image": old.Reference}}}}}}
	plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev"}, Resources: []deploymentplan.Resource{{UID: "original-uid", Desired: workload}}}
	version, _ := json.Marshal(ciRolloutState{Environment: "dev", Repository: repo, Branch: "main", Images: map[string]environmentReleaseImage{imageRepo: old}})
	cm := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"namespace": "video-cloud-dev-video-cloud", "name": "rtk-ci-state-rtk-billing"}, "data": map[string]any{"state.json": string(version)}}
	dir := t.TempDir()
	write := func(name string, v any) {
		b, _ := json.Marshal(v)
		if e := os.WriteFile(filepath.Join(dir, name), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	write("state.json", cm)
	write("workload.json", workload)
	script := `#!/bin/sh
case "$*" in
 *'get ConfigMap '*) cat "$CI_SCHEMA_DIR/state.json" ;;
 *'get Deployment '*) cat "$CI_SCHEMA_DIR/workload.json" ;;
 *) exit 1 ;;
esac
`
	tool := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(tool, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", tool)
	t.Setenv("CI_SCHEMA_DIR", dir)
	changed := false
	read := func(_ context.Context, command string, args ...string) ([]byte, error) {
		if command != "gh" || len(args) != 2 {
			return nil, errors.New("unexpected query")
		}
		ref := args[1][strings.LastIndex(args[1], "/")+1:]
		switch ref {
		case oldRev:
			return []byte(`{"tree":[{"path":"migrations","type":"tree","sha":"1111111111111111111111111111111111111111"}]}`), nil
		case newRev:
			return []byte(`{"tree":[{"path":"migrations","type":"tree","sha":"2222222222222222222222222222222222222222"}]}`), nil
		case strings.Repeat("1", 40):
			return []byte(`{"tree":[{"path":"001.sql","type":"blob","sha":"3333333333333333333333333333333333333333"}]}`), nil
		case strings.Repeat("2", 40):
			blob := strings.Repeat("3", 40)
			if changed {
				blob = strings.Repeat("4", 40)
			}
			return []byte(fmt.Sprintf(`{"tree":[{"path":"001.sql","type":"blob","sha":%q}]}`, blob)), nil
		default:
			return nil, errors.New("unknown tree")
		}
	}
	images := map[string]environmentReleaseImage{imageRepo: next}
	if err := checkCIRolloutSchemaState(context.Background(), backend, plan, repo, images, read); err != nil {
		t.Fatal("unchanged migrations:", err)
	}
	changed = true
	if err := checkCIRolloutSchemaState(context.Background(), backend, plan, repo, images, read); !errors.Is(err, errCIRolloutMigrationChanged) {
		t.Fatal("changed migrations were not stopped:", err)
	}
	deploymentplan.Map(workload["metadata"])["uid"] = "replacement-uid"
	write("workload.json", workload)
	if err := checkCIRolloutSchemaState(context.Background(), backend, plan, repo, images, read); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatal("replaced workload accepted:", err)
	}
}

func TestCIRolloutMigrationFingerprintRejectsIncompleteEvidence(t *testing.T) {
	repo := "hkt999rtk/rtk_billing"
	revision := strings.Repeat("a", 40)
	for _, response := range []string{`{"truncated":true,"tree":[]}`, `{"tree":[]}`, `{"tree":[{"path":"migrations","type":"blob","sha":"1111111111111111111111111111111111111111"}]}`} {
		read := func(context.Context, string, ...string) ([]byte, error) { return []byte(response), nil }
		if _, err := ciRolloutMigrationFingerprint(context.Background(), repo, revision, read); err == nil {
			t.Fatalf("accepted incomplete migration tree %s", response)
		}
	}
}
