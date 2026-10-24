package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestNativeControlledRetirementAndWaitUseExactOwnersAndAPIs(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			config := frozenKubernetesFixture(t)
			makeIsolatedTestSecretStore(t, environment)
			raw, _ := json.Marshal(config)
			backend, err := newEnvironmentKubernetesBackend(string(raw), environment)
			if err != nil {
				t.Fatal(err)
			}
			backend.RolloutTimeout = 90 * time.Second
			dir := t.TempDir()
			argsPath, bodyPath := filepath.Join(dir, "arguments"), filepath.Join(dir, "body")
			writeTestFile(t, filepath.Join(dir, "kubectl"), `#!/bin/sh
shift 3
printf '%s\n' "$*" > "$RTK_LIFECYCLE_ARGUMENTS"
cat > "$RTK_LIFECYCLE_BODY"
test "$RTK_LIFECYCLE_REJECT" != yes
`)
			if err := os.Chmod(filepath.Join(dir, "kubectl"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_LIFECYCLE_ARGUMENTS", argsPath)
			t.Setenv("RTK_LIFECYCLE_BODY", bodyPath)
			t.Setenv("RTK_LIFECYCLE_REJECT", "no")
			for _, route := range []struct{ kind, collection string }{
				{"Deployment", "apis/apps/v1/deployments"}, {"StatefulSet", "apis/apps/v1/statefulsets"}, {"DaemonSet", "apis/apps/v1/daemonsets"},
				{"Job", "apis/batch/v1/jobs"}, {"CronJob", "apis/batch/v1/cronjobs"},
				{"Pod", "api/v1/pods"}, {"Service", "api/v1/services"}, {"ServiceAccount", "api/v1/serviceaccounts"},
				{"Role", "apis/rbac.authorization.k8s.io/v1/roles"}, {"RoleBinding", "apis/rbac.authorization.k8s.io/v1/rolebindings"},
				{"ClusterRole", "apis/rbac.authorization.k8s.io/v1/clusterroles"}, {"ClusterRoleBinding", "apis/rbac.authorization.k8s.io/v1/clusterrolebindings"},
				{"NetworkPolicy", "apis/networking.k8s.io/v1/networkpolicies"}, {"Ingress", "apis/networking.k8s.io/v1/ingresses"}, {"IngressClass", "apis/networking.k8s.io/v1/ingressclasses"},
				{"ValidatingWebhookConfiguration", "apis/admissionregistration.k8s.io/v1/validatingwebhookconfigurations"},
				{"PodDisruptionBudget", "apis/policy/v1/poddisruptionbudgets"}, {"HorizontalPodAutoscaler", "apis/autoscaling/v2/horizontalpodautoscalers"},
			} {
				t.Run(route.kind, func(t *testing.T) {
					namespace := "video-cloud-" + environment + "-platform"
					if strings.HasPrefix(route.kind, "Cluster") || route.kind == "IngressClass" || route.kind == "ValidatingWebhookConfiguration" {
						namespace = ""
					}
					body := deploymentplan.Object{"kind": route.kind, "metadata": map[string]any{"name": "original", "namespace": namespace, "uid": "original-uid", "resourceVersion": "7"}}
					parts := strings.Split(route.collection, "/")
					collection := parts[len(parts)-1]
					prefix := "/" + strings.Join(parts[:len(parts)-1], "/") + "/"
					if namespace != "" {
						prefix += "namespaces/" + namespace + "/"
					}
					for _, dry := range []bool{true, false} {
						_, err := backend.Write(context.Background(), "retire", body, dry)
						path := prefix + collection + "/original"
						if dry {
							path += "?dryRun=All"
						}
						if err != nil || strings.TrimSpace(readTestFile(t, argsPath)) != "delete --raw="+path+" -f -" {
							t.Fatal("native retirement used a different owner route", err)
						}
						var options map[string]any
						if json.Unmarshal([]byte(readTestFile(t, bodyPath)), &options) != nil || options["kind"] != "DeleteOptions" || options["propagationPolicy"] != "Foreground" || deploymentplan.Map(options["preconditions"])["uid"] != "original-uid" || deploymentplan.Map(options["preconditions"])["resourceVersion"] != "7" {
							t.Fatal("native deletion lost UID/version preconditions")
						}
						if dry {
							values, ok := options["dryRun"].([]any)
							if !ok || len(values) != 1 || values[0] != "All" {
								t.Fatal("API server ignores query dryRun when DeleteOptions has a body; deletion must be dry-run in the body")
							}
						} else if _, present := options["dryRun"]; present {
							t.Fatal("approved deletion unexpectedly became a dry-run")
						}
					}
					t.Setenv("RTK_LIFECYCLE_REJECT", "yes")
					if _, err := backend.Write(context.Background(), "retire", body, false); err == nil {
						t.Fatal("native retirement ignored a rejected owner precondition")
					}
					t.Setenv("RTK_LIFECYCLE_REJECT", "no")
					resource := deploymentplan.Resource{ID: route.kind + "/" + namespace + "/original", Action: "retire"}
					if err := backend.Wait(context.Background(), resource); err != nil || !strings.Contains(readTestFile(t, argsPath), "wait --for=delete "+route.kind+"/original --timeout=90s") {
						t.Fatal("retirement completion was not awaited", err)
					}
				})
			}
			for _, kind := range []string{"Secret", "PersistentVolumeClaim", "Namespace", "ConfigMap"} {
				if _, err := backend.Write(context.Background(), "retire", deploymentplan.Object{"kind": kind}, true); err == nil {
					t.Fatal("ordinary retirement accepted durable or PKI lifecycle", kind)
				}
			}
			for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "Job", "PersistentVolumeClaim", "Service"} {
				resource := deploymentplan.Resource{ID: kind + "/video-cloud-" + environment + "-platform/original", Action: "update"}
				writeTestFile(t, argsPath, "no-call")
				var progressOutput, progressLog bytes.Buffer
				progress := &environmentProgress{out: &progressOutput, log: &progressLog, resources: -1}
				progressCtx := context.WithValue(context.Background(), environmentProgressKey{}, progress)
				environmentDeploymentProgress(progressCtx, "deployment.execution.verification")
				if err := backend.Wait(progressCtx, resource); err != nil {
					t.Fatal(err)
				}
				arguments := readTestFile(t, argsPath)
				if kind == "Service" {
					if arguments != "no-call" {
						t.Fatal("Service attempted an unsupported rollout watch")
					}
					continue
				}
				if !strings.Contains(progressOutput.String(), "[RUNNING] deployment.execution.rollout") || strings.Contains(progressOutput.String(), "[OK]") || !strings.Contains(progressLog.String(), "[OK] deployment.execution.rollout") || progress.stage != "deployment.execution.verification" {
					t.Fatal("native wait lost running work or its logged rollout outcome, or printed success", progressOutput.String(), progressLog.String())
				}
				if !strings.Contains(arguments, kind+"/original --timeout=90s") || kind == "Job" && !strings.Contains(arguments, "--for=condition=complete") || kind == "PersistentVolumeClaim" && !strings.Contains(arguments, "--for=jsonpath={.status.phase}=Bound") {
					t.Fatal("workload/data readiness used an incorrect native wait", arguments)
				}
				t.Setenv("RTK_LIFECYCLE_REJECT", "yes")
				progressOutput.Reset()
				progressLog.Reset()
				if err := backend.Wait(progressCtx, resource); err == nil {
					t.Fatal("native wait failure was ignored")
				}
				if !strings.Contains(progressOutput.String(), "[FAIL] deployment.execution.rollout") || !strings.Contains(progressLog.String(), "[FAIL] deployment.execution.rollout") || strings.Contains(progressOutput.String()+progressLog.String(), "[OK]") || progress.status != "FAIL" {
					t.Fatal("failed native wait stayed running or reported OK", progressOutput.String())
				}
				t.Setenv("RTK_LIFECYCLE_REJECT", "no")
			}
		})
	}
}
