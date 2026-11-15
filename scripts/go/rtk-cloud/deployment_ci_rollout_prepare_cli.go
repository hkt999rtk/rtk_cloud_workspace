package main

import (
	"context"
	"encoding/json"
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

// Prepare the dedicated identities from one current environment plan. Cloud
// mutations happen only after the complete target inventory is assembled.
func runCIRolloutPrepare(args []string) (result error) {
	fs := flag.NewFlagSet("ci-rollout-prepare", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	environment := fs.String("environment", "", "selected environment")
	workspace := fs.String("workspace", "", "workspace root")
	apply := fs.Bool("apply", false, "configure restricted identities")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *environment != "dev" {
		return errors.New("CI rollout prepare: --environment dev is required")
	}
	if *workspace == "" {
		root, err := workspaceRoot()
		if err != nil {
			return err
		}
		*workspace = root
	}
	_, _, total, err := resolveCIRolloutSettings(*workspace, *environment)
	if err != nil {
		return err
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, "")
	if err != nil {
		return err
	}
	policy, err := readCIBranchSelection(cfg)
	if err != nil {
		return err
	}
	if len(policy) == 0 {
		return errors.New("CI rollout prepare: no repositories are selected in dev environment settings")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, total)
	defer cancel()
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	report, err := newCIRolloutProvisionReport(store)
	if err != nil {
		return err
	}
	fmt.Printf("[RUNNING] Provisioning log %s\n", filepath.Join(store.Root, report.relative))
	defer func() {
		if result != nil {
			report.Status = "failed"
			report.Error = result.Error()
		} else if !*apply {
			report.Status = "planned"
		} else {
			report.Status = "completed"
		}
		if err := report.save(true); err != nil {
			result = errors.Join(result, errors.New("CI rollout prepare: provisioning log could not be saved"))
		}
	}()
	if err = report.step("compile", ""); err != nil {
		return err
	}
	if err = checkLocalExecutionFence(store.Root); err != nil {
		return err
	}
	fmt.Println("[RUNNING] Read existing dev workload targets")
	kubePath, err := privateDeploymentStatePath(cfg.Environment, filepath.Join("kube", "kubeconfig.yaml"))
	if err != nil {
		return err
	}
	kube, err := readBoundedEnvironmentFile(kubePath, true)
	if err != nil {
		return errors.New("CI rollout prepare: selected dev Kubernetes identity is unavailable")
	}
	backend, err := newEnvironmentKubernetesBackendForConfig(string(kube), cfg)
	if err != nil {
		return err
	}
	stack := cfg.Values["CLOUD_STACK_NAME"]
	if !ciRolloutKubernetesName.MatchString(stack) {
		return errors.New("CI rollout prepare: invalid configured stack")
	}
	rawInventory, err := backend.command(ctx, nil, "get", "deployments", "--all-namespaces", "-l", "rtk.realtek.com/stack="+stack, "-o", "json")
	if err != nil {
		return errors.New("CI rollout prepare: existing dev Deployments cannot be read")
	}
	inventory, err := ciRolloutDecodeDeploymentInventory(rawInventory)
	if err != nil {
		return err
	}
	compiled := &compiledEnvironmentDeployment{Backend: backend, Plan: &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: cfg.Environment, Stack: stack, Operation: "scoped"}}}
	plans := map[string]*deploymentplan.Plan{}
	objects := map[string][]deploymentplan.Object{}
	for _, repo := range ciRolloutSortedRepositories(policy) {
		target, err := ciRolloutPlanFromInventory(cfg, policy, repo, inventory)
		if err != nil {
			return fmt.Errorf("%s: %w", repo, err)
		}
		targets, err := ciRolloutTargetsForPlan(target, repo)
		if err != nil {
			return err
		}
		rbac, err := ciRolloutRBAC(target, repo, targets)
		if err != nil {
			return err
		}
		plans[repo], objects[repo] = target, rbac
		fmt.Printf("[PASS] %s: %d existing workload targets\n", repo, len(targets))
	}
	if !*apply {
		fmt.Println("[PASS] Restricted identity plan prepared; use --apply to configure it")
		return nil
	}
	tokenBytes, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		return errors.New("CI rollout prepare: GitHub authentication is unavailable")
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return errors.New("CI rollout prepare: GitHub authentication is unavailable")
	}
	run := func(ctx context.Context, command string, args ...string) ([]byte, error) {
		if command != "gh" {
			return nil, errors.New("CI rollout prepare: unsupported provider command")
		}
		return runDeploymentCIRead(ctx, args, token, 20*time.Second, time.Second)
	}
	leaseID := "Lease/" + compiled.Plan.Stack + "-video-cloud/rtk-deployment-execution"
	release, err := waitForCIRolloutLease(ctx, 2*time.Second, func(ctx context.Context) (bool, error) {
		object, err := compiled.Backend.Read(ctx, leaseID)
		return object != nil, err
	}, func(ctx context.Context) (func() error, error) {
		lease, err := compiled.Backend.AcquireExecution(ctx, compiled.Plan)
		if err != nil {
			return nil, err
		}
		return func() error {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return lease.Release(cleanup)
		}, nil
	})
	if err != nil {
		return err
	}
	held := true
	defer func() {
		if held {
			_ = release()
		}
	}()
	prepared := []ciRolloutPreparedIdentity{}
	for _, repo := range ciRolloutSortedRepositories(policy) {
		if err = report.step("configure-identity", repo); err != nil {
			return err
		}
		fmt.Printf("[RUNNING] Configure restricted identity for %s\n", repo)
		target := plans[repo]
		rbac := objects[repo]
		account := deploymentplan.String(deploymentplan.Map(rbac[0]["metadata"])["name"])
		namespace := target.Stack + "-video-cloud"
		for _, object := range rbac {
			if err = ciRolloutEnsureManagedObject(ctx, compiled.Backend, repo, object); err != nil {
				return fmt.Errorf("%s: %w", repo, err)
			}
		}
		accountObject, err := compiled.Backend.Read(ctx, "ServiceAccount/"+namespace+"/"+account)
		if err != nil || accountObject == nil {
			return errors.New("CI rollout prepare: restricted ServiceAccount cannot be read")
		}
		accountUID := deploymentplan.String(deploymentplan.Map(accountObject["metadata"])["uid"])
		if accountUID == "" {
			return errors.New("CI rollout prepare: restricted ServiceAccount UID is unavailable")
		}
		stateName, _ := ciRolloutStateName(repo)
		existingState, err := compiled.Backend.Read(ctx, "ConfigMap/"+namespace+"/"+stateName)
		if err != nil {
			return err
		}
		var observed map[string]environmentReleaseImage
		if existingState == nil || deploymentplan.String(deploymentplan.Map(existingState["data"])["state.json"]) == "" {
			observed, err = ciRolloutObserveBaseline(ctx, compiled.Backend, target, policy, repo, run)
			if err != nil {
				return fmt.Errorf("%s: %w", repo, err)
			}
			if len(observed) == 0 && (repo == "hkt999rtk/rtk_account_manager" || repo == "hkt999rtk/rtk_billing") {
				return fmt.Errorf("%s: current schema source cannot be established from the selected CI image and healthy workloads", repo)
			}
		}
		if err = ciRolloutEnsureState(ctx, compiled.Backend, target, repo, policy[repo], observed); err != nil {
			return err
		}
		restrictedToken, secretUID, err := ciRolloutToken(ctx, compiled.Backend, namespace, account, repo)
		if err != nil {
			return err
		}
		kube, err := ciRolloutKubeconfig(string(compiled.Backend.KubeconfigContent), "dev", restrictedToken)
		if err != nil {
			return err
		}
		restrictedBackend, err := newEnvironmentKubernetesBackend(string(kube), "dev")
		if err != nil {
			return err
		}
		targets, err := ciRolloutTargetsForPlan(target, repo)
		if err != nil {
			return err
		}
		if err = ciRolloutVerifyAccess(ctx, restrictedBackend, namespace, repo, targets); err != nil {
			return fmt.Errorf("%s: %w", repo, err)
		}
		rawTargets, err := json.Marshal(target)
		if err != nil {
			return err
		}
		binding := ciRolloutIdentityBinding{Environment: "dev", Repository: repo, Namespace: namespace, ServiceAccount: account, ServiceAccountUID: accountUID, TokenSecret: account + "-token", TokenSecretUID: secretUID}
		if _, err = ciRolloutSaveBinding(store, binding, kube, rawTargets); err != nil {
			return err
		}
		prepared = append(prepared, ciRolloutPreparedIdentity{Repository: repo, Kubeconfig: kube, Targets: rawTargets})
	}
	if err = release(); err != nil {
		return err
	}
	held = false
	for _, item := range prepared {
		if err = report.step("store-github-identity", item.Repository); err != nil {
			return err
		}
		fmt.Printf("[RUNNING] Store restricted dev CI identity for %s\n", item.Repository)
		if err = ciRolloutGitHubEnvironment(ctx, item.Repository); err != nil {
			return err
		}
		if err = ciRolloutGitHubSecret(ctx, item.Repository, "RTK_DEV_ROLLOUT_KUBECONFIG", item.Kubeconfig); err != nil {
			return err
		}
		if err = ciRolloutGitHubSecret(ctx, item.Repository, "RTK_DEV_ROLLOUT_TARGETS", item.Targets); err != nil {
			return err
		}
		fmt.Printf("[PASS] Restricted dev identity configured for %s\n", item.Repository)
	}
	fmt.Println("[PASS] All selected dev CI identities configured")
	return nil
}
