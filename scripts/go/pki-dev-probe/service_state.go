package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Inspect inside the identity owner. Only public metadata and a state hash leave
// this process; private keys are never printed or copied to the operator host.
type privateServiceState struct {
	Subject string `json:"subject"`
	Current *struct {
		Key   string `json:"private_key_pem"`
		Chain string `json:"certificate_chain_pem"`
	} `json:"current"`
	Pending *struct {
		RequestID string `json:"request_id"`
	} `json:"pending"`
}

// clearPending removes only the exact retained Dev claim. It never prints or
// copies private key material and preserves the installed current identity.
func clearPending(path, requestID string) error {
	state, _, err := loadServiceState(path)
	if err != nil {
		return err
	}
	if state.Pending == nil || state.Pending.RequestID != requestID {
		return fmt.Errorf("exact pending request is not retained")
	}
	state.Pending = nil
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".service-state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func loadServiceState(path string) (state privateServiceState, raw []byte, err error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 1<<20 {
		return state, nil, fmt.Errorf("private Service state unavailable")
	}
	raw, err = os.ReadFile(path)
	if err != nil || len(raw) > 1<<20 {
		return state, nil, fmt.Errorf("private Service state unreadable")
	}
	if json.Unmarshal(raw, &state) != nil || state.Current == nil {
		return state, nil, fmt.Errorf("installed Service identity required")
	}
	return state, raw, nil
}

func serviceState(path string, output io.Writer) error {
	state, raw, err := loadServiceState(path)
	if err != nil {
		return err
	}
	pair, err := tls.X509KeyPair([]byte(state.Current.Chain), []byte(state.Current.Key))
	if err != nil || len(pair.Certificate) == 0 {
		return fmt.Errorf("stored Service key/certificate mismatch")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("stored Service certificate invalid")
	}
	hash := func(raw []byte) string { value := sha256.Sum256(raw); return hex.EncodeToString(value[:]) }
	result := map[string]any{"subject": state.Subject, "fingerprint": hash(pair.Certificate[0]),
		"public_key_sha256": hash(leaf.RawSubjectPublicKeyInfo), "root_sha256": hash(pair.Certificate[len(pair.Certificate)-1]),
		"state_sha256": hash(raw), "pending": state.Pending != nil}
	if state.Pending != nil {
		result["pending_request_id"] = state.Pending.RequestID
	}
	return json.NewEncoder(output).Encode(result)
}
