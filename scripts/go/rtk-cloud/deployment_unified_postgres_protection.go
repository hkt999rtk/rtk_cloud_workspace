package main

import (
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

// Include an explicitly enabled existing backup component without installing
// WAL/PITR behavior or changing its source PostgreSQL Pod configuration.
func renderEnvironmentPostgresProtection(definitions []deploymentplan.Definition, protection environmentPostgresProtection, release environmentRelease) ([]deploymentplan.Definition, error) {
	c := protection.Configuration
	if c.validate() != nil || c.ScheduleEnabled == nil {
		return nil, errors.New("deployment.configuration.postgres-backup: explicit valid backup configuration and schedule intent required")
	}
	if c.Worker.Source.PostgresImage != release.Images["LKE_POSTGRES_IMAGE"].Reference || c.RunnerImage != release.Images["LKE_POSTGRES_BACKUP_RUNNER_IMAGE"].Reference {
		return nil, errors.New("deployment.release.postgres: enabled backup images differ from selected release")
	}
	for _, key := range []string{"source-password", "ACCESS_KEY_ID", "SECRET_ACCESS_KEY"} {
		if strings.TrimSpace(protection.Credentials[key]) == "" {
			return nil, errors.New("deployment.configuration.postgres-backup: original backup credentials unavailable")
		}
	}
	source := false
	for _, definition := range definitions {
		if deploymentplan.ID(definition.Object) == "StatefulSet/"+c.Namespace+"/postgresql" {
			source = true
		}
	}
	if !source {
		return nil, errors.New("deployment.configuration.postgres-backup: source PostgreSQL target is absent")
	}
	objects := postgresBackupObjects(c, *c.ScheduleEnabled)
	objects = append(objects, map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": postgresBackupMetadata(c, postgresBackupName+"-credentials"), "type": "Opaque", "stringData": protection.Credentials})
	for _, object := range objects {
		definitions = append(definitions, deploymentplan.Definition{Phase: environmentResourcePhase(deploymentplan.String(object["kind"])), Object: deploymentplan.Object(object)})
	}
	return definitions, nil
}
