package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestInitializedOpenBaoRecoveryRechecksOriginalIdentityWithoutWrites(t *testing.T) {
	for _, mode := range []string{
		"valid", "missing-init", "malformed-init", "missing-root", "changed-root", "missing-unseal", "changed-unseal",
		"receipt-hash", "receipt-pod", "receipt-controller", "receipt-root-ref", "receipt-unseal-ref", "malformed-receipt",
		"wrong-pod", "invalid-status", "sealed", "audit-raw", "audit-path", "audit-type", "audit-unavailable",
	} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, backend, e, log := createOpenBaoNativeFixture(t, "valid")
			if _, err := initializeEnvironmentCreateOpenBao(context.Background(), cfg, store, backend, e); err != nil {
				t.Fatal("isolated native initialization", err)
			}
			if err := os.WriteFile(log, nil, 0600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing-init", "missing-root", "missing-unseal":
				name := map[string]string{"missing-init": "init.json", "missing-root": "root-token", "missing-unseal": "unseal-key"}[mode]
				if err := os.Remove(filepath.Join(store.Root, "openbao", name)); err != nil {
					t.Fatal(err)
				}
			case "malformed-init":
				if err := store.write("openbao/init.json", []byte(`{"root_token":"incomplete"}`), true); err != nil {
					t.Fatal(err)
				}
			case "changed-root", "changed-unseal":
				name := "root-token"
				if mode == "changed-unseal" {
					name = "unseal-key"
				}
				if err := store.write(filepath.Join("openbao", name), []byte("foreign-identity"), true); err != nil {
					t.Fatal(err)
				}
			case "wrong-pod", "invalid-status":
				t.Setenv("RTK_CREATE_BAO_MODE", mode)
			case "sealed":
				if err := os.Remove(filepath.Join(os.Getenv("RTK_CREATE_BAO_DIRECTORY"), "unsealed")); err != nil {
					t.Fatal(err)
				}
			case "audit-raw", "audit-path", "audit-type", "audit-unavailable":
				path := filepath.Join(os.Getenv("RTK_CREATE_BAO_DIRECTORY"), "kubectl")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				old, next := `"log_raw":"false"`, `"log_raw":"true"`
				switch mode {
				case "audit-path":
					old, next = `"file_path":"/openbao/audit/audit.jsonl"`, `"file_path":"/other/audit.jsonl"`
				case "audit-type":
					old, next = `"type":"file"`, `"type":"socket"`
				case "audit-unavailable":
					old, next = `*'bao audit list -format=json'*) cat >/dev/null;printf`, `*'bao audit list -format=json'*) exit 1;printf`
				}
				if !bytes.Contains(raw, []byte(old)) {
					t.Fatal("external audit fixture response not found")
				}
				if err := os.WriteFile(path, bytes.ReplaceAll(raw, []byte(old), []byte(next)), 0755); err != nil {
					t.Fatal(err)
				}
			default:
				if strings.HasPrefix(mode, "receipt-") || mode == "malformed-receipt" {
					for i, entry := range e.Journal.Entries {
						if entry.Key != "openbao-initialize" {
							continue
						}
						if mode == "malformed-receipt" {
							e.Journal.Entries[i].Receipt = json.RawMessage(`"not-an-initialization-proof"`)
						} else {
							var proof map[string]string
							if err := json.Unmarshal(entry.Receipt, &proof); err != nil {
								t.Fatal(err)
							}
							key := map[string]string{
								"receipt-hash": "init_sha256", "receipt-pod": "pod_uid", "receipt-controller": "statefulset_uid",
								"receipt-root-ref": "root_token_ref", "receipt-unseal-ref": "unseal_ref",
							}[mode]
							proof[key] = "foreign-original"
							e.Journal.Entries[i].Receipt = mustProviderRecoveryJSON(proof)
						}
					}
					if err := e.save(); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			root, audit, err := verifyInitializedOpenBaoIdentity(context.Background(), cfg, store, e, backend)
			if mode == "valid" {
				if err != nil || root != "private-native-root-token" || !bytes.Contains(audit, []byte("deployment-audit/")) {
					t.Fatal("original initialization was not independently verified", err)
				}
			} else if err == nil {
				t.Fatal("unverifiable initialization identity accepted", mode)
			}
			if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, store.Root)) {
				t.Fatal("identity read changed original keys or receipts")
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"operator init", "sys/unseal", "auth enable", "bao write", "private-native-root-token"} {
				if bytes.Contains(calls, []byte(forbidden)) {
					t.Fatal("recovery read regenerated identity, mutated OpenBao, or exposed its token", forbidden)
				}
			}
		})
	}
}

