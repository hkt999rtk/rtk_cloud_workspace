package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateTerminalAuditFixture(t *testing.T) (*createManagedLateRemaining, createFoundationRecoveryArtifact, time.Time) {
	t.Helper()
	r, old, origin := managedLateFinalizationNegativeFixture(t)
	phase, err := newManagedLateFinalizationAuthority(r, old, origin.Add(90*time.Minute), origin, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(old)))
	if err != nil {
		t.Fatal(err)
	}
	r.FinalizationAuthority = phase
	now := origin.Add(55 * time.Minute)
	r.TerminalFinalizerCompletion = &createManagedLateTerminalFinalizerCompletion{
		Schema: managedLateTerminalFinalizerSchema, SourceSHA: strings.Repeat("a", 64), PrefixSHA: r.CurrentSourceSHA,
		ProofSHA: strings.Repeat("b", 64), PendingSHA: strings.Repeat("c", 64), OriginalAuthoritySHA: deploymentplan.Digest(phase),
		NativeEvidence: map[string]string{},
	}
	for _, key := range []string{"owners", "all-volumes", "clients", "host-servers", "api-servers", "ca-registry", "current-crls", "sealed-session", "revocation", "service-crl", "service-crl-acks", "copies", "key", "csr"} {
		r.TerminalFinalizerCompletion.NativeEvidence[key] = strings.Repeat("d", 64)
	}
	var a createFoundationRecoveryArtifact
	if err := json.Unmarshal(mustProviderRecoveryJSON(old), &a); err != nil {
		t.Fatal(err)
	}
	a.Plan.ParentJournalSHA = r.TerminalFinalizerCompletion.SourceSHA
	a.Plan.CheckerSHA = strings.Repeat("f", 64)
	a.Plan.Materials[managedLateRemainingMaterial+"terminal-completion"] = deploymentplan.Digest(r.TerminalFinalizerCompletion)
	a.Plan.Materials[managedLateRemainingMaterial+"terminal-policy"] = managedLateTerminalFinalizerPolicySHA()
	a.QualifiedAt = now.Add(-time.Minute)
	a.ValidUntil = a.QualifiedAt.Add(15 * time.Minute)
	managedLateRemainingNegativeBind(t, r, &a)
	return r, a, now
}

func TestManagedLateTerminalCompletionAuditFixedOriginalOverallAndFreshAuthority(t *testing.T) {
	for _, mode := range []string{"valid-after-old-fifty", "after-old-overall", "exact-fifteen", "too-short", "caller-over-ninety", "leaf-reserve", "expiry-too-short", "wrong-source", "wrong-owner", "wrong-review", "missing-completion-pin", "wrong-policy", "expired-go", "future-go", "missing-gate", "optional-gate", "failed-gate", "wrong-gate-evidence", "empty-raw-hash", "malformed-native-evidence", "missing-native-evidence", "foreign-native-evidence"} {
		t.Run(mode, func(t *testing.T) {
			r, a, now := managedLateTerminalAuditFixture(t)
			if !now.After(r.FinalizationAuthority.Deadline) {
				t.Fatal("fixture did not preserve expired old fifty-minute phase")
			}
			pass := mode == "valid-after-old-fifty" || mode == "after-old-overall" || mode == "exact-fifteen" || mode == "leaf-reserve"
			overall := now.Add(90 * time.Minute)
			expected := now.Add(30 * time.Minute)
			rawSHA := environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a))
			switch mode {
			case "after-old-overall":
				now = r.FinalizationAuthority.OverallDeadline.Add(5 * time.Minute)
				overall = now.Add(90 * time.Minute)
				expected = now.Add(30 * time.Minute)
				a.QualifiedAt = now.Add(-time.Minute)
				a.ValidUntil = a.QualifiedAt.Add(15 * time.Minute)
			case "exact-fifteen":
				overall = now.Add(15 * time.Minute)
				expected = overall
			case "too-short":
				overall = now.Add(15*time.Minute - time.Nanosecond)
			case "caller-over-ninety":
				overall = now.Add(90*time.Minute + time.Nanosecond)
			case "leaf-reserve":
				now = r.LeafNotAfter.Add(-25 * time.Minute)
				overall = now.Add(90 * time.Minute)
				expected = now.Add(20 * time.Minute)
				a.QualifiedAt = now.Add(-time.Minute)
				a.ValidUntil = a.QualifiedAt.Add(15 * time.Minute)
			case "expiry-too-short":
				now = r.LeafNotAfter.Add(-19 * time.Minute)
				overall = now.Add(90 * time.Minute)
				a.QualifiedAt = now.Add(-time.Minute)
				a.ValidUntil = a.QualifiedAt.Add(15 * time.Minute)
			case "wrong-source":
				a.Plan.ParentJournalSHA = strings.Repeat("1", 64)
			case "wrong-owner":
				a.Plan.ParentOwnerSHA = strings.Repeat("1", 64)
			case "wrong-review":
				r.Review = strings.Repeat("1", 64)
			case "missing-completion-pin":
				delete(a.Plan.Materials, managedLateRemainingMaterial+"terminal-completion")
			case "wrong-policy":
				a.Plan.Materials[managedLateRemainingMaterial+"terminal-policy"] = strings.Repeat("1", 64)
			case "expired-go":
				now = a.ValidUntil
			case "future-go":
				now = a.QualifiedAt.Add(-time.Nanosecond)
			case "missing-gate":
				a.Qualification.Gates = a.Qualification.Gates[:5]
			case "optional-gate":
				a.Qualification.Gates[3].Required = false
			case "failed-gate":
				a.Qualification.Gates[3].Status = "BLOCKED"
			case "wrong-gate-evidence":
				a.Qualification.Gates[3].EvidenceSHA256 = strings.Repeat("1", 64)
			case "empty-raw-hash":
				rawSHA = ""
			case "malformed-native-evidence":
				r.TerminalFinalizerCompletion.NativeEvidence["service-crl"] = "unverified"
			case "missing-native-evidence":
				delete(r.TerminalFinalizerCompletion.NativeEvidence, "service-crl")
			case "foreign-native-evidence":
				r.TerminalFinalizerCompletion.NativeEvidence["foreign"] = strings.Repeat("d", 64)
			}
			before := deploymentplan.Digest([]any{r, a})
			got, err := newManagedLateTerminalFinalizerAuthority(r, a, overall, now, rawSHA)
			if (err == nil) != pass {
				t.Fatalf("terminal authority acceptance differs: %v", err)
			}
			if pass && (!got.Deadline.Equal(expected) || !got.StartedAt.Equal(now) || !got.OriginalOverallDeadline.Equal(r.FinalizationAuthority.OverallDeadline) || !got.OverallDeadline.Equal(overall) || got.OriginalAuthoritySHA != deploymentplan.Digest(r.FinalizationAuthority) || got.QualifiedBytesSHA != rawSHA) {
				t.Fatal("terminal continuation changed historical authority or lost its fixed thirty/caller/expiry bound")
			}
			if deploymentplan.Digest([]any{r, a}) != before {
				t.Fatal("terminal authority modified historical evidence")
			}
		})
	}
}

