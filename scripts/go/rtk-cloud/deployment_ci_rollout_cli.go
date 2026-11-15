package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func runCIRollout(args []string) error {
	fs := flag.NewFlagSet("ci-rollout", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	environment := fs.String("environment", "", "selected environment")
	repository := fs.String("repository", "", "selected service repository")
	workspace := fs.String("workspace", "", "workspace root")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *environment != "dev" || !ciRepositoryName.MatchString(*repository) {
		return errors.New("CI rollout: --environment dev and --repository owner/name are required")
	}
	if caller := os.Getenv("GITHUB_REPOSITORY"); caller != "" && caller != *repository {
		return errors.New("CI rollout: repository differs from CI caller")
	}
	if *workspace == "" {
		root, err := workspaceRoot()
		if err != nil {
			return err
		}
		*workspace = root
	}
	cfg, rolloutTimeout, totalTimeout, err := resolveCIRolloutSettings(*workspace, *environment)
	if err != nil {
		return err
	}
	policy, err := readCIBranchSelection(cfg)
	if err != nil {
		return err
	}
	branch := policy[*repository]
	if branch == "" {
		return errors.New("CI rollout: repository is not selected by the dev environment")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, totalTimeout)
	defer cancel()
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	targetPath, err := privateDeploymentStatePath(cfg.Environment, filepath.Join("deployment", "ci-rollout", "targets.json"))
	if err != nil {
		return err
	}
	raw, err := readBoundedEnvironmentFile(targetPath, true)
	if err != nil {
		return errors.New("CI rollout: restricted environment target inventory is unavailable")
	}
	var plan deploymentplan.Plan
	if decodeEnvironmentJSON(raw, &plan) != nil || plan.Environment != cfg.Environment || plan.Stack != cfg.Values["CLOUD_STACK_NAME"] {
		return errors.New("CI rollout: target inventory does not match selected dev environment")
	}
	imageRepositories, err := ciRolloutImageRepositories(&plan, *repository)
	if err != nil {
		return err
	}
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
		if err != nil {
			return errors.New("CI rollout: GitHub read authentication is unavailable")
		}
		token = strings.TrimSpace(string(out))
	}
	if token == "" {
		return errors.New("CI rollout: GitHub read authentication is unavailable")
	}
	run := func(ctx context.Context, command string, args ...string) ([]byte, error) {
		if command != "gh" {
			return nil, errors.New("CI rollout: unsupported provider command")
		}
		return runDeploymentCIRead(ctx, args, token, 20*time.Second, time.Second)
	}
	fmt.Printf("[RUNNING] Resolve %s branch %s CI publication\n", *repository, branch)
	images := map[string]environmentReleaseImage{}
	for _, imageRepository := range imageRepositories {
		image, err := resolveCIRolloutImage(ctx, policy, *repository, imageRepository, run)
		if errors.Is(err, errCIRolloutNotReady) {
			return errors.New("CI rollout: selected branch CI or image publication is not complete; rerun the manual rollout after it succeeds")
		}
		if err != nil {
			return err
		}
		images[imageRepository] = image
	}
	if _, err = ciRolloutBatchSteps(&plan, policy, *repository, branch, images); err != nil {
		return err
	}
	kubePath, err := privateDeploymentStatePath(cfg.Environment, filepath.Join("kube", "kubeconfig.yaml"))
	if err != nil {
		return err
	}
	kube, err := readBoundedEnvironmentFile(kubePath, true)
	if err != nil {
		return errors.New("CI rollout: restricted Kubernetes identity is unavailable")
	}
	backend, err := newEnvironmentKubernetesBackend(string(kube), cfg.Environment)
	if err != nil {
		return err
	}
	backend.RolloutTimeout = rolloutTimeout
	if err = checkCIRolloutSchemaState(ctx, backend, &plan, *repository, images, run); err != nil {
		return err
	}
	nonce := make([]byte, 12)
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	invocation := "run-" + hex.EncodeToString(nonce)
	journal, err := newCIRolloutBatchJournal(store, *repository, branch, images, invocation)
	if err != nil {
		return err
	}
	fmt.Println("[RUNNING] Wait for deployment ownership, update selected workloads and check readiness")
	hooks := ciRolloutNativeBatchHooks(backend, &plan, journal, run, totalTimeout)
	if err = executeCIRolloutBatch(ctx, &plan, policy, *repository, branch, images, hooks); err != nil {
		return err
	}
	fmt.Printf("[PASS] dev rollout %s branch %s; journal %s\n", *repository, branch, filepath.Join(store.Root, journal.relative))
	return nil
}
