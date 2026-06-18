package main

import (
	"errors"
	"fmt"
)

type provisionRuntime string

const (
	provisionRuntimeKubernetes provisionRuntime = "kubernetes"
	provisionRuntimeVM         provisionRuntime = "vm"
)

var errProviderUnsupported = errors.New("cloud provider not implemented")

type provisionContext struct {
	Paths provisionPaths
	Env   map[string]string
	Opts  provisionOptions
}

type cloudProvider interface {
	Name() string
	Runtime() provisionRuntime
	EnsureKubeAccess(ctx provisionContext) error
	RunProvision(ctx provisionContext) error
}

func newCloudProvider(name string) (cloudProvider, error) {
	switch name {
	case "", "linode":
		return linodeVMProvider{}, nil
	case "lke":
		return lkeCloudProvider{}, nil
	case "gke", "aks", "eks":
		return unsupportedKubernetesProvider{name: name}, nil
	default:
		return nil, fmt.Errorf("unsupported CLOUD_PROVIDER %q", name)
	}
}
