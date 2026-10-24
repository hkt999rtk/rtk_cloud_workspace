package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func qualifiedEnvironmentBaselineFixture(t *testing.T, environment string) (*deploymentplan.Plan, map[string]deploymentplan.Object) {
	t.Helper()
	stack := "video-cloud-" + environment
	meta := func(name, uid string) deploymentplan.Object {
		return deploymentplan.Object{"namespace": stack + "-platform", "name": name, "uid": uid, "resourceVersion": "1", "labels": deploymentplan.Object{"rtk.realtek.com/stack": stack}}
	}
	secret := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": meta("original-identity", "secret-owner"), "data": deploymentplan.Object{"tls.crt": "private-baseline-canary"}}
	configuration := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": meta("original-policy", "configuration-owner"), "data": deploymentplan.Object{"policy": "original"}}
	service := deploymentplan.Object{"apiVersion": "v1", "kind": "Service", "metadata": meta("existing", "service-owner"), "spec": deploymentplan.Object{"type": "ClusterIP", "ports": []any{deploymentplan.Object{"name": "http", "port": float64(80)}}}}
	updated := deploymentplan.Projection(service)
	deploymentplan.Map(updated["spec"])["ports"] = []any{deploymentplan.Object{"name": "http", "port": float64(81)}}
	created := deploymentplan.Object{"apiVersion": "v1", "kind": "Service", "metadata": deploymentplan.Object{"namespace": stack + "-platform", "name": "new"}, "spec": deploymentplan.Object{"type": "ClusterIP"}}
	live := []deploymentplan.Object{secret, configuration, service}
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: stack, Operation: "upgrade", ConfigurationSHA256: "configuration", ReleaseSHA256: "release", BindingsSHA256: "bindings"}, []deploymentplan.Definition{
		{Object: deploymentplan.Projection(secret), Phase: "configuration", Retain: true},
		{Object: deploymentplan.Projection(configuration), Phase: "configuration", Retain: true},
		{Object: updated, Phase: "configuration"},
		{Object: created, Phase: "configuration"},
	}, live)
	if err != nil {
		t.Fatal(err)
	}
	observed := map[string]deploymentplan.Object{}
	for _, object := range live {
		observed[deploymentplan.ID(object)] = object
	}
	return plan, observed
}

func TestQualifiedEnvironmentConsumptionChecksAllOriginalBaselines(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "status-only", "secret-drift", "configuration-drift", "workload-drift", "owner-drift", "missing-retained", "create-collision", "outside-inventory", "read-blocker", "cancelled"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				plan, observed := qualifiedEnvironmentBaselineFixture(t, environment)
				stack := plan.Stack
				secretID := "Secret/" + stack + "-platform/original-identity"
				policyID := "ConfigMap/" + stack + "-platform/original-policy"
				serviceID := "Service/" + stack + "-platform/existing"
				createdID := "Service/" + stack + "-platform/new"
				original := deploymentplan.Clone(observed[secretID])
				switch mode {
				case "status-only":
					observed[serviceID]["status"] = deploymentplan.Object{"phase": "changed"}
					deploymentplan.Map(observed[serviceID]["metadata"])["resourceVersion"] = "2"
				case "secret-drift":
					deploymentplan.Map(observed[secretID]["data"])["tls.crt"] = "replacement"
				case "configuration-drift":
					deploymentplan.Map(observed[policyID]["data"])["policy"] = "changed"
				case "workload-drift":
					deploymentplan.Map(observed[serviceID]["spec"])["type"] = "NodePort"
				case "owner-drift":
					deploymentplan.Map(observed[secretID]["metadata"])["uid"] = "replacement-owner"
				case "missing-retained", "outside-inventory", "read-blocker":
					delete(observed, secretID)
				case "create-collision":
					observed[createdID] = deploymentplan.Object{"kind": "Service", "metadata": deploymentplan.Object{"namespace": stack + "-platform", "name": "new", "uid": "unqualified-owner"}}
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "cancelled" {
					cancel()
				}
				reads := map[string]int{}
				reader := func(ctx context.Context, id string) (deploymentplan.Object, error) {
					reads[id]++
					if mode == "outside-inventory" && id == secretID {
						return original, nil
					}
					if mode == "read-blocker" && id == secretID {
						return nil, &deploymentplan.Blocker{ID: "deployment.access.forbidden", Resource: id, Cause: "selected identity lacks original resource read access", Next: "restore the selected identity permission"}
					}
					return nil, nil
				}
				before := plan.SHA256()
				err := checkQualifiedEnvironmentBaselines(ctx, plan, observed, reader)
				wantSuccess := mode == "valid" || mode == "status-only" || mode == "outside-inventory"
				if (err == nil) != wantSuccess || plan.SHA256() != before {
					t.Fatal("wrong baseline outcome or changed qualified plan", err)
				}
				if err != nil && strings.Contains(err.Error(), "private-baseline-canary") {
					t.Fatal("baseline drift exposed private contents")
				}
				if mode == "cancelled" && (!errors.Is(err, context.Canceled) || len(reads) != 0) {
					t.Fatal("cancelled proof consumption still read resources", err, reads)
				}
				if mode == "read-blocker" {
					var blocker *deploymentplan.Blocker
					if !errors.As(err, &blocker) || blocker.ID != "deployment.access.forbidden" {
						t.Fatal("original read permission blocker was lost", err)
					}
				}
				if wantSuccess && (reads[createdID] != 1 || len(reads) != 1 && mode != "outside-inventory" || mode == "outside-inventory" && reads[secretID] != 1) {
					t.Fatal("current inventory was redundantly reread or missing planned owner was skipped", reads)
				}
			})
		}
	}
}

