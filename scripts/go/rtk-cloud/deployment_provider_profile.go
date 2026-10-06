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
	store, err := newSecretStore("", environment)
	if err != nil {
		return "", err
	}
	for _, directory := range []string{store.ConfigRoot, store.Root, filepath.Join(store.Root, "deployment")} {
		info, err := os.Lstat(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
			return "", errors.New("provider profile requires real private 0700 environment directories")
		}
	}
	path, err := store.safePath("deployment/provider-preflight.env")
	if err != nil {
		return "", err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return "", errors.New("provider profile must be a private regular 0600 file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return path, nil
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
	path, err := deploymentProviderProfilePath(environment)
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
	// Use the existing private atomic publisher; no receipt/GO flag is generated.
	return store.write("deployment/provider-preflight.env", []byte(sortedEnvContents(values)), true)
}
