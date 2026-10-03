package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/postgresbackup"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

func (m *postgresBackupManager) sourceSQL(ctx context.Context, sql string) ([]byte, error) {
	c := m.Config
	return m.kube(ctx, strings.NewReader(sql), "-n", c.Namespace, "exec", "-i", c.SourcePod, "-c", c.SourceContainer, "--", "psql", "-X", "-U", "postgres", "-d", "postgres", "-At", "-v", "ON_ERROR_STOP=1", "-f", "-")
}

func (m *postgresBackupManager) validateSourcePod(ctx context.Context) error {
	c := m.Config
	b, err := m.kube(ctx, nil, "-n", c.Namespace, "get", "pod", c.SourcePod, "-o", "json")
	if err != nil {
		return err
	}
	var pod struct {
		Spec struct {
			Containers []struct{ Name, Image string }
			Volumes    []struct{ PersistentVolumeClaim *struct{ ClaimName string } }
		}
		Status struct {
			ContainerStatuses []struct {
				Name    string
				ImageID string `json:"imageID"`
				Ready   bool
			}
		}
	}
	if json.Unmarshal(b, &pod) != nil {
		return errors.New("invalid source Pod inventory")
	}
	imageOK, pvcOK := false, false
	for _, container := range pod.Status.ContainerStatuses {
		_, actualDigest, ok := strings.Cut(container.ImageID, "@sha256:")
		_, expectedDigest, _ := strings.Cut(c.Worker.Source.PostgresImage, "@sha256:")
		if container.Name == c.SourceContainer && container.Ready && ok && actualDigest == expectedDigest {
			imageOK = true
		}
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == c.SourcePVC {
			pvcOK = true
		}
	}
	if !imageOK || !pvcOK {
		return errors.New("source PostgreSQL image or PVC differs from reviewed config")
	}
	return nil
}

func (m *postgresBackupManager) validateSource(ctx context.Context) error {
	c := m.Config
	if err := m.validateSourcePod(ctx); err != nil {
		return err
	}
	b, err := m.kube(ctx, nil, "-n", c.Namespace, "get", "pvc", c.SourcePVC, "-o", "json")
	if err != nil {
		return err
	}
	var pvc struct {
		Status struct{ Capacity map[string]string }
	}
	if json.Unmarshal(b, &pvc) != nil {
		return errors.New("invalid source PVC capacity")
	}
	size, err := postgresBackupQuantityBytes(pvc.Status.Capacity["storage"])
	if err != nil {
		return err
	}
	scratch, _ := postgresBackupStorageBytes(c.ScratchStorage)
	if scratch < size*3 {
		return errors.New("scratch PVC must be at least three times the source PVC capacity")
	}
	b, err = m.sourceSQL(ctx, "SELECT current_setting('server_version_num')::int / 10000, system_identifier::text, current_setting('wal_level'), current_setting('max_wal_senders'), current_setting('max_replication_slots') FROM pg_control_system();\n")
	if err != nil {
		return err
	}
	fields := strings.Split(strings.TrimSpace(string(b)), "|")
	if len(fields) != 5 || fields[0] != "16" || fields[1] != c.Worker.Source.SystemIdentifier || fields[2] != "replica" && fields[2] != "logical" {
		return errors.New("source PostgreSQL major, system identifier or WAL level differs from required settings")
	}
	senders, _ := strconv.Atoi(fields[3])
	slots, _ := strconv.Atoi(fields[4])
	if senders < 2 || slots < 1 {
		return errors.New("backup requires at least two WAL senders and one replication slot; change source settings explicitly before activation")
	}
	return nil
}

func postgresBackupQuantityBytes(v string) (int64, error) {
	for _, unit := range []struct {
		suffix     string
		multiplier int64
	}{{"Gi", 1 << 30}, {"Mi", 1 << 20}, {"Ki", 1 << 10}, {"", 1}} {
		if unit.suffix != "" && !strings.HasSuffix(v, unit.suffix) {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSuffix(v, unit.suffix), 10, 64)
		if err == nil && n > 0 && n < (1<<60)/unit.multiplier {
			return n * unit.multiplier, nil
		}
	}
	return 0, errors.New("unrecognized source PVC storage capacity")
}

