package main

import "fmt"

type linodeVMProvider struct{}

func (linodeVMProvider) Name() string { return "linode" }

func (linodeVMProvider) Runtime() provisionRuntime { return provisionRuntimeVM }

func (linodeVMProvider) EnsureKubeAccess(provisionContext) error {
	return fmt.Errorf("%w: linode uses the legacy VM runtime, not Kubernetes", errProviderUnsupported)
}

func (linodeVMProvider) RunProvision(ctx provisionContext) error {
	return runLinodeVMProvision(ctx.Paths, ctx.Env, ctx.Opts)
}
