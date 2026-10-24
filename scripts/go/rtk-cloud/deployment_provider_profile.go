package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Normalized provider metadata is environment state, never checkout config or
// deployment completion evidence. Both its producer and acceptance reader use
// this path; a missing private file never selects the legacy runtime/state file.
func deploymentProviderProfilePath(environment string) (string, error) {
	return privateDeploymentStatePath(environment, "deployment/provider-preflight.env")
}

func privateDeploymentStatePath(environment, relative string) (string, error) {
	store, err := newSecretStore("", environment)
	if err != nil {
		return "", err
	}
	path, err := store.safePath(relative)
	if err != nil {
		return "", err
	}
	directories := []string{store.ConfigRoot, store.Root}
	parent := store.Root
	for _, part := range strings.Split(filepath.Dir(relative), string(filepath.Separator)) {
		parent = filepath.Join(parent, part)
		directories = append(directories, parent)
	}
	for _, directory := range directories {
		info, err := os.Lstat(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
			return "", errors.New("deployment state requires real private 0700 environment directories")
		}
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return "", errors.New("deployment state must be a private regular 0600 file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return path, nil
}

func writePrivateDeploymentState(environment, relative string, raw []byte) error {
	path, err := privateDeploymentStatePath(environment, relative)
	if err != nil {
		return err
	}
	store, err := newSecretStore("", environment)
	if err != nil {
		return err
	}
	for _, directory := range []string{store.ConfigRoot, store.Root, filepath.Dir(path)} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return err
		}
	}
	return store.write(relative, raw, true)
}

func writeDeploymentProviderProfile(environment string, values map[string]string) error {
	for key, value := range values {
		if key != "PROVIDER_REGION" && key != "PROVIDER_ACTIVE_SERVICE_LIMIT" {
			return errors.New("provider profile contains a value outside the normalized metadata contract")
		}
		if strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n") || value == "" {
			return errors.New("provider profile requires nonempty single-line metadata values")
		}
		if key == "PROVIDER_ACTIVE_SERVICE_LIMIT" {
			if err := validateActiveServiceLimit(value); err != nil {
				return err
			}
		}
	}
	// Use the existing private atomic publisher; no receipt/GO flag is generated.
	return writePrivateDeploymentState(environment, "deployment/provider-preflight.env", []byte(sortedEnvContents(values)))
}
