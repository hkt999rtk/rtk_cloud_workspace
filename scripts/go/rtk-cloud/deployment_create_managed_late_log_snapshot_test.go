package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateLogSQLSnapshotFixture(state managedSessionState) []byte {
	subjects, _ := json.Marshal(state.Subjects)
	installed, _ := json.Marshal(state.Installed)
	return []byte(fmt.Sprintf(`{"session_id" : %q, "issuer_id" : %q, "root_sha256" : %q, "bootstrap_fingerprint" : %q, "status" : %q, "expires_at" : %q, "subjects" : %s, "installed_subjects" : %s, "other_active" : %d}`, state.Session, state.Issuer, state.Root, state.Fingerprint, state.Status, state.Expires.Format(time.RFC3339Nano), subjects, installed, state.OtherActive))
}

func managedLateLogStoredSnapshotFixture(t *testing.T) (*createManagedLateSessionRecovery, createManagedLateLogObservation, createManagedLateLogFinalSession, *createManagedLateLogProof) {
	t.Helper()
	a, job, pod, state, now := managedLateLogTerminalFixture(t)
	a.OriginalClaimUID = "77777777-7777-4777-8777-777777777777"
	subjects, err := environmentCreateBootstrapSubjects(deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}})
	if err != nil {
		t.Fatal(err)
	}
	state.Subjects, state.Installed = subjects, append([]string(nil), subjects...)
	a.NewSession = state
	raw := managedLateLogSQLSnapshotFixture(state)
	native, err := verifyLogACKDraftCompleted(a, deploymentplan.String(deploymentplan.Map(job["metadata"])["uid"]), deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"]), job, []deploymentplan.Object{pod}, state, a.Identity, a.OriginalClientsSHA, a.Identity.StateSHA256, now)
	if err != nil {
		t.Fatal(err)
	}
	native["native_session_raw_sha256"] = environmentPreflightBytesSHA256(raw)
	late := &createManagedLateSessionRecovery{ParentSHA: a.Parent, Review: a.FreshReview, NewSessionID: state.Session, OriginalClientsSHA: a.OriginalClientsSHA, LogProducer: a}
	observation := createManagedLateLogObservation{Schema: "rtk-cloud.create-managed-late-log-observation/v1", Parent: a.Parent, Review: a.FreshReview, ProducerSHA: deploymentplan.Digest(a), ClaimUID: a.OriginalClaimUID, Job: job, Pod: pod, Identity: a.Identity, Session: state, SessionRaw: raw, ClientsSHA: a.OriginalClientsSHA, PublicLogSHA: strings.Repeat("8", 64), Native: native}
	final := createManagedLateLogFinalSession{Schema: "rtk-cloud.create-managed-late-log-final-session/v1", Parent: a.Parent, Review: a.FreshReview, Raw: raw}
	proof := &createManagedLateLogProof{Parent: a.Parent, Review: a.FreshReview, NativeSessionSHA: environmentPreflightBytesSHA256(raw), RestoredAt: now}
	// Exercise exactly the RawMessage serialization used by immutable writer.
	var storedObservation createManagedLateLogObservation
	var storedFinal createManagedLateLogFinalSession
	if decodeEnvironmentJSON(mustProviderRecoveryJSON(observation), &storedObservation) != nil || decodeEnvironmentJSON(mustProviderRecoveryJSON(final), &storedFinal) != nil {
		t.Fatal("stored fixture")
	}
	if environmentPreflightBytesSHA256(storedFinal.Raw) == proof.NativeSessionSHA {
		t.Fatal("fixture did not reproduce native JSON compaction hash drift")
	}
	if deploymentplan.Digest(observation) != deploymentplan.Digest(storedObservation) {
		t.Fatal("RawMessage round trip changed the original canonical observation digest")
	}
	return late, storedObservation, storedFinal, proof
}

