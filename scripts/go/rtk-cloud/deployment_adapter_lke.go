package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func resolveLKEAdapterResources(workspace string, generic, pins map[string]string) (map[string]string, error) {
	locations, err := readStrictEnv(filepath.Join(workspace, "cloud_deploy", "adapters", "lke", "locations.env"))
	if err != nil {
		return nil, err
	}
	location := strings.TrimSpace(generic["DEPLOYMENT_LOCATION"])
	locationKey := "LOCATION_" + strings.ToUpper(strings.ReplaceAll(location, "-", "_"))
	region := firstNonEmpty(pins["LKE_REGION_PIN"], locations[locationKey])
	if region == "" {
		return nil, fmt.Errorf("LKE adapter has no region mapping for DEPLOYMENT_LOCATION=%q", location)
	}
	resolved := map[string]string{"LKE_REGION": region}
	for _, class := range []string{"GENERAL", "BROKER", "DATABASE"} {
		cpu, _ := strconv.Atoi(generic["NODE_CLASS_"+class+"_MIN_VCPU"])
		memory, _ := strconv.Atoi(generic["NODE_CLASS_"+class+"_MIN_MEMORY_GIB"])
		nodeType, selectErr := selectLKENodeType(cpu, memory, pins["LKE_"+class+"_NODE_TYPE_PIN"])
		if selectErr != nil {
			return nil, fmt.Errorf("node class %s: %w", strings.ToLower(class), selectErr)
		}
		resolved["LKE_"+class+"_NODE_TYPE"] = nodeType
	}
	return resolved, nil
}

func selectLKENodeType(minVCPU, minMemoryGi int, pin string) (string, error) {
	if pin != "" {
		shape, ok := lkeNodeTypeShape(pin)
		if !ok {
			return "", fmt.Errorf("unknown pinned Linode type %q", pin)
		}
		if shape.cpu < minVCPU || shape.memoryGi < minMemoryGi {
			return "", fmt.Errorf("pinned Linode type %s provides %d vCPU/%d GiB, below required %d vCPU/%d GiB", pin, shape.cpu, shape.memoryGi, minVCPU, minMemoryGi)
		}
		return pin, nil
	}
	type candidate struct {
		name          string
		memorySurplus int
		cpuSurplus    int
	}
	catalog := lkeNodeTypeCatalog()
	candidates := make([]candidate, 0, len(catalog))
	for name, shape := range catalog {
		if shape.cpu >= minVCPU && shape.memoryGi >= minMemoryGi {
			candidates = append(candidates, candidate{name: name, memorySurplus: shape.memoryGi - minMemoryGi, cpuSurplus: shape.cpu - minVCPU})
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no Linode type satisfies %d vCPU/%d GiB", minVCPU, minMemoryGi)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].memorySurplus != candidates[j].memorySurplus {
			return candidates[i].memorySurplus < candidates[j].memorySurplus
		}
		if candidates[i].cpuSurplus != candidates[j].cpuSurplus {
			return candidates[i].cpuSurplus < candidates[j].cpuSurplus
		}
		return candidates[i].name < candidates[j].name
	})
	return candidates[0].name, nil
}
