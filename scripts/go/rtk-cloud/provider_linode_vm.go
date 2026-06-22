package main

import "fmt"

type retiredVMProvider struct {
	name string
}

func (p retiredVMProvider) Name() string { return p.name }

func (retiredVMProvider) Runtime() provisionRuntime { return provisionRuntimeKubernetes }

func (p retiredVMProvider) EnsureKubeAccess(provisionContext) error {
	return fmt.Errorf("%w: CLOUD_PROVIDER=%s used the legacy Linode VM/systemd runtime; use CLOUD_PROVIDER=lke or another Kubernetes provider", errVMRuntimeRetired, p.name)
}

func (p retiredVMProvider) RunProvision(ctx provisionContext) error {
	return p.EnsureKubeAccess(ctx)
}
