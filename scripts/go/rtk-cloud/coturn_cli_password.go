package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Each relay has its own CLI credential. This is deployment output, not a
// configuration option or a generic catalog addition that may be regenerated.
// The caller must first select the environment-owned VM and read its original
// runtime credential. Only a newly allocated VM permits first-time generation.
func retainCoturnCLIPassword(store secretStore, relay, original string, bootstrap bool) (string, error) {
	if !secretEnvironmentPattern.MatchString(relay) {
		return "", errors.New("invalid Coturn relay credential owner")
	}
	relative := filepath.Join("deployment", "turn", relay, "cli-password")
	directories := []string{store.ConfigRoot, store.Root, filepath.Join(store.Root, "deployment"), filepath.Join(store.Root, "deployment", "turn"), filepath.Join(store.Root, "deployment", "turn", relay)}
	for _, directory := range directories {
		info, err := os.Lstat(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
			return "", errors.New("Coturn CLI credential directories must be real private directories with mode 0700")
		}
	}
	saved, err := store.read(relative)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("private Coturn CLI credential cannot be read; restore its original file with mode 0600")
	}
	original = strings.TrimSuffix(original, "\n")
	if original != "" && !validCoturnCLIPassword(original) {
		return "", errors.New("original Coturn CLI credential is invalid; no replacement was generated")
	}
	if err == nil {
		if !validCoturnCLIPassword(saved) || (original != "" && saved != original) {
			return "", errors.New("saved and runtime Coturn CLI credentials disagree; restore the original credential, do not rotate during upgrade")
		}
		return saved, nil // Includes retry after allocation but before installation.
	}
	if original == "" {
		if !bootstrap {
			return "", errors.New("existing Coturn CLI credential is unavailable; restore original runtime or private recovery data")
		}
		var value [24]byte
		if _, err := rand.Read(value[:]); err != nil {
			return "", errors.New("cannot generate bootstrap Coturn CLI credential")
		}
		original = hex.EncodeToString(value[:])
	}
	for _, directory := range directories {
		if err := ensurePrivateDirectory(directory); err != nil {
			return "", err
		}
	}
	// Publish without replacing a concurrently created credential.
	file, err := os.CreateTemp(directories[len(directories)-1], ".cli-password-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.WriteString(original + "\n")
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return "", errors.New("cannot persist Coturn CLI credential")
	}
	if err := os.Link(file.Name(), filepath.Join(store.Root, relative)); err != nil {
		return "", err
	}
	return original, nil
}

func validCoturnCLIPassword(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, c := range []byte(value) {
		if c <= 32 || c >= 127 {
			return false
		}
	}
	return true
}
