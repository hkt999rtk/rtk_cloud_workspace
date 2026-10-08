package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReleaseSharedPullCancellationAndRetry(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"cancelled", "failed-pull-retry"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				revision := strings.Repeat("a", 40)
				reference := "docker.io/valkey/valkey@sha256:" + strings.Repeat("b", 64)
				cfg := deploymentConfig{Workspace: "/frozen", Environment: environment}
				release := environmentRelease{Environment: environment, WorkspaceRevision: revision, Images: map[string]environmentReleaseImage{
					"LKE_REDIS_IMAGE":        {Reference: reference},
					"LKE_FLEET_VALKEY_IMAGE": {Reference: reference},
				}}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				pulls := 0
				run := func(ctx context.Context, command string, args ...string) ([]byte, error) {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					if command == "git" {
						switch args[2] {
						case "rev-parse":
							return []byte(revision + "\n"), nil
						case "status":
							return nil, nil
						}
					}
					if command != "docker" || len(args) != 4 || args[0] != "pull" || args[1] != "--platform" || args[2] != "linux/amd64" || args[3] != reference {
						t.Fatal("unexpected shared-image verification command", command, args)
					}
					pulls++
					if mode == "cancelled" {
						cancel()
						return nil, nil
					}
					if pulls == 1 {
						return nil, errors.New("private-pull-output")
					}
					return nil, nil
				}
				err := verifyEnvironmentReleaseWithCommand(ctx, cfg, release, run)
				if mode == "cancelled" {
					if !errors.Is(err, context.Canceled) || pulls != 1 {
						t.Fatal("cancelled verification succeeded from a cached pull", err, pulls)
					}
					return
				}
				if err == nil || pulls != 1 {
					t.Fatal("failed first pull passed release verification", err, pulls)
				}
				if err := verifyEnvironmentReleaseWithCommand(ctx, cfg, release, run); err != nil || pulls != 2 {
					t.Fatal("new invocation did not retry the failed pull exactly once", err, pulls)
				}
			})
		}
	}
}
