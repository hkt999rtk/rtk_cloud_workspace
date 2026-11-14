package main

import (
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Validate declared images before the joined fixture records any original
// owner. Runtime OCI digest/Pod acceptance remains in the ordinary executor.
func auditJoinedNativeContainerImages(t *testing.T, definitions []deploymentplan.Definition) {
	t.Helper()
	for _, definition := range definitions {
		object := definition.Object
		pod := environmentPKIPod(object)
		if object["kind"] == "Pod" {
			pod = deploymentplan.Map(object["spec"])
		}
		if pod == nil {
			continue
		}
		containers, err := environmentPKIPodContainers(pod)
		if err != nil {
			t.Fatalf("joined declared container shape %s: %v", deploymentplan.ID(object), err)
		}
		seen := map[string]bool{}
		for _, container := range containers {
			name := deploymentplan.String(container["name"])
			image := deploymentplan.String(container["image"])
			if name == "" || seen[name] || !immutableEnvironmentImage.MatchString(image) {
				t.Fatalf("joined declared container identity/image %s container=%s", deploymentplan.ID(object), name)
			}
			seen[name] = true
		}
	}
}
