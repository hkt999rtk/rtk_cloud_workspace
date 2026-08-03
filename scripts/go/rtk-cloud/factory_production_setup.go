package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type factoryProductionCredential struct {
	JWT                 string
	BrandCloudID        string
	DeviceItemProfileID string
	ProductionRunID     string
}

type factoryProductionPreparer func(string, string, string, string, int, time.Time) (factoryProductionCredential, error)

func useProvidedFactoryProductionCredential(logsDir, runID, productionJWT string) ([]string, e2eStep, error) {
	step := e2eStep{Name: "prepare_factory_production", Status: "PASS", ExitCode: 0, LogFile: filepath.Join(logsDir, "prepare_factory_production.log")}
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(productionJWT) == "" {
		step.Status = "FAIL"
		step.ExitCode = 1
		return nil, step, errors.New("caller-provided factory production credential requires a run ID and non-empty JWT")
	}
	if err := os.WriteFile(step.LogFile, []byte("factory_production_run=PASS\nproduction_jwt_source=caller_issued\n"), 0o600); err != nil {
		return nil, step, err
	}
	return []string{
		"FACTORY_ENROLL_PRODUCTION_JWT=" + strings.TrimSpace(productionJWT),
		"FACTORY_ENROLL_RUN_ID=" + runID,
		"FACTORY_ENROLL_BATCH_ID=" + firstNonEmpty(os.Getenv("FACTORY_ENROLL_BATCH_ID"), runID),
	}, step, nil
}

func prepareFactoryProductionStep(workspace, envRoot, outDir, logsDir, brandname, runID string, quantity int, started time.Time, prepare factoryProductionPreparer) ([]string, e2eStep, error) {
	step := e2eStep{Name: "prepare_factory_production", Status: "PASS", ExitCode: 0, LogFile: filepath.Join(logsDir, "prepare_factory_production.log")}
	credential, err := prepare(workspace, envRoot, brandname, runID, quantity, started)
	step.DurationSeconds = int64(time.Since(started).Seconds())
	if err != nil {
		step.Status = "FAIL"
		step.ExitCode = 1
		return nil, step, err
	}
	if err := writeFactoryProductionSetupEvidence(filepath.Join(outDir, "factory-production.json"), runID, brandname, credential); err != nil {
		return nil, step, err
	}
	logBody := fmt.Sprintf("factory_production_run=PASS\nbrand_cloud_id=%s\ndevice_item_profile_id=%s\nproduction_run_id=%s\nallowed_quantity=%d\n", credential.BrandCloudID, credential.DeviceItemProfileID, credential.ProductionRunID, quantity)
	if err := os.WriteFile(step.LogFile, []byte(logBody), 0o600); err != nil {
		return nil, step, err
	}
	return []string{
		"FACTORY_ENROLL_PRODUCTION_JWT=" + credential.JWT,
		"FACTORY_ENROLL_RUN_ID=" + runID,
		"FACTORY_ENROLL_BATCH_ID=" + runID,
	}, step, nil
}

func prepareFactoryProductionCredential(workspace, envRoot, brandname, runID string, quantity int, now time.Time) (factoryProductionCredential, error) {
	if strings.TrimSpace(runID) == "" || quantity <= 0 {
		return factoryProductionCredential{}, errors.New("factory production run requires a run ID and positive quantity")
	}
	ctx, err := factoryProductionAccountContext(workspace, envRoot)
	if err != nil {
		return factoryProductionCredential{}, err
	}
	defer ctx.Close()
	token, err := accountLogin(ctx, func(string, ...any) {})
	if err != nil {
		return factoryProductionCredential{}, fmt.Errorf("factory production platform-admin login: %w", err)
	}
	brand, err := accountFindBrandCloud(ctx, token, brandname)
	if err != nil {
		return factoryProductionCredential{}, err
	}
	brandID := stringValue(brand["id"])
	if brandID == "" {
		return factoryProductionCredential{}, errors.New("factory production brand cloud has no ID")
	}
	profileDigest := sha256.Sum256([]byte(brandID + "\x00" + runID))
	profileKey := "e2e-" + fmt.Sprintf("%x", profileDigest[:10])
	profileID, err := ensureFactoryProductionProfile(ctx, token, brandID, profileKey, runID)
	if err != nil {
		return factoryProductionCredential{}, err
	}
	productionRunID, jwt, err := createFactoryProductionRun(ctx, token, brandID, profileID, runID, quantity, now)
	if err != nil {
		return factoryProductionCredential{}, err
	}
	return factoryProductionCredential{JWT: jwt, BrandCloudID: brandID, DeviceItemProfileID: profileID, ProductionRunID: productionRunID}, nil
}

