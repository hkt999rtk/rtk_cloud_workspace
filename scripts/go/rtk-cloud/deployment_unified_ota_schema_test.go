package main

import (
	"context"
	"encoding/json"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestOTASchemaChecksOriginalDatabaseWithoutFutureWorkloadReadiness(t *testing.T) {
	for _, mode := range []string{"valid", "missing-table", "missing-column", "foreign-schema", "malformed", "changed-db-owner", "changed-pod", "related-owner-migration", "unrelated-retained-migration", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			config, _ := yaml.Marshal(frozenKubernetesFixture(t))
			backend, err := newEnvironmentKubernetesBackend(string(config), "dev")
			if err != nil {
				t.Fatal(err)
			}
			store := backend.Store
			if err := store.write("kube/kubeconfig.yaml", []byte("must-not-be-consumed"), true); err != nil {
				t.Fatal(err)
			}
			_, ota := otaStartupFixture(t, "dev")
			plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "upgrade"}, Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(ota), Action: "update", UID: "original-ota", Desired: ota}, {ID: "StatefulSet/video-cloud-dev-platform/postgresql", Action: "retain", UID: "original-database"}}}
			if mode == "related-owner-migration" {
				plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: "Job/video-cloud-dev-video-cloud/ota-owner-migrate", Phase: "migration", Action: "create", Desired: deploymentplan.Object{"kind": "Job", "metadata": deploymentplan.Object{"namespace": "video-cloud-dev-video-cloud", "name": "ota-owner-migrate"}}})
			}
			if mode == "unrelated-retained-migration" {
				plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: "Job/video-cloud-dev-support/zammad-init", Phase: "migration", Action: "retain", Desired: deploymentplan.Object{"kind": "Job", "metadata": deploymentplan.Object{"namespace": "video-cloud-dev-support", "name": "zammad-init"}}})
			}
			body := map[string]any{"schema": "public", "tables": []string{}, "columns": []string{}}
			if mode == "missing-table" {
				body["tables"] = []string{"ota_releases"}
			}
			if mode == "missing-column" {
				body["columns"] = []string{"ota_artifact_grants.delivery_channel"}
			}
			if mode == "foreign-schema" {
				body["schema"] = "other"
			}
			raw, _ := json.Marshal(body)
			if mode == "malformed" {
				raw = []byte("private-canary")
			}
			calls := deploymentRuntimeTestKubectl(t, "selected=\nwhile test $# -gt 0; do if test \"$1\" = --kubeconfig; then shift; selected=\"$1\"; break; fi; shift; done\ncase \"$(cat \"$selected\")\" in *private-frozen-token*) :;; *) exit 99;; esac\nprintf '%s' '"+string(raw)+"'\n")
			bin := t.TempDir()
			if err := os.Symlink(os.Getenv("RTK_CLOUD_KUBECTL"), filepath.Join(bin, "kubectl")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			reads := 0
			read := func(_ context.Context, id string) (deploymentplan.Object, error) {
				reads++
				if strings.HasPrefix(id, "Deployment/") {
					t.Fatal("schema qualification depended on future OTA workload", id)
				}
				if strings.HasPrefix(id, "StatefulSet/") {
					uid := "original-database"
					if mode == "changed-db-owner" {
						uid = "replacement"
					}
					return deploymentplan.Object{"kind": "StatefulSet", "metadata": deploymentplan.Object{"uid": uid}}, nil
				}
				uid := "original-pod"
				if mode == "changed-pod" && reads > 2 {
					uid = "replacement"
				}
				return deploymentplan.Object{"kind": "Pod", "metadata": deploymentplan.Object{"uid": uid, "ownerReferences": []any{deploymentplan.Object{"kind": "StatefulSet", "uid": "original-database", "controller": true}}}, "status": deploymentplan.Object{"phase": "Running", "conditions": []any{deploymentplan.Object{"type": "Ready", "status": "False"}}}}, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			err = checkEnvironmentOTASchema(ctx, plan, store, read, func(ctx context.Context, args ...string) ([]byte, error) { return backend.command(ctx, nil, args...) })
			pass := mode == "valid" || mode == "unrelated-retained-migration"
			if (err == nil) != pass {
				t.Fatal("original schema prerequisite decision wrong", mode, err)
			}
			if err != nil && strings.Contains(err.Error(), "private-canary") {
				t.Fatal("schema error leaked private diagnostic", err)
			}
			if mode == "related-owner-migration" && reads != 0 {
				t.Fatal("candidate owner migration was mistaken for a preexisting-table prerequisite")
			}
			if pass && deploymentRuntimeCallCount(t, calls) != 1 {
				t.Fatal("read-only SQL retried or did not execute once")
			}
			assertPlanAccessPrivateFilesRemoved(t, backend)
		})
	}
	query := otaSchemaQuery()
	if !strings.HasPrefix(query, "BEGIN TRANSACTION READ ONLY;") || !strings.Contains(query, "statement_timeout='10s'") || strings.Contains(query, "CREATE ") || strings.Contains(query, "INSERT ") {
		t.Fatal("OTA schema query writes or lacks deadline")
	}
}

func TestOTASchemaHonorsParentDeadline(t *testing.T) {
	config, _ := yaml.Marshal(frozenKubernetesFixture(t))
	backend, err := newEnvironmentKubernetesBackend(string(config), "dev")
	if err != nil {
		t.Fatal(err)
	}
	store := backend.Store
	_, ota := otaStartupFixture(t, "dev")
	plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Operation: "upgrade"}, Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(ota), Desired: ota}, {ID: "StatefulSet/video-cloud-dev-platform/postgresql", UID: "original-database"}}}
	deploymentRuntimeTestKubectl(t, "sleep 3\n")
	bin := t.TempDir()
	if err := os.Symlink(os.Getenv("RTK_CLOUD_KUBECTL"), filepath.Join(bin, "kubectl")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	read := func(_ context.Context, id string) (deploymentplan.Object, error) {
		if strings.HasPrefix(id, "StatefulSet/") {
			return deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "original-database"}}, nil
		}
		return deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "original-pod", "ownerReferences": []any{deploymentplan.Object{"kind": "StatefulSet", "uid": "original-database", "controller": true}}}}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := checkEnvironmentOTASchema(ctx, plan, store, read, func(ctx context.Context, args ...string) ([]byte, error) { return backend.command(ctx, nil, args...) }); err == nil || time.Since(started) > 2*time.Second {
		t.Fatal("original database read ignored parent deadline", err)
	}
}
