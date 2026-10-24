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
