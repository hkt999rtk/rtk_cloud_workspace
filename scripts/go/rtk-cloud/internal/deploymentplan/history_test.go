package deploymentplan

import "testing"

func TestTerminalStandaloneJobHistoryIsPreservedWithoutWeakeningInventory(t *testing.T) {
	for _, env := range []string{"dev", "staging", "prod"} {
		t.Run(env, func(t *testing.T) {
			for _, mode := range []string{"completed", "failed", "active", "partial", "running-pod", "foreign", "wrong-uid", "scheduled", "standalone-completed", "standalone-failed", "standalone-running", "standalone-restart"} {
				t.Run(mode, func(t *testing.T) {
					in, def, current := fixture(t, env, "Deployment")
					ns := in.Stack + "-platform"
					job := Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": Object{"name": "historical-check", "namespace": ns, "uid": "job-owner"}, "status": Object{"conditions": []any{Object{"type": "Complete", "status": "True"}}}}
					pod := Object{"apiVersion": "v1", "kind": "Pod", "metadata": Object{"name": "historical-pod", "namespace": ns, "uid": "pod-owner", "ownerReferences": []any{Object{"kind": "Job", "apiVersion": "batch/v1", "name": "historical-check", "uid": "job-owner", "controller": true}}}, "status": Object{"phase": "Succeeded"}}
					if len(mode) > 10 && mode[:10] == "standalone" {
						delete(Map(pod["metadata"]), "ownerReferences")
						pod["spec"] = Object{"restartPolicy": "Never"}
					}
					switch mode {
					case "standalone-failed":
						Map(pod["status"])["phase"] = "Failed"
					case "standalone-running":
						Map(pod["status"])["phase"] = "Running"
					case "standalone-restart":
						Map(pod["spec"])["restartPolicy"] = "Always"
					case "failed":
						Map(job["status"])["conditions"] = []any{Object{"type": "Failed", "status": "True"}}
						Map(pod["status"])["phase"] = "Failed"
					case "active":
						Map(job["status"])["active"] = float64(1)
					case "partial":
						Map(job["status"])["conditions"] = []any{Object{"type": "SuccessCriteriaMet", "status": "True"}}
					case "running-pod":
						Map(pod["status"])["phase"] = "Running"
					case "foreign":
						Map(job["metadata"])["labels"] = Object{"rtk.realtek.com/stack": "foreign"}
					case "wrong-uid":
						Map(Map(pod["metadata"])["ownerReferences"].([]any)[0])["uid"] = "another-owner"
					case "scheduled":
						Map(job["metadata"])["ownerReferences"] = []any{Object{"kind": "CronJob", "controller": true, "uid": "scheduler"}}
					}
					objects := []Object{current, job, pod}
					plan, err := Compile(in, []Definition{def}, objects)
					pass := mode == "completed" || mode == "failed" || mode == "standalone-completed" || mode == "standalone-failed"
					if (err == nil) != pass {
						t.Fatal("wrong history qualification", mode, err)
					}
					basePlan, err := Compile(in, []Definition{def}, []Object{current})
					if err != nil {
						t.Fatal(err)
					}
					if (basePlan.CheckWorkloadInventory(objects) == nil) != pass {
						t.Fatal("execution inventory differs", mode)
					}
					if pass && len(plan.Resources) != 1 {
						t.Fatal("historical job became an execution target")
					}
				})
			}
		})
	}
}
