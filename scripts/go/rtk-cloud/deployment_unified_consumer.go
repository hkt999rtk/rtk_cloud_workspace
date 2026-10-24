package main

import (
	"context"
	"errors"
	"io"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func loadQualifiedEnvironmentDeployment(ctx context.Context, cfg deploymentConfig, operation, sha string) (*compiledEnvironmentDeployment, error) {
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return nil, err
	}
	artifact, err := loadEnvironmentPreflightProof(cfg, store, operation, sha, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return hydrateQualifiedEnvironmentDeployment(ctx, cfg, store, artifact)
}

// Read only the current canonical inputs and frozen artifacts. Loading a GO
// never invokes renderers, CI selection, image pulls or candidate startup.
func hydrateQualifiedEnvironmentDeployment(ctx context.Context, cfg deploymentConfig, store secretStore, artifact *environmentPreflightArtifact) (*compiledEnvironmentDeployment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if artifact == nil || artifact.Plan == nil || artifact.Plan.Environment != cfg.Environment || artifact.Plan.Stack != cfg.Values["CLOUD_STACK_NAME"] || store.Environment != cfg.Environment {
		return nil, &deploymentplan.Blocker{ID: "deployment.preflight.proof", Cause: "complete selected-environment qualified artifacts are unavailable", Next: "run full preflight and use its exact reviewed plan SHA-256"}
	}
	snapshot, err := readEnvironmentDeploymentSnapshot(cfg, store)
	if err != nil || snapshot.Digest(cfg) != artifact.Plan.ConfigurationSHA256 {
		return nil, &deploymentplan.Blocker{ID: "deployment.inputs.changed", Cause: "maintained configuration or original canonical credentials changed after preflight", Next: "run full preflight against the current selected environment and review its new plan SHA-256"}
	}
	backend, err := newEnvironmentKubernetesBackend(snapshot.Kubeconfig, cfg.Environment)
	if err != nil {
		return nil, environmentCompilationError(err, "deployment.access.kubeconfig", "selected Kubernetes identity could not be initialized", "restore the selected environment's embedded Kubernetes identity and repeat preflight")
	}
	cluster, identity, err := environmentPreflightKubernetesIdentity(backend.KubeconfigContent)
	if err != nil || cluster != artifact.Proof.ClusterSHA256 || identity != artifact.Proof.KubernetesIdentitySHA256 {
		return nil, &deploymentplan.Blocker{ID: "deployment.preflight.identity", Cause: "selected Kubernetes cluster or authentication identity differs from qualified preflight", Next: "run full preflight with the selected canonical kubeconfig and review its exact new plan SHA-256"}
	}
	backend.SchemaWorkspace = cfg.Workspace
	backend.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return nil, err
	}
	c := &compiledEnvironmentDeployment{Plan: artifact.Plan, Release: artifact.Release, Bindings: artifact.Bindings, Store: store, Backend: backend, Configuration: cfg, Proof: &artifact.Proof, PreflightGeneration: artifact.Proof.Generation, SourceCommand: localEnvironmentReleaseSourceCommand}
	observed, err := c.FenceInventory(ctx, c.Plan)
	if err != nil {
		return nil, err
	}
	if err := checkQualifiedEnvironmentBaselines(ctx, c.Plan, observed, c.Backend.Read); err != nil {
		return nil, err
	}
	return c, nil
}

// Existing source fences require only local Git reads. Refuse any command that
// could silently turn consumption of frozen evidence into release resolution.
func localEnvironmentReleaseSourceCommand(ctx context.Context, command string, args ...string) ([]byte, error) {
	if command != "git" {
		return nil, errors.New("deployment.preflight.source: only local source identity verification is allowed")
	}
	return deploymentUpgradeCommand(ctx, command, args...)
}

