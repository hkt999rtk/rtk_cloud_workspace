package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type initialOperatorBinding struct {
	Schema                string `json:"schema"`
	Parent                string `json:"parent_plan_sha256"`
	UserID                string `json:"user_id"`
	AccountUID            string `json:"account_manager_uid"`
	EmailSHA              string `json:"email_sha256"`
	OriginalConfiguredSHA string `json:"original_configured_operator_sha256"`
}

func resolveInitialOperatorBinding(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string) (initialOperatorBinding, bool, error) {
	var binding initialOperatorBinding
	if e == nil || e.Store.Root != store.Root || e.Journal.Environment != cfg.Environment {
		return binding, false, createProviderRecoveryBlock("initial operator requires its original selected create generation")
	}
	ns := cfg.Values["CLOUD_STACK_NAME"] + "-account-manager"
	account, err := b.Read(ctx, "Deployment/"+ns+"/account-manager")
	if err != nil {
		// Read returns a sanitized, resource-bound error. Preserve its deadline,
		// cancellation and access classification instead of reporting owner loss.
		return binding, false, err
	}
	if account == nil {
		return binding, false, createProviderRecoveryBlock("original initial Account Manager owner unavailable")
	}
	uid := deploymentplan.String(deploymentplan.Map(account["metadata"])["uid"])
	originalOwner := false
	for _, entry := range e.Journal.Entries {
		if entry.State == "completed" && (entry.Key == "kubernetes/account-bootstrap" || strings.HasPrefix(entry.Key, "kubernetes/account-admission-recovery-") || strings.HasPrefix(entry.Key, "kubernetes/account-startup-recovery-")) {
			var receipt struct {
				Owners map[string]string `json:"owners"`
			}
			if json.Unmarshal(entry.Receipt, &receipt) == nil && receipt.Owners["Deployment/"+ns+"/account-manager"] == uid && uid != "" {
				originalOwner = true
			}
		}
	}
	if !originalOwner {
		return binding, false, createProviderRecoveryBlock("initial operator must use the original accepted Account Manager owner")
	}
	path := filepath.Join("deployment/create/material", e.Journal.PlanSHA256, "operator-binding.json")
	_, statErr := os.Lstat(filepath.Join(store.Root, path))
	exists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return binding, false, statErr
	}
	var raw []byte
	if exists {
		raw, err = readBoundedEnvironmentFile(filepath.Join(store.Root, path), true)
		if err != nil {
			return binding, false, err
		}
	}
	if exists {
		if json.Unmarshal(raw, &binding) != nil || binding.Schema != "rtk-cloud.initial-operator-binding/v1" || binding.Parent != e.Journal.PlanSHA256 || binding.AccountUID != uid || binding.OriginalConfiguredSHA != environmentPreflightBytesSHA256([]byte(operator["PKI_OPERATOR_USER_ID"])) || binding.EmailSHA != environmentPreflightBytesSHA256([]byte(strings.ToLower(strings.TrimSpace(operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"])))) || !environmentBootstrapOwnerUUID.MatchString(binding.UserID) {
			return binding, false, createProviderRecoveryBlock("recorded generation operator binding changed")
		}
	} else {
		if err := verifyNoInitialIssuers(ctx, cfg, b); err != nil {
			return binding, false, err
		}
		binding = initialOperatorBinding{Schema: "rtk-cloud.initial-operator-binding/v1", Parent: e.Journal.PlanSHA256, AccountUID: uid, EmailSHA: environmentPreflightBytesSHA256([]byte(strings.ToLower(strings.TrimSpace(operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"])))), OriginalConfiguredSHA: environmentPreflightBytesSHA256([]byte(operator["PKI_OPERATOR_USER_ID"]))}
	}
	script := "set -eu\nexport PGPASSWORD=\"$POSTGRES_PASSWORD\"\npsql -X -A -t -v ON_ERROR_STOP=1 -v seed_email=\"$1\" -h 127.0.0.1 -U postgres -d rtk_account_manager -f - <<'SQL'\nSELECT id FROM users WHERE platform_admin AND disabled_at IS NULL AND lower(email)=lower(:'seed_email') AND (SELECT count(*) FROM users WHERE platform_admin AND disabled_at IS NULL)=1;\nSQL\n"
	result, err := b.command(ctx, nil, "-n", cfg.Values["CLOUD_STACK_NAME"]+"-platform", "exec", "postgresql-0", "-c", "postgres", "--", "/bin/sh", "-ec", script, "initial-operator", operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"])
	user := strings.TrimSpace(string(result))
	if err != nil || !environmentBootstrapOwnerUUID.MatchString(user) || user == "00000000-0000-0000-0000-000000000000" || exists && binding.UserID != user {
		return binding, false, createProviderRecoveryBlock("native sole initial Platform Admin does not match this generation's binding")
	}
	binding.UserID = user
	return binding, exists, nil
}

func recordInitialOperatorBinding(ctx context.Context, store secretStore, e *environmentCreateExecution, binding initialOperatorBinding) error {
	if err := e.Check(ctx); err != nil {
		return err
	}
	raw := mustProviderRecoveryJSON(binding)
	if err := e.BeginEntry("initial-operator-binding", "bootstrap", raw, time.Now().UTC()); err != nil {
		return err
	}
	if err := store.write(filepath.Join("deployment/create/material", e.Journal.PlanSHA256, "operator-binding.json"), raw, false); err != nil {
		return err
	}
	if err := e.RecordReceipt("initial-operator-binding", raw); err != nil {
		return err
	}
	return e.CompleteEntry("initial-operator-binding", time.Now().UTC())
}
