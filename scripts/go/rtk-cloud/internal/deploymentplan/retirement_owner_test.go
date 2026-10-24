package deploymentplan

import (
	"errors"
	"testing"
)

func TestExplicitRetirementOwnerCannotRebindOrRetireChangedRecipe(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"original", "api-defaults", "replacement", "image-drift", "command-drift", "selector-drift", "unbound-retirement"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				in, definition, live := fixture(t, environment, "Deployment")
				definition.Retire, definition.Phase = true, "retirement"
				Map(definition.Object["metadata"])["uid"] = "owner"
				container := Map(Map(Map(definition.Object["spec"])["template"])["spec"])["containers"].([]any)[0].(map[string]any)
				container["command"] = []any{"/bin/sh", "-ec", "exec sleep 300"}
				container["env"] = []any{Object{"name": "EMPTY", "value": ""}}
				container["volumeMounts"] = []any{Object{"name": "original-state", "mountPath": "/var/lib/state", "readOnly": false}}
				live = Clone(definition.Object)
				Map(live["metadata"])["resourceVersion"] = "7"
				live["status"] = Object{"readyReplicas": float64(1)}
				currentContainer := Map(Map(Map(live["spec"])["template"])["spec"])["containers"].([]any)[0].(map[string]any)
				blockerID := ""
				switch mode {
				case "api-defaults":
					currentContainer["imagePullPolicy"] = "IfNotPresent"
					currentContainer["terminationMessagePath"] = "/dev/termination-log"
					delete(Map(currentContainer["env"].([]any)[0]), "value")
					delete(Map(currentContainer["volumeMounts"].([]any)[0]), "readOnly")
				case "replacement":
					Map(live["metadata"])["uid"] = "replacement"
					blockerID = "deployment.ownership"
				case "image-drift":
					currentContainer["image"] = "registry.example/replacement@sha256:changed"
					blockerID = "deployment.retirement"
				case "command-drift":
					currentContainer["command"] = []any{"/bin/sh", "-ec", "exec other-workload"}
					blockerID = "deployment.retirement"
				case "selector-drift":
					Map(Map(Map(live["spec"])["selector"])["matchLabels"])["app"] = "replacement"
					blockerID = "deployment.retirement"
				case "unbound-retirement":
					delete(Map(definition.Object["metadata"]), "uid")
					Map(live["metadata"])["uid"] = "replacement"
					currentContainer["image"] = "registry.example/replacement@sha256:changed"
				}
				beforeDefinition, beforeLive := Digest(definition.Object), Digest(live)
				plan, err := Compile(in, []Definition{definition}, []Object{live})
				if Digest(definition.Object) != beforeDefinition || Digest(live) != beforeLive {
					t.Fatal("retirement planning mutated its reviewed definition or inventory")
				}
				if blockerID != "" {
					var blocker *Blocker
					if !errors.As(err, &blocker) || blocker.ID != blockerID || blocker.Resource != ID(live) || plan != nil {
						t.Fatal("changed retirement owner or recipe was not blocked", err)
					}
					return
				}
				if err != nil || plan == nil || len(plan.Resources) != 1 {
					t.Fatal("valid retirement failed", err)
				}
				resource := plan.Resources[0]
				if resource.Action != "retire" || resource.UID != Map(live["metadata"])["uid"] || Digest(resource.Baseline) != Digest(Projection(live)) {
					t.Fatal("retirement lost its exact original UID or inventory baseline")
				}
				if _, exists := Map(resource.Desired["metadata"])["uid"]; exists {
					t.Fatal("server UID remained in the desired resource body")
				}
				body, err := resource.Body(live)
				if err != nil || Map(body["metadata"])["uid"] != resource.UID || Map(body["metadata"])["resourceVersion"] != "7" {
					t.Fatal("retirement lost its execution-time UID/version fence", err)
				}
			})
		}
	}
}