func TestExecutionConsumesFrozenReleaseEvidenceAndKeepsCurrentPrerequisites(t *testing.T) {
	checks := executionEnvironmentChecks(&compiledEnvironmentDeployment{})
	for _, id := range []string{"deployment.target", "deployment.execution", "deployment.pki", "deployment.compatibility", "deployment.network"} {
		if checks[id] == nil {
			t.Fatal("required immediate check unavailable", id)
		}
	}
	if len(checks) != 5 || checks["deployment.release"] != nil {
		t.Fatal("execution reruns frozen release qualification or drops current prerequisites")
	}
	for _, command := range []string{"gh", "docker", "helm"} {
		if _, err := localEnvironmentReleaseSourceCommand(context.Background(), command, "private-command-canary"); err == nil || strings.Contains(err.Error(), "private-command-canary") {
			t.Fatal("frozen release consumption allowed a nonlocal command or exposed arguments", command, err)
		}
	}
}

func TestExecutionPKICoversActualDeadlineAndMaintainedMargin(t *testing.T) {
	now := time.Now().UTC()
	for _, mode := range []string{"long-run", "short-run", "no-deadline", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			deadline := now.Add(90 * time.Minute)
			if mode == "short-run" {
				deadline = now.Add(15 * time.Minute)
			}
			if mode != "no-deadline" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, deadline)
				defer cancel()
				if mode == "cancelled" {
					cancel()
				}
			}
			cutoff, err := environmentExecutionPKICutoff(ctx, now, 3600)
			if mode == "no-deadline" || mode == "cancelled" {
				if err == nil {
					t.Fatal("unbounded or cancelled run passed PKI deadline check")
				}
				return
			}
			want := deadline
			if mode == "short-run" {
				want = now.Add(time.Hour)
			}
			if err != nil || !cutoff.Equal(want) {
				t.Fatal("PKI validity did not cover execution and maintained margin", cutoff, want, err)
			}
		})
	}
}

