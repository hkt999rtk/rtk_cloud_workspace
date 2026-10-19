package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestUnifiedProgressSeparatesPreflightQualificationFromExecutionRechecks(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, action := range []string{"preflight", "upgrade"} {
			t.Run(environment+"/"+action, func(t *testing.T) {
				compiled, deps, writes := unifiedCLIProfile(t, environment)
				calls := map[string]int{}
				// An ordinary update has six gates; recovery/activation belong
				// to their separate lifecycle operations.
				gateIDs := deploymentplan.RequiredGateIDs(false)[:6]
				checks := func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
					checks := map[string]deploymentplan.GateCheck{}
					for _, id := range gateIDs {
						checks[id] = func(context.Context, *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
							calls[id]++
							return deploymentplan.GateEvidence{SHA256: deploymentplan.Digest("isolated round evidence")}, nil
						}
					}
					return checks
				}
				deps.Checks = checks
				args := []string{"--environment", environment}
				round := "preflight"
				selectedGates := gateIDs
				if action == "upgrade" {
					args = append(args, "--confirm", "video-cloud-"+environment, "--plan-sha256", compiled.Plan.SHA256())
					round = "execution"
					selectedGates = []string{"deployment.target", "deployment.execution", "deployment.pki", "deployment.compatibility", "deployment.network"}
					deps.LiveChecks = checks
					deps.Checks = func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
						t.Fatal("deployment repeated full preflight qualification")
						return nil
					}
					deps.Compile = func(context.Context, deploymentConfig, string) (*compiledEnvironmentDeployment, error) {
						t.Fatal("deployment recompiled its qualified plan")
						return nil, nil
					}
				}
				var out, diagnostics bytes.Buffer
				if err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), action, args, &out, &diagnostics, deps); err != nil {
					t.Fatal("qualification progress changed the public flow", err, diagnostics.String())
				}
				for _, id := range selectedGates {
					if calls[id] != 1 {
						t.Fatal("required check did not run exactly once", id, calls[id])
					}
					stage := id
					if action == "upgrade" {
						stage = "deployment.execution.recheck." + strings.TrimPrefix(id, "deployment.")
					}
					starts := 0
					for _, line := range strings.Split(out.String(), "\n") {
						fields := strings.Fields(line)
						if len(fields) >= 2 && fields[0] == "[RUNNING]" && fields[1] == stage && (action == "upgrade" || strings.Contains(" "+line+" ", " round="+round+" ")) {
							starts++
						}
					}
					if starts != 1 {
						t.Fatal("gate starts are duplicated or check phases are indistinguishable", stage, round, starts, out.String())
					}
				}
				if action == "upgrade" && (calls["deployment.release"] != 0 || strings.Contains(out.String(), "round=preflight")) {
					t.Fatal("deployment reran preflight proof gates", calls, out.String())
				}
				if *writes != map[string]int{"preflight": 0, "upgrade": 1}[action] || strings.Contains(out.String(), "[OK]") || strings.Contains(out.String(), "elapsed=") {
					t.Fatal("progress changed execution or restored unnecessary success/timing rows", *writes, out.String())
				}
			})
		}
	}
}
