package main

import (
	"encoding/json"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func openBaoFirstQualificationFixture(t *testing.T, environment string) (deploymentConfig, secretStore, *environmentCreateExecution, map[string]string) {
	t.Helper()
	cfg, store, _, prior := createStateFixture(t, environment)
	operator := map[string]string{"LINODE_TOKEN": "isolated-only"}
	var plan environmentCreatePlan
	if err := json.Unmarshal(prior.Plan, &plan); err != nil {
		t.Fatal(err)
	}
	plan.OpenBaoFirst = true
	plan.OperatorSHA256 = deploymentplan.Digest(operator)
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	sha, err := environmentCreatePlanSHA256(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := prior.Qualification
	q.PlanSHA256 = sha
	generation, err := invalidateEnvironmentPreflightProofWithToken(cfg)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := publishEnvironmentCreateArtifact(t.Context(), cfg, generation, raw, plan.OperatorSHA256, q, prior.ReleaseProofs, prior.QualifiedAt)
	if err != nil {
		t.Fatal(err)
	}
	e, err := startEnvironmentCreateExecution(t.Context(), cfg, artifact, plan.OperatorSHA256, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return cfg, store, e, operator
}

func TestOpenBaoFirstQualificationRejectsUnknownInitialOutcomes(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			for _, mode := range []string{"unstarted", "foreign-config", "foreign-operator", "unrecorded-material", "pending-init", "downstream-before-openbao", "completed-journal"} {
				t.Run(mode, func(t *testing.T) {
					cfg, store, e, operator := openBaoFirstQualificationFixture(t, environment)
					b := environmentKubernetesBackend{Store: store}
					switch mode {
					case "foreign-config":
						cfg.Values["CLOUD_STACK_NAME"] = "video-cloud-foreign"
					case "foreign-operator":
						operator["LINODE_TOKEN"] = "foreign"
					case "unrecorded-material":
						if err := store.write("openbao/root-token", []byte("isolated-foreign"), false); err != nil {
							t.Fatal(err)
						}
					case "pending-init":
						if err := e.BeginEntry("openbao-initialize", "bootstrap", []byte(`{"synthetic":true}`), time.Now().UTC()); err != nil {
							t.Fatal(err)
						}
					case "downstream-before-openbao":
						if err := produceOpenBaoFirstTestMaterial(t, t.Context(), cfg, store, e, "management-transport-material"); err != nil {
							t.Fatal(err)
						}
					case "completed-journal":
						e.Journal.CompletedAt = time.Now().UTC()
						if err := e.save(); err != nil {
							t.Fatal(err)
						}
					}
					before := deploymentplan.Digest(e.Journal)
					q, err := qualifyOpenBaoFirstRecovery(t.Context(), cfg, store, e, b, operator)
					if mode == "unstarted" {
						if err != nil || q.OpenBao != "not-started" || q.Transport != "absent" || q.Management != "absent" || q.Signer != "absent" {
							t.Fatal("original untouched checkpoint rejected", err)
						}
					} else if err == nil {
						t.Fatal("foreign or uncertain checkpoint accepted")
					}
					if deploymentplan.Digest(e.Journal) != before {
						t.Fatal("preflight qualification modified parent journal")
					}
				})
			}
		})
	}
}
