package main

import "rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"

// Select the maintained class before allocating a new claim. Existing owners
// retain their original immutable claim/template, including the original class.
func bindEnvironmentNewStorageClasses(definitions []deploymentplan.Definition, class string, owners map[string]string) error {
	for i := range definitions {
		d := &definitions[i]
		if d.Retain || d.Retire || owners[deploymentplan.ID(d.Object)] != "" || deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["uid"]) != "" {
			continue
		}
		var specs []deploymentplan.Object
		switch d.Object["kind"] {
		case "PersistentVolumeClaim":
			specs = append(specs, deploymentplan.Map(d.Object["spec"]))
		case "StatefulSet":
			for _, template := range certIssuerObjectList(deploymentplan.Map(d.Object["spec"])["volumeClaimTemplates"]) {
				specs = append(specs, deploymentplan.Map(deploymentplan.Map(template)["spec"]))
			}
		}
		if len(specs) == 0 {
			continue
		}
		if class == "" {
			return &deploymentplan.Blocker{ID: "deployment.target.storage", Resource: deploymentplan.ID(d.Object), Cause: "new persistent allocation has no selected maintained storage class", Next: "set the shared durable storage class before qualifying the complete target"}
		}
		for _, spec := range specs {
			if spec == nil || spec["storageClassName"] != nil && spec["storageClassName"] != class {
				return &deploymentplan.Blocker{ID: "deployment.target.storage", Resource: deploymentplan.ID(d.Object), Cause: "new persistent allocation differs from the selected storage class", Next: "align the maintained new allocation recipe; preserve existing claims through their original owner"}
			}
			spec["storageClassName"] = class
		}
	}
	return nil
}

// Fleet's existing claim template is an immutable allocation. Keep the native
// template envelope and storage class, and compile mutable workload settings
// from the maintained recipe.
func bindFleetValkeyAllocations(definitions []deploymentplan.Definition, live []deploymentplan.Object, bindings environmentIdentityBindings) error {
	id := "StatefulSet/" + bindings.Stack + "-platform/fleet-valkey"
	for _, definition := range definitions {
		if deploymentplan.ID(definition.Object) != id || definition.Retain || definition.Retire {
			continue
		}
		block := func(cause string) error {
			return &deploymentplan.Blocker{ID: "deployment.compatibility.fleet-valkey", Resource: id, Cause: cause,
				Next: "verify the original Fleet Valkey owner and claim; qualify any storage layout change separately"}
		}
		var current deploymentplan.Object
		uid := bindings.Owners[id]
		for _, object := range live {
			if deploymentplan.ID(object) != id {
				continue
			}
			meta := deploymentplan.Map(object["metadata"])
			if current != nil || uid == "" || meta["uid"] != uid || meta["deletionTimestamp"] != nil {
				return block("Fleet Valkey allocation does not belong to the observed owner")
			}
			current = object
		}
		if current == nil {
			if uid != "" {
				return block("original Fleet Valkey owner is absent")
			}
			continue // Fresh allocation uses the maintained storage class.
		}

		want := deploymentplan.Map(definition.Object["spec"])
		old := deploymentplan.Map(current["spec"])
		original := certIssuerObjectList(old["volumeClaimTemplates"])
		target := certIssuerObjectList(deploymentplan.Clone(want)["volumeClaimTemplates"])
		if len(original) != 1 || len(target) != 1 {
			return block("Fleet Valkey requires exactly one original data claim template")
		}
		originalClaim := deploymentplan.Map(original[0])
		targetClaim := deploymentplan.Map(target[0])
		originalSpec := deploymentplan.Map(originalClaim["spec"])
		targetSpec := deploymentplan.Map(targetClaim["spec"])
		if deploymentplan.Map(originalClaim["metadata"])["name"] != "data" || originalSpec == nil || targetSpec == nil ||
			originalSpec["volumeMode"] != nil && originalSpec["volumeMode"] != "Filesystem" {
			return block("original Fleet Valkey data claim template is incompatible")
		}
		for _, field := range []string{"storageClassName", "volumeMode"} {
			if _, declared := targetSpec[field]; !declared {
				if value, ok := originalSpec[field].(string); ok {
					targetSpec[field] = value
				}
			}
		}
		if !managedLateVolumeClaimTemplatesEqual(original, target) {
			return block("maintained Fleet Valkey claim layout or size differs from the original allocation")
		}

		want["volumeClaimTemplates"] = deploymentplan.Clone(old)["volumeClaimTemplates"]
		if want["podManagementPolicy"] == nil && old["podManagementPolicy"] != nil {
			want["podManagementPolicy"] = old["podManagementPolicy"]
		}
	}
	return nil
}
