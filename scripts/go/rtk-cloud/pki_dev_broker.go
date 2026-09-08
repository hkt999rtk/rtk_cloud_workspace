package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var pkiDevBrokerFiles = []string{"api-key", "api-secret", "database-password", "node-cookie", "dashboard-password"}

// A separate fixture owns its credentials; never reuse or rotate the existing
// broker/controller passwords to prepare the isolated dev MQTT rehearsal.
func preparePKIDevBroker(store secretStore) (string, error) {
	if store.Environment != "dev" {
		return "", errors.New("broker rehearsal credentials are dev-only")
	}
	parent := filepath.Join(store.Root, "pki")
	for _, dir := range []string{store.ConfigRoot, store.Root, parent} {
		if err := ensurePrivateDirectory(dir); err != nil {
			return "", err
		}
	}
	dir := filepath.Join(parent, "mqtt-pki-runtime")
	if err := os.Mkdir(dir+".lock", 0700); err != nil {
		return "", fmt.Errorf("lock broker credential preparation: %w", err)
	}
	defer os.Remove(dir + ".lock")
	if _, err := os.Lstat(dir); err == nil {
		return dir, validatePKIDevBroker(dir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	staged, err := os.MkdirTemp(parent, ".mqtt-pki-runtime-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staged)
	for _, name := range pkiDevBrokerFiles {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(staged, name), []byte(base64.RawURLEncoding.EncodeToString(raw)), 0600); err != nil {
			return "", err
		}
	}
	if err := validatePKIDevBroker(staged); err != nil {
		return "", err
	}
	if err := os.Rename(staged, dir); err != nil {
		return "", err
	}
	return dir, nil
}
func validatePKIDevBroker(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("broker credential directory must be mode 0700")
	}
	seen := map[string]bool{}
	for _, name := range pkiDevBrokerFiles {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != 43 {
			return fmt.Errorf("invalid protected broker credential %s", name)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read broker credential %s failed", name)
		}
		decoded, err := base64.RawURLEncoding.DecodeString(string(raw))
		if err != nil || len(decoded) != 32 || seen[string(raw)] {
			return fmt.Errorf("invalid or duplicated broker credential %s", name)
		}
		seen[string(raw)] = true
	}
	return nil
}
