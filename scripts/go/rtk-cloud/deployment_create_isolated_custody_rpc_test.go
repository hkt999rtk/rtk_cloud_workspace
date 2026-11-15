package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// isolatedCustodyScripts reconstructs exactly what the native custody
// producer emits. It never permits arbitrary command prefixes or shell input.
func isolatedCustodyScripts(cfg deploymentConfig, binding environmentCustodyBinding) (string, string, string, string, error) {
	header := "set -eu\nIFS= read -r BAO_TOKEN\nexport BAO_TOKEN\n"
	mount := cfg.Values["PKI_STATE_KV_MOUNT"]
	if err := validateEnvironmentCustodySettings(cfg.Values); err != nil {
		return "", "", "", "", err
	}
	auths, err := environmentCustodyAuthorizations(cfg, binding)
	if err != nil {
		return "", "", "", "", err
	}
	var writes, reads strings.Builder
	writes.WriteString(header)
	reads.WriteString(header)
	fmt.Fprintf(&writes, "bao write %s/config cas_required=true >/dev/null\n", mount)
	fmt.Fprintf(&reads, "bao read -format=json %s/config\n", mount)
	for _, auth := range auths {
		body, _ := json.Marshal(deploymentplan.Object{"bound_service_account_names": []string{auth.account}, "bound_service_account_namespaces": []string{auth.namespace}, "bound_service_account_namespace_selector": "", "audience": "openbao", "token_policies": []string{auth.role}, "token_no_default_policy": true, "token_ttl": 900, "token_max_ttl": 900, "token_period": 0})
		fmt.Fprintf(&writes, "bao policy write %s - >/dev/null <<'RTK_CUSTODY_POLICY'\n%sRTK_CUSTODY_POLICY\nbao write auth/kubernetes/role/%s - >/dev/null <<'RTK_CUSTODY_ROLE'\n%s\nRTK_CUSTODY_ROLE\n", auth.role, auth.policy, auth.role, body)
		fmt.Fprintf(&reads, "bao read -format=json sys/policies/acl/%s\nbao read -format=json auth/kubernetes/role/%s\n", auth.role, auth.role)
	}
	prefix := "case \"${BAO_CACERT-}\" in /openbao/transport/ca.crt|/run/openbao-pki-root/root.pem) ;; *) exit 64 ;; esac\n"
	withCreate := header + fmt.Sprintf("bao secrets enable -path=%s -version=2 kv >/dev/null\n", mount) + strings.TrimPrefix(writes.String(), header)
	return prefix + header + "bao secrets list -format=json\n", prefix + withCreate, prefix + writes.String(), prefix + reads.String(), nil
}

func isolatedCustodyEntryAllows(entry *environmentCreateJournalEntry, expected string, write bool) bool {
	if entry == nil || entry.Key != "workload-custody-initialize" || entry.Phase != "bootstrap" || entry.IntentSHA256 != expected || entry.StartedAt.IsZero() {
		return false
	}
	switch entry.State {
	case "pending":
		return entry.CompletedAt.IsZero() && len(entry.Receipt) == 0
	case "allocated":
		return entry.CompletedAt.IsZero() && len(entry.Receipt) > 0
	case "completed":
		return !write && !entry.CompletedAt.IsZero() && len(entry.Receipt) > 0
	default:
		return false
	}
}

