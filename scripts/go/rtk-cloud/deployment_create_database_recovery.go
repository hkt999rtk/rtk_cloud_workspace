package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func loadCreateDatabaseSecretsCheckpoint(store secretStore, e *environmentCreateExecution, p *createFoundationRecoveryPlan) (deploymentplan.Plan, deploymentplan.Journal, map[string]string, error) {
	var plan deploymentplan.Plan
	var journal deploymentplan.Journal
	bad := func() (deploymentplan.Plan, deploymentplan.Journal, map[string]string, error) {
		return plan, journal, nil, createProviderRecoveryBlock("original completed database Secrets lack exact credential and write-owner evidence")
	}
	if len(e.Journal.KubernetesLeases) == 0 || len(e.Journal.Entries) == 0 {
		return bad()
	}
	sha := strings.SplitN(e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1].Holder, ":", 2)[0]
	return loadCreateDatabaseSecretsEvidence(store, p, sha, e.Journal.Entries[len(e.Journal.Entries)-1].IntentSHA256)
}

func loadCreateDatabaseSecretsEvidence(store secretStore, p *createFoundationRecoveryPlan, sha, expectedIntent string) (deploymentplan.Plan, deploymentplan.Journal, map[string]string, error) {
	var plan deploymentplan.Plan
	var journal deploymentplan.Journal
	bad := func() (deploymentplan.Plan, deploymentplan.Journal, map[string]string, error) {
		return plan, journal, nil, createProviderRecoveryBlock("original completed database Secrets lack exact credential and write-owner evidence")
	}
	if !environmentPreflightDigest.MatchString(sha) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), &plan) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "journal.json"), &journal) != nil || plan.SHA256() != sha || journal.PlanSHA256 != sha || journal.Accepted || !journal.AcceptanceStarted || journal.InFlight != "" || journal.Failed != "deployment.post-deploy.workloads" || len(plan.Resources) != 3 || len(journal.Owners) != 3 || len(journal.Completed) != 3 {
		return bad()
	}
	intent, err := environmentCreateKubernetesStageIntent(&plan)
	intentSHA, hashErr := environmentCreatePlanSHA256(intent)
	if err != nil || hashErr != nil || expectedIntent != intentSHA {
		return bad()
	}
	if err := bindCreateDatabaseMaterial(store, p.ParentSHA, p.Materials); err != nil {
		return plan, journal, nil, err
	}
	passwords := map[string]string{}
	for _, role := range []string{"controller", "issuer", "verifier"} {
		raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment/create/material", p.ParentSHA, "database", role+".password"), true)
		if err != nil {
			return bad()
		}
		passwords[role] = string(raw)
	}
	seen := map[string]bool{}
	for _, r := range plan.Resources {
		var role string
		for candidate := range passwords {
			if r.ID == "Secret/"+p.Plan.Stack+"-video-cloud/pki-"+candidate+"-database" {
				role = candidate
			}
		}
		if role == "" || seen[role] || journal.Owners[r.ID] == "" || r.Action != "create" && r.Action != "update" {
			return bad()
		}
		seen[role] = true
		completed := false
		for _, id := range journal.Completed {
			if id == r.ID {
				completed = true
			}
		}
		if !completed {
			return bad()
		}
		dsn := "postgres://" + url.UserPassword("rtk_pki_"+role+"_login", passwords[role]).String() + "@postgresql." + p.Plan.Stack + "-platform.svc:5432/video_cloud?sslmode=disable"
		data := deploymentplan.Map(r.Desired["data"])
		if len(data) != 1 || data["url"] != base64.StdEncoding.EncodeToString([]byte(dsn)) {
			return bad()
		}
	}
	p.CheckpointPlanSHA, p.CheckpointJournalSHA = sha, deploymentplan.Digest(journal)
	return plan, journal, passwords, nil
}

func bindCreateDatabaseMaterial(store secretStore, parent string, materials map[string]string) error {
	for _, role := range []string{"controller", "issuer", "verifier"} {
		name := filepath.Join("deployment/create/material", parent, "database", role+".password")
		raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, name), true)
		if err != nil || len(raw) == 0 || strings.ContainsAny(string(raw), "\r\n") {
			return createProviderRecoveryBlock("complete original database passwords are required; regeneration is prohibited")
		}
		materials[name] = environmentPreflightBytesSHA256(raw)
	}
	return nil
}

