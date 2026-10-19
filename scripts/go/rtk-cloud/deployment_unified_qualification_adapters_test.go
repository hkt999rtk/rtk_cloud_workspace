package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCompleteQualificationTimeoutDefaults(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		target, image := deploymentUpgradeFixture(t)
		target.Metadata.Namespace = "video-cloud-" + environment + "-billing"
		raw, _ := json.Marshal(target)
		manifest := filepath.Join(t.TempDir(), "simulator.json")
		if err := os.WriteFile(manifest, raw, 0600); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name string
			args []string
			want time.Duration
		}{
			{"complete", []string{"--phase", "pre-deploy"}, defaultCompleteDeploymentTimeout},
			{"create", []string{"--phase", "pre-deploy", "--operation", "create"}, defaultCompleteDeploymentTimeout},
			{"health", nil, 10 * time.Minute},
			{"image-upgrade", []string{"--phase", "pre-deploy", "--operation", "image-upgrade", "--image", image, "--manifest", manifest}, 10 * time.Minute},
			{"fast-health", []string{"--phase", "post-deploy", "--fast"}, 2 * time.Minute},
			{"explicit", []string{"--phase", "pre-deploy", "--timeout", "3m"}, 3 * time.Minute},
		} {
			t.Run(environment+"/"+tc.name, func(t *testing.T) {
				o, err := parseDeploymentCheckOptions(append([]string{"--environment", environment}, tc.args...), io.Discard)
				if err != nil || o.timeout != tc.want {
					t.Fatalf("timeout=%s want=%s err=%v", o.timeout, tc.want, err)
				}
			})
		}
		for _, args := range [][]string{nil, {"--timeout", "3m"}} {
			o, err := parseUnifiedDeploymentOptions("preflight", append([]string{"--environment", environment}, args...), io.Discard)
			want := defaultCompleteDeploymentTimeout
			if len(args) > 0 {
				want = 3 * time.Minute
			}
			if err != nil || o.Timeout != want {
				t.Fatalf("direct options=%+v err=%v", o, err)
			}
		}
	}
}

func TestPreflightRejectsRemovedFastOptionBeforeAnyWork(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, option := range []string{"--fast", "-fast", "--fast=true", "--fast=false", "-fast=false"} {
			t.Run(environment+"/"+option, func(t *testing.T) {
				for _, operation := range []string{"full-deployment", "create", "image-upgrade"} {
					args := []string{"--environment", environment, "--phase", "pre-deploy", "--operation", operation, option}
					deps := deploymentCheckDependencies{guard: func([]string) error {
						t.Fatal("removed option reached operator state")
						return nil
					}, resolve: func(string, string, string) (deploymentConfig, error) {
						t.Fatal("removed option reached configuration")
						return deploymentConfig{}, nil
					}}
					if err := runDeploymentCheckWithDependencies(t.Context(), args, io.Discard, io.Discard, deps); err != exitCode(2) {
						t.Fatalf("operation=%s err=%v", operation, err)
					}
				}
				for _, operation := range []string{"create", "upgrade"} {
					args := []string{"--environment", environment, "--operation", operation, option}
					deps := unifiedDeploymentDependencies{Guard: func([]string) error {
						t.Fatal("removed option reached operator state")
						return nil
					}, Resolve: func(string, string, string) (deploymentConfig, error) {
						t.Fatal("removed option reached configuration")
						return deploymentConfig{}, nil
					}}
					if err := runUnifiedEnvironmentDeploymentWithDependencies(t.Context(), "preflight", args, io.Discard, io.Discard, deps); err != exitCode(2) {
						t.Fatalf("direct operation=%s err=%v", operation, err)
					}
				}
			})
		}
	}
}

func TestCompleteSimulatorQualificationSelectsWorkloadInsteadOfSameNamedService(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			target, image := deploymentUpgradeFixture(t)
			target.Metadata.Namespace = "video-cloud-" + environment + "-billing"
			raw, _ := json.Marshal(target)
			var object deploymentplan.Object
			if err := json.Unmarshal(raw, &object); err != nil {
				t.Fatal(err)
			}
			workload := deploymentplan.Resource{ID: deploymentplan.ID(object), Action: "update", Desired: object}
			service := deploymentplan.Object{"kind": "Service", "metadata": map[string]any{"name": "payment-simulator", "namespace": target.Metadata.Namespace}, "spec": map[string]any{"ports": []any{map[string]any{"port": float64(8080)}}}}
			resources := []deploymentplan.Resource{{ID: deploymentplan.ID(service), Action: "update", Desired: service}, workload}
			selected := 0
			for _, resource := range resources {
				candidate, err := environmentPaymentSimulatorTarget(resource)
				if err != nil {
					t.Fatal(err)
				}
				if candidate == nil {
					continue
				}
				selected++
				container, err := deploymentUpgradeSimulatorContainer(*candidate)
				if err != nil || container["image"] != image {
					t.Fatalf("candidate lost executable: %v", err)
				}
				if err := verifyDeploymentSimulatorMigrationSetting(secretStore{}, *candidate); err != nil {
					t.Fatal(err)
				}
			}
			if selected != 1 {
				t.Fatalf("qualified %d executables", selected)
			}
			workload.Action = "retire"
			if candidate, err := environmentPaymentSimulatorTarget(workload); candidate != nil || err != nil {
				t.Fatal("retired simulator selected", err)
			}
		})
	}
}

func TestSimulatorQualificationRejectsMalformedTargetsWithoutPanic(t *testing.T) {
	for _, mode := range []string{"service", "missing-template", "missing-pod", "missing-containers", "invalid-container"} {
		t.Run(mode, func(t *testing.T) {
			target, _ := deploymentUpgradeFixture(t)
			switch mode {
			case "service":
				target.Kind = "Service"
			case "missing-template":
				delete(target.Spec, "template")
			case "missing-pod":
				delete(target.Spec["template"].(map[string]any), "spec")
			case "missing-containers":
				delete(target.Spec["template"].(map[string]any)["spec"].(map[string]any), "containers")
			case "invalid-container":
				target.Spec["template"].(map[string]any)["spec"].(map[string]any)["containers"] = []any{"private-invalid-spec"}
			}
			if _, err := deploymentUpgradeSimulatorContainer(target); err == nil || strings.Contains(err.Error(), "private-invalid-spec") {
				t.Fatal("malformed simulator accepted or leaked", err)
			}
			if mode == "service" {
				return
			}
			raw, _ := json.Marshal(target)
			var object deploymentplan.Object
			_ = json.Unmarshal(raw, &object)
			_, err := environmentPaymentSimulatorTarget(deploymentplan.Resource{ID: deploymentplan.ID(object), Action: "update", Desired: object})
			var blocker *deploymentplan.Blocker
			if !errors.As(err, &blocker) || blocker.ID != "deployment.compatibility.simulator" {
				t.Fatal("missing safe simulator diagnosis", err)
			}
		})
	}
}