func (c *isolatedOpenBaoFirstController) authorizeIsolatedCustodyScript(ctx context.Context, request isolatedOpenBaoFirstRequest) (string, bool) {
	if ctx.Err() != nil || c.rpc == nil || !c.rpc.containerOwned() || c.cfg.Environment != "dev" || !isolatedScriptTempPath(c.store.Root) {
		return "", false
	}
	checkpoint, err := inspectCreateRecoveryCheckpoint(c.cfg, c.store)
	if err != nil {
		return "", false
	}
	plan, configSHA, err := loadEnvironmentCreateAdmittedGeneration(c.store, checkpoint.Journal.PlanSHA256, checkpoint.Journal.ArtifactSHA256)
	if err != nil || !plan.OpenBaoFirst || configSHA != deploymentplan.Digest(c.cfg) || plan.Stack != c.cfg.Values["CLOUD_STACK_NAME"] {
		return "", false
	}
	binding, err := createEnvironmentCustodyBinding(c.cfg, plan.Bootstrap, checkpoint.Journal.PlanSHA256, "")
	if err != nil || binding == nil {
		return "", false
	}
	intent := deploymentplan.Object{"schema": "rtk.custody.bootstrap/v1", "environment": c.cfg.Environment, "generation": binding.Generation, "mount": c.cfg.Values["PKI_STATE_KV_MOUNT"], "cas_required": true, "owners": binding.Owners}
	body, err := json.Marshal(intent)
	if err != nil {
		return "", false
	}
	sha, err := environmentCreatePlanSHA256(body)
	if err != nil {
		return "", false
	}
	var entry *environmentCreateJournalEntry
	for i := range checkpoint.Journal.Entries {
		v := &checkpoint.Journal.Entries[i]
		if v.Key == "workload-custody-initialize" {
			if entry != nil {
				return "", false
			}
			entry = v
		}
	}
	list, create, update, read, err := isolatedCustodyScripts(c.cfg, *binding)
	if err != nil {
		return "", false
	}
	write := request.Script == create || request.Script == update
	action := ""
	switch request.Script {
	case list:
		action = "inventory"
	case create:
		action = "create"
	case update:
		action = "update"
	case read:
		action = "readback"
	default:
		return "", false
	}
	if (request.ReadOnly && write) || !isolatedCustodyEntryAllows(entry, sha, write) {
		return "", false
	}
	if entry.State != "pending" {
		proof, _ := json.Marshal(deploymentplan.Object{"scope_sha256": sha})
		if deploymentplan.Digest(jsonRawObject(entry.Receipt)) != deploymentplan.Digest(jsonRawObject(proof)) {
			return "", false
		}
	}
	tokenPath, err := c.store.safePath(filepath.Join("openbao", "root-token"))
	if err != nil {
		return "", false
	}
	expected, err := readBoundedEnvironmentFile(tokenPath, true)
	if err != nil {
		return "", false
	}
	input, err := base64.StdEncoding.DecodeString(request.Input)
	if err != nil {
		return "", false
	}
	if !isolatedCustodyTokenMatches(input, expected) {
		return "", false
	}
	return action, true
}

func isolatedCustodyTokenMatches(input, expected []byte) bool {
	return len(bytes.TrimSpace(expected)) > 0 && hmac.Equal(bytes.TrimSpace(input), bytes.TrimSpace(expected))
}

func TestIsolatedCustodyEntryAndTokenRejectForeign(t *testing.T) {
	now := time.Now().UTC()
	sha := strings.Repeat("a", 64)
	entry := environmentCreateJournalEntry{Key: "workload-custody-initialize", Phase: "bootstrap", IntentSHA256: sha, State: "pending", StartedAt: now}
	if !isolatedCustodyEntryAllows(&entry, sha, true) {
		t.Fatal("original pending intent rejected")
	}
	if isolatedCustodyEntryAllows(&entry, strings.Repeat("b", 64), true) {
		t.Fatal("foreign intent accepted")
	}
	entry.State = "completed"
	entry.CompletedAt = now
	entry.Receipt = []byte(`{"fixture":true}`)
	if isolatedCustodyEntryAllows(&entry, sha, true) || !isolatedCustodyEntryAllows(&entry, sha, false) {
		t.Fatal("completed custody write was allowed or read refused")
	}
	if isolatedCustodyTokenMatches([]byte("foreign\n"), []byte("original\n")) || !isolatedCustodyTokenMatches([]byte("original\n"), []byte("original\n")) {
		t.Fatal("foreign token accepted")
	}
}

func TestIsolatedCustodyScriptsRejectForeignMutation(t *testing.T) {
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "PKI_STATE_BACKEND": "openbao", "PKI_STATE_KV_MOUNT": "identities", "PKI_STATE_LEASE_DURATION": "30s"}}
	binding := environmentCustodyBinding{Generation: strings.Repeat("a", 64), Owners: []environmentCustodyOwnerBinding{{ID: "api", Namespace: "video-cloud-dev-video-cloud", ServiceAccount: "video-cloud-api"}}}
	list, create, update, read, err := isolatedCustodyScripts(cfg, binding)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{list: true, create: true, update: true, read: true}
	for _, script := range []string{list + "; id", strings.Replace(create, "identities", "foreign", 1), read + "cat /etc/passwd\n", "cat /etc/passwd"} {
		if allowed[script] {
			t.Fatal("foreign custody script accepted")
		}
	}
}

// Proof may inspect status/audit/auth only. It can never initialize, unseal,
// enable auth, or change the original custody configuration.
func isolatedOpenBaoInitializerReadOnly(script string) bool {
	prefix := "case \"${BAO_CACERT-}\" in /openbao/transport/ca.crt|/run/openbao-pki-root/root.pem) ;; *) exit 64 ;; esac\n"
	header := "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\n"
	for _, exact := range []string{
		"set +e; bao status -format=json; result=$?; [ \"$result\" -eq 0 ] || [ \"$result\" -eq 2 ]",
		header + "bao audit list -format=json\n",
		header + "bao auth list -format=json\n",
		header + "bao read -format=json auth/kubernetes/config\n",
	} {
		if script == prefix+exact {
			return true
		}
	}
	return false
}
