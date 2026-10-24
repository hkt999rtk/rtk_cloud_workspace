package main

import (
	"encoding/json"
	"path/filepath"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This typed snapshot includes the entire authenticated parent journal. Its
// bound matches the existing private completion reader; small ceremony records
// retain their separate 256 KiB limit.
func writeManagedLateRedisDurableSource(store secretStore, saved managedLateRedisDurableSaved) (string, error) {
	d := &saved.Descriptor
	if d.Schema != managedLateRedisDurableSchema || !environmentPreflightDigest.MatchString(d.ParentSHA) || d.ParentSHA != saved.Source.PlanSHA256 || len(saved.Source.Entries) != 434 || d.SourceLeases != len(saved.Source.KubernetesLeases) || d.SourceSHA != deploymentplan.Digest(saved.Source) {
		return "", managedLateRedisDurableError("source-snapshot-shape")
	}
	raw, err := json.Marshal(saved)
	if err != nil || len(raw) > 4<<20 {
		return "", managedLateRedisDurableError("source-snapshot-encoding")
	}
	relative := filepath.Join(managedLateRedisDurableBase(d.ParentSHA), "source.json")
	path, err := privateDeploymentStatePath(store.Environment, relative)
	if err != nil || path != filepath.Join(store.Root, relative) {
		return "", managedLateRedisDurableError("source-snapshot-path")
	}
	if _, err := prepareEnvironmentCreateDirectories(store, filepath.Dir(relative)); err != nil {
		return "", err
	}
	if err := store.write(relative, raw, false); err != nil {
		return "", managedLateRedisDurableError("source-snapshot-exclusive")
	}
	return "/store/" + filepath.ToSlash(relative), nil
}
