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

func managedLateInstalledCleanupNegativeFixture(t *testing.T) (*createManagedLateRemaining, createFoundationRecoveryArtifact, createFoundationRecoveryArtifact, time.Time) {
	t.Helper()
	r, old, now := managedLateRemainingNegativeFixture(t)
	r.NotBefore = now.Add(-115 * time.Minute)
	r.SourceLeases = 2
	old.QualifiedAt = now.Add(-101 * time.Minute)
	old.ValidUntil = old.QualifiedAt.Add(15 * time.Minute)
	old.Qualification = deploymentplan.Qualification{}
	managedLateRemainingNegativeBind(t, r, &old)
	original, err := newManagedLateRemainingAuthority(r, old, now.Add(-10*time.Minute), now.Add(-100*time.Minute), environmentPreflightBytesSHA256(mustProviderRecoveryJSON(old)))
	if err != nil {
		t.Fatal(err)
	}
	r.Authority = original
	r.CurrentSourceSHA = strings.Repeat("3", 64)
	r.CleanupSourceSHA = r.CurrentSourceSHA
	r.HelperObservationSHA = strings.Repeat("4", 64)
	r.CleanupOnly = true
	r.InstalledAt = now.Add(-20 * time.Minute)
	var fresh createFoundationRecoveryArtifact
	if err := json.Unmarshal(mustProviderRecoveryJSON(old), &fresh); err != nil {
		t.Fatal(err)
	}
	fresh.Plan.ParentJournalSHA = r.CurrentSourceSHA
	fresh.Plan.CheckerSHA = strings.Repeat("5", 64)
	fresh.QualifiedAt = now.Add(-time.Minute)
	fresh.ValidUntil = fresh.QualifiedAt.Add(15 * time.Minute)
	fresh.Qualification = deploymentplan.Qualification{}
	for key, value := range managedLateRemainingPins(r) {
		fresh.Plan.Materials[managedLateRemainingMaterial+key] = value
	}
	managedLateRemainingNegativeBind(t, r, &fresh)
	return r, old, fresh, now
}

func TestManagedLateInstalledCleanupNegativePreservesOldRemainingWire(t *testing.T) {
	r, _, _, _ := managedLateInstalledCleanupNegativeFixture(t)
	normalized := *r
	normalized.Review = ""
	normalized.Authority = nil
	normalized.CurrentSourceSHA, normalized.HelperObservationSHA = "", ""
	normalized.CleanupOnly, normalized.CleanupSourceSHA, normalized.InstalledAt, normalized.CleanupAuthority = false, "", time.Time{}, nil
	// Rebuild the exact pre-extension struct field order and JSON tags. Maps
	// would sort keys and could conceal a change to the historical wire digest.
	typ := reflect.TypeOf(normalized)
	value := reflect.ValueOf(normalized)
	removed := map[string]bool{"CleanupOnly": true, "CleanupSourceSHA": true, "InstalledAt": true, "CleanupAuthority": true, "CurrentSourceSHA": true, "HelperObservationSHA": true}
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
		t.Fatal("new optional cleanup fields changed original97 wire digest")
	}
	changed := *r
	changed.KeySHA = strings.Repeat("e", 64)
	if managedLateRemainingDigest(&changed) == managedLateRemainingDigest(r) {
		t.Fatal("immutable original identity disappeared from wire digest")
	}
}