func TestManagedLateTerminalCompletionAuditPendingIntentAndOriginalWire(t *testing.T) {
	r, _, now := managedLateTerminalAuditFixture(t)
	s := r.Ceremony
	s.BootstrapSessionID = r.SessionID
	s.Intermediate.ID = "11111111-1111-4111-8111-111111111111"
	intent := deploymentplan.Object{"session_id": s.BootstrapSessionID, "issuer_id": s.Intermediate.ID, "cleanup_binding": s.CleanupBinding}
	j := environmentCreateJournal{Entries: make([]environmentCreateJournalEntry, 396)}
	j.Entries[395] = environmentCreateJournalEntry{Key: "service-session-finalize", Phase: "bootstrap", State: "pending", StartedAt: now.Add(-25 * time.Minute), IntentSHA256: activeTransportJournalDigest(intent)}
	if !managedLateTerminalFinalizerPendingIntent(j, s) {
		t.Fatal("exact existing pending finalizer refused")
	}
	for _, mode := range []string{"short", "extra", "overall-complete", "duplicate", "wrong-phase", "wrong-state", "zero-start", "has-completion", "has-receipt", "changed-intent", "changed-session", "changed-issuer", "changed-cleanup"} {
		t.Run(mode, func(t *testing.T) {
			var changed environmentCreateJournal
			if err := json.Unmarshal(mustProviderRecoveryJSON(j), &changed); err != nil {
				t.Fatal(err)
			}
			ceremony := s
			switch mode {
			case "short":
				changed.Entries = changed.Entries[:395]
			case "extra":
				changed.Entries = append(changed.Entries, environmentCreateJournalEntry{})
			case "overall-complete":
				changed.CompletedAt = now
			case "duplicate":
				changed.Entries[394] = changed.Entries[395]
			case "wrong-phase":
				changed.Entries[395].Phase = "foreign"
			case "wrong-state":
				changed.Entries[395].State = "completed"
			case "zero-start":
				changed.Entries[395].StartedAt = time.Time{}
			case "has-completion":
				changed.Entries[395].CompletedAt = now
			case "has-receipt":
				changed.Entries[395].Receipt = json.RawMessage(`{"unverified":true}`)
			case "changed-intent":
				changed.Entries[395].IntentSHA256 = strings.Repeat("0", 64)
			case "changed-session":
				ceremony.BootstrapSessionID = "22222222-2222-4222-8222-222222222222"
			case "changed-issuer":
				ceremony.Intermediate.ID = "22222222-2222-4222-8222-222222222222"
			case "changed-cleanup":
				ceremony.CleanupBinding.SecretUID = "22222222-2222-4222-8222-222222222222"
			}
			if managedLateTerminalFinalizerPendingIntent(changed, ceremony) {
				t.Fatal("foreign or completed pending-finalizer intent accepted")
			}
		})
	}
	normalized := *r
	normalized.Review = ""
	normalized.Authority = nil
	normalized.CurrentSourceSHA, normalized.HelperObservationSHA = "", ""
	normalized.CleanupOnly, normalized.CleanupSourceSHA, normalized.InstalledAt, normalized.CleanupAuthority = false, "", time.Time{}, nil
	normalized.FinalizationOnly, normalized.LogProofSHA, normalized.FinalizationAuthority = false, "", nil
	normalized.TerminalFinalizerCompletion, normalized.TerminalAuthority = nil, nil
	typ, value := reflect.TypeOf(normalized), reflect.ValueOf(normalized)
	fields := []reflect.StructField{}
	indices := []int{}
	for i := range typ.NumField() {
		if typ.Field(i).PkgPath != "" || typ.Field(i).Tag.Get("json") == "-" {
			continue
		}
		if typ.Field(i).Name != "TerminalFinalizerCompletion" && typ.Field(i).Name != "TerminalAuthority" {
			fields = append(fields, typ.Field(i))
			indices = append(indices, i)
		}
	}
	prior := reflect.New(reflect.StructOf(fields)).Elem()
	for i, index := range indices {
		prior.Field(i).Set(value.Field(index))
	}
	raw, err := json.Marshal(prior.Interface())
	if err != nil {
		t.Fatal(err)
	}
	if managedLateRemainingDigest(r) != environmentPreflightBytesSHA256(raw) {
		t.Fatal("terminal extension changed immutable prior Remaining descriptor bytes")
	}
}