// Initial consumption requires every original baseline, including retained
// Secrets and configuration. Later phase fences intentionally use current
// owners because earlier approved phases have already changed those baselines.
func checkQualifiedEnvironmentBaselines(ctx context.Context, plan *deploymentplan.Plan, observed map[string]deploymentplan.Object, read func(context.Context, string) (deploymentplan.Object, error)) error {
	for _, resource := range plan.Resources {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, found := observed[resource.ID]
		if !found {
			var err error
			current, err = read(ctx, resource.ID)
			if err != nil {
				return err
			}
		}
		if _, err := resource.Body(current); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// Immutable image publication, rendering and candidate startup evidence belong
// to preflight. Execution refreshes only prerequisites that can change after
// qualification; exact resource admission is performed once by
// Plan.Execute immediately after these checks.
func executionEnvironmentChecks(c *compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
	return map[string]deploymentplan.GateCheck{
		"deployment.target": func(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
			if err := c.Fence(ctx, p); err != nil {
				return deploymentplan.GateEvidence{}, err
			}
			return deploymentplan.GateEvidence{SHA256: p.SHA256()}, nil
		},
		"deployment.execution": func(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
			nextStep, abortSteps := environmentDeploymentSteps(ctx)
			defer abortSteps()
			if p.Operation != "upgrade" {
				nextStep("deployment.execution.provider-quota")
				if err := c.CheckProviderQuota(ctx, p); err != nil {
					return deploymentplan.GateEvidence{}, err
				}
			}
			nextStep("deployment.execution.node-capacity")
			if err := c.CheckNodeCapacity(ctx, p); err != nil {
				return deploymentplan.GateEvidence{}, err
			}
			nextStep("deployment.execution.surge-capacity")
			if err := c.CheckSurgeCapacity(ctx, p); err != nil {
				return deploymentplan.GateEvidence{}, err
			}
			nextStep("deployment.execution.storage")
			if err := c.Backend.CheckStorageBindings(ctx, p); err != nil {
				return deploymentplan.GateEvidence{}, err
			}
			if c.Configuration.Values["OTA_SERVICE_REGISTRATION_ENABLED"] == "true" || c.Configuration.Values["VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED"] == "true" {
				operator, err := c.Store.readOperator()
				if err != nil {
					return deploymentplan.GateEvidence{}, &deploymentplan.Blocker{ID: "deployment.execution.storage", Cause: "original canonical provider and storage credentials cannot be read", Next: "restore the selected environment's original storage credentials and repeat preflight"}
				}
				if c.Configuration.Values["OTA_SERVICE_REGISTRATION_ENABLED"] == "true" {
					nextStep("deployment.execution.ota-storage")
					if err := checkOTAStorageAccess(ctx, c.Configuration, c.Store, operator, defaultDeploymentCredentialChecker()); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
				}
				if c.Configuration.Values["VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED"] == "true" {
					nextStep("deployment.execution.media-storage")
					if err := checkEnvironmentVideoStorageStartup(ctx, p, c.Configuration, c.Store, c.Backend.Read, operator, defaultDeploymentCredentialChecker()); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					if err := checkQualifiedEnvironmentMediaStorageAccess(ctx, c.Configuration, operator, defaultDeploymentCredentialChecker()); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
				}
			}
			nextStep("deployment.execution.permissions")
			if err := c.Backend.CheckPlanAccess(ctx, p); err != nil {
				return deploymentplan.GateEvidence{}, err
			}
			nextStep("deployment.execution.lease")
			if err := c.Backend.CheckExecutionPrerequisites(ctx, p, c.Store.Root); err != nil {
				return deploymentplan.GateEvidence{}, err
			}
			nextStep("")
			return deploymentplan.GateEvidence{SHA256: p.SHA256()}, nil
		},
		"deployment.pki": func(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
			nextStep, abortSteps := environmentDeploymentSteps(ctx)
			defer abortSteps()
			nextStep("deployment.pki.before-fence")
			if err := c.Fence(ctx, p); err != nil {
				return deploymentplan.GateEvidence{}, err
			}
			nextStep("deployment.pki.identity-permissions")
			if err := verifyQualifiedControllerIdentityPermissions(ctx, c); err != nil {
				return deploymentplan.GateEvidence{}, err
			}
			window, err := environmentPKIValidityWindow(c.Configuration.Values)
			if err != nil {
				return deploymentplan.GateEvidence{}, err
			}
			store := c.Store
			store.checkRuntime = newFrozenDeploymentCheckRuntime(ctx, p.Environment, c.Backend)
			// Keep deployment readiness pending until the existing rollout
			// acceptance runs; current native trust/registry checks still run.
			store.checkRuntime.preflightPlan = p
			nextStep("deployment.pki.bindings")
			if err := verifySecretStoreK8SBindings(store); err != nil {
				return deploymentplan.GateEvidence{}, &deploymentplan.Blocker{ID: "deployment.pki.bindings", Cause: "original runtime Secret bindings cannot be verified", Next: "restore the selected environment's original synchronized credentials and bindings; never generate replacement identity to pass"}
			}
			now := time.Now().UTC()
			cutoff, err := environmentExecutionPKICutoff(ctx, now, window)
			if err != nil {
				return deploymentplan.GateEvidence{}, err
			}
			for index, at := range []time.Time{now, cutoff} {
				nextStep([]string{"deployment.pki.mqtt-now", "deployment.pki.mqtt-window"}[index])
				if err := c.CheckMQTTFoundationIdentity(ctx, at); err != nil {
					return deploymentplan.GateEvidence{}, err
				}
				nextStep([]string{"deployment.pki.runtime-now", "deployment.pki.runtime-window"}[index])
				if err := verifySecretStoreK8SRuntime(store, at); err != nil {
					return deploymentplan.GateEvidence{}, environmentRuntimePKIQualificationError(err)
				}
			}
			c.PKICheckedAt = now
			c.PKIVerifiedUntil = cutoff
			nextStep("deployment.pki.after-fence")
			if err := c.Fence(ctx, p); err != nil {
				return deploymentplan.GateEvidence{}, err
			}
			nextStep("")
			return deploymentplan.GateEvidence{SHA256: p.SHA256(), PendingRolloutHealth: store.checkRuntime.pendingRolloutHealth()}, nil
		},
		"deployment.compatibility": func(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
			if err := c.Fence(ctx, p); err != nil {
				return deploymentplan.GateEvidence{}, err
			}
			store := c.Store
			store.checkRuntime = newFrozenDeploymentCheckRuntime(ctx, p.Environment, c.Backend)
			if err := verifyEnvironmentReleaseSchemasForPlan(ctx, c.Configuration, store, c.Release, p, c.Backend.Read); err != nil {
				return deploymentplan.GateEvidence{}, environmentSchemaQualificationError(err)
			}
			for _, resource := range p.Resources {
				if resource.Action != "retire" && otaStartupContainer(resource.Desired) != nil {
					if err := checkEnvironmentOTASchema(ctx, p, c.Store, c.Backend.Read, func(ctx context.Context, args ...string) ([]byte, error) { return c.Backend.command(ctx, nil, args...) }); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					if err := checkEnvironmentOTARegistration(ctx, p, c.Release, c.Store, c.Backend.Read, func(ctx context.Context, args ...string) ([]byte, error) { return c.Backend.command(ctx, nil, args...) }); err != nil {
						return deploymentplan.GateEvidence{}, err
					}
					break
				}
			}
			if err := c.Fence(ctx, p); err != nil {
				return deploymentplan.GateEvidence{}, err
			}
			return deploymentplan.GateEvidence{SHA256: p.SHA256()}, nil
		},
		"deployment.network": func(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
			if err := c.Fence(ctx, p); err != nil {
				return deploymentplan.GateEvidence{}, err
			}
			store := c.Store
			store.checkRuntime = newFrozenDeploymentCheckRuntime(ctx, p.Environment, c.Backend)
			if err := verifyDeploymentPublicCertIssuer(ctx, c.Configuration, store); err != nil {
				return deploymentplan.GateEvidence{}, &deploymentplan.Blocker{ID: "deployment.network", Cause: "required public CertIssuer route or authenticated mTLS path is unavailable", Next: "repair the selected environment's owned DNS/ingress/Service and required mTLS path; verify with check-deployment-health.sh, then repeat preflight"}
			}
			if err := c.Fence(ctx, p); err != nil {
				return deploymentplan.GateEvidence{}, err
			}
			return deploymentplan.GateEvidence{SHA256: p.SHA256()}, nil
		},
	}
}

// The current credentials must cover the actual execution deadline and at
// least the maintained PKI validity margin. Evidence TTL is not a run limit.
func environmentExecutionPKICutoff(ctx context.Context, now time.Time, window int64) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	cutoff := now.Add(time.Duration(window) * time.Second)
	deadline, ok := ctx.Deadline()
	if !ok {
		return time.Time{}, &deploymentplan.Blocker{ID: "deployment.pki.validity-window", Cause: "current PKI validation requires the execution deadline", Next: "use a positive deployment --timeout and validate the original credentials through that deadline"}
	}
	if deadline.After(cutoff) {
		cutoff = deadline
	}
	return cutoff, nil
}

// Current provider grants and public ACLs are independent of plan/file hashes.
// Force the maintained validator's read-only branch: no bucket/key creation,
// write canary or receipt may be used while consuming qualification evidence.
func checkQualifiedEnvironmentMediaStorageAccess(ctx context.Context, cfg deploymentConfig, operator map[string]string, checker deploymentCredentialChecker) error {
	checker = checker.withCheckContext(ctx)
	checker.readOnly, checker.out, checker.session = true, io.Discard, nil
	if check := checker.checkResolvedObjectStorage(cfg, operator); !check.Passed {
		return videoStorageStartupBlocker("", "original selected media allocation, storage privacy or read access cannot be verified")
	}
	return nil
}

func verifyQualifiedControllerIdentityPermissions(ctx context.Context, c *compiledEnvironmentDeployment) error {
	return checkControllerIdentityPermissions(c.Plan, "", c.Release.Images["LKE_ALPINE_IMAGE"].Reference, func(args ...string) ([]byte, error) {
		if len(args) < 2 || args[0] != "--kubeconfig" || args[1] != "" {
			return nil, errors.New("deployment.pki.identity-permissions: frozen Kubernetes command binding is unavailable")
		}
		return c.Backend.command(ctx, nil, args[2:]...)
	})
}
