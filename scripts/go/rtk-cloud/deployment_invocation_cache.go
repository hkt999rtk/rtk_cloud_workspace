package main

import (
	"context"
	"encoding/json"
	"regexp"
	"sync"
)

type deploymentInvocationCacheKey struct{}
type deploymentInvocationCache struct {
	mu     sync.Mutex
	values map[string][]byte
}

var immutableCIArchive = regexp.MustCompile(`^repos/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/actions/artifacts/[1-9][0-9]*/zip$`)

// Reuse only immutable content within this invocation. Run metadata, artifact
// expiry/availability, source revisions, credentials and live state stay fresh.
func cacheableDeploymentCICommand(command string, args []string) bool {
	if command == "gh" {
		return len(args) == 2 && args[0] == "api" && immutableCIArchive.MatchString(args[1])
	}
	return command == "docker" && len(args) == 5 && args[0] == "image" && args[1] == "inspect" && immutableEnvironmentImage.MatchString(args[2]) && args[3] == "--format" && args[4] == "{{json .Config.Labels}}"
}
func cachedDeploymentCICommand(ctx context.Context, environment, command string, args []string, run func() ([]byte, error)) ([]byte, error) {
	c, ok := ctx.Value(deploymentInvocationCacheKey{}).(*deploymentInvocationCache)
	if !ok || !cacheableDeploymentCICommand(command, args) {
		return run()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keyRaw, _ := json.Marshal([]any{environment, command, args})
	key := string(keyRaw)
	c.mu.Lock()
	value, hit := c.values[key]
	c.mu.Unlock()
	if hit {
		deploymentMeasurementCacheHitKind(ctx, "immutable-ci-content")
		return append([]byte(nil), value...), nil
	}
	raw, err := run()
	if err == nil && len(raw) <= 16<<20 {
		c.mu.Lock()
		c.values[key] = append([]byte(nil), raw...)
		c.mu.Unlock()
	}
	return raw, err
}
