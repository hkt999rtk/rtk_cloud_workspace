package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

func lkeSQLitePVCEnabled(env map[string]string, workloadKey string) bool {
	key := ""
	switch workloadKey {
	case "cloud-admin":
		key = "LKE_CLOUD_ADMIN_SQLITE_PVC_ENABLED"
	case "frontend":
		key = "LKE_FRONTEND_SQLITE_PVC_ENABLED"
	default:
		return false
	}
	return strings.EqualFold(strings.TrimSpace(lkeEnvValue(env, key)), "true")
}

func lkeSQLitePVCName(workloadKey string) string {
	return workloadKey + "-sqlite-data"
}

func lkeSQLitePVCManifest(env map[string]string, workload lkeWorkload) string {
	return fmt.Sprintf(`apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: %s
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  storageClassName: linode-block-storage-retain
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: %s
`, lkeSQLitePVCName(workload.Key), workload.Namespace, workload.Name, env["CLOUD_STACK_NAME"],
		firstNonEmpty(lkeEnvValue(env, "LKE_"+strings.ToUpper(strings.ReplaceAll(workload.Key, "-", "_"))+"_SQLITE_STORAGE"), "5Gi"))
}

func lkeProtectedSQLiteEnvironment(env map[string]string) bool {
	name := strings.ToLower(strings.TrimSpace(env["CLOUD_ENV_NAME"]))
	if name == "" {
		stack := strings.ToLower(strings.TrimSpace(env["CLOUD_STACK_NAME"]))
		switch {
		case strings.HasSuffix(stack, "-staging"):
			name = "staging"
		case strings.HasSuffix(stack, "-prod"), strings.HasSuffix(stack, "-production"):
			name = "prod"
		}
	}
	return name == "staging" || name == "prod" || name == "production"
}

func lkePreventSQLitePVCDisable(env map[string]string, workload lkeWorkload) error {
	deployment, err := lkeSQLiteDeploymentJSON(workload)
	if err != nil {
		return fmt.Errorf("inspect %s Deployment before SQLite PVC configuration change: %w", workload.Name, err)
	}
	if deployment == nil {
		return nil
	}
	if lkeProtectedSQLiteEnvironment(env) {
		return fmt.Errorf("%s in %s has an existing container-layer SQLite database; enable and verify its SQLite PVC migration before protected rollout", workload.Name, env["CLOUD_STACK_NAME"])
	}
	spec, _ := deployment["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	podSpec, _ := template["spec"].(map[string]any)
	volumes, _ := podSpec["volumes"].([]any)
	for _, raw := range volumes {
		volume, _ := raw.(map[string]any)
		if volume["name"] == "sqlite-data" {
			return fmt.Errorf("%s already uses a SQLite data PVC; keep its LKE_*_SQLITE_PVC_ENABLED setting true", workload.Name)
		}
	}
	return nil
}

func lkeRequireCloudAdminImagePVC(env map[string]string, deployment map[string]any) error {
	if !lkeProtectedSQLiteEnvironment(env) {
		return nil
	}
	if !lkeSQLitePVCEnabled(env, "cloud-admin") {
		return fmt.Errorf("protected Cloud Admin image rollout requires LKE_CLOUD_ADMIN_SQLITE_PVC_ENABLED=true")
	}
	spec, _ := deployment["spec"].(map[string]any)
	if spec["replicas"] != float64(1) {
		return fmt.Errorf("protected Cloud Admin image rollout requires one SQLite writer")
	}
	strategy, _ := spec["strategy"].(map[string]any)
	if strategy["type"] != "Recreate" {
		return fmt.Errorf("protected Cloud Admin image rollout requires Recreate strategy")
	}
	template, _ := spec["template"].(map[string]any)
	podSpec, _ := template["spec"].(map[string]any)
	volumes, _ := podSpec["volumes"].([]any)
	claimFound := false
	for _, raw := range volumes {
		volume, _ := raw.(map[string]any)
		if volume["name"] != "sqlite-data" {
			continue
		}
		claim, _ := volume["persistentVolumeClaim"].(map[string]any)
		claimFound = claim["claimName"] == lkeSQLitePVCName("cloud-admin")
	}
	containers, _ := podSpec["containers"].([]any)
	mountFound := false
	for _, raw := range containers {
		container, _ := raw.(map[string]any)
		if container["name"] != "app" {
			continue
		}
		mounts, _ := container["volumeMounts"].([]any)
		for _, item := range mounts {
			mount, _ := item.(map[string]any)
			if mount["name"] == "sqlite-data" && mount["mountPath"] == "/app/data" {
				mountFound = true
			}
		}
	}
	if !claimFound || !mountFound {
		return fmt.Errorf("protected Cloud Admin image rollout requires the migrated cloud-admin-sqlite-data PVC mounted at /app/data")
	}
	return nil
}

func lkeSQLiteDeploymentJSON(workload lkeWorkload) (map[string]any, error) {
	body, err := kubectlCombinedOutput(nil, "-n", workload.Namespace, "get", "deployment", workload.Name, "-o", "json", "--ignore-not-found=true")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(body)) == "" {
		return nil, nil
	}
	var deployment map[string]any
	if err := json.Unmarshal(body, &deployment); err != nil {
		return nil, fmt.Errorf("decode %s Deployment: %w", workload.Name, err)
	}
	return deployment, nil
}