func loadCreateDatabaseCheckpoint(store secretStore, e *environmentCreateExecution, p *createFoundationRecoveryPlan) (deploymentplan.Plan, deploymentplan.Journal, map[string]string, error) {
	var plan deploymentplan.Plan
	var journal deploymentplan.Journal
	bad := func() (deploymentplan.Plan, deploymentplan.Journal, map[string]string, error) {
		return plan, journal, nil, createProviderRecoveryBlock("original partial database role initialization lacks exact native write evidence")
	}
	if len(e.Journal.KubernetesLeases) == 0 {
		return bad()
	}
	sha := strings.SplitN(e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1].Holder, ":", 2)[0]
	secretID := "Secret/" + p.Plan.Stack + "-video-cloud/pki-create-database-logins"
	jobID := "Job/" + p.Plan.Stack + "-video-cloud/pki-create-database-logins"
	if !environmentPreflightDigest.MatchString(sha) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), &plan) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "journal.json"), &journal) != nil || plan.SHA256() != sha || journal.PlanSHA256 != sha || journal.Accepted || journal.AcceptanceStarted || journal.InFlight != "" || journal.Failed != jobID || len(plan.Resources) != 2 || len(journal.Owners) != 2 || len(journal.Completed) != 1 || journal.Completed[0] != secretID || journal.Owners[secretID] == "" || journal.Owners[jobID] == "" {
		return bad()
	}
	intent, err := environmentCreateKubernetesStageIntent(&plan)
	intentSHA, hashErr := environmentCreatePlanSHA256(intent)
	if err != nil || hashErr != nil || e.Journal.Entries[len(e.Journal.Entries)-1].IntentSHA256 != intentSHA {
		return bad()
	}
	if err := bindCreateDatabaseMaterial(store, p.ParentSHA, p.Materials); err != nil {
		return plan, journal, nil, err
	}
	passwords := map[string]string{}
	for _, role := range []string{"controller", "issuer", "verifier"} {
		raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment/create/material", p.ParentSHA, "database", role+".password"), true)
		if err != nil {
			return bad()
		}
		passwords[role] = string(raw)
	}
	var secret, job bool
	for _, r := range plan.Resources {
		if r.Action != "create" {
			return bad()
		}
		switch r.ID {
		case secretID:
			secret = true
			data := deploymentplan.Map(r.Desired["data"])
			if len(data) != 3 {
				return bad()
			}
			for role, password := range passwords {
				if data[role] != base64.StdEncoding.EncodeToString([]byte(password)) {
					return bad()
				}
			}
		case jobID:
			job = true
		default:
			return bad()
		}
	}
	if !secret || !job {
		return bad()
	}
	p.CheckpointPlanSHA, p.CheckpointJournalSHA = sha, deploymentplan.Digest(journal)
	return plan, journal, passwords, nil
}

// This profile admits only the exact observed partial outcome: three original
// logins, two memberships, and the missing verifier membership. It authenticates
// all original passwords over TCP, without changing roles or password hashes.
func verifyCreateDatabaseCheckpoint(ctx context.Context, cfg deploymentConfig, store secretStore, backend environmentKubernetesBackend, plan *deploymentplan.Plan, journal deploymentplan.Journal, passwords map[string]string, repaired bool) error {
	for _, r := range plan.Resources {
		actual, err := backend.Read(ctx, r.ID)
		if err != nil || actual == nil || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != journal.Owners[r.ID] || !deploymentplan.Matches(r.Desired, actual) {
			return createProviderRecoveryBlock("original failed role Job or credential Secret drifted")
		}
		if r.Desired["kind"] == "Job" {
			failed := false
			for _, c := range certIssuerObjectList(deploymentplan.Map(actual["status"])["conditions"]) {
				if deploymentplan.Map(c)["type"] == "Failed" && deploymentplan.Map(c)["status"] == "True" {
					failed = true
				}
			}
			if !failed {
				return createProviderRecoveryBlock("original role initializer is not conclusively failed")
			}
		}
	}
	return verifyCreateDatabaseLogins(ctx, cfg, backend, passwords, repaired)
}