func (m *postgresBackupManager) configure(ctx context.Context, enable bool, qualification string) (resultErr error) {
	c := m.Config
	release, err := recovery.AcquireClusterLock(ctx, lkeKubectl(), m.Kubeconfig, c.Namespace, "postgres-configure-"+postgresBackupRandomID(), true)
	if err != nil {
		return err
	}
	defer func() {
		if err := release(); err != nil && resultErr == nil {
			resultErr = err
		}
	}()
	if err := m.validateSource(ctx); err != nil {
		return err
	}
	if err := m.validateSourceNetworkPolicy(ctx); err != nil {
		return err
	}
	password, err := m.Store.readRuntime("postgres-backup")
	if os.IsNotExist(err) {
		var b [32]byte
		if _, err = rand.Read(b[:]); err != nil {
			return err
		}
		password = hex.EncodeToString(b[:])
		err = m.Store.write("runtime/postgres-backup", []byte(password), false)
	}
	if err != nil || password == "" {
		return errors.New("dedicated postgres-backup runtime secret unavailable")
	}
	credentials := map[string]string{"source-password": password}
	for _, key := range []string{"ACCESS_KEY_ID", "SECRET_ACCESS_KEY"} {
		value, err := m.Store.read(filepath.Join("operator", "env", "RTK_POSTGRES_BACKUP_"+key))
		if err != nil || value == "" {
			return fmt.Errorf("dedicated RTK_POSTGRES_BACKUP_%s credential unavailable", key)
		}
		credentials[key] = value
	}
	if _, err = m.sourceSQL(ctx, "DO $backup$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='rtk_postgres_backup') THEN CREATE ROLE rtk_postgres_backup; END IF; END $backup$;\nALTER ROLE rtk_postgres_backup LOGIN REPLICATION NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT CONNECTION LIMIT 2 PASSWORD '"+strings.ReplaceAll(password, "'", "''")+"';\nGRANT CONNECT ON DATABASE postgres TO rtk_postgres_backup;\n"); err != nil {
		return err
	}
	hba, err := m.sourceSQL(ctx, "SELECT pg_read_file(current_setting('hba_file'));\n")
	if err != nil {
		return err
	}
	updated, err := postgresBackupHBA(string(hba))
	if err != nil {
		return err
	}
	// The supported layout has a fixed HBA file; never trust a config-supplied shell path.
	path, err := m.sourceSQL(ctx, "SHOW hba_file;\n")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(path)) != "/var/lib/postgresql/data/pgdata/pg_hba.conf" {
		return errors.New("custom PostgreSQL HBA location requires a reviewed adapter")
	}
	if _, err = m.kube(ctx, strings.NewReader(updated), "-n", c.Namespace, "exec", "-i", c.SourcePod, "-c", c.SourceContainer, "--", "sh", "-ceu", `p=/var/lib/postgresql/data/pgdata/pg_hba.conf; test -f "$p"; test ! -L "$p"; umask 077; t="$p.rtk-backup-tmp"; trap 'rm -f "$t"' EXIT; cat > "$t"; chown postgres:postgres "$t"; mv "$t" "$p"`); err != nil {
		return err
	}
	b, err := m.sourceSQL(ctx, "SELECT count(*) FROM pg_hba_file_rules WHERE error IS NOT NULL;\nSELECT pg_reload_conf();\n")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(b)) != "0\nt" {
		return errors.New("PostgreSQL HBA validation/reload failed")
	}
	objects := postgresBackupObjects(c, false)
	secret := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": postgresBackupMetadata(c, postgresBackupName+"-credentials"), "type": "Opaque", "stringData": credentials}
	if err = m.apply(ctx, secret); err != nil {
		return err
	}
	if err = m.apply(ctx, objects...); err != nil {
		return err
	}
	status, statusErr := postgresBackupReadStatus(ctx, m)
	if statusErr != nil {
		return statusErr
	}
	if status.Version == 0 {
		status = postgresbackup.Status{Version: 1, Environment: c.Environment, Stack: c.Stack, ClusterID: c.Worker.ClusterID, GeneratedAt: time.Now().UTC()}
		if err = m.writeStatus(ctx, status); err != nil {
			return err
		}
	}
	// Scheduling starts only after a published backup and successful isolated drill.
	if enable {
		if err := postgresBackupValidateQualification(c, qualification, status); err != nil {
			return err
		}
		if status.Latest == nil || status.LatestDrill == nil || !status.LatestDrill.Success || status.LatestDrill.ClusterID != c.Worker.ClusterID || status.LatestDrill.SystemIdentifier != c.Worker.Source.SystemIdentifier {
			return errors.New("schedule remains suspended: complete a manual backup and successful isolated restore drill before enabling it")
		}
		b, _ := json.Marshal(map[string]any{"spec": map[string]any{"suspend": false}})
		if _, err = m.kube(ctx, bytes.NewReader(b), "-n", c.Namespace, "patch", "cronjob", postgresBackupName, "--type=merge", "--patch-file=/dev/stdin"); err != nil {
			return err
		}
	}
	fmt.Printf("PostgreSQL backup configured: environment=%s schedule_enabled=%t; include scratch PVC %s-scratch as an explicit exclusion in core recovery inventory.\n", c.Environment, enable, postgresBackupName)
	return nil
}

