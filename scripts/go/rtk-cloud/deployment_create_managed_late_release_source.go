package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"time"
)

// The completed ingress child ends the old parsing phase. Reauthenticate that
// exact held checkpoint before sharing only immutable ancestry with ACK readers.
func managedLateNormalTailReleaseCommands(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime) ([]*environmentCreateExecution, error) {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r == nil || r.normalTail == nil {
		return nil, nil
	}
	bad := func() ([]*environmentCreateExecution, error) { return nil, managedLateSourceReuseBlock() }
	n := r.normalTail
	if r.redisDurable != nil && e != nil && len(e.Journal.Entries) == 443 {
		if err := managedLateRedisRuntime(ctx, cfg, store, e, r, r.redisDurable); err != nil {
			return nil, err
		}
		outer := createOpenBaoACKCompletionEntry(e.Journal, managedLateRedisDurableKey)
		if outer == nil || outer.State != "completed" {
			return bad()
		}
		if _, _, err := readManagedLateRedisDurableVolumeAuthority(store, e); err != nil {
			return nil, err
		}
		invalidateManagedLateSourceReuse(e)
		return []*environmentCreateExecution{e}, nil
	}
	if e == nil || e.Check(ctx) != nil || store != e.Store || runtime.Backend.Store != store || runtime.ExecutionSHA256 != e.Journal.PlanSHA256 || cfg.Environment != store.Environment || !managedLateNormalTailControllerSelected(e.Journal) || e.Journal.Entries[431].State != "completed" || n.Authority == nil || n.OwnerSHA != deploymentplan.Digest(e.owner) || !time.Now().Before(managedLateNormalTailEffectiveDeadline(r)) {
		return bad()
	}
	if managedLateSourceReuseActive(e) {
		if err := checkManagedLateSourceReuse(e); err != nil {
			return nil, err
		}
	}
	fresh, err := loadManagedLateCompletedTailSource(ctx, cfg, store, e)
	if err != nil {
		return nil, err
	}
	if fresh == nil || fresh.normalTail == nil || fresh.normalTail.Authority == nil || fresh.normalTail.SourceSHA != n.SourceSHA || fresh.normalTail.OwnerSHA != n.OwnerSHA || deploymentplan.Digest(fresh.normalTail.Approval) != deploymentplan.Digest(n.Approval) || deploymentplan.Digest(fresh.normalTail.Authority) != deploymentplan.Digest(n.Authority) || !managedLateSourceReuseActive(e) {
		return bad()
	}
	if err := checkManagedLateSourceReuse(e); err != nil {
		return nil, err
	}
	if err := e.Check(ctx); err != nil {
		return nil, err
	}
	return []*environmentCreateExecution{e}, nil
}

// A stopped normal-tail command may already have published these exact public
// bindings. Reuse only identical private bytes; a new publication ends the phase.
func writeManagedLateNormalTailBindings(ctx context.Context, store secretStore, e *environmentCreateExecution, bindings environmentIdentityBindings) (string, error) {
	bad := func() (string, error) { return "", managedLateSourceReuseBlock() }
	if e == nil || store != e.Store || e.Check(ctx) != nil || !managedLateNormalTailControllerSelected(e.Journal) || e.Journal.Entries[431].State != "completed" || bindings.Environment != store.Environment || bindings.Stack != e.Journal.Stack {
		return bad()
	}
	raw, err := json.Marshal(bindings)
	if err != nil || len(raw) > 256<<10 {
		return bad()
	}
	relative := filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/bindings.json")
	path, err := privateDeploymentStatePath(store.Environment, relative)
	if err != nil || path != filepath.Join(store.Root, relative) || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
		return bad()
	}
	if managedLateSourceReuseActive(e) {
		if err := checkManagedLateSourceReuse(e); err != nil {
			return "", err
		}
	}
	if _, statErr := os.Lstat(path); statErr == nil {
		old, readErr := readBoundedEnvironmentFile(path, true)
		if readErr != nil || !bytes.Equal(old, raw) {
			return bad()
		}
		if err := e.Check(ctx); err != nil {
			return "", err
		}
		return "/store/" + filepath.ToSlash(relative), nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return bad()
	}
	invalidateManagedLateSourceReuse(e)
	result, err := writeEnvironmentCreateBootstrapJSON(store, relative, bindings)
	if err != nil {
		return "", err
	}
	return result, e.Check(ctx)
}
