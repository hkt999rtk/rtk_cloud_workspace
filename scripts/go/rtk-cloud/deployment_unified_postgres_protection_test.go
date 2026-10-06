package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCommonPostgresProtectionPinsArchiveAndBackupAcrossEnvironments(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-protection-fixture")
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
			suspended := false
			c.ScheduleEnabled = &suspended
			protection := environmentPostgresProtection{Configuration: c, Credentials: map[string]string{"source-password": "PRIVATE_SOURCE_CREDENTIAL", "ACCESS_KEY_ID": "PRIVATE_ARCHIVE_ID", "SECRET_ACCESS_KEY": "PRIVATE_ARCHIVE_SECRET"}}
			for _, mode := range []string{"success", "missing-runner", "changed-source", "missing-credential", "missing-schedule-intent", "ephemeral-data", "conflicting-fsgroup"} {
				t.Run(mode, func(t *testing.T) {
					definitions, err := renderEnvironmentResources(env, bindings, release)
					if err != nil {
						t.Fatal(err)
					}
					selected := release
					selected.Images = map[string]environmentReleaseImage{}
					for k, image := range release.Images {
						selected.Images[k] = image
					}
					profile := protection
					profile.Credentials = map[string]string{}
					for k, v := range protection.Credentials {
						profile.Credentials[k] = v
					}
					for _, definition := range definitions {
						if deploymentplan.ID(definition.Object) != "StatefulSet/"+c.Namespace+"/postgresql" {
							continue
						}
						spec := deploymentplan.Map(definition.Object["spec"])
						if mode == "ephemeral-data" {
							delete(spec, "volumeClaimTemplates")
						}
						if mode == "conflicting-fsgroup" {
							deploymentplan.Map(deploymentplan.Map(spec["template"])["spec"])["securityContext"] = deploymentplan.Object{"fsGroup": float64(999)}
						}
					}
					switch mode {
					case "missing-runner":
						delete(selected.Images, "LKE_POSTGRES_BACKUP_RUNNER_IMAGE")
					case "changed-source":
						profile.Configuration.Worker.Source.PostgresImage = "postgres@sha256:" + strings.Repeat("f", 64)
					case "missing-credential":
						delete(profile.Credentials, "SECRET_ACCESS_KEY")
					case "missing-schedule-intent":
						profile.Configuration.ScheduleEnabled = nil
					}
					definitions, err = renderEnvironmentPostgresProtection(definitions, profile, selected)
					if (err == nil) != (mode == "success") {
						t.Fatalf("unexpected %s outcome: %v", mode, err)
					}
					if err != nil {
						return
					}
					objects := map[string]deploymentplan.Object{}
					for _, definition := range definitions {
						objects[deploymentplan.ID(definition.Object)] = definition.Object
					}
					for _, id := range []string{"CronJob/" + c.Namespace + "/" + postgresBackupName, "CronJob/" + c.Namespace + "/" + postgresBackupName + "-wal-monitor", "ConfigMap/" + c.Namespace + "/" + postgresBackupName + "-config", "Secret/" + c.Namespace + "/" + postgresBackupName + "-credentials", "ServiceAccount/" + c.Namespace + "/" + postgresBackupName} {
						if objects[id] == nil {
							t.Fatal("protection component omitted", id)
						}
					}
					if deploymentplan.Map(objects["CronJob/"+c.Namespace+"/"+postgresBackupName]["spec"])["suspend"] != true {
						t.Fatal("schedule enabled without qualification")
					}
					monitor := deploymentplan.Map(objects["CronJob/"+c.Namespace+"/"+postgresBackupName+"-wal-monitor"]["spec"])
					if monitor["suspend"] != true || monitor["concurrencyPolicy"] != "Forbid" || monitor["schedule"] != "* * * * *" {
						t.Fatal("continuous monitoring omitted or enabled without qualification")
					}
					monitorPod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(monitor["jobTemplate"])["spec"])["template"])["spec"])
					monitorJSON, _ := json.Marshal(monitorPod)
					if monitorPod["automountServiceAccountToken"] != false || strings.Contains(string(monitorJSON), "persistentVolumeClaim") || strings.Contains(string(monitorJSON), "data-postgresql") {
						t.Fatal("WAL monitoring gains Kubernetes credentials or competes for a backup/source PVC")
					}
					pg := objects["StatefulSet/"+c.Namespace+"/postgresql"]
					pod := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(pg["spec"])["template"])["spec"])
					raw, _ := json.Marshal(pod)
					for _, expected := range []string{"archive_mode=on", "archive_timeout=60s", "archive-wal", "--wal-pgdata", "install-wal-archiver", "/backup", "rtk-wal-spool", c.RunnerImage} {
						if !strings.Contains(string(raw), expected) {
							t.Fatal("source protection missing", expected)
						}
					}
					for _, secret := range protection.Credentials {
						if strings.Contains(string(raw), secret) {
							t.Fatal("source Pod contains literal credential")
						}
					}
					// The pure catalog test supplies the lifecycle-owned admission
					// Secret as a definition. Real compilation binds its live owner.
					definitions = append(definitions, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": bindings.IngressAdmission.SecretName, "namespace": c.Stack + "-ingress"}, "type": "Opaque", "data": deploymentplan.Object{}}, Phase: "configuration"})
					plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: c.Stack, Operation: "upgrade", ConfigurationSHA256: deploymentplan.Digest("config"), ReleaseSHA256: deploymentplan.Digest(selected), BindingsSHA256: deploymentplan.Digest(bindings)}, definitions, nil)
					if err != nil {
						t.Fatal(err)
					}
					public, _ := json.Marshal(plan.Summary())
					for _, secret := range protection.Credentials {
						if strings.Contains(string(public), secret) {
							t.Fatal("protection report leaked credentials")
						}
					}
				})
			}
		})
	}
}