func postgresBackupHBA(existing string) (string, error) {
	const begin = "# BEGIN RTK POSTGRES BACKUP"
	const end = "# END RTK POSTGRES BACKUP"
	if strings.Count(existing, begin) != strings.Count(existing, end) || strings.Count(existing, begin) > 1 {
		return "", errors.New("ambiguous managed backup HBA block")
	}
	if a := strings.Index(existing, begin); a >= 0 {
		b := strings.Index(existing, end)
		if b < a {
			return "", errors.New("invalid backup HBA block")
		}
		existing = existing[:a] + existing[b+len(end):]
	}
	return strings.TrimRight(existing, "\r\n") + "\n\n" + begin + "\nhost replication rtk_postgres_backup all scram-sha-256\n" + end + "\n", nil
}

func postgresBackupObjects(c postgresBackupDeployment, enabled bool) []map[string]any {
	workerJSON, _ := json.Marshal(c)
	object := func(version, kind, name string, body map[string]any) map[string]any {
		body["apiVersion"] = version
		body["kind"] = kind
		body["metadata"] = postgresBackupMetadata(c, name)
		return body
	}
	roleRules := []any{
		map[string]any{"apiGroups": []string{""}, "resources": []string{"pods"}, "verbs": []string{"get"}, "resourceNames": []string{c.SourcePod}},
		map[string]any{"apiGroups": []string{""}, "resources": []string{"configmaps"}, "verbs": []string{"get", "delete"}, "resourceNames": []string{"rtk-core-recovery-command"}},
		map[string]any{"apiGroups": []string{""}, "resources": []string{"configmaps"}, "verbs": []string{"get"}, "resourceNames": []string{"rtk-core-recovery"}},
		map[string]any{"apiGroups": []string{""}, "resources": []string{"configmaps"}, "verbs": []string{"get", "update", "patch"}, "resourceNames": []string{postgresBackupStatusName}},
		map[string]any{"apiGroups": []string{""}, "resources": []string{"configmaps"}, "verbs": []string{"create"}},
	}
	pvcSpec := map[string]any{"accessModes": []string{"ReadWriteOnce"}, "volumeMode": "Filesystem", "resources": map[string]any{"requests": map[string]string{"storage": c.ScratchStorage}}}
	if c.StorageClass != "" {
		pvcSpec["storageClassName"] = c.StorageClass
	}
	objects := []map[string]any{
		object("v1", "ServiceAccount", postgresBackupName, map[string]any{}),
		object("rbac.authorization.k8s.io/v1", "Role", postgresBackupName, map[string]any{"rules": roleRules}),
		object("rbac.authorization.k8s.io/v1", "RoleBinding", postgresBackupName, map[string]any{"subjects": []any{map[string]string{"kind": "ServiceAccount", "name": postgresBackupName, "namespace": c.Namespace}}, "roleRef": map[string]string{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": postgresBackupName}}),
		object("v1", "ConfigMap", postgresBackupName+"-config", map[string]any{"data": map[string]string{"config.json": string(workerJSON)}}),
		object("v1", "PersistentVolumeClaim", postgresBackupName+"-scratch", map[string]any{"spec": pvcSpec}),
		object("networking.k8s.io/v1", "NetworkPolicy", postgresBackupName+"-source", map[string]any{"spec": map[string]any{"podSelector": map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/name": "postgresql"}}, "policyTypes": []string{"Ingress"}, "ingress": []any{map[string]any{"from": []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/name": postgresBackupName}}}}, "ports": []any{map[string]any{"protocol": "TCP", "port": 5432}}}}}}),
		object("batch/v1", "CronJob", postgresBackupName, map[string]any{"spec": map[string]any{"schedule": c.schedule(), "timeZone": c.timeZone(), "suspend": !enabled, "concurrencyPolicy": "Forbid", "startingDeadlineSeconds": 3600, "successfulJobsHistoryLimit": 2, "failedJobsHistoryLimit": 3, "jobTemplate": map[string]any{"spec": postgresBackupJobSpec(c, "run", "")}}}),
	}
	return objects
}

