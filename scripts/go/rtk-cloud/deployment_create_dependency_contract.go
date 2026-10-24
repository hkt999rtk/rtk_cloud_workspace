package main

import (
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Lifecycle outputs are contracts, not synthetic Kubernetes objects. Return
// the real consumer definitions; the separate, unsaved graph below only checks
// ordering/cycles after the declared prior-stage outputs have been supplied.
func validateEnvironmentDeploymentDependencyContract(inputs deploymentplan.Inputs, definitions []deploymentplan.Definition, live []deploymentplan.Object, bindings environmentIdentityBindings, outputs map[string]string) ([]deploymentplan.Definition, error) {
	for id, contract := range outputs {
		parts := strings.Split(id, "/")
		if len(parts) != 3 || parts[0] == "" || parts[2] == "" || parts[1] != "" && !strings.HasPrefix(parts[1], inputs.Stack+"-") || !environmentPreflightDigest.MatchString(contract) {
			return nil, environmentCreateBootstrapBlocker("future dependency output is not an exact selected lifecycle contract")
		}
	}
	bound, err := bindRetainedEnvironmentDependenciesWithOutputs(slices.Clone(definitions), live, bindings, outputs)
	if err != nil {
		return nil, err
	}
	present := map[string]bool{}
	for _, definition := range bound {
		present[deploymentplan.ID(definition.Object)] = true
	}
	graph := slices.Clone(bound)
	for i := range graph {
		graph[i].Dependencies = nil
		for _, id := range bound[i].Dependencies {
			if outputs[id] != "" && !present[id] {
				continue
			}
			graph[i].Dependencies = append(graph[i].Dependencies, id)
		}
	}
	if _, err := deploymentplan.Compile(inputs, graph, live, bindings.Owners); err != nil {
		return nil, err
	}
	return bound, nil
}