func TestManagedLateLogSnapshotLegacyRoundTripPreservesBothOriginalRecordedHashes(t *testing.T) {
	late, observation, final, proof := managedLateLogStoredSnapshotFixture(t)
	before := deploymentplan.Digest([]any{late, observation, final, proof})
	uid := deploymentplan.String(deploymentplan.Map(observation.Job["metadata"])["uid"])
	if err := validateManagedLateLogObservation(late, observation, uid); err != nil {
		t.Fatal("genuine legacy terminal observation hash refused", err)
	}
	if _, err := validateManagedLateLogFinalSession(late, proof, observation, final); err != nil {
		t.Fatal("genuine legacy raw nine-ACK snapshot refused", err)
	}
	if before != deploymentplan.Digest([]any{late, observation, final, proof}) {
		t.Fatal("legacy hash verification rewrote original proof or payload")
	}
	compact, err := managedLateCompactLogSessionRaw(managedLateLogSQLSnapshotFixture(observation.Session))
	if err != nil || !bytes.Equal(compact, final.Raw) {
		t.Fatal("future writer did not hash exact persisted compact bytes", err)
	}
	future := createManagedLateLogFinalSession{Schema: final.Schema, Parent: final.Parent, Review: final.Review, Raw: compact}
	var roundTrip createManagedLateLogFinalSession
	if decodeEnvironmentJSON(mustProviderRecoveryJSON(future), &roundTrip) != nil || environmentPreflightBytesSHA256(roundTrip.Raw) != environmentPreflightBytesSHA256(compact) {
		t.Fatal("new writer still loses hashed bytes during persistence")
	}
	if !managedLateLogSessionHashesMatch(final.Raw, proof.NativeSessionSHA, environmentPreflightBytesSHA256(compact)) {
		t.Fatal("same archived payload did not authenticate both exact native/compact forms")
	}
}

func TestManagedLateLogSnapshotCodecRejectsUnknownLexicalOrStructuralAuthority(t *testing.T) {
	_, observation, final, proof := managedLateLogStoredSnapshotFixture(t)
	legacy := managedLateLogSQLSnapshotFixture(observation.Session)
	for _, mode := range []string{"empty-hash", "foreign-hash", "alternate-whitespace-hash", "trailing-newline-hash", "changed-token", "duplicate-key", "reordered-key", "extra-key", "missing-key", "nested-scalar", "nested-array", "null-array", "numeric-string", "fraction-count", "negative-count", "trailing-object", "array-envelope", "invalid-json"} {
		t.Run(mode, func(t *testing.T) {
			raw := append([]byte(nil), final.Raw...)
			hash := proof.NativeSessionSHA
			switch mode {
			case "empty-hash":
				hash = ""
			case "foreign-hash":
				hash = strings.Repeat("f", 64)
			case "alternate-whitespace-hash":
				hash = environmentPreflightBytesSHA256(bytes.ReplaceAll(legacy, []byte(" : "), []byte(":")))
			case "trailing-newline-hash":
				hash = environmentPreflightBytesSHA256(append(append([]byte(nil), legacy...), '\n'))
			case "changed-token":
				raw = bytes.Replace(raw, []byte(`"active"`), []byte(`"sealed"`), 1)
			case "duplicate-key":
				raw = bytes.Replace(raw, []byte(`"issuer_id":`), []byte(`"session_id":"duplicate","issuer_id":`), 1)
			case "reordered-key":
				raw = bytes.Replace(raw, []byte(`"issuer_id":`), []byte(`"unexpected_order":`), 1)
			case "extra-key":
				raw = append(raw[:len(raw)-1], []byte(`,"extra":true}`)...)
			case "missing-key":
				raw = bytes.Replace(raw, []byte(`,"other_active":0`), nil, 1)
			case "nested-scalar":
				raw = bytes.Replace(raw, []byte(`"status":"active"`), []byte(`"status":{"active":true}`), 1)
			case "nested-array":
				raw = bytes.Replace(raw, []byte(`"subjects":[`), []byte(`"subjects":[{"hidden":true},`), 1)
			case "null-array":
				raw = bytes.Replace(raw, []byte(`"subjects":[`), []byte(`"subjects":null,"subjects":[`), 1)
			case "numeric-string":
				raw = bytes.Replace(raw, []byte(`"other_active":0`), []byte(`"other_active":"0"`), 1)
			case "fraction-count":
				raw = bytes.Replace(raw, []byte(`"other_active":0`), []byte(`"other_active":0.5`), 1)
			case "negative-count":
				raw = bytes.Replace(raw, []byte(`"other_active":0`), []byte(`"other_active":-1`), 1)
			case "trailing-object":
				raw = append(raw, []byte(`{}`)...)
			case "array-envelope":
				raw = append(append([]byte("["), raw...), ']')
			case "invalid-json":
				raw = []byte("private-invalid-canary")
			}
			if managedLateLogSessionHashesMatch(raw, hash) {
				t.Fatal("foreign schema/token/hash or unobserved lexical layout accepted")
			}
		})
	}
	// Strings are opaque tokens, including delimiters and escaped quotes.
	var state managedSessionState
	if decodeEnvironmentJSON(final.Raw, &state) != nil {
		t.Fatal("state fixture")
	}
	state.Subjects = []string{`literal : {, "quoted" }`}
	state.Installed = append([]string(nil), state.Subjects...)
	quoted := managedLateLogSQLSnapshotFixture(state)
	var compact bytes.Buffer
	if json.Compact(&compact, quoted) != nil || !managedLateLogSessionHashesMatch(compact.Bytes(), environmentPreflightBytesSHA256(quoted)) {
		t.Fatal("finite lexer changed string delimiters or escapes")
	}
}