// The annotation is an operator's verified-copy attestation, not a substitute
// for a quiescent copy, SQLite integrity check and retained rollback archive.
func lkeRequireSQLitePVCMigration(workload lkeWorkload) error {
	deployment, err := lkeSQLiteDeploymentJSON(workload)
	if err != nil {
		return fmt.Errorf("inspect existing %s Deployment before SQLite PVC switch: %w", workload.Name, err)
	}
	if deployment == nil {
		return nil // Fresh installation has no container-layer database to preserve.
	}
	spec, _ := deployment["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	podSpec, _ := template["spec"].(map[string]any)
	volumes, _ := podSpec["volumes"].([]any)
	for _, raw := range volumes {
		volume, _ := raw.(map[string]any)
		if volume["name"] != "sqlite-data" {
			continue
		}
		claim, _ := volume["persistentVolumeClaim"].(map[string]any)
		if claim["claimName"] == lkeSQLitePVCName(workload.Key) {
			return nil // Reconciliation of an already migrated Deployment.
		}
		return fmt.Errorf("existing %s Deployment uses a different SQLite data volume", workload.Name)
	}

	podsRaw, err := kubectlCombinedOutput(nil, "-n", workload.Namespace, "get", "pods", "-l", "app.kubernetes.io/name="+workload.Name, "-o", "json")
	if err != nil {
		return fmt.Errorf("inspect %s source Pod: %w", workload.Name, err)
	}
	var pods struct {
		Items []struct {
			Metadata struct {
				UID string `json:"uid"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(podsRaw, &pods); err != nil || len(pods.Items) != 1 || pods.Items[0].Metadata.UID == "" {
		return fmt.Errorf("expected exactly one identifiable %s SQLite source Pod", workload.Name)
	}
	pvc, err := kubectlResourceJSON(workload.Namespace, "pvc", lkeSQLitePVCName(workload.Key))
	if err != nil {
		return fmt.Errorf("create and populate the %s SQLite migration PVC before switching storage: %w", workload.Name, err)
	}
	metadata, _ := pvc["metadata"].(map[string]any)
	annotations, _ := metadata["annotations"].(map[string]any)
	copyHash, _ := annotations["rtk.realtek.com/sqlite-copy-sha256"].(string)
	decodedHash, decodeErr := hex.DecodeString(copyHash)
	if annotations["rtk.realtek.com/sqlite-source-pod-uid"] != pods.Items[0].Metadata.UID || decodeErr != nil || len(decodedHash) != 32 {
		return fmt.Errorf("%s SQLite PVC lacks verified data-copy annotations for the current source Pod", workload.Name)
	}
	status, _ := pvc["status"].(map[string]any)
	if status["phase"] != "Bound" {
		return fmt.Errorf("%s SQLite migration PVC is not bound", workload.Name)
	}
	return nil
}
