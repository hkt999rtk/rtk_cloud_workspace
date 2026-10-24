package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
)

// Operational artifacts use the configured environment, never the checkout's
// artifact directory or an ambient default environment.
func deploymentLKEArtifactPath(paths provisionPaths, relative string) (string, error) {
	relative = filepath.ToSlash(relative)
	if filepath.ToSlash(filepath.Clean(relative)) != relative || filepath.IsAbs(relative) || strings.ContainsAny(relative, "\\:") {
		return "", errors.New("invalid LKE operational artifact path")
	}
	parts := strings.Split(relative, "/")
	if len(parts) < 2 || parts[0] != "edge-haproxy" && parts[0] != "coturn-vm" {
		return "", errors.New("unknown LKE operational artifact category")
	}
	for _, part := range parts {
		if part == "." || part == ".." || part == "" {
			return "", errors.New("invalid LKE operational artifact path")
		}
	}
	environment, _, err := lkeProviderTarget(paths, nil)
	if err != nil {
		return "", err
	}
	return privateDeploymentStatePath(environment, filepath.Join("deployment", "artifacts", relative))
}

func writeDeploymentLKEArtifact(paths provisionPaths, relative string, raw []byte) error {
	path, err := deploymentLKEArtifactPath(paths, relative)
	if err != nil {
		return err
	}
	environment, _, err := lkeProviderTarget(paths, nil)
	if err != nil {
		return err
	}
	store, err := newSecretStore("", environment)
	if err != nil {
		return err
	}
	for _, directory := range []string{store.ConfigRoot, store.Root} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return err
		}
	}
	relative, err = filepath.Rel(store.Root, path)
	if err != nil {
		return err
	}
	return store.write(relative, raw, true)
}

func writeDeploymentLKEArtifactJSON(paths provisionPaths, relative string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeDeploymentLKEArtifact(paths, relative, append(raw, '\n'))
}