func TestManagedLateLogSnapshotNineACKAndIdentityPredicatesRemainMandatory(t *testing.T) {
	for _, mode := range []string{"schema", "parent", "review", "session", "issuer", "root", "fingerprint", "sealed", "other-active", "changed-expiry", "expired-at-restore", "different-subjects", "missing-ACK", "duplicate-ACK"} {
		t.Run(mode, func(t *testing.T) {
			late, observation, final, proof := managedLateLogStoredSnapshotFixture(t)
			var state managedSessionState
			if decodeEnvironmentJSON(final.Raw, &state) != nil {
				t.Fatal("state")
			}
			switch mode {
			case "schema":
				final.Schema = "foreign"
			case "parent":
				final.Parent = strings.Repeat("f", 64)
			case "review":
				final.Review = strings.Repeat("f", 64)
			case "session":
				state.Session = "99999999-9999-4999-8999-999999999999"
			case "issuer":
				state.Issuer = "99999999-9999-4999-8999-999999999999"
			case "root":
				state.Root = strings.Repeat("f", 64)
			case "fingerprint":
				state.Fingerprint = strings.Repeat("f", 64)
			case "sealed":
				state.Status = "sealed"
			case "other-active":
				state.OtherActive = 1
			case "changed-expiry":
				state.Expires = state.Expires.Add(time.Minute)
			case "expired-at-restore":
				proof.RestoredAt = state.Expires
			case "different-subjects":
				state.Subjects[0] = "service:foreign"
			case "missing-ACK":
				state.Installed = state.Installed[:8]
			case "duplicate-ACK":
				state.Installed[8] = state.Installed[0]
			}
			final.Raw = managedLateLogSQLSnapshotFixture(state)
			proof.NativeSessionSHA = environmentPreflightBytesSHA256(final.Raw)
			if _, err := validateManagedLateLogFinalSession(late, proof, observation, final); err == nil {
				t.Fatal("valid lexical hash waived native nine-ACK/identity/expiry evidence")
			}
		})
	}
}

func TestManagedLateLogSnapshotDurableCrossHashUsesSameImmutablePayload(t *testing.T) {
	_, _, final, proof := managedLateLogStoredSnapshotFixture(t)
	store := makeIsolatedTestSecretStore(t, "dev")
	path := managedLateLogFile(store, proof.Review, "managed-late-log-final-session.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw := mustProviderRecoveryJSON(final)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(final.Raw, &fields) != nil {
		t.Fatal("valid fixed-order session snapshot cannot be decoded")
	}
	sorted, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if !managedLateLogProofSessionHashMatches(store, proof, environmentPreflightBytesSHA256(final.Raw)) || !managedLateLogProofSessionHashMatches(store, proof, proof.NativeSessionSHA) || !managedLateLogProofSessionHashMatches(store, proof, environmentPreflightBytesSHA256(sorted)) {
		t.Fatal("same legacy/compact payload durable binding refused")
	}
	sealed, err := managedLateSealedLogSessionRaw(final.Raw)
	if err != nil || !managedLateLogProofSessionHashMatches(store, proof, environmentPreflightBytesSHA256(sealed)) {
		t.Fatal("exact active-to-sealed native finalization binding refused", err)
	}
	var sealedFields map[string]json.RawMessage
	if json.Unmarshal(sealed, &sealedFields) != nil {
		t.Fatal("sealed session projection cannot be decoded")
	}
	sealedSorted, err := json.Marshal(sealedFields)
	if err != nil || !managedLateLogProofSessionHashMatches(store, proof, environmentPreflightBytesSHA256(sealedSorted)) {
		t.Fatal("lexical-key sealed finalizer projection refused", err)
	}
	var changed managedSessionState
	if decodeEnvironmentJSON(sealed, &changed) != nil {
		t.Fatal("sealed session cannot be decoded")
	}
	changed.Fingerprint = strings.Repeat("e", 64)
	if managedLateLogProofSessionHashMatches(store, proof, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(changed))) {
		t.Fatal("sealed session with changed identity admitted")
	}
	if managedLateLogProofSessionHashMatches(store, proof, strings.Repeat("f", 64)) || managedLateLogProofSessionHashMatches(store, proof, "") {
		t.Fatal("foreign/empty native digest admitted")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("durable codec rewrote original artifact")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if managedLateLogProofSessionHashMatches(store, proof, proof.NativeSessionSHA) {
		t.Fatal("public-mode snapshot admitted")
	}
}
