package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Explicitly opt in: this reads current GitHub data but never writes cloud state.
func TestCIRolloutResolveSelectedDevBranchesLive(t *testing.T) {
	if os.Getenv("RTK_CI_ROLLOUT_LIVE") != "1" {
		t.Skip("live GitHub verification is opt-in")
	}
	workspace, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := readCIBranchSelection(deploymentConfig{Environment: "dev", EnvironmentRoot: filepath.Join(workspace, "cloud_env", "dev")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	run := func(ctx context.Context, command string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, command, args...).Output()
	}
	for _, source := range lkeServiceImageSources() {
		source := source
		t.Run(source.RepoName, func(t *testing.T) {
			repo := "hkt999rtk/" + source.RepoName
			image, err := resolveCIRolloutImage(ctx, policy, repo, "ghcr.io/"+repo+"/"+source.Name, run)
			if err != nil {
				t.Fatalf("selected branch CI publication unavailable: %v", err)
			}
			if image.Publication == nil || image.Reference == "" {
				t.Fatal("missing immutable publication")
			}
			t.Logf("branch=%s revision=%s publication=%d", policy[repo], image.SourceRevision[:12], image.Publication.RunID)
		})
	}
}
