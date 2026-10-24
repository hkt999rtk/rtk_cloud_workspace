package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
)

func managedLateTerminalFinalizerAdmissionPath(parent string) string {
	return filepath.Join("deployment/create", parent, "bootstrap/service", "managed-late-terminal-finalizer-admission.json")
}

// A partial pair is evidence of an attempted admission, never a fresh clock.
func readManagedLateTerminalFinalizerAuthorityPair(store secretStore, parent string) ([]byte, error) {
	bad := func() ([]byte, error) { return nil, createOpenBaoACKBlock("terminal-finalizer-admission-pair") }
	if !environmentPreflightDigest.MatchString(parent) {
		return bad()
	}
	paths := []string{managedLateTerminalFinalizerPath(parent), managedLateTerminalFinalizerAdmissionPath(parent)}
	absent := 0
	for i, relative := range paths {
		path, err := privateDeploymentStatePath(store.Environment, relative)
		if err != nil || path != filepath.Join(store.Root, relative) || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
			return bad()
		}
		paths[i] = path
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			absent++
		} else if err != nil {
			return bad()
		}
	}
	if absent == 2 {
		return nil, nil
	}
	if absent != 0 {
		return bad()
	}
	primary, err := readBoundedEnvironmentFile(paths[0], true)
	witness, witnessErr := readBoundedEnvironmentFile(paths[1], true)
	if err != nil || witnessErr != nil || len(primary) == 0 || !bytes.Equal(primary, witness) {
		return bad()
	}
	return primary, nil
}

func writeManagedLateTerminalFinalizerAuthorityPair(store secretStore, parent string, raw []byte) error {
	prior, err := readManagedLateTerminalFinalizerAuthorityPair(store, parent)
	if err != nil || len(prior) != 0 || len(raw) == 0 {
		return createOpenBaoACKBlock("terminal-finalizer-admission-pair-exists")
	}
	// Commit the witness first. A failure creating the primary leaves a durable
	// orphan which all later readers reject instead of admitting another clock.
	for _, relative := range []string{managedLateTerminalFinalizerAdmissionPath(parent), managedLateTerminalFinalizerPath(parent)} {
		path, err := privateDeploymentStatePath(store.Environment, relative)
		if err != nil || path != filepath.Join(store.Root, relative) || writeExclusivePrivateDeploymentJSON(path, raw) != nil || syncEnvironmentPreflightDirectory(filepath.Dir(path)) != nil {
			return createOpenBaoACKBlock("terminal-finalizer-admission-pair-write")
		}
	}
	observed, err := readManagedLateTerminalFinalizerAuthorityPair(store, parent)
	if err != nil || !bytes.Equal(observed, raw) {
		return createOpenBaoACKBlock("terminal-finalizer-admission-pair-readback")
	}
	return nil
}