func verifyCreateDatabaseLogins(ctx context.Context, cfg deploymentConfig, backend environmentKubernetesBackend, passwords map[string]string, repaired bool) error {
	query := `SELECT json_build_object('roles', (SELECT count(*) FROM pg_roles WHERE rolname IN ('rtk_pki_controller_login','rtk_pki_issuer_login','rtk_pki_verifier_login') AND rolcanlogin AND NOT (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls)), 'groups', (SELECT count(*) FROM pg_roles WHERE rolname IN ('rtk_pki_controller','rtk_pki_issuer','rtk_pki_verifier') AND NOT rolcanlogin), 'controller', pg_has_role('rtk_pki_controller_login','rtk_pki_controller','MEMBER'), 'issuer', pg_has_role('rtk_pki_issuer_login','rtk_pki_issuer','MEMBER'), 'verifier', pg_has_role('rtk_pki_verifier_login','rtk_pki_verifier','MEMBER'), 'memberships', (SELECT count(*) FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.member WHERE r.rolname IN ('rtk_pki_controller_login','rtk_pki_issuer_login','rtk_pki_verifier_login')));`
	script := "set -eu\nexport PGPASSWORD=\"$POSTGRES_PASSWORD\"\npsql -X -A -t -v ON_ERROR_STOP=1 -h 127.0.0.1 -U postgres -d video_cloud <<'SQL'\n" + query + "\nSQL\n"
	for _, role := range []string{"controller", "issuer", "verifier"} {
		script += "IFS= read -r PGPASSWORD\nexport PGPASSWORD\npsql -X -A -t -v ON_ERROR_STOP=1 -h 127.0.0.1 -U rtk_pki_" + role + "_login -d video_cloud -c 'SELECT 1' >/dev/null\n"
	}
	input := []byte(passwords["controller"] + "\n" + passwords["issuer"] + "\n" + passwords["verifier"] + "\n")
	raw, err := backend.command(ctx, input, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-platform", "exec", "-i", "postgresql-0", "--container", "postgres", "--", "/bin/sh", "-ec", script)
	var result struct {
		Roles       int
		Groups      int
		Controller  bool
		Issuer      bool
		Verifier    bool
		Memberships int
	}
	wanted := 2
	if repaired {
		wanted = 3
	}
	if err != nil || json.Unmarshal(raw, &result) != nil || result.Roles != 3 || result.Groups != 3 || !result.Controller || !result.Issuer || result.Verifier != repaired || result.Memberships != wanted {
		return createProviderRecoveryBlock("actual original runtime login authentication or least-privilege memberships differ")
	}
	return nil
}

func repairCreateDatabaseRoles(ctx context.Context, cfg deploymentConfig, store secretStore, backend environmentKubernetesBackend, p *createFoundationRecoveryPlan, e *environmentCreateExecution, apply environmentCreateBootstrapApply, review string, original *deploymentplan.Plan, journal deploymentplan.Journal, passwords map[string]string) error {
	if err := verifyCreateDatabaseCheckpoint(ctx, cfg, store, backend, original, journal, passwords, false); err != nil {
		return err
	}
	name := "pki-repair-verifier-grant-" + review[:12]
	script := `set -eu
psql "$PKI_MIGRATION_URL" -X -v ON_ERROR_STOP=1 <<'SQL'
BEGIN;
DO $check$ BEGIN
  IF (SELECT count(*) FROM pg_roles WHERE rolname IN ('rtk_pki_controller_login','rtk_pki_issuer_login','rtk_pki_verifier_login') AND rolcanlogin AND NOT (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls)) <> 3
     OR NOT pg_has_role('rtk_pki_controller_login','rtk_pki_controller','MEMBER')
     OR NOT pg_has_role('rtk_pki_issuer_login','rtk_pki_issuer','MEMBER')
     OR pg_has_role('rtk_pki_verifier_login','rtk_pki_verifier','MEMBER')
     OR (SELECT count(*) FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.member WHERE r.rolname IN ('rtk_pki_controller_login','rtk_pki_issuer_login','rtk_pki_verifier_login')) <> 2 THEN
    RAISE EXCEPTION 'original partial login membership differs';
  END IF;
END $check$;
GRANT rtk_pki_verifier TO rtk_pki_verifier_login;
COMMIT;
SQL
`
	job := deploymentplan.Definition{Phase: "migration", Object: deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": name, "namespace": p.Plan.Stack + "-video-cloud"}, "spec": deploymentplan.Object{"backoffLimit": float64(0), "activeDeadlineSeconds": float64(300), "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app.kubernetes.io/name": name}}, "spec": deploymentplan.Object{"restartPolicy": "Never", "automountServiceAccountToken": false, "containers": []any{deploymentplan.Object{"name": "repair", "image": p.Plan.Release.Images["LKE_POSTGRES_IMAGE"].Reference, "command": []any{"/bin/sh", "-ec", script}, "env": []any{deploymentplan.Object{"name": "PKI_MIGRATION_URL", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "pki-migration-database", "key": "url"}}}}, "securityContext": deploymentplan.Object{"allowPrivilegeEscalation": false, "capabilities": deploymentplan.Object{"drop": []any{"ALL"}}}}}}}}}}
	stage := "database-verifier-repair-" + review
	if err := apply(ctx, stage, []deploymentplan.Definition{job}); err != nil {
		return err
	}
	if err := verifyCreateDatabaseCheckpoint(ctx, cfg, store, backend, original, journal, passwords, true); err != nil {
		return err
	}
	proof := map[string]string{"original_plan_sha256": p.CheckpointPlanSHA, "original_failed_journal_sha256": p.CheckpointJournalSHA, "actual_repair_stage": "kubernetes/" + stage, "original_password_authentication": "verified", "memberships": "three-native-permission-groups"}
	if err := e.RecordReceipt("kubernetes/database-runtime-roles", mustProviderRecoveryJSON(proof)); err != nil {
		return err
	}
	return e.CompleteEntry("kubernetes/database-runtime-roles", time.Now().UTC())
}