func TestQualifiedEnvironmentLoaderReusesFrozenPlanWithoutCIOrRendering(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "private-drift", "identity-drift", "baseline-drift", "source-drift", "invalidated"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				cfg, store, _, bindings := maintainedDeploymentSnapshotFixture(t, environment)
				stack := cfg.Values["CLOUD_STACK_NAME"]
				release := environmentRelease{Schema: "rtk-cloud.release/v1", Environment: environment, Stack: stack, WorkspaceRevision: strings.Repeat("a", 40), Images: map[string]environmentReleaseImage{"LKE_REDIS_IMAGE": {Reference: "docker.io/library/redis@sha256:" + strings.Repeat("b", 64)}}}
				object := deploymentplan.Object{"apiVersion": "v1", "kind": "Service", "metadata": deploymentplan.Object{"namespace": stack + "-platform", "name": "original", "uid": "service-owner", "resourceVersion": "1", "labels": deploymentplan.Object{"rtk.realtek.com/stack": stack}}, "spec": deploymentplan.Object{"type": "ClusterIP"}}
				bindings.Owners = map[string]string{deploymentplan.ID(object): "service-owner"}
				snapshot, err := readEnvironmentDeploymentSnapshot(cfg, store)
				if err != nil {
					t.Fatal(err)
				}
				backend, err := newEnvironmentKubernetesBackend(snapshot.Kubeconfig, environment)
				if err != nil {
					t.Fatal(err)
				}
				plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: stack, Operation: "upgrade", ConfigurationSHA256: snapshot.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(bindings)}, []deploymentplan.Definition{{Object: deploymentplan.Projection(object), Phase: "configuration", Retain: true}}, []deploymentplan.Object{object})
				if err != nil {
					t.Fatal(err)
				}
				plan.StorageSHA256 = deploymentplan.Digest([]environmentStorageBinding(nil))
				now := time.Now().UTC()
				window, err := environmentPKIValidityWindow(cfg.Values)
				if err != nil {
					t.Fatal(err)
				}
				c := &compiledEnvironmentDeployment{Plan: plan, Configuration: cfg, Store: store, Backend: backend, Release: release, Bindings: bindings, PKICheckedAt: now.Add(-2 * time.Second), PKIVerifiedUntil: now.Add(-2*time.Second + time.Duration(window)*time.Second)}
				qualification := deploymentplan.Qualification{PlanSHA256: plan.SHA256(), Decision: "GO"}
				for _, id := range deploymentplan.RequiredGateIDs(false)[:6] {
					qualification.Gates = append(qualification.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: plan.SHA256()})
				}
				c.PreflightGeneration, err = invalidateEnvironmentPreflightProofWithToken(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if err := publishEnvironmentPreflightBindings(cfg, c, "NO-GO"); err != nil {
					t.Fatal(err)
				}
				if _, err := saveEnvironmentPreflightProof(c, qualification, now.Add(-time.Second)); err != nil {
					t.Fatal("fixture could not publish complete proof", err)
				}
				directory := t.TempDir()
				writeJSON := func(name string, value any) {
					t.Helper()
					raw, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					writeTestFile(t, filepath.Join(directory, name), string(raw))
				}
				if mode == "baseline-drift" {
					deploymentplan.Map(object["spec"])["type"] = "NodePort"
				}
				writeJSON("inventory.json", map[string]any{"items": []deploymentplan.Object{object}})
				writeJSON("namespaces.json", map[string]any{"items": []deploymentplan.Object{{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": stack + "-platform", "uid": "namespace-owner"}}}})
				writeTestFile(t, filepath.Join(directory, "kubectl"), `#!/bin/sh
printf 'kubectl %s\n' "$*" >> "$RTK_CONSUMER_CALLS"
case "$*" in
 *'get namespace '*) cat "$RTK_CONSUMER_DIR/namespaces.json" ;;
 *'get deployment,'*) cat "$RTK_CONSUMER_DIR/inventory.json" ;;
 *'get clusterrole,'*|*'get validatingwebhookconfiguration '*) printf '{"items":[]}' ;;
 *) exit 2 ;;
esac
`)
				writeTestFile(t, filepath.Join(directory, "git"), `#!/bin/sh
printf 'git %s\n' "$*" >> "$RTK_CONSUMER_CALLS"
case "$*" in
 *'rev-parse HEAD') printf '%s\n' "$RTK_CONSUMER_REVISION" ;;
 *) exit 2 ;;
esac
`)
				for _, tool := range []string{"gh", "docker", "helm"} {
					writeTestFile(t, filepath.Join(directory, tool), "#!/bin/sh\nprintf 'forbidden immutable check\\n' >> \"$RTK_CONSUMER_CALLS\"\nexit 2\n")
				}
				for _, tool := range []string{"kubectl", "git", "gh", "docker", "helm"} {
					if err := os.Chmod(filepath.Join(directory, tool), 0755); err != nil {
						t.Fatal(err)
					}
				}
				t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(directory, "kubectl"))
				t.Setenv("RTK_CONSUMER_DIR", directory)
				t.Setenv("RTK_CONSUMER_CALLS", filepath.Join(directory, "calls"))
				t.Setenv("RTK_CONSUMER_REVISION", release.WorkspaceRevision)
				switch mode {
				case "private-drift":
					if err := store.write("runtime/postgres", []byte("private-consumer-canary"), true); err != nil {
						t.Fatal(err)
					}
				case "identity-drift":
					raw, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
					if err != nil {
						t.Fatal(err)
					}
					if err := store.write("kube/kubeconfig.yaml", []byte(strings.ReplaceAll(string(raw), "private-frozen-token", "private-consumer-canary")), true); err != nil {
						t.Fatal(err)
					}
				case "source-drift":
					t.Setenv("RTK_CONSUMER_REVISION", strings.Repeat("c", 40))
				case "invalidated":
					if err := invalidateEnvironmentPreflightProof(cfg); err != nil {
						t.Fatal(err)
					}
				}
				loaded, err := loadQualifiedEnvironmentDeployment(context.Background(), cfg, "upgrade", plan.SHA256())
				if (err == nil) != (mode == "valid") {
					t.Fatal("wrong frozen consumer outcome", err)
				}
				if err != nil && strings.Contains(err.Error(), "private-consumer-canary") {
					t.Fatal("consumer exposed changed private inputs")
				}
				if loaded != nil && (loaded.Plan.SHA256() != plan.SHA256() || deploymentplan.Digest(loaded.Release) != deploymentplan.Digest(release) || loaded.Proof == nil || loaded.RequirePreflightBindings) {
					t.Fatal("consumer changed or omitted frozen artifacts")
				}
				calls, _ := os.ReadFile(filepath.Join(directory, "calls"))
				if strings.Contains(string(calls), "forbidden immutable check") || strings.Contains(string(calls), "--dry-run") {
					t.Fatal("loader requalified immutable evidence or admitted resources")
				}
				if (mode == "private-drift" || mode == "identity-drift" || mode == "invalidated") && len(calls) != 0 {
					t.Fatal("known invalid proof triggered unnecessary live checks")
				}
			})
		}
	}
}
