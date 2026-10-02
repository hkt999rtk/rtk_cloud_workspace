package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// This evidence covers every use of the key across its entire bucket, including
// external consumers. A migration of one prefix alone cannot establish that.
type storageRetirementEvidence struct {
	Environment       string                      `json:"environment"`
	CutoverID         string                      `json:"cutover_id"`
	SourceBucket      string                      `json:"source_bucket"`
	SourceRegion      string                      `json:"source_region"`
	DestinationBucket string                      `json:"destination_bucket"`
	DestinationRegion string                      `json:"destination_region"`
	KeyID             int                         `json:"key_id"`
	Scope             string                      `json:"scope"`
	InventoryComplete *bool                       `json:"inventory_complete"`
	GenericKeyInUse   *bool                       `json:"generic_key_in_use"`
	ObservationStart  time.Time                   `json:"observation_started_at"`
	ObservationEnd    time.Time                   `json:"observation_ended_at"`
	URLExpiry         time.Time                   `json:"last_issued_url_expires_at"`
	ObservedAt        time.Time                   `json:"observed_at"`
	Consumers         []storageRetirementConsumer `json:"consumers"`
}

type storageRetirementConsumer struct {
	Name       string    `json:"name"`
	KeyInUse   *bool     `json:"key_in_use"`
	ObservedAt time.Time `json:"observed_at"`
}

type storageRetirementCutover struct {
	Environment     string    `json:"environment"`
	Bucket          string    `json:"bucket"`
	Region          string    `json:"region"`
	Prefix          string    `json:"prefix"`
	CutoverAt       time.Time `json:"cutover_at"`
	CutoverID       string    `json:"cutover_id"`
	MigrationSHA256 string    `json:"migration_receipt_sha256"`
}

