package main

import "fmt"

type unsupportedKubernetesProvider struct {
	name string
}

func (p unsupportedKubernetesProvider) Name() string { return p.name }

func (p unsupportedKubernetesProvider) Runtime() provisionRuntime {
	return provisionRuntimeKubernetes
}

func (p unsupportedKubernetesProvider) EnsureKubeAccess(provisionContext) error {
	return fmt.Errorf("%w: CLOUD_PROVIDER=%s Kubernetes adapter is not implemented", errProviderUnsupported, p.name)
}

func (p unsupportedKubernetesProvider) RunProvision(ctx provisionContext) error {
	return p.EnsureKubeAccess(ctx)
}