func TestOpenBaoRecoveryRejectsConflictingCheckpointsBeforeOpeningState(t *testing.T) {
	// Mutually exclusive recovery selectors are operator-facing safety
	// contracts. Each invalid request must fail before inspecting or creating
	// an environment's execution history.
	conflicts := [][]string{
		{"--after-service-core-enrollment", "--after-managed-core-order"},
		{"--after-service-core-enrollment", "--after-application-enrollment-trust"},
		{"--after-managed-core-order", "--after-application-enrollment-trust"},
		{"--after-service-core-enrollment", "--at-service-session"},
		{"--after-service-core-enrollment", "--at-certissuer-server-enrollment"},
		{"--after-service-core-enrollment", "--at-service-root-trust-admission"},
		{"--after-service-core-enrollment", "--at-service-root-trust-rollout"},
		{"--after-service-core-enrollment", "--recover-initial-reservation"},
		{"--after-service-core-enrollment", "--at-pki-listeners"},
		{"--after-service-core-enrollment", "--after-pki-listeners"},
		{"--after-service-core-enrollment", "--renew-initial-transports"},
		{"--at-service-session", "--at-service-root-trust-admission"},
		{"--at-service-session", "--at-service-root-trust-rollout"},
		{"--at-service-session", "--recover-initial-reservation"},
		{"--at-service-session", "--at-pki-listeners"},
		{"--at-service-session", "--after-pki-listeners"},
		{"--at-service-session", "--renew-initial-transports"},
		{"--at-certissuer-server-enrollment", "--at-service-session"},
		{"--at-certissuer-server-enrollment", "--at-service-root-trust-admission"},
		{"--at-certissuer-server-enrollment", "--at-service-root-trust-rollout"},
		{"--at-certissuer-server-enrollment", "--recover-initial-reservation"},
		{"--at-certissuer-server-enrollment", "--at-pki-listeners"},
		{"--at-certissuer-server-enrollment", "--after-pki-listeners"},
		{"--at-certissuer-server-enrollment", "--renew-initial-transports"},
		{"--at-service-root-trust-admission", "--recover-initial-reservation"},
		{"--after-pki-listeners", "--at-pki-listeners"},
		{"--after-pki-listeners", "--renew-initial-transports"},
		{"--after-account-startup", "--at-account-admission"},
		{"--after-account-startup", "--after-migration"},
		{"--after-account-startup", "--after-database-roles"},
		{"--after-account-startup", "--after-database-secrets"},
		{"--at-account-admission", "--after-migration"},
		{"--at-account-admission", "--after-database-roles"},
		{"--at-account-admission", "--after-database-secrets"},
		{"--after-migration", "--after-database-roles"},
		{"--after-migration", "--after-database-secrets"},
		{"--after-database-roles", "--after-database-secrets"},
		{"--account-schema-migrate", "--at-account-admission"},
		{"--after-bootstrap-renewal-permissions", "--at-service-session"},
		{"--timeout=0"},
		{"--timeout=-1s"},
		{"--environment=staging"},
		{"--parent-plan-sha256=unreviewed"},
		{"unexpected-positional-argument"},
		{"--unknown-selector"},
	}
	for _, conflict := range conflicts {
		t.Run(strings.Join(conflict, ","), func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			before := createBootstrapStateSnapshot(t, store.Root)
			args := append([]string{"--environment=dev", "--parent-plan-sha256=" + strings.Repeat("a", 64)}, conflict...)
			var out, diagnostic bytes.Buffer
			err := runEnvironmentCreateOpenBaoRecoveryContext(context.Background(), args, &out, &diagnostic, true)
			if err == nil || out.Len() != 0 {
				t.Fatal("invalid recovery selector produced an accepted result")
			}
			if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, store.Root)) {
				t.Fatal("invalid selector opened or changed an execution")
			}
			if strings.Contains(err.Error(), "canonical operator") || strings.Contains(err.Error(), "owner.json") {
				t.Fatal("conflicting selectors reached private environment reads", err)
			}
			if strings.Contains(diagnostic.String(), store.Root) {
				t.Fatal("flag diagnostics disclosed the private environment path")
			}
		})
	}
}

func TestInitializedOpenBaoRecoveryRejectsChangedOwnerAfterNativeRead(t *testing.T) {
	cfg, store, backend, e, log := createOpenBaoNativeFixture(t, "valid")
	if _, err := initializeEnvironmentCreateOpenBao(context.Background(), cfg, store, backend, e); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, nil, 0600); err != nil {
		t.Fatal(err)
	}
	owner := e.owner
	owner.OwnerToken = deploymentplan.Digest("replacement-owner")
	if err := store.write("deployment/.execution-active/owner.json", mustProviderRecoveryJSON(owner), true); err != nil {
		t.Fatal(err)
	}
	before := createBootstrapStateSnapshot(t, store.Root)
	if _, _, err := verifyInitializedOpenBaoIdentity(context.Background(), cfg, store, e, backend); err == nil {
		t.Fatal("native readback concealed replaced local owner")
	}
	if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, store.Root)) {
		t.Fatal("stale recovery replaced the new owner")
	}
	if raw, err := os.ReadFile(log); err != nil || !bytes.Contains(raw, []byte("bao audit list")) {
		t.Fatal("test did not reach the native audit readback")
	}
}
