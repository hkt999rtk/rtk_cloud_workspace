package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/postgresbackup"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

func (m *postgresBackupManager) drill(ctx context.Context, id, drillID, identity string) (resultErr error) {
	c := m.Config
	if !recovery.Name.MatchString(id) {
		return errors.New("drill requires a valid --id")
	}
	if drillID == "" {
		drillID = time.Now().UTC().Format("20060102-150405")
	}
	namespace, err := postgresBackupDrillNamespace(c, drillID)
	if err != nil {
		return err
	}
	info, err := os.Lstat(identity)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return errors.New("drill --identity must be a regular private 0600 age identity file")
	}
	key, err := os.ReadFile(identity)
	if err != nil {
		return errors.New("private restore identity unavailable")
	}
	secretData := map[string]string{"identity.agekey": string(key)}
	for _, name := range []string{"ACCESS_KEY_ID", "SECRET_ACCESS_KEY"} {
		value, err := m.Store.read(filepath.Join("operator", "env", "RTK_POSTGRES_RESTORE_"+name))
		if err != nil || value == "" {
			return fmt.Errorf("dedicated read-only RTK_POSTGRES_RESTORE_%s credential unavailable", name)
		}
		secretData[name] = value
	}
	remoteEnv, err := postgresBackupRemoteEnvironment(m.Store, false)
	if err != nil {
		return err
	}
	defer remoteEnv()
	release, err := recovery.AcquireClusterLock(ctx, lkeKubectl(), m.Kubeconfig, c.Namespace, "postgres-drill-"+drillID, true)
	if err != nil {
		return err
	}
	defer func() {
		if err := release(); err != nil && resultErr == nil {
			resultErr = err
		}
	}()
	labels := postgresBackupLabels(c)
	labels["rtk.realtek.com/purpose"] = "postgres-restore-drill"
	ns := map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": namespace, "labels": labels}}
	b, _ := json.Marshal(ns)
	// Namespace creation is exclusive: an old drill is never reused or overwritten.
	if _, err = m.kube(ctx, bytes.NewReader(b), "create", "-f", "-"); err != nil {
		return err
	}
	// Image-pull credentials are namespace-scoped; copy only this reviewed
	// registry Secret so private runner images also work in a fresh drill namespace.
	pullRaw, pullErr := m.kube(ctx, nil, "-n", c.Namespace, "get", "secret", c.imagePullSecret(), "-o", "json")
	if pullErr != nil {
		return pullErr
	}
	var pull struct {
		Type string            `json:"type"`
		Data map[string]string `json:"data"`
	}
	if json.Unmarshal(pullRaw, &pull) != nil || pull.Type != "kubernetes.io/dockerconfigjson" || pull.Data[".dockerconfigjson"] == "" {
		return errors.New("reviewed image pull Secret is invalid")
	}
	if err = m.apply(ctx, map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": c.imagePullSecret(), "namespace": namespace, "labels": labels}, "type": pull.Type, "data": pull.Data}); err != nil {
		return err
	}
	defer func() {
		_, _ = m.kube(context.WithoutCancel(ctx), nil, "-n", namespace, "delete", "secret", postgresBackupName+"-restore-credentials", "--ignore-not-found")
	}()
	objects := postgresBackupDrillObjects(c, namespace, id, secretData)
	if err = m.apply(ctx, objects...); err != nil {
		return err
	}
	result := postgresbackup.Drill{Version: 1, Environment: c.Environment, Stack: c.Stack, ClusterID: c.Worker.ClusterID, BackupID: id, SystemIdentifier: c.Worker.Source.SystemIdentifier, PostgresImage: c.Worker.Source.PostgresImage, Success: false}
	err = m.waitDrill(ctx, namespace)
	if err == nil {
		logs, logErr := m.kube(ctx, nil, "-n", namespace, "logs", "job/"+postgresBackupName+"-drill", "-c", "drill")
		if logErr != nil || json.Unmarshal(bytes.TrimSpace(logs), &result) != nil || !result.Success || result.Environment != c.Environment || result.Stack != c.Stack || result.ClusterID != c.Worker.ClusterID || result.BackupID != id || result.SystemIdentifier != c.Worker.Source.SystemIdentifier || result.PostgresImage != c.Worker.Source.PostgresImage {
			err = errors.New("drill completion evidence does not match selected backup")
		}
	}
	result.FinishedAt = time.Now().UTC()
	result.Success = err == nil
	if recordErr := postgresbackup.RecordDrill(context.WithoutCancel(ctx), c.Worker, result); recordErr != nil && err == nil {
		err = recordErr
	}
	status, statusErr := postgresbackup.GetStatus(context.WithoutCancel(ctx), c.Worker)
	if statusErr == nil {
		if prior, readErr := postgresBackupReadStatus(context.WithoutCancel(ctx), m); readErr == nil {
			status.LastAttempt = prior.LastAttempt
		}
		if writeErr := m.writeStatus(context.WithoutCancel(ctx), status); writeErr != nil && err == nil {
			err = writeErr
		}
	} else if err == nil {
		err = statusErr
	}
	fmt.Printf("Isolated PostgreSQL restore drill: namespace=%s backup=%s success=%t. Cleanup requires postgres-restore cleanup --drill-id %s.\n", namespace, id, result.Success, drillID)
	return err
}

func postgresBackupDrillNamespace(c postgresBackupDeployment, id string) (string, error) {
	if !postgresBackupDNSName.MatchString(id) || len(id) > 24 {
		return "", errors.New("drill-id must be a DNS label of at most 24 characters")
	}
	name := c.Stack + "-pgdrill-" + id
	if len(name) > 63 {
		return "", errors.New("drill namespace exceeds Kubernetes name length")
	}
	return name, nil
}

