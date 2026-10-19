package main

import (
	"context"
	"errors"
	"io"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func createNormalTailProgramChecks(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, p *createNormalTailProgram, rendered *renderedEnvironmentTarget) (map[string]func(context.Context) error, error) {
	preview, err := createNormalTailProgramAdmissionPreview(cfg, p, rendered.Live)
	if err != nil {
		return nil, err
	}
	storage, err := rendered.Backend.storageBindings(ctx, preview)
	if err != nil {
		return nil, err
	}
	preview.StorageSHA256 = deploymentplan.Digest(storage)
	c := &compiledEnvironmentDeployment{Plan: preview, Configuration: cfg, Release: p.Release, Bindings: p.Bindings, Backend: rendered.Backend, Store: rendered.Store}
	checks := map[string]func(context.Context) error{
		"deployment.target": func(ctx context.Context) error {
			if err := createNormalTailProgramFence(ctx, cfg, h, p); err != nil {
				return err
			}
			live, err := c.Backend.Inventory(ctx, preview.Stack)
			if err != nil {
				return err
			}
			observed := make(map[string]deploymentplan.Object, len(live))
			for _, object := range live {
				observed[deploymentplan.ID(object)] = object
			}
			// Compare every baseline against a fresh complete inventory. Missing
			// entries still receive an exact GET to detect foreign create collisions.
			return checkQualifiedEnvironmentBaselines(ctx, preview, observed, c.Backend.Read)
		},
		"deployment.release": func(ctx context.Context) error {
			if err := verifyEnvironmentRelease(ctx, cfg, p.Release); err != nil {
				return err
			}
			return verifyCreateNormalTailProductCapabilities(ctx, p.Release, p.Products, cfg.Values["TEST_LAB_ENABLED"] == "true")
		},
		"deployment.execution": func(ctx context.Context) error {
			for _, check := range []func(context.Context, *deploymentplan.Plan) error{c.CheckProviderQuota, c.CheckNodeCapacity, c.CheckSurgeCapacity, c.Backend.CheckStorageBindings, c.Backend.CheckPlanAccess} {
				if err := check(ctx, preview); err != nil {
					return err
				}
			}
			if err := c.Backend.CheckExecutionPrerequisites(ctx, preview, c.Store.Root); err != nil {
				return err
			}
			return preview.Admit(ctx, c.Backend)
		},
		"deployment.pki": func(ctx context.Context) error {
			// The declared native producer proves future product identities.
			// Existing identities still undergo their complete native checks.
			if _, err := createNormalTailProductOutputContracts(cfg, p.Products); err != nil {
				return err
			}
			if err := verifyQualifiedControllerIdentityPermissions(ctx, c); err != nil {
				return err
			}
			if err := checkEnvironmentCRLConsumerPolicy(preview); err != nil {
				return err
			}
			store := c.Store
			store.checkRuntime = newFrozenDeploymentCheckRuntime(ctx, preview.Environment, c.Backend)
			store.checkRuntime.preflightPlan = preview
			if err := verifySecretStoreK8SBindings(store); err != nil {
				return environmentRuntimePKIQualificationError(err)
			}
			window, err := environmentPKIValidityWindow(cfg.Values)
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			for _, at := range []time.Time{now, now.Add(time.Duration(window) * time.Second)} {
				if err := verifySecretStoreK8SRuntime(store, at); err != nil {
					return environmentRuntimePKIQualificationError(err)
				}
			}
			return nil
		},
		"deployment.compatibility": func(ctx context.Context) error {
			store := c.Store
			store.checkRuntime = newFrozenDeploymentCheckRuntime(ctx, preview.Environment, c.Backend)
			if err := verifyEnvironmentReleaseSchemasForPlan(ctx, cfg, store, p.Release, preview, c.Backend.Read); err != nil {
				return environmentSchemaQualificationError(err)
			}
			operator, err := c.Store.readOperator()
			if err != nil {
				return err
			}
			if cfg.Values["OTA_SERVICE_REGISTRATION_ENABLED"] == "true" {
				if err := validateEnvironmentOTAStartup(p.Definitions); err != nil {
					return err
				}
				if err := checkOTAStorageAccess(ctx, cfg, c.Store, operator, defaultDeploymentCredentialChecker()); err != nil {
					return err
				}
			}
			if cfg.Values["VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED"] == "true" {
				if err := checkEnvironmentVideoStorageStartup(ctx, preview, cfg, c.Store, c.Backend.Read, operator, defaultDeploymentCredentialChecker()); err != nil {
					return err
				}
			}
			return checkEnvironmentAppTURNPlan(preview)
		},
		"deployment.network": func(ctx context.Context) error {
			if err := checkEnvironmentPublicTLSPrerequisites(ctx, cfg, c.Store, p.PublicTLS); err != nil {
				return err
			}
			operator, err := c.Store.readOperator()
			if err != nil {
				return err
			}
			if _, err := environmentCreateDataPlaneNodeUpstreams(ctx, cfg, c.Backend); err != nil {
				return err
			}
			return qualifyCreateNormalTailDNS(ctx, cfg, h, operator)
		},
		"deployment.recovery": func(ctx context.Context) error {
			if err := managedLateRedisVerifyOriginalVolumes(ctx, c.Backend, h.OriginalVolumes); err != nil {
				return err
			}
			return verifyCreateNormalTailRedis(ctx, cfg, c.Store, h, c.Backend)
		},
	}
	return checks, nil
}

// Reuse the maintained gate/report contract for a staged create program. This
// is one complete qualification, not a separate GO for each producer or child.
func qualifyCreateNormalTailProgram(ctx context.Context, p *createNormalTailProgram, checks map[string]func(context.Context) error, out io.Writer) (deploymentplan.Qualification, error) {
	q := deploymentplan.Qualification{PlanSHA256: deploymentplan.Digest(p), Decision: "GO"}
	for _, id := range deploymentplan.RequiredGateIDs(false) {
		check := checks[id]
		err := errors.New("complete remaining-program adapter is unavailable")
		if check != nil {
			err = runEnvironmentProgress(ctx, out, id, func(ctx context.Context) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := check(ctx); err != nil {
					return err
				}
				return ctx.Err()
			})
		}
		row := deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: q.PlanSHA256}
		if err != nil || deploymentplan.Digest(p) != q.PlanSHA256 {
			q.Decision = "NO-GO"
			row.Status, row.EvidenceSHA256 = "FAIL", ""
			row.Cause, row.Next = "complete remaining-program prerequisite failed", "repair the reported prerequisite before any remaining native stage"
			var blocker *deploymentplan.Blocker
			if errors.As(err, &blocker) {
				row.Cause, row.Next = blocker.Cause, blocker.Next
				if blocker.Resource != "" {
					row.Cause += " (" + blocker.Resource + ")"
				}
			}
			if ctx.Err() != nil {
				row.Status, row.Cause = "ERROR", "remaining-program qualification was interrupted"
			}
		}
		q.Gates = append(q.Gates, row)
	}
	if q.Decision != "GO" {
		return q, errors.New("complete remaining program is NO-GO")
	}
	return q, nil
}
