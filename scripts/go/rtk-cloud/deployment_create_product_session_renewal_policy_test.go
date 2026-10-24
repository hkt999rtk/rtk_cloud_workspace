package main

import (
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestProductSessionRenewalOldPolicyDeleteReconcilesExactPendingOutcome(t *testing.T) {
	for _, mode := range []string{"new", "begin-before-delete", "lost-after-delete", "lost-before-delete", "foreign-body", "unrecorded-absence", "completed-reappeared"} {
		t.Run(mode, func(t *testing.T) {
			f := newProductForwardFixture(t)
			r := &productSessionRenewal{SourceSHA256: deploymentplan.Digest("exact immutable product renewal source"), Source: productSessionRenewalSource{OriginalSession: productSessionInstallations{Session: f.service.BootstrapSessionID}, SignerPolicySHA256: environmentPreflightBytesSHA256([]byte("client text"))}, Installed: environmentProductIdentitySession{Ceremony: f.service}}
			name := deploymentplan.String(productSessionRenewalRetirePolicyIntent(f.cfg, r)["policy"])
			present, body := true, "client text"
			deleteCalls, effects := 0, 0
			f.nativeOverride = func(args []string) ([]byte, int, bool) {
				joined := strings.Join(args, " ")
				if strings.Contains(joined, "bao policy list -format=json") {
					values := []string{}
					if present {
						values = append(values, name)
					}
					return mustProviderRecoveryJSON(values), 0, true
				}
				if strings.Contains(joined, "bao policy read "+name) {
					return []byte(body), 0, true
				}
				if strings.Contains(joined, "bao policy delete "+name) {
					deleteCalls++
					if mode == "lost-before-delete" && deleteCalls == 1 {
						return nil, 1, true
					}
					if present {
						present = false
						effects++
					}
					if mode == "lost-after-delete" && deleteCalls == 1 {
						return nil, 1, true
					}
					return nil, 0, true
				}
				return nil, 0, false
			}
			intent := productSessionRenewalRetirePolicyIntent(f.cfg, r)
			if mode == "begin-before-delete" || mode == "foreign-body" {
				if err := f.e.BeginEntry(productSessionRenewalRetirePolicyKey(r), "bootstrap", mustProviderRecoveryJSON(intent), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "foreign-body" {
				body = "foreign policy"
			}
			if mode == "unrecorded-absence" {
				present = false
			}
			err := retireProductSessionRenewalPolicy(t.Context(), f.cfg, f.runtime, r)
			if mode == "lost-after-delete" || mode == "lost-before-delete" {
				if err == nil {
					t.Fatal("unknown response was treated as completed")
				}
				err = retireProductSessionRenewalPolicy(t.Context(), f.cfg, f.runtime, r)
			}
			if mode == "foreign-body" || mode == "unrecorded-absence" {
				if err == nil || deleteCalls != 0 || effects != 0 {
					t.Fatal("unowned policy mutation", err, deleteCalls, effects)
				}
				return
			}
			if err != nil || present || effects != 1 {
				t.Fatal("exact original policy did not reconcile", err, present, effects)
			}
			if mode == "completed-reappeared" {
				present = true
				if err = retireProductSessionRenewalPolicy(t.Context(), f.cfg, f.runtime, r); err == nil || effects != 1 {
					t.Fatal("completed absence adopted replacement")
				}
				return
			}
			calls := deleteCalls
			if err = retireProductSessionRenewalPolicy(t.Context(), f.cfg, f.runtime, r); err != nil || deleteCalls != calls {
				t.Fatal("completed policy retirement replayed", err)
			}
			entry := createOpenBaoACKCompletionEntry(f.e.Journal, productSessionRenewalRetirePolicyKey(r))
			if entry == nil || entry.State != "completed" {
				t.Fatal("native absence has no genuine completion")
			}
		})
	}
}

func TestProductSessionRenewalPolicyKnownReadbackAndZeroEffectPrefixes(t *testing.T) {
	for _, mode := range []string{"allocated", "pending-before-write", "pending-after-write", "changed-readback", "changed-receipt", "changed-intent", "list-failure", "read-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newProductForwardFixture(t)
			name := "rtk-dev-service-bootstrap-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			text := "synthetic exact native signer policy"
			intent := deploymentplan.Object{"environment": f.cfg.Environment, "policy": name, "policy_sha256": environmentPreflightBytesSHA256([]byte(text))}
			key := "openbao-policy-" + name
			beforeWrite := mode == "pending-before-write" || mode == "list-failure"
			if beforeWrite {
				if err := f.e.BeginEntry(key, "bootstrap", mustProviderRecoveryJSON(intent), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			} else {
				// Lose the actual native write response: the exact HCL exists,
				// but no local receipt can be recorded by the failed request.
				f.nativeOverride = func(args []string) ([]byte, int, bool) {
					if strings.Contains(strings.Join(args, " "), "bao policy write "+name) {
						f.policies[name] = text
						return nil, 1, true
					}
					return nil, 0, false
				}
				if err := installEnvironmentCreateOpenBaoPolicy(t.Context(), f.cfg, f.runtime, name, text); err == nil {
					t.Fatal("lost native response unexpectedly completed")
				}
			}
			entry := createOpenBaoACKCompletionEntry(f.e.Journal, key)
			if entry == nil || entry.State != "pending" || len(entry.Receipt) != 0 {
				t.Fatal("missing genuine pending policy intent")
			}
			if mode == "allocated" || mode == "changed-receipt" {
				if err := f.e.RecordReceipt(key, mustProviderRecoveryJSON(intent)); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "changed-readback" {
				f.policies[name] = "foreign current policy"
			}
			if mode == "changed-receipt" {
				entry.Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"foreign": true})
			}
			if mode == "changed-intent" {
				entry.IntentSHA256 = deploymentplan.Digest("foreign durable intent")
			}
			if err := f.e.save(); err != nil {
				t.Fatal(err)
			}
			writes := 0
			f.nativeOverride = func(args []string) ([]byte, int, bool) {
				joined := strings.Join(args, " ")
				if strings.Contains(joined, "bao policy list -format=json") {
					if mode == "list-failure" {
						return nil, 1, true
					}
					var names = []string{}
					for n := range f.policies {
						names = append(names, n)
					}
					return mustProviderRecoveryJSON(names), 0, true
				}
				if strings.Contains(joined, "bao policy read "+name) && mode == "read-failure" {
					return nil, 1, true
				}
				if strings.Contains(joined, "bao policy write "+name) {
					writes++
				}
				return nil, 0, false
			}
			err := installEnvironmentCreateOpenBaoPolicy(t.Context(), f.cfg, f.runtime, name, text)
			valid := mode == "allocated" || mode == "pending-before-write" || mode == "pending-after-write"
			if valid {
				if err != nil || entry.State != "completed" {
					t.Fatal("known native outcome not closed", err)
				}
			} else if err == nil {
				t.Fatal("uncertain or changed policy accepted")
			}
			wantWrites := 0
			if mode == "pending-before-write" {
				wantWrites = 1
			}
			if writes != wantWrites {
				t.Fatal("unexpected repeated native write", writes, wantWrites)
			}
		})
	}
}
