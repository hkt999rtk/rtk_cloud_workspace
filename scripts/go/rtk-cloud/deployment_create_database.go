package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Runtime logins are separate from the migration owner and the native NOLOGIN
// permission groups. This initialization changes only the new database roles;
// it does not insert issuer, authority, approval or consumer receipt rows.
func initializeEnvironmentCreateDatabaseRoles(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, store secretStore, execution *environmentCreateExecution, apply environmentCreateBootstrapApply) error {
	groups := map[string]string{"controller": "rtk_pki_controller", "issuer": "rtk_pki_issuer", "verifier": "rtk_pki_verifier"}
	passwords := map[string]string{}
	for _, role := range sortedMapKeys(groups) {
		relative := filepath.Join("deployment", "create", "material", execution.Journal.PlanSHA256, "database", role+".password")
		path, err := privateDeploymentStatePath(cfg.Environment, relative)
		if err != nil {
			return err
		}
		raw, err := readStorageEvidenceFile(path)
		if errors.Is(err, os.ErrNotExist) {
			raw = []byte(randomSecret())
			if err := store.write(relative, raw, false); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		passwords[role] = strings.TrimSpace(string(raw))
		if passwords[role] == "" {
			return environmentCreateBlocker("deployment.bootstrap.database", "original database login material is empty")
		}
	}
	ns := plan.Stack + "-video-cloud"
	credentials := environmentCreateSecret(ns, "pki-create-database-logins", passwords)
	// psql's format(%I/%L) quotes the genuine generated credential inputs;
	// only fixed login/group identifiers are emitted by this maintained script.
	script := `set -eu
psql "$PKI_MIGRATION_URL" -v ON_ERROR_STOP=1 -v controller_password="$CONTROLLER_PASSWORD" -v issuer_password="$ISSUER_PASSWORD" -v verifier_password="$VERIFIER_PASSWORD" <<'SQL'
SELECT format('CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS', 'rtk_pki_controller_login', :'controller_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='rtk_pki_controller_login') \gexec
SELECT format('CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS', 'rtk_pki_issuer_login', :'issuer_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='rtk_pki_issuer_login') \gexec
SELECT format('CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS', 'rtk_pki_verifier_login', :'verifier_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='rtk_pki_verifier_login') \gexec
GRANT rtk_pki_controller TO rtk_pki_controller_login;
GRANT rtk_pki_issuer TO rtk_pki_issuer_login;
GRANT rtk_pki_verifier TO rtk_pki_verifier_login;
DO $check$ BEGIN
  IF (SELECT count(*) FROM pg_roles WHERE rolname IN ('rtk_pki_controller_login','rtk_pki_issuer_login','rtk_pki_verifier_login')) <> 3
     OR EXISTS (SELECT 1 FROM pg_roles WHERE rolname IN ('rtk_pki_controller_login','rtk_pki_issuer_login','rtk_pki_verifier_login') AND (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls)) THEN
    RAISE EXCEPTION 'fresh PKI runtime login privilege validation failed';
  END IF;
END $check$;
SQL
`
	var env []any
	env = append(env, deploymentplan.Object{"name": "PKI_MIGRATION_URL", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "pki-migration-database", "key": "url"}}})
	for _, role := range sortedMapKeys(groups) {
		env = append(env, deploymentplan.Object{"name": strings.ToUpper(role) + "_PASSWORD", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "pki-create-database-logins", "key": role}}})
	}
	job := deploymentplan.Definition{Phase: "migration", Object: deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": "pki-create-database-logins", "namespace": ns}, "spec": deploymentplan.Object{"backoffLimit": float64(0), "activeDeadlineSeconds": float64(600), "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app.kubernetes.io/name": "pki-create-database-logins"}}, "spec": deploymentplan.Object{"restartPolicy": "Never", "automountServiceAccountToken": false, "containers": []any{deploymentplan.Object{"name": "initialize", "image": plan.Release.Images["LKE_POSTGRES_IMAGE"].Reference, "command": []any{"/bin/sh", "-ec", script}, "env": env, "securityContext": deploymentplan.Object{"allowPrivilegeEscalation": false, "capabilities": deploymentplan.Object{"drop": []any{"ALL"}}}}}}}}}}
	if err := apply(ctx, "database-runtime-roles", []deploymentplan.Definition{credentials, job}); err != nil {
		return err
	}
	return applyEnvironmentCreateDatabaseSecrets(ctx, plan, passwords, apply)
}

func applyEnvironmentCreateDatabaseSecrets(ctx context.Context, plan *environmentCreatePlan, passwords map[string]string, apply environmentCreateBootstrapApply) error {
	ns := plan.Stack + "-video-cloud"
	var secrets []deploymentplan.Definition
	for _, role := range sortedMapKeys(passwords) {
		user := "rtk_pki_" + role + "_login"
		dsn := "postgres://" + url.UserPassword(user, passwords[role]).String() + "@postgresql." + plan.Stack + "-platform.svc:5432/video_cloud?sslmode=disable"
		secrets = append(secrets, environmentCreateSecret(ns, "pki-"+role+"-database", map[string]string{"url": dsn}))
	}
	return apply(ctx, "database-runtime-secrets", secrets)
}
