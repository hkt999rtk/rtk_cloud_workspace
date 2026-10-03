package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDestroyRejectsRegisteredRetainedBucketNames(t *testing.T) {
	for _, bucket := range []string{"rtk-cloud-staging-backup-us-sea", "rtk-cloud-staging-billing-backup-us-sea", "rtk-cloud-staging-pki-backup-us-sea", "rtk-cloud-staging-reports-us-sea", "rtk-cloud-shared-reports-us-sea"} {
		if err := validateDestroyRuntimeBucket(map[string]string{"CLOUD_ENV_NAME": "staging"}, bucket, "us-sea"); err == nil {
			t.Errorf("allowed retained bucket misbound as runtime: %s", bucket)
		}
	}
}

func TestDestroyRejectsConfiguredBillingBackupRegardlessOfRegionAlias(t *testing.T) {
	env := map[string]string{"CLOUD_ENV_NAME": "staging", "LKE_BILLING_BACKUP_BUCKET": "legacy-billing-evidence", "LKE_BILLING_BACKUP_REGION": "us-sea"}
	for _, region := range []string{"", "us-sea", "us-east-1"} {
		if err := validateDestroyRuntimeBucket(env, "legacy-billing-evidence", region); err == nil || !strings.Contains(err.Error(), "LKE_BILLING_BACKUP_BUCKET") {
			t.Fatalf("configured billing backup not protected in region %q: %v", region, err)
		}
	}
	if err := validateDestroyRuntimeBucket(env, "rtk-cloud-staging-runtime-us-sea", "us-sea"); err != nil {
		t.Fatalf("unrelated runtime bucket rejected: %v", err)
	}
}

func TestDestroyReadsRetainedBackupJSONDestinations(t *testing.T) {
	for _, name := range []string{"backup.json", "postgres-backup.json"} {
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			root := filepath.Join(workspace, "cloud_env", "staging")
			destination := `"remote":{"bucket":"legacy-private-backup","region":"us-sea"}`
			if name == "postgres-backup.json" {
				destination = `"worker":{` + destination + `}`
			}
			writeTestFile(t, filepath.Join(root, name), `{"environment":"staging","stack":"video-cloud-staging",`+destination+`}`)
			env := map[string]string{"CLOUD_ENV_NAME": "staging", "CLOUD_STACK_NAME": "video-cloud-staging", "VIDEO_CLOUD_BLOB_BUCKET": "legacy-private-backup", "VIDEO_CLOUD_BLOB_REGION": "us-sea"}
			if err := validateDestroyBackupConfigurations(workspace, filepath.Join(root, "runtime"), env); err == nil || !strings.Contains(err.Error(), "overlaps retained backup") {
				t.Fatalf("retained backup not protected: %v", err)
			}
			env["VIDEO_CLOUD_BLOB_REGION"] = "us-east-1"
			if err := validateDestroyBackupConfigurations(workspace, filepath.Join(root, "runtime"), env); err == nil {
				t.Fatal("signing region mismatch allowed destruction of retained bucket")
			}
			env["VIDEO_CLOUD_BLOB_BUCKET"] = "runtime-bucket"
			if err := validateDestroyBackupConfigurations(workspace, filepath.Join(root, "runtime"), env); err != nil {
				t.Fatalf("different bucket name rejected: %v", err)
			}
		})
	}
}

func TestDestroyBackupInspectionFailsClosed(t *testing.T) {
	for _, body := range []string{
		`not json`,
		`{"environment":"prod","stack":"video-cloud-prod","remote":{"bucket":"backup","region":"us-sea"}}`,
		`{"environment":"staging","stack":"video-cloud-staging","remote":{"bucket":"backup"}}`,
	} {
		workspace := t.TempDir()
		root := filepath.Join(workspace, "cloud_env", "staging")
		writeTestFile(t, filepath.Join(root, "backup.json"), body)
		env := map[string]string{"CLOUD_ENV_NAME": "staging", "CLOUD_STACK_NAME": "video-cloud-staging"}
		if err := validateDestroyBackupConfigurations(workspace, filepath.Join(root, "runtime"), env); err == nil {
			t.Fatal("unresolved backup identity permitted destruction")
		}
	}
}