func postgresBackupJobSpec(c postgresBackupDeployment, action, id string) map[string]any {
	args := []string{"postgres-backup-worker", action, "--config", "/etc/postgres-backup/config.json"}
	if id != "" {
		args = append(args, "--id", id)
	}
	return map[string]any{"backoffLimit": 0, "activeDeadlineSeconds": 14400, "ttlSecondsAfterFinished": 604800, "template": map[string]any{"metadata": map[string]any{"labels": postgresBackupLabels(c)}, "spec": map[string]any{
		"serviceAccountName": postgresBackupName, "automountServiceAccountToken": true, "restartPolicy": "Never", "terminationGracePeriodSeconds": 60, "nodeSelector": map[string]string{"rtk.io/node-class": "general"}, "securityContext": map[string]any{"fsGroup": 70, "fsGroupChangePolicy": "OnRootMismatch"}, "imagePullSecrets": []any{map[string]string{"name": c.imagePullSecret()}},
		"initContainers": []any{map[string]any{"name": "scratch-permissions", "image": c.RunnerImage, "command": []string{"sh", "-ceu", "mkdir -p /scratch/data; chown 70:70 /scratch/data; chmod 0700 /scratch/data"}, "securityContext": map[string]any{"runAsUser": 0, "allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []string{"ALL"}, "add": []string{"CHOWN", "FOWNER", "DAC_OVERRIDE"}}}, "volumeMounts": []any{map[string]string{"name": "scratch", "mountPath": "/scratch"}}}},
		"containers":     []any{map[string]any{"name": "backup", "image": c.RunnerImage, "command": []string{"/usr/local/bin/rtk-cloud"}, "args": args, "securityContext": map[string]any{"runAsUser": 70, "runAsGroup": 70, "runAsNonRoot": true, "readOnlyRootFilesystem": true, "allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []string{"ALL"}}}, "resources": map[string]any{"requests": map[string]string{"cpu": "100m", "memory": "256Mi"}, "limits": map[string]string{"cpu": "500m", "memory": "512Mi"}}, "env": postgresBackupCredentialEnv(postgresBackupName + "-credentials"), "volumeMounts": []any{map[string]string{"name": "scratch", "mountPath": "/backup", "subPath": "data"}, map[string]any{"name": "config", "mountPath": "/etc/postgres-backup", "readOnly": true}, map[string]any{"name": "credentials", "mountPath": "/run/postgres-backup", "readOnly": true}, map[string]any{"name": "tmp", "mountPath": "/tmp"}}}},
		"volumes":        []any{map[string]any{"name": "tmp", "emptyDir": map[string]any{"medium": "Memory", "sizeLimit": "64Mi"}}, map[string]any{"name": "scratch", "persistentVolumeClaim": map[string]string{"claimName": postgresBackupName + "-scratch"}}, map[string]any{"name": "config", "configMap": map[string]string{"name": postgresBackupName + "-config"}}, map[string]any{"name": "credentials", "secret": map[string]any{"secretName": postgresBackupName + "-credentials", "defaultMode": 288, "items": []any{map[string]string{"key": "source-password", "path": "source-password"}}}}},
	}}}
}

func postgresBackupCredentialEnv(secret string) []any {
	out := []any{map[string]string{"name": "HOME", "value": "/tmp"}}
	for _, key := range []string{"ACCESS_KEY_ID", "SECRET_ACCESS_KEY"} {
		out = append(out, map[string]any{"name": "RTK_BACKUP_" + key, "valueFrom": map[string]any{"secretKeyRef": map[string]string{"name": secret, "key": key}}})
	}
	return out
}

func (m *postgresBackupManager) createJob(ctx context.Context, action, id string) error {
	if action == "run" {
		if err := m.validateSource(ctx); err != nil {
			return err
		}
	}
	name := postgresBackupName + "-" + strings.ReplaceAll(action, "-", "") + "-" + time.Now().UTC().Format("20060102-150405") + "-" + postgresBackupRandomID()[:6]
	job := map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": postgresBackupMetadata(m.Config, name), "spec": postgresBackupJobSpec(m.Config, action, id)}
	b, _ := json.Marshal(job)
	if _, err := m.kube(ctx, bytes.NewReader(b), "create", "-f", "-"); err != nil {
		return err
	}
	fmt.Printf("Created Job %s/%s; use postgres-backup status and kubectl logs for completion.\n", m.Config.Namespace, name)
	return nil
}

func postgresBackupReadStatus(ctx context.Context, m *postgresBackupManager) (postgresbackup.Status, error) {
	var status postgresbackup.Status
	b, err := m.kube(ctx, nil, "-n", m.Config.Namespace, "get", "configmap", postgresBackupStatusName, "--ignore-not-found", "-o", "json")
	if err != nil {
		return status, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return status, nil
	}
	var object struct {
		Data map[string]string `json:"data"`
	}
	if json.Unmarshal(b, &object) != nil || json.Unmarshal([]byte(object.Data["status.json"]), &status) != nil {
		return status, errors.New("invalid backup status ConfigMap")
	}
	if status.Version != 1 || status.Environment != m.Config.Environment || status.Stack != m.Config.Stack || status.ClusterID != m.Config.Worker.ClusterID {
		return status, errors.New("backup status does not belong to the configured environment and cluster")
	}
	return status, nil
}
