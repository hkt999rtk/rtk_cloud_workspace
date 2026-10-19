package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateFinalizationNegativeFixture(t *testing.T) (*createManagedLateRemaining, createFoundationRecoveryArtifact, time.Time) {
	t.Helper()
	r, _, installed, now := managedLateInstalledCleanupNegativeFixture(t)
	r.InstalledAt = now.Add(-80 * time.Minute)
	installed.QualifiedAt = now.Add(-51 * time.Minute)
	installed.ValidUntil = installed.QualifiedAt.Add(15 * time.Minute)
	installed.Qualification = deploymentplan.Qualification{}
	for key, value := range managedLateRemainingPins(r) {
		installed.Plan.Materials[managedLateRemainingMaterial+key] = value
	}
	managedLateRemainingNegativeBind(t, r, &installed)
	prior, err := newManagedLateInstalledCleanupAuthority(r, installed, now.Add(40*time.Minute), now.Add(-50*time.Minute), environmentPreflightBytesSHA256(mustProviderRecoveryJSON(installed)))
	if err != nil {
		t.Fatal(err)
	}
	r.CleanupAuthority = prior
	r.FinalizationOnly = true
	r.LogProofSHA = strings.Repeat("6", 64)
	r.CurrentSourceSHA = strings.Repeat("7", 64)
	var a createFoundationRecoveryArtifact
	if err := json.Unmarshal(mustProviderRecoveryJSON(installed), &a); err != nil {
		t.Fatal(err)
	}
	a.Plan.ParentJournalSHA = r.CurrentSourceSHA
	a.Plan.CheckerSHA = strings.Repeat("8", 64)
	a.QualifiedAt = now.Add(-time.Minute)
	a.ValidUntil = a.QualifiedAt.Add(15 * time.Minute)
	a.Qualification = deploymentplan.Qualification{}
	for key, value := range managedLateRemainingPins(r) {
		a.Plan.Materials[managedLateRemainingMaterial+key] = value
	}
	a.Plan.Materials[managedLateRemainingMaterial+"finalization-policy"] = managedLateFinalizationPolicySHA()
	a.Plan.Materials[managedLateRemainingMaterial+"log-proof"] = r.LogProofSHA
	managedLateRemainingNegativeBind(t, r, &a)
	return r, a, now
}

func TestManagedLateFinalizationNegativeKeepsBothHistoricalAuthoritiesAndOldWire(t *testing.T) {
	r, a, now := managedLateFinalizationNegativeFixture(t)
	before := deploymentplan.Digest([]any{r.Authority, r.CleanupAuthority})
	if !now.After(r.Authority.Deadline) || !now.After(r.CleanupAuthority.Deadline) {
		t.Fatal("fixture did not distinguish elapsed historical phases")
	}
	got, err := newManagedLateFinalizationAuthority(r, a, now.Add(90*time.Minute), now, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a)))
	if err != nil {
		t.Fatal("independently qualified finalization phase refused", err)
	}
	if !got.StartedAt.Equal(now) || !got.Deadline.Equal(now.Add(50*time.Minute)) || got.OriginalAuthoritySHA != deploymentplan.Digest(r.Authority) || got.OriginalInstalledAuthoritySHA != deploymentplan.Digest(r.CleanupAuthority) || got.OriginalQualifiedBytesSHA != r.Authority.QualifiedBytesSHA || got.OriginalInstalledQualifiedBytesSHA != r.CleanupAuthority.QualifiedBytesSHA || got.LogProofSHA != r.LogProofSHA || got.SourceSHA != r.CurrentSourceSHA || got.SourceLeases != r.SourceLeases+3 {
		t.Fatal("new finalization authority lost fixed limits or historical linkage")
	}
	if deploymentplan.Digest([]any{r.Authority, r.CleanupAuthority}) != before {
		t.Fatal("finalization rewrote a prior clock")
	}
	normalized := *r
	normalized.Review = ""
	normalized.Authority = nil
	normalized.CurrentSourceSHA, normalized.HelperObservationSHA = "", ""
	normalized.CleanupOnly, normalized.CleanupSourceSHA, normalized.InstalledAt, normalized.CleanupAuthority = false, "", time.Time{}, nil
	normalized.FinalizationOnly, normalized.LogProofSHA, normalized.FinalizationAuthority = false, "", nil
	typ, value := reflect.TypeOf(normalized), reflect.ValueOf(normalized)
	removed := map[string]bool{"CleanupOnly": true, "CleanupSourceSHA": true, "InstalledAt": true, "CleanupAuthority": true, "CurrentSourceSHA": true, "HelperObservationSHA": true, "FinalizationOnly": true, "LogProofSHA": true, "FinalizationAuthority": true}
	fields := []reflect.StructField{}
	indices := []int{}
	for i := range typ.NumField() {
		if typ.Field(i).PkgPath != "" || typ.Field(i).Tag.Get("json") == "-" {
			continue
		}
		if !removed[typ.Field(i).Name] {
			fields = append(fields, typ.Field(i))
			indices = append(indices, i)
		}
	}
	old := reflect.New(reflect.StructOf(fields)).Elem()
	for i, index := range indices {
		old.Field(i).Set(value.Field(index))
	}
	raw, err := json.Marshal(old.Interface())
	if err != nil {
		t.Fatal(err)
	}
	if managedLateRemainingDigest(r) != environmentPreflightBytesSHA256(raw) {
		t.Fatal("optional finalization fields changed original remaining97 wire")
	}
}

