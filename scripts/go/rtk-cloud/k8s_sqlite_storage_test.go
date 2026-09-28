package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLKESQLitePVCManifestsAreOptInAndSingleWriter(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	for _, key := range []string{"cloud-admin", "frontend"} {
		workload := lkeWorkload{Key: key, Name: key, Namespace: lkeNamespaceName(env, key), Port: 8080, Image: "example.test/" + key}
		if strings.Contains(lkeDeploymentManifest(env, workload, nil), "sqlite-data") {
			t.Fatalf("%s SQLite PVC must remain opt-in", key)
		}
		envKey := "LKE_" + strings.ToUpper(strings.ReplaceAll(key, "-", "_")) + "_SQLITE_PVC_ENABLED"
		env[envKey] = "true"
		manifest := lkeDeploymentManifest(env, workload, nil)
		for _, want := range []string{"type: Recreate", "name: sqlite-data", "claimName: " + lkeSQLitePVCName(key), "fsGroupChangePolicy: OnRootMismatch"} {
			if !strings.Contains(manifest, want) {
				t.Fatalf("%s Deployment lacks %q", key, want)
			}
		}
		if key == "cloud-admin" && (!strings.Contains(manifest, "mountPath: /app/data") || !strings.Contains(manifest, "value: \"/app/data/rtk-cloud-admin.db\"") || !strings.Contains(manifest, "fsGroup: 999")) {
			t.Fatal("Cloud Admin SQLite path or volume ownership is wrong")
		}
		if key == "frontend" && (!strings.Contains(manifest, "mountPath: /data") || !strings.Contains(manifest, "fsGroup: 101")) {
			t.Fatal("frontend SQLite path or volume ownership is wrong")
		}
		pvc := lkeSQLitePVCManifest(env, workload)
		for _, want := range []string{"kind: PersistentVolumeClaim", "name: " + lkeSQLitePVCName(key), "storageClassName: linode-block-storage-retain", "accessModes: [\"ReadWriteOnce\"]", "storage: 5Gi"} {
			if !strings.Contains(pvc, want) {
				t.Fatalf("%s PVC lacks %q", key, want)
			}
		}
		delete(env, envKey)
	}
}

func TestLKESQLitePVCQuotaCountsEachSelectedClaim(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "LKE_FRONTEND_SQLITE_PVC_ENABLED": "true"}
	plan := lkeProviderServices(env, 1, provisionOptions{workloads: []string{"frontend"}})
	if plan.AdminSQLiteVolumes != 0 || plan.FrontendSQLiteVolumes != 1 {
		t.Fatalf("frontend-only PVC quota = (%d,%d)", plan.AdminSQLiteVolumes, plan.FrontendSQLiteVolumes)
	}
	env["LKE_CLOUD_ADMIN_SQLITE_PVC_ENABLED"] = "true"
	plan = lkeProviderServices(env, 1, provisionOptions{})
	if plan.AdminSQLiteVolumes != 1 || plan.FrontendSQLiteVolumes != 1 {
		t.Fatalf("full deployment PVC quota = (%d,%d)", plan.AdminSQLiteVolumes, plan.FrontendSQLiteVolumes)
	}
	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "kubeconfig")
	writeTestFile(t, kubeconfig, "test kubeconfig\n")
	kubectl := filepath.Join(dir, "kubectl")
	writeTestFile(t, kubectl, `#!/bin/sh
case "$*" in
  *"-n video-cloud-staging-frontend get pvc frontend-sqlite-data "*) printf 'Bound' ;;
esac
`)
	if err := os.Chmod(kubectl, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("RTK_CLOUD_KUBECONFIG", kubeconfig)
	sqliteOnly := lkeProviderServicePlan{AdminSQLiteVolumes: 1, FrontendSQLiteVolumes: 1}
	if got := lkeMissingPlannedVolumeServices(provisionPaths{EnvRoot: dir}, env, sqliteOnly); got != 1 {
		t.Fatalf("missing SQLite PVCs = %d, want only unbound Admin PVC", got)
	}
}

func TestLKESQLiteStorageSwitchRequiresVerifiedCurrentPod(t *testing.T) {
	dir := t.TempDir()
	kubectl := filepath.Join(dir, "kubectl")
	writeTestFile(t, kubectl, `#!/bin/sh
case "$*" in
  *"get deployment cloud-admin -o json"*) printf '%s\n' "$FAKE_SQLITE_DEPLOYMENT" ;;
  *"get pods -l app.kubernetes.io/name=cloud-admin -o json"*) printf '%s\n' "$FAKE_SQLITE_PODS" ;;
  *"get pvc cloud-admin-sqlite-data -o json"*) printf '%s\n' "$FAKE_SQLITE_PVC" ;;
  *) exit 1 ;;
esac
`)
	if err := os.Chmod(kubectl, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "1")
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	workload := lkeWorkload{Key: "cloud-admin", Name: "cloud-admin", Namespace: lkeNamespaceName(env, "admin")}
	t.Setenv("FAKE_SQLITE_DEPLOYMENT", `{"spec":{"template":{"spec":{"volumes":[]}}}}`)
	t.Setenv("FAKE_SQLITE_PODS", `{"items":[{"metadata":{"uid":"source-1"}}]}`)
	const copied = `{"metadata":{"annotations":{"rtk.realtek.com/sqlite-source-pod-uid":"source-1","rtk.realtek.com/sqlite-copy-sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},"status":{"phase":"Bound"}}`
	t.Setenv("FAKE_SQLITE_PVC", copied)
	if err := lkeRequireSQLitePVCMigration(workload); err != nil {
		t.Fatalf("verified copy rejected: %v", err)
	}
	t.Setenv("FAKE_SQLITE_PODS", `{"items":[{"metadata":{"uid":"replacement-pod"}}]}`)
	if err := lkeRequireSQLitePVCMigration(workload); err == nil || !strings.Contains(err.Error(), "current source Pod") {
		t.Fatalf("stale copy accepted: %v", err)
	}
	t.Setenv("FAKE_SQLITE_PODS", `{"items":[{"metadata":{"uid":"source-1"}}]}`)
	t.Setenv("FAKE_SQLITE_PVC", strings.Replace(copied, `"phase":"Bound"`, `"phase":"Pending"`, 1))
	if err := lkeRequireSQLitePVCMigration(workload); err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("unbound PVC accepted: %v", err)
	}
	t.Setenv("FAKE_SQLITE_DEPLOYMENT", `{"spec":{"template":{"spec":{"volumes":[{"name":"sqlite-data","persistentVolumeClaim":{"claimName":"cloud-admin-sqlite-data"}}]}}}}`)
	if err := lkePreventSQLitePVCDisable(workload); err == nil || !strings.Contains(err.Error(), "already uses a SQLite data PVC") {
		t.Fatalf("removal of a live SQLite PVC accepted: %v", err)
	}
	t.Setenv("FAKE_SQLITE_DEPLOYMENT", "")
	if err := lkeRequireSQLitePVCMigration(workload); err != nil {
		t.Fatalf("fresh installation rejected: %v", err)
	}
}
