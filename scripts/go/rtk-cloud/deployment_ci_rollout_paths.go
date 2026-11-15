package main

import (
	"errors"
	"path/filepath"
)

func ciRolloutPrivatePath(store secretStore, relative string) (string, error) {
	path, err := privateDeploymentStatePath(store.Environment, relative)
	if err != nil {
		return "", err
	}
	if filepath.Clean(path) != filepath.Join(store.Root, filepath.Clean(relative)) {
		return "", errors.New("CI rollout: selected canonical environment store is required")
	}
	return path, nil
}