func TestManagedLateFinalizationNegativeFreshGOAndNoReplayAuthority(t *testing.T) {
	for _, mode := range []string{"caller-cap", "exact-fifteen", "too-short", "caller-over-ninety", "not-finalization", "missing-installed-authority", "changed-old-authority", "changed-installed-authority", "missing-log-proof", "changed-plan-source", "changed-owner", "missing-policy", "wrong-policy", "missing-log-pin", "wrong-log-pin", "wrong-installed-pin", "old-go", "future-go", "missing-gate", "failed-gate", "wrong-gate-evidence", "empty-raw-pin"} {
		t.Run(mode, func(t *testing.T) {
			r, a, now := managedLateFinalizationNegativeFixture(t)
			overall := now.Add(90 * time.Minute)
			expected := now.Add(50 * time.Minute)
			pass := mode == "caller-cap" || mode == "exact-fifteen"
			rawSHA := environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a))
			switch mode {
			case "caller-cap":
				overall = now.Add(25 * time.Minute)
				expected = overall
			case "exact-fifteen":
				overall = now.Add(15 * time.Minute)
				expected = overall
			case "too-short":
				overall = now.Add(15*time.Minute - time.Nanosecond)
			case "caller-over-ninety":
				overall = now.Add(90*time.Minute + time.Nanosecond)
			case "not-finalization":
				r.FinalizationOnly = false
			case "missing-installed-authority":
				r.CleanupAuthority = nil
			case "changed-old-authority":
				copy := *r.Authority
				copy.QualifiedBytesSHA = strings.Repeat("e", 64)
				r.Authority = &copy
			case "changed-installed-authority":
				copy := *r.CleanupAuthority
				copy.QualifiedBytesSHA = strings.Repeat("e", 64)
				r.CleanupAuthority = &copy
			case "missing-log-proof":
				r.LogProofSHA = ""
			case "changed-plan-source":
				a.Plan.ParentJournalSHA = strings.Repeat("e", 64)
			case "changed-owner":
				a.Plan.ParentOwnerSHA = strings.Repeat("e", 64)
			case "missing-policy":
				delete(a.Plan.Materials, managedLateRemainingMaterial+"finalization-policy")
			case "wrong-policy":
				a.Plan.Materials[managedLateRemainingMaterial+"finalization-policy"] = managedLateInstalledCleanupPolicySHA()
			case "missing-log-pin":
				delete(a.Plan.Materials, managedLateRemainingMaterial+"log-proof")
			case "wrong-log-pin":
				a.Plan.Materials[managedLateRemainingMaterial+"log-proof"] = strings.Repeat("e", 64)
			case "wrong-installed-pin":
				a.Plan.Materials[managedLateRemainingMaterial+"cleanup-authority"] = strings.Repeat("e", 64)
			case "old-go":
				now = a.ValidUntil
			case "future-go":
				now = a.QualifiedAt.Add(-time.Nanosecond)
			case "missing-gate":
				a.Qualification.Gates = a.Qualification.Gates[:5]
			case "failed-gate":
				a.Qualification.Gates[3].Status = "BLOCKED"
			case "wrong-gate-evidence":
				a.Qualification.Gates[3].EvidenceSHA256 = strings.Repeat("e", 64)
			case "empty-raw-pin":
				rawSHA = ""
			}
			before := deploymentplan.Digest([]any{r, a})
			got, err := newManagedLateFinalizationAuthority(r, a, overall, now, rawSHA)
			if (err == nil) != pass {
				t.Fatalf("finalization authority acceptance differs: %v", err)
			}
			if pass && (!got.Deadline.Equal(expected) || !got.StartedAt.Equal(now)) {
				t.Fatal("fixed finalization deadline changed")
			}
			if deploymentplan.Digest([]any{r, a}) != before {
				t.Fatal("finalization validation mutated prior evidence")
			}
		})
	}
	now := time.Now().UTC()
	if got := managedLateFinalizationDeadline(now, now.Add(90*time.Minute), now.Add(25*time.Minute), now.Add(25*time.Minute)); !got.Equal(now.Add(20 * time.Minute)) {
		t.Fatal("expiry reserve lost")
	}
}

