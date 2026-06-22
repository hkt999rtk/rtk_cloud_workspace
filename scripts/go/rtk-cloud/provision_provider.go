package main

import (
	"errors"
	"fmt"
)

type provisionRuntime string

const (
	provisionRuntimeKubernetes provisionRuntime = "kubernetes"
)

var errProviderUnsupported = errors.New("cloud provider not implemented")
var errVMRuntimeRetired = errors.New("VM runtime deployment retired")

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
	case "":
		return lkeCloudProvider{}, nil
	case "linode":
		return retiredVMProvider{name: name}, nil
	case "lke":
		return lkeCloudProvider{}, nil
	case "k8s", "gke", "aks", "eks":
		return unsupportedKubernetesProvider{name: name}, nil
	default:
		return nil, fmt.Errorf("unsupported CLOUD_PROVIDER %q", name)
	}
}

func isKubernetesProviderName(name string) bool {
	switch name {
	case "lke", "k8s", "gke", "aks", "eks":
		return true
	default:
		return false
	}
}
