package main

import (
	"encoding/base64"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestEnabledDailyBackupPinsImagesAndPreservesPostgresAcrossEnvironments(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-backup-fixture")
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, bindings, release := completeEnvironmentRenderFixture(environment)
			bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			restore := installAllCredentialEnvironment(env)
			defer restore()
			c := postgresBackupTestConfig(t)
			c.Environment, c.Stack, c.Namespace = environment, bindings.Stack, bindings.Stack+"-platform"
			c.Worker.Environment, c.Worker.Stack, c.Worker.Remote.Prefix = environment, bindings.Stack, environment+"/postgres"
			c.Worker.Source.Host = "postgresql." + c.Namespace + ".svc.cluster.local"
			c.Worker.Source.PostgresImage = release.Images["LKE_POSTGRES_IMAGE"].Reference
			c.RunnerImage = "ghcr.io/hkt999rtk/rtk_cloud_workspace/postgres-backup@sha256:" + strings.Repeat("d", 64)
			release.Images["LKE_POSTGRES_BACKUP_RUNNER_IMAGE"] = environmentReleaseImage{Reference: c.RunnerImage, SourceRevision: release.WorkspaceRevision}
			for _, mode := range []string{"suspended", "enabled", "missing-runner", "changed-source", "missing-credential", "missing-schedule"} {
				t.Run(mode, func(t *testing.T) {
					definitions, err := renderEnvironmentResources(env, bindings, release)
					if err != nil {
						t.Fatal(err)
					}
					enabled := mode == "enabled"
					c.ScheduleEnabled = &enabled
					profile := environmentPostgresProtection{Configuration: c, Credentials: map[string]string{"source-password": "private-source", "ACCESS_KEY_ID": "private-id", "SECRET_ACCESS_KEY": "private-key"}}
					selected := release
					selected.Images = map[string]environmentReleaseImage{}
					for key, image := range release.Images {
						selected.Images[key] = image
					}
					switch mode {
					case "missing-runner":
						delete(selected.Images, "LKE_POSTGRES_BACKUP_RUNNER_IMAGE")
					case "changed-source":
						profile.Configuration.Worker.Source.PostgresImage = "postgres@sha256:" + strings.Repeat("f", 64)
					case "missing-credential":
						delete(profile.Credentials, "SECRET_ACCESS_KEY")
					case "missing-schedule":
						profile.Configuration.ScheduleEnabled = nil
					}
					id := "StatefulSet/" + c.Namespace + "/postgresql"
					before := ""
					for _, definition := range definitions {
						if deploymentplan.ID(definition.Object) == id {
							before = deploymentplan.Digest(definition.Object)
						}
					}
					result, err := renderEnvironmentPostgresProtection(definitions, profile, selected)
					if (err == nil) != (mode == "suspended" || mode == "enabled") {
						t.Fatal(mode, err)
					}
					if err != nil {
						return
					}
					objects := map[string]deploymentplan.Object{}
					for _, definition := range result {
						objects[deploymentplan.ID(definition.Object)] = definition.Object
					}
					if before == "" || deploymentplan.Digest(objects[id]) != before {
						t.Fatal("backup component modified source PostgreSQL")
					}
					for _, kindName := range []string{"CronJob/" + postgresBackupName, "ConfigMap/" + postgresBackupName + "-config", "Secret/" + postgresBackupName + "-credentials", "ServiceAccount/" + postgresBackupName} {
						parts := strings.SplitN(kindName, "/", 2)
						if objects[parts[0]+"/"+c.Namespace+"/"+parts[1]] == nil {
							t.Fatal("existing backup component omitted", kindName)
						}
					}
					job := objects["CronJob/"+c.Namespace+"/"+postgresBackupName]
					if deploymentplan.Map(job["spec"])["suspend"] != !enabled {
						t.Fatal("maintained schedule intent lost")
					}
					if objects["CronJob/"+c.Namespace+"/"+postgresBackupName+"-wal-monitor"] != nil {
						t.Fatal("update installed an unrequested WAL project")
					}
				})
			}
		})
	}
}
