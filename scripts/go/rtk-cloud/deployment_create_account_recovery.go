package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func renderEnvironmentCreateAccountMigration(cfg deploymentConfig, release environmentRelease) ([]deploymentplan.Definition, error) {
	values := environmentCreateProviderValues(cfg)
	for key, image := range release.Images {
		values[key] = image.Reference
	}
	definitions, err := decodeEnvironmentCreateManifests(lkeAccountManagerMigrationJobManifest(values))
	if err != nil {
		return nil, err
	}
	if len(definitions) != 1 {
		return nil, createProviderRecoveryBlock("exact native Account Manager migration definition required")
	}
	definition := &definitions[0]
	definition.Phase = "migration"
	spec := deploymentplan.Map(definition.Object["spec"])
	spec["backoffLimit"] = float64(0)
	spec["activeDeadlineSeconds"] = float64(300)
	pod := deploymentplan.Map(deploymentplan.Map(spec["template"])["spec"])
	pod["restartPolicy"] = "Never"
	pod["automountServiceAccountToken"] = false
	return definitions, nil
}

func verifyEmptyInitialAccountDatabase(ctx context.Context, cfg deploymentConfig, backend environmentKubernetesBackend, account *deploymentplan.Plan, journal deploymentplan.Journal) error {
	for _, r := range account.Resources {
		actual, err := backend.Read(ctx, r.ID)
		if err != nil || actual == nil || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != journal.Owners[r.ID] || !deploymentplan.Matches(r.Desired, actual) {
			return createProviderRecoveryBlock("original Account Manager owner or body changed before initial migration")
		}
	}
	job, err := backend.Read(ctx, "Job/"+cfg.Values["CLOUD_STACK_NAME"]+"-account-manager/account-manager-migrate")
	if err != nil || job != nil {
		return createProviderRecoveryBlock("initial native Account Manager migration Job already exists; no replay allowed")
	}
	script := "set -eu\nexport PGPASSWORD=\"$POSTGRES_PASSWORD\"\npsql -X -A -t -v ON_ERROR_STOP=1 -h 127.0.0.1 -U postgres -d rtk_account_manager -c \"SELECT count(*) FROM pg_tables WHERE schemaname='public'\"\n"
	raw, err := backend.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-platform", "exec", "postgresql-0", "--container", "postgres", "--", "/bin/sh", "-ec", script)
	if err != nil || strings.TrimSpace(string(raw)) != "0" {
		return createProviderRecoveryBlock("initial Account Manager database is not provably empty; existing data requires its native migration lifecycle")
	}
	return nil
}

func loadOriginalAccountStartupCheckpoint(store secretStore, e *environmentCreateExecution, p *createFoundationRecoveryPlan) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	var plan deploymentplan.Plan
	var journal deploymentplan.Journal
	bad := func() (*deploymentplan.Plan, deploymentplan.Journal, error) {
		return nil, journal, createProviderRecoveryBlock("original completed-write Account Manager stage lacks exact owner and failed acceptance evidence")
	}
	if len(e.Journal.KubernetesLeases) == 0 {
		return bad()
	}
	sha := strings.SplitN(e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1].Holder, ":", 2)[0]
	if !environmentPreflightDigest.MatchString(sha) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), &plan) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "journal.json"), &journal) != nil || plan.SHA256() != sha || journal.PlanSHA256 != sha || journal.Accepted || !journal.AcceptanceStarted || journal.InFlight != "" || journal.Failed != "deployment.post-deploy.workloads" || len(plan.Resources) != 2 || len(journal.Owners) != 2 || len(journal.Completed) != 2 {
		return bad()
	}
	for _, r := range plan.Resources {
		if r.Action != "create" || journal.Owners[r.ID] == "" || r.ID != "Deployment/"+p.Plan.Stack+"-account-manager/account-manager" && r.ID != "Service/"+p.Plan.Stack+"-account-manager/account-manager" {
			return bad()
		}
		complete := false
		for _, id := range journal.Completed {
			if id == r.ID {
				complete = true
			}
		}
		if !complete {
			return bad()
		}
	}
	intent, err := environmentCreateKubernetesStageIntent(&plan)
	intentSHA, hashErr := environmentCreatePlanSHA256(intent)
	if err != nil || hashErr != nil || intentSHA != e.Journal.Entries[len(e.Journal.Entries)-1].IntentSHA256 {
		return bad()
	}
	p.CheckpointPlanSHA, p.CheckpointJournalSHA = sha, deploymentplan.Digest(journal)
	return &plan, journal, nil
}

