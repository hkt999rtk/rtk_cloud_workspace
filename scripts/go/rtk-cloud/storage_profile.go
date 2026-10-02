package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type storageCredentialPromotion struct {
	Environment string            `json:"environment"`
	Purpose     string            `json:"purpose"`
	Before      map[string]string `json:"before"`
	After       map[string]string `json:"after"`
}

func storagePromotionPath(purpose string) (string, error) {
	if purpose != "media" && purpose != "ota" && purpose != "artifacts" {
		return "", errors.New("unsupported storage credential purpose")
	}
	return filepath.Join("operator", "storage", purpose+"-promotion.json"), nil
}

// Persist the prior pair before changing active credentials. Never promote an
// unmarked file or copy unrelated operator credentials out of a candidate.
func activateStorageCandidate(cfg deploymentConfig, candidate, purpose string) error {
	values, check := deploymentCredentialValuesFromFile(candidate)
	if !check.Passed {
		return errors.New(check.Detail)
	}
	if values["RTK_STORAGE_CANDIDATE_ENVIRONMENT"] != cfg.Environment {
		return errors.New("storage activation requires this environment's candidate profile")
	}
	path, err := storagePromotionPath(purpose)
	if err != nil {
		return err
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	active, err := store.readOperator()
	if err != nil {
		return err
	}
	prefix := map[string]string{"media": "LINODE_MEDIA_OBJ_", "ota": "LINODE_OTA_OBJ_", "artifacts": "LINODE_ARTIFACT_OBJ_"}[purpose]
	state := storageCredentialPromotion{Environment: cfg.Environment, Purpose: purpose, Before: map[string]string{}, After: map[string]string{}}
	for _, suffix := range []string{"ACCESS_KEY_ID", "SECRET_ACCESS_KEY"} {
		key := prefix + suffix
		if values[key] == "" {
			return fmt.Errorf("candidate lacks %s", key)
		}
		state.After[key] = values[key]
		if old, ok := active[key]; ok {
			state.Before[key] = old
		}
	}
	if existing, err := store.read(path); err == nil {
		var previous storageCredentialPromotion
		if json.Unmarshal([]byte(existing), &previous) != nil || previous.Environment != cfg.Environment || previous.Purpose != purpose {
			return errors.New("invalid existing storage credential promotion journal")
		}
		if equalCredentialPairs(active, previous.After) && equalCredentialPairs(state.After, previous.After) {
			return nil
		}
		return errors.New("previous storage credential promotion must be retired or rolled back before another activation")
	} else if !os.IsNotExist(err) {
		return err
	}
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := store.write(path, body, false); err != nil {
		return err
	}
	for _, suffix := range []string{"ACCESS_KEY_ID", "SECRET_ACCESS_KEY"} {
		key := prefix + suffix
		if err := store.write(filepath.Join("operator", "env", key), []byte(state.After[key]), true); err != nil {
			restoreErr := restoreStorageCredentialPair(store, state, false)
			if restoreErr == nil {
				journal, pathErr := store.safePath(path)
				if pathErr == nil {
					restoreErr = os.Remove(journal)
				} else {
					restoreErr = pathErr
				}
			}
			return errors.Join(err, restoreErr)
		}
	}
	return nil
}

func equalCredentialPairs(actual, expected map[string]string) bool {
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func restoreStorageCredentialPair(store secretStore, state storageCredentialPromotion, checkCurrent bool) error {
	if checkCurrent {
		active, err := store.readOperator()
		if err != nil {
			return err
		}
		if !equalCredentialPairs(active, state.After) {
			return errors.New("active storage credentials changed after cutover; refusing rollback overwrite")
		}
	}
	var failures []error
	for key := range state.After {
		relative := filepath.Join("operator", "env", key)
		if old, ok := state.Before[key]; ok {
			failures = append(failures, store.write(relative, []byte(old), true))
		} else {
			path, err := store.safePath(relative)
			if err == nil {
				err = os.Remove(path)
				if os.IsNotExist(err) {
					err = nil
				}
			}
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func rollbackStorageCandidate(cfg deploymentConfig, candidate, purpose string) error {
	path, err := storagePromotionPath(purpose)
	if err != nil {
		return err
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	body, err := store.read(path)
	if os.IsNotExist(err) {
		return nil
	} // Cutover did not reach credential activation.
	if err != nil {
		return err
	}
	var state storageCredentialPromotion
	if json.Unmarshal([]byte(body), &state) != nil || state.Environment != cfg.Environment || state.Purpose != purpose || len(state.After) != 2 {
		return errors.New("invalid storage credential promotion journal")
	}
	prefix := map[string]string{"media": "LINODE_MEDIA_OBJ_", "ota": "LINODE_OTA_OBJ_", "artifacts": "LINODE_ARTIFACT_OBJ_"}[purpose]
	for key, value := range state.After {
		if value == "" || (key != prefix+"ACCESS_KEY_ID" && key != prefix+"SECRET_ACCESS_KEY") {
			return errors.New("invalid storage credential journal key")
		}
	}
	for key := range state.Before {
		if _, ok := state.After[key]; !ok {
			return errors.New("invalid prior storage credential journal key")
		}
	}
	if err := restoreStorageCredentialPair(store, state, true); err != nil {
		return err
	}
	resolved, err := store.safePath(path)
	if err != nil {
		return err
	}
	return os.Remove(resolved)
}

// stageDeploymentStorageProfile isolates replacement keys from the active
// environment-local SecretStore until consumer cutover has succeeded.
func stageDeploymentStorageProfile(environment, source, destination string, create bool) error {
	if !filepath.IsAbs(destination) {
		return errors.New("candidate storage profile must be an absolute path")
	}
	if filepath.Clean(source) == filepath.Clean(destination) {
		return errors.New("candidate storage profile must differ from the active profile")
	}
	if st, err := os.Lstat(destination); err == nil {
		if !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
			return errors.New("candidate storage profile must be a regular 0600 file")
		}
		values, check := deploymentCredentialValuesFromFile(destination)
		if !check.Passed {
			return errors.New(check.Detail)
		}
		if values["RTK_STORAGE_CANDIDATE_ENVIRONMENT"] != environment {
			return errors.New("candidate storage profile belongs to another environment or lacks its ownership marker")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if !create {
		return errors.New("candidate storage profile does not exist; prepare it with storage-bootstrap")
	}
	values, check := deploymentCredentialProfileValues(environment, source, "")
	if !check.Passed {
		return errors.New(check.Detail)
	}
	values["RTK_STORAGE_CANDIDATE_ENVIRONMENT"] = environment
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	// Exclusive creation prevents clobbering a concurrently prepared profile.
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create candidate storage profile: %w", err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := writeSortedEnv(destination, values, 0o600); err != nil {
		return err
	}
	return nil
}
