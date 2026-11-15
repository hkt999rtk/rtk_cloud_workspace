package main

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A whole-environment continuation observes an already completed retained Job
// using the same environment-selected source boundary as the original writer.
// Neither a missing version nor a replaced original Job can be read as success.
func TestConfiguredContinuationReadsCompletedSchemaWithoutReplayingJob(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"complete", "missing-version", "foreign-job"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				f := newSchemaOwnerFixture(t, environment)
				f.setState(t, true, 9)
				actual := deploymentplan.Clone(f.job.Object)
				uid := "33333333-3333-4333-8333-333333333333"
				deploymentplan.Map(actual["metadata"])["uid"] = uid
				actual["status"] = deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
				if mode == "missing-version" {
					writeTestFile(t, f.missing, `["002_required.sql"]`)
				}
				if mode == "foreign-job" {
					deploymentplan.Map(actual["metadata"])["uid"] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
				}
				raw, _ := json.Marshal(actual)
				writeTestFile(t, f.jobPath, string(raw))
				kube, _ := yaml.Marshal(frozenKubernetesFixture(t))
				t.Setenv("RTK_CLOUD_CONFIG_ROOT", f.store.ConfigRoot)
				f.cfg.Values["DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS"] = "1"
				f.cfg.Values["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"] = "3600"
				backend, err := newEnvironmentKubernetesBackendForConfig(string(kube), f.cfg)
				if err != nil {
					t.Fatal(err)
				}
				resource := deploymentplan.Resource{ID: deploymentplan.ID(f.job.Object), Desired: f.job.Object, Action: "retain", Phase: "migration", UID: uid}
				if err := backend.Wait(t.Context(), resource); (err == nil) != (mode == "complete") {
					t.Fatal("retained completed schema readback", err)
				}
			})
		}
	}
}
