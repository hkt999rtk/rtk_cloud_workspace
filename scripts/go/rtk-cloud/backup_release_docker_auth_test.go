package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPostgresBackupReleaseUsesJobIsolatedDockerCredentials(t *testing.T) {
	root, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".github/workflows/postgres-backup-image.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Uses string `yaml:"uses"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err = yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	isolate, buildx := -1, -1
	for i, step := range workflow.Jobs["runner"].Steps {
		if step.Name == "Isolate Docker credentials" {
			isolate = i
			for _, value := range []string{"$RUNNER_TEMP", "$GITHUB_RUN_ID", "$GITHUB_RUN_ATTEMPT", "install -d -m 700", "DOCKER_CONFIG=", "$GITHUB_ENV"} {
				if !strings.Contains(step.Run, value) {
					t.Errorf("isolated Docker config missing %s", value)
				}
			}
		}
		if strings.HasPrefix(step.Uses, "docker/setup-buildx-action@") {
			buildx = i
		}
	}
	if isolate < 0 || buildx < 0 || isolate >= buildx {
		t.Fatal("Docker credentials must be isolated before Buildx setup")
	}
}
