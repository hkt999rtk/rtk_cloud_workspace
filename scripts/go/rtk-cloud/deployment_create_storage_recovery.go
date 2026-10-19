package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func validateProviderStorageRecoveryJournal(j environmentCreateJournal, p environmentCreatePlan, cfg deploymentConfig, originalAdmission time.Time) (map[string]environmentCreateProviderReceipt, string, error) {
	if j.OriginalJournal == "" || len(j.Entries) < 8 || len(j.Entries) > 9 || len(j.KubernetesLeases) != 0 || !j.CompletedAt.IsZero() {
		return nil, "", createProviderRecoveryBlock("storage recovery requires only verified completed canaries before any bootstrap")
	}
	copy := j
	copy.OriginalJournal, copy.OriginalJournalSHA256, copy.ProviderMaterialPlanSHA256 = "", "", ""
	copy.AdmittedAt = originalAdmission
	copy.Entries = append([]environmentCreateJournalEntry(nil), j.Entries[:7]...)
	receipts, kubeSHA, err := validateProviderOnlyCreateJournal(copy, p)
	if err != nil {
		return nil, "", err
	}
	seen := map[string]bool{}
	for _, entry := range j.Entries[7:] {
		var target deploymentStorageTarget
		for _, candidate := range []deploymentStorageTarget{cfg.Storage.RuntimeMedia, cfg.Storage.OTAFirmware} {
			if entry.Key == "storage-canary-"+candidate.Purpose {
				target = candidate
			}
		}
		object := strings.Trim(target.Prefix, "/") + "/__rtk_cloud_create_validation__/" + j.PlanSHA256 + "/" + target.Purpose
		bodySHA := environmentPreflightBytesSHA256([]byte("rtk-cloud-create-storage-canary"))
		intent, _ := json.Marshal(deploymentplan.Object{"bucket": target.Bucket, "region": target.Region, "object": object, "body_sha256": bodySHA})
		intentSHA, _ := environmentCreatePlanSHA256(intent)
		var proof struct {
			Bucket  string `json:"bucket"`
			Region  string `json:"region"`
			Object  string `json:"object"`
			BodySHA string `json:"body_sha256"`
			Deleted int    `json:"deleted_verified_http"`
		}
		if target.Purpose == "" || seen[entry.Key] || entry.Phase != "storage" || entry.State != "completed" || entry.StartedAt.Before(j.AdmittedAt) || entry.CompletedAt.Before(entry.StartedAt) || entry.IntentSHA256 != intentSHA || decodeEnvironmentJSON(entry.Receipt, &proof) != nil || proof.Bucket != target.Bucket || proof.Region != target.Region || proof.Object != object || proof.BodySHA != bodySHA || proof.Deleted != 404 {
			return nil, "", createProviderRecoveryBlock("storage canary is uncertain, foreign or lacks original verified cleanup evidence")
		}
		seen[entry.Key] = true
	}
	return receipts, kubeSHA, nil
}

// Read-only live proof supplements the original native DELETE/404 receipt. It
// never deletes an unverified object or marks an uncertain canary completed.
func verifyProviderRecoveryCanaryAbsence(ctx context.Context, cfg deploymentConfig, j environmentCreateJournal, operator map[string]string) error {
	return verifyProviderRecoveryCanaryAbsenceWithChecker(ctx, cfg, j, operator, defaultDeploymentCredentialChecker())
}

func verifyProviderRecoveryCanaryAbsenceWithChecker(ctx context.Context, cfg deploymentConfig, j environmentCreateJournal, operator map[string]string, checker deploymentCredentialChecker) error {
	checker = checker.withCheckContext(ctx)
	for _, entry := range j.Entries[7:] {
		var proof struct {
			Bucket  string `json:"bucket"`
			Region  string `json:"region"`
			Object  string `json:"object"`
			BodySHA string `json:"body_sha256"`
			Deleted int    `json:"deleted_verified_http"`
		}
		if decodeEnvironmentJSON(entry.Receipt, &proof) != nil {
			return createProviderRecoveryBlock("canary cleanup receipt unavailable")
		}
		var target deploymentStorageTarget
		for _, candidate := range []deploymentStorageTarget{cfg.Storage.RuntimeMedia, cfg.Storage.OTAFirmware} {
			if entry.Key == "storage-canary-"+candidate.Purpose {
				target = candidate
			}
		}
		if target.Purpose == "" {
			return createProviderRecoveryBlock("unknown canary receipt")
		}
		bucket, err := checker.resolveStorageBucket(operator["LINODE_TOKEN"], target)
		if err != nil {
			return err
		}
		endpoint, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint)
		if err != nil {
			return err
		}
		access, secret := environmentCreateStorageCredentials(operator, target.Purpose)
		_, err = provisionSignedObjectRequestWithClient(checker.client, provisionObjectStore{bucket: target.Bucket, region: target.Region, endpoint: endpoint, accessKey: access, secretKey: secret}, http.MethodGet, proof.Object, nil, nil)
		var absent *provisionObjectStorageHTTPError
		if !errors.As(err, &absent) || absent.StatusCode != http.StatusNotFound {
			return createProviderRecoveryBlock("the original completed canary no longer has verified absence")
		}
	}
	return ctx.Err()
}
