package main

import (
	"encoding/json"
	"errors"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentPostgresProtection struct {
	Configuration postgresBackupDeployment
	Credentials   map[string]string
}

func environmentPostgresProtectionFromSnapshot(snapshot environmentDeploymentSnapshot) (environmentPostgresProtection, error) {
	var result environmentPostgresProtection
	c := snapshot.PostgresProtection
	if err := c.validate(); err != nil {
		return result, errors.New("deployment.recovery.postgres: a maintained exact-cluster postgres-backup.json is required")
	}
	password := snapshot.Secrets["postgres-backup"]
	if password == "" {
		return result, errors.New("deployment.recovery.postgres: bootstrap the dedicated backup role credential in the environment SecretStore")
	}
	result.Configuration, result.Credentials = c, map[string]string{"source-password": password}
	for _, key := range []string{"ACCESS_KEY_ID", "SECRET_ACCESS_KEY"} {
		value := snapshot.Operator["RTK_POSTGRES_BACKUP_"+key]
		if value == "" {
			return result, errors.New("deployment.recovery.postgres: dedicated environment archive credentials are unavailable")
		}
		result.Credentials[key] = value
	}
	return result, nil
}

// The same protection catalog is mandatory in every complete environment plan.
// This does not initialize a database role, enable an unqualified schedule or
// claim fresh backup evidence; those are separate managed stage/qualification checks.
func renderEnvironmentPostgresProtection(definitions []deploymentplan.Definition, protection environmentPostgresProtection, release environmentRelease) ([]deploymentplan.Definition, error) {
	c := protection.Configuration
	if err := c.validate(); err != nil {
		return nil, errors.New("deployment.recovery.postgres: invalid maintained protection profile")
	}
	if c.ScheduleEnabled == nil {
		return nil, errors.New("deployment.recovery.postgres: maintained profile must explicitly select schedule_enabled; no live-spec copying or implicit schedule reset")
	}
	if c.Worker.Source.PostgresImage != release.Images["LKE_POSTGRES_IMAGE"].Reference || c.RunnerImage != release.Images["LKE_POSTGRES_BACKUP_RUNNER_IMAGE"].Reference {
		return nil, errors.New("deployment.release.postgres: source and backup runner must match the selected immutable release")
	}
	for _, key := range []string{"source-password", "ACCESS_KEY_ID", "SECRET_ACCESS_KEY"} {
		if strings.TrimSpace(protection.Credentials[key]) == "" {
			return nil, errors.New("deployment.recovery.postgres: dedicated archive credentials are missing")
		}
	}
	objects := map[string]deploymentplan.Object{}
	for _, definition := range definitions {
		objects[deploymentplan.ID(definition.Object)] = definition.Object
	}
	source := objects["StatefulSet/"+c.Namespace+"/postgresql"]
	if source == nil {
		return nil, errors.New("deployment.recovery.postgres: source StatefulSet is absent from the common catalog")
	}
	spec := deploymentplan.Map(source["spec"])
	claims := certIssuerObjectList(spec["volumeClaimTemplates"])
	data := false
	for _, claim := range claims {
		if deploymentplan.String(deploymentplan.Map(deploymentplan.Map(claim)["metadata"])["name"]) == "data" {
			data = true
		}
	}
	if !data {
		return nil, errors.New("deployment.recovery.postgres: continuous protection requires the original durable data claim")
	}
	pod := deploymentplan.Map(deploymentplan.Map(spec["template"])["spec"])
	security := deploymentplan.Map(pod["securityContext"])
	if security == nil {
		security = deploymentplan.Object{}
		pod["securityContext"] = security
	}
	if group, present := security["fsGroup"]; present && group != float64(70) && group != 70 {
		return nil, errors.New("deployment.recovery.postgres: source fsGroup conflicts with the reviewed PostgreSQL runner")
	}
	security["fsGroup"], security["fsGroupChangePolicy"] = float64(70), "OnRootMismatch"
	var postgres deploymentplan.Object
	for _, item := range certIssuerObjectList(pod["containers"]) {
		candidate := deploymentplan.Map(item)
		if candidate["name"] == "postgres" {
			postgres = candidate
		}
	}
	if postgres == nil {
		return nil, errors.New("deployment.recovery.postgres: maintained source container missing")
	}
	command := "/opt/rtk-tools/rtk-cloud postgres-backup-worker archive-wal --config /etc/postgres-backup/config.json --wal-pgdata /var/lib/postgresql/data/pgdata --wal-name '%f' --wal-source '%p'"
	args := certIssuerObjectList(postgres["args"])
	for _, arg := range args {
		if value := deploymentplan.String(arg); strings.HasPrefix(value, "archive_") {
			return nil, errors.New("deployment.recovery.postgres: archive settings must have one maintained owner")
		}
	}
	postgres["args"] = append(args, "-c", "archive_mode=on", "-c", "archive_timeout=60s", "-c", "archive_command="+command)
	env := certIssuerObjectList(postgres["env"])
	for _, entry := range postgresBackupCredentialEnv(postgresBackupName + "-credentials") {
		setting := deploymentplan.Map(entry)
		for _, existing := range env {
			if deploymentplan.Map(existing)["name"] == setting["name"] {
				return nil, errors.New("deployment.recovery.postgres: archive environment binding conflicts with source configuration")
			}
		}
		env = append(env, entry)
	}
	postgres["env"] = append(env, deploymentplan.Object{"name": "RTK_POSTGRES_RUNNER_SOURCE_IMAGE", "value": c.Worker.Source.PostgresImage})
	volumes := []any{
		deploymentplan.Object{"name": "rtk-tools", "emptyDir": deploymentplan.Object{}},
		deploymentplan.Object{"name": "backup-config", "configMap": deploymentplan.Object{"name": postgresBackupName + "-config", "defaultMode": float64(0440)}},
		deploymentplan.Object{"name": "backup-credentials", "secret": deploymentplan.Object{"secretName": postgresBackupName + "-credentials", "defaultMode": float64(0440), "items": []any{deploymentplan.Object{"key": "source-password", "path": "source-password"}}}},
	}
	for _, added := range volumes {
		for _, existing := range certIssuerObjectList(pod["volumes"]) {
			if deploymentplan.Map(existing)["name"] == deploymentplan.Map(added)["name"] {
				return nil, errors.New("deployment.recovery.postgres: archive volume conflicts with identity state")
			}
		}
	}
	pod["volumes"] = append(certIssuerObjectList(pod["volumes"]), volumes...)
	postgres["volumeMounts"] = append(certIssuerObjectList(postgres["volumeMounts"]),
		deploymentplan.Object{"name": "rtk-tools", "mountPath": "/opt/rtk-tools", "readOnly": true},
		deploymentplan.Object{"name": "backup-config", "mountPath": "/etc/postgres-backup", "readOnly": true},
		deploymentplan.Object{"name": "backup-credentials", "mountPath": "/run/postgres-backup", "readOnly": true},
		deploymentplan.Object{"name": "data", "mountPath": "/backup", "subPath": "rtk-wal-spool"})
	for _, existing := range certIssuerObjectList(pod["initContainers"]) {
		if deploymentplan.Map(existing)["name"] == "install-wal-archiver" {
			return nil, errors.New("deployment.recovery.postgres: archive installer has multiple owners")
		}
	}
	pod["initContainers"] = append(certIssuerObjectList(pod["initContainers"]), deploymentplan.Object{
		"name": "install-wal-archiver", "image": c.RunnerImage,
		"command":         []any{"sh", "-ceu", "test \"$(id -u postgres)\" = 70; cp /usr/local/bin/rtk-cloud /opt/rtk-tools/rtk-cloud; chmod 0755 /opt/rtk-tools/rtk-cloud; mkdir -p /postgres-data/rtk-wal-spool/wal-spool; chown 70:70 /postgres-data/rtk-wal-spool /postgres-data/rtk-wal-spool/wal-spool; chmod 0700 /postgres-data/rtk-wal-spool /postgres-data/rtk-wal-spool/wal-spool"},
		"securityContext": deploymentplan.Object{"runAsUser": float64(0), "allowPrivilegeEscalation": false, "capabilities": deploymentplan.Object{"drop": []any{"ALL"}, "add": []any{"CHOWN", "FOWNER", "DAC_OVERRIDE"}}},
		"resources":       deploymentplan.Object{"requests": deploymentplan.Object{"cpu": "100m", "memory": "128Mi"}, "limits": deploymentplan.Object{"cpu": "500m", "memory": "256Mi"}},
		"volumeMounts":    []any{deploymentplan.Object{"name": "rtk-tools", "mountPath": "/opt/rtk-tools"}, deploymentplan.Object{"name": "data", "mountPath": "/postgres-data"}},
	})
	backupObjects := postgresBackupObjects(c, *c.ScheduleEnabled)
	backupObjects = append(backupObjects, deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": postgresBackupMetadata(c, postgresBackupName+"-credentials"), "type": "Opaque", "stringData": protection.Credentials})
	for _, object := range backupObjects {
		if objects[deploymentplan.ID(object)] != nil {
			return nil, errors.New("deployment.recovery.postgres: duplicate backup catalog owner")
		}
		// Convert helper-native values to the same Kubernetes JSON shape.
		raw, err := json.Marshal(object)
		if err != nil || json.Unmarshal(raw, &object) != nil {
			return nil, errors.New("invalid backup catalog resource")
		}
		definitions = append(definitions, deploymentplan.Definition{Object: object, Phase: environmentResourcePhase(deploymentplan.String(object["kind"]))})
	}
	return definitions, pinEnvironmentResourceImages(definitions, release)
}
