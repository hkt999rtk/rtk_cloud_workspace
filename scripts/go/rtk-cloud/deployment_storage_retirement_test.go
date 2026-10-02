package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type storageRetirementFixture struct {
	cfg      deploymentConfig
	store    secretStore
	values   map[string]string
	key      linodeStorageKey
	evidence storageRetirementEvidence
	journal  storageCutoverJournal
	cutover  storageRetirementCutover
}

func makeStorageRetirementFixture(t *testing.T) storageRetirementFixture {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, "dev")
	f := storageRetirementFixture{
		cfg: deploymentConfig{Environment: "dev", RuntimeRoot: t.TempDir(), Storage: deploymentStoragePlan{
			RuntimeMedia:     deploymentStorageTarget{Bucket: "rtk-cloud-dev-runtime-us-lax", Region: "us-lax", Prefix: "environments/stack"},
			ReleaseArtifacts: deploymentStorageTarget{Bucket: "rtk-cloud-shared-artifacts-us-sea", Region: "us-sea"},
			OTAFirmware:      deploymentStorageTarget{Bucket: "rtk-cloud-dev-ota-firmware-us-lax", Region: "us-lax"},
		}}, store: store, values: map[string]string{"LINODE_TOKEN": "token", "LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "new-media"},
	}
	if err := store.write("operator/env/LINODE_MEDIA_OBJ_ACCESS_KEY_ID", []byte("new-media"), true); err != nil {
		t.Fatal(err)
	}
	sourceFile := filepath.Join(t.TempDir(), "source.env")
	if err := os.WriteFile(sourceFile, []byte("LINODE_OBJ_BUCKET=old-media\nLINODE_OBJ_REGION=us-sea\nLINODE_OBJ_ENDPOINT=https://us-sea-1.linodeobjects.com\nLINODE_OBJ_ACCESS_KEY_ID=old-media-key\nLINODE_OBJ_SECRET_ACCESS_KEY=old-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	migration := deploymentStorageMigrationState{Environment: "dev", Purpose: "media", Source: "old-media", SourceRegion: "us-sea", SourceEndpoint: "https://us-sea-1.linodeobjects.com", Destination: f.cfg.Storage.RuntimeMedia.Bucket, DestinationRegion: "us-lax", Prefix: "environments/stack"}
	if err := writeStorageState(storageCutoverMigrationPath(f.cfg, "media"), migration); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(storageCutoverMigrationPath(f.cfg, "media"))
	if err != nil {
		t.Fatal(err)
	}
	f.journal = storageCutoverJournal{Environment: "dev", Purpose: "media", ID: "cutover-1", Status: "complete", SourceFile: sourceFile, SourceAccessSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("old-media-key"))), MigrationSHA256: fmt.Sprintf("%x", sha256.Sum256(body)), Mutations: []storageCutoverMutation{{Kind: "Deployment", Namespace: "stack-video-cloud", Name: "video-cloud-api"}}}
	if err := saveStorageCutoverJournal(store, f.journal, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	f.cutover = storageRetirementCutover{Environment: "dev", Bucket: migration.Destination, Region: migration.DestinationRegion, Prefix: migration.Prefix, CutoverID: f.journal.ID, CutoverAt: now.Add(-48 * time.Hour), MigrationSHA256: f.journal.MigrationSHA256}
	if err := writeStorageState(filepath.Join(f.cfg.RuntimeRoot, "state", "storage-cutover.json"), f.cutover); err != nil {
		t.Fatal(err)
	}
	unused, complete := false, true
	f.evidence = storageRetirementEvidence{Environment: "dev", CutoverID: f.journal.ID, KeyID: 101, SourceBucket: migration.Source, SourceRegion: migration.SourceRegion, DestinationBucket: migration.Destination, DestinationRegion: migration.DestinationRegion, Scope: "entire-bucket", GenericKeyInUse: &unused, InventoryComplete: &complete, ObservationStart: f.cutover.CutoverAt, ObservationEnd: now.Add(-time.Hour), URLExpiry: now.Add(-2 * time.Hour), ObservedAt: now.Add(-time.Minute), Consumers: []storageRetirementConsumer{{Name: "stack-video-cloud/Deployment/video-cloud-api", KeyInUse: &unused, ObservedAt: now.Add(-time.Minute)}}}
	if err := json.Unmarshal([]byte(`{"id":101,"access_key":"old-media-key","bucket_access":[{"bucket_name":"old-media","region":"us-sea","permissions":"read_only"}]}`), &f.key); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRetireStorageKeyRequiresExactCompletedSourceAndCurrentConsumers(t *testing.T) {
	tests := []struct {
		name string
		edit func(*testing.T, *storageRetirementFixture)
		want bool
	}{
		{name: "verified old source key", want: true},
		{name: "unrelated key", edit: func(_ *testing.T, f *storageRetirementFixture) { f.key.AccessKey = "unrelated" }},
		{name: "wrong key id", edit: func(_ *testing.T, f *storageRetirementFixture) { f.key.ID = 102 }},
		{name: "source key changed since cutover", edit: func(t *testing.T, f *storageRetirementFixture) {
			body, err := os.ReadFile(f.journal.SourceFile)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f.journal.SourceFile, []byte(strings.ReplaceAll(string(body), "old-media-key", "other-key")), 0o600); err != nil {
				t.Fatal(err)
			}
			f.key.AccessKey = "other-key"
		}},
		{name: "unrestricted key", edit: func(_ *testing.T, f *storageRetirementFixture) { f.key.BucketAccess = nil }},
		{name: "multiple bucket grants", edit: func(_ *testing.T, f *storageRetirementFixture) {
			f.key.BucketAccess = append(f.key.BucketAccess, f.key.BucketAccess[0])
		}},
		{name: "wrong bucket grant", edit: func(_ *testing.T, f *storageRetirementFixture) { f.key.BucketAccess[0].BucketName = "other" }},
		{name: "wrong region grant", edit: func(_ *testing.T, f *storageRetirementFixture) { f.key.BucketAccess[0].Region = "us-lax" }},
		{name: "selected active key", edit: func(_ *testing.T, f *storageRetirementFixture) {
			f.values["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"] = "old-media-key"
		}},
		{name: "operator active artifact key", edit: func(t *testing.T, f *storageRetirementFixture) {
			if err := f.store.write("operator/env/LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID", []byte("old-media-key"), true); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "artifact source bucket", edit: func(_ *testing.T, f *storageRetirementFixture) {
			f.cfg.Storage.ReleaseArtifacts = deploymentStorageTarget{Bucket: "old-media", Region: "us-sea"}
		}},
		{name: "OTA source bucket", edit: func(_ *testing.T, f *storageRetirementFixture) {
			f.cfg.Storage.OTAFirmware = deploymentStorageTarget{Bucket: "old-media", Region: "us-sea"}
		}},
		{name: "retained backup source", edit: func(_ *testing.T, f *storageRetirementFixture) { f.values["RTK_BACKUP_BUCKET"] = "old-media" }},
		{name: "wrong environment", edit: func(_ *testing.T, f *storageRetirementFixture) { f.evidence.Environment = "staging" }},
		{name: "wrong cutover", edit: func(_ *testing.T, f *storageRetirementFixture) { f.evidence.CutoverID = "other" }},
		{name: "wrong evidence key", edit: func(_ *testing.T, f *storageRetirementFixture) { f.evidence.KeyID = 999 }},
		{name: "partial namespace inventory", edit: func(_ *testing.T, f *storageRetirementFixture) { f.evidence.Scope = "clips/" }},
		{name: "missing explicit unused", edit: func(_ *testing.T, f *storageRetirementFixture) { f.evidence.GenericKeyInUse = nil }},
		{name: "consumer still using key", edit: func(_ *testing.T, f *storageRetirementFixture) {
			used := true
			f.evidence.Consumers[0].KeyInUse = &used
		}},
		{name: "missing changed workload", edit: func(_ *testing.T, f *storageRetirementFixture) { f.evidence.Consumers[0].Name = "other-workload" }},
		{name: "stale evidence", edit: func(_ *testing.T, f *storageRetirementFixture) {
			f.evidence.ObservedAt = time.Now().Add(-25 * time.Hour)
		}},
		{name: "observation not elapsed", edit: func(_ *testing.T, f *storageRetirementFixture) { f.evidence.ObservationEnd = time.Now().Add(time.Hour) }},
		{name: "issued URLs not expired", edit: func(_ *testing.T, f *storageRetirementFixture) { f.evidence.URLExpiry = time.Now().Add(time.Hour) }},
		{name: "observation before cutover", edit: func(_ *testing.T, f *storageRetirementFixture) {
			f.evidence.ObservationStart = f.cutover.CutoverAt.Add(-time.Second)
		}},
		{name: "rolled back journal", edit: func(t *testing.T, f *storageRetirementFixture) {
			f.journal.Status = "rolled-back"
			if err := saveStorageCutoverJournal(f.store, f.journal, true); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "altered migration", edit: func(t *testing.T, f *storageRetirementFixture) {
			if err := os.WriteFile(storageCutoverMigrationPath(f.cfg, "media"), []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unbound completed receipt", edit: func(t *testing.T, f *storageRetirementFixture) {
			f.cutover.CutoverID = "other"
			if err := writeStorageState(filepath.Join(f.cfg.RuntimeRoot, "state", "storage-cutover.json"), f.cutover); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := makeStorageRetirementFixture(t)
			if tc.edit != nil {
				tc.edit(t, &f)
			}
			if err := writeStorageState(filepath.Join(f.cfg.RuntimeRoot, "state", "storage-consumers.json"), f.evidence); err != nil {
				t.Fatal(err)
			}
			deletes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/object-storage/keys/101" {
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				if r.Method == http.MethodGet {
					_ = json.NewEncoder(w).Encode(f.key)
					return
				}
				if r.Method == http.MethodDelete {
					deletes++
					_, _ = w.Write([]byte(`{}`))
					return
				}
				t.Errorf("unexpected method %s", r.Method)
			}))
			defer server.Close()
			err := (deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL}).retireStorageKey(f.cfg, f.values, 101)
			if tc.want && (err != nil || deletes != 1) {
				t.Fatalf("verified retirement: deletes=%d, error=%v", deletes, err)
			}
			if !tc.want && (err == nil || deletes != 0) {
				t.Fatalf("unsafe retirement: deletes=%d, error=%v", deletes, err)
			}
		})
	}
}

func TestStorageRetirementRejectsFalseSubstringAndTrailingJSON(t *testing.T) {
	for _, body := range []string{
		`{"other":{"generic_key_in_use": false}}`,
		`{"generic_key_in_use": false} {"generic_key_in_use": true}`,
		`{"generic_key_in_use": "false"}`,
	} {
		path := filepath.Join(t.TempDir(), "evidence.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readStorageRetirementEvidence(path); err == nil {
			t.Fatalf("accepted invalid evidence %s", strings.TrimSpace(body))
		}
	}
}
