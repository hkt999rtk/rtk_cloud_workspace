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
		fmt.Fprint(out, `Pre-deploy deployability check (pre-deploy)
  Validate prerequisites before deployment.
  For existing Pod image updates, use --operation image-upgrade.

Operation
  - Default: complete upgrade of an existing Kubernetes environment.
  - --operation create detects fresh creation or an unfinished original
    dev checkpoint and qualifies the appropriate prerequisites.
  - Original-checkpoint qualification preserves the execution fence and
    OpenBao/PKI identities; it never starts deployment.
  - Original recovery inputs are resolved from journal-bound evidence in
    the selected canonical store; no extra recovery operation is needed.
  - Fresh dev/staging create qualifies provider allocation and native
    bootstrap without requiring an existing kubeconfig.

Validation and results
  - Runs complete validation; every required gate must pass.
  - Missing capability or evidence means NO-GO.
  - Builds the exact plan from settings, live identity references and
    formal CI image-manifest.json artifacts.
  - Saves checked bindings and evidence with an expiry time.
  - Deployment verifies and reuses valid evidence, runs live checks,
    then performs post-deploy acceptance.

Provider access
  - Checks the configured media bucket; may create it if missing and
    no storage cutover is required.
  - Other provider probes are read-only.
  - No data migration or credential replacement.

Required inputs
  - Existing Kubernetes upgrades use the selected kubeconfig.
  - Fresh create uses selected operator credentials, verified target
    absence and formal CI images; deployment creates new runtime state.
  - No pre-existing bindings or local release file is required.
  - Existing upgrades need no local Linode cluster ID or account quota state.
  - Private schema-1 plans are retired.

`)
	case deploymentCheckPostDeploy:
		fmt.Fprint(out, `Post-deploy environment health check (post-deploy)
  Run after deployment or to diagnose the current environment.

Checks
  - Deployed Secret bindings and PKI.
  - Required public mTLS endpoints.
  - Environment workload readiness.
  - Selected provider and input checks.

Scope
  - Reports the current environment's health.
  - Use check-deployment-preflight.sh before deploying a new target.
  - Full application acceptance requires separate application tests.

Execution mode
  - Default provider validation may use temporary DNS or storage writes.
  - Use --read-only for read-only checks.
  - --fast reduces validation depth.

`)
	default:
		fmt.Fprintln(out, "Deployment environment check: Use --phase pre-deploy for deployability, or --phase post-deploy for current environment health (default).")
	}
}

func deploymentCheckScope(phase string) string {
	if phase == deploymentCheckPreDeploy {
		return "full create/upgrade deployability with configured media bucket creation when missing: desired configuration, provision prerequisites, local SecretStore, safe route migration and selected provider/input checks; current runtime health and write permissions unverified; not release approval"
	}
	return "existing deployment health: live SecretStore bindings, PKI, required public CertIssuer mTLS, all environment workload readiness and selected provider/input checks; not full application acceptance or release approval"
}

func executeDeploymentPreDeployCheck(ctx context.Context, o deploymentCheckOptions, cfg deploymentConfig, deps deploymentCheckDependencies, reporter *deploymentCheckReporter) {
	store, storeErr := deps.store(cfg.Environment)
	store.checkRuntime = newDeploymentCheckRuntime(ctx, cfg.Environment)
	q := o.qualification
	q.readOnly = true
	q.envFile = defaultDeploymentEnvironmentCredentialFile(cfg.Environment)
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
			if deps.preflight == nil {
				return errors.New("deployment preflight is unavailable")
			}
			return deps.preflight(ctx, cfg, reporter.out)
		}},
	}
	if deps.bucket != nil && cfg.Adapter == "lke" && cfg.Storage.RuntimeMedia.Bucket != "" && !cfg.Storage.RuntimeMediaCutoverRequired {
		tasks = append(tasks, struct {
			id  string
			run func() error
		}{
			id:  "provider.storage.media.bucket",
			run: func() error { return deps.bucket(ctx, cfg, reporter.out) },
		})
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
