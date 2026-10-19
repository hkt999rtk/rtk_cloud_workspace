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

func managedLatePostNativeAuditFixture(t *testing.T) (*createManagedLateRemaining, createFoundationRecoveryArtifact, time.Time) {
	t.Helper()
	r, a, terminalStart := managedLateTerminalAuditFixture(t)
	phase, err := newManagedLateTerminalFinalizerAuthority(r, a, terminalStart.Add(90*time.Minute), terminalStart, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a)))
	if err != nil {
		t.Fatal(err)
	}
	r.TerminalAuthority = phase
	now := terminalStart.Add(35 * time.Minute)
	r.PostNativeCleanup = &createManagedLatePostNativeCleanup{
		Schema: managedLatePostNativeCleanupSchema, SourceSHA: strings.Repeat("1", 64), TerminalSourceSHA: phase.SourceSHA,
		TerminalAuthoritySHA: deploymentplan.Digest(phase), CustodyEntrySHA: strings.Repeat("2", 64), TombstoneSHA: strings.Repeat("3", 64),
		Native:         environmentCreateBootstrapFinalization{Environment: r.Ceremony.Root.Environment, SessionID: r.SessionID, LeafSHA256: r.Fingerprint, CRLSHA256: strings.Repeat("4", 64), CleanupSHA256: strings.Repeat("5", 64), FinalizedAt: terminalStart.Add(5 * time.Minute)},
		NativeEvidence: map[string]string{},
	}
	for key, value := range r.TerminalFinalizerCompletion.NativeEvidence {
		r.PostNativeCleanup.NativeEvidence[key] = value
	}
	a.Plan.ParentJournalSHA = r.PostNativeCleanup.SourceSHA
	a.Plan.CheckerSHA = strings.Repeat("6", 64)
	a.Plan.Materials[managedLateRemainingMaterial+"post-native-cleanup"] = deploymentplan.Digest(r.PostNativeCleanup)
	a.Plan.Materials[managedLateRemainingMaterial+"post-native-policy"] = managedLatePostNativeCleanupPolicySHA()
	a.QualifiedAt, a.ValidUntil = now.Add(-time.Minute), now.Add(14*time.Minute)
	managedLateRemainingNegativeBind(t, r, &a)
	return r, a, now
}

func TestManagedLatePostNativeCleanupAuditAuthorityCannotReopenNativeWindow(t *testing.T) {
	for _, mode := range []string{"valid-after-terminal-expiry", "caller-cap", "old-overall-cap", "less-than-fifteen", "native-before-old", "native-after-old", "wrong-session", "wrong-fingerprint", "missing-tombstone", "missing-evidence", "wrong-source", "wrong-owner", "wrong-review", "missing-post-pin", "wrong-policy", "failed-gate", "wrong-gate-evidence", "expired-go", "missing-raw-pin"} {
		t.Run(mode, func(t *testing.T) {
			r, a, now := managedLatePostNativeAuditFixture(t)
			oldTerminal := deploymentplan.Digest(r.TerminalAuthority)
			overall, expected := now.Add(90*time.Minute), now.Add(30*time.Minute)
			pass := mode == "valid-after-terminal-expiry" || mode == "caller-cap" || mode == "old-overall-cap"
			switch mode {
			case "caller-cap":
				overall = now.Add(20 * time.Minute)
				expected = overall
			case "old-overall-cap":
				now = r.TerminalAuthority.OverallDeadline.Add(-20 * time.Minute)
				overall = now.Add(90 * time.Minute)
				expected = r.TerminalAuthority.OverallDeadline
				a.QualifiedAt, a.ValidUntil = now.Add(-time.Minute), now.Add(14*time.Minute)
			case "less-than-fifteen":
				overall = now.Add(15*time.Minute - time.Nanosecond)
			case "native-before-old":
				r.PostNativeCleanup.Native.FinalizedAt = r.TerminalAuthority.StartedAt.Add(-time.Nanosecond)
			case "native-after-old":
				r.PostNativeCleanup.Native.FinalizedAt = r.TerminalAuthority.Deadline.Add(time.Nanosecond)
			case "wrong-session":
				r.PostNativeCleanup.Native.SessionID = "99999999-9999-4999-8999-999999999999"
			case "wrong-fingerprint":
				r.PostNativeCleanup.Native.LeafSHA256 = strings.Repeat("9", 64)
			case "missing-tombstone":
				r.PostNativeCleanup.TombstoneSHA = ""
			case "missing-evidence":
				delete(r.PostNativeCleanup.NativeEvidence, "copies")
			case "wrong-source":
				a.Plan.ParentJournalSHA = strings.Repeat("9", 64)
			case "wrong-owner":
				a.Plan.ParentOwnerSHA = strings.Repeat("9", 64)
			case "wrong-review":
				r.Review = strings.Repeat("9", 64)
			case "missing-post-pin":
				delete(a.Plan.Materials, managedLateRemainingMaterial+"post-native-cleanup")
			case "wrong-policy":
				a.Plan.Materials[managedLateRemainingMaterial+"post-native-policy"] = strings.Repeat("9", 64)
			case "failed-gate":
				a.Qualification.Gates[3].Status = "BLOCKED"
			case "wrong-gate-evidence":
				a.Qualification.Gates[3].EvidenceSHA256 = strings.Repeat("9", 64)
			case "expired-go":
				now = a.ValidUntil
			}
			rawSHA := environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a))
			if mode == "missing-raw-pin" {
				rawSHA = ""
			}
			before := deploymentplan.Digest([]any{r, a})
			got, err := newManagedLatePostNativeCleanupAuthority(r, a, overall, now, rawSHA)
			if (err == nil) != pass {
				t.Fatalf("acceptance differs: %v", err)
			}
			if pass && (!got.Deadline.Equal(expected) || got.OverallDeadline.After(r.TerminalAuthority.OverallDeadline) || got.TerminalAuthoritySHA != oldTerminal || got.PendingSHA != r.TerminalFinalizerCompletion.PendingSHA || got.QualifiedBytesSHA != rawSHA) {
				t.Fatal("new local-cleanup clock changed old native authority or lost an absolute cap")
			}
			if deploymentplan.Digest([]any{r, a}) != before || deploymentplan.Digest(r.TerminalAuthority) != oldTerminal {
				t.Fatal("authority constructor changed immutable source")
			}
		})
	}
}