func readStorageRetirementEvidence(path string) (storageRetirementEvidence, error) {
	var evidence storageRetirementEvidence
	body, err := os.ReadFile(path)
	if err != nil {
		return evidence, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil {
		return evidence, errors.New("consumer retirement evidence is not a valid typed inventory")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return evidence, errors.New("consumer retirement evidence contains trailing data")
	}
	return evidence, nil
}

func validateStorageRetirementEvidence(e storageRetirementEvidence, cfg deploymentConfig, journal storageCutoverJournal, migration deploymentStorageMigrationState, cutover storageRetirementCutover, keyID int, now time.Time) error {
	if e.Environment != cfg.Environment || e.CutoverID != journal.ID || e.KeyID != keyID ||
		e.SourceBucket != migration.Source || e.SourceRegion != migration.SourceRegion ||
		e.DestinationBucket != migration.Destination || e.DestinationRegion != migration.DestinationRegion {
		return errors.New("consumer retirement evidence does not match the environment, cutover, source, destination and key")
	}
	if e.Scope != "entire-bucket" || e.InventoryComplete == nil || !*e.InventoryComplete || e.GenericKeyInUse == nil || *e.GenericKeyInUse {
		return errors.New("complete entire-bucket consumer inventory must explicitly confirm the old key is unused")
	}
	if e.ObservationStart.Before(cutover.CutoverAt) || !e.ObservationEnd.After(e.ObservationStart) ||
		e.URLExpiry.Before(cutover.CutoverAt) || e.ObservedAt.Before(e.ObservationEnd) || e.ObservedAt.Before(e.URLExpiry) ||
		e.ObservedAt.After(now) || now.Sub(e.ObservedAt) > 24*time.Hour {
		return errors.New("retirement requires elapsed post-cutover observation and URL expiry, verified within the last 24 hours")
	}
	seen := map[string]bool{}
	for _, consumer := range e.Consumers {
		if strings.TrimSpace(consumer.Name) == "" || seen[consumer.Name] || consumer.KeyInUse == nil || *consumer.KeyInUse ||
			consumer.ObservedAt.Before(e.ObservationEnd) || consumer.ObservedAt.Before(e.URLExpiry) || consumer.ObservedAt.After(e.ObservedAt) || now.Sub(consumer.ObservedAt) > 24*time.Hour {
			return errors.New("each consumer must have unique, current evidence that the retired key is unused after observation and URL expiry")
		}
		seen[consumer.Name] = true
	}
	if len(seen) == 0 {
		return errors.New("consumer retirement evidence must identify the verified consumers")
	}
	for _, mutation := range journal.Mutations {
		if storageCutoverPodPath(mutation.Kind) != "" && !seen[mutation.Namespace+"/"+mutation.Kind+"/"+mutation.Name] {
			return errors.New("consumer retirement evidence omits a workload changed by the cutover")
		}
	}
	return nil
}

func (c deploymentCredentialChecker) retireStorageKey(cfg deploymentConfig, values map[string]string, keyID int) error {
	if keyID <= 0 || strings.TrimSpace(values["LINODE_TOKEN"]) == "" {
		return errors.New("a positive key ID and LINODE_TOKEN are required for retirement")
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	journalBody, err := store.read(storageCutoverJournalName("media"))
	if err != nil {
		return fmt.Errorf("completed private media cutover journal is required: %w", err)
	}
	var journal storageCutoverJournal
	if json.Unmarshal([]byte(journalBody), &journal) != nil || journal.Environment != cfg.Environment || journal.Purpose != "media" || journal.Status != "complete" || journal.ID == "" {
		return errors.New("private media cutover journal is incomplete or belongs to another environment")
	}
	migrationBody, err := os.ReadFile(storageCutoverMigrationPath(cfg, "media"))
	if err != nil {
		return err
	}
	var migration deploymentStorageMigrationState
	target := cfg.Storage.RuntimeMedia
	if json.Unmarshal(migrationBody, &migration) != nil || fmt.Sprintf("%x", sha256.Sum256(migrationBody)) != journal.MigrationSHA256 ||
		migration.Environment != cfg.Environment || migration.Purpose != "media" || migration.Source == "" || migration.SourceRegion == "" ||
		migration.Destination != target.Bucket || migration.DestinationRegion != target.Region || migration.Prefix != target.Prefix {
		return errors.New("retirement requires the unchanged migration receipt for the completed media cutover")
	}
	var cutover storageRetirementCutover
	body, err := os.ReadFile(filepath.Join(cfg.RuntimeRoot, "state", "storage-cutover.json"))
	if err != nil || json.Unmarshal(body, &cutover) != nil || cutover.Environment != cfg.Environment || cutover.Bucket != target.Bucket || cutover.Region != target.Region || cutover.Prefix != target.Prefix ||
		cutover.CutoverAt.IsZero() || cutover.CutoverID != journal.ID || cutover.MigrationSHA256 != journal.MigrationSHA256 {
		return errors.New("retirement requires a completed receipt bound to the private cutover journal and migration")
	}
	evidence, err := readStorageRetirementEvidence(filepath.Join(cfg.RuntimeRoot, "state", "storage-consumers.json"))
	if err != nil {
		return err
	}
	if err := validateStorageRetirementEvidence(evidence, cfg, journal, migration, cutover, keyID, time.Now().UTC()); err != nil {
		return err
	}
	sourceValues, check := deploymentCredentialValues(journal.SourceFile)
	if !check.Passed {
		return errors.New("preserved source profile is required to identify the retiring key")
	}
	source, err := provisionObjectStoreFromEnv(sourceValues)
	if err != nil || source.accessKey == "" || fmt.Sprintf("%x", sha256.Sum256([]byte(source.accessKey))) != journal.SourceAccessSHA256 || source.bucket != migration.Source || source.region != migration.SourceRegion || source.endpoint != migration.SourceEndpoint {
		return errors.New("preserved source profile does not match the completed migration")
	}
	if source.bucket == target.Bucket && source.region == target.Region {
		return errors.New("refuse to retire a key for the active destination bucket")
	}
	for _, retained := range []deploymentStorageTarget{cfg.Storage.ReleaseArtifacts, cfg.Storage.OTAFirmware} {
		if source.bucket == retained.Bucket && source.region == retained.Region {
			return errors.New("refuse to retire an artifact or OTA bucket key through media retirement")
		}
	}
	active, err := store.readOperator()
	if err != nil {
		return err
	}
	for _, profile := range []map[string]string{values, active, cfg.Values} {
		if err := validateDestroyRuntimeBucket(profile, source.bucket, source.region); err != nil {
			return fmt.Errorf("retirement source overlaps retained storage: %w", err)
		}
		for name, access := range profile {
			if strings.HasSuffix(name, "ACCESS_KEY_ID") && access == source.accessKey {
				return errors.New("refuse to retire a key still present in active or selected credentials")
			}
		}
	}
	// Fetch the requested key itself immediately before deletion. A matching
	// source grant on a multi-bucket or unrestricted key is insufficient.
	path := fmt.Sprintf("/object-storage/keys/%d", keyID)
	body, err = c.linodeAuthorizedRequest(values["LINODE_TOKEN"], http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	var key linodeStorageKey
	if json.Unmarshal(body, &key) != nil || key.ID != keyID || key.AccessKey != source.accessKey || len(key.BucketAccess) != 1 {
		return errors.New("retiring key must be the exact preserved source key with one limited bucket grant")
	}
	grant := key.BucketAccess[0]
	if grant.BucketName != source.bucket || grant.Region != source.region || (grant.Permissions != "read_write" && grant.Permissions != "read_only") {
		return errors.New("retiring key grant does not match the preserved source bucket and region")
	}
	_, err = c.linodeAuthorizedRequest(values["LINODE_TOKEN"], http.MethodDelete, path, nil)
	return err
}
