package deploymentplan

import (
	"encoding/json"
	"testing"
)

func TestStatefulSetUpdatePreservesOriginalOmittedStorageClass(t *testing.T) {
	for _, env := range []string{"dev", "staging", "prod"} {
		for _, name := range []string{"postgresql", "fleet-valkey", "support-database"} {
			t.Run(env+"/"+name, func(t *testing.T) {
				in, definition, live := fixture(t, env, "StatefulSet")
				Map(definition.Object["metadata"])["name"], Map(live["metadata"])["name"] = name, name
				claim := Object{"metadata": Object{"name": "data"}, "spec": Object{"accessModes": []any{"ReadWriteOnce"}, "resources": Object{"requests": Object{"storage": "20Gi"}}, "volumeMode": "Filesystem"}}
				Map(definition.Object["spec"])["volumeClaimTemplates"] = []any{Clone(claim)}
				Map(claim["spec"])["storageClassName"] = "original-environment-retain"
				Map(live["spec"])["volumeClaimTemplates"] = []any{claim}
				Map(definition.Object["spec"])["replicas"] = float64(2)
				plan, err := Compile(in, []Definition{definition}, []Object{live})
				if err != nil {
					t.Fatal(err)
				}
				// A previously frozen, admitted input must remain byte-identical.
				raw, _ := json.Marshal(plan)
				var frozen Plan
				if err = json.Unmarshal(raw, &frozen); err != nil {
					t.Fatal(err)
				}
				before, original := frozen.SHA256(), Digest(live)
				body, err := frozen.Resources[0].Body(live)
				if err != nil {
					t.Fatal(err)
				}
				got := Map(Map(body["spec"])["volumeClaimTemplates"].([]any)[0])
				if Map(got["spec"])["storageClassName"] != "original-environment-retain" || Map(body["spec"])["replicas"] != float64(2) {
					t.Fatal("mutable update lost its original immutable storage allocation")
				}
				if frozen.SHA256() != before || Digest(live) != original {
					t.Fatal("materialization rewrote frozen intent or live input")
				}
				changed := Clone(live)
				Map(changed["metadata"])["uid"] = "replacement"
				if _, err = frozen.Resources[0].Body(changed); err == nil {
					t.Fatal("replacement owner accepted")
				}
				changed = Clone(live)
				Map(Map(Map(changed["spec"])["volumeClaimTemplates"].([]any)[0])["spec"])["storageClassName"] = "changed-allocation"
				if _, err = frozen.Resources[0].Body(changed); err == nil {
					t.Fatal("allocation drift accepted")
				}
				for _, explicit := range []string{"", "different-declared-class"} {
					r := frozen.Resources[0]
					r.Desired = Clone(r.Desired)
					Map(Map(Map(r.Desired["spec"])["volumeClaimTemplates"].([]any)[0])["spec"])["storageClassName"] = explicit
					b, err := r.Body(live)
					if err != nil {
						t.Fatal(err)
					}
					if Map(Map(Map(b["spec"])["volumeClaimTemplates"].([]any)[0])["spec"])["storageClassName"] != explicit {
						t.Fatal("explicit environment setting overwritten")
					}
				}
				fresh := frozen.Resources[0]
				fresh.Action, fresh.UID, fresh.Baseline = "create", "", nil
				created, err := fresh.Body(nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, exists := Map(Map(Map(created["spec"])["volumeClaimTemplates"].([]any)[0])["spec"])["storageClassName"]; exists {
					t.Fatal("fresh allocation selected a class outside environment intent")
				}
			})
		}
	}
}
