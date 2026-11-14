package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func (r *maintenanceRuntime) acquireMaintenanceExecution(ctx context.Context, plan *deploymentplan.Plan) error {
	fence := filepath.Join(r.store.Root, "deployment/.execution-active")
	if _, err := os.Lstat(fence); err == nil {
		var owner environmentCreateOwner
		if readEnvironmentPreflightJSON(filepath.Join(fence, "owner.json"), &owner) != nil {
			return errors.New("CRL recovery cannot authenticate the retained original create owner")
		}
		e, artifact, err := loadCreateRecoveryExecutionOwner(r.cfg, r.store, owner.PlanSHA256)
		if err != nil {
			return err
		}
		accepted := false
		defer func() {
			if !accepted {
				_ = e.Close()
			}
		}()
		// The original admission binds raw input bytes. The backend separately
		// freezes that exact input into a selected, normalized API/auth context.
		if !environmentPreflightDigest.MatchString(r.sourceKubeconfigSHA) || artifact.Plan.KubeconfigSHA256 != r.sourceKubeconfigSHA {
			return errors.New("CRL recovery Kubernetes target differs from the original create generation")
		}
		plan.CreateParentSHA256 = e.Journal.PlanSHA256
		if err := checkCreateRootCRLLeaseAvailable(ctx, e, r.backend, plan); err != nil {
			return err
		}
		original := environmentCreateKubernetesBackend{environmentKubernetesBackend: r.backend, execution: e, cfg: r.cfg, store: r.store}
		lease, err := original.AcquireExecution(ctx, plan)
		if err != nil {
			return err
		}
		if err := original.RecordParentExecutionLease(ctx, plan, lease.Owner()); err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = lease.Release(cleanup)
			return err
		}
		r.lease, r.createExecution = lease, e
		accepted = true
		return nil
	}
	if err := checkEnvironmentExecutionOccupancy(ctx, r.backend, plan, r.store.Root); err != nil {
		return err
	}
	lease, err := r.backend.AcquireExecution(ctx, plan)
	if err != nil {
		return err
	}
	r.lease = lease
	return nil
}
