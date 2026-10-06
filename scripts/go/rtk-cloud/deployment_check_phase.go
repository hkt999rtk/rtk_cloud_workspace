package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	deploymentCheckPreDeploy  = "pre-deploy"
	deploymentCheckPostDeploy = "post-deploy"
)

// This scan only selects the opening description. The flag parser below is
// still authoritative and rejects invalid or conflicting phase arguments.
func deploymentCheckBannerPhase(args []string) string {
	phase := deploymentCheckPostDeploy
	selected := ""
	for i := 0; i < len(args); i++ {
		value := ""
		if args[i] == "--phase" || args[i] == "-phase" {
			i++
			if i == len(args) {
				return ""
			}
			value = args[i]
		} else if strings.HasPrefix(args[i], "--phase=") || strings.HasPrefix(args[i], "-phase=") {
			value = strings.SplitN(args[i], "=", 2)[1]
		} else {
			continue
		}
		if value != deploymentCheckPreDeploy && value != deploymentCheckPostDeploy {
			return ""
		}
		if selected != "" && selected != value {
			return ""
		}
		selected, phase = value, value
	}
	return phase
}

func printDeploymentCheckPurpose(out io.Writer, phase string) {
	switch phase {
	case deploymentCheckPreDeploy:
		fmt.Fprintln(out, "Pre-deploy deployability check (pre-deploy): Validate prerequisites for the selected deployment operation. Use --operation image-upgrade to qualify existing Pod image updates. Run before deployment.")
		fmt.Fprintln(out, "No cloud resources are persisted by this check. Default full-deployment checks desired inputs and safe route migration; a private managed upgrade plan additionally validates Kubernetes update admission with server dry-run. Image-upgrade additionally requires live health, exact CI images and local startup fixtures; --fast cannot qualify it.")
	case deploymentCheckPostDeploy:
		fmt.Fprintln(out, "Post-deploy environment health check (post-deploy): Verify deployed Secret bindings, PKI, required public mTLS and environment workload readiness. Run after deployment or when diagnosing the current environment.")
		fmt.Fprintln(out, "This checks current health, not pre-deploy deployability or full application acceptance. Default provider validation may use temporary DNS or storage writes. Use --read-only for read-only checks; --fast only reduces validation depth.")
	default:
		fmt.Fprintln(out, "Deployment environment check: Use --phase pre-deploy for deployability, or --phase post-deploy for current environment health (default).")
	}
}

func deploymentCheckScope(phase string) string {
	if phase == deploymentCheckPreDeploy {
		return "read-only full create/upgrade deployability: desired configuration, provision prerequisites, local SecretStore, safe route migration and selected provider/input checks; current runtime health and write permissions unverified; not release approval"
	}
	return "existing deployment health: live SecretStore bindings, PKI, required public CertIssuer mTLS, all environment workload readiness and selected provider/input checks; not full application acceptance or release approval"
}

func executeDeploymentPreDeployCheck(ctx context.Context, o deploymentCheckOptions, cfg deploymentConfig, deps deploymentCheckDependencies, reporter *deploymentCheckReporter) {
	store, storeErr := deps.store(cfg.Environment)
	store.checkRuntime = newDeploymentCheckRuntime(ctx, cfg.Environment)
	q := o.qualification
	q.readOnly = true
	q.envFile = defaultDeploymentEnvironmentCredentialFile(cfg.Environment)
	var managedErr error
	var managedPlan *managedUpgradePlan
	if storeErr == nil {
		managedPlan, managedErr = loadManagedUpgradePlan(store, cfg.Values["CLOUD_STACK_NAME"])
		if managedPlan != nil {
			reporter.scope = "full managed workload replacement: every environment Deployment desired spec, retained StatefulSet/DaemonSet dependencies, exact CI/digest image pulls, schemas, startup, identity/CRL/Secret bindings, Kubernetes server dry-run and public mTLS; provider resizing, feature activation and CA lifecycle changes require separate plans; plan_sha256=" + managedPlan.digest
			if q.fast {
				managedErr = errors.New("--fast cannot qualify a full managed workload overwrite; run the default preflight without --fast")
			}
			if q.selected != nil && !q.selected["ghcr"] {
				managedErr = errors.New("managed full upgrade requires ghcr image qualification")
			}
			q.images = uniqueNonEmpty(append(q.images, managedPlan.Images...)...)
		}
	}
	tasks := []struct {
		id  string
		run func() error
	}{
		{"secrets.local", func() error {
			if storeErr != nil {
				return errors.New("selected environment SecretStore cannot be resolved")
			}
			return deps.local(store)
		}},
		{"secrets.legacy-paths", func() error { return verifyDeploymentCheckLegacyPaths(store, cfg.Workspace) }},
		{"deployment.preflight", func() error {
			if err := validateDeploymentStorageActivation(cfg); err != nil {
				return err
			}
			if managedErr != nil {
				return managedErr
			}
			if deps.preflight == nil {
				return errors.New("deployment preflight is unavailable")
			}
			return deps.preflight(ctx, cfg, reporter.out)
		}},
	}
	for _, t := range tasks {
		reporter.emit(deploymentCredentialCheck{ID: t.id, Status: "PENDING", Required: true, Resource: cfg.Environment})
	}
	if deps.plan != nil {
		deps.plan(ctx, cfg, q.envFile, q, reporter.emit)
	}
	for _, t := range tasks {
		if err := ctx.Err(); err != nil {
			reporter.emit(deploymentCheckFailure(t.id, err))
			continue
		}
		reporter.emit(deploymentCredentialCheck{ID: t.id, Status: "RUNNING"})
		if err := t.run(); err != nil {
			reporter.emit(deploymentCheckFailure(t.id, err))
		} else {
			reporter.emit(deploymentCredentialCheck{ID: t.id, Status: "PASS", Passed: true, Required: true, Detail: "verified", Attempts: 1})
		}
	}
	// Local desired-input checks and provider read probes remain independent.
	// Never perform credential repairs, write canaries or write receipts here.
	deps.collect(ctx, cfg, q.envFile, q, false, reporter.emit)
}
