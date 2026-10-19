package main

import (
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestProductSessionRenewalRoleReconcilesOnlyExactNativeHistory(t *testing.T) {
	for _, mode := range []string{"before-write", "lost-after-write", "new-absent", "previous-allocated", "foreign-role", "prior-disappeared", "foreign-prior-receipt", "list-failure", "read-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newProductForwardFixture(t)
			name := "pki-product-bootstrap-aaaaaaaaaaaa"
			accounts, oldPolicies, newPolicies := []string{name}, []string{"old-signer"}, []string{"new-signer"}
			if mode != "new-absent" {
				if err := configureEnvironmentCreateOpenBaoRole(t.Context(), f.cfg, f.runtime, name, accounts, oldPolicies); err != nil {
					t.Fatal(err)
				}
			}
			intent := deploymentplan.Object{"role": name, "namespace": f.p.Stack + "-video-cloud", "service_accounts": accounts, "policies": newPolicies, "audience": "openbao", "ttl": "15m", "no_default_policy": true}
			key := "openbao-role-" + name + "-" + deploymentplan.Digest(intent)[:12]
			if mode == "lost-after-write" {
				f.nativeOverride = func(args []string) ([]byte, int, bool) {
					if strings.Contains(strings.Join(args, " "), "bao write auth/kubernetes/role/"+name) {
						f.roles[name] = newPolicies
						return nil, 1, true
					}
					return nil, 0, false
				}
				if err := configureEnvironmentCreateOpenBaoRole(t.Context(), f.cfg, f.runtime, name, accounts, newPolicies); err == nil {
					t.Fatal("lost native response closed")
				}
			} else if err := f.e.BeginEntry(key, "bootstrap", mustProviderRecoveryJSON(intent), time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			for i := range f.e.Journal.Entries {
				entry := &f.e.Journal.Entries[i]
				if entry.Key == key || !strings.HasPrefix(entry.Key, "openbao-role-"+name+"-") {
					continue
				}
				if mode == "previous-allocated" {
					entry.State = "allocated"
					entry.CompletedAt = time.Time{}
				}
				if mode == "foreign-prior-receipt" {
					entry.Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"foreign": true})
				}
			}
			if mode == "foreign-role" {
				f.roles[name] = []string{"foreign-signer"}
			}
			if mode == "prior-disappeared" {
				delete(f.roles, name)
			}
			if err := f.e.save(); err != nil {
				t.Fatal(err)
			}
			writes := 0
			f.nativeOverride = func(args []string) ([]byte, int, bool) {
				joined := strings.Join(args, " ")
				if strings.Contains(joined, "bao list -format=json auth/kubernetes/role") {
					if mode == "list-failure" {
						return nil, 1, true
					}
					names := []string{}
					for n := range f.roles {
						names = append(names, n)
					}
					return mustProviderRecoveryJSON(names), 0, true
				}
				if mode == "read-failure" && strings.Contains(joined, "bao read -format=json auth/kubernetes/role/"+name) {
					return nil, 1, true
				}
				if strings.Contains(joined, "bao write auth/kubernetes/role/"+name) {
					writes++
				}
				return nil, 0, false
			}
			err := configureEnvironmentCreateOpenBaoRole(t.Context(), f.cfg, f.runtime, name, accounts, newPolicies)
			valid := mode == "before-write" || mode == "lost-after-write" || mode == "new-absent" || mode == "previous-allocated"
			if valid {
				entry := createOpenBaoACKCompletionEntry(f.e.Journal, key)
				if err != nil || entry == nil || entry.State != "completed" {
					t.Fatal("genuine pending role did not reconcile", err)
				}
			} else if err == nil {
				t.Fatal("foreign or ambiguous role accepted")
			}
			want := 0
			if mode == "before-write" || mode == "new-absent" || mode == "previous-allocated" {
				want = 1
			}
			if writes != want {
				t.Fatal("unexpected native role writes", writes, want)
			}
		})
	}
}
