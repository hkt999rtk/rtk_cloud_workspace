package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The bundle only preserves bytes. It does not qualify evidence or authorize
// mutation: callers must independently validate the proof and live owners.
func saveFailedForwardBundle(store secretStore, e *environmentCreateExecution, q createFailedForwardProof, repair, retain *deploymentplan.Plan) error {
	bad := func() error {
		return createProviderRecoveryBlock("private original failed-stage proof and plans cannot be preserved")
	}
	if e == nil || e.Store != store || e.Check(context.Background()) != nil || q.Schema != failedForwardProofSchema || q.Environment != store.Environment || q.FailedStage.ParentSHA256 != e.Journal.PlanSHA256 || repair == nil || retain == nil || q.CandidateSHA256 != repair.SHA256() || q.RetainCandidateSHA256 != retain.SHA256() {
		return bad()
	}
	review := deploymentplan.Digest(q)
	dir, err := failedForwardBundleDirectory(store, e, review, true)
	if err != nil {
		return err
	}
	// Plans precede proof: a visible complete proof must never imply plans exist
	// if the process was interrupted during the save.
	for _, entry := range []struct {
		name  string
		value any
	}{{"repair-plan.json", repair}, {"retain-plan.json", retain}, {"qualification.json", q}} {
		if e.Check(context.Background()) != nil {
			return bad()
		}
		if err := writeFailedForwardBundleImmutable(filepath.Join(dir, entry.name), entry.value); err != nil {
			return err
		}
	}
	return e.Check(context.Background())
}

func loadFailedForwardBundle(store secretStore, e *environmentCreateExecution, review string) (createFailedForwardProof, *deploymentplan.Plan, *deploymentplan.Plan, error) {
	var q createFailedForwardProof
	var repair, retain deploymentplan.Plan
	bad := func() (createFailedForwardProof, *deploymentplan.Plan, *deploymentplan.Plan, error) {
		return createFailedForwardProof{}, nil, nil, createProviderRecoveryBlock("private original failed-stage proof and exact plans are unavailable or changed")
	}
	if e == nil || e.Store != store || e.Check(context.Background()) != nil {
		return bad()
	}
	dir, err := failedForwardBundleDirectory(store, e, review, false)
	if err != nil {
		return bad()
	}
	if readEnvironmentPreflightJSON(filepath.Join(dir, "qualification.json"), &q) != nil || readEnvironmentPreflightJSON(filepath.Join(dir, "repair-plan.json"), &repair) != nil || readEnvironmentPreflightJSON(filepath.Join(dir, "retain-plan.json"), &retain) != nil {
		return bad()
	}
	if q.Schema != failedForwardProofSchema || q.Environment != store.Environment || q.FailedStage.ParentSHA256 != e.Journal.PlanSHA256 || deploymentplan.Digest(q) != review || q.CandidateSHA256 != repair.SHA256() || q.RetainCandidateSHA256 != retain.SHA256() || e.Check(context.Background()) != nil {
		return bad()
	}
	return q, &repair, &retain, nil
}

func failedForwardBundleDirectory(store secretStore, e *environmentCreateExecution, review string, create bool) (string, error) {
	bad := func() (string, error) {
		return "", createProviderRecoveryBlock("private failed-stage bundle directory is unavailable")
	}
	if e == nil || e.Store != store || !environmentPreflightDigest.MatchString(e.Journal.PlanSHA256) || !environmentPreflightDigest.MatchString(review) || store.Root != filepath.Join(store.ConfigRoot, store.Environment) {
		return bad()
	}
	for _, path := range []string{store.ConfigRoot, store.Root} {
		if validateEnvironmentPreflightDirectory(path) != nil {
			return bad()
		}
	}
	dir := store.Root
	for _, part := range []string{"deployment", "create", e.Journal.PlanSHA256, "failed-stage-forward", review} {
		dir = filepath.Join(dir, part)
		if create {
			if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return bad()
			}
		}
		if validateEnvironmentPreflightDirectory(dir) != nil {
			return bad()
		}
	}
	return dir, nil
}

// A private temporary file and exclusive link publish fully synced bytes without
// clobbering evidence if a retry or another writer already owns the pathname.
func writeFailedForwardBundleImmutable(path string, value any) error {
	bad := func() error {
		return createProviderRecoveryBlock("private failed-stage evidence differs or cannot be safely written")
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) == 0 || len(raw) > environmentPreflightArtifactLimit {
		return bad()
	}
	if previous, err := readBoundedDerivedArtifact(path); err == nil {
		if bytes.Equal(previous, raw) {
			return nil
		}
		return bad()
	} else if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		return bad()
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".qualification-*")
	if err != nil {
		return bad()
	}
	name := f.Name()
	defer os.Remove(name)
	if f.Chmod(0600) != nil {
		f.Close()
		return bad()
	}
	n, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || n != len(raw) {
		return bad()
	}
	if err = os.Link(name, path); err != nil {
		if previous, readErr := readBoundedDerivedArtifact(path); readErr == nil && bytes.Equal(previous, raw) {
			return nil
		}
		return bad()
	}
	return nil
}