func postgresBackupDrillObjects(c postgresBackupDeployment, namespace, id string, secretData map[string]string) []map[string]any {
	meta := func(name string) map[string]any {
		v := postgresBackupMetadata(c, name)
		v["namespace"] = namespace
		return v
	}
	config, _ := json.Marshal(c)
	pvcSpec := map[string]any{"accessModes": []string{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]string{"storage": c.ScratchStorage}}}
	if c.StorageClass != "" {
		pvcSpec["storageClassName"] = c.StorageClass
	}
	jobSpec := postgresBackupJobSpec(c, "drill", id)
	template := jobSpec["template"].(map[string]any)
	spec := template["spec"].(map[string]any)
	spec["automountServiceAccountToken"] = false
	delete(spec, "serviceAccountName")
	container := spec["containers"].([]any)[0].(map[string]any)
	container["name"] = "drill"
	container["args"] = []string{"postgres-backup-worker", "drill", "--config", "/etc/postgres-backup/config.json", "--id", id, "--identity", "/run/postgres-restore/identity.agekey"}
	container["env"] = postgresBackupCredentialEnv(postgresBackupName + "-restore-credentials")
	container["volumeMounts"] = []any{map[string]string{"name": "scratch", "mountPath": "/backup", "subPath": "data"}, map[string]any{"name": "config", "mountPath": "/etc/postgres-backup", "readOnly": true}, map[string]any{"name": "credentials", "mountPath": "/run/postgres-restore", "readOnly": true}, map[string]any{"name": "tmp", "mountPath": "/tmp"}}
	spec["volumes"] = []any{map[string]any{"name": "tmp", "emptyDir": map[string]any{"medium": "Memory", "sizeLimit": "64Mi"}}, map[string]any{"name": "scratch", "persistentVolumeClaim": map[string]string{"claimName": postgresBackupName + "-scratch"}}, map[string]any{"name": "config", "configMap": map[string]string{"name": postgresBackupName + "-config"}}, map[string]any{"name": "credentials", "secret": map[string]any{"secretName": postgresBackupName + "-restore-credentials", "defaultMode": 288, "items": []any{map[string]string{"key": "identity.agekey", "path": "identity.agekey"}}}}}
	return []map[string]any{
		{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": meta("isolated-postgres-drill"), "spec": map[string]any{"podSelector": map[string]any{}, "policyTypes": []string{"Ingress", "Egress"}, "ingress": []any{}, "egress": []any{
			map[string]any{"to": []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]string{"kubernetes.io/metadata.name": "kube-system"}}}}, "ports": []any{map[string]any{"protocol": "UDP", "port": 53}, map[string]any{"protocol": "TCP", "port": 53}}},
			map[string]any{"to": []any{map[string]any{"ipBlock": map[string]any{"cidr": "0.0.0.0/0", "except": []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16"}}}}, "ports": []any{map[string]any{"protocol": "TCP", "port": 443}}},
		}}},
		{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": meta(postgresBackupName + "-scratch"), "spec": pvcSpec},
		{"apiVersion": "v1", "kind": "ConfigMap", "metadata": meta(postgresBackupName + "-config"), "data": map[string]string{"config.json": string(config)}},
		{"apiVersion": "v1", "kind": "Secret", "metadata": meta(postgresBackupName + "-restore-credentials"), "type": "Opaque", "stringData": secretData},
		{"apiVersion": "batch/v1", "kind": "Job", "metadata": meta(postgresBackupName + "-drill"), "spec": jobSpec},
	}
}

func (m *postgresBackupManager) waitDrill(ctx context.Context, namespace string) error {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Hour+5*time.Minute)
	defer cancel()
	for {
		b, err := m.kube(ctx, nil, "-n", namespace, "get", "job", postgresBackupName+"-drill", "-o", "json")
		if err != nil {
			return err
		}
		var job struct {
			Status struct {
				Conditions []struct{ Type, Status string }
			}
		}
		if json.Unmarshal(b, &job) != nil {
			return errors.New("invalid drill Job response")
		}
		for _, condition := range job.Status.Conditions {
			if condition.Status == "True" {
				if condition.Type == "Complete" {
					return nil
				}
				if condition.Type == "Failed" {
					return errors.New("isolated restore drill Job failed; inspect its restricted logs")
				}
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("isolated restore drill wait canceled or timed out")
		case <-time.After(5 * time.Second):
		}
	}
}

func (m *postgresBackupManager) cleanupDrill(ctx context.Context, id string) error {
	name, err := postgresBackupDrillNamespace(m.Config, id)
	if err != nil {
		return err
	}
	b, err := m.kube(ctx, nil, "get", "namespace", name, "-o", "json")
	if err != nil {
		return err
	}
	var ns struct {
		Metadata struct {
			UID    string
			Labels map[string]string
		}
	}
	if json.Unmarshal(b, &ns) != nil || ns.Metadata.UID == "" || ns.Metadata.Labels["rtk.realtek.com/stack"] != m.Config.Stack || ns.Metadata.Labels["rtk.realtek.com/environment"] != m.Config.Environment || ns.Metadata.Labels["rtk.realtek.com/purpose"] != "postgres-restore-drill" {
		return errors.New("namespace is not an owned isolated restore drill")
	}
	if !strings.HasPrefix(name, m.Config.Stack+"-pgdrill-") {
		return errors.New("refusing cleanup outside isolated drill namespace")
	}
	options, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": map[string]string{"uid": ns.Metadata.UID}})
	_, err = m.kube(ctx, bytes.NewReader(options), "delete", "--raw", "/api/v1/namespaces/"+name, "-f", "-")
	return err
}