func TestManagedLatePostNativeCleanupAuditPairCannotRegenerate(t *testing.T) {
	for _, mode := range []string{"same", "primary-missing", "witness-missing", "different", "public-witness", "symlink-witness"} {
		t.Run(mode, func(t *testing.T) {
			store, parent, _, _ := terminalWitnessFixture(t)
			primary := filepath.Join(store.Root, managedLatePostNativeCleanupPath(parent, false))
			witness := filepath.Join(store.Root, managedLatePostNativeCleanupPath(parent, true))
			raw := []byte(`{"first_clock":"2030-01-01T00:00:00Z"}`)
			if got, err := readManagedLatePostNativeCleanupPair(store, parent); err != nil || got != nil {
				t.Fatal("first absence refused", err)
			}
			if err := writeManagedLatePostNativeCleanupPair(store, parent, raw); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "primary-missing":
				if err := os.Remove(primary); err != nil {
					t.Fatal(err)
				}
			case "witness-missing":
				if err := os.Remove(witness); err != nil {
					t.Fatal(err)
				}
			case "different":
				if err := os.WriteFile(witness, []byte(`{"replacement":true}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "public-witness":
				if err := os.Chmod(witness, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink-witness":
				if err := os.Remove(witness); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(primary, witness); err != nil {
					t.Fatal(err)
				}
			}
			got, err := readManagedLatePostNativeCleanupPair(store, parent)
			if mode == "same" {
				if err != nil || string(got) != string(raw) {
					t.Fatal("first paired clock changed", err)
				}
			} else if err == nil {
				t.Fatal("unsafe or orphan admission accepted")
			}
			if err := writeManagedLatePostNativeCleanupPair(store, parent, []byte(`{"reset":true}`)); err == nil {
				t.Fatal("existing attempted admission regenerated")
			}
		})
	}
}

func TestManagedLatePostNativeCleanupAuditPreservesOriginalDescriptorWire(t *testing.T) {
	r, _, _ := managedLatePostNativeAuditFixture(t)
	want := *r
	want.PostNativeCleanup, want.PostNativeAuthority = nil, nil
	if managedLateRemainingDigest(r) != managedLateRemainingDigest(&want) {
		t.Fatal("post-native fields changed original Remaining descriptor")
	}
	typ, value := reflect.TypeOf(want), reflect.ValueOf(want)
	var fields []reflect.StructField
	var indices []int
	for i := range typ.NumField() {
		if typ.Field(i).PkgPath != "" || typ.Field(i).Tag.Get("json") == "-" {
			continue
		}
		if typ.Field(i).Name != "PostNativeCleanup" && typ.Field(i).Name != "PostNativeAuthority" {
			fields = append(fields, typ.Field(i))
			indices = append(indices, i)
		}
	}
	legacy := reflect.New(reflect.StructOf(fields)).Elem()
	for i, index := range indices {
		legacy.Field(i).Set(value.Field(index))
	}
	before, _ := json.Marshal(legacy.Interface())
	after, _ := json.Marshal(want)
	if string(before) != string(after) {
		t.Fatal("new nil fields changed original serialized descriptor")
	}
}
