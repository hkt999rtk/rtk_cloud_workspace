package main

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"slices"
	"time"
)

// readManagedSessionState selects these exact json_build_object fields. Native
// TEXT[] values are compact; only the outer object adds PostgreSQL's spacing.
// RawMessage persistence compacts that spacing, so old recorded raw hashes may
// be verified by rebuilding precisely this one lexical form, never by replacing
// a recorded hash with a semantic digest or rewriting original evidence.
func managedLateLogSessionEncodings(raw []byte) ([]byte, []byte, error) {
	bad := func() ([]byte, []byte, error) {
		return nil, nil, logACKDraftBlock("exact native session JSON representation")
	}
	if len(raw) == 0 || len(raw) > 256<<10 {
		return bad()
	}
	keys := []string{"session_id", "issuer_id", "root_sha256", "bootstrap_fingerprint", "status", "expires_at", "subjects", "installed_subjects", "other_active"}
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return bad()
	}
	var legacy bytes.Buffer
	legacy.WriteByte('{')
	for index, key := range keys {
		token, err := d.Token()
		if err != nil || token != key {
			return bad()
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return bad()
		}
		var compact bytes.Buffer
		if json.Compact(&compact, value) != nil {
			return bad()
		}
		if index < 6 {
			var text string
			if json.Unmarshal(value, &text) != nil || text == "" {
				return bad()
			}
			if key == "expires_at" {
				if _, err := time.Parse(time.RFC3339Nano, text); err != nil {
					return bad()
				}
			}
		} else if index < 8 {
			var subjects []string
			if json.Unmarshal(value, &subjects) != nil || subjects == nil {
				return bad()
			}
		} else {
			var count int
			if json.Unmarshal(value, &count) != nil || count < 0 {
				return bad()
			}
		}
		if index > 0 {
			legacy.WriteString(", ")
		}
		encoded, _ := json.Marshal(key)
		legacy.Write(encoded)
		legacy.WriteString(" : ")
		legacy.Write(compact.Bytes())
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') || d.Decode(new(any)) != io.EOF {
		return bad()
	}
	legacy.WriteByte('}')
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return bad()
	}
	return compact.Bytes(), legacy.Bytes(), nil
}

func managedLateCompactLogSessionRaw(raw []byte) ([]byte, error) {
	compact, _, err := managedLateLogSessionEncodings(raw)
	return compact, err
}

func managedLateLogSessionHashesMatch(raw []byte, hashes ...string) bool {
	if len(hashes) == 0 {
		return false
	}
	compact, legacy, err := managedLateLogSessionEncodings(raw)
	if err != nil {
		return false
	}
	// The retained PostgreSQL reader returns the selected columns in query
	// order. A later native finalizer projects those same exact columns through
	// a Go map, whose JSON encoder writes keys in lexical order. Accept that
	// third deterministic byte encoding only after the strict reader above has
	// proved the complete nine-field shape and every value type.
	var fields map[string]json.RawMessage
	if json.Unmarshal(compact, &fields) != nil || len(fields) != 9 {
		return false
	}
	sorted, err := json.Marshal(fields)
	if err != nil {
		return false
	}
	a, b, c := environmentPreflightBytesSHA256(compact), environmentPreflightBytesSHA256(legacy), environmentPreflightBytesSHA256(sorted)
	for _, hash := range hashes {
		if !environmentPreflightDigest.MatchString(hash) || hash != a && hash != b && hash != c {
			return false
		}
	}
	return true
}

// The caller has already authenticated the complete durable Log proof. Bind a
// second native hash to the same archived payload and exact two byte encodings.
func managedLateLogProofSessionHashMatches(store secretStore, proof *createManagedLateLogProof, nativeSHA string) bool {
	if proof == nil || !environmentPreflightDigest.MatchString(proof.Parent) || !environmentPreflightDigest.MatchString(proof.Review) {
		return false
	}
	path := managedLateLogFile(store, proof.Review, "managed-late-log-final-session.json")
	var final createManagedLateLogFinalSession
	if validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil || readExecutionJSON(path, &final) != nil || final.Schema != "rtk-cloud.create-managed-late-log-final-session/v1" || final.Parent != proof.Parent || final.Review != proof.Review {
		return false
	}
	if !managedLateLogSessionHashesMatch(final.Raw, proof.NativeSessionSHA) {
		return false
	}
	if managedLateLogSessionHashesMatch(final.Raw, nativeSHA) {
		return true
	}
	sealed, err := managedLateSealedLogSessionRaw(final.Raw)
	return err == nil && managedLateLogSessionHashesMatch(sealed, nativeSHA)
}

// Native finalization makes exactly one semantic change after Log captures the
// active nine-ACK snapshot: status becomes sealed. Rebuild that exact typed
// projection so the completion proof can bind either PostgreSQL field order or
// the finalizer's deterministic lexical-key encoding without accepting drift in
// identity, expiry, subjects, installed ACKs or competing-session count.
func managedLateSealedLogSessionRaw(raw []byte) ([]byte, error) {
	if _, _, err := managedLateLogSessionEncodings(raw); err != nil {
		return nil, err
	}
	var state managedSessionState
	if decodeEnvironmentJSON(raw, &state) != nil || state.Status != "active" {
		return nil, logACKDraftBlock("active native session before final seal")
	}
	state.Status = "sealed"
	projection := struct {
		Session     string    `json:"session_id"`
		Issuer      string    `json:"issuer_id"`
		Root        string    `json:"root_sha256"`
		Fingerprint string    `json:"bootstrap_fingerprint"`
		Status      string    `json:"status"`
		Expires     time.Time `json:"expires_at"`
		Subjects    []string  `json:"subjects"`
		Installed   []string  `json:"installed_subjects"`
		OtherActive int       `json:"other_active"`
	}{state.Session, state.Issuer, state.Root, state.Fingerprint, state.Status, state.Expires, state.Subjects, state.Installed, state.OtherActive}
	sealed, err := json.Marshal(projection)
	if err != nil {
		return nil, logACKDraftBlock("sealed native session projection")
	}
	return sealed, nil
}

func validateManagedLateLogFinalSession(late *createManagedLateSessionRecovery, proof *createManagedLateLogProof, observation createManagedLateLogObservation, final createManagedLateLogFinalSession) (managedSessionState, error) {
	var state managedSessionState
	if late == nil || proof == nil || final.Schema != "rtk-cloud.create-managed-late-log-final-session/v1" || final.Parent != proof.Parent || final.Review != proof.Review || decodeEnvironmentJSON(final.Raw, &state) != nil || !managedLateLogSessionHashesMatch(final.Raw, proof.NativeSessionSHA) || state.Session != late.NewSessionID || state.Issuer != observation.Session.Issuer || state.Root != observation.Session.Root || state.Fingerprint != observation.Session.Fingerprint || state.Status != "active" || state.OtherActive != 0 || !state.Expires.Equal(observation.Session.Expires) || !state.Expires.After(proof.RestoredAt) || !slices.Equal(state.Subjects, observation.Session.Subjects) || !managedLateLogExactSet(state.Installed, state.Subjects) {
		return state, logACKDraftBlock("native restored raw nine-ACK snapshot")
	}
	return state, nil
}
