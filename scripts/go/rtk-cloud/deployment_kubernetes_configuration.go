package main

// Whole-environment render and continuation use the same selected configuration.
// Low-level identity inspection retains its context-only constructor.
func newEnvironmentKubernetesBackendForConfig(raw string, cfg deploymentConfig) (environmentKubernetesBackend, error) {
	b, err := newEnvironmentKubernetesBackend(raw, cfg.Environment)
	if err != nil {
		return b, err
	}
	b.SchemaWorkspace = cfg.Workspace
	b.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	return b, err
}
