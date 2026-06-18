package main

type lkeCloudProvider struct{}

func (lkeCloudProvider) Name() string { return "lke" }

func (lkeCloudProvider) Runtime() provisionRuntime { return provisionRuntimeKubernetes }

func (lkeCloudProvider) EnsureKubeAccess(ctx provisionContext) error {
	return ensureLKEKubeAccess(ctx.Paths, ctx.Env, ctx.Opts.mode.apply)
}

func (lkeCloudProvider) RunProvision(ctx provisionContext) error {
	return runKubernetesProvision(lkeCloudProvider{}, ctx)
}