// This exact initial repair derives the previously configured seed as the legacy
// renderer did, before any outbox data existed. Existing valid encryption keys
// cannot use this profile, and no OpenBao/PKI identity is changed.
func verifyOriginalAccountEmailRepair(store secretStore, e *environmentCreateExecution, path string, foundation *deploymentplan.Plan, foundationJournal deploymentplan.Journal, account *deploymentplan.Plan, accountJournal deploymentplan.Journal) (string, string, error) {
	bad := func() (string, string, error) {
		return "", "", createProviderRecoveryBlock("original unused email-key format repair is incomplete, changed or belongs to another generation")
	}
	if !strings.HasPrefix(path, store.Root+"/deployment/repairs/account-email-format-") || filepath.Clean(path) != path || filepath.Base(path) != "verified.json" {
		return bad()
	}
	checked, err := store.safePath(strings.TrimPrefix(path, store.Root+"/"))
	if err != nil || checked != path {
		return bad()
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil {
		return bad()
	}
	var proof struct {
		Schema        string `json:"schema"`
		Environment   string `json:"environment"`
		Parent        string `json:"parent_plan_sha256"`
		Child         string `json:"original_child_plan_sha256"`
		SecretUID     string `json:"secret_uid"`
		DeploymentUID string `json:"deployment_uid"`
		OldSHA        string `json:"original_runtime_sha256"`
		NewSHA        string `json:"runtime_sha256"`
		OriginalFile  string `json:"original_file"`
		Derivation    string `json:"derivation"`
		Rows          int    `json:"outbox_rows"`
		TablePresent  bool   `json:"outbox_table_present"`
		OnlyEmail     bool   `json:"only_email_secret_field_changed"`
		RestartUID    string `json:"pod_restart_uid"`
		Completed     bool   `json:"completed"`
	}
	if json.Unmarshal(raw, &proof) != nil || proof.Schema != "rtk-cloud.initial-email-key-format-repair/v1" || proof.Environment != store.Environment || proof.Parent != e.Journal.PlanSHA256 || proof.Child != account.SHA256() || proof.DeploymentUID != accountJournal.Owners["Deployment/"+account.Stack+"-account-manager/account-manager"] || !proof.Completed || !proof.OnlyEmail || proof.Rows != 0 || proof.OriginalFile != "original-runtime-value" || proof.Derivation != "base64-standard-of-sha256-of-original-opaque-seed" || proof.RestartUID == "" {
		return bad()
	}
	old, err := readBoundedEnvironmentFile(filepath.Join(filepath.Dir(path), proof.OriginalFile), true)
	if err != nil || environmentPreflightBytesSHA256(old) != proof.OldSHA {
		return bad()
	}
	seed := strings.TrimSpace(string(old))
	if seed == "" {
		return bad()
	}
	if decoded, err := base64.StdEncoding.DecodeString(seed); err == nil && len(decoded) == 32 {
		return bad()
	}
	hash := sha256.Sum256([]byte(seed))
	key := base64.StdEncoding.EncodeToString(hash[:])
	current, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "runtime/email-outbox-encryption"), true)
	if err != nil || environmentPreflightBytesSHA256(current) != proof.NewSHA || strings.TrimSpace(string(current)) != key {
		return bad()
	}
	secretID := "Secret/" + account.Stack + "-account-manager/account-manager-runtime"
	bound := false
	for _, r := range foundation.Resources {
		if r.ID == secretID {
			bound = foundationJournal.Owners[r.ID] == proof.SecretUID && deploymentplan.Map(r.Desired["data"])["EMAIL_OUTBOX_ENCRYPTION_KEY"] == base64.StdEncoding.EncodeToString([]byte(seed))
		}
	}
	if !bound {
		return bad()
	}
	return base64.StdEncoding.EncodeToString([]byte(key)), environmentPreflightBytesSHA256(raw), nil
}
