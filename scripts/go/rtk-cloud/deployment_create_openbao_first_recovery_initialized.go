package main

import (
	"context"
	"encoding/json"
	"path/filepath"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type openBaoFirstCompletedInit struct {
	Init struct {
		InitSHA   string `json:"init_sha256"`
		PodUID    string `json:"pod_uid"`
		SetUID    string `json:"statefulset_uid"`
		RootRef   string `json:"root_token_ref"`
		UnsealRef string `json:"unseal_ref"`
	}
	AuthEnable map[string]any
	AuthConfig map[string]any
}

// Validate only committed original operations. A pending or allocated entry is
// an uncertain side effect and is never treated as a completed initialization.
func validateOpenBaoFirstCompletedInit(ctx context.Context, store secretStore, e *environmentCreateExecution) (openBaoFirstCompletedInit, error) {
	var result openBaoFirstCompletedInit
	bad := func() (openBaoFirstCompletedInit, error) {
		return openBaoFirstCompletedInit{}, createProviderRecoveryBlock("original OpenBao initialization, unseal, audit or auth completion is incomplete or uncertain")
	}
	if e == nil || e.Store != store || e.Check(ctx) != nil {
		return bad()
	}
	keys := []string{"openbao-initialize", "openbao-unseal", "openbao-audit-enable", "openbao-auth-enable", "openbao-auth-config"}
	entries := map[string]environmentCreateJournalEntry{}
	next := 0
	for _, entry := range e.Journal.Entries {
		for i, key := range keys {
			if entry.Key == key {
				if i != next || entry.Phase != "bootstrap" || entry.State != "completed" || entry.StartedAt.IsZero() || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || len(entry.Receipt) == 0 {
					return bad()
				}
				entries[key] = entry
				next++
			}
		}
	}
	if next != len(keys) {
		return bad()
	}
	init := entries[keys[0]]
	if decodeEnvironmentJSON(init.Receipt, &result.Init) != nil || !environmentPreflightDigest.MatchString(result.Init.InitSHA) || result.Init.PodUID == "" || result.Init.SetUID == "" || result.Init.RootRef != "openbao/root-token" || result.Init.UnsealRef != "openbao/unseal-key" {
		return bad()
	}
	initIntent, _ := json.Marshal(deploymentplan.Object{"pod_uid": result.Init.PodUID, "statefulset_uid": result.Init.SetUID, "key_shares": 1, "key_threshold": 1})
	if sha, err := environmentCreatePlanSHA256(initIntent); err != nil || sha != init.IntentSHA256 {
		return bad()
	}
	unseal, err := store.read("openbao/unseal-key")
	if err != nil || unseal == "" {
		return bad()
	}
	unsealIntent, _ := json.Marshal(deploymentplan.Object{"pod_uid": result.Init.PodUID, "unseal_sha256": environmentPreflightBytesSHA256([]byte(unseal))})
	if sha, err := environmentCreatePlanSHA256(unsealIntent); err != nil || sha != entries[keys[1]].IntentSHA256 {
		return bad()
	}
	var unsealStatus lkeOpenBaoStatus
	if decodeEnvironmentJSON(entries[keys[1]].Receipt, &unsealStatus) != nil || !unsealStatus.Initialized || unsealStatus.Sealed {
		return bad()
	}
	auditIntent, _ := json.Marshal(deploymentplan.Object{"path": "sys/audit/deployment-audit", "type": "file", "file_path": "/openbao/audit/audit.jsonl", "log_raw": false})
	var audit struct {
		SHA string `json:"audit_sha256"`
	}
	if sha, err := environmentCreatePlanSHA256(auditIntent); err != nil || sha != entries[keys[2]].IntentSHA256 || decodeEnvironmentJSON(entries[keys[2]].Receipt, &audit) != nil || !environmentPreflightDigest.MatchString(audit.SHA) {
		return bad()
	}
	for _, item := range []struct {
		key string
		dst *map[string]any
	}{{keys[3], &result.AuthEnable}, {keys[4], &result.AuthConfig}} {
		var receipt struct {
			Configured map[string]any `json:"configured"`
			Readback   string         `json:"readback_sha256"`
		}
		entry := entries[item.key]
		if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || !environmentPreflightDigest.MatchString(receipt.Readback) {
			return bad()
		}
		raw, err := json.Marshal(receipt.Configured)
		if err != nil {
			return bad()
		}
		sha, err := environmentCreatePlanSHA256(raw)
		if err != nil || sha != entry.IntentSHA256 {
			return bad()
		}
		*item.dst = receipt.Configured
	}
	if len(result.AuthEnable) != 2 || result.AuthEnable["path"] != "auth/kubernetes" || result.AuthEnable["type"] != "kubernetes" || len(result.AuthConfig) != 3 || result.AuthConfig["path"] != "auth/kubernetes/config" || result.AuthConfig["reviewer"] != "rotating-local-projection" || deploymentplan.String(result.AuthConfig["host"]) == "" {
		return bad()
	}
	return result, e.Check(ctx)
}

// readOpenBaoFirstInitialized never writes or starts an initialization. It
// authenticates the original parent/child/UID and then performs actual status,
// audit and Kubernetes auth reads using the original root credential.
func readOpenBaoFirstInitialized(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend) (environmentCreateOpenBaoBootstrap, error) {
	var result environmentCreateOpenBaoBootstrap
	bad := func() (environmentCreateOpenBaoBootstrap, error) {
		return result, createProviderRecoveryBlock("original initialized OpenBao identity or live status cannot be verified")
	}
	if ctx.Err() != nil || e == nil || e.Store != store || b.Store != store || e.Check(ctx) != nil {
		return bad()
	}
	stage, err := inspectOpenBaoFirstRecordedStage(ctx, cfg, store, e, "foundation", b)
	if err != nil || stage == nil || !stage.HasJournal || !stage.Journal.Accepted || stage.ParentState != "completed" {
		return bad()
	}
	material, exists, missing, err := readOpenBaoFirstRetainedTransport(ctx, cfg, store, e, b)
	if err != nil || !exists || missing {
		return bad()
	}
	proof, err := validateOpenBaoFirstCompletedInit(ctx, store, e)
	if err != nil {
		return result, err
	}
	if _, _, err := originalCreateOpenBaoFoundationIdentity(ctx, cfg, store, e, b, "kubernetes/foundation"); err != nil {
		return bad()
	}
	token, err := store.read("openbao/root-token")
	if err != nil || token == "" {
		return bad()
	}
	status, err := readEnvironmentCreateOpenBaoStatus(ctx, cfg, b)
	if err != nil || !status.Initialized || status.Sealed {
		return bad()
	}
	prefix := "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\n"
	var audit deploymentplan.Object
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, b, []byte(token+"\n"), prefix+"bao audit list -format=json\n")
	if err != nil || json.Unmarshal(raw, &audit) != nil {
		return bad()
	}
	device := deploymentplan.Map(audit["deployment-audit/"])
	options := deploymentplan.Map(device["options"])
	if device["type"] != "file" || options["file_path"] != "/openbao/audit/audit.jsonl" || options["log_raw"] != "false" {
		return bad()
	}
	var mounts deploymentplan.Object
	raw, err = environmentCreateOpenBaoExec(ctx, cfg, b, []byte(token+"\n"), prefix+"bao auth list -format=json\n")
	if err != nil || json.Unmarshal(raw, &mounts) != nil || deploymentplan.Map(mounts["kubernetes/"])["type"] != proof.AuthEnable["type"] {
		return bad()
	}
	var config deploymentplan.Object
	raw, err = environmentCreateOpenBaoExec(ctx, cfg, b, []byte(token+"\n"), prefix+"bao read -format=json auth/kubernetes/config\n")
	data := deploymentplan.Object(nil)
	if err == nil && json.Unmarshal(raw, &config) == nil {
		data = deploymentplan.Map(config["data"])
	}
	if data == nil || data["kubernetes_host"] != proof.AuthConfig["host"] || data["disable_local_ca_jwt"] != false || data["token_reviewer_jwt_set"] == true {
		return bad()
	}
	caPath, ca, err := initialTransportAuthorityPath(store, e.Journal.PlanSHA256, "openbao")
	if err != nil || ca != material.CACert || e.Check(ctx) != nil {
		return bad()
	}
	rootPath, err := store.safePath("openbao/root-token")
	if err != nil {
		return bad()
	}
	result = environmentCreateOpenBaoBootstrap{RootTokenPath: rootPath, URL: "https://openbao." + cfg.Values["CLOUD_STACK_NAME"] + "-secrets.svc:8200", CACertPath: filepath.Clean(caPath)}
	return result, nil
}