func TestManagedLateInstalledCleanupNegativeFixedFreshThirtyMinuteScope(t *testing.T) {
	for _, mode := range []string{"valid-after-original-deadline", "caller-cap", "exact-fifteen", "too-short", "caller-over-ninety", "not-cleanup", "missing-original-authority", "source-mismatch", "zero-installed", "installed-after-old-deadline", "future-installed", "missing-helper", "old-policy", "changed-source-plan", "changed-owner-plan", "old-go", "future-go", "missing-gate", "failed-gate", "optional-gate", "wrong-gate-review", "empty-raw-pin"} {
		t.Run(mode, func(t *testing.T) {
			r, _, a, now := managedLateInstalledCleanupNegativeFixture(t)
			oldSHA := deploymentplan.Digest(r.Authority)
			overall := now.Add(90 * time.Minute)
			expected := now.Add(30 * time.Minute)
			bytesSHA := environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a))
			pass := mode == "valid-after-original-deadline" || mode == "caller-cap" || mode == "exact-fifteen"
			switch mode {
			case "caller-cap":
				overall = now.Add(20 * time.Minute)
				expected = overall
			case "exact-fifteen":
				overall = now.Add(15 * time.Minute)
				expected = overall
			case "too-short":
				overall = now.Add(15*time.Minute - time.Nanosecond)
			case "caller-over-ninety":
				overall = now.Add(90*time.Minute + time.Nanosecond)
			case "not-cleanup":
				r.CleanupOnly = false
			case "missing-original-authority":
				r.Authority = nil
			case "source-mismatch":
				r.CurrentSourceSHA = strings.Repeat("e", 64)
			case "zero-installed":
				r.InstalledAt = time.Time{}
			case "installed-after-old-deadline":
				r.InstalledAt = r.Authority.Deadline.Add(time.Nanosecond)
			case "future-installed":
				r.InstalledAt = now.Add(time.Second)
			case "missing-helper":
				r.HelperObservationSHA = ""
			case "old-policy":
				a.Plan.Materials[managedLateRemainingMaterial+"cleanup-policy"] = managedLateRemainingPolicySHA()
			case "changed-source-plan":
				a.Plan.ParentJournalSHA = strings.Repeat("e", 64)
			case "changed-owner-plan":
				a.Plan.ParentOwnerSHA = strings.Repeat("e", 64)
			case "old-go":
				now = a.ValidUntil
			case "future-go":
				now = a.QualifiedAt.Add(-time.Nanosecond)
			case "missing-gate":
				a.Qualification.Gates = a.Qualification.Gates[:5]
			case "failed-gate":
				a.Qualification.Gates[3].Status = "BLOCKED"
			case "optional-gate":
				a.Qualification.Gates[3].Required = false
			case "wrong-gate-review":
				a.Qualification.Gates[3].EvidenceSHA256 = strings.Repeat("e", 64)
			case "empty-raw-pin":
				bytesSHA = ""
			}
			before := deploymentplan.Digest([]any{r, a})
			got, err := newManagedLateInstalledCleanupAuthority(r, a, overall, now, bytesSHA)
			if (err == nil) != pass {
				t.Fatalf("cleanup-only acceptance differs: %v", err)
			}
			if pass && (!got.StartedAt.Equal(now) || !got.Deadline.Equal(expected) || got.OriginalAuthoritySHA != oldSHA || got.SourceSHA != r.CleanupSourceSHA || got.HelperObservationSHA != r.HelperObservationSHA || got.SourceLeases != r.SourceLeases+1 || !got.InstalledAt.Equal(r.InstalledAt)) {
				t.Fatal("fresh cleanup phase lost original authority/current helper or fixed limits")
			}
			if deploymentplan.Digest([]any{r, a}) != before {
				t.Fatal("cleanup producer modified old proofs")
			}
		})
	}
	now := time.Now().UTC()
	if got := managedLateInstalledCleanupDeadline(now, now.Add(90*time.Minute), now.Add(25*time.Minute), now.Add(25*time.Minute)); !got.Equal(now.Add(20 * time.Minute)) {
		t.Fatal("genuine expiry five-minute reserve lost")
	}
}

func TestManagedLateInstalledCleanupNegativeImmutableMarkerAndExactRawGO(t *testing.T) {
	for _, mode := range []string{"valid-retry", "absent", "malformed", "missing-raw-go", "changed-raw-go", "extended", "changed-epoch", "changed-old-authority", "changed-helper", "changed-current-source", "unsafe-mode", "symlink", "noncanonical"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			r, old, a, now := managedLateInstalledCleanupNegativeFixture(t)
			got, err := newManagedLateInstalledCleanupAuthority(r, a, now.Add(90*time.Minute), now, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a)))
			if err != nil {
				t.Fatal(err)
			}
			for review, artifact := range map[string]createFoundationRecoveryArtifact{r.Authority.Review: old, r.Review: a} {
				if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-recovery", review, "qualification.json"), mustProviderRecoveryJSON(artifact)); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(store.Root, managedLateInstalledCleanupPath(r.ParentSHA))
			if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
				t.Fatal(err)
			}
			raw := mustProviderRecoveryJSON(got)
			switch mode {
			case "malformed":
				raw = []byte(`{"unknown":"orphan"}`)
			case "extended":
				got.Deadline = got.Deadline.Add(time.Second)
				raw = mustProviderRecoveryJSON(got)
			case "changed-epoch":
				got.StartedAt = got.StartedAt.Add(time.Second)
				raw = mustProviderRecoveryJSON(got)
			case "noncanonical":
				raw = append(raw, '\n')
			}
			if mode != "absent" {
				if err := writeExclusivePrivateDeploymentJSON(path, raw); err != nil {
					t.Fatal(err)
				}
			}
			qualification := filepath.Join(store.Root, "deployment/create/openbao-recovery", r.Review, "qualification.json")
			switch mode {
			case "missing-raw-go":
				if err := os.Remove(qualification); err != nil {
					t.Fatal(err)
				}
			case "changed-raw-go":
				if err := os.WriteFile(qualification, append(mustProviderRecoveryJSON(a), '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed-old-authority":
				copy := *r.Authority
				copy.QualifiedBytesSHA = strings.Repeat("e", 64)
				r.Authority = &copy
			case "changed-helper":
				r.HelperObservationSHA = strings.Repeat("e", 64)
			case "changed-current-source":
				r.CleanupSourceSHA = strings.Repeat("e", 64)
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
			loaded, err := readManagedLateInstalledCleanupAuthority(store, r)
			if mode == "absent" {
				if err != nil || loaded != nil {
					t.Fatal("missing unadmitted cleanup marker manufactured", err)
				}
				return
			}
			if mode == "valid-retry" {
				if err != nil || deploymentplan.Digest(loaded) != deploymentplan.Digest(got) {
					t.Fatal("same admitted cleanup clock not reusable", err)
				}
				if err := writeExclusivePrivateDeploymentJSON(path, []byte(`{"reset":true}`)); err == nil {
					t.Fatal("cleanup clock could be replaced")
				}
				later, err := readManagedLateInstalledCleanupAuthority(store, r)
				if err != nil || !later.StartedAt.Equal(got.StartedAt) || !later.Deadline.Equal(got.Deadline) {
					t.Fatal("retry advanced cleanup epoch", err)
				}
			} else if err == nil {
				t.Fatal("changed or orphan cleanup authority accepted")
			}
		})
	}
}