func factoryProductionAccountContext(workspace, envRoot string) (accountManagerContext, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("ACCOUNT_MANAGER_BASE_URL")), "/")
	platformEnv := filepath.Join(envRoot, "services", "account-manager", "account-manager-platform-admin.env")
	adminEmail := firstNonEmpty(os.Getenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"), envFileValue(platformEnv, "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"))
	adminPassword := firstNonEmpty(os.Getenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"), envFileValue(platformEnv, "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"))
	if baseURL != "" && adminEmail != "" && adminPassword != "" {
		return accountManagerContext{EnvRoot: envRoot, BaseURL: baseURL, AdminEmail: adminEmail, AdminPassword: adminPassword, PlatformAdminEnv: platformEnv}, nil
	}
	return accountManagerContextFromFlags(workspace, envRoot)
}

func ensureFactoryProductionProfile(ctx accountManagerContext, token, brandID, profileKey, runID string) (string, error) {
	endpoint := fmt.Sprintf("%s/v1/admin/brand-clouds/%s/device-item-profiles", ctx.BaseURL, url.PathEscape(brandID))
	body, status, err := curlJSONStatus(endpoint+"?limit=200", token, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("list factory production profiles failed: HTTP %d%s", status, accountAPIErrorSuffix(body))
	}
	var listed struct {
		Profiles []struct {
			ID         string `json:"id"`
			ProfileKey string `json:"profile_key"`
			Status     string `json:"status"`
		} `json:"device_item_profiles"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		return "", err
	}
	for _, profile := range listed.Profiles {
		if profile.ProfileKey == profileKey {
			if profile.ID == "" || profile.Status != "active" {
				return "", fmt.Errorf("factory production profile %s is not active", profileKey)
			}
			return profile.ID, nil
		}
	}
	payload, err := json.Marshal(map[string]any{
		"profile_key": profileKey, "display_name": "Runtime factory " + runID,
		"category": "ip_camera", "ca_profile": "factory-device", "issuer_profile": "runtime-e2e",
		"service_options":   []string{"mqtt", "video_streaming", "video_storage"},
		"metadata_defaults": map[string]string{"e2e_run_id": runID},
	})
	if err != nil {
		return "", err
	}
	body, status, err = curlJSONStatus(endpoint, token, payload)
	if err != nil {
		return "", err
	}
	if status != http.StatusCreated {
		return "", fmt.Errorf("create factory production profile failed: HTTP %d%s", status, accountAPIErrorSuffix(body))
	}
	var created struct {
		Profile struct {
			ID string `json:"id"`
		} `json:"device_item_profile"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		return "", err
	}
	if created.Profile.ID == "" {
		return "", errors.New("create factory production profile returned no ID")
	}
	return created.Profile.ID, nil
}

func createFactoryProductionRun(ctx accountManagerContext, token, brandID, profileID, runID string, quantity int, now time.Time) (string, string, error) {
	payload, err := json.Marshal(map[string]any{
		"factory_id": "runtime-e2e", "batch_id": runID, "allowed_quantity": quantity,
		"valid_from":  now.UTC().Add(-time.Minute).Format(time.RFC3339),
		"valid_until": now.UTC().Add(2 * time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		return "", "", err
	}
	endpoint := fmt.Sprintf("%s/v1/admin/brand-clouds/%s/device-item-profiles/%s/production-runs", ctx.BaseURL, url.PathEscape(brandID), url.PathEscape(profileID))
	body, status, err := curlJSONStatus(endpoint, token, payload)
	if err != nil {
		return "", "", err
	}
	if status != http.StatusCreated {
		return "", "", fmt.Errorf("create factory production run failed: HTTP %d%s", status, accountAPIErrorSuffix(body))
	}
	var created struct {
		ProductionRun struct {
			ID string `json:"id"`
		} `json:"production_run"`
		JWT string `json:"factory_jwt"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		return "", "", err
	}
	if created.ProductionRun.ID == "" || strings.TrimSpace(created.JWT) == "" {
		return "", "", errors.New("create factory production run returned incomplete credential data")
	}
	return created.ProductionRun.ID, created.JWT, nil
}

func writeFactoryProductionSetupEvidence(path, runID, brandname string, credential factoryProductionCredential) error {
	return writeJSON(path, map[string]any{
		"schema": "rtk-factory-production-setup/v1", "status": "PASS", "run_id": runID,
		"brandname": brandname, "brand_cloud_id": credential.BrandCloudID,
		"device_item_profile_id": credential.DeviceItemProfileID, "production_run_id": credential.ProductionRunID,
		"production_jwt_issued": true,
	})
}
