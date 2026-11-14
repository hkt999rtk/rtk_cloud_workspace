package main

import (
	"reflect"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Apply only authenticated owned-field changes to the captured API baseline.
// Unchanged defaults/allocations remain exact drift fences, not new intent.
func recordedWorkloadBaseline(known, before, after deploymentplan.Object) deploymentplan.Object {
	return deploymentplan.Map(recordedOwnedDelta(known, before, after))
}
func cloneRecordedValue(value any) any {
	return deploymentplan.Clone(deploymentplan.Object{"value": value})["value"]
}
func recordedOwnedDelta(known, before, after any) any {
	if reflect.DeepEqual(before, after) {
		return cloneRecordedValue(known)
	}
	previous, next := deploymentplan.Map(before), deploymentplan.Map(after)
	if previous != nil && next != nil {
		result := deploymentplan.Map(cloneRecordedValue(known))
		if result == nil {
			result = deploymentplan.Object{}
		}
		for key := range previous {
			if _, exists := next[key]; !exists {
				delete(result, key)
			}
		}
		for key, value := range next {
			old, declared := previous[key]
			if !declared {
				result[key] = cloneRecordedValue(value)
			} else {
				result[key] = recordedOwnedDelta(result[key], old, value)
			}
		}
		return result
	}
	oldList, oldOK := before.([]any)
	newList, newOK := after.([]any)
	if oldOK && newOK && len(oldList)+len(newList) > 0 && recordedNamedList(oldList) && recordedNamedList(newList) {
		oldByName := map[string]any{}
		for _, item := range oldList {
			oldByName[deploymentplan.String(deploymentplan.Map(item)["name"])] = item
		}
		knownList, _ := known.([]any)
		knownByName := map[string]any{}
		for _, item := range knownList {
			knownByName[deploymentplan.String(deploymentplan.Map(item)["name"])] = item
		}
		result := []any{}
		selected := map[string]bool{}
		for _, item := range newList {
			name := deploymentplan.String(deploymentplan.Map(item)["name"])
			selected[name] = true
			if old, exists := oldByName[name]; exists {
				result = append(result, recordedOwnedDelta(knownByName[name], old, item))
			} else {
				result = append(result, cloneRecordedValue(item))
			}
		}
		for _, item := range knownList {
			name := deploymentplan.String(deploymentplan.Map(item)["name"])
			if _, owned := oldByName[name]; !owned && !selected[name] {
				result = append(result, cloneRecordedValue(item))
			}
		}
		return result
	}
	return cloneRecordedValue(after)
}
func recordedNamedList(values []any) bool {
	seen := map[string]bool{}
	for _, value := range values {
		name := deploymentplan.String(deploymentplan.Map(value)["name"])
		if name == "" || seen[name] {
			return false
		}
		seen[name] = true
	}
	return true
}
