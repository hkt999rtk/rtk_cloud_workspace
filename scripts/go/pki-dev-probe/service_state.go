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
)

// Inspect inside the identity owner. Only public metadata and a state hash leave
// this process; private keys are never printed or copied to the operator host.
func serviceState(path string, output io.Writer) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 1<<20 {
		return fmt.Errorf("private Service state unavailable")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 1<<20 {
		return fmt.Errorf("private Service state unreadable")
	}
	var state struct {
		Subject string `json:"subject"`
		Current *struct {
			Key   string `json:"private_key_pem"`
			Chain string `json:"certificate_chain_pem"`
		} `json:"current"`
		Pending *struct {
			RequestID string `json:"request_id"`
		} `json:"pending"`
	}
	if json.Unmarshal(raw, &state) != nil || state.Current == nil {
		return fmt.Errorf("installed Service identity required")
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