func TestManagedLateFinalizationNegativeImmutableMarkerRawAndBothPriorLinks(t *testing.T) {
	for _, mode := range []string{"valid-retry", "missing", "malformed", "changed-raw-go", "changed-source", "changed-installed-raw", "changed-log-proof", "changed-epoch", "extended", "unsafe-mode", "symlink", "noncanonical"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			r, a, now := managedLateFinalizationNegativeFixture(t)
			authority, err := newManagedLateFinalizationAuthority(r, a, now.Add(90*time.Minute), now, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a)))
			if err != nil {
				t.Fatal(err)
			}
			relative := filepath.Join("deployment/create/openbao-recovery", r.Review, "qualification.json")
			if err := writeEnvironmentCreateDerivedPrivateFile(store, relative, mustProviderRecoveryJSON(a)); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(store.Root, managedLateFinalizationAuthorityPath(r.ParentSHA))
			if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
				t.Fatal(err)
			}
			raw := mustProviderRecoveryJSON(authority)
			switch mode {
			case "malformed":
				raw = []byte(`{"orphan":true}`)
			case "changed-epoch":
				authority.StartedAt = authority.StartedAt.Add(time.Second)
				raw = mustProviderRecoveryJSON(authority)
			case "extended":
				authority.Deadline = authority.Deadline.Add(time.Second)
				raw = mustProviderRecoveryJSON(authority)
			case "noncanonical":
				raw = append(raw, '\n')
			}
			if mode != "missing" {
				if err := writeExclusivePrivateDeploymentJSON(path, raw); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "changed-raw-go":
				if err := os.WriteFile(filepath.Join(store.Root, relative), append(mustProviderRecoveryJSON(a), '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed-source":
				r.CurrentSourceSHA = strings.Repeat("e", 64)
			case "changed-installed-raw":
				copy := *r.CleanupAuthority
				copy.QualifiedBytesSHA = strings.Repeat("e", 64)
				r.CleanupAuthority = &copy
			case "changed-log-proof":
				r.LogProofSHA = strings.Repeat("e", 64)
			case "unsafe-mode":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".original", path); err != nil {
					t.Fatal(err)
				}
			}
			loaded, err := readManagedLateFinalizationAuthority(store, r)
			if mode == "missing" {
				if err != nil || loaded != nil {
					t.Fatal("missing finalization authority manufactured", err)
				}
				return
			}
			if mode == "valid-retry" {
				if err != nil || deploymentplan.Digest(loaded) != deploymentplan.Digest(authority) {
					t.Fatal("immutable finalization authority not reusable", err)
				}
				if err := writeExclusivePrivateDeploymentJSON(path, []byte(`{"reset":true}`)); err == nil {
					t.Fatal("retry reset finalization authority")
				}
				later, err := readManagedLateFinalizationAuthority(store, r)
				if err != nil || !later.StartedAt.Equal(authority.StartedAt) || !later.Deadline.Equal(authority.Deadline) {
					t.Fatal("retry advanced finalization clock", err)
				}
			} else if err == nil {
				t.Fatal("changed or orphan finalization authority accepted")
			}
		})
	}
}
