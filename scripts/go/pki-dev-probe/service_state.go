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
	"slices"
	"strings"
	"time"
)

// Inspect inside the identity owner. Only public metadata and a state hash leave
// this process; private keys are never printed or copied to the operator host.
type privateServiceState struct {
	Version  int      `json:"version"`
	Subject  string   `json:"subject"`
	Domain   string   `json:"domain"`
	DNSNames []string `json:"dns_names"`
	Current  *struct {
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
	return writePrivateState(path, raw)
}

// migrateServerState upgrades only the pre-policy Dev host-state shape. It
// proves the retained certificate matches the exact requested OpenBao policy
// before adding public policy metadata; private key material never leaves the
// mounted identity volume.
func migrateServerState(path, domain, rootPin string, names []string) error {
	if domain != "openbao_tls" || len(rootPin) != 64 || strings.Trim(rootPin, "0123456789abcdef") != "" || len(names) == 0 {
		return fmt.Errorf("invalid server state migration policy")
	}
	wantNames := slices.Clone(names)
	slices.Sort(wantNames)
	if slices.Contains(wantNames, "") || !slices.Equal(wantNames, names) {
		return fmt.Errorf("server state DNS names must be sorted and nonempty")
	}
	state, _, err := loadServiceState(path)
	if err != nil {
		return err
	}
	if state.Version != 0 || state.Domain != "" || len(state.DNSNames) != 0 || state.Pending != nil || state.Subject != wantNames[0] {
		return fmt.Errorf("legacy server state migration is not applicable")
	}
	pair, err := tls.X509KeyPair([]byte(state.Current.Chain), []byte(state.Current.Key))
	if err != nil || len(pair.Certificate) != 3 {
		return fmt.Errorf("stored server certificate chain invalid")
	}
	chain := make([]*x509.Certificate, len(pair.Certificate))
	for i, raw := range pair.Certificate {
		if chain[i], err = x509.ParseCertificate(raw); err != nil {
			return fmt.Errorf("stored server certificate invalid")
		}
	}
	actualNames := slices.Clone(chain[0].DNSNames)
	slices.Sort(actualNames)
	if !slices.Equal(actualNames, wantNames) || chain[0].Subject.CommonName != state.Subject ||
		len(chain[0].ExtKeyUsage) != 1 || chain[0].ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth ||
		fingerprint(chain[2].Raw) != rootPin || chain[0].CheckSignatureFrom(chain[1]) != nil ||
		chain[1].CheckSignatureFrom(chain[2]) != nil || chain[2].CheckSignatureFrom(chain[2]) != nil ||
		chain[0].NotAfter.Before(time.Now()) {
		return fmt.Errorf("stored server certificate does not match migration policy")
	}
	state.Version, state.Domain, state.DNSNames = 1, domain, wantNames
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writePrivateState(path, raw)
}

func fingerprint(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func writePrivateState(path string, raw []byte) error {
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
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
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
	result := map[string]any{"version": state.Version, "subject": state.Subject,
		"domain": state.Domain, "dns_names": state.DNSNames,
		"fingerprint":       hash(pair.Certificate[0]),
		"public_key_sha256": hash(leaf.RawSubjectPublicKeyInfo), "root_sha256": hash(pair.Certificate[len(pair.Certificate)-1]),
		"state_sha256": hash(raw), "pending": state.Pending != nil}
	if state.Pending != nil {
		result["pending_request_id"] = state.Pending.RequestID
	}
	return json.NewEncoder(output).Encode(result)
}
